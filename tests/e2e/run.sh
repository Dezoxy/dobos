#!/usr/bin/env bash
# End-to-end stream test: a local seeder seeds a private torrent, the real Flodni
# image adds it through the library API, and the file is read back through FUSE
# and compared by sha256. Runs two Flodni configs in sequence (the default one
# has no source or mount path in config.json), then a TV
# replacement scenario (a second release replaces the first, a third that never
# delivers data is refused).
#
#   bash tests/e2e/run.sh [flodni-image] [seeder-image]
#
# Test hooks: E2E_CORRUPT_EXPECTED=1 flips the expected hash (the run must fail);
# E2E_KILL_SEEDER_AFTER_ADD=1 kills the seeder once the add is done (the read must
# fail within its timeout instead of hanging); E2E_REPLACEMENT_EXPECT_SUCCESS_FOR_C=1
# expects the undeliverable release to be accepted (the replacement variant must
# fail); E2E_PROM_DUMP=<file> saves the /metrics/prometheus body of each
# non-replacement variant (the last one wins; a scrape sample for the docs);
# E2E_ONLY=<variant> runs just that variant (default, slow-seeder,
# index-cache, warm-on-add, replacement). slow-seeder is default with a seeder that
# keeps the engine's 1 MiB per-peer request allowance. The index-cache variant runs
# with a random FLODNI_API_TOKEN: the seeder must see 401 without the bearer
# header before its authorised add, and the same for GoStorm's 8090 (401 on
# POST /torrents, 200 on /echo); the other variants run with auth off.
# E2E_WARM_NEGATIVE=1 runs the warm-on-add variant with the switch off: it must
# fail at step 2 (no fill). It is not part of make e2e.
set -euo pipefail

FLODNI_IMAGE=${1:-flodni:ci}
SEEDER_IMAGE=${2:-flodni-e2e-seeder:ci}
SOURCE_PATH=/mnt/flodni-real
MOUNT_PATH=/mnt/flodni-virtual
WAIT_SECONDS=180
READ_TIMEOUT=180

work=$(mktemp -d)
network=""
flodni=""
seeder=""

cleanup() {
  local rc=$?
  [ -n "$seeder" ] && docker rm -f "$seeder" >/dev/null 2>&1 || true
  [ -n "$flodni" ] && docker rm -f "$flodni" >/dev/null 2>&1 || true
  [ -n "$network" ] && docker network rm "$network" >/dev/null 2>&1 || true
  # State dirs were written by root inside the containers.
  docker run --rm -v "$work:/w" --entrypoint sh "$SEEDER_IMAGE" -c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1 || true
  rm -rf "$work" 2>/dev/null || true
  return $rc
}
trap cleanup EXIT

dump_diagnostics() {
  echo "=== diagnostics: $flodni ===" >&2
  docker logs --tail 200 "$flodni" >&2 || true
  echo "=== fuse mounts in $flodni ===" >&2
  docker exec "$flodni" sh -c 'grep fuse /proc/mounts' >&2 || true
  echo "=== diagnostics: $seeder ===" >&2
  docker logs --tail 200 "$seeder" >&2 || true
}

# diag_dir is created on first use and, unlike $work, survives the run.
diag_dir=""

# slow_diagnostics variant seconds: print and save both containers' recent logs.
slow_diagnostics() {
  local variant=$1 seconds=$2 file
  [ -n "$diag_dir" ] || diag_dir=$(mktemp -d)
  file="$diag_dir/slow-$variant-$(date +%H%M%S).log"
  echo "SLOW ($variant): full read ${seconds}s" >&2
  {
    echo "=== flodni $flodni: last 300 lines ==="
    docker logs --tail 300 "$flodni" 2>&1 || true
    echo "=== seeder $seeder: last 300 lines ==="
    docker logs --tail 300 "$seeder" 2>&1 || true
  } >"$file"
  cat "$file" >&2
  echo "SLOW ($variant): diagnostics saved to $file" >&2
}

# report_first_byte variant: the first_byte_ms of the stub's sessions, read from
# Flodni's /api/sessions on its metrics port (no curl in the image, so bash's /dev/tcp).
report_first_byte() {
  local variant=$1 body
  body=$(docker exec "$flodni" bash -c 'exec 3<>/dev/tcp/127.0.0.1/9080 && printf "GET /api/sessions HTTP/1.0\r\nHost: x\r\n\r\n" >&3 && cat <&3' 2>/dev/null || true)
  echo "first_byte_ms ($variant): $(grep -o '"first_byte_ms":[0-9a-z]*' <<<"$body" | tr '\n' ' ')" >&2
}

# health_body: Flodni's /api/torrents/health. The container's FLODNI_API_TOKEN, when
# set, is read inside it and sent as the bearer header (the replacement variant sets none).
health_body() {
  docker exec "$flodni" bash -c 'h=""
    if [ -n "${FLODNI_API_TOKEN:-}" ]; then h="Authorization: Bearer $FLODNI_API_TOKEN\r\n"; fi
    exec 3<>/dev/tcp/127.0.0.1/9080 && printf "GET /api/torrents/health HTTP/1.0\r\nHost: x\r\n${h}\r\n" >&3 && cat <&3' 2>/dev/null || true
}

# check_files_record: after the full read through the mount, the per-file record
# (experimental `files` array) of the fixture is printed, not asserted: the mount path
# records a file once the recorder has run, so poll up to 30 s for a non-empty array.
# The expected measurement is readability warmed and cache cached.
check_files_record() {
  local body="" deadline=$((SECONDS + 30))
  while [ "$SECONDS" -lt "$deadline" ]; do
    body=$(health_body)
    grep -q '"files":\[{' <<<"$body" && break
    sleep 2
  done
  echo "FILES (index-cache): $(grep -o '"files":\[[^]]*\]' <<<"$body" | tr '\n' ' ')" >&2
}

# prom_raw [noauth]: Flodni's /metrics/prometheus with headers, over /dev/tcp. The
# container's FLODNI_API_TOKEN is read inside it, so this script never handles the
# token; the Authorization header goes only when one is set, and not at all with noauth.
prom_raw() {
  docker exec "$flodni" bash -c 'h=""
    if [ -z "$1" ] && [ -n "${FLODNI_API_TOKEN:-}" ]; then h="Authorization: Bearer $FLODNI_API_TOKEN\r\n"; fi
    exec 3<>/dev/tcp/127.0.0.1/9080 && printf "GET /metrics/prometheus HTTP/1.0\r\nHost: x\r\n${h}\r\n" >&3 && cat <&3' _ "${1:-}" 2>/dev/null || true
}

