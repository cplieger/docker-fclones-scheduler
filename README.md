# docker-fclones-scheduler

![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue)
[![GitHub release](https://img.shields.io/github/v/release/cplieger/docker-fclones-scheduler)](https://github.com/cplieger/docker-fclones-scheduler/releases)
[![Image Size](https://ghcr-badge.egpl.dev/cplieger/fclones/size)](https://github.com/cplieger/docker-fclones-scheduler/pkgs/container/fclones)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)

Find and deduplicate files on a schedule — reclaim wasted disk space automatically.

## What it does

Wraps the fclones duplicate file finder in a Go scheduler with
interval-based scheduling and a CLI health probe. Supports all fclones
actions (group, link, remove) with configurable arguments. Reports scan
statistics including duplicates found, space reclaimable, and files
processed. All output goes to stdout/stderr for collection by log
aggregators (Alloy, Promtail, etc.) and alerting via Grafana or similar.

- Mount your media directory and schedule periodic scans — fclones finds duplicates and can replace them with hardlinks or remove them entirely
- Pipe container logs to your observability stack for alerting
- Supports all fclones actions: `group` (report only), `link` (replace with hardlinks), `remove` (delete duplicates)
- Configurable scan interval, paths, and fclones arguments
- Built-in Docker healthcheck with automatic recovery

### Why this design

- **Self-contained scheduler** — wraps fclones in a Go-based interval scheduler so you don't need external cron, systemd timers, or orchestrator-level scheduling
- **Distroless and rootless** — runs as `nonroot` (UID 65534) on `gcr.io/distroless/static` with no shell or package manager, minimizing attack surface
- **Dangerous flags blocked by default** — `--command`, `--transform`, `--in-place`, and `--no-copy` are rejected unless you explicitly opt in with `FCLONES_ALLOW_UNSAFE=true`, preventing command injection via environment variables
- **Structured logs for observability** — all output goes to stdout/stderr in a format ready for log aggregators, enabling alerting on scan failures or duplicate detection without custom exporters

## Quick start

The image is published to both GHCR (`ghcr.io/cplieger/fclones`) and Docker Hub (`cplieger/fclones`) — identical contents, use whichever you prefer.

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/fclones:latest
    container_name: fclones
    restart: unless-stopped
    user: "1000:1000"  # match your host user

    environment:
      TZ: "Europe/Paris"
      FCLONES_INTERVAL: "1h"  # Go duration (e.g. 1h, 30m, 12h)
      FCLONES_SCAN_PATHS: "/scandir"
      FCLONES_ARGS: "--rf-over 1"
      FCLONES_ACTION: "link"  # group (report), link (hardlink), or remove
      FCLONES_ACTION_ARGS: "--priority bottom"

    volumes:
      - "/path/to/media:/scandir"
      - "/opt/appdata/fclones:/cache"
```

## Configuration reference

### Environment variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `TZ` | Container timezone | `Europe/Paris` | No |
| `FCLONES_INTERVAL` | Scan interval as a Go duration (e.g. `1h`, `30m`, `12h`). Defaults to `3h` on unset or unparseable values. The first scan runs at startup; subsequent scans fire every interval thereafter. | `1h` | No |
| `FCLONES_SCAN_PATHS` | Paths inside the container to scan for duplicates. Must match the volume mounts. Multiple paths can be space-separated (e.g. `/media /photos`), each requiring a corresponding volume mount. | `/scandir` | No |
| `FCLONES_ARGS` | Extra arguments passed to `fclones group` scan phase | `--rf-over 1` | No |
| `FCLONES_ACTION` | Dedup action after scan — group (report only), link (hardlink), or remove | `link` | No |
| `FCLONES_ACTION_ARGS` | Extra arguments for the dedup action phase | `--priority bottom` | No |
| `FCLONES_ALLOW_UNSAFE` | Set to `true` to allow dangerous flags (`--command`, `--transform`, `--in-place`, `--no-copy`) | `false` | No |

### Volumes

| Mount | Description |
|-------|-------------|
| `/scandir` | Directory to scan for duplicate files. Must match the paths in `FCLONES_SCAN_PATHS`. You can mount multiple directories and list them all in `FCLONES_SCAN_PATHS` (space-separated). |
| `/cache` | fclones cache and state directory |

## Healthcheck

The built-in healthcheck (`/app/wrapper health`) checks for a marker file created after each successful scan and action phase. The container becomes unhealthy when fclones exits non-zero (e.g. scan path missing, permission denied, corrupted cache) or the action phase fails (e.g. hardlink across filesystems). It recovers automatically on the next successful scan — no restart required. On startup the container begins unhealthy and transitions to healthy after the first successful scan completes, so size `healthcheck.start_period` accordingly for large filesystems where the initial scan may take minutes.

## Code quality

| Metric | Value |
|--------|-------|
| [Test Coverage](https://go.dev/blog/cover) | 56.5% |
| Tests | 191 |
| [Cyclomatic Complexity](https://en.wikipedia.org/wiki/Cyclomatic_complexity) (avg) | 4.0 |
| [Cognitive Complexity](https://www.sonarsource.com/docs/CognitiveComplexity.pdf) (avg) | 4.2 |
| [Mutation Efficacy](https://en.wikipedia.org/wiki/Mutation_testing) | 86.3% (59 runs) |
| Test Framework | Property-based ([rapid](https://github.com/flyingmutant/rapid)) + table-driven |

Tests cover argument parsing with shell quoting
and injection prevention, fclones output parsing (stats, duplicates,
whitespace edge cases), config loading and validation, action
allowlisting, dangerous flag rejection (`--command`, `--transform`,
`--in-place`, `--no-copy` blocked by default; opt-in via
`FCLONES_ALLOW_UNSAFE`), file size limits, and the health file lifecycle. Property-based tests
verify that parsing functions never panic on arbitrary input and
that config loading always produces valid actions.

Not tested: `main()` orchestration and `exec.Command` calls to the
fclones binary — these are process-level I/O validated by container
logs and Grafana alerting in production.

## Security

**No vulnerabilities found.** All scans clean across 10 tools.

| Tool | Result |
|------|--------|
| [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | No vulnerabilities in call graph |
| [golangci-lint](https://golangci-lint.run/) (gosec, gocritic) | 0 issues |
| [trivy](https://trivy.dev/) | 0 vulnerabilities |
| [grype](https://github.com/anchore/grype) | 0 vulnerabilities |
| [gitleaks](https://github.com/gitleaks/gitleaks) | No secrets detected |
| [semgrep](https://semgrep.dev/) | 2 info (false positives) |
| [hadolint](https://github.com/hadolint/hadolint) | DL3008 in builder stage (discarded) |

No network listener, no HTTP server, no exposed ports. The
`FCLONES_ACTION` env var is validated against an allowlist and
dangerous flags (`--command`, `--transform`, `--in-place`,
`--no-copy`) are blocked by default to prevent command injection
via env vars. Set `FCLONES_ALLOW_UNSAFE=true` to disable the
guardrails if you need `--transform` for content-aware
deduplication. Runs as `nonroot` on a distroless base image
with no shell.

**Details for advanced users:** Arguments are passed to
`exec.Command` as explicit arg lists (no shell expansion). Temp
files use `os.CreateTemp` with unpredictable names. Output reads
are capped at 50 MB. Concurrent scan requests are guarded by a
mutex. Semgrep flags the distroless nonroot image as "missing
USER" (false positive, UID 65534 is baked in) and the
`/tmp/.healthy` marker (fixed path, single-process container).
Hadolint DL3008 applies to the Rust builder stage only, which is
discarded in the final image.

## Dependencies

| Dependency | Version | Source |
|------------|---------|--------|
| rust | `1.95-trixie` | [Rust](https://hub.docker.com/_/rust) |
| golang | `1.26-trixie` | [Go](https://hub.docker.com/_/golang) |
| gcr.io/distroless/static-debian13 | `nonroot` | [Distroless](https://github.com/GoogleContainerTools/distroless) |
| fclones | `v0.35.0` | [GitHub](https://github.com/pkolaczk/fclones) |

Updated automatically via [Renovate](https://github.com/renovatebot/renovate) and pinned by digest. Builds carry signed SBOMs and provenance attestations verifiable with `gh attestation verify`.

## Credits

This project packages [fclones](https://github.com/pkolaczk/fclones) into a container image. All credit for the core functionality goes to the upstream maintainers.

## Contributing

Issues and pull requests are welcome. Please open an issue first for
larger changes so the approach can be discussed before implementation.

## Disclaimer

These images are built with care and follow security best practices, but they are intended for **homelab use**. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
