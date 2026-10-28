# Stage 1: build the web UI (Nuxt SPA → static web/dist).
# The output is platform-independent, so it always builds natively.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run generate

# Stage 2: build the single Go binary (embeds web/dist).
# Cross-compiles for the target platform instead of running under emulation.
FROM --platform=$BUILDPLATFORM golang:1.25.14-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/server ./cmd/server

# Stage 3a: minimal non-root runtime (lite: no photo gallery).
FROM gcr.io/distroless/static-debian12:nonroot AS runtime-lite
COPY --from=build /out/server /server
# Documentation only (EXPOSE does not publish). The runtime port is set via APP_PORT env.
ARG APP_PORT=8080
EXPOSE ${APP_PORT}
USER nonroot:nonroot
ENTRYPOINT ["/server"]

# Stage 3a': the go-pmtiles CLI (map extracts for the gallery), pinned release with
# pinned SHA-256 sums (upstream publishes none; computed from the release assets).
FROM alpine:3.22 AS pmtiles
ARG TARGETARCH
ARG PMTILES_VERSION=1.31.2
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) a=x86_64; sum=3ed7dbf4ec2e6dfe5e25b6f70d1ffc932729f93c86db353bf514dd71010a312f ;; \
      arm64) a=arm64;  sum=f8bd47e7ea866863489cad588fbaf2f31f42e5821f7a03f009b3769f05801cb1 ;; \
      *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    wget -q -O /tmp/p.tgz "https://github.com/protomaps/go-pmtiles/releases/download/v${PMTILES_VERSION}/go-pmtiles_${PMTILES_VERSION}_Linux_${a}.tar.gz"; \
    echo "${sum}  /tmp/p.tgz" | sha256sum -c -; \
    tar -xzf /tmp/p.tgz -C /usr/local/bin pmtiles

# Stage 3b: full runtime with the media stack for the photo gallery.
# Last stage, so it is the default target. uid/gid 65532 match distroless
# "nonroot", so ownership of /data stays the same across both flavors.
FROM alpine:3.22 AS runtime-full
RUN apk add --no-cache vips-tools vips-heif ffmpeg exiftool ca-certificates tzdata
COPY --from=pmtiles /usr/local/bin/pmtiles /usr/local/bin/pmtiles
COPY --from=build /out/server /server
ARG APP_PORT=8080
EXPOSE ${APP_PORT}
USER 65532:65532
ENTRYPOINT ["/server"]
