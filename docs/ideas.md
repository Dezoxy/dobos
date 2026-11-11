# Ideas

Things that would make Flodni more reliable or easier to maintain, not yet
planned. An idea moves to [roadmap.md](roadmap.md) when it is chosen; until
then nothing here is a commitment. Line numbers are as of 2026-11-10.

## Later product work

- **A proper file-lifecycle API.** Add, replace with a policy, confirm and
  remove, as explicit operations instead of side effects.
- **Leave the engine alone**, except for fixes. Watch advisories for the
  vendored `anacrolix/torrent`.

## Must fix

Bugs and data-loss risks. None open.

## Should improve

- **An `App` struct instead of package-level state.** The FUSE handle, the
  pump, the webhook and the cleanup manager share seven package-level maps
  (`activeHandles`, `activePumps`, `playbackRegistry`, `pumpTimers`,
  `priorityTimers`, `inFlightFetches`, `inFlightPrefetches`). Moving them
  behind one struct was gated on unit tests of the read path; those now cover
  the decision helpers, the cache hit, short-read repair and `Release`. The
  branches that fetch through the native bridge (`FetchAhead`, `FetchBlock`,
  the pump goroutine) are still covered only by the end-to-end test;
  Reliability step 5 in the roadmap closes that gap.

## Nice to have

- **Structured logging.** 222 log call sites use `log.Printf`. Moving them
  to `slog` with fields would help log search, but each line needs its own
  key choices; worth doing when logs ship to a central store.
- **CodeQL.** Left out for now: the cgo FUSE build may defeat its automatic
  build. Needs a manual build step.
- **A threat model** for the control API and its opt-in token.
- **An updater for the `Makefile` image pins.** Dependabot does not read
  them.

## Enterprise-grade, optional

- **Less container privilege.** The image runs with `SYS_ADMIN` and AppArmor
  unconfined for the FUSE mount; a narrower profile may be enough.
