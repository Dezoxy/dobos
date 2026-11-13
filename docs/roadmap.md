# Roadmap

What is being worked on, in order. Each step is one pull request, merged only
when every required check passes. Background and the full backlog are in
[ideas.md](ideas.md).

## Trim to the streaming core

Flodni keeps what streams a torrent through the FUSE mount: the virtual file
system and read pump, the embedded GoStorm engine, the library add and remove
API, the index cache, the playback webhook, the control panel and the
streaming metrics. Everything else is removed, starting with what runs without
a switch or calls out to third parties.

1. **Stop calling home and close open endpoints.** [FINISHED] Remove telemetry
   (on by default, posts to the upstream project's endpoint), the update check
   (polls the upstream repository), the MCP server (unauthenticated, always on)
   and the `net/http/pprof` import (unauthenticated debug endpoints).
2. **Remove the dashboard.** [FINISHED] `/dashboard` and its API routes, the
   collector (polls every 5 s, looks up the public IP, broadcasts on the LAN)
   and the macOS toolbar app that wraps it.
3. **Remove dead and switched-off code.** [FINISHED] NAT-PMP (and `iptables` in
   the image), the IP blocklist, index seeding, the Samba watchdog,
   `internal/preload`, `internal/syncer/statestore`, `Config.EngineScripts` and
   the configuration keys nothing reads.
4. **Remove upstream packaging.** [FINISHED] `docker-windows/`, `install.sh`,
   `tiramisu.service`. The container image is the only supported install.
5. **End-to-end streaming test.** [FINISHED] A local tracker seeds a small
   torrent, Flodni mounts it and the test reads the file back and compares
   hashes. It must pass before steps 7 and 8, which touch the read path.
6. **Keep private torrents private.** [FINISHED] A library add without a magnet
   merges public UDP trackers into every release, private or not, and GoStorm's
   default retracker mode appends public trackers to every torrent; a DHT
   announce may also start before the private flag is known. Fix all three and
   pin them with unit tests at those seams. The end-to-end test's private
   fixture then guards that streaming still works.
7. **Remove catalogue sync.** [FINISHED] The scheduler, the movie, TV and
   watchlist engines, the TMDB, Torrentio and Rotten Tomatoes clients, the
   built-in Prowlarr client, their control panel cards and `Config.LogDir`,
   which only the engines' log files use.
8. **Remove music and audiobooks.** [FINISHED] `internal/musicimport`,
   `cmd/musicimport`, `internal/audio` and the audio parts of
   `internal/library`, `internal/metadb`, `startup.go` and `main.go`, then
   `internal/prowlarr`, `Config.Prowlarr` and `music_discovery`, which only the
   music import still uses.

## Own identity and must-fix bugs

1. **Rename to Flodni.** [FINISHED] Go module, binary, image default paths,
   `FLODNI_*` environment variables with the old names kept as aliases.
   Persisted file names stay, so existing state carries over.
2. **Keep working stubs when a pack replaces them.** [FINISHED] Replacing a pack
   deletes stubs that played before the new release is known to be readable
   (`internal/library/manager.go`). A pack still wins, but the old stubs go only
   once the new ones are confirmed readable.

3. **Atomic writes and bounded servers.** [FINISHED] Stub and config writes go
   through a temporary file, sync and rename; the control HTTP server gets read,
   write and idle timeouts.

Releases and deployment are scheduled separately.

## Maintainability

1. **Clear the dependency backlog.** [FINISHED] Merge or supersede the open
   Dependabot pull requests, align the vendored engine's `go.mod` with patched
   versions, and run the engine's private-torrent tests in CI.
2. **End-to-end coverage for replacement.** [FINISHED] A second fixture filed
   over the first as the same TV episode, so the readability check before a
   replacement runs against the real engine.
3. **Break up `main.go`.** [FINISHED] Phase a: split it into files by concern in
   the same package, as a pure move. Phase b: move the read-ahead cache into its
   own package with unit tests, and give the HTTP routes their own mux. The
   `App` struct that replaces the shared package-level maps waits for tests on
   the read path (see ideas).
4. **Make failures visible.** [FINISHED] Phase a: triage every unchecked error,
   fix the ones that hide failures, and gate new ones with errcheck. Phase b:
   report sessions where a file was opened but never served bytes through the
   API. Structured logging with `slog` waits (see ideas).

## Hardening

1. **Opt-in token on the control API.** [FINISHED] `api_token` or
   `FLODNI_API_TOKEN` enables bearer-token auth on every control route except
   the static panel page; webhooks may pass the token as a query parameter.
   Unset keeps today's open API.
2. **Unit tests on the read path.** [FINISHED] Cover the read decision tree in
   `handle.go` and the pump's decision helpers, the prerequisite for the `App`
   struct.
3. **Stubs land where the mount serves them.** [FINISHED] The library writes
   stubs under the configured `physical_source_path` while FUSE serves the path
   given on the command line; make them one value, or refuse to start when they
   differ.
4. **Bound and protect GoStorm's server.** [FINISHED] Read-header and idle
   timeouts on port 8090, and the same opt-in token, since it exposes torrent
   control and shutdown.

## Reliability

1. **A health endpoint.** [FINISHED] `GET /api/health` reports the mount, the
   engine and the state database, each with a status and a reason, and the
   container health check uses it instead of `mountpoint` alone.
2. **A shutdown deadline.** [FINISHED] Shutdown saves state and unmounts within
   a bounded time and exits even when the unmount hangs, so a restart cannot
   block forever.
3. **Reads against slow seeders.** [FINISHED] Reproduce in the end-to-end test
   the stalls seen against a seeder with a shallow request pipeline, then fix
   the leecher's request timeouts so a sequential read completes.
4. **Characterization tests for the library.** [FINISHED] Pin stub naming, stub
   contents and one-stub-per-episode filing for movies, episodes and season
   packs, so later restructuring cannot change them unnoticed.
5. **An interface for the native bridge.** [FINISHED] The read path calls the
   engine through an interface instead of the concrete client, and tests cover
   the fetch, prefetch and pump branches that only the end-to-end test reaches
   today. This is the last prerequisite for the `App` struct.
6. **No stall after a tail probe.** [FINISHED] A read that follows a tail
   read stalled for about 34 s, and in some runs about 58 s, in the
   end-to-end test. Both causes were in the cache and are fixed (see the
   Done list); what the engine still leaves open is in
   [ideas.md](ideas.md).
7. **Say when no play webhook arrives.** [FINISHED] Three features act only
   on a playback the media server's webhook confirmed: played-file
   protection, the reader budget's refusal and the keep-alive below.
   Without a webhook configured and reachable they silently do nothing.
   Flodni logs at start whether a play webhook has matched and which of the
   built features are active, and warns when readers play through the mount
   but no play webhook has matched for `playback_webhook_warn_days`: a log
   line, `warnings` in `/api/health` (advisory, never a status change) and
   `flodni_warning{name="no_play_webhook"}`. The README's "Play webhook"
   section says plainly that these features need the webhook. The keep-alive
   is not part of this step; it stays designed, in step 8.
8. **Keep a playing torrent connected.** The engine closes a torrent that
   has had no reader for its idle timeout (`TorrentDisconnectTimeout`,
   15 s by default), which keeps idle titles cheap: they neither leech nor
   seed. A media server reads in bursts, and a direct play or a throttled
   transcoder can wait longer than that between reads. The torrent then
   closes and the next read reopens it, with a new announce and a new
   search for peers; on a swarm of a few seeders each reopening costs
   seconds. A play webhook already marks the torrent as not to expire, but
   the mark sits on the engine's torrent object: it is set only if the
   torrent is loaded when the webhook arrives, and a reopened torrent comes
   back without it. Phase a is built (2026-11-13) and changes no
   behaviour: it counts the engine's idle closes, the reopens of a closed
   torrent for a read, and the reads slower than one second, each split by
   whether a file of the torrent had a webhook-confirmed playback, and adds
   them to the session record. Phase b is designed, not built: while a file has a
   confirmed playback its torrent is not closed for idleness. The hold is
   decided at the moment of expiry from the playback state, so it survives
   a reopening. A stop webhook ends it at once; otherwise it ends 15
   minutes after the last read or play or resume webhook
   (`playback_keepalive_minutes`, one off switch), and a pause does not
   extend it. At most 4 torrents are held at once (a config key; the cap
   times the engine's per-torrent cache must stay well under the memory
   limit), and a torrent over the cap falls back to today's idle close.
   Releases are counted by reason and losses to the cap are counted. It is
   on by default, and the effective settings are logged at start next to
   the webhook status of step 7. A reader with no confirmed playback, a
   scan, warm on add and catch-up keep today's idle close, and a held
   torrent gets no tracker or peer source it would not have had. Entry
   condition for phase b: an end-to-end test that sends webhooks shows a
   confirmed playback whose torrent closes for idleness today.

## Readability evidence

Flodni records facts about bytes: did a file read, is it cached, who reads
it. Which release to replace, and when, is decided by tools outside it.
These steps do not touch the read path and do not wait for Reliability
step 6.

1. **Record the decision.** [FINISHED] An ADR: Flodni fills and protects its own
   cache and records whether its files read; choosing and replacing releases
   stays outside. It weighs an external reader through the mount, warming in
   process and warming on the media server's first scan, and names what outside
   tools may rely on. Everything in it is labelled designed until the step that
   builds it lands.
2. **A readability record over the API.** [FINISHED] The state database already
   counts sessions that ended with a failed read and nothing served, per
   torrent, but no endpoint reads it back. Add the time of the last read the
   swarm served and the reason and source of the last failure, keep the rule
   that only bytes from the swarm clear it, and serve it read-only at `GET
   /api/torrents/health` with the full hash, so outside tools stop parsing logs
   and stub names. Rows of torrents that left the library are pruned at startup,
   the endpoint answers 503 when the state database is off, and the README
   documents the fields as the supported contract.
3. **One log line per closed session.** [FINISHED] A JSON line with the hash,
   file index, path, outcome, bytes served by serving path, whether the swarm
   served a read, time to first byte and duration, so a log store keeps what the
   200-entry ring behind `/api/sessions` forgets. The existing `[DeadSwarm]` and
   `[TTFF]` lines stay as they are.
4. **Scrapeable metrics.** [FINISHED] Prometheus text exposition on its own
   path, next to the JSON the panel reads: the time-to-first-byte histograms,
   dead-swarm verdicts, warmup cache hits by source, cache bytes and evictions.
   Later steps add their own series in their own pull requests.

## Readiness and protection

A file should start from the SSD the first time it is opened, and a reader
that is not playback should not pull a whole file through the swarm. These
steps build on index mode (`warmup_dir` set, `pump_on_open` off), where
only hash-checked data reaches the cache and tail coverage survives a
restart. Steps 2 and 3 waited on Reliability step 6, which is now
finished.

1. **Honest cache state and eviction.** [FINISHED] Phase a: the cache
   remembers per file when a playback confirmed by the media server's
   webhook last played it, and whether the quota evicted it; the state
   database keeps both across restarts. A scanner's read does not mark a
   file played, and one webhook marks at most one file (an episode only when
   series title, season and episode all match; with several equally good
   candidates, none). A play protects its file for 90 days from the last play
   (`played_protection_days`); after that it ranks as never played. Eviction
   takes a file's head and tail together: files never played first, oldest
   written first, then played files, oldest first. It removes bytes only;
   the readability record stays. Each eviction is logged, and two counters
   report how many plays started on a file the quota had evicted, out of how
   many plays. Phase b: one internal call lists which files have their head
   and tail windows on disk now, without a `stat` per file. It is not
   served; it feeds a gauge of warm files by window, next to the cache
   bytes, quota and evictions. A gauge of torrents by readability (ok,
   failed, never read) is built on the readability record. Evicting the
   least recently read file is not built: it waits for those counters (see
   ideas).
2. **Warm on add.** [FINISHED] After a library add returns, a background
   queue with a small fixed worker pool fills the head and tail windows of
   each new file through the verified path, straight from the engine and not
   through the mount. At a full quota it may evict only files never played,
   oldest first, and it checks for room before it fetches; with none left,
   or with no evidence of plays (state database off, or no play ever
   recorded), it drops the new file and counts it. It is off by default
   (`warm_on_add`), pauses while a playback session is open and records each
   outcome per file in the readability record; only pieces the swarm
   delivered count as success. The end-to-end test (`warm-on-add`) adds a
   fixture with the switch on and reads nothing through the mount, waits for
   the fill, checks the metric, the `[Warm]` line, the per-file row and the
   torrent record, stops the seeder and reads both windows back from the
   SSD against the seeder's hashes. Pause, wake of a closed torrent and
   removal while queued rest on unit tests (see ideas).
3. **Catch-up and retries.** [FINISHED] A pass one pace interval after
   start, then every 6 hours, feeds the library files the warmer has never
   warmed to it, at a configurable pace, so a bulk import is worked off
   across restarts without a persistent queue. Catch-up never evicts: at
   a full quota it stops and counts what it left. A file the cache has
   evicted is not warmed again; it is left to its next play. A failed warm
   is retried no sooner than most of a day later and a capped number of
   times; after that the record keeps the evidence and the warmer leaves the
   file alone. The end-to-end test (`catch-up`) adds a fixture with the switch
   off and sees no catch-up, turns it on and restarts the daemon, waits for the
   pass to warm the title with no read through the mount, restarts again and
   sees a pass with nothing to do, then stops the seeder and reads both windows
   back from the SSD. The quota stop, the exclusion of evicted files, the
   overwrite of a stale eviction mark and the 20-hour rule rest on unit tests.
4. **A byte budget for readers that are not playback.** [FINISHED] Phase a is
   built: Flodni counts the bytes a file serves, from the engine or the
   read-ahead cache, to a session with no confirmed playback, past its two
   windows, and logs each session once when it crosses a margin
   (`unconfirmed_read_margin_mb`); it measures only and refuses nothing.
   Phase b is built, opt-in and off by default (`refuse_unconfirmed_reads`):
   past the budget it stops fetching from the swarm for such a reader and
   serves only what is cached; a refused read waits 15 s for a webhook, then
   fails with EIO, and the pump is parked, not cancelled. It is tested against
   a fake engine and end to end (`budget-refusal`, below); the pump park is
   fake-engine only. The budget lifts as soon as a session registers, applies
   only once a matched playback webhook has been seen since start
   (`flodni_playback_webhooks_received_total{matched="true"}`), and a refusal
   never counts as a failed read in the readability record. Entry condition
   for phase b: the end-to-end test sends a play webhook in the middle of an
   unconfirmed read that has crossed the margin, and shows the read
   completes, `flodni_unconfirmed_read_sessions_total{outcome="confirmed_late"}`
   counts it and `flodni_playback_webhooks_received_total{matched="true"}`
   counts the webhook. That condition is met: the end-to-end variant
   `late-confirm` does exactly that. The end-to-end variant `budget-refusal`
   runs the refusal against the real mount: a scan is cut with EIO at the
   windows plus the margin (20 to 32 MiB of a 150 MiB file), with one
   `refused` line and no failed read, the seeder is asked for nothing more,
   a late play lifts the hold, and a stop gives the next reader a fresh
   margin; while a reader is held, other opens and reads of the file answer
   in under 1 s. The pump park rests on fake-engine tests only. The step is
   finished; the switch stays off by default, and the owner turns it on after
   reading `late_ms` and `matched="false"` on the host.

## Before a deployment relies on it

These came out of reviews of the removal path, the cache directory and the
file mapping. Steps 1 to 4 should land before a deployment depends on
Flodni; steps 5 to 7 can land at any time. All are designed, not built.

1. **One wake path, and removals that stay removed.** The mount's open and
   its read-time rewake wake a closed torrent from a bare magnet without
   checking that the engine still stores it; only warm on add's
   `WakeStored` checks. A handle left open across a removal therefore
   re-adds the hash on its next read, with no stored metainfo, so a private
   torrent gets DHT and PEX until its metainfo arrives (hard rule 3), and a
   removed torrent can be stored again. All wakes go through one path that
   refuses a hash the engine no longer stores, and a removal racing a wake
   leaves the torrent neither loaded nor stored. Tests remove a batch of
   torrents with fakes and injected ordering while wakes, reads and fills
   run, and one end-to-end run removes several titles back to back while
   another is read. Unlinking one episode of a pack through the mount
   removes the whole torrent today; it should follow the same reference
   check as a library removal.
2. **A missing cache disk means no cache.** With `warmup_dir` on a
   separately mounted disk that is absent at start, Flodni creates the
   directory on the filesystem underneath and fills it to the quota, and it
   re-creates the directory at runtime if it vanishes. Flodni warns when
   `warmup_dir` is set but the cache did not start, never re-creates the
   directory after start, does not count a failed deletion as an eviction,
   and says once that a read-only directory means no cache. The README
   names the deployment side: make the start depend on the mount, keep the
   empty mountpoint unwritable, and use a journalling filesystem.
3. **Never serve another file's bytes.** An open trusts the stub's file
   index when a warm head exists under it, and otherwise resolves the file
   by size and name, so a stub whose index is stale can be served from the
   wrong file. Flodni's own stubs resolve to themselves; a stale index needs
   a stub from elsewhere or a rare tie in the file order. First a counter
   for opens where the resolved index differs from the stub's, then guards:
   the warmer refuses a file whose engine length differs from the stub's
   size, and the warm shortcut checks the resolved file when the torrent is
   loaded. File ids come from the engine's own file list, and hashes are
   lower-cased.
4. **Align the tail window to a page.** The kernel reads whole 4 KiB pages,
   so a read of exactly the last N MiB of a file whose size is not
   page-aligned starts a page before an N MiB tail window, and that page
   comes from the engine instead of the cache. The end-to-end test measured
   12 KiB so. The tail window's start is rounded down to a page boundary.
5. **End-to-end variants as parallel jobs.** [FINISHED] CI runs each variant
   on its own runner, so wall time stays near the longest variant as variants
   are added, and variants can no longer collide on container names.
4. **Align the tail window to a page.** [FINISHED] The kernel reads whole
   4 KiB pages, so a read of exactly the last N MiB of a file whose size is
   not page-aligned starts a page before an N MiB tail window, and that page
   came from the engine instead of the cache. The end-to-end test measured
   12 KiB so. The tail window's start is rounded down to a page boundary
   (`warmup.TailStart`, used by the writers, the readers, the fills and the
   reader budget), so the window grows by up to 4095 bytes. Covers written
   before (exactly the configured size from the end) still load and serve
   their own window as complete; they lack the leading page, which comes from
   the engine as before.
5. **End-to-end variants as parallel jobs.** CI runs each variant on its
   own runner, so wall time stays near the longest variant as variants are
   added, and variants can no longer collide on container names.
6. **Free cache space on request.** The quota is read once at start, and a
   lower quota evicts only when a new head is written. A call evicts down to
   a given size now, in the normal order: never-played files first, oldest
   first, and played files only after them.
7. **Remove by the full hash.** [FINISHED] Removal by hash matched stub names
   on eight hex digits; stubs from older tools carry the first or the last
   eight, so some were missed, and a stub of another torrent could match. It
   matches the full hash in the stub's own link instead, as do the
   already-present check of an add and the reference check before a torrent is
   dropped.

## Done

- 2026-11-11, trim step 1: telemetry, the update check, the MCP server and
  the pprof endpoints are removed.
- 2026-11-11, trim step 2: the dashboard, its collector and LAN discovery,
  and the macOS toolbar app are removed. Done before the dead-code step
  because the collector read NAT-PMP and blocklist state.
- 2026-11-11, trim step 3: NAT-PMP and `iptables` in the image, the IP
  blocklist, index seeding, the Samba watchdog, the never-filled preload
  cache, `syncer/statestore`, `EngineScripts` and seven unread
  configuration keys are removed.
- 2026-11-11, trim step 4: the Windows installer, `install.sh` and the
  systemd unit are removed; the container image is the only install.
- 2026-11-11, trim step 5: `make e2e` and the CI image job seed a private
  torrent locally and read it back through FUSE in two configurations.
- 2026-11-11, trim step 6: private torrents get no public trackers from the
  library, GoStorm's retrackers or a stored spec on wake, and the engine
  knows a torrent is private before its DHT announcer can run.
- 2026-11-11, trim step 7: the scheduler, the movie, TV and watchlist
  engines, the TMDB, Torrentio and Rotten Tomatoes clients, the sync caches,
  their control panel cards and `Config.LogDir` are removed. The Prowlarr
  client and `prowlarr` config key stay for `cmd/musicimport` until step 8;
  the control panel no longer exposes them. The GoStorm client moved into
  `internal/library`.
- 2026-11-11, trim step 8: music and audiobooks (import, cue splitting,
  audio library API, projections and their FUSE paths), the Prowlarr client
  and the music-only configuration are removed. Existing `music/` and
  `audiobooks/` folders stay on disk; the mount lists them as directories
  without their audio files. Trim to the streaming core is complete.
- 2026-11-11, own identity step 1: the module, binary, image paths and
  environment variables are named Flodni; old names still work.
- 2026-11-11, own identity step 2: a release that replaces existing stubs
  must deliver data first; if it does not, the existing files stay.
- 2026-11-11, own identity step 3: stub and config writes are atomic, and
  the control server has read, write and idle timeouts.
- 2026-11-11, maintainability step 1: the vendored engine's `go.mod` is
  aligned with patched versions (gRPC, OpenTelemetry, x/crypto, websocket),
  its private-torrent tests run in `make go-test` and CI, and the Dependabot
  backlog is merged or superseded.
- 2026-11-11, maintainability step 2: the end-to-end test files a second
  release over a TV episode and a third that never delivers, checking that
  the first replaces and the second is refused.
- 2026-11-11, maintainability step 3a: `main.go` is split into nine files
  by concern (FUSE nodes, handle, pump, read-ahead cache, playback, webhook,
  profiling, inode map, wiring); a line-multiset check proves no code
  changed.
- 2026-11-11, maintainability step 3b: the read-ahead cache is
  `internal/readahead` with unit tests, and the control routes have their own
  mux; `/debug/vars` is no longer served.
- 2026-11-11, maintainability step 4a: 43 unchecked errors that could hide
  failures now return or log; the rest are marked intentional, and errcheck
  gates new ones in `make go-check` and CI.
- 2026-11-11, maintainability step 4b: `GET /api/sessions` lists open
  sessions and the last 200 closed ones, each with bytes served, time to first
  byte and an outcome (`served`, `probe`, `dead_swarm`, `unserved`).
- 2026-11-11, hardening step 1: opt-in bearer-token auth on the control API;
  the panel asks for the token, webhooks may pass it in the URL.
- 2026-11-11, hardening step 2: table tests pin the pump and handle decision
  helpers, and unit tests cover the read path's cache hit, short-read repair
  and release; handle.go and pump.go coverage went from 0.6% to 32.0%.
- 2026-11-11, hardening step 3: the library writes stubs under the path the
  mount serves; a different path in config.json is logged and overridden.
- 2026-11-11, hardening step 4: GoStorm's server on 8090 has read-header and
  idle timeouts and honours the same opt-in token (except `/echo`).
- 2026-11-11, reliability step 1: `GET /api/health` reports the mount, engine
  and state database, and the image's health check runs `flodni healthcheck`
  against it.
- 2026-11-11, reliability step 2: shutdown saves state, then unmounts with a
  lazy detach after 5 s and exits by 8 s at the latest, inside Docker's
  default 10 s grace period.
- 2026-11-11, reliability step 3: the engine's upload side waited on a request
  it could not yet allocate buffer for while granted requests went unread, so a
  seeder at the default 1 MiB per-peer allowance sent nothing until the
  leecher's ~35 s timeout; it now skips ungranted requests and restarts on a
  release; the end-to-end test gains a slow-seeder variant.
- 2026-11-11, reliability step 4: characterization tests pin stub naming,
  stub contents, movie, episode and season-pack filing, replacement and
  removal; internal/library coverage went from 48.1% to 68.9%.
- 2026-11-11, reliability step 5: the read path reaches the engine through an
  interface, and tests with a fake engine cover fetch, singleflight, prefetch
  and the pump; handle.go and pump.go coverage went from 32% to 80%.
- 2026-11-12, readability evidence step 1: ADR 0002 records that Flodni
  fills and protects its cache and records readability while the choice of
  release stays outside; what is not built yet is labelled designed.
- 2026-11-12, readability evidence step 2: `GET /api/torrents/health` serves
  the readability record (state database schema 12): consecutive failures,
  first and last failure, reason and source of the last failure and the
  close time of the last swarm-served read. A success keeps the row and
  resets the count. Rows of torrents that left the library are pruned at
  startup after a complete library scan; README documents the fields.
- 2026-11-12, readability evidence step 3: Flodni logs one JSON line per
  closed session with bytes by serving path and whether the swarm served a
  read (`swarm_served`), plus the file index and failure reason; the same
  fields join `/api/sessions`. The bytes are split by the path that
  answered, not into swarm and cache. README documents the line.
- 2026-11-12, readability evidence step 4: `GET /metrics/prometheus` serves
  the time-to-first-byte and read histograms, read counts and bytes by
  serving path, session outcomes, dead-swarm verdicts, warmup cache size,
  evictions and drops, behind the control token. The README lists the series.
- 2026-11-12, readiness step 1, phase a: the cache keeps per file the time of
  the last play a media server webhook confirmed and whether the quota
  evicted it (state database schema 13). Eviction takes head and tail
  together, files never played first, then played files, oldest first; a
  play protects for `played_protection_days` (default 90) from the last play.
  Each eviction logs one `[WarmupEvict]` line, and
  `flodni_playback_starts_total` and `flodni_warmup_cold_reopen_total`
  report plays and plays that opened on an evicted file. The per-file listing
  and its gauges (phase b), warm on add and catch-up are not built.
- 2026-11-12, readiness step 1, phase b: the cache keeps an internal
  in-memory list of which files have their head and tail windows on disk (not
  served), and a tail of a file shorter than the tail window can now complete.
  `flodni_warmup_files{window}` counts warm heads and fully covered tails, and
  `flodni_readability_torrents{state}` counts library torrents that read, that
  failed and that were never read, refreshed every 60 s. A head recreated at
  offset 0 over a stale size entry no longer leaves a hole. Warm on add and
  catch-up are not built.
- 2026-11-12, readiness step 2, phase a: the per-file warm record. The engine
  stamps each piece with the time it completed (RAM storage, so a stamp is a
  swarm delivery), and a read through the mount that leaves a file's head
  and tail windows complete records the file in the state database (schema
  14) as `warmed` with the delivery time, or `cached` when no stamp exists.
  `GET /api/torrents/health` serves the records as an experimental `files`
  array, and `flodni_warm_fills_total{trigger="mount"}` counts them. Nothing
  fills windows on its own yet: the warmer, its `warm_on_add` setting and its
  outcomes are not built.
- 2026-11-12, readiness step 2, PR 3b: the warmer. With `warm_on_add` (off by
  default, needs `warmup_dir`) a library add that wrote stubs queues each new
  file; two workers fill its head and tail windows in the background, pause
  while a playback is open, wake a closed torrent only if the engine still
  stores it, evict only never-played files and only with evidence of plays, and
  record each outcome per file (`present`, `failed`, `dropped_quota`,
  `dropped_no_evidence`, `evicted`, `lost`, `write_failed`; `warmed` also stamps
  `last_swarm_read_at`). Cache-side failures retry in memory at 10 minutes, 1
  hour and 6 hours. New series: `flodni_warm_fills_total{trigger="add"}`,
  `flodni_warm_fill_bytes_total`, `flodni_warm_cache_failures_total`,
  `flodni_warm_queue_files`, `flodni_warm_paused`. Tested against fakes; the
  end-to-end test (PR 4) is not written, so step 2 is not finished.
- 2026-11-13, readiness step 2, PR 4: the end-to-end variant `warm-on-add`
  runs the warmer against the real engine and the real FUSE mount (a 16 MiB
  head window and an 8 MiB tail window). Before any mount read the warm fill
  is counted as `warmed` and `flodni_read_bytes_total` is 0; after the seeder
  is killed an 8 MiB head read and a 4 MiB tail read match the seeder's
  sha256 and are served from `warmup_head` and `warmup_tail` only. Quota
  pressure, the pause and the wake of a closed torrent, and removal while
  queued stay on unit tests. Step 2 is finished.
- 2026-11-13, readiness step 3, PR A: the seams of catch-up in the warmer, with
  no caller: a room-only fill that asks the cache for room before anything is
  fetched, a per-file trigger so a catch-up file never evicts, the feeder that
  hands one title at a time to an idle warmer, and a refusal the feeder can tell
  from a busy engine.
- 2026-11-13, readiness step 3, PR B: catch-up is wired. With `warm_on_add`,
  `warmup_dir` and the state database in place, a pass one pace interval after
  start and every 6 hours lists the stubs and feeds the files that are not warm
  on disk, not evicted and not failing, newest first, at `warm_catchup_per_hour`
  (default 20, 0 off, read on each feed). It never evicts and ends the pass at
  a full cache; a failed file waits 20 hours and is given up after 4 failed
  attempts, computed from the record with no new state. New series:
  `flodni_warm_fills_total{trigger="catchup"}`,
  `flodni_warm_fill_bytes_total{trigger="catchup"}` and
  `flodni_warm_catchup_files{state}`. Tested against fakes; the end-to-end
  variant (PR C) is not written, so step 3 is not finished.
- 2026-11-13, readiness step 3, PR C: the end-to-end variant `catch-up` runs
  catch-up against the real engine across daemon restarts. With `warm_on_add`
  off nothing is logged and every series is 0. After `config.json` is
  rewritten and the daemon restarted, the first pass warms the title: one
  `trigger="catchup"` fill, every `trigger="add"` outcome 0, bytes above 0,
  `flodni_read_bytes_total` 0, one `[Warm]` line and a `warmed`, `cached` row.
  A second restart finds `pending` 0, feeds nothing and leaves the row alone;
  with the seeder killed, the windows read back from the SSD match the
  seeder's sha256. The quota stop, the eviction exclusion, the stale-mark
  overwrite and the 20-hour rule stay on unit tests. Step 3 is finished.
- 2026-11-12, readiness step 4, phase a: Flodni counts bytes past the head and
  tail windows that the engine or the read-ahead cache serve to a session with
  no confirmed playback, logs each session once at the margin
  (`unconfirmed_read_margin_mb`, default 64) and exports
  `flodni_unconfirmed_read_bytes_total`,
  `flodni_unconfirmed_read_sessions_total` and
  `flodni_playback_webhooks_received_total`. Counting only; refusal (phase b)
  is designed, not built.
- 2026-11-12, reliability step 6, part one: the 34 s stall after a tail probe
  is fixed in the cache. `torrstor` raised piece priorities but never dropped
  them on completion, so stale complete pieces used the engine's storage cap
  and the wanted piece was never requested; `MarkComplete` now drops the
  priority. 0 of 38 e2e runs stalled at 34 s, against 15 of 17 before. A 58 s
  peer-drop stall remains open.
- 2026-11-12, reliability step 6, part two: the 58 s stall is fixed in the
  cache. When the last piece completed, the capped cache briefly held every
  piece (it evicted at most once a second), so the engine advertised every
  piece and the seeder closed the connection as mutually complete; with
  that seeder the only peer and tracker, the reader waited for the next
  announce, a minute later. `MarkComplete` now evicts down to capacity
  before the engine records the piece. 0 of 40 `default` runs stalled,
  against 4 of 24 before.
- 2026-11-13, readiness step 4, entry condition for phase b: the end-to-end
  variant `late-confirm` (`make e2e`) holds a full read of the fixture half
  way once it has crossed an 8 MiB margin, posts a play webhook for the file
  in the middle of the read, and lets the read finish. The sha256 matches,
  `flodni_playback_webhooks_received_total{matched="true"}` counts the
  webhook, and when the session closes
  `flodni_unconfirmed_read_sessions_total{outcome="confirmed_late"}` counts
  it, with the `confirmed_late` `[ReaderBudget]` line. Test only; phase b is
  designed, not built.
- 2026-11-13, reliability step 7: Flodni says when no play webhook arrives. The
  state database (schema 15, `daemon_marks`) keeps the last matched webhook, the
  last unmatched webhook and the last unconfirmed reader session, written at
  most hourly. At start a `[Webhook]` line states the last matched webhook,
  whether played-file protection has a recorded play and whether the reader
  budget refusal is on. When a reader session closed unconfirmed within
  `playback_webhook_warn_days` (default 7, 0 = off) and no play webhook matched
  in that time, a `WARNING: [Webhook]` line (marked as possibly false when
  every caller was a known hasher or analysis tool), `warnings` in
  `/api/health` (never changes the status or the HTTP code) and
  `flodni_warning{name="no_play_webhook"}` report it. The README has a "Play
  webhook" section. The keep-alive remains designed, not built.
- 2026-11-13, reliability step 8, phase a: `flodni_torrent_idle_closes_total`,
  `flodni_torrent_reopens_total` and `flodni_read_stalls_total`, each split
  by `confirmed`, and `reopens`, `stalls`, `max_stall_ms` and `confirmed` on
  the session record. No behaviour change. Phase b (the hold) stays designed,
  not built, so the step is not finished. Its entry condition is met: the new
  keep-alive end-to-end variant asserts the known leak, a confirmed idle
  close after a webhook that arrived while the torrent was closed
  (`idle_closes{confirmed="true"}` at least 1). Phase b flips that
  assertion to 0.
- 2026-11-13, bug fix: a new open of a file no longer waits for a slow read
  of that file. The mount returned open flags 0, so the kernel dropped the
  file's cached pages at every open and waited for each page a read in flight
  held (a 15 s budget hold, a 30 s slot wait, fetch retries). Open now returns
  `FOPEN_KEEP_CACHE` when another handle of the path is live; the first open
  still drops the cache. Unit tests pin the flag; the real-kernel check is the
  `budget-refusal` variant (item 9: opens and reads under 1 s during a hold).
- 2026-11-13, readiness step 4: the opt-in refusal is tested end to end. The
  variant `budget-refusal` (`make e2e`) cuts a scan with EIO at the windows plus
  the margin after a 15 s hold, with no failed read and no dead-swarm verdict,
  asks the seeder for nothing more, lifts the hold with a late play, gives the
  next reader a fresh margin after a stop, and keeps other opens and reads of
  the file under 1 s while a reader is held. The test found that a held read
  froze every new open of the file; #133 fixed it. The pump park rests on
  fake-engine tests only.
- 2026-11-13, readiness step 5: CI runs each end-to-end variant on its own
  runner (a matrix after the image job, which hands over both images as an
  artifact), and one job named `End-to-end` fails if any variant failed; it is
  the check to require.
- 2026-11-13, before a deployment relies on it, step 4: the tail window's start
  is rounded down to a 4 KiB boundary, so a read of exactly the last N MiB of
  a file that is not page-aligned stays inside the window. Existing tail covers
  keep loading. The `warm-on-add` end-to-end variant is back to a 4 MiB tail
  window with the same assertion: 0 bytes through `fetch_block` for the 4 MiB
  tail read.
- 2026-11-13, before a deployment relies on it, step 7: removal by hash
  (`POST /api/library/remove`), the already-present check of an add and the
  reference check before a torrent is dropped match the full 40-hex hash in each
  stub's own link, not the eight digits in its name. Legacy stubs tagged with
  the first or the last eight are found in either folder, and a stub of another
  torrent that shares the digits is left alone. The walk reads every stub under
  the folder (a few hundred bytes each); a stub with no readable hash is skipped
  and logged once per walk.
