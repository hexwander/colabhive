# syntax=docker/dockerfile:1.7
# ==============================================================================
# ColabHive Multi-Stage Production Dockerfile (Cloud Team Server Ready)
#   Stage 1: Node 22 + pnpm builds Frontend Observatory, Workbench & Portal SPA
#   Stage 2: Go (CGO_ENABLED=0) builds static `colabhive` + `colab` CLI binaries
#   Stage 3: Minimal non-root Alpine runtime (`--mode cloud --trust-proxy`)
# ==============================================================================

# ------------------------------------------------------------------------------
# Stage 1 — Build Frontend SPA (web/dist)
# ------------------------------------------------------------------------------
FROM node:22-alpine AS web-builder

RUN corepack enable && corepack prepare pnpm@latest --activate
WORKDIR /src/web

COPY web/package.json web/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile || pnpm install

COPY web/ ./
RUN pnpm run build

# ------------------------------------------------------------------------------
# Stage 2 — Build Static Go Binaries (`colabhive` & `colab` drop-in CLI)
# ------------------------------------------------------------------------------
FROM golang:alpine AS go-builder

ARG VERSION=1.0.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
COPY --from=web-builder /src/web/dist ./web/dist

RUN mkdir -p /out && \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/colabhive \
      ./cmd/colabhive && \
    if [ -d "./cmd/colab" ]; then \
      go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
        -o /out/colab \
        ./cmd/colab; \
    else \
      ln -s colabhive /out/colab; \
    fi

# ------------------------------------------------------------------------------
# Stage 3 — Minimal Non-Root Cloud Runtime Image
# ------------------------------------------------------------------------------
FROM alpine:3.21 AS runtime

LABEL org.opencontainers.image.title="ColabHive" \
      org.opencontainers.image.description="Multi-account Google Colab GPU observatory, parallel shard runner & AI Agent MCP/CLI router" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/colabhive/colabhive"

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 -S colabhive && \
    adduser -u 10001 -S -G colabhive -h /data colabhive && \
    mkdir -p /data /data/artifacts /app/web/dist && \
    chown -R colabhive:colabhive /data /app

COPY --from=go-builder /out/ /usr/local/bin/
COPY --from=web-builder --chown=colabhive:colabhive /src/web/dist /app/web/dist

# Cloud-friendly production defaults (override COLABHIVE_MODE=local for trusted LAN/desktop)
ENV COLABHIVE_MODE=cloud \
    COLABHIVE_TRUST_PROXY=true \
    COLABHIVE_HOST=0.0.0.0 \
    COLABHIVE_PORT=7860 \
    COLABHIVE_DATA_DIR=/data \
    COLABHIVE_DB_PATH=/data/colabhive.db \
    COLABHIVE_WEB_DIR=/app/web/dist \
    COLABHIVE_DEFAULT_STRATEGY=waste-zero \
    COLABHIVE_KEEPALIVE_INTERVAL_SEC=8

VOLUME ["/data"]
EXPOSE 7860

USER colabhive
WORKDIR /data

# `/api/v1/health` remains unauthenticated even when COLABHIVE_ADMIN_TOKEN is enforced
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -qO- http://127.0.0.1:${COLABHIVE_PORT:-7860}/api/v1/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["colabhive"]
CMD ["serve", "--trust-proxy"]
