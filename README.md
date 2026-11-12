# Flodni

Flodni presents torrents as ordinary media files. A media server such as Plex
or Jellyfin scans and plays them from a FUSE mount, and the bytes are fetched
from the swarm when they are read. Nothing is downloaded to disk in full.

It is one Go binary: a torrent engine, a FUSE virtual filesystem and a small
control API.

## Status

Flodni is a hard fork of
[Tiramisu](https://github.com/MrRobotoGit/tiramisu) by Matteo Rancilio,
started on 2026-11-10. It no longer follows upstream; the reasons are in
[ADR 0001](docs/architecture/decisions/0001-hard-fork-tiramisu.md).

The code is Tiramisu's with this fork's reliability changes. The Go module,
the binary, the image paths and the environment variables are named Flodni.
It is maintained by one person for their own use. Expect breaking changes
and no support.

## Documentation

| Document | What it holds |
|---|---|
| [docs/roadmap.md](docs/roadmap.md) | What is planned, in order, and what is done. |
| [docs/ideas.md](docs/ideas.md) | What could be improved, not yet planned. |
| [docs/architecture/README.md](docs/architecture/README.md) | The C4 model, its views and the decisions. |
| [README.upstream.md](README.upstream.md) | Tiramisu's user guide as inherited: install, configuration, API. |
| [SECURITY.md](SECURITY.md) | How to report a vulnerability. |

## Building

Linux only: the FUSE layer is cgo against `libfuse3`.

```bash
sudo apt-get install -y libfuse3-dev pkg-config
make go-check   # gofmt, build, vet
make go-test    # tests with the race detector
```

The container image builds anywhere Docker runs:

```bash
docker build -f docker/Dockerfile -t flodni:dev .
```

## Running

The image mounts the FUSE view at `/mnt/flodni-virtual`, reads library
files from `/mnt/flodni-real` and keeps its state under `/state`. Set the
paths with `FLODNI_ROOT_PATH`, `FLODNI_SOURCE_PATH`, `FLODNI_MOUNT_PATH`,
`FLODNI_STATE_DIR`, `FLODNI_LOG_DIR`, `FLODNI_LOG_FILE` and
`FLODNI_HOST_MOUNT_HINT`; the Plex settings use `FLODNI_PLEX_URL` and
`FLODNI_PLEX_TOKEN`. The older `TIRAMISU_*` names still work and lose to the
`FLODNI_*` ones. Existing state carries over: the database is still
`tiramisu.db` and the log file `tiramisu.log`.

Shutdown saves state, then unmounts; a mount that will not unmount is
detached lazily after 5 s, and the process exits by 8 s at the latest. That
fits inside Docker's default 10 s `stop_grace_period`. A longer grace period
is fine; do not set it below 10 s, or Docker kills the process before the
deadline.

The command-line paths (the entrypoint's `FLODNI_SOURCE_PATH` and
`FLODNI_MOUNT_PATH`) win over `physical_source_path` and `fuse_mount_path` in
`config.json`; a different value there is logged and overridden.

The control server on `metrics_port` (default 9080) is open unless you set a
token. Set `api_token` in `config.json` or `FLODNI_API_TOKEN` (the variable
wins) and every route except the panel page and
`/api/health` needs `Authorization: Bearer <token>`. The Plex or Jellyfin webhook cannot send
headers, so `/plex/webhook` and `/webhook` also accept the token in the URL,
for example `http://host:9080/plex/webhook?token=<token>`; no other route
does. `/control` loads without it and asks for the token on its first call,
then keeps it in the browser's local storage. `GET /api/config` never
returns the token, and saving from the panel leaves it unchanged. The
same token covers GoStorm's port 8090 (every route except `GET /echo`, header
only, no `?token=`); external clients of 8090 must send it. There is no TLS:
see `SECURITY.md`.

