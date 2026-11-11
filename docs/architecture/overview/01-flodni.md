## Architecture Overview

### Purpose

Flodni presents torrents as ordinary media files. A media server scans and
plays them like any other library, and the bytes are fetched from the swarm
when they are read. Nothing is downloaded to disk in full.

### Context

A media server (Plex or Jellyfin) reads files from a FUSE mount that Flodni
provides. Each file is backed by a small stub on the host that names a
torrent and a file inside it. When the media server reads a byte range, Flodni
asks its torrent engine for the pieces that hold it.

Titles are added, replaced and removed through the control API, by the
operator or by automation outside this repository. Choosing which release to
add is not Flodni's job.

![System context view: what Flodni depends on, and who depends on it](embed:SystemContext)

### What runs

Flodni is one Go process. It owns three kinds of state on the host: the stub
library, the engine's stores and one configuration file.

![Container view: the daemon and the stores it owns](embed:Containers)

### Inside the daemon

A read enters through the virtual filesystem. Head and index bytes come from
the on-disk index cache; everything else comes from the torrent engine, which
talks to the swarm. The library manager owns the stub files. The control API
is the only way in besides the mount.

![Component view: the parts of the daemon](embed:Daemon)

### Status

Implemented, inherited from Tiramisu and running. The control API and the
engine's server on port 8090 take an opt-in bearer token (`api_token`); with
none set they are open and meant to be reachable only from the host and a
trusted network. Planned changes are in the repository's `docs/roadmap.md`;
ideas not yet planned are in `docs/ideas.md`.
