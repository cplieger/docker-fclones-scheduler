# check=error=true
FROM --platform=$BUILDPLATFORM rust:1.94-trixie@sha256:7e322aa1b876cbb977e0df46812af6c4e8be2efbfb2ce3712c28a93ba2968726 AS fclones-builder

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

FROM --platform=$BUILDPLATFORM golang:1.26-trixie@sha256:ab8c4944b04c6f97c2b5bffce471b7f3d55f2228badc55eae6cce87596d5710b AS go-builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o wrapper main.go

FROM gcr.io/distroless/static-debian13:nonroot@sha256:f512d819b8f109f2375e8b51d8cfd8aafe81034bc3e319740128b7d7f70d5036

WORKDIR /app
COPY --chmod=755 --from=fclones-builder /usr/src/fclones/fclones /usr/bin/fclones
COPY --chmod=755 --from=go-builder /app/wrapper /app/wrapper
ENV XDG_CACHE_HOME="/cache" \
    HOME="/tmp" \
    PATH="/usr/bin:$PATH"
ENTRYPOINT ["/app/wrapper"]
