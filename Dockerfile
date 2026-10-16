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

# Stage 3: minimal non-root runtime.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
# Documentation only (EXPOSE does not publish). The runtime port is set via APP_PORT env.
ARG APP_PORT=8080
EXPOSE ${APP_PORT}
USER nonroot:nonroot
ENTRYPOINT ["/server"]
