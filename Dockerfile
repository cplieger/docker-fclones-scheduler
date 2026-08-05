# check=error=true

# FCLONES_VERSION is the single source of truth for the pinned fclones release.
# Declared as a global ARG (before the first FROM) so every build stage consumes
# it with a bare `ARG FCLONES_VERSION`; Renovate bumps this one line.
# renovate: datasource=github-tags depName=pkolaczk/fclones
ARG FCLONES_VERSION=v0.35.0

FROM rust:1.97-trixie@sha256:f1400ab14caacbb8a2c4a9730718a737499d930e9e59cc3d6890ae428b4edf0b AS fclones-builder

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
# Integrity pins -- a stale pin fail-closes the build. The amd64 sha256 is
# recomputed in the version-bump PR itself by the repin postUpgradeTask, which
# reads the marker below for the release-asset URL.
# repin: dep=pkolaczk/fclones url=https://github.com/pkolaczk/fclones/releases/download/{version}/fclones-{version_nov}-linux-musl-x86_64.tar.gz
ARG FCLONES_SHA256_AMD64=9eae0466e5b78871cf25822e503ee9efbfa28dc36cc167060c4a4920306389ac
# arm64: commit that the FCLONES_VERSION tag dereferences to (git tags are
# mutable; pin the commit). No script moves this one -- recompute it by hand on
# every version bump:
#   git ls-remote https://github.com/pkolaczk/fclones.git "refs/tags/<version>^{}"
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

# ---------------------------------------------------------------------------
# Embedded SBOM fragment. The final image is distroless static (no OS package
# DB) and fclones is a plain Rust release build (not cargo-auditable), so
# Syft sees the Go wrapper via its embedded buildinfo but /usr/bin/fclones is
# invisible to the signed release SBOM and to vulnerability scanners.
# Generate a CycloneDX fragment from the same Renovate-tracked
# FCLONES_VERSION ARG the build pins — a Renovate bump keeps the SBOM correct
# with zero extra maintenance — and ship it in the final image (see the COPY
# there) where Syft's sbom-cataloger picks it up. The cataloger is enabled
# centrally by the release pipeline (cplieger/ci); no per-repo .syft.yaml is
# needed.
# purl: pkg:cargo/fclones — fclones IS the crates.io-published crate of the
# same name (bin name `fclones`, published by upstream pkolaczk), and the
# cargo purl type keys scanners into the RustSec/GHSA crates ecosystem, the
# strongest advisory matching available for a Rust payload; a pkg:github
# purl would carry forge provenance but match almost no advisory data. The
# version is identical on both provenance paths — amd64 ships the upstream
# prebuilt musl release tarball, arm64 builds the same release from source
# at the pinned FCLONES_COMMIT — so this one component covers both.
# cpe: omitted — the NVD CPE dictionary carries no fclones entry as of
# 2026-07-22 (keyword search: 0 products); do not invent one. Add the field
# if NVD ever assigns fclones a CPE.
# ---------------------------------------------------------------------------
RUN cat > /usr/src/fclones-scheduler.cdx.json <<EOF
{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "version": 1,
  "components": [
    {
      "bom-ref": "pkg:cargo/fclones@${FCLONES_VERSION#v}",
      "type": "application",
      "name": "fclones",
      "version": "${FCLONES_VERSION#v}",
      "purl": "pkg:cargo/fclones@${FCLONES_VERSION#v}"
    }
  ]
}
EOF

FROM golang:1.26-trixie@sha256:4ee9ffa999b4583ce281939cdff828763083610292f252279a0cee77473bd9a7 AS go-builder
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

# ---------------------------------------------------------------------------
# SBOM test stage — asserts the embedded CycloneDX fragment ships correct
# (exists, JSON-object-shaped, names fclones at the ARG-derived version) and
# pins the final-stage COPY directive, because the distroless final stage has
# no shell to assert in (docker-static-web's scratch pattern). The final
# stage COPYs the fragment from THIS stage, so a failing assertion fails the
# centralized `ci / validate` docker build gate.
# ---------------------------------------------------------------------------
FROM fclones-builder AS sbom-test
ARG FCLONES_VERSION
COPY Dockerfile /tmp/Dockerfile
COPY tests/sbom-smoke.sh /tmp/tests/sbom-smoke.sh
# ${FCLONES_VERSION:?} fails the build if the ARG wiring ever breaks, so the
# smoke test's exact-version assertion can never be skipped in-image.
RUN FCLONES_EXPECTED_VERSION="${FCLONES_VERSION:?}" \
    DOCKERFILE=/tmp/Dockerfile \
    SBOM_FRAGMENT=/usr/src/fclones-scheduler.cdx.json \
    sh /tmp/tests/sbom-smoke.sh

FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

WORKDIR /app
COPY --chmod=755 --from=fclones-builder /usr/src/fclones/fclones /usr/bin/fclones
COPY --chmod=755 --from=go-builder /wrapper /app/wrapper
# CycloneDX SBOM fragment for the Rust-built fclones payload (generated in
# the fclones-builder stage from the Renovate-tracked version ARG). Placed
# where the release pipeline's Syft sbom-cataloger inventories it, so SBOMs
# and scanners see fclones alongside the Go wrapper's buildinfo. Copied
# --from=sbom-test (not the builder) so that stage's assertions gate the
# shipped file.
COPY --from=sbom-test /usr/src/fclones-scheduler.cdx.json /usr/share/sbom/fclones-scheduler.cdx.json
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
