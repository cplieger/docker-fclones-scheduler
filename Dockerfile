# check=error=true

# FCLONES_VERSION is the single source of truth for the pinned fclones release.
# Declared as a global ARG (before the first FROM) so every build stage consumes
# it with a bare `ARG FCLONES_VERSION`; Renovate bumps this one line.
# renovate: datasource=github-tags depName=pkolaczk/fclones
ARG FCLONES_VERSION=v0.35.0

FROM rust:1.97-trixie@sha256:c396edf290de1f8c0dd88565b76a7726486bc3dc2279236c23f36159c0924110 AS fclones-builder

WORKDIR /usr/src/fclones
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# The cross-compilation toolchain and musl target below are used only by the
# arm64 branch, which builds fclones from source. The amd64 branch downloads a
# prebuilt musl binary and needs none of them (see the arch split below).
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    musl-tools \
    cmake \
    gcc-aarch64-linux-gnu \
    libc6-dev-arm64-cross \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add aarch64-unknown-linux-musl
ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=aarch64-linux-gnu-gcc \
    CC_aarch64_unknown_linux_musl=aarch64-linux-gnu-gcc
# Consume the global FCLONES_VERSION (declared before the first FROM with its
# renovate datasource comment) in this stage.
ARG FCLONES_VERSION
# Integrity pins -- re-verify on every version bump (stale pin fail-closes the build).
# amd64: sha256 of fclones-<version>-linux-musl-x86_64.tar.gz
ARG FCLONES_SHA256_AMD64=9eae0466e5b78871cf25822e503ee9efbfa28dc36cc167060c4a4920306389ac
# arm64: commit that the FCLONES_VERSION tag dereferences to (git tags are mutable; pin the commit)
ARG FCLONES_COMMIT=a74f90d293e05856d19a4c0ac2b29b46ef16cf23
RUN VERSION="${FCLONES_VERSION#v}" && \
    ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then \
      curl -fsSL --connect-timeout 10 --max-time 120 --retry 3 --retry-delay 5 -o /tmp/fclones.tar.gz \
        "https://github.com/pkolaczk/fclones/releases/download/${FCLONES_VERSION}/fclones-${VERSION}-linux-musl-x86_64.tar.gz" && \
      { printf '%s  /tmp/fclones.tar.gz\n' "${FCLONES_SHA256_AMD64}" | sha256sum -c - || { \
        echo "fclones amd64 sha256 pin mismatch: fclones-${VERSION}-linux-musl-x86_64.tar.gz does not match FCLONES_SHA256_AMD64=${FCLONES_SHA256_AMD64}; recompute the sha256 of the new release asset and update ARG FCLONES_SHA256_AMD64 (and FCLONES_COMMIT) for the new version -- see CONTRIBUTING.md" >&2; \
        exit 1; \
      }; } && \
      tar xz --strip-components=3 -C /usr/src/fclones -f /tmp/fclones.tar.gz && \
      rm -f /tmp/fclones.tar.gz; \
    elif [ "$ARCH" = "arm64" ]; then \
      git clone --branch "${FCLONES_VERSION}" --depth 1 https://github.com/pkolaczk/fclones.git . && \
      { [ "$(git rev-parse HEAD)" = "${FCLONES_COMMIT}" ] || { \
          echo "fclones arm64 commit pin mismatch: ${FCLONES_VERSION} dereferences to $(git rev-parse HEAD) but FCLONES_COMMIT=${FCLONES_COMMIT}; update ARG FCLONES_COMMIT (and FCLONES_SHA256_AMD64) for the new version -- see CONTRIBUTING.md" >&2; \
          exit 1; \
        }; } && \
      cargo build --locked --release --target aarch64-unknown-linux-musl && \
      mv target/aarch64-unknown-linux-musl/release/fclones /usr/src/fclones/fclones; \
    else \
      echo "unsupported build architecture: ${ARCH} (expected amd64 or arm64); no integrity pin defined" >&2; \
      exit 1; \
    fi

FROM golang:1.26-trixie@sha256:c5fa560ff09f181211b5dc09158b5ac08c05fe379a7f8f083ac618386098f602 AS go-builder
ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY *.go ./
COPY internal/ internal/
# Fail the build if config.go's dangerous-flag denylist audit comment does not
# match the pinned fclones version. The amd64 sha256 and arm64 commit pins
# already fail-close the build on a stale version; this extends the same
# fail-closed coupling to the security re-audit, so a FCLONES_VERSION bump
# cannot silently ship an un-re-audited denylist.
ARG FCLONES_VERSION
RUN grep -qF "Audited against fclones ${FCLONES_VERSION};" config.go || { \
      echo "config.go dangerous-flag audit comment does not match FCLONES_VERSION=${FCLONES_VERSION}; re-audit dangerousFlags and bump the 'Audited against fclones <version>;' comment in config.go (see CONTRIBUTING.md)" >&2; \
      exit 1; \
    }
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wrapper .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:d29e660cc75a5b6b1334e03c5c81ccf9bc0884a002c6000dbf0fb96034814478

WORKDIR /app
COPY --chmod=755 --from=fclones-builder /usr/src/fclones/fclones /usr/bin/fclones
COPY --chmod=755 --from=go-builder /wrapper /app/wrapper
# XDG_CACHE_HOME puts fclones' cache on the persistent /cache volume instead of
# ephemeral container storage.
# HOME=/tmp gives any operator-chosen UID a writable home for tools that consult $HOME;
# distroless ships /tmp world-writable (1777) so that succeeds for any UID.
# The wrapper writes its fclones report to a temp file under /cache
# (os.CreateTemp(cacheDir, ...)), the operator-mounted volume it already
# requires to be writable -- not to /tmp.
# PATH lets the wrapper resolve fclones by name.
ENV XDG_CACHE_HOME="/cache" \
    HOME="/tmp" \
    PATH="/usr/bin:$PATH"
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=15s \
    CMD ["/app/wrapper", "health"]
ENTRYPOINT ["/app/wrapper"]
