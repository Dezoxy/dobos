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

- **Library naming and filing quirks.** Pinned as they are by the
  characterization tests (`internal/library/*_char_test.go`), so each change
  is a deliberate one:
  - A pack file named `S00E01` (a special) is filed under the requested
    season as that season's episode 1 and competes with the real one.
  - Samples are dropped only when a larger file names the same episode; a
    lone sample is filed as the episode.
  - A year in the title is read as the release year when no date is given
    (`Blade_Runner_2049_2049_…`, `Dune_2021_2021_…`); a title with no ASCII
    letters starts with `_`.
  - `EpisodeKey` drops non-ASCII letters (`Élite` → `lite_s01e01`), so two
    shows can share a key.
  - Episode stubs are matched by the first 8 hash characters; two releases
    with the same prefix are treated as one.

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
