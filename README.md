# Flodni

Flodni presents torrents as ordinary media files. A media server such as Plex
or Jellyfin scans and plays them from a FUSE mount, and the bytes are fetched
from the swarm when they are read. Nothing is downloaded to disk in full.

It is one Go binary: a torrent engine, a FUSE virtual filesystem and a small
control API.

## Status

Flodni is a hard fork of
[Tiramisu](https://github.com/MrRobotoGit/tiramisu) by Matteo Rancilio,
started on 2026-11-10. It no longer follows upstream; the reasons are in
[ADR 0001](docs/architecture/decisions/0001-hard-fork-tiramisu.md).

The code is Tiramisu's with this fork's reliability changes. The Go module,
the binary, the image paths and the environment variables are named Flodni.
It is maintained by one person for their own use. Expect breaking changes
and no support.

## Documentation

| Document | What it holds |
|---|---|
| [docs/roadmap.md](docs/roadmap.md) | What is planned, in order, and what is done. |
| [docs/ideas.md](docs/ideas.md) | What could be improved, not yet planned. |
| [docs/architecture/README.md](docs/architecture/README.md) | The C4 model, its views and the decisions. |
| [README.upstream.md](README.upstream.md) | Tiramisu's user guide as inherited: install, configuration, API. |
| [SECURITY.md](SECURITY.md) | How to report a vulnerability. |

## Building

Linux only: the FUSE layer is cgo against `libfuse3`.

```bash
sudo apt-get install -y libfuse3-dev pkg-config
make go-check   # gofmt, build, vet
make go-test    # tests with the race detector
```

The container image builds anywhere Docker runs:

```bash
docker build -f docker/Dockerfile -t flodni:dev .
```

## Running

The image mounts the FUSE view at `/mnt/flodni-virtual`, reads library
files from `/mnt/flodni-real` and keeps its state under `/state`. Set the
paths with `FLODNI_ROOT_PATH`, `FLODNI_SOURCE_PATH`, `FLODNI_MOUNT_PATH`,
`FLODNI_STATE_DIR`, `FLODNI_LOG_DIR`, `FLODNI_LOG_FILE` and
`FLODNI_HOST_MOUNT_HINT`; the Plex settings use `FLODNI_PLEX_URL` and
`FLODNI_PLEX_TOKEN`. The older `TIRAMISU_*` names still work and lose to the
`FLODNI_*` ones. Existing state carries over: the database is still
`tiramisu.db` and the log file `tiramisu.log`.

Shutdown saves state, then unmounts; a mount that will not unmount is
detached lazily after 5 s, and the process exits by 8 s at the latest. That
fits inside Docker's default 10 s `stop_grace_period`. A longer grace period
is fine; do not set it below 10 s, or Docker kills the process before the
deadline.

The command-line paths (the entrypoint's `FLODNI_SOURCE_PATH` and
`FLODNI_MOUNT_PATH`) win over `physical_source_path` and `fuse_mount_path` in
`config.json`; a different value there is logged and overridden.

The control server on `metrics_port` (default 9080) is open unless you set a
token. Set `api_token` in `config.json` or `FLODNI_API_TOKEN` (the variable
wins) and every route except the panel page and
`/api/health` needs `Authorization: Bearer <token>`. The Plex or Jellyfin webhook cannot send
headers, so `/plex/webhook` and `/webhook` also accept the token in the URL,
for example `http://host:9080/plex/webhook?token=<token>`; no other route
does. `/control` loads without it and asks for the token on its first call,
then keeps it in the browser's local storage. `GET /api/config` never
returns the token, and saving from the panel leaves it unchanged. The
same token covers GoStorm's port 8090 (every route except `GET /echo`, header
only, no `?token=`); external clients of 8090 must send it. There is no TLS:
see `SECURITY.md`.

`GET /api/health` on the control port reports the mount, the engine and the
state database, each as `{"ok": ..., "reason": "..."}`, with an overall
`status` of `ok` (HTTP 200), `degraded` (engine or state database down) or
`down` (mount down), the last two with HTTP 503. It is open even when a token
is set and returns status words only. The image's health check runs
`flodni healthcheck`, which requests it on `127.0.0.1:<metrics_port>` and
exits 0 on HTTP 200 and 1 otherwise; run it with
`docker exec <container> flodni healthcheck`.

## Viewing the model

`make check` validates the architecture model and `make view` serves it at
<http://localhost:8080/workspace/1>. Both need Docker. `make docs` checks
that the documentation agrees with the tree.

## Contributing

Pull requests are opened by the maintainer only. Issues are welcome; see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

GPL-3.0, see [LICENSE.md](LICENSE.md). Tiramisu is copyright Matteo
Rancilio; third-party components are listed in [NOTICE](NOTICE).
