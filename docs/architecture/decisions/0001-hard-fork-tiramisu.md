# 1. Hard-fork Tiramisu as Dobos

Date: 2026-11-10

## Status

Accepted

## Context

Dobos started as a fork of [Tiramisu](https://github.com/MrRobotoGit/tiramisu)
that tracked upstream and carried a growing set of its own changes: pumping
on open, an on-disk index cache, index seeding, a guard for private torrents
and stricter file naming.

Tracking upstream had a rising cost. Upstream develops features this fork
does not use (music import, catalogue sync, an MCP server, telemetry, a
self-updater), most of the code sits in one 6,000-line `main.go` that every
sync touches, and upstream keeps its full test suite private. Each sync was a
review of code with no tests behind it.

## Decision drivers

- Reliability of playback matters more than new features.
- One maintainer: the code has to be small enough to understand in full.
- Changes must be testable in this repository.

## Considered options

1. Keep tracking upstream and carry the changes as patches.
2. Contribute the changes upstream and run upstream's releases.
3. Hard-fork: stop syncing, rename, and maintain it as its own project.

## Decision

Option 3. Dobos keeps Tiramisu's history and licence (GPL-3.0) and the
original copyright notice, stops merging from upstream, and is free to delete
and restructure code.

Option 1 keeps the sync cost and forbids restructuring. Option 2 depends on
upstream wanting changes that serve a narrower use than its own.

## Consequences

- Subsystems this project does not use can be deleted, and `main.go` can be
  split into packages.
- Upstream's fixes no longer arrive by merge. Relevant ones are ported by
  hand, and advisories for the vendored `anacrolix/torrent` are watched
  directly.
- The torrent engine is still the vendored one. It is left alone except for
  fixes; replacing it is not part of this decision.
- The Go module and the binary are still named `tiramisu` until a later
  change renames them.
