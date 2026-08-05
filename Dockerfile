# Multi-stage build for the lfpweather-agent runtime.

# STAGE 1 — build the binary, cross-compiling to TARGETARCH from the native
# BUILDPLATFORM so the toolchain runs without emulation.
FROM --platform=$BUILDPLATFORM golang:1-bookworm AS build

ARG VERSION=dev
ARG PKG_VER_PATH=github.com/michaelpeterswa/lfpweather-agent/internal/config.AppVersion

WORKDIR /var/build/go
ARG TARGETARCH
ENV GOARCH=$TARGETARCH
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY ./ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -v -trimpath \
        -ldflags "-s -w -X ${PKG_VER_PATH}=${VERSION}" \
        -o /var/build/bin/ ./...

# STAGE 2 — a minimal runtime with CA certificates for the HTTPS call to the
# Anthropic API and tzdata for Pacific-time reasoning.
FROM debian:bookworm AS runtime

# hadolint ignore=DL3008
RUN apt-get update --fix-missing && \
    apt-get install -yqq --no-install-recommends \
        ca-certificates \
        tzdata \
        && \
    apt-get clean -yqq && \
    rm -rf /var/lib/apt/lists/*

ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/michaelpeterswa/lfpweather-agent" \
      org.opencontainers.image.description="Conversational agent runtime for lfpweather.com, backed by the lfpweather-mcp server" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /var/build/bin/* /usr/local/bin/

# HTTP chat + health port; override with PORT.
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/lfpweather-agent"]
