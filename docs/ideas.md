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
  behind one struct was gated on unit tests of the read path. Those now cover
  the decision helpers, the cache hit, short-read repair, `Release`, and the
  fetch, prefetch and pump branches through a fake engine behind the
  `engineBridge` interface. The prerequisites are met; the move itself is not
  planned yet.

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

- **A read stalls about 30 s while the pump runs (engine, unresolved).**
  Symptom: in `tests/e2e/run.sh` the `default` variant, a 1 MiB tail read
  followed by a full `sha256sum`, takes 33 to 58 s instead of 1 to 5 s in
  roughly half the runs (150 MB file, 1 MiB pieces, 128 MB cache). Reads at
  about 136 MB wait the 8 s fetch timeout three times, and the pump's chunk
  read returns short after about 30 s. Reproduce: the `default` variant on
  `main`, repeated until it stalls. Control: with `pump_on_open` false the
  full read took 0 to 1 s in 3 of 3 runs; skipping the tail read does not
  prevent it (one run stalled, one failed outright), and the `slow-seeder`
  and `index-cache` variants do not stall. Ruled out: the pump's grace
  period and its re-anchoring (the pump starts at offset 0 at open, never
  waits on a hard lead limit, and ends cleanly), and the seeder (the
  leecher's peer is unchoked and nothing is pending at the seeder). What was
  seen on the leecher during a stall: the peer holds no requests and has
  sent "not interested" while pieces the readers want are pending or
  already marked complete, no request update runs for 30 s or more, and it
  resumes when the readers time out or a piece is released. The last update
  before such a stall scanned 129 pieces and stopped at the 128 MB storage
  cap with nothing to request. Waking idle peers when the piece order
  changes did not remove the stall. It likely lives in the interplay of the
  request strategy's storage cap with two readers (`GetRequestablePieces`
  in `internal/anacrolix-torrent/request-strategy/order.go`) and the cache
  completion state in `internal/gostorm/torr/storage/torrstor`; trace per
  piece completion against the cache's `Complete` flag first. Hypothesis
  to test next: the test file (150 MB) is larger than GoStorm's default
  128 MB cache, so two readers can hold pieces up to the cap and leave the
  strategy nothing to request until a read times out and releases one. The
  cheap experiment is the `default` variant with GoStorm's `CacheSize` raised
  to 512 MB (set through its settings API: get, modify, set); if the stall
  goes away, the fix is in how the cap counts pieces readers still hold.
  Players probe file ends before playing, so this can delay real starts.

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
