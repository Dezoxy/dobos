#!/usr/bin/env bash
# End-to-end stream test: a local seeder seeds a private torrent, the real Flodni
# image adds it through the library API, and the file is read back through FUSE
# and compared by sha256. Runs two Flodni configs in sequence.
#
#   bash tests/e2e/run.sh [flodni-image] [seeder-image]
#
# Test hooks: E2E_CORRUPT_EXPECTED=1 flips the expected hash (the run must fail);
# E2E_KILL_SEEDER_AFTER_ADD=1 kills the seeder once the add is done (the read must
# fail within its timeout instead of hanging).
set -euo pipefail

FLODNI_IMAGE=${1:-flodni:ci}
SEEDER_IMAGE=${2:-flodni-e2e-seeder:ci}
SOURCE_PATH=/mnt/tiramisu-mkv-real
MOUNT_PATH=/mnt/tiramisu-mkv-virtual
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
    *) echo "unknown variant $variant" >&2; return 1 ;;
  esac
}

run_variant() {
  local variant=$1
  local dir="$work/$variant"
  mkdir -p "$dir/state" "$dir/out" "$dir/cache"
  chmod 777 "$dir/out" "$dir/cache"
  write_config "$variant" "$dir/config.json"

  echo "=== variant: $variant ===" >&2
  network="e2e-$variant-$$"
  flodni="e2e-flodni-$variant"
  seeder="e2e-seeder-$variant"
  docker network create --internal "$network" >/dev/null

  docker run -d --name "$flodni" --network "$network" --network-alias flodni \
    --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor:unconfined \
    -v "$dir/config.json:/config.json:ro" -v "$dir/state:/state" \
    -v "$dir/cache:/cache" "$FLODNI_IMAGE" >/dev/null
  docker run -d --name "$seeder" --network "$network" --network-alias seeder \
    -v "$dir/out:/e2e" "$SEEDER_IMAGE" -out /e2e >/dev/null

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
  local actual
  if ! actual=$(docker exec "$flodni" timeout -k 10 "$READ_TIMEOUT" sha256sum "$path"); then
    echo "FAIL ($variant): full read failed or timed out" >&2
    dump_diagnostics
    return 1
  fi
  actual=${actual%% *}
  if [ "$actual" != "$expected" ]; then
    echo "FAIL ($variant): sha256 mismatch: got $actual, want $expected" >&2
    dump_diagnostics
    return 1
  fi
  echo "PASS ($variant): sha256 $actual" >&2

  docker rm -f "$seeder" "$flodni" >/dev/null
  docker network rm "$network" >/dev/null
  seeder="" flodni="" network=""
}

run_variant default
run_variant index-cache
echo "e2e: all variants passed" >&2
