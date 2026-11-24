# syntax=docker/dockerfile:1

ARG NODE_VERSION=22
ARG GO_VERSION=1.27

# -------------------------------
# Stage 1: Build React + Vite SPA
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY web/ ./
RUN npm run build

# -------------------------------
# Stage 2: Compile static Go binary with embedded SPA, migrations, and tzdata
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS go-builder
ARG TARGETOS=linux
ARG TARGETARCH
ARG APP_VERSION=dev
WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web-builder /src/web/dist/ ./internal/web/dist/

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${APP_VERSION}" \
    -o /out/ai-ledger-server ./cmd/ai-ledger-server

RUN mkdir -p /rootfs/data /rootfs/tmp && chmod 1777 /rootfs/tmp

# -------------------------------
# Stage 3: Minimal scratch runtime
FROM scratch

LABEL org.opencontainers.image.title="AI Ledger" \
      org.opencontainers.image.description="Personal, lossless AI usage logger, raw transcript archive, and local monitoring dashboard" \
      org.opencontainers.image.source="https://github.com/lancewilhelm/ai-ledger" \
      org.opencontainers.image.licenses="MIT"

COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-builder /rootfs/data /data
COPY --from=go-builder /rootfs/tmp /tmp
COPY --from=go-builder /out/ai-ledger-server /usr/local/bin/ai-ledger-server

ENV AI_LEDGER_BIND=0.0.0.0:8484 \
    AI_LEDGER_DATA_DIR=/data \
    AI_LEDGER_KEEP_ALL_SNAPSHOTS=true

VOLUME ["/data"]
EXPOSE 8484

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/ai-ledger-server", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/ai-ledger-server"]
CMD ["serve"]
