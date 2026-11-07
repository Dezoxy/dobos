# Dezoxy/tiramisu fork

A fork of [MrRobotoGit/tiramisu](https://github.com/MrRobotoGit/tiramisu) for one homelab
(toom-macmini-homelab, ADRs 15 and 16): Tiramisu streams nCore and other indexers' torrents
as files for Plex, and this fork carries the homelab's own code on top of upstream releases.

## What the fork changes

Every change is opt-in: with the fork's settings unset, Tiramisu behaves exactly as upstream.

| Setting | Default | Change |
| --- | --- | --- |
| `pump_on_open` | `true` (upstream) | `false`: opening a file no longer starts the read-ahead pump. A scanner (Plex, Sonarr and Radarr's ffprobe, Bazarr's hash) gets small on-demand fetches; the pump starts once a webhook confirms playback or the reads look like playback (`IsInferredPlayback`: three reads past 2 MB). Measured on the homelab, a scanner read ~0.4 MB of a file while the pump fetched ~500 MB. With it off, a read with no pump and no playback signal is a *scan read*: it fetches only the rest of the head window or the bytes it asked for, skips the next-chunk prefetch and WarmLanding, uses the SSD head in any handle state, and a bounded range request reads ahead no further than its end. Ffprobe over Bones' 245 episodes (16 MiB pieces) cost 10.3 GB with only the lazy pump. |
| `warmup_dir` | empty (upstream) | Set, the disk warmup becomes an **index cache** in that directory, whether or not GoStorm stores pieces on disk (`UseDisk`). The head and tail windows are `warmup_head_size_mb` and `warmup_tail_size_mb` (upstream: fixed 64 and 16), with no boundary chunk past the head. Tail coverage is saved beside each tail file (`.cover`, written after the data is synced) and survives restarts; a cover file for another window size is dropped. A scan read that misses the tail queues the window for one background worker, which requests its whole pieces, waits until they verify and writes it whole: a scan burst filling hundreds of tails at once evicted pieces from the RAM cache before they verified. Nothing is written before every piece it covers has passed its checksum: responsive streaming hands out bytes before the hash check, so a block waits up to 60 s for its pieces, asking the swarm for the whole pieces under it (a scan reads part of a piece, and the rest would otherwise never be requested), then writes a fresh copy of the verified bytes or is dropped. Once a file's head and tail are both on SSD, `Open` returns at once (the torrent wakes in the background, peers only) and a scan's reads are served from SSD, so a rescan downloads no pieces. |

## Branches

| Branch | Contents |
| --- | --- |
| `homelab` (default) | Upstream's latest reviewed release + the fork's changes + fork CI. Images come from here. |
| `main` | Untouched mirror of upstream. |
| `sync/vX.Y.Z` | Opened by the upstream sync workflow; one per upstream release. |

## Upstream updates

Upstream releases often (several a week in October 2026) and keeps its full test suite in a
private repository, so a passing build says little about a change. Every update is read.

1. **Upstream sync** (`.github/workflows/upstream-sync.yml`) runs on Mondays or on demand. When
   upstream's latest release is not in `homelab`, it merges it into `sync/vX.Y.Z` and opens a pull
   request listing the commits, the changed files and every added line that touches the network,
   trackers, telemetry, processes or files. A conflict opens an issue instead.
2. Review the pull request. Read every flagged line and any change to `docker/`, `go.mod` and the
   code the fork changes. Check the defaults the homelab relies on: telemetry stays switchable
   off, private torrents get no extra trackers, DHT and PEX stay switchable off, the APIs stay
   unauthenticated (so the deployment keeps them on loopback).
3. Merge it with a **merge commit**, never squash: the merge keeps upstream's history, so the next
   sync contains only what is new. The fork's own pull requests are squash-merged.
4. Tag a release (below). Nothing reaches the homelab until its image pin is bumped there.

## Releasing an image

CI (`.github/workflows/fork-ci.yml`) builds and tests every change. A tag
`v<upstream version>-homelab.<n>` on `homelab` also publishes `ghcr.io/dezoxy/tiramisu:<tag>` with
an SBOM and a provenance attestation; the run summary shows the digest to pin.

```bash
git tag v1.11.8-homelab.1 homelab
git push origin v1.11.8-homelab.1
gh attestation verify oci://ghcr.io/dezoxy/tiramisu:v1.11.8-homelab.1 --repo Dezoxy/tiramisu
```

Release tags cannot be moved or deleted: confirm the tag commit contains the merged change first.

Only the `Publish` job can write packages, and only for a pushed `v*-homelab.*` tag: it runs in
the `release` environment, which accepts deployments from those tags alone. Every other job,
including the one that builds and runs unreviewed upstream code, has read-only permissions.
The release is pushed by digest, scanned, and only then tagged.

## Contributing and security

- `homelab` and `main` accept changes only through pull requests, after every check passes. Nobody,
  including the owner, can push to them, force-push or delete them.
- Checks: build, vet, module tidiness and tests; gitleaks over the full history; actionlint and
  zizmor; Trivy dependency and image scans; dependency review; CodeQL; the image smoke test.
- Actions are pinned to commit SHAs; Dependabot keeps the pins and the Docker base images
  current. Go modules follow upstream.
- Report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

Tiramisu is licensed under GPL-3.0; this fork's source is public to satisfy it.
