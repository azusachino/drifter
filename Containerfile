FROM golang:1.27.2-bookworm AS build

WORKDIR /src
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/drifter-server ./cmd/server

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libgcc-s1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 drifter \
    && useradd --uid 10001 --gid drifter --no-create-home --home-dir /var/lib/drifter --shell /usr/sbin/nologin drifter \
    && install -d --owner=drifter --group=drifter --mode=0700 /var/lib/drifter
COPY --from=build --chown=drifter:drifter /out/drifter-server /usr/local/bin/drifter-server
USER 10001:10001
WORKDIR /var/lib/drifter
ENTRYPOINT ["/usr/local/bin/drifter-server"]
CMD ["--config", "/etc/drifter/config.toml", "--db", "/var/lib/drifter/mappings.sqlite"]
