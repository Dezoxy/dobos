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
   `internal/preload`, `internal/syncer/statestore`, `Config.EngineScripts`,
   `Config.LogDir` and the configuration keys nothing reads.
4. **Remove upstream packaging.** `docker-windows/`, `install.sh`,
   `tiramisu.service`. The container image is the only supported install.
5. **End-to-end streaming test.** A local tracker seeds a small torrent,
   Flodni mounts it and the test reads the file back and compares hashes. It
   must pass before steps 6 and 7, which touch the read path.
6. **Remove catalogue sync.** The scheduler, the movie, TV and watchlist
   engines, the TMDB, Torrentio and Rotten Tomatoes clients, the built-in
   Prowlarr client and their control panel cards.
7. **Remove music and audiobooks.** `internal/musicimport`,
   `cmd/musicimport`, `internal/audio` and the audio parts of
   `internal/library`, `internal/metadb`, `startup.go` and `main.go`.

## Done

- 2026-11-11, trim step 1: telemetry, the update check, the MCP server and
  the pprof endpoints are removed.
- 2026-11-11, trim step 2: the dashboard, its collector and LAN discovery,
  and the macOS toolbar app are removed. Done before the dead-code step
  because the collector read NAT-PMP and blocklist state.
