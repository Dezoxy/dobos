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

## Done

- 2026-11-11, maintainability step 4a: 43 unchecked errors that could hide
  failures now return or log; the rest are marked intentional, and errcheck
  gates new ones in `make go-check` and CI.
- 2026-11-11, maintainability step 3b: the read-ahead cache is
  `internal/readahead` with unit tests, and the control routes have their own
  mux; `/debug/vars` is no longer served.
- 2026-11-11, maintainability step 2: the end-to-end test files a second
  release over a TV episode and a third that never delivers, checking that
  the first replaces and the second is refused.
- 2026-11-11, own identity step 3: stub and config writes are atomic, and
  the control server has read, write and idle timeouts.
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
- 2026-11-11, maintainability step 1: the vendored engine's `go.mod` is
  aligned with patched versions (gRPC, OpenTelemetry, x/crypto, websocket),
  its private-torrent tests run in `make go-test` and CI, and the Dependabot
  backlog is merged or superseded.
- 2026-11-11, maintainability step 3a: `main.go` is split into nine files
  by concern (FUSE nodes, handle, pump, read-ahead cache, playback, webhook,
  profiling, inode map, wiring); a line-multiset check proves no code
  changed.
