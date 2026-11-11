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
