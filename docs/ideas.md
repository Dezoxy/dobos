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
   before the removals that touch the read path.
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

Bugs and data-loss risks. None open.

## Should improve

- **Characterization tests** around naming, stub creation and replacement,
  so restructuring cannot change behaviour unnoticed.
- **Stubs outside the mount.** The library writes stubs under the config's
  `physical_source_path`, while FUSE serves the path from the command line.
  A config without it files stubs outside the mount and still returns 201
  (`main.go` around 5261 against 4653).
- **A health endpoint.** There is none since the dashboard went. One that
  reports the mount, the engine and the stores would let a container health
  check mean more than `mountpoint`.
- **Remove the dead index-seed path** once the supported one is the only
  caller.

- **Reads stall against a seeder with a shallow request pipeline.** In the
  end-to-end test, a seeder left at the engine's default 1 MiB per-peer
  request allowance served about 600 kB/s with 35 to 60 s stalls, and a
  sequential read ended in EIO. Real swarms have such peers; check the
  leecher's request timeouts against them.

- **Timeouts on the engine's HTTP server.** GoStorm's gin server on port
  8090 runs with no read, write or idle timeouts (`internal/gostorm/web`).
  `/stream` responses are long-lived, so a write timeout does not fit; read
  header and idle timeouts do.

- **An `App` struct instead of package-level state.** The FUSE handle, the
  pump, the webhook and the cleanup manager share seven package-level maps
  (`activeHandles`, `activePumps`, `playbackRegistry`, `pumpTimers`,
  `priorityTimers`, `inFlightFetches`, `inFlightPrefetches`). Moving them
  behind one struct was gated on unit tests of the read path; those now cover
  the decision helpers, the cache hit, short-read repair and `Release`. The
  branches that fetch through the native bridge (`FetchAhead`, `FetchBlock`,
  the pump goroutine) are still covered only by the end-to-end test.

## Nice to have

- **Move fork-era code out of `main.go`** ahead of the full split, since it
  is the best-understood part.
- **Structured logging.** 222 log call sites use `log.Printf`. Moving them
  to `slog` with fields would help log search, but each line needs its own
  key choices; worth doing when logs ship to a central store.
- **A shutdown deadline**, so a hung unmount cannot block a restart forever.
- **CodeQL.** Left out for now: the cgo FUSE build may defeat its automatic
  build. Needs a manual build step.
- **A threat model** for the control API for the opt-in token.
- **An updater for the `Makefile` image pins.** Dependabot does not read
  them.

## Enterprise-grade, optional

- **Less container privilege.** The image runs with `SYS_ADMIN` and AppArmor
  unconfined for the FUSE mount; a narrower profile may be enough.