`GET /api/health` on the control port reports the mount, the engine and the
state database, each as `{"ok": ..., "reason": "..."}`, with an overall
`status` of `ok` (HTTP 200), `degraded` (engine or state database down) or
`down` (mount down), the last two with HTTP 503. It is open even when a token
is set and returns status words only. The image's health check runs
`flodni healthcheck`, which requests it on `127.0.0.1:<metrics_port>` and
exits 0 on HTTP 200 and 1 otherwise; run it with
`docker exec <container> flodni healthcheck`.

## Readability record

`GET /api/torrents/health` on the control port lists what Flodni has
recorded about each torrent's reads. With a token set it needs the bearer
header (no `?token=`). It answers 503 when the state database is off:
nothing is recorded then. The fields below are the supported contract for
outside tools; log wording and stub names are not (the `[Session]` log
line below is the one log format that is).

| Field | Meaning |
|---|---|
| `hash` | Full info hash, lower-case hex. Join it with `GET /api/library/list`. |
| `consecutive_failures` | Sessions in a row that ended with a failed read and nothing served by the swarm. A session counts once, and only if it lasted 25 s. |
| `first_failure_at` | Start of the current run of failures; `null` when the count is 0. |
| `last_failure_at` | Last recorded failure; kept after a reset. |
| `last_failure_reason` | `no_peers`, `timeout` (peers connected, the read gave up), `not_active` (the engine did not have the torrent running), `unknown` (the engine did not answer in time); `null` for older records. |
| `last_failure_source` | `mount`: a reader through the FUSE mount. |
| `last_swarm_read_at` | Close time of the last session in which the swarm served a read; `null` if none was recorded. |

`?failing=1` keeps torrents with at least one consecutive failure,
`?min_failures=N` those with at least N.

- Only bytes from the swarm reset the count. A cache hit never does.
- No entry means no evidence either way, not that the torrent reads.
- A `null` `last_swarm_read_at` is not a failure: reads answered from the
  caches do not set it.
- A session whose bytes came from the read-ahead pump rather than direct
  fetches may not set `last_swarm_read_at`; treat a single failure as weak
  evidence.
- A wake that had to fetch the metainfo from the swarm resets the count
  without setting `last_swarm_read_at`.
- A metainfo that never arrives is not counted: the open fails before a
  session exists. A cancelled read, a read Flodni refuses, and an open that
  fails before any read are not recorded either.
- Times are UTC and are taken when the session closes, up to two minutes
  after its last read.
- An entry goes when the last stub of its torrent is removed through the
  library API. A session still open on that torrent writes the entry back
  when it closes, and a stub deleted another way leaves it in place. Such
  entries stay until a startup prunes them. The prune runs only after a
  complete scan: both `movies` and `tv` exist and every stub was read. It
  is skipped otherwise, and the log says so.
- Join from `GET /api/library/list`: an entry whose hash that list does
  not carry belongs to a torrent that left the library. Ignore it.
