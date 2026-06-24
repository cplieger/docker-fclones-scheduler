# check=error=true
FROM rust:1.96-trixie@sha256:e7336b1e0bb2290b0d7bfd3ce1237bf11e5c2ae937ee3e250e6554b98338bea6 AS fclones-builder

WORKDIR /usr/src/fclones
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    musl-tools \
    cmake \
    git \
    gcc-aarch64-linux-gnu \
    libc6-dev-arm64-cross \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add aarch64-unknown-linux-musl
ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=aarch64-linux-gnu-gcc \
    CC_aarch64_unknown_linux_musl=aarch64-linux-gnu-gcc \
    CXX_aarch64_unknown_linux_musl=aarch64-linux-gnu-g++
# renovate: datasource=github-tags depName=pkolaczk/fclones
ARG FCLONES_VERSION=v0.35.0
RUN VERSION="${FCLONES_VERSION#v}" && \
    ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then \
      curl -fsSL "https://github.com/pkolaczk/fclones/releases/download/${FCLONES_VERSION}/fclones-${VERSION}-linux-musl-x86_64.tar.gz" \
        | tar xz --strip-components=3 -C /usr/src/fclones; \
    else \
      git clone --branch ${FCLONES_VERSION} --depth 1 https://github.com/pkolaczk/fclones.git . && \
      cargo build --release --target aarch64-unknown-linux-musl && \
      mv target/aarch64-unknown-linux-musl/release/fclones /usr/src/fclones/fclones; \
    fi

FROM golang:1.26-trixie@sha256:a35623f66647055b29b6e7d6e57d6aff02c7fcbc1aa3f81625bebb4e55563514 AS go-builder
ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wrapper .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240

WORKDIR /app
COPY --chmod=755 --from=fclones-builder /usr/src/fclones/fclones /usr/bin/fclones
COPY --chmod=755 --from=go-builder /wrapper /app/wrapper
ENV XDG_CACHE_HOME="/cache" \
    HOME="/tmp" \
    PATH="/usr/bin:$PATH"
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=15s \
    CMD ["/app/wrapper", "health"]
ENTRYPOINT ["/app/wrapper"]
