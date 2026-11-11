FROM golang:1.26-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config libfuse3-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY . .

# Use Docker's automatic TARGETARCH variable for multi-arch support
ARG TARGETARCH
ARG APP_VERSION=dev
# CGO_ENABLED=1 is required for FUSE integration.
# -pgo=auto ensures we use profile-guided optimization if default.pgo is present.
RUN CGO_ENABLED=1 GOOS=linux GOARCH=$TARGETARCH go build -pgo=auto -ldflags "-X main.AppVersion=${APP_VERSION}" -o /out/flodni .

# End-to-end test seeder (tests/e2e). Built only on request (--target e2e-seeder);
# the runtime stage below stays last so the default target is the shipped image.
FROM build AS e2e-seeder
ARG TARGETARCH
RUN CGO_ENABLED=1 GOOS=linux GOARCH=$TARGETARCH go build -o /out/seeder ./tests/e2e/seeder
ENTRYPOINT ["/out/seeder"]

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates \
      fuse3 \
      tini \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /out/flodni /usr/local/bin/flodni
COPY docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh \
 && mkdir -p /state

ENV MKV_PROXY_CONFIG_PATH=/config.json
# Everything that must outlive the container lives under /state. It is deliberately
# a plain directory and not a VOLUME: an anonymous volume looks mounted, so the
# entrypoint's persistence check would pass while "docker rm" still loses the data.
# The path defaults (/state, /mnt/flodni-real, /mnt/flodni-virtual) live in
# docker-entrypoint.sh, which reads FLODNI_*, then the older TIRAMISU_* and
# GOSTREAM_* names. They are not baked in as ENV here: an image-level FLODNI_*
# value would win over TIRAMISU_* set by an existing deployment.

EXPOSE 8090 9080

# `flodni healthcheck` asks GET /api/health on the control port: the mount, the
# engine and the state database. The start period covers mount and engine start.
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
  CMD ["/usr/local/bin/flodni", "healthcheck"]

# tini is used as PID 1 to forward signals (e.g. SIGTERM on docker stop) to
# flodni and reap zombie processes. Without it, the shell entrypoint script
# would be PID 1 and would not forward signals, causing an unclean shutdown.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/docker-entrypoint.sh"]
