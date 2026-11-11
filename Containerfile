FROM golang:1.27-alpine AS build

WORKDIR /src
RUN apk add --no-cache gcc musl-dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/drifter-server ./cmd/server
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/drifter ./cmd/cli

FROM alpine:3.24 AS runtime
ARG SOURCE_REVISION
LABEL org.opencontainers.image.title="drifter" \
      org.opencontainers.image.description="Headless reading-status companion bridging WeRead to Hardcover" \
      org.opencontainers.image.source="https://github.com/azusachino/drifter" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"

# ca-certificates is required for outbound HTTPS (WeRead, Hardcover, Douban, LLM Advisor);
# tzdata ensures timezone handling matches host/cluster specs;
# tini is PID 1 so signals reach the Go process and no zombies accumulate.
RUN apk add --no-cache ca-certificates tini tzdata \
    && addgroup -g 10001 -S drifter \
    && adduser -u 10001 -S -G drifter -h /var/lib/drifter drifter \
    && install -d -o drifter -g drifter -m 0700 /var/lib/drifter

COPY --from=build --chown=drifter:drifter /out/drifter-server /usr/local/bin/drifter-server
COPY --from=build --chown=drifter:drifter /out/drifter /usr/local/bin/drifter

USER 10001:10001
WORKDIR /var/lib/drifter
EXPOSE 8081

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/drifter-server"]
CMD ["--config", "/etc/drifter/config.toml", "--db", "/var/lib/drifter/mappings.sqlite"]