# check_prometheus variant: the exposition must answer 200 with the text format's
# content type, every line must fit the sample grammar, the first-byte histogram must
# be declared first-class, and the full read must show in the read counts and bytes.
# Per-source values are printed as a measurement, not asserted. In index-cache (token
# set) the request without the header must get 401.
check_prometheus() {
  local variant=$1 raw body status
  raw=$(prom_raw)
  status=$(head -n 1 <<<"$raw" | tr -d '\r')
  body=${raw#*$'\r\n\r\n'}
  if [[ "$status" != *" 200 "* ]]; then
    echo "FAIL ($variant): /metrics/prometheus: $status" >&2
    dump_diagnostics
    return 1
  fi
  if ! grep -qi '^content-type: text/plain; version=0.0.4; charset=utf-8' <<<"$raw"; then
    echo "FAIL ($variant): /metrics/prometheus content type is not text format 0.0.4" >&2
    dump_diagnostics
    return 1
  fi
  local bad
  bad=$(grep -vE '^(# (HELP|TYPE) [a-zA-Z_:][a-zA-Z0-9_:]* .+|[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^{}]*\})? [-+0-9.eE]+|[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^{}]*\})? (\+Inf|NaN))$' <<<"$body" | head -n 3 || true)
  if [ -n "$bad" ]; then
    echo "FAIL ($variant): /metrics/prometheus has lines outside the sample grammar: $bad" >&2
    dump_diagnostics
    return 1
  fi
  if ! grep -qx '# TYPE flodni_session_first_byte_seconds histogram' <<<"$body"; then
    echo "FAIL ($variant): /metrics/prometheus lacks the first-byte histogram TYPE line" >&2
    dump_diagnostics
    return 1
  fi
  local reads bytes
  reads=$(awk '/^flodni_read_duration_seconds_count\{/ { n += $2 } END { print n + 0 }' <<<"$body")
  bytes=$(awk '/^flodni_read_bytes_total\{/ { n += $2 } END { print n + 0 }' <<<"$body")
  if [ "$reads" -le 0 ] || [ "$bytes" -le 0 ]; then
    echo "FAIL ($variant): after a full read the read counts are $reads and the bytes $bytes" >&2
    dump_diagnostics
    return 1
  fi
  echo "PROM ($variant): reads=$reads bytes=$bytes" >&2
  echo "PROM ($variant): $(grep -E '^flodni_(read_duration_seconds_count|read_bytes_total)\{' <<<"$body" | tr '\n' ' ')" >&2
  # Measurement only: warm files and the readability split, as the scrape sees them.
  echo "PROM ($variant): $(grep -E '^flodni_(warmup_files|warmup_cache_bytes|readability_torrents)[{ ]' <<<"$body" | tr '\n' ' ')" >&2
  if [ "$variant" = index-cache ]; then
    status=$(prom_raw noauth | head -n 1 | tr -d '\r')
    if [[ "$status" != *" 401 "* ]]; then
      echo "FAIL ($variant): /metrics/prometheus without the token: $status, want 401" >&2
      dump_diagnostics
      return 1
    fi
    echo "ok ($variant): /metrics/prometheus without token refused with 401" >&2
  fi
  if [ -n "${E2E_PROM_DUMP:-}" ]; then printf '%s\n' "$body" >"$E2E_PROM_DUMP"; fi
}

# check_reader_budget variant path: the full sha256sum read is a reader no webhook
# confirmed, so past the windows it must show in flodni_unconfirmed_read_bytes_total
# (printed in every variant, with the session and webhook series and the log line,
# whose swarm_bytes/cache_bytes split is a measurement). In
# index-cache (4 MB windows, 150 MiB fixture, default 64 MiB margin) the bytes must sum
# to at least 64 MiB and exactly one over_margin [ReaderBudget] line must exist,
# without the file's name, with a caller pid. The other variants only print.
check_reader_budget() {
  local variant=$1 path=$2 body logs lines
  body=$(prom_raw)
  body=${body#*$'\r\n\r\n'}
  echo "PROM ($variant): $(grep -E '^flodni_(unconfirmed_read_bytes_total|unconfirmed_read_sessions_total|playback_webhooks_received_total)[{ ]' <<<"$body" | tr '\n' ' ')" >&2
  logs=$(docker logs "$flodni" 2>&1 || true)
  lines=$(grep -F '[ReaderBudget] ' <<<"$logs" || true)
  echo "ReaderBudget ($variant): ${lines:-none}" >&2
  [ "$variant" = index-cache ] || return 0
  local total
  total=$(awk '/^flodni_unconfirmed_read_bytes_total / { printf "%d", $2 }' <<<"$body")
  total=${total:-0}
  if [ "$total" -lt $((64 * 1048576)) ]; then
    echo "FAIL ($variant): flodni_unconfirmed_read_bytes_total sums to $total, want at least 64 MiB" >&2
    dump_diagnostics
    return 1
  fi
  if [ "$(grep -c '"event":"over_margin"' <<<"$lines")" != 1 ]; then
    echo "FAIL ($variant): want exactly one over_margin [ReaderBudget] line" >&2
    dump_diagnostics
    return 1
  fi
  if grep -qF "$(basename "$path")" <<<"$lines" || grep -qF "$(basename "$path" .mkv)" <<<"$lines"; then
    echo "FAIL ($variant): the [ReaderBudget] line carries the file name" >&2
    dump_diagnostics
    return 1
  fi
  if ! grep -Eq '"pid":[1-9]' <<<"$lines"; then
    echo "FAIL ($variant): the [ReaderBudget] line has no caller pid" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($variant): $total unconfirmed bytes counted, one [ReaderBudget] line, no file name" >&2
}

# check_sessions_closed: after B's session closed, the counter of served sessions must be
# at least 1 (check_session_line has just found the line, so this is deterministic).
check_sessions_closed() {
  local body n
  body=$(prom_raw)
  n=$(grep -E '^flodni_sessions_closed_total\{outcome="served"\} ' <<<"$body" | awk '{ print $2 }')
  if [ -z "$n" ] || [ "$n" -lt 1 ]; then
    echo "FAIL (replacement): flodni_sessions_closed_total{outcome=\"served\"} is '${n:-absent}', want >= 1" >&2
    dump_diagnostics
    return 1
  fi
  echo "PROM (replacement): sessions_closed served=$n" >&2
}

# check_readability_gauge: the gauge refreshes every 60 s, so poll up to 70 s
# for the library to show in it (inode map count and ticker, end to end).
check_readability_gauge() {
  local body="" sum=0 deadline=$((SECONDS + 70))
  while [ "$SECONDS" -lt "$deadline" ]; do
    body=$(prom_raw)
    sum=$(awk '/^flodni_readability_torrents\{/ { n += $2 } END { print n + 0 }' <<<"$body")
    [ "$sum" -ge 1 ] && break
    sleep 5
  done
  echo "PROM (replacement): $(grep -E '^flodni_readability_torrents\{' <<<"$body" | tr '\n' ' ')" >&2
  if [ "$sum" -lt 1 ]; then
    echo "FAIL (replacement): flodni_readability_torrents still sums to 0 after 70 s" >&2
    dump_diagnostics
    return 1
  fi
}

# check_readability out: B's session closes up to two minutes after its last read, so
# poll up to 130 s. The endpoint must answer with a record list and C must have no
# record. B stamped is reported, not asserted: sessions fed by the read-ahead pump
# may not stamp (README), so B not stamped is the measurement, not a failure.
check_readability() {
  local out=$1 a_hash b_hash c_hash body="" deadline=$((SECONDS + 130))
  a_hash=$(cat "$out/a.hash") b_hash=$(cat "$out/b.hash") c_hash=$(cat "$out/c.hash")
  while [ "$SECONDS" -lt "$deadline" ]; do
    body=$(health_body)
    grep -q "\"hash\":\"$b_hash\"[^}]*\"last_swarm_read_at\":\"" <<<"$body" && break
    sleep 5
  done
  if ! grep -q '"torrents":' <<<"$body"; then
    echo "FAIL (replacement): /api/torrents/health returned no record list: $body" >&2
    dump_diagnostics
    return 1
  fi
  if ! grep -q "\"hash\":\"$b_hash\"[^}]*\"last_swarm_read_at\":\"" <<<"$body"; then
    echo "READABILITY (replacement): B not stamped" >&2
    echo "READABILITY (replacement): endpoint: $body" >&2
    return 0
  fi
  echo "READABILITY (replacement): B stamped: $(grep -o "{\"hash\":\"$b_hash\"[^}]*}" <<<"$body")" >&2
  # C never read, so it must have no record. A was read before B replaced it and its
  # session can close after the library dropped the row, which leaves an orphan until
  # the next restart's prune: reported, not judged.
  if grep -q "\"hash\":\"$c_hash\"" <<<"$body"; then
    echo "FAIL (replacement): readability record of C ($c_hash) exists: $body" >&2
    dump_diagnostics
    return 1
  fi
  if grep -q "\"hash\":\"$a_hash\"" <<<"$body"; then
    echo "READABILITY (replacement): A has a record after replacement: $(grep -o "{\"hash\":\"$a_hash\"[^}]*}" <<<"$body")" >&2
  else
    echo "READABILITY (replacement): A has no record" >&2
  fi
}

# check_session_line out: after B's session closed, its [Session] log line must exist
# with outcome served, a file index and a byte split that sums to bytes_served. The
# split is printed as a measurement; swarm_served false is reported, not failed
# (a pump-fed session may not set it, README).
check_session_line() {
  local out=$1 b_hash logs="" line="" deadline=$((SECONDS + 3))
  b_hash=$(cat "$out/b.hash")
  # Read the log once per try: grep -q on a live docker logs pipe fails under pipefail.
  while :; do
    logs=$(docker logs "$flodni" 2>&1 || true)
    line=$(grep -F '[Session] ' <<<"$logs" | grep -F "\"hash\":\"$b_hash\"" | grep -F '"outcome":"served"' | tail -n 1 || true)
    [ -n "$line" ] || [ "$SECONDS" -ge "$deadline" ] && break
    sleep 1
  done
  if [ -z "$line" ]; then
    echo "FAIL (replacement): no [Session] line with outcome served for B ($b_hash)" >&2
    dump_diagnostics
    return 1
  fi
  local json=${line#*\[Session\] } idx served sum f h t r
  idx=$(grep -o '"file_index":[0-9]*' <<<"$json" | cut -d: -f2)
  served=$(grep -o '"bytes_served":[0-9]*' <<<"$json" | cut -d: -f2)
  f=$(grep -o '"bytes_fetch_block":[0-9]*' <<<"$json" | cut -d: -f2)
  r=$(grep -o '"bytes_racache_hit":[0-9]*' <<<"$json" | cut -d: -f2)
  h=$(grep -o '"bytes_warmup_head":[0-9]*' <<<"$json" | cut -d: -f2)
  t=$(grep -o '"bytes_warmup_tail":[0-9]*' <<<"$json" | cut -d: -f2)
  if [ -z "$idx" ] || [ "$idx" -lt 1 ] || [ -z "$served" ] || [ -z "$f" ] || [ -z "$r" ] || [ -z "$h" ] || [ -z "$t" ]; then
    echo "FAIL (replacement): [Session] line of B lacks file_index >= 1 or the byte fields: $json" >&2
    dump_diagnostics
    return 1
  fi
  sum=$((f + r + h + t))
  if [ "$sum" -ne "$served" ]; then
    echo "FAIL (replacement): [Session] byte fields sum to $sum, bytes_served is $served: $json" >&2
    dump_diagnostics
    return 1
  fi
  echo "SESSION (replacement): B file_index=$idx bytes_served=$served fetch_block=$f racache_hit=$r warmup_head=$h warmup_tail=$t" >&2
  echo "SESSION (replacement): B line: $json" >&2
  if grep -q '"swarm_served":true' <<<"$json"; then
    echo "SESSION (replacement): B swarm_served true" >&2
  else
    echo "SESSION (replacement): B swarm_served false" >&2
  fi
}

is_running() {
  [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]
}

# catchup_config warm_on_add file: the catch-up variant's config. Written with `cat >` so
# a rewrite keeps the inode (the container bind-mounts the single file).
catchup_config() {
  cat >"$2" <<JSON
{"telemetry": false, "telemetry_url": "", "scheduler": {"enabled": false},
 "physical_source_path": "$SOURCE_PATH", "fuse_mount_path": "$MOUNT_PATH",
 "pump_on_open": false, "warmup_dir": "/cache/index",
 "warmup_head_size_mb": 16, "warmup_tail_size_mb": 8, "disk_warmup_quota_gb": 1,
 "warm_on_add": $1, "warm_catchup_per_hour": 3600}
JSON
}

write_config() { # variant file
  local variant=$1 file=$2
  case "$variant" in
    default)
      # No physical_source_path or fuse_mount_path: the entrypoint's
      # command-line paths alone decide where stubs go and what FUSE serves.
      cat >"$file" <<JSON
{"telemetry": false, "telemetry_url": "", "scheduler": {"enabled": false}}
JSON
      ;;
    replacement)
      cat >"$file" <<JSON
{"telemetry": false, "telemetry_url": "", "scheduler": {"enabled": false},
 "physical_source_path": "$SOURCE_PATH", "fuse_mount_path": "$MOUNT_PATH"}
JSON
      ;;
    index-cache)
      cat >"$file" <<JSON
{"telemetry": false, "telemetry_url": "", "scheduler": {"enabled": false},
 "physical_source_path": "$SOURCE_PATH", "fuse_mount_path": "$MOUNT_PATH",
 "pump_on_open": false, "warmup_dir": "/cache/index",
 "warmup_head_size_mb": 4, "warmup_tail_size_mb": 4, "disk_warmup_quota_gb": 1}
JSON
      ;;
    warm-on-add)
      # The index-cache config with the warmer on and windows larger than what the
      # variant reads (16 MiB head for 8 MiB, 8 MiB tail for 4 MiB). The kernel
      # reads whole pages and reads ahead: the head read runs up to 4 MiB past its
      # end, and the tail read of the last 4 MiB starts on the page before the
      # file's size minus 4 MiB (the size is not page aligned), which a 4 MiB tail
      # window does not cover (measured: 12 KiB went through fetch_block).
      # E2E_WARM_NEGATIVE=1 switches the warmer off, so step 2 must fail.
      local warm=true
      if [ "${E2E_WARM_NEGATIVE:-}" = "1" ]; then warm=false; fi
      cat >"$file" <<JSON
{"telemetry": false, "telemetry_url": "", "scheduler": {"enabled": false},
 "physical_source_path": "$SOURCE_PATH", "fuse_mount_path": "$MOUNT_PATH",
 "pump_on_open": false, "warmup_dir": "/cache/index",
 "warmup_head_size_mb": 16, "warmup_tail_size_mb": 8, "disk_warmup_quota_gb": 1,
 "warm_on_add": $warm}
