# Ideas

Things that would make Flodni more reliable or easier to maintain, not yet
planned. An idea moves to [roadmap.md](roadmap.md) when it is chosen; until
then nothing here is a commitment. Line numbers are as of 2026-11-10.

## Sequence for becoming its own project

The order matters: each step makes the next one safe.

1. **End-to-end test first.** A local tracker seeds a small torrent, Flodni
   mounts it through FUSE, the test reads the file and compares hashes. It
   pins the one behaviour that must not break, before any code moves.
2. **Delete unused subsystems.** Chosen: now "Trim to the streaming core" in
   [roadmap.md](roadmap.md), with the end-to-end test of step 1 placed
   before the removals that touch the read path. The Go module, binary and
   mount path rename follows it.
3. **Break up `main.go`.** 6,126 lines and about 20 `global*` singletons.
   Move to packages behind an `App` struct, with its own HTTP mux and server
   timeouts.
4. **Make failures visible.** Structured logging with `slog`, stop discarding
   errors (169 places), and expose per-file session results through the API.
5. **A proper file-lifecycle API.** Add, replace with a policy, confirm and
   remove, as explicit operations instead of side effects.
6. **Leave the engine alone**, except for fixes. Watch advisories for the
   vendored `anacrolix/torrent`.

## Must fix

Bugs and data-loss risks.

- **Replacing a pack deletes working stubs**
  (`internal/library/manager.go:709`). A replacement that never becomes
  playable still removes files that played. Options: keep the old stubs until
  the new ones are confirmed readable (preferred), a grace period, or never
  replace automatically.
- **Stub and config writes are not atomic** (`WriteStub` in
  `internal/library/naming.go:235`, `Save` in
  `internal/config/config.go:386`). A crash mid-write leaves a truncated
  file. Write to a temporary file, sync, rename.
- **The control server's start error is discarded and it has no timeouts**
  (`main.go:5464`). A port conflict is silent, and a slow client can hold a
  connection open forever.

## Should improve

- **Characterization tests** around naming, stub creation and replacement,
  so restructuring cannot change behaviour unnoticed.
- **A test that a private torrent stays private.** No DHT, PEX or added
  trackers for a torrent flagged private (BEP 27). It is a hard rule with no
  check behind it.
- **A real health endpoint.** `/api/health` always answers 200. It should
  report the mount, the engine and the stores, so a container health check
  means something.
- **Expose unserved sessions.** A file that was opened and never got bytes
  should be a metric and an API result, not only a log line.
- **Remove the dead index-seed path** once the supported one is the only
  caller.

## Nice to have

- **Move fork-era code out of `main.go`** ahead of the full split, since it
  is the best-understood part.
- **A shutdown deadline**, so a hung unmount cannot block a restart forever.
- **CodeQL.** Left out for now: the cgo FUSE build may defeat its automatic
  build. Needs a manual build step.
- **A threat model** for the control API before it gains authentication.
- **An updater for the `Makefile` image pins.** Dependabot does not read
  them.

## Enterprise-grade, optional

- **Token authentication on the control API.** `/api/restart`, `/api/config`
  and `/api/kill-stream` accept any caller that can reach the port.
- **Less container privilege.** The image runs with `SYS_ADMIN` and AppArmor
  unconfined for the FUSE mount; a narrower profile may be enough.
