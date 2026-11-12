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

- **A read stalls about 30 s while the pump runs (engine, cause found).**
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
  piece completion against the cache's `Complete` flag first.
  **Cache experiment (2026-11-12): the cache size decides it.** With
  `E2E_GOSTORM_CACHE_MB` (the seeder sets GoStorm's `CacheSize` before the
  add), the `default` variant stalled in 8 of 8 runs at the default 128 MB
  (median 34 s) and in 3 of 3 at 128 MB set through the same API, but in 0
  of 8 at 512 MB and 0 of 3 at 160 MB (median 1 s). The file is 150 MiB, so
  the threshold is the file size. Likely mechanism: `GetRequestablePieces`
  subtracts every piece it walks in priority order from the storage cap,
  complete or not, and returns once the cap is spent, so pieces the readers
  already hold use it up and the ones they need are never requested.
  Fix options, smallest first: let GoStorm's storage report no cap
  (`torrstor/storage.go` `capFunc`), leaving eviction to its own cache; or,
  as a fixes-only engine change, stop counting pieces that are complete
  toward the cap. Larger files than the cache are the normal case, so this
  matters beyond the test.
  **Uncapped-storage experiment (2026-11-12):** with GoStorm's storage
  capacity removed (`capFunc` reporting no cap) the stall dropped to 3 of 8
  runs, at 58 to 59 s, but did not go away; reverted. So the cap is not the
  whole cause. **New readers' read-ahead (2026-11-12):** `newReader` called
  `SetReadahead` before `cache` was set, so it returned early and a new
  reader ran with the engine's default 48 MiB until the next 1 s stats
  tick. It now starts with the tick's value (cache size times read-ahead
  percent, split across the active readers). Measured on `default`, 8 runs
  each: 7 of 8 stalled before (33 to 34 s) and 7 of 8 after (33 to 42 s);
  first byte of the first read, median 9.5 ms both ways. The fix is correct
  but does not touch the stall. In a stall Flodni logs nothing between the
  pump's start and its end about 30 s later.
  **Read-ahead against the protected range (2026-11-12):** the engine
  read-ahead (cache size times percent, split across readers) outran the
  range `torrstor` protects from eviction by the missing 0.85 safety factor
  (96 MiB against 81.6 MiB for one reader at 128 MiB and 75%). It now
  applies the factor. The seeder's upload ratio (piece data sent over video
  size) was 1.000 in 8 of 8 runs before and in 8 of 8 after, so the engine
  never fetched a piece twice in this test and the mismatch is not the
  stall. Stalls: 8 of 8 runs before, 6 of 8 after (two fast runs, noise
  at this sample size); first-byte median 6 ms to 10 ms on `default`.
  Lead from the stall logs (2026-11-12, unverified): during a stall Flodni
  logs nothing for about 30 s, and the next line is GoStorm's second failed
  public tracker-list download. The e2e network has no outside access, so
  every download attempt waits out its timeout; if that refresh holds a lock
  the read path needs, the stall is an artifact of the offline test network,
  which would also explain why deployments with internet access have not
  shown it. Check what the tracker-list refresh locks
  (`internal/gostorm/torr/utils`) and rerun the variant with the refresh
  disabled.
  Players probe file ends before playing, so this can delay real starts.
  Closed lead (2026-11-12): the tracker-list refresh holds no lock while it
  downloads, and its second attempt is a 30 s retry timer started at the
  add (a saved stall log shows attempt 1 at the add and attempt 2 exactly
  30 s later), so it is a clock, not a cause. The four attempts so far each
  flipped one input and counted stalls over 8 runs; the next step is to
  capture state at the stall instead: on the first 8 s fetch timeout, dump
  the reader positions and read-ahead, for the pieces under them the
  engine's completion, priority and pending requests against the cache's
  `Complete` and size, the request-order scan (pieces walked, cap left) and
  the peer's interest and request count. Hypotheses for that trace: the
  request strategy stops once the cap is spent while the cache evicts only
  when strictly over capacity, so at exactly full neither side moves; and
  the engine assumes storage keeps the top pieces by its own order while
  the cache evicts by reader range and access time. Two relevance checks:
  the `slow-seeder` variant does not stall, so cap the seeder's upload rate
  and rerun `default`; and post a play webhook in the `index-cache` variant
  (pump off until playback) before the full read, to learn whether index
  mode's playback path stalls too.

- **Warm the next episode during playback.** While episode N plays, fill
  episode N+1's windows so a binge starts at once. Not planned: a second
  reader on the playing torrent splits the engine's read-ahead and is the
  pattern of the open stall. Needs warm on add in place, the stall settled
  and a measurement that the playing stream does not slow down.
- **Warm again after eviction.** The catch-up leaves evicted files to their
  next play; warming them again needs a quota that fits the library, or a
  rule for which files deserve it.
- **Quota accounting by allocated blocks.** The warmup quota counts apparent
  file sizes; sparse files make it count more than the disk holds. Safe, but
  it wastes part of the quota.
- **A swarm signal from the engine.** For awake torrents: peers connected,
  time of the last piece received, observed rate; what the swarm does, as
  opposed to what a tracker reports. It covers only torrents that are awake.
- **Per-file replacement check for packs.** The check before a replacement
  reads one file of the release; a pack whose other episodes do not read
  passes. Weigh the extra reads against the risk.
- **Count a metainfo that never arrives.** A torrent whose metainfo times out
  fails the open with EIO before any session exists, and `Wake` never
  condemns it, so the readability record shows nothing for it. A failure
  reason of its own would cover it; weigh the retries of a player counting
  several times for one occasion.
- **Stop replaced torrents leaving an entry behind.** A session still open
  on a replaced torrent writes its readability entry back after the library
  cleared it. Closing the session where the stub is invalidated
  (`invalidateSyncRemovedPath`), before the library clears the row, would
  fix replacement; clearing the row in the FUSE unlink path would cover
  deleted stubs. If `movies` and `tv` can be separate mounts, the startup
  prune also needs a per-directory non-empty guard.
- **Keep verdicts across a shutdown.** Sessions still open at shutdown never
  reach a verdict; only the release path and the cleanup loop close them, so
  a stop within two minutes of a session loses its evidence. Closing them in
  the shutdown path would keep it.

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