JSON
      ;;
    catch-up)
      # The warm-on-add windows with the warmer off and catch-up at its fastest pace:
      # the first pass runs 1 s after a start. The variant rewrites this file in place
      # with warm_on_add true.
      catchup_config false "$file"
      ;;
    slow-seeder) write_config default "$file" ;;
    *) echo "unknown variant $variant" >&2; return 1 ;;
  esac
}

# start_stack variant [seeder args...]: network, Flodni and seeder for one variant.
start_stack() {
  local variant=$1
  shift
  dir="$work/$variant"
  mkdir -p "$dir/state" "$dir/out" "$dir/cache"
  chmod 777 "$dir/out" "$dir/cache"
  write_config "$variant" "$dir/config.json"
  local -a envargs=()
  if [ "$variant" = index-cache ]; then
    envargs=(-e "FLODNI_API_TOKEN=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')")
  fi

  echo "=== variant: $variant ===" >&2
  network="e2e-$variant-$$"
  flodni="e2e-flodni-$variant"
  seeder="e2e-seeder-$variant"
  docker network create --internal "$network" >/dev/null

  docker run -d --name "$flodni" --network "$network" --network-alias flodni \
    --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor:unconfined \
    -v "$dir/config.json:/config.json:ro" -v "$dir/state:/state" \
    -v "$dir/cache:/cache" ${envargs[@]+"${envargs[@]}"} "$FLODNI_IMAGE" >/dev/null
  local -a seederenv=()
  if [ -n "${E2E_GOSTORM_CACHE_MB:-}" ]; then
    seederenv=(-e "E2E_GOSTORM_CACHE_MB=$E2E_GOSTORM_CACHE_MB")
  fi
  docker run -d --name "$seeder" --network "$network" --network-alias seeder \
    -v "$dir/out:/e2e" ${envargs[@]+"${envargs[@]}"} ${seederenv[@]+"${seederenv[@]}"} "$SEEDER_IMAGE" -out /e2e "$@" >/dev/null
}

