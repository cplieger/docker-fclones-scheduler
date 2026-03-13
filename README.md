# docker-fclones-scheduler

![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue)
[![GitHub release](https://img.shields.io/github/v/release/cplieger/docker-fclones-scheduler)](https://github.com/cplieger/docker-fclones-scheduler/releases)
[![Image Size](https://ghcr-badge.egpl.dev/cplieger/fclones/size)](https://github.com/cplieger/docker-fclones-scheduler/pkgs/container/fclones)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)

Scheduled duplicate file finder with cron scheduling and health monitoring

## Overview

Wraps the fclones duplicate file finder in a Go scheduler with cron-based
scheduling (via robfig/cron) and a CLI health probe. Supports all fclones
actions (group, link, remove) with configurable arguments. Reports scan
statistics including duplicates found, space reclaimable, and files
processed. All output goes to stdout/stderr for collection by log
aggregators (Alloy, Promtail, etc.) and alerting via Grafana or similar.

**Example use case:** You have a large media library where downloads,
imports, or manual copies have created duplicate files wasting disk space.
Mount your media directory and schedule periodic scans — fclones finds
duplicates and can replace them with hardlinks or remove them entirely.
Pipe container logs to your observability stack for alerting.

This is a distroless, rootless container — it runs as `nonroot` on
`gcr.io/distroless/static` with no shell or package manager.


### How It Differs From fclones

The upstream [fclones](https://github.com/pkolaczk/fclones) is a CLI tool
you run manually. This image adds scheduled execution, structured log
output, health monitoring, and packages everything in a distroless
container. The fclones binary is included — amd64 uses the prebuilt
release, arm64 is cross-compiled from source.

## Container Registries

This image is published to both GHCR and Docker Hub:

| Registry | Image |
|----------|-------|
| GHCR | `ghcr.io/cplieger/fclones` |
| Docker Hub | `docker.io/cplieger/fclones` |

```bash
# Pull from GHCR
docker pull ghcr.io/cplieger/fclones:latest

# Pull from Docker Hub
docker pull cplieger/fclones:latest
```

Both registries receive identical images and tags. Use whichever you prefer.

## Quick Start

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/fclones:latest
    container_name: fclones
    restart: unless-stopped
    user: "1000:1000"  # match your host user
    mem_limit: 512m

    environment:
      TZ: "Europe/Paris"
      FCLONES_SCHEDULE: "0 * * * *"  # cron syntax
      FCLONES_SCAN_PATHS: "/scandir"
      FCLONES_ARGS: "--rf-over 1"
      FCLONES_ACTION: "link"  # group (report), link (hardlink), or remove
      FCLONES_ACTION_ARGS: "--priority bottom"

    volumes:
      - "/path/to/media:/scandir"
      - "/opt/appdata/fclones:/cache"

    healthcheck:
      test:
        - CMD
        - /app/wrapper
        - health
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 15s
```

## Deployment

1. Mount the directory you want to scan for duplicates to `/scandir` (or change `FCLONES_SCAN_PATHS`).
2. Mount a persistent directory to `/cache` for fclones state between scans.
3. Set `FCLONES_SCHEDULE` to a cron expression for how often to scan
   (default: every hour).
4. Set `FCLONES_ACTION` to control what happens with duplicates:
   `group` (report only), `link` (replace with hardlinks), or
   `remove` (delete duplicates).
5. The `FCLONES_ARGS` and `FCLONES_ACTION_ARGS` are passed directly
   to the fclones binary — see
   [fclones documentation](https://github.com/pkolaczk/fclones#usage)
   for all available options.


## Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `TZ` | Container timezone | `Europe/Paris` | No |
| `FCLONES_SCHEDULE` | Cron expression for scan schedule (standard 5-field) | `0 * * * *` | Yes |
| `FCLONES_SCAN_PATHS` | Paths inside the container to scan for duplicates. Must match the volume mounts. Multiple paths can be space-separated (e.g. `/media /photos`), each requiring a corresponding volume mount. | `/scandir` | Yes |
| `FCLONES_ARGS` | Extra arguments passed to `fclones group` scan phase | `--rf-over 1` | No |
| `FCLONES_ACTION` | Dedup action after scan — group (report only), link (hardlink), or remove | `link` | Yes |
| `FCLONES_ACTION_ARGS` | Extra arguments for the dedup action phase | `--priority bottom` | No |


## Volumes

| Mount | Description |
|-------|-------------|
| `/scandir` | Directory to scan for duplicate files. Must match the paths in `FCLONES_SCAN_PATHS`. You can mount multiple directories and list them all in `FCLONES_SCAN_PATHS` (space-separated). |
| `/cache` | fclones cache and state directory |


## Docker Healthcheck

The container includes a built-in Docker healthcheck. After each scan
completes, the main process creates or removes a marker file at
`/tmp/.healthy`. The `health` subcommand checks for this file's existence.

**When it becomes unhealthy:**
- The fclones binary exits with a non-zero code (e.g. scan path doesn't exist, permission denied, corrupted cache)
- The scan is interrupted by a shutdown signal

**When it recovers:**
- The next successful scan recreates the marker file and the container reports healthy again. No restart required.

**On startup:** The container marks itself healthy immediately, then
triggers a startup scan. If that scan fails, it transitions to unhealthy.

To check health manually:
```bash
docker inspect --format='{{json .State.Health.Log}}' fclones | python3 -m json.tool
```

| Type | Command | Meaning |
|------|---------|---------|
| Docker | `/app/wrapper health` | Exit 0 = last scan succeeded |


## Dependencies

All dependencies are updated automatically via [Renovate](https://github.com/renovatebot/renovate) and pinned by digest or version for reproducibility.

| Dependency | Version | Source |
|------------|---------|--------|
| rust | `1.94-trixie` | [Rust](https://hub.docker.com/_/rust) |
| golang | `1.26-trixie` | [Go](https://hub.docker.com/_/golang) |
| gcr.io/distroless/static-debian13 | `nonroot` | [Distroless](https://github.com/GoogleContainerTools/distroless) |
| fclones | `v0.35.0` | [GitHub](https://github.com/pkolaczk/fclones) |

## Design Principles

- **Always up to date**: Base images, packages, and libraries are updated automatically via Renovate. Unlike many community Docker images that ship outdated or abandoned dependencies, these images receive continuous updates.
- **Minimal attack surface**: When possible, pure Go apps use `gcr.io/distroless/static:nonroot` (no shell, no package manager, runs as non-root). Apps requiring system packages use Alpine with the minimum necessary privileges.
- **Digest-pinned**: Every `FROM` instruction pins a SHA256 digest. All GitHub Actions are digest-pinned.
- **Multi-platform**: Built for `linux/amd64` and `linux/arm64`.
- **Healthchecks**: Every container includes a Docker healthcheck.
- **Provenance**: Build provenance is attested via GitHub Actions, verifiable with `gh attestation verify`.

## Credits

This project packages [fclones](https://github.com/pkolaczk/fclones) into a container image. All credit for the core functionality goes to the upstream maintainers.

## Disclaimer

These images are built with care and follow security best practices, but they are intended for **homelab use**. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