- New reason and source values may appear; ignore the ones you do not
  know. `GET /api/library/list` always reports `file_index` (the
  engine's index, starting at 1; 0 means the stub carries none). Per-file
  records are designed, not implemented (ADR 0002).

## Session log

A session covers the reads of one torrent-backed file, from an open until
the file has been idle for a minute. `GET /api/sessions` lists the open
sessions and the last 200 closed ones, in memory. Each closed session is
also written to the log as one line, so a log store keeps the history. The
image tees the log to `tiramisu.log` under `$FLODNI_LOG_DIR` (default
`/state/logs`); that file survives a container recreate only when its
directory is on a mounted volume, and it is not rotated. Without the log FIFO the line
goes to stdout only.

```text
[GoProxy] 2026/11/12 09:02:10 [Session] {"path":"/mnt/flodni-real/tv/Example/Example.S01E01_0123abcd.mkv","hash":"0123456789abcdef0123456789abcdef01234567","size":31461601,"opened_at":"2026-11-12T09:00:05Z","closed_at":"2026-11-12T09:02:10Z","bytes_served":31461601,"first_byte_ms":12,"reads":241,"failed_reads":0,"outcome":"served","file_index":1,"bytes_fetch_block":135393,"bytes_racache_hit":31326208,"bytes_warmup_head":0,"bytes_warmup_tail":0,"swarm_served":true,"duration_ms":125000,"failure_reason":null}
```

Everything after `[Session] ` is one JSON object on one line, the same
object `/api/sessions` returns; there an open session has outcome `open`,
no `closed_at` and live counters, so the byte fields can differ from
`bytes_served` by a read in flight. Parse the JSON; do not grep the path,
which `json.Marshal` escapes (`&`, `<`, `>`). Key order is not part of the
contract. The prefix and the fields below are the
supported contract: fields may be added; none is renamed, retyped or
removed. Ignore fields and outcome values you do not know. The `[TTFF]`
and `[DeadSwarm]` lines are unchanged, and their wording is not a contract.

| Field | Type | Meaning |
|---|---|---|
| `path` | string | The stub's path in the library. |
| `hash` | string | Full info hash, lower-case hex. |
| `file_index` | number | The index the stub carries, as `file_index` in `GET /api/library/list`; 0 if it carries none. |
| `size` | number | File size in bytes. |
| `opened_at`, `closed_at` | string | UTC, RFC 3339, whole seconds. |
| `duration_ms` | number | Open to close, including the idle wait before the close. |
| `first_byte_ms` | number or null | Open to the first byte served; `null` if none was. |
| `bytes_served` | number | Bytes handed to the kernel; the sum of the four fields below. |
| `bytes_fetch_block` | number | Bytes of reads that waited for the engine. |
| `bytes_racache_hit` | number | Bytes of reads answered from the read-ahead cache in memory. |
| `bytes_warmup_head`, `bytes_warmup_tail` | number | Bytes of reads answered from the head or tail window on disk. |
| `swarm_served` | boolean | A read was answered through the engine while a peer was connected: the rule that sets `last_swarm_read_at`. |
| `reads`, `failed_reads` | number | Reads served, and reads that gave up. |
| `failure_reason` | string or null | Why the last failed read gave up (the values of `last_failure_reason`); `null` if no read failed. Set whenever a read failed; only `dead_swarm` says the session counted as a failure. |
| `outcome` | string | `served`: 1 MiB or more, or over 64 KiB with a read past 8 MiB or in the tail window. `probe`: fewer bytes, a scan. `unserved`: no bytes. `dead_swarm`: the session counted as a failure in the readability record, whatever it served; it never appears when the state database is off. The thresholds may change. |

- The four byte fields name the path that answered each read, not where
  the bytes came from. Bytes fetched from the swarm in a session reappear
  under the cache fields when later reads find them there, and the engine
  can answer a fetch from pieces it already holds. Only `swarm_served`
  says the swarm delivered.
- `served` means the reader got bytes, not that the swarm sent them.
- A session is per file, not per open or per playback. Opens that overlap
  share one. After a session closes, reads on a handle that is still open
  are not counted until the file is opened again. A playback that pauses
  for more than a minute (30 s once the session is older than 30 minutes)
  is cut short in this record, and one whose session ended when another
  handle closed before the first byte shows as `unserved`.
- A session still open when Flodni stops gets no line.
- `bytes_served` counts what Flodni handed to the kernel. The kernel's
  page cache answers repeated reads without Flodni seeing them.
- The line carries the library path. Remove it before sharing a log.

## Reader budget (measurement only)

Counting exists; refusal is designed and not built. A file that no media
server has confirmed as playing can still be read end to end by a scanner, a
hasher or a copy. Flodni counts what such a reader takes past the head and
tail windows and logs the session once. It never refuses or slows a read.

- **What counts.** A served read counts when the engine or the read-ahead
  cache answered it, the bytes lie outside the head window and the tail
  window, and the file has no playback confirmed by a webhook. Reads the
  warmup files answer never count. The unit is the session (overlapping
  opens of one file, ended after 60 s idle).
- **The counter** `flodni_unconfirmed_read_bytes_total` is the bytes the
  reader took past the windows, whichever path served them. It is not bytes
  downloaded: with the pump on (the default), the engine fill happened
  earlier through the pump or a prefetch, and attributing that fill to the
  reader is not built; it is revisited after Reliability step 6.
- **`unconfirmed_read_margin_mb`** in `config.json` (default 64, 0 logs at the
  first byte past the windows; a negative or absurd value is replaced by 64).
  When a session's counted bytes reach the margin, one `[ReaderBudget]` line
  is written with `"event":"over_margin"`: `hash`, `file_index`, `size`,
  `swarm_bytes`, `cache_bytes`, `margin`, `open_age_ms`, `comm`, `pid`, `uid`
  and `"enforced":false`. It carries no path or file name. The line prefix
  and field names are meant to be stable. `swarm_bytes` and `cache_bytes`
  split the counted bytes by the read path that answered, for debugging:
  `swarm_bytes` waited for the engine (which can answer from pieces it
  already holds), `cache_bytes` came from the read-ahead cache. Neither is
  bytes downloaded; with the pump on, almost everything shows as
  `cache_bytes`.
- **Late confirmation.** If a webhook then confirms that file, a second line,
  `"event":"confirmed_late"`, carries `hash`, `file_index`, `size`,
  `swarm_bytes`, `cache_bytes` and `late_ms`: a playback that a budget would
  have cut. `flodni_unconfirmed_read_sessions_total` counts each crossed
  session once, when it closes.
- **`comm`, `pid`, `uid`** come from the FUSE request of the latest open of
  the file. `comm` is read from `/proc/<pid>/comm` when the line is written,
  and is empty when the pid is 0 or not visible (another PID namespace, or
  the process already exited). The opener is not always the process that
  reads.
- **`flodni_playback_webhooks_received_total{matched}`** counts play and
  resume webhooks for a movie or an episode; `matched="true"` when the
  webhook resolved to one file. Phase b's guard is designed to arm only once
  `matched="true"` has been seen since start; that guard is not built.

Limits: reads on a handle after its session closed and sessions open at
shutdown are not counted; reads the kernel page cache answers never reach
Flodni; a fuzzy webhook match or a state restored at start can mark a file
confirmed that is being scanned. A confirmation also outlives the playback:
it stays until a stop webhook, or until 15 minutes after the last handle of
the file closes, and a scan in that window is not counted.

## Warmup cache eviction

With the warmup cache on, a new head file that would pass the quota evicts
whole media files, head and tail together. Files never played go first, then
played files; each class goes oldest written first. This is built and runs
today. The cache also keeps an in-memory list of which files are warm; it is
internal and not served, and only the aggregates below are exported
(`flodni_warmup_files`). What is not built: warm on add (designed in
`docs/roadmap.md`), and evicting the least recently read file (an idea in
`docs/ideas.md`, not planned).

- **Played** means a playback confirmed by the media server webhook (Plex or
  Jellyfin), never a scanner's read. A webhook marks at most one file: a
  movie by file name, IMDB id or a unique title (and year); an episode by
  file name, or by a unique IMDB id that agrees with the file's season and
  episode, or by series title, season and episode together
  (`Metadata.parentIndex` and `Metadata.index`, the Plex names; a Jellyfin
  template must send the same fields, and that is not verified against a
  Jellyfin server). When several candidates fit equally, or an episode
  webhook carries no season and episode and no file name, no file is marked:
  a missed play costs one file its protection, a wrong mark would protect a
  season. Marking does not change which file the webhook starts playback
  for.
- **Protection expires.** A play protects its file for `played_protection_days`
  (default 90) counted from the last play; after that the file ranks as never
  played. `0` means a play protects forever. A negative value is logged at
  load and replaced by 90.
- **Limits.** Without a webhook nothing is ever played, so the order is
  oldest first. With the state database off (`enable_state_db`) the marks
  live in memory and are gone at restart. A play whose webhook arrives
  before the file is opened is not marked. When only played files remain,
  any read through the mount can evict the oldest played file, because a
  play's first reads cannot be told from a scan before the webhook arrives.
- **State.** The marks are in the state database table `file_state` (schema
  13): the last play and whether the quota evicted the file. Eviction
  removes bytes only; the readability record stays. A torrent removed from
  the library loses its marks, and a startup prune after a complete library
  scan drops the marks of torrents that left it.
- **Log line.** Each evicted file logs one line, `[WarmupEvict] ` followed by
  a JSON object: `hash` (full), `file` (index), `head_bytes`, `tail_bytes`
  (0 when that window was not on disk), `age_s` (since the newer window was
  written), `played` (inside its protection) and `trigger` (`head_create`
  today). It carries no path. The prefix and field names are meant to be
  stable.
- **Cold reopen.** `flodni_warmup_cold_reopen_total` counts a confirmed play
  on a session that opened with no ready head, of a file with an eviction
  mark; it counts once per eviction and clears the mark. `previously_played`
  says whether the file had been played before, expired or not.
  `flodni_playback_starts_total` counts the same confirmed starts (not
  playing to playing), the denominator for the rate. A start whose webhook
  resolves to no single file is still counted, but its cold reopen is not,
  so the rate is a lower bound. Neither counts a play whose webhook matches
  no open file.

## Metrics

`GET /metrics/prometheus` on the control port serves the Prometheus text
format 0.0.4, next to the JSON at `/metrics` that the control panel reads.
It follows the token rule of the other control routes: open when no token is
set, the bearer header only when one is (no `?token=`). It answers `GET`
only, with 405 and `Allow: GET` otherwise. A scrape reads in-memory counters
and never calls the torrent engine.

```yaml
scrape_configs:
  - job_name: flodni
    metrics_path: /metrics/prometheus
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/flodni-token
    static_configs:
      - targets: ["flodni:9080"]
```

Drop `authorization` when no token is set. The port has no TLS, so keep the
scraper on the same host or Docker network.

| Series | Type | Labels | Meaning |
|---|---|---|---|
| `flodni_build_info` | gauge | `version` | Always 1. |
| `process_start_time_seconds` | gauge | none | Start of the process. |
| `flodni_session_first_byte_seconds` | histogram | none | Open to the first byte served, per real playback. |
| `flodni_session_first_deep_read_seconds` | histogram | none | Open to the first read at 8 MiB or later or in the tail, per real playback. |
| `flodni_seek_read_duration_seconds` | histogram | none | Duration of the read that followed a seek. |
| `flodni_read_duration_seconds` | histogram | `source` | Duration of each served read. Its `_count` is the number of reads each path answered. |
| `flodni_read_bytes_total` | counter | `source` | Bytes handed to the kernel. |
| `flodni_sessions_closed_total` | counter | `outcome` | Closed sessions: `served`, `probe`, `unserved`, `dead_swarm`. |
| `flodni_dead_swarm_verdicts_total` | counter | `reason`, `source` | Dead-swarm verdicts; `reason` as `last_failure_reason`, `source` is `mount`. |
| `flodni_warmup_cache_bytes` | gauge | none | Approximate size of the warmup cache; only with the cache enabled. |
| `flodni_warmup_cache_quota_bytes` | gauge | none | Its size limit; only with the cache enabled. |
| `flodni_warmup_evictions_total` | counter | `window` | Files removed by quota eviction, `head` or `tail`. |
| `flodni_warmup_evicted_bytes_total` | counter | `window` | Their apparent size in bytes. |
| `flodni_warmup_files` | gauge | `window` | Media files with a warm window on disk now: `head` counts heads at or above the 1 MiB ready floor, `tail` counts tails fully covered. Only with the cache enabled. |
| `flodni_playback_starts_total` | counter | none | Playbacks the media server webhook confirmed (not playing to playing). |
| `flodni_playback_webhooks_received_total` | counter | `matched` | Play and resume webhooks for a movie or an episode; `matched` is `true` when the webhook resolved to one file, else `false`. |
| `flodni_unconfirmed_read_bytes_total` | counter | none | Bytes past the head and tail windows that readers took from files with no confirmed playback, whichever path served them. Not bytes downloaded. See Reader budget. |
| `flodni_unconfirmed_read_sessions_total` | counter | `outcome` | Sessions that crossed `unconfirmed_read_margin_mb`, each counted once when it closes: `confirmed_late` if a webhook confirmed the file afterwards, else `unconfirmed`. |
| `flodni_warmup_cold_reopen_total` | counter | `previously_played` | Confirmed plays that opened cold on a file quota eviction had removed, `true` or `false` by whether it had been played before. |
| `flodni_warmup_unverified_drops_total` | counter | none | Warmup blocks, head or tail, never written because their pieces did not verify in time. |
| `flodni_warmup_write_queue_drops_total` | counter | none | Head chunks dropped because the warmup write queue was full. |
| `flodni_readability_torrents` | gauge | `state` | Library torrents by readability: `failed` has a failure streak, `ok` has none and a read served by the swarm, `never_read` is the rest (no record, or a streak reset by a metainfo without a read). Not exported when the state database is off. |
| `flodni_readahead_cache_bytes` | gauge | none | Bytes in the read-ahead cache. |
| `flodni_readahead_cache_active_bytes` | gauge | none | The part that is not stale. |
| `flodni_readahead_cache_entries` | gauge | none | Entries in the cache. |
| `flodni_readahead_cache_budget_bytes` | gauge | none | The cache's size budget. |
| `flodni_fuse_short_reads_total` | counter | none | Reads served with fewer bytes than requested away from EOF. |
| `flodni_fuse_short_read_results_total` | counter | `result` | Outcome of the refill: `repaired` or `failed`. |
| `flodni_fetch_dedup_total` | counter | none | Concurrent fetches of one chunk that shared a single fetch. |

`source` is `warmup_head`, `warmup_tail`, `racache_hit` or `fetch_block`.
Every label value is exported at 0 from the start. Durations are in seconds
and the buckets are fixed (1 ms to 10 s). Series names and labels are meant
to be stable; buckets and help text are not.

- Counters reset when Flodni restarts; use `rate()` or `increase()`.
- `flodni_session_first_byte_seconds` and
  `flodni_session_first_deep_read_seconds` cover only sessions that reach
  the `served` thresholds above, not scanner probes, and a session counts
  when it closes. `flodni_seek_read_duration_seconds` is recorded at the
  read itself in any open session, scanner probes included.
- `source` names the path that answered a read, not where the bytes came
  from: a `racache_hit` may hold bytes the swarm delivered a moment ago.
- The dead-swarm series stay 0 when the state database is off; nothing is
  recorded then.
- Durations are truncated to whole milliseconds: reads under 1 ms land in
  `le="0.001"` and add nothing to `_sum`.
- `flodni_read_bytes_total` counts every served read, including reads after
  a session closed and reads with no session, so it differs from the
  `[Session]` lines.
- `flodni_warmup_cache_bytes` is a running total, resynced from the disk on
  each eviction walk. It can drift in between; the known causes are in
  `docs/ideas.md`.
- `flodni_warmup_files` reads the cache's in-memory index. A head removed
  behind the cache's back shows until the next lookup of it, a tail until
  the next restart. Warm bytes are `flodni_warmup_cache_bytes`. The series is a count of files, not of
  complete media files: a head counts once it holds 1 MiB, whatever the
  head window size.
- `flodni_readability_torrents` is recomputed every 60 s, so a scrape stays
  in memory. The library size is the number of distinct torrents
  looked up through the mount, so a new torrent counts once it appears
  there. Records of torrents that left the library are not subtracted (a
  startup prune removes them), and `never_read` stops at 0 rather than
  going negative.
- Engine and peer counters, read-ahead evictions and Go runtime metrics are
  not exported yet (`docs/ideas.md`).

## Viewing the model

`make check` validates the architecture model and `make view` serves it at
<http://localhost:8080/workspace/1>. Both need Docker. `make docs` checks
that the documentation agrees with the tree.

## Contributing

Pull requests are opened by the maintainer only. Issues are welcome; see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

GPL-3.0, see [LICENSE.md](LICENSE.md). Tiramisu is copyright Matteo
Rancilio; third-party components are listed in [NOTICE](NOTICE).
