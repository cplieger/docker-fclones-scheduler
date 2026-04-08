# check=error=true
FROM --platform=$BUILDPLATFORM rust:1.94-trixie@sha256:dbc91e219681fe9916c23882ca9b4b7b0485951c818a6781b02c889a30fd4e14 AS fclones-builder

WORKDIR /usr/src/fclones
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
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
ARG TARGETARCH
RUN VERSION="${FCLONES_VERSION#v}" && \
    if [ "$TARGETARCH" = "amd64" ]; then \
      curl -fsSL "https://github.com/pkolaczk/fclones/releases/download/${FCLONES_VERSION}/fclones-${VERSION}-linux-musl-x86_64.tar.gz" \
        | tar xz --strip-components=3 -C /usr/src/fclones; \
    else \
      git clone --branch ${FCLONES_VERSION} --depth 1 https://github.com/pkolaczk/fclones.git . && \
      cargo build --release --target aarch64-unknown-linux-musl && \
      mv target/aarch64-unknown-linux-musl/release/fclones /usr/src/fclones/fclones; \
    fi

FROM --platform=$BUILDPLATFORM golang:1.26-trixie@sha256:503c84fd135f0d9bb9fb3be1c9ad0524fdba1d06ff81c79ab1d013cf474abe68 AS go-builder
ENV GOTOOLCHAIN=auto

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o wrapper main.go

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e3f945647ffb95b5839c07038d64f9811adf17308b9121d8a2b87b6a22a80a39

WORKDIR /app
COPY --chmod=755 --from=fclones-builder /usr/src/fclones/fclones /usr/bin/fclones
COPY --chmod=755 --from=go-builder /app/wrapper /app/wrapper
ENV XDG_CACHE_HOME="/cache" \
    HOME="/tmp" \
    PATH="/usr/bin:$PATH"
ENTRYPOINT ["/app/wrapper"]