stop_stack() {
  docker rm -f "$seeder" "$flodni" >/dev/null
  docker network rm "$network" >/dev/null
  seeder="" flodni="" network=""
}

# wait_for_file path seconds label: wait for a seeder output, failing fast if a
# container exits.
wait_for_file() {
  local file=$1 limit=$2 label=$3 waited=0
  until [ -s "$file" ]; do
    if ! is_running "$flodni" || ! is_running "$seeder"; then
      echo "FAIL ($label): a container exited before $(basename "$file") appeared" >&2
      dump_diagnostics
      return 1
    fi
    if [ "$waited" -ge "$limit" ]; then
      echo "FAIL ($label): no $(basename "$file") after ${limit}s" >&2
      dump_diagnostics
      return 1
    fi
    sleep 1
    waited=$((waited + 1))
  done
}

run_variant() {
  local variant=$1 dir t0=$SECONDS
  local -a seeder_args=()
  # The seeder keeps the engine's default per-peer request allowance, like a
  # real peer that is not tuned for one fast leecher.
  if [ "$variant" = slow-seeder ]; then seeder_args=(-engine-default-alloc); fi
  start_stack "$variant" ${seeder_args[@]+"${seeder_args[@]}"}

  local waited=0
  until [ -s "$dir/out/expected.sha256" ] && [ -s "$dir/out/fuse_path" ]; do
    if ! is_running "$flodni" || ! is_running "$seeder"; then
      echo "FAIL ($variant): a container exited before the add finished" >&2
      dump_diagnostics
      return 1
    fi
    if [ "$waited" -ge "$WAIT_SECONDS" ]; then
      echo "FAIL ($variant): no expected.sha256/fuse_path after ${WAIT_SECONDS}s" >&2
      dump_diagnostics
      return 1
    fi
    sleep 2
    waited=$((waited + 2))
  done

  local expected fuse_path path
  expected=$(cat "$dir/out/expected.sha256")
  fuse_path=$(cat "$dir/out/fuse_path")
  path="$MOUNT_PATH/$fuse_path"
  if [ "${E2E_CORRUPT_EXPECTED:-}" = "1" ]; then
    expected=$(printf '%s' "$expected" | tr '0-9a-f' '1-9a-f0')
    echo "E2E_CORRUPT_EXPECTED: expecting $expected" >&2
  fi
  if [ "${E2E_KILL_SEEDER_AFTER_ADD:-}" = "1" ]; then
    echo "E2E_KILL_SEEDER_AFTER_ADD: killing $seeder" >&2
    docker kill "$seeder" >/dev/null
  fi

  echo "tail read of $path" >&2
  if ! docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" \
    sh -c "size=\$(stat -c %s '$path') && dd if='$path' of=/dev/null bs=1048576 skip=\$(( (size - 1) / 1048576 - 1 )) status=none"; then
    echo "FAIL ($variant): tail read failed" >&2
    dump_diagnostics
    return 1
  fi

  echo "full read of $path" >&2
  local actual full_t0=$SECONDS
  if ! actual=$(docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" sha256sum "$path"); then
    echo "FAIL ($variant): full read failed or timed out" >&2
    dump_diagnostics
    return 1
  fi
  actual=${actual%% *}
  local full_s=$((SECONDS - full_t0))
  echo "full read of $variant took ${full_s}s" >&2
  report_first_byte "$variant"
  check_prometheus "$variant" || return 1
  check_reader_budget "$variant" "$path" || return 1
  # Only reported for now: the bound comes once the stall is fixed.
  if [ "$full_s" -gt 20 ]; then slow_diagnostics "$variant" "$full_s"; fi
  if [ "$actual" != "$expected" ]; then
    echo "FAIL ($variant): sha256 mismatch: got $actual, want $expected" >&2
    dump_diagnostics
    return 1
  fi
  if [ "$variant" = index-cache ]; then
    # Read the log once: grep -q exits on the first match, and under pipefail the
    # SIGPIPE it gives a still-writing docker logs would fail the whole check.
    local seeder_log
    seeder_log=$(docker logs "$seeder" 2>&1)
    if ! grep -q 'unauthenticated library add refused: status 401' <<<"$seeder_log"; then
      echo "FAIL ($variant): the seeder did not see a 401 for the add without a token" >&2
      dump_diagnostics
      return 1
    fi
    echo "ok ($variant): add without token refused with 401, with token accepted" >&2
    if ! grep -q 'unauthenticated POST :8090/torrents refused: status 401' <<<"$seeder_log" ||
      ! grep -q 'unauthenticated GET :8090/echo open: status 200' <<<"$seeder_log"; then
      echo "FAIL ($variant): GoStorm on 8090 did not refuse torrent control without a token, or closed /echo" >&2
      dump_diagnostics
      return 1
    fi
    echo "ok ($variant): GoStorm 8090 refused POST /torrents with 401, /echo open" >&2
    check_files_record
  fi
  echo "PASS ($variant): sha256 $actual, $((SECONDS - t0))s" >&2

  # A graceful stop makes the seeder log its upload stats (piece data sent / video size).
  docker stop -t 10 "$seeder" >/dev/null 2>&1 || true
  docker logs "$seeder" 2>&1 | grep 'upload stats' | sed "s/^/($variant) /" >&2 || true
  stop_stack
}

# check_sha label path want: full read through FUSE, compared by sha256.
check_sha() {
  local label=$1 path=$2 want=$3 got
  if ! got=$(docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" sha256sum "$path"); then
    echo "FAIL (replacement): $label: read failed or timed out" >&2
    dump_diagnostics
    return 1
  fi
  got=${got%% *}
  if [ "$got" != "$want" ]; then
    echo "FAIL (replacement): $label: sha256 mismatch: got $got, want $want" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($label): sha256 $got" >&2
}

# stub_gone path: the old stub must disappear from the mount (polled briefly, as
# FUSE may cache the directory).
stub_gone() {
  local tries=0
  while [ "$tries" -lt 10 ]; do
    tries=$((tries + 1))
    docker exec "$flodni" test ! -e "$1" && return 0
    sleep 1
  done
  return 1
}

run_replacement() {
  local dir t0
  t0=$SECONDS
  start_stack replacement -mode replacement
  local out="$dir/out"

  wait_for_file "$out/a.path" "$WAIT_SECONDS" replacement
  echo "step add A: $((SECONDS - t0))s since start" >&2
  local path_a path_b
  path_a="$MOUNT_PATH/$(cat "$out/a.path")"
  check_sha "A read" "$path_a" "$(cat "$out/a.sha256")"
  : >"$out/go-b"

  t0=$SECONDS
  wait_for_file "$out/b.path" "$WAIT_SECONDS" replacement
  echo "step add B (replaces A): $((SECONDS - t0))s" >&2
  path_b="$MOUNT_PATH/$(cat "$out/b.path")"
  if [ "$path_a" = "$path_b" ]; then
    echo "FAIL (replacement): B filed at A's path $path_a" >&2
    dump_diagnostics
    return 1
  fi
  if ! stub_gone "$path_a"; then
    echo "FAIL (replacement): A's stub $path_a still exists after B replaced it" >&2
    dump_diagnostics
    return 1
  fi
  check_sha "B read" "$path_b" "$(cat "$out/b.sha256")"
  : >"$out/go-c"

  t0=$SECONDS
  wait_for_file "$out/c.status" "$WAIT_SECONDS" replacement
  echo "step add C (refused): $((SECONDS - t0))s, seeder measured $(cat "$out/c.seconds")s" >&2
  local want_status=422 status
  if [ "${E2E_REPLACEMENT_EXPECT_SUCCESS_FOR_C:-}" = "1" ]; then
    want_status=200
    echo "E2E_REPLACEMENT_EXPECT_SUCCESS_FOR_C: expecting status $want_status" >&2
  fi
  status=$(cat "$out/c.status")
  if [ "$status" != "$want_status" ]; then
    echo "FAIL (replacement): add C: status $status, want $want_status: $(cat "$out/c.body")" >&2
    dump_diagnostics
    return 1
  fi
  if ! grep -q 'existing files kept' "$out/c.body"; then
    echo "FAIL (replacement): add C: body does not say existing files were kept: $(cat "$out/c.body")" >&2
    dump_diagnostics
    return 1
  fi
  if ! docker exec "$flodni" test -e "$path_b"; then
    echo "FAIL (replacement): B's stub $path_b is gone after the refused add" >&2
    dump_diagnostics
    return 1
  fi
  check_readability "$out" || return 1
  check_readability_gauge || return 1
  check_session_line "$out" || return 1
  check_sessions_closed || return 1
  check_sha "B read after refused C" "$path_b" "$(cat "$out/b.sha256")"
  echo "PASS (replacement): A replaced by B, C refused with $status" >&2

  stop_stack
}

# prom_body: the /metrics/prometheus body without the HTTP headers.
prom_body() {
  local raw
  raw=$(prom_raw)
  printf '%s\n' "${raw#*$'\r\n\r\n'}"
}

# read_source body source: flodni_read_bytes_total of one serving path, 0 if absent.
read_source() {
  awk -v s="source=\"$2\"" 'index($0, "flodni_read_bytes_total{") == 1 && index($0, s) { printf "%d", $2; f = 1 } END { if (!f) printf "0" }' <<<"$1"
}

# check_readback v path head_want tail_want: the seeder dies, the head (8 MiB) and the
# tail (4 MiB) are read back through the mount and compared with the seeder's hashes, and
# every byte must come from the SSD windows. The read counters must be 0 before (the
# variant has not read through the mount since the process started).
check_readback() {
  local v=$1 path=$2 head_want=$3 tail_want=$4
  echo "killing $seeder" >&2
  docker kill "$seeder" >/dev/null
  local first got ms size body
  size=$(docker exec "$flodni" stat -c %s "$path")
  # Measurement only: the first read of the head after the kill, then the whole head.
  first=$(docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" sh -c "s=\$(date +%s%N); dd if='$path' of=/dev/null bs=4096 count=1 status=none && e=\$(date +%s%N) && echo \$(( (e - s) / 1000000 ))") || first=failed
  echo "LATENCY ($v): first 4 KiB of the head after the kill: ${first} ms" >&2
  if ! got=$(docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" sh -c "s=\$(date +%s%N); h=\$(head -c 8388608 '$path' | sha256sum) && e=\$(date +%s%N) && echo \"\${h%% *} \$(( (e - s) / 1000000 ))\""); then
    echo "FAIL ($v): head read of 8 MiB failed or timed out after the seeder was killed" >&2
    dump_diagnostics
    return 1
  fi
  ms=${got#* } got=${got%% *}
  echo "LATENCY ($v): head 8 MiB read and hash: ${ms} ms" >&2
  if [ "$got" != "$head_want" ]; then
    echo "FAIL ($v): head sha256 mismatch: got $got, want $head_want" >&2
    dump_diagnostics
    return 1
  fi
  if ! got=$(docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" sh -c "s=\$(date +%s%N); h=\$(tail -c 4194304 '$path' | sha256sum) && e=\$(date +%s%N) && echo \"\${h%% *} \$(( (e - s) / 1000000 ))\""); then
    echo "FAIL ($v): tail read of 4 MiB failed or timed out after the seeder was killed" >&2
    dump_diagnostics
    return 1
  fi
  ms=${got#* } got=${got%% *}
  echo "LATENCY ($v): tail 4 MiB read and hash: ${ms} ms (file size $size)" >&2
  if [ "$got" != "$tail_want" ]; then
    echo "FAIL ($v): tail sha256 mismatch: got $got, want $tail_want" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): head 8 MiB and tail 4 MiB match the seeder's sha256 with the seeder dead" >&2

  # Reads by source: every byte from the SSD windows (the counters were 0 before).
  body=$(prom_body)
  local fb rc wh wt
  fb=$(read_source "$body" fetch_block) rc=$(read_source "$body" racache_hit)
  wh=$(read_source "$body" warmup_head) wt=$(read_source "$body" warmup_tail)
  echo "READS ($v): fetch_block=$fb racache_hit=$rc warmup_head=$wh warmup_tail=$wt" >&2
  if [ "$wh" -lt $((8 * 1048576)) ] || [ "$wt" -lt $((4 * 1048576)) ] || [ "$fb" -ne 0 ] || [ "$rc" -ne 0 ]; then
    echo "FAIL ($v): the reads did not come from the SSD windows: want warmup_head >= 8 MiB, warmup_tail >= 4 MiB, fetch_block 0, racache_hit 0" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): reads served from warmup_head and warmup_tail only" >&2
}

# run_warm_on_add: the warmer against the real engine. Add the fixture with warm_on_add
# on and read nothing through the mount; wait for the fill; check the evidence (metric,
# [Warm] line, per-file row, torrent record); kill the seeder; read the head and the
# tail back through the mount and compare them with the seeder's hashes, and show that
# the bytes came from the SSD windows. Pause/wake and removal while queued are not here
# (docs/ideas.md); the replacement variant runs with the switch off.
run_warm_on_add() {
  local dir t0=$SECONDS
  start_stack warm-on-add
  local out="$dir/out" v=warm-on-add
  wait_for_file "$out/expected.sha256" "$WAIT_SECONDS" "$v"
  wait_for_file "$out/fuse_path" "$WAIT_SECONDS" "$v"
  local hash head_want tail_want path
  hash=$(cat "$out/hash") head_want=$(cat "$out/head8m.sha256") tail_want=$(cat "$out/tail4m.sha256")
  path="$MOUNT_PATH/$(cat "$out/fuse_path")"
  echo "step add: $((SECONDS - t0))s since start" >&2

  # Step 2: wait for the warmer, with no read through the mount.
  local body="" warmed=0 deadline=$((SECONDS + 120)) w0=$SECONDS
  while [ "$SECONDS" -lt "$deadline" ]; do
    body=$(prom_body)
    warmed=$(awk '/^flodni_warm_fills_total\{trigger="add",outcome="warmed"\} / { printf "%d", $2 }' <<<"$body")
    [ "${warmed:-0}" -ge 1 ] && break
    sleep 1
  done
  if [ "${warmed:-0}" -lt 1 ]; then
    echo "FAIL ($v): no warm-on-add fill reached outcome warmed in 120 s: $(grep -E '^flodni_warm_fills_total\{trigger="add"' <<<"$body" | tr '\n' ' ')" >&2
    dump_diagnostics
    return 1
  fi
  echo "step warm: $((SECONDS - w0))s from the add to a warmed fill" >&2
  echo "ok ($v): $(grep -E '^flodni_warm_fills_total\{trigger="add",outcome="warmed"\}' <<<"$body")" >&2
  local src before_total=0 n
  for src in fetch_block racache_hit warmup_head warmup_tail; do
    n=$(read_source "$body" "$src")
    before_total=$((before_total + n))
  done
  if [ "$before_total" -ne 0 ]; then
    echo "FAIL ($v): flodni_read_bytes_total sums to $before_total before any mount read, want 0: $(grep -E '^flodni_read_bytes_total\{' <<<"$body" | tr '\n' ' ')" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): flodni_read_bytes_total is 0 before any mount read; the warmer filled the windows" >&2
  echo "PROM ($v): $(grep -E '^flodni_warm_(fill_bytes_total|queue_files|paused|cache_failures_total)[{ ]' <<<"$body" | tr '\n' ' ')" >&2

  # The [Warm] line: full windows, swarm-delivered pieces, no file name.
  local logs line json pieces hb tb idx
  logs=$(docker logs "$flodni" 2>&1 || true)
  line=$(grep -F '[Warm] ' <<<"$logs" | grep -F "\"hash\":\"$hash\"" | grep -F '"outcome":"warmed"' | head -n 1 || true)
  if [ -z "$line" ]; then
    echo "FAIL ($v): no [Warm] line with outcome warmed for $hash" >&2
    dump_diagnostics
    return 1
  fi
  json=${line#*\[Warm\] }
  pieces=$(grep -o '"delivered_pieces":[0-9]*' <<<"$json" | cut -d: -f2)
  hb=$(grep -o '"head_bytes":[0-9]*' <<<"$json" | cut -d: -f2)
  tb=$(grep -o '"tail_bytes":[0-9]*' <<<"$json" | cut -d: -f2)
  idx=$(grep -o '"file":[0-9]*' <<<"$json" | cut -d: -f2)
  if [ "${pieces:-0}" -le 0 ] || [ "${hb:-0}" -ne $((16 * 1048576)) ] || [ "${tb:-0}" -ne $((8 * 1048576)) ]; then
    echo "FAIL ($v): [Warm] line wants delivered_pieces > 0, head_bytes 16 MiB, tail_bytes 8 MiB: $json" >&2
    dump_diagnostics
    return 1
  fi
  if grep -qE 'Flodni\.E2E|\.mkv|/mnt/' <<<"$json"; then
    echo "FAIL ($v): the [Warm] line carries a path or file name: $json" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): [Warm] $json" >&2

  # The per-file row and the torrent record.
  local row="" trow="" deadline2=$((SECONDS + 30))
  while [ "$SECONDS" -lt "$deadline2" ]; do
    body=$(health_body)
    row=$(grep -o "{\"file_index\":$idx,[^}]*}" <<<"$body" || true)
    trow=$(grep -o "{\"hash\":\"$hash\"[^}]*" <<<"$body" || true)
    [ -n "$row" ] && break
    sleep 1
  done
  if [[ "$row" != *'"readability":"warmed"'* || "$row" != *'"cache":"cached"'* ||
    "$row" != *'"last_swarm_delivery_at":"'* || "$row" != *'"last_attempt_at":"'* ]]; then
    echo "FAIL ($v): files row of file $idx wants readability warmed, cache cached, last_swarm_delivery_at and last_attempt_at set: ${row:-none}" >&2
    dump_diagnostics
    return 1
  fi
  if [[ "$trow" != *'"last_swarm_read_at":"'* ]]; then
    echo "FAIL ($v): the torrent record has no last_swarm_read_at: ${trow:-none}" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): files row $row" >&2
  echo "ok ($v): torrent record ${trow}" >&2

  check_readback "$v" "$path" "$head_want" "$tail_want" || return 1
  echo "NOTE ($v): pause/wake and removal while queued are not exercised here; the replacement variant runs with warm_on_add off" >&2
  echo "PASS ($v): windows filled by the warmer, read back with the seeder dead, $((SECONDS - t0))s" >&2

  stop_stack
}

# catchup_lines: the [WarmCatchup] lines of the container's whole log (a restart keeps it).
catchup_lines() { docker logs "$flodni" 2>&1 | grep -F '[WarmCatchup] ' || true; }

# json_num line key: the integer value of "key" in a JSON log line, empty if absent.
json_num() { grep -o "\"$2\":[0-9]*" <<<"$1" | head -n 1 | cut -d: -f2; }

# catchup_zero body label: every catch-up and add fill series, the catch-up fill bytes
# and every backlog gauge state must be 0. Prints the series it checked.
catchup_zero() {
  local body=$1 label=$2 bad
  bad=$(grep -E '^(flodni_warm_fills_total\{trigger="(add|catchup)"|flodni_warm_fill_bytes_total\{trigger="catchup"|flodni_warm_catchup_files\{)' <<<"$body" | awk '$2 != 0' || true)
  if [ -n "$bad" ] || ! grep -q '^flodni_warm_catchup_files{state="pending"} 0$' <<<"$body"; then
    echo "FAIL ($label): want every add/catch-up fill series, the catch-up bytes and the gauge at 0: ${bad:-gauge missing}" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($label): add and catch-up fills, catch-up bytes and the backlog gauge are all 0" >&2
}

# catchup_pass label want_passes: after a restart, wait up to 120 s for the want_passes-th
# [WarmCatchup] pass_end line. Fails fast, with the [Warm] and [WarmCatchup] lines, on a
# wake_error outcome or a container that exited. A first pass that feeds nothing is not
# retried for 6 hours, so waiting longer would only hide it. Sets pass_start_line and
# pass_end_line.
catchup_pass() {
  local label=$1 want=$2 deadline=$((SECONDS + 120)) lines n
  while :; do
    lines=$(catchup_lines)
    n=$(grep -c '"event":"pass_end"' <<<"$lines" || true)
    [ "$n" -ge "$want" ] && break
    if ! is_running "$flodni" || docker logs "$flodni" 2>&1 | grep -F '[Warm] ' | grep -qF '"outcome":"wake_error"'; then
      echo "FAIL ($label): a container exited or a wake_error was logged before pass $want ended" >&2
      docker logs "$flodni" 2>&1 | grep -E '\[Warm(Catchup)?\] ' >&2 || true
      dump_diagnostics
      return 1
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "FAIL ($label): no pass_end number $want in 120 s" >&2
      docker logs "$flodni" 2>&1 | grep -E '\[Warm(Catchup)?\] |WARNING' >&2 || true
      dump_diagnostics
      return 1
    fi
    sleep 1
  done
  pass_start_line=$(grep -F '"event":"pass_start"' <<<"$lines" | sed -n "${want}p")
  pass_end_line=$(grep -F '"event":"pass_end"' <<<"$lines" | sed -n "${want}p")
  echo "ok ($label): $pass_start_line" >&2
  echo "ok ($label): $pass_end_line" >&2
}

# gostorm_list: the engine's torrent list (POST /torrents list on 8090; the variant sets
# no token), over /dev/tcp, body only. Empty until the engine's API is up.
gostorm_list() {
  docker exec "$flodni" bash -c 'b="{\"action\":\"list\"}"
    exec 3<>/dev/tcp/127.0.0.1/8090 && printf "POST /torrents HTTP/1.0\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: ${#b}\r\n\r\n%s" "$b" >&3 && cat <&3' 2>/dev/null | sed '1,/^\r$/d' || true
}

# run_catchup: catch-up (#124, #125) against the real engine across daemon restarts.
# Off phase: the fixture is added with warm_on_add false and nothing reads it, so catch-up
# must stay silent. Then config.json is rewritten in place with warm_on_add true and the
# container restarted (state and cache are volumes, the stub stays in the container's
# layer): the first pass, 1 s after the start, must warm the title with trigger catchup.
# A second restart must find nothing to do. Last, the seeder dies and the windows are read
# back from the SSD. The quota stop, the eviction exclusion, the stale-evicted overwrite
# and the 20 h rule are unit tests only (warm_catchup_test.go, internal/warmer).
run_catchup() {
  local dir t0=$SECONDS
  start_stack catch-up
  local out="$dir/out" v=catch-up
  wait_for_file "$out/expected.sha256" "$WAIT_SECONDS" "$v"
  wait_for_file "$out/fuse_path" "$WAIT_SECONDS" "$v"
  local hash head_want tail_want path
  hash=$(cat "$out/hash") head_want=$(cat "$out/head8m.sha256") tail_want=$(cat "$out/tail4m.sha256")
  path="$MOUNT_PATH/$(cat "$out/fuse_path")"
  echo "step add: $((SECONDS - t0))s since start" >&2

  # Step 1: off phase. The first pass (1 s after the start, long done) was skipped.
  local body lines
  sleep 5
  lines=$(catchup_lines)
  if [ -n "$lines" ] || docker logs "$flodni" 2>&1 | grep -qF 'WARNING: [WarmCatchup]'; then
    echo "FAIL ($v): off phase: a [WarmCatchup] line or warning was logged: $lines" >&2
    dump_diagnostics
    return 1
  fi
  body=$(prom_body)
  catchup_zero "$body" "$v off" || return 1
  echo "ok ($v): off phase: no [WarmCatchup] line, no catch-up warning" >&2

  # Step 2: warm_on_add on, in place, restart. The torrent is only a record in the engine's
  # database after the restart; poll the engine's list to see its state before the pass.
  echo "step restart 1: warm_on_add true, warm_catchup_per_hour 3600" >&2
  catchup_config true "$dir/config.json"
  local r0=$SECONDS list="" stat=""
  docker restart "$flodni" >/dev/null
  while [ "$((SECONDS - r0))" -lt 20 ]; do
    list=$(gostorm_list)
    stat=$(grep -o "\"hash\":\"$hash\"[^}]*" <<<"$list" | grep -o '"stat_string":"[^"]*"' | head -n 1 | cut -d'"' -f4 || true)
    [ -n "$stat" ] && break
    sleep 0.2
  done
  echo "WAKE ($v): engine list right after the restart: ${stat:-no entry}" >&2
  local wake_seen=${stat:-none}
  catchup_pass "$v restart 1" 1 || return 1
  local start1=$pass_start_line end1=$pass_end_line
  echo "step catch-up: $((SECONDS - r0))s from the restart to pass_end" >&2
  if [ "$(json_num "$start1" pending)" != 1 ] || [ "$(json_num "$start1" candidate_hashes)" != 1 ]; then
    echo "FAIL ($v): pass_start wants candidate_hashes 1 and pending 1: $start1" >&2
    dump_diagnostics
    return 1
  fi
  if [ "$(json_num "$end1" fed)" != 1 ] || [ "$(json_num "$end1" fed_hashes)" != 1 ]; then
    echo "FAIL ($v): pass_end wants fed_hashes 1 and fed 1: $end1" >&2
    docker logs "$flodni" 2>&1 | grep -E '\[Warm(Catchup)?\] ' >&2 || true
    dump_diagnostics
    return 1
  fi
  body=$(prom_body)
  local cu add_bad bytes rt
  cu=$(awk '/^flodni_warm_fills_total\{trigger="catchup",outcome="warmed"\} / { printf "%d", $2 }' <<<"$body")
  add_bad=$(grep -E '^flodni_warm_fills_total\{trigger="add"' <<<"$body" | awk '$2 != 0' || true)
  bytes=$(awk '/^flodni_warm_fill_bytes_total\{trigger="catchup"/ { n += $2 } END { printf "%d", n }' <<<"$body")
  rt=0
  local src n
  for src in fetch_block racache_hit warmup_head warmup_tail; do
    n=$(read_source "$body" "$src")
    rt=$((rt + n))
  done
  if [ "${cu:-0}" -ne 1 ] || [ -n "$add_bad" ] || [ "${bytes:-0}" -le 0 ] || [ "$rt" -ne 0 ]; then
    echo "FAIL ($v): want catchup warmed 1, every add outcome 0, catch-up bytes > 0, read bytes 0: warmed=${cu:-absent} add_nonzero='$add_bad' bytes=$bytes read=$rt" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): $(grep -E '^flodni_warm_fills_total\{trigger="catchup",outcome="warmed"\}' <<<"$body"), every trigger=add outcome 0, catch-up fill bytes $bytes, flodni_read_bytes_total 0" >&2
  echo "PROM ($v): $(grep -E '^flodni_warm_fill_bytes_total\{trigger="catchup"' <<<"$body" | tr '\n' ' ')" >&2
  # Every gauge state is 0 once the pass has ended (the gauge is set before the line).
  if [ -n "$(grep -E '^flodni_warm_catchup_files\{' <<<"$body" | awk '$2 != 0')" ]; then
    echo "FAIL ($v): a backlog gauge state is not 0 after pass_end: $(grep -E '^flodni_warm_catchup_files\{' <<<"$body" | tr '\n' ' ')" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): after pass_end every flodni_warm_catchup_files state is 0" >&2

  local line json idx
  line=$(docker logs "$flodni" 2>&1 | grep -F '[Warm] ' | grep -F '"trigger":"catchup"' || true)
  if [ "$(wc -l <<<"$line" | tr -d ' ')" != 1 ] || [ -z "$line" ]; then
    echo "FAIL ($v): want exactly one [Warm] line with trigger catchup: ${line:-none}" >&2
    dump_diagnostics
    return 1
  fi
  json=${line#*\[Warm\] }
  idx=$(json_num "$json" file)
  if ! grep -qF "\"hash\":\"$hash\"" <<<"$json" || ! grep -qF '"outcome":"warmed"' <<<"$json" ||
    grep -qE 'Flodni\.E2E|\.mkv|/mnt/' <<<"$json"; then
    echo "FAIL ($v): the catch-up [Warm] line is not warmed for $hash or carries a path: $json" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): [Warm] $json" >&2

  local row="" trow="" deadline=$((SECONDS + 30))
  while [ "$SECONDS" -lt "$deadline" ]; do
    body=$(health_body)
    row=$(grep -o "{\"file_index\":$idx,[^}]*}" <<<"$body" || true)
    trow=$(grep -o "{\"hash\":\"$hash\"[^}]*" <<<"$body" || true)
    [ -n "$row" ] && break
    sleep 1
  done
  if [[ "$row" != *'"readability":"warmed"'* || "$row" != *'"cache":"cached"'* ||
    "$row" != *'"last_swarm_delivery_at":"'* || "$row" != *'"last_attempt_at":"'* ]]; then
    echo "FAIL ($v): files row of file $idx wants readability warmed, cache cached, last_swarm_delivery_at and last_attempt_at set: ${row:-none}" >&2
    dump_diagnostics
    return 1
  fi
  if [[ "$trow" != *'"last_swarm_read_at":"'* ]]; then
    echo "FAIL ($v): the torrent record has no last_swarm_read_at: ${trow:-none}" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): files row $row" >&2
  echo "ok ($v): torrent record ${trow}" >&2
  local attempt1
  attempt1=$(grep -o '"last_attempt_at":"[^"]*"' <<<"$row")

  # The WakeStored claim: a woken torrent was closed. Only the engine's list right after
  # the start can show it, and the pass runs 1 s after the start, so say what was seen.
  if [[ "$wake_seen" == *"in db"* ]]; then
    echo "ok ($v): WakeStored on a closed private torrent shown: the engine listed it as \"${wake_seen}\" when its API first answered, before the pass fed it" >&2
  else
    echo "NOTE ($v): the WakeStored-on-a-closed-private-torrent claim is unverified by this run: the engine list when its API first answered showed \"${wake_seen}\"" >&2
  fi

  # Step 3: a second restart with the same config finds nothing to do.
  echo "step restart 2: no config change" >&2
  docker restart "$flodni" >/dev/null
  catchup_pass "$v restart 2" 2 || return 1
  local start2=$pass_start_line end2=$pass_end_line
  if [ "$(json_num "$start2" pending)" != 0 ] || [ "$(json_num "$end2" fed)" != 0 ]; then
    echo "FAIL ($v): second pass wants pending 0 and fed 0: $start2 / $end2" >&2
    dump_diagnostics
    return 1
  fi
  body=$(prom_body)
  catchup_zero "$body" "$v restart 2" || return 1
  if [ "$(docker logs "$flodni" 2>&1 | grep -F '[Warm] ' | grep -cF '"trigger":"catchup"')" != 1 ]; then
    echo "FAIL ($v): a second catch-up [Warm] line appeared after the second restart" >&2
    dump_diagnostics
    return 1
  fi
  local row2 attempt2
  row2=$(health_body | grep -o "{\"file_index\":$idx,[^}]*}" || true)
  attempt2=$(grep -o '"last_attempt_at":"[^"]*"' <<<"$row2")
  if [ -z "$attempt1" ] || [ "$attempt1" != "$attempt2" ] || [[ "$row2" != *'"cache":"cached"'* ]]; then
    echo "FAIL ($v): the files row changed or is gone after the second restart: before $attempt1, after: ${row2:-none}" >&2
    dump_diagnostics
    return 1
  fi
  echo "ok ($v): second restart: pending 0, fed 0, no catch-up fill or bytes, no new [Warm] line, $attempt2 unchanged" >&2

  # Step 4: the windows survive the restarts: read them back with the seeder dead.
  check_readback "$v" "$path" "$head_want" "$tail_want" || return 1
  echo "PASS ($v): off phase silent, one catch-up fill after a restart, none after the next, read back with the seeder dead, $((SECONDS - t0))s" >&2

  stop_stack
}

want_variant() { [ -z "${E2E_ONLY:-}" ] || [ "$E2E_ONLY" = "$1" ]; }

if want_variant default; then run_variant default; fi
if want_variant slow-seeder; then run_variant slow-seeder; fi
if want_variant index-cache; then run_variant index-cache; fi
if want_variant warm-on-add; then run_warm_on_add; fi
if want_variant catch-up; then run_catchup; fi
if want_variant replacement; then run_replacement; fi
echo "e2e: all variants passed" >&2
