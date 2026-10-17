# syntax=docker/dockerfile:1.7
#
# Multi-stage, multi-arch build. The web and Go stages run on the BUILD platform
# (no QEMU emulation): the UI is architecture independent and Go cross-compiles
# via GOOS/GOARCH. Only the final FROM scratch stage is per target platform.
#
#   docker build -t firehose .
#   docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/cicerbro/firehose:dev .

# ---- 1. Frontend (Vite + TypeScript, built with Bun) -> web/dist ----
FROM --platform=$BUILDPLATFORM oven/bun:1.4-alpine AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
# Shared Go/TS test vectors live outside web/; tests/ is part of the tsc include.
COPY testdata/ /src/testdata/
RUN bun --bun run build

# ---- 2. Go binary (embeds web/dist) ----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/firehose ./cmd/firehose

# ---- 3. Runtime ----
FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="firehose" \
      org.opencontainers.image.description="Self-hosted network speed test (server + CLI client)" \
      org.opencontainers.image.source="https://github.com/CicerBro/firehose" \
      org.opencontainers.image.version="${VERSION}"
# CA bundle for the `client` subcommand (HTTPS to a firehose server).
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/firehose /firehose
USER 65534:65534
# Listen on the container interface so published ports can reach the server.
# Host-network deployments can override this with 127.0.0.1:8080.
ENV FIREHOSE_LISTEN=0.0.0.0:8080
EXPOSE 8080 50000/udp
ENTRYPOINT ["/firehose"]
CMD ["serve"]
