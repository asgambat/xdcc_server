# ── Build-time arguments for non-root user ────────────────────────
#  Override at build time:   docker build --build-arg UID=1001 --build-arg GID=1001 .
#  Override at runtime with docker-compose:  UID=1001 GID=1001 docker-compose up
ARG UID=1000
ARG GID=1000

# ============================================================
# Stage 1 — Build frontend (Svelte + Vite)
# ============================================================
FROM node:22-alpine AS frontend-builder

WORKDIR /app/web

# Copy frontend source
COPY web/package.json web/package-lock.json* ./
RUN npm ci

COPY web/ ./

# Build to web/dist/
RUN npm run build

# ============================================================
# Stage 2 — Build Go binaries
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

# Re-declare ARGs for this stage (required after FROM)
ARG UID
ARG GID

# Install git and ca-certificates (the latter is needed to copy certs to scratch)
RUN apk add --no-cache git ca-certificates

# Copy Go module files first for better layer caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Copy pre-built frontend from stage 1 (for go:embed)
COPY --from=frontend-builder /app/web/dist ./web/dist

# Build all binaries with static linking for the target platform
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/xdcc-dl ./cmd/xdcc-dl && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/xdcc-search ./cmd/xdcc-search && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/xdcc-browse ./cmd/xdcc-browse && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/xdcc-server ./cmd/xdcc-server

# ── Create non-root user and runtime directories ─────────────────
# The /data and /var/lib/xdcc-server directories are created here so
# that when Docker initializes a named volume for the first time, it
# copies the directory tree with the correct uid:gid ownership.
RUN addgroup -g ${GID} xdcc && \
    adduser -D -u ${UID} -G xdcc xdcc && \
    mkdir -p /data/downloads/tmp \
             /data/downloads/complete \
             /data/logs && \
    mkdir -p /var/lib/xdcc-server/db && \
    mkdir -p /etc/xdcc-server && \
    cp /app/config.yaml /etc/xdcc-server/config.yaml && \
    chown -R xdcc:xdcc /data /var/lib/xdcc-server /etc/xdcc-server

# ============================================================
# Stage 3 — Minimal runtime image (scratch)
# ============================================================
FROM scratch

# Re-declare ARGs for the runtime stage
ARG UID
ARG GID

# Copy CA certificates (needed for HTTPS requests to search APIs)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy user/group files so the xdcc user can be resolved by name
COPY --from=builder /etc/passwd /etc/group /etc/

# Copy all binaries in a single layer
COPY --from=builder /out/ /usr/local/bin/

# Copy default config with correct ownership so the non-root user can save config changes
COPY --chown=${UID}:${GID} --from=builder /etc/xdcc-server /etc/xdcc-server

# Copy runtime directories with correct ownership (uid:gid).
# When a named volume is first mounted, Docker copies the image
# content into it, preserving these permissions.
COPY --chown=${UID}:${GID} --from=builder /data /data
COPY --chown=${UID}:${GID} --from=builder /var/lib/xdcc-server /var/lib/xdcc-server

# Expose HTTP port (REST API + web UI)
EXPOSE 8080

# Declare volumes (directories must exist in the image for Docker
# to copy permissions on first mount — see COPY --chown above)
VOLUME ["/data", "/var/lib/xdcc-server/db"]

# Default config: use /data for all persistent files
ENV XDCC_HTTP_PORT=8080 \
    XDCC_DOWNLOAD_TEMP_DIR=/data/downloads/tmp \
    XDCC_DOWNLOAD_DEST_DIR=/data/downloads/complete \
    XDCC_LOGGING_FILE_PATH=/data/logs/xdcc-server.log \
    XDCC_STORAGE_DB_PATH=/var/lib/xdcc-server/db

# Run as non-root user (overridable at runtime via docker-compose `user:` directive)
USER ${UID}:${GID}

WORKDIR /data

CMD ["/usr/local/bin/xdcc-server", "--config", "/etc/xdcc-server/config.yaml"]
