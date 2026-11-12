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
# fail); E2E_ONLY=<variant> runs just that variant (default, slow-seeder,
# index-cache, replacement). slow-seeder is default with a seeder that keeps the
# engine's 1 MiB per-peer request allowance. The index-cache variant runs
# with a random FLODNI_API_TOKEN: the seeder must see 401 without the bearer
# header before its authorised add, and the same for GoStorm's 8090 (401 on
# POST /torrents, 200 on /echo); the other variants run with auth off.
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

is_running() {
  [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]
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
  echo "full read of $variant took $((SECONDS - full_t0))s" >&2
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
  fi
  echo "PASS ($variant): sha256 $actual, $((SECONDS - t0))s" >&2

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
  check_sha "B read after refused C" "$path_b" "$(cat "$out/b.sha256")"
  echo "PASS (replacement): A replaced by B, C refused with $status" >&2

  stop_stack
}

want_variant() { [ -z "${E2E_ONLY:-}" ] || [ "$E2E_ONLY" = "$1" ]; }

if want_variant default; then run_variant default; fi
if want_variant slow-seeder; then run_variant slow-seeder; fi
if want_variant index-cache; then run_variant index-cache; fi
if want_variant replacement; then run_replacement; fi
echo "e2e: all variants passed" >&2
