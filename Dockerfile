# check=error=true

# The pinned fclones release tag and the commit it dereferences to. Global
# ARGs, so each stage consumes them with a bare ARG; Renovate moves the pair
# together, and the marker must stay directly above the two lines.
# renovate: datasource=github-tags depName=pkolaczk/fclones digest=commit
ARG FCLONES_REF=v0.35.0
ARG FCLONES_COMMIT=a74f90d293e05856d19a4c0ac2b29b46ef16cf23

FROM rust:1.99-trixie@sha256:6ff07edce8775d0f64be7aba9197229407301bddf2054d62c27b541a6238a181 AS fclones-builder

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
    jq \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add aarch64-unknown-linux-musl
ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=aarch64-linux-gnu-gcc \
    CC_aarch64_unknown_linux_musl=aarch64-linux-gnu-gcc
ARG FCLONES_REF
ARG FCLONES_COMMIT
# Integrity pins -- a stale pin fail-closes the build. The repin postUpgradeTask
# recomputes each sha256 in the version-bump PR from the URL in its marker.
# repin: dep=pkolaczk/fclones url=https://github.com/pkolaczk/fclones/releases/download/{version}/fclones-{version_nov}-linux-musl-x86_64.tar.gz
ARG FCLONES_SHA256_AMD64=9eae0466e5b78871cf25822e503ee9efbfa28dc36cc167060c4a4920306389ac
# The release tarball holds the binary and nothing else, so amd64 fetches the license
# text at the pinned tag; arm64 takes it from the clone the commit pin verifies.
# repin: dep=pkolaczk/fclones url=https://raw.githubusercontent.com/pkolaczk/fclones/{version}/LICENSE
ARG FCLONES_LICENSE_SHA256=fa876876689ee8c7ad01e3d332f53502bece80de772d4239dced8f3398014000
COPY scripts/collect-cargo-licenses.sh /usr/local/bin/
# The committed crate license set. amd64 ships it as-is (a prebuilt binary leaves no
# crate sources to collect from); arm64 collects its own and diffs the two.
COPY licenses/crates/ /licenses/crates/
# SC2034 is a false positive here: hadolint's shellcheck does not read the
# heredoc body below, which is the only consumer of $provenance (measured on
# hadolint 2.15.1 with a 3-line Dockerfile).
# hadolint ignore=SC2034
RUN VERSION="${FCLONES_REF#v}" && \
    ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then \
      url="https://github.com/pkolaczk/fclones/releases/download/${FCLONES_REF}/fclones-${VERSION}-linux-musl-x86_64.tar.gz" && \
      provenance="download_url=${url}&checksum=sha256:${FCLONES_SHA256_AMD64}" && \
      curl -fsSL --connect-timeout 10 --max-time 120 --retry 3 --retry-delay 5 -o /tmp/fclones.tar.gz "$url" && \
      { printf '%s  /tmp/fclones.tar.gz\n' "${FCLONES_SHA256_AMD64}" | sha256sum -c - || { \
        echo "fclones amd64 sha256 pin mismatch: fclones-${VERSION}-linux-musl-x86_64.tar.gz does not match FCLONES_SHA256_AMD64=${FCLONES_SHA256_AMD64}; a Renovate bump recomputes it from its repin marker, a hand bump must recompute it -- see CONTRIBUTING.md" >&2; \
        exit 1; \
      }; } && \
      tar xz --strip-components=3 -C /usr/local/bin -f /tmp/fclones.tar.gz && \
      rm -f /tmp/fclones.tar.gz && \
      curl -fsSL --connect-timeout 10 --max-time 60 --retry 3 --retry-delay 5 -o /tmp/fclones-LICENSE "https://raw.githubusercontent.com/pkolaczk/fclones/${FCLONES_REF}/LICENSE" && \
      { printf '%s  /tmp/fclones-LICENSE\n' "${FCLONES_LICENSE_SHA256}" | sha256sum -c - || { \
        echo "fclones LICENSE sha256 pin mismatch: ${FCLONES_REF}/LICENSE does not match FCLONES_LICENSE_SHA256=${FCLONES_LICENSE_SHA256}; a Renovate bump recomputes it from its repin marker, a hand bump must recompute it -- see CONTRIBUTING.md" >&2; \
        exit 1; \
      }; } && \
      install -D -m 644 /tmp/fclones-LICENSE /out/usr/share/licenses/fclones/LICENSE && \
      for crate in /licenses/crates/*/; do cp -R "$crate" /out/usr/share/licenses/; done; \
    elif [ "$ARCH" = "arm64" ]; then \
      provenance="vcs_url=git%2Bhttps://github.com/pkolaczk/fclones.git%40${FCLONES_COMMIT}" && \
      git clone --branch "${FCLONES_REF}" --depth 1 https://github.com/pkolaczk/fclones.git . && \
      { [ "$(git rev-parse HEAD)" = "${FCLONES_COMMIT}" ] || { \
          echo "fclones arm64 commit pin mismatch: ${FCLONES_REF} dereferences to $(git rev-parse HEAD) but FCLONES_COMMIT=${FCLONES_COMMIT}; the tag moved upstream or the pair was edited apart, and both must name one commit (git ls-remote https://github.com/pkolaczk/fclones.git refs/tags/${FCLONES_REF}^{}) -- see CONTRIBUTING.md" >&2; \
          exit 1; \
        }; } && \
      cargo build --locked --release --target aarch64-unknown-linux-musl && \
      mv target/aarch64-unknown-linux-musl/release/fclones /usr/local/bin/fclones && \
      install -D -m 644 LICENSE /out/usr/share/licenses/fclones/LICENSE && \
      sh /usr/local/bin/collect-cargo-licenses.sh --out /out/usr/share/licenses --fallback /licenses/crates && \
      cut -d' ' -f1 /licenses/crates/MANIFEST | sort -u >/tmp/manifest-crates && \
      find /out/usr/share/licenses -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | grep -vx fclones | sort -u >/tmp/collected-crates && \
      { diff -u /tmp/manifest-crates /tmp/collected-crates || { \
        echo "the crates this build collected differ from licenses/crates/MANIFEST (- manifest, + collected); run scripts/vendor-crate-licenses.sh and commit the result" >&2; \
        exit 1; \
      }; }; \
    else \
      echo "unsupported build architecture: ${ARCH} (expected amd64 or arm64); no integrity pin defined" >&2; \
      exit 1; \
    fi && \
    # SBOM fragment: Syft cannot see a plain Rust release binary in a distroless
    # image, so this CycloneDX file makes fclones visible to the signed release
    # SBOM and to scanners (the release pipeline enables Syft's sbom-cataloger).
    # pkg:cargo keys advisory matching to the RustSec/GHSA crates ecosystem;
    # neither arch fetches fclones from crates.io. ${provenance} names the fetch
    # this shell verified: amd64 the release asset and its sha256, arm64 the
    # clone at its commit and no checksum, since a commit is not a content
    # digest. No cpe: NVD has no fclones entry (checked 2026-07-22).
    cat > /usr/src/fclones-scheduler.cdx.json <<EOF
{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "version": 1,
  "components": [
    {
      "bom-ref": "pkg:cargo/fclones@${FCLONES_REF#v}",
      "type": "application",
      "name": "fclones",
      "version": "${FCLONES_REF#v}",
      "purl": "pkg:cargo/fclones@${FCLONES_REF#v}?${provenance}"
    }
  ]
}
EOF

FROM golang:1.27-trixie@sha256:22b64c486d44847387a2d9591bb705dc4b3a1227bb393d76a9d4ae176d046327 AS go-builder
ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY *.go ./
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wrapper .
COPY LICENSE NOTICE ./
COPY scripts/collect-licenses.sh scripts/
RUN --mount=type=cache,target=/go/pkg/mod \
    sh scripts/collect-licenses.sh --name docker-fclones-scheduler .

# Option snapshot gate: fails when this fclones adds or removes an option
# against internal/fclonesflags/flags.txt, naming each one. The final stage
# copies the fclones binary from here, so the gate is on every arch's build.
FROM go-builder AS flags-test
COPY --from=fclones-builder /usr/local/bin/fclones /usr/local/bin/fclones
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    FCLONES_BIN=/usr/local/bin/fclones go test -count=1 -run '^TestBinaryMatchesSnapshot$' ./internal/fclonesflags

# ---------------------------------------------------------------------------
# SBOM test stage — asserts the embedded CycloneDX fragment ships correct
# (exists, JSON-object-shaped, names fclones at the ARG-derived version) and
# pins the final-stage COPY directive, because the distroless final stage has
# no shell to assert in (docker-static-web's scratch pattern). The final
# stage COPYs the fragment from THIS stage, so a failing assertion fails the
# centralized `ci / validate` docker build gate.
# ---------------------------------------------------------------------------
FROM fclones-builder AS sbom-test
ARG FCLONES_REF
COPY Dockerfile /tmp/Dockerfile
COPY tests/sbom-smoke.sh /tmp/tests/sbom-smoke.sh
# ${FCLONES_REF:?} fails the build if the ARG wiring ever breaks, so the
# smoke test's exact-version assertion can never be skipped in-image.
RUN FCLONES_EXPECTED_VERSION="${FCLONES_REF:?}" \
    DOCKERFILE=/tmp/Dockerfile \
    SBOM_FRAGMENT=/usr/src/fclones-scheduler.cdx.json \
    sh /tmp/tests/sbom-smoke.sh

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

WORKDIR /app
COPY --chmod=755 --from=flags-test /usr/local/bin/fclones /usr/bin/fclones
COPY --chmod=755 --from=go-builder /wrapper /app/wrapper
COPY --from=fclones-builder /out/usr/share/licenses /usr/share/licenses
COPY --from=go-builder /out/usr/share/licenses /usr/share/licenses
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
