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

1. **Stop calling home and close open endpoints.** Remove telemetry (on by
   default, posts to the upstream project's endpoint), the update check (polls
   the upstream repository), the MCP server (unauthenticated, always on) and
   the `net/http/pprof` import (unauthenticated debug endpoints).
2. **Remove the dashboard.** `/dashboard` and its API routes, the collector
   (polls every 5 s, looks up the public IP, broadcasts on the LAN) and the
   macOS toolbar app that wraps it.
3. **Remove dead and switched-off code.** NAT-PMP (and `iptables` in the
   image), the IP blocklist, index seeding, the Samba watchdog,
   `internal/preload`, `internal/syncer/statestore`, `Config.EngineScripts`
   and the configuration keys nothing reads.
4. **Remove upstream packaging.** `docker-windows/`, `install.sh`,
   `tiramisu.service`. The container image is the only supported install.
5. **End-to-end streaming test.** A local tracker seeds a small torrent,
   Flodni mounts it and the test reads the file back and compares hashes. It
   must pass before steps 7 and 8, which touch the read path.
6. **Keep private torrents private.** A library add without a magnet merges
   public UDP trackers into every release, private or not, and GoStorm's
   default retracker mode appends public trackers to every torrent; a DHT
   announce may also start before the private flag is known. Fix all three
   and pin them with unit tests at those seams. The end-to-end test's
   private fixture then guards that streaming still works.
7. **Remove catalogue sync.** The scheduler, the movie, TV and watchlist
   engines, the TMDB, Torrentio and Rotten Tomatoes clients, the built-in
   Prowlarr client, their control panel cards and `Config.LogDir`, which only
   the engines' log files use.
8. **Remove music and audiobooks.** `internal/musicimport`,
   `cmd/musicimport`, `internal/audio` and the audio parts of
   `internal/library`, `internal/metadb`, `startup.go` and `main.go`, then
   `internal/prowlarr`, `Config.Prowlarr` and `music_discovery`, which only
   the music import still uses.

## Own identity and must-fix bugs

1. **Rename to Flodni.** Go module, binary, image default paths, `FLODNI_*`
   environment variables with the old names kept as aliases. Persisted file
   names stay, so existing state carries over.
2. **Keep working stubs when a pack replaces them.** Replacing a pack deletes
   stubs that played before the new release is known to be readable
   (`internal/library/manager.go`). A pack still wins, but the old stubs go
   only once the new ones are confirmed readable.

3. **Atomic writes and bounded servers.** Stub and config writes go through a
   temporary file, sync and rename; the control HTTP server gets read, write
   and idle timeouts.

Releases and deployment are scheduled separately.

## Maintainability

1. **Clear the dependency backlog.** Merge or supersede the open Dependabot
   pull requests, align the vendored engine's `go.mod` with patched versions,
   and run the engine's private-torrent tests in CI.
2. **End-to-end coverage for replacement.** A second fixture filed over the
   first as the same TV episode, so the readability check before a
   replacement runs against the real engine.
3. **Break up `main.go`.** Phase a: split it into files by concern in the
   same package, as a pure move. Phase b: move the read-ahead cache into its
   own package with unit tests, and give the HTTP routes their own mux. The
   `App` struct that replaces the shared package-level maps waits for tests
   on the read path (see ideas).
4. **Make failures visible.** Phase a: triage every unchecked error, fix the
   ones that hide failures, and gate new ones with errcheck. Phase b: report
   sessions where a file was opened but never served bytes through the API.
   Structured logging with `slog` waits (see ideas).

## Hardening

1. **Opt-in token on the control API.** `api_token` or `FLODNI_API_TOKEN`
   enables bearer-token auth on every control route except the static panel
   page; webhooks may pass the token as a query parameter. Unset keeps
   today's open API.
2. **Unit tests on the read path.** Cover the read decision tree in
   `handle.go` and the pump's decision helpers, the prerequisite for the
   `App` struct.
3. **Stubs land where the mount serves them.** The library writes stubs
   under the configured `physical_source_path` while FUSE serves the path
   given on the command line; make them one value, or refuse to start when
   they differ.
4. **Bound and protect GoStorm's server.** Read-header and idle timeouts on
   port 8090, and the same opt-in token, since it exposes torrent control
   and shutdown.

## Reliability

1. **A health endpoint.** `GET /api/health` reports the mount, the engine and
   the state database, each with a status and a reason, and the container
   health check uses it instead of `mountpoint` alone.
