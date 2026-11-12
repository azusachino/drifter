FROM golang:1.27-bookworm AS build

WORKDIR /src
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/drifter-server ./cmd/server

# Same shared runtime as the other workstation services (containers/media-base
# in harus-workstation): Debian-based, floating patch levels, refreshed via
# REFRESH=1 rebuilds. media-base already carries ca-certificates and the
# glibc/libgcc the CGO binary links against.
FROM azusachino.com/media-base:py3.14-trixie-r1 AS runtime
RUN groupadd --gid 10001 drifter \
    && useradd --uid 10001 --gid drifter --no-create-home --home-dir /var/lib/drifter --shell /usr/sbin/nologin drifter \
    && install -d --owner=drifter --group=drifter --mode=0700 /var/lib/drifter
COPY --from=build --chown=drifter:drifter /out/drifter-server /usr/local/bin/drifter-server
USER 10001:10001
WORKDIR /var/lib/drifter
ENTRYPOINT ["/usr/local/bin/drifter-server"]
CMD ["--config", "/etc/drifter/config.toml", "--db", "/var/lib/drifter/mappings.sqlite"]
