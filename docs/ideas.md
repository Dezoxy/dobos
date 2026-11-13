# Ideas

Things that would make Flodni more reliable or easier to maintain, not yet
planned. An idea moves to [roadmap.md](roadmap.md) when it is chosen; until
then nothing here is a commitment. Line numbers are as of 2026-11-10.

## Later product work

- **A proper file-lifecycle API.** Add, replace with a policy, confirm and
  remove, as explicit operations instead of side effects.
- **Leave the engine alone**, except for fixes. Watch advisories for the
  vendored `anacrolix/torrent`.

- **Reader budget follow-ups (not planned).** Attribute pump and prefetch
  fills to the reader that caused them, after Reliability step 6. Reads
  after a session closes are not budgeted (counted or refused). The
  read-ahead step past a window edge: a miss inside a window is never refused,
  and its read-ahead can extend past the window edge into the middle the
  budget is meant to protect; measure the overshoot, then decide whether to
  clamp it.

- **Warm on add: pause, wake and removal in the end-to-end test (not
  planned).** The `warm-on-add` variant files one title. A canary public
  tracker for the wake of a closed private torrent, and a removal while a
  title is queued or paused, need a second title held open; the seeder has
  no such mode.

## Must fix

Bugs and data-loss risks. None open.

## Should improve

- **The periodic inode prune checks the walk too.** `cleanup.go` prunes ghost
  inodes after its own walk without the "the walk was whole" guard the start-up
  prune now has; the walk adds the root path itself, so an empty or unmounted
  library still leaves a non-empty set. Low stakes, since inodes derive from
  hash and index.
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

- **Stalls after a tail probe: fixed, two gaps left.** Both stalls of
  Reliability step 6 are fixed in the cache (see the Done lines of
  [roadmap.md](roadmap.md)). What stays open, not planned:
  - **An order-only change wakes no idle peer.** In the engine a priority or
    request-order change that leaves the pending set as it was triggers no
    request update. The stale cache priorities exposed it; an engine-side
    wake removed the 34 s stall too (0 of 11 runs) but was not shipped,
    since fixes belong in Flodni's own code. Another producer of an
    order-only change could expose it again. An earlier note that waking
    peers did not help came from an attempt that was never committed;
    probably it ran before the stale priorities were cleared, or the 58 s
    stall was read as the same one. Unverified.
  - **A dropped only peer comes back at the next announce, a minute away.**
    The engine floors a tracker's announce interval at one minute
    (`internal/anacrolix-torrent/tracker_scraper.go`) and does not keep a
    dropped peer to redial. A private torrent with one seeder and one
    tracker that loses its peer for any reason waits up to a minute.
    Not covered: a cache over capacity with nothing evictable (everything
    sits in a reader window or a file guard) can still reach "have all".
  - **Reapply the instrumentation:** `git apply docs/ideas/stall-dump.patch`
    ([patch](ideas/stall-dump.patch)) on `main` adds a dump at the first
    fetch timeout. Set `FLODNI_STALL_DUMP=<n>` for the dump and
    `FLODNI_STALL_POKE=1` for the wake experiment; `tests/e2e` passes them
    through as `E2E_STALL_DUMP` and `E2E_STALL_POKE`.

- **Warm the next episode during playback.** While episode N plays, fill
  episode N+1's windows so a binge starts at once. Not planned: a second
  reader on the playing torrent splits the engine's read-ahead. Needs warm
  on add in place and a measurement that the playing stream does not slow
  down.
- **Evict the least recently read file.** Not built. Eviction takes files
  never played first, then the oldest written. Whether read recency would
  keep more starts warm is measured first: `flodni_warmup_cold_reopen_total`
  with `previously_played="true"` over `flodni_playback_starts_total`. It
  moves to the roadmap if that share reaches 5% of plays over a few weeks at
  a full quota. The `previously_played="false"` series is a different
  problem: warm data evicted before its first play points at the quota or
  the eviction order, not at recency.
- **Warm again after eviction.** The catch-up leaves evicted files to their
  next play; warming them again needs a quota that fits the library, or a
  rule for which files deserve it.
- **Dead packs probed file by file.** After the head file of a pack gives up
  (4 failed attempts), the next file becomes the head and gets its own four
  attempts, so a dead pack costs one wake a day for days per file. Bounded, and
  it announces only to the stored trackers; a pack-level give-up would end it.
  Not planned.
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
  a stop within two minutes of a session loses its evidence, and its
  `[Session]` log line is never written. Closing them in the shutdown path
  would keep both.
- **Count reads after a session closes.** A session closes after 60 s idle
  (30 s once it is older than 30 minutes), or when a probe releases while a
  player without a pump slot has read nothing. Reads on a handle that is
  still open then find no session and are counted nowhere, so a paused
  playback is cut short in `/api/sessions` and the `[Session]` line. Opening
  a session on the first read after a close would fix it; weigh a new
  session per pause against one long one.
- **Warmup accounting drift sources left.** The head write and removal race
  is closed, and `GetAvailableRange` no longer stores a stale-low size over
  a writer's newer one. `totalSize` can still drift: the eviction walk
  stores an absolute total outside `tailMu`; `dropResidue` unlinks and
  subtracts without `tailMu`; a CORRUPT CACHE removal never subtracts; and
  the head create's `handles.Store` should be a `Swap` that closes the old
  descriptor, or it leaks one. `flodni_warmup_cache_bytes` exports this
  total as approximate.
- **Stale comment in `fuse_nodes.go` (`Open`).** It says `Open` needs head
  and tail ready. Its warm path uses `headReady` only; `tailReady` feeds the
  `[TTFF]` log line. Fix the comment in its own change.
- **More series on `/metrics/prometheus`.** Left out of the first version,
  each for its own reason:
  - Engine and peer series (hedge, eject, churn, the gradient loop). The
    JSON `/metrics` sums them over the active torrents under engine locks,
    so a scrape would take those locks, and the sums fall when a torrent
    expires, which a counter must not do. They need per-process counters
    in the engine first.
  - Short reads in the engine (`native.ShortReadCounts`,
    `ShortReadRepairCounts`). These are already per-process atomic counters
    with no engine lock, so exporting them is two families and their README
    rows; not done in the first version.
  - Read-ahead evictions: needs a counter inside `Put` under the shard
    lock.
  - The head-fill duration histogram (`warmupDurationBuckets`): it has no
    sum, so it cannot be a Prometheus histogram without a new counter.
  - Go runtime and process gauges (goroutines, memory, descriptors), and a
    gauge of open sessions. The usual route is `client_golang` with a
    private registry, which also brings OpenMetrics; weigh the dependency
    against the hand-written encoder in `internal/promtext`.
  - Sub-millisecond precision: the histograms keep whole milliseconds, so
    cache hits add nothing to `_sum`. Changing it means storing
    microseconds.
  - `promtool check metrics` in the end-to-end test; needs a new image pin
    in the `Makefile`.

## Nice to have

- **Structured logging.** 222 log call sites use `log.Printf`. Moving them
  to `slog` with fields would help log search, but each line needs its own
  key choices; worth doing when logs ship to a central store.
- **CodeQL.** Left out for now: the cgo FUSE build may defeat its automatic
  build. Needs a manual build step.
- **A threat model** for the control API and its opt-in token.

## Enterprise-grade, optional

- **Less container privilege.** The image runs with `SYS_ADMIN` and AppArmor
  unconfined for the FUSE mount; a narrower profile may be enough.