2. **A shutdown deadline.** Shutdown saves state and unmounts within a
   bounded time and exits even when the unmount hangs, so a restart cannot
   block forever.
3. **Reads against slow seeders.** Reproduce in the end-to-end test the
   stalls seen against a seeder with a shallow request pipeline, then fix the
   leecher's request timeouts so a sequential read completes.
4. **Characterization tests for the library.** Pin stub naming, stub
   contents and one-stub-per-episode filing for movies, episodes and season
   packs, so later restructuring cannot change them unnoticed.
5. **An interface for the native bridge.** The read path calls the engine
   through an interface instead of the concrete client, and tests cover the
   fetch, prefetch and pump branches that only the end-to-end test reaches
   today. This is the last prerequisite for the `App` struct.
6. **No stall after a tail probe.** A read that follows a tail read stalls
   for about 30 s in the end-to-end test. The pump's grace period is not the
   cause; the engine's request scheduling is the suspect (see
   [ideas.md](ideas.md)). Players probe the end of a file before playing it,
   so this delays real starts.

## Readability evidence

Flodni records facts about bytes: did a file read, is it cached, who reads
it. Which release to replace, and when, is decided by tools outside it.
These steps do not touch the read path and do not wait for Reliability
step 6.

1. **Record the decision.** An ADR: Flodni fills and protects its own cache
   and records whether its files read; choosing and replacing releases
   stays outside. It weighs an external reader through the mount, warming
   in process and warming on the media server's first scan, and names what
   outside tools may rely on. Everything in it is labelled designed until
   the step that builds it lands.
2. **A readability record over the API.** The state database already counts
   sessions that ended with a failed read and nothing served, per torrent,
   but no endpoint reads it back. Add the time of the last read the swarm
   served and the reason and source of the last failure, keep the rule
   that only bytes from the swarm clear it, and serve it read-only at
   `GET /api/torrents/health` with the full hash, so outside tools stop
   parsing logs and stub names. Rows of torrents that left the library are
   pruned at startup, the endpoint answers 503 when the state database is
   off, and the README documents the fields as the supported contract.
3. **One log line per closed session.** A JSON line with the hash, file
   index, path, outcome, bytes served by serving path, whether the swarm
   served a read, time to first byte and duration, so a log store keeps
   what the 200-entry ring behind `/api/sessions` forgets. The existing
   `[DeadSwarm]` and `[TTFF]` lines stay as they are.
4. **Scrapeable metrics.** Prometheus text exposition on its own path, next
   to the JSON the panel reads: the time-to-first-byte histograms, dead-swarm
   verdicts, warmup cache hits by source, cache bytes and evictions. Later
   steps add their own series in their own pull requests.

## Readiness and protection

A file should start from the SSD the first time it is opened, and a reader
that is not playback should not pull a whole file through the swarm. These
steps build on index mode (`warmup_dir` set, `pump_on_open` off), where
only hash-checked data reaches the cache and tail coverage survives a
restart. Steps 2 and 3 wait until Reliability step 6 is fixed or its
trigger is known.

1. **Honest cache state and eviction.** One call that lists which files
   have their head and tail windows on disk now, without a `stat` per file,
   and a rule that background fills never evict: when the quota is full
   they stop. Then try evicting the least recently read file instead of
   the oldest written one, measured with the counters from the metrics
   step. It lands only if the measurement shows a clear win; otherwise the
   reason goes to ideas.
2. **Warm on add.** After a library add returns, a background queue with a
   small fixed worker pool fills the head and tail windows of each new file
   through the verified path, straight from the engine and not through the
   mount. It is off by default, pauses while a playback session is open and
   records each outcome per file in the readability record; only pieces the
   swarm delivered count as success. The end-to-end test adds a fixture,
   waits, stops the seeder and reads both windows back.
3. **Catch-up and retries.** At startup the warmer queues the library files
   it has never warmed, at a configurable pace, so a bulk import is worked
   off across restarts without a persistent queue. A failed warm is retried
   no sooner than most of a day later and a capped number of times; after
   that the record keeps the evidence and the warmer leaves the file alone.
   A file the cache has since evicted is left to its next play.
4. **A byte budget for readers that are not playback.** Phase a: count the
   bytes a file serves from the swarm to a reader with no playback session,
   past its two windows and a margin, and log each file once. Phase b: an
   opt-in switch that stops fetching from the swarm for such a reader past
   the budget and serves only what is cached. The budget lifts as soon as a
   session registers, applies only once a playback webhook has been seen
   since start, and a refusal never counts as a failed read in the
   readability record.

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
