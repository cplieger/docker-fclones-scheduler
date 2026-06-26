# docker-fclones-scheduler

[![Image Size](https://ghcr-badge.egpl.dev/cplieger/docker-fclones-scheduler/size)](https://github.com/cplieger/docker-fclones-scheduler/pkgs/container/fclones)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)
[![Go Report Card](https://goreportcard.com/badge/github.com/cplieger/docker-fclones-scheduler)](https://goreportcard.com/report/github.com/cplieger/docker-fclones-scheduler)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/coverage.json)](https://github.com/cplieger/docker-fclones-scheduler/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/mutation.json)](https://github.com/cplieger/docker-fclones-scheduler/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13204/badge)](https://www.bestpractices.dev/projects/13204)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/docker-fclones-scheduler/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/docker-fclones-scheduler)
[![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/docker-fclones-scheduler/releases)

Find and deduplicate files on a schedule — reclaim wasted disk space automatically.

## What it does

Wraps the fclones duplicate file finder in a Go scheduler with
interval-based scheduling and a CLI health probe. Supports the group,
link, remove, and dedupe actions with configurable arguments. Reports scan
statistics including duplicates found, space reclaimable, and files
processed. All output goes to stdout/stderr for collection by log
aggregators (Alloy, Promtail, etc.) and alerting via Grafana or similar.

- Mount your media directory and schedule periodic scans — fclones finds duplicates and can replace them with hardlinks or remove them entirely
- Pipe container logs to your observability stack for alerting
- Supports the `group` (report only), `link` (replace with hardlinks), `remove` (delete duplicates), and `dedupe` (reflink/copy-on-write) actions
- Configurable scan interval, paths, and fclones arguments
- Built-in scheduler, or hand scheduling to an external scheduler (cron, Ofelia, etc.) via the `scan` subcommand
- Built-in Docker healthcheck with automatic recovery

### Why this design

- **Scheduler your way** — ships with a self-contained Go interval scheduler so you don't need external cron, systemd timers, or orchestrator-level scheduling. If you already run a central scheduler (Ofelia, cron), set `FCLONES_INTERVAL=off` and trigger scans with `docker exec fclones /app/wrapper scan` instead
- **Distroless and rootless** — runs as `nonroot` (UID 65534) on `gcr.io/distroless/static-debian13` with no shell or package manager, minimizing attack surface
- **Dangerous flags blocked by default** — `--command`, `--transform`, `--in-place`, and `--no-copy` are rejected unless you explicitly opt in with `FCLONES_ALLOW_UNSAFE=true`, preventing command injection via environment variables
- **Structured logs for observability** — all output goes to stdout/stderr in a format ready for log aggregators, enabling alerting on scan failures or duplicate detection without custom exporters

## Quick start

The image is published to both GHCR (`ghcr.io/cplieger/docker-fclones-scheduler`) and Docker Hub (`cplieger/docker-fclones-scheduler`) — identical contents, use whichever you prefer.

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/docker-fclones-scheduler:latest
    container_name: fclones
    restart: unless-stopped
    user: "1000:1000"  # match your host user

    environment:
      TZ: "Europe/Paris"
      FCLONES_INTERVAL: "1h"  # Go duration (e.g. 1h, 30m, 12h)
      FCLONES_SCAN_PATHS: "/scandir"
      FCLONES_ARGS: "--rf-over 1"
      FCLONES_ACTION: "link"  # group (report), link (hardlink), remove (delete), or dedupe (reflink/copy-on-write)
      FCLONES_ACTION_ARGS: "--priority bottom"

    volumes:
      - "/path/to/media:/scandir"
      - "/opt/appdata/fclones:/cache"
```

## Scheduling modes

The container runs in one of three modes, selected by `FCLONES_INTERVAL`.

### Built-in scheduler (default)

Set `FCLONES_INTERVAL` to a positive Go duration (`1h`, `30m`, `12h`, …). The container runs a scan at startup and then every interval. This is the zero-dependency default; nothing else is required. An unset, unparseable, or negative value falls back to the `3h` default cadence in this mode (a negative value is treated as a typo and logged as a warning).

### External scheduler

Set `FCLONES_INTERVAL=off` (alias: `disabled`). The container stays running but idle, and you trigger each scan out-of-band by exec'ing the `scan` subcommand:

```bash
docker exec fclones /app/wrapper scan
```

The scan runs once and exits; its exit code is non-zero on failure, and it updates the same health marker the long-running container reports. This lets a central scheduler own the cadence. Example with [Ofelia](https://github.com/mcuadros/ofelia) labels:

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/docker-fclones-scheduler:latest
    container_name: fclones
    restart: unless-stopped
    user: "1000:1000"
    environment:
      TZ: "Europe/Paris"
      FCLONES_INTERVAL: "off"   # disable built-in loop; Ofelia drives it
      FCLONES_SCAN_PATHS: "/scandir"
      FCLONES_ACTION: "link"
    labels:
      ofelia.enabled: "true"
      ofelia.job-exec.fclones-scan.schedule: "@every 6h"
      ofelia.job-exec.fclones-scan.command: "/app/wrapper scan"
      ofelia.job-exec.fclones-scan.no-overlap: "true"
    volumes:
      - "/path/to/media:/scandir"
      - "/opt/appdata/fclones:/cache"
```

Overlapping scans are prevented in both modes by an advisory file lock (`flock`) on `/cache/.fclones.lock`, so a manual `docker exec` scan that races a scheduled one will skip rather than corrupt the shared fclones cache. Ofelia's `no-overlap` is still recommended to avoid queuing redundant triggers.

### Run once

Set `FCLONES_INTERVAL=0` (or `0s`). The container runs exactly one scan and dedup action, then exits — non-zero if the scan failed, timed out, was interrupted (SIGTERM/SIGINT) before it finished, **or was skipped because another process held the `/cache` scan lock**. This suits a batch or one-shot context (a Kubernetes `Job`, a CI step, or a manual `docker run --rm`) where an external system, not the container, decides when to run again: a run that did not complete a scan — whether cut short or skipped on lock contention — surfaces as a failed run (logged with `outcome=skipped` for the lock case) so the orchestrator retries rather than recording success. (In the long-running modes a SIGTERM is a clean shutdown and a lock conflict is a benign no-op, both exiting 0; only run-once treats "no scan actually ran" as a failure, because there the exit code is the job result.)

## Configuration reference

### Environment variables

| Variable               | Description                                                                                                                                                                                                                                                                                                                                                                           | Default        | Required |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------- | -------- |
| `TZ`                   | Container timezone                                                                                                                                                                                                                                                                                                                                                                    | `Europe/Paris` | No       |
| `FCLONES_INTERVAL`     | Built-in scan interval as a Go duration (e.g. `1h`, `30m`, `12h`). The first scan runs at startup; subsequent scans fire every interval thereafter. Set to `off` (or `disabled`) to idle and trigger scans externally, or to `0` (or `0s`) to run a single scan and exit — see [Scheduling modes](#scheduling-modes). Falls back to `3h` on an unset, unparseable, or negative value. | `3h`           | No       |
| `FCLONES_SCAN_PATHS`   | Paths inside the container to scan for duplicates. Must match the volume mounts. Multiple paths can be space-separated (e.g. `/media /photos`), each requiring a corresponding volume mount.                                                                                                                                                                                          | `/scandir`     | No       |
| `FCLONES_ARGS`         | Extra arguments passed to `fclones group` scan phase                                                                                                                                                                                                                                                                                                                                  | `(none)`       | No       |
| `FCLONES_ACTION`       | Dedup action after scan — group (report only), link (hardlink), remove (delete), or dedupe (reflink/copy-on-write)                                                                                                                                                                                                                                                                    | `group`        | No       |
| `FCLONES_ACTION_ARGS`  | Extra arguments for the dedup action phase                                                                                                                                                                                                                                                                                                                                            | `(none)`       | No       |
| `FCLONES_ALLOW_UNSAFE` | Set to `true` to allow dangerous flags (`--command`, `--transform`, `--in-place`, `--no-copy`)                                                                                                                                                                                                                                                                                        | `false`        | No       |
| `FCLONES_SCAN_TIMEOUT` | Per-phase timeout (Go duration) applied to each fclones scan and action phase. A phase exceeding it is terminated and the run is marked unhealthy. Set to `0` for no timeout (unbounded — the phase runs until it finishes or the container stops). Raise for large filesystems whose initial scan can exceed 12h.                                                                    | `12h`          | No       |
| `FCLONES_LOG_LEVEL`    | slog level: `debug`, `info`, `warn`/`warning`, or `error`. Unrecognized values fall back to `info`.                                                                                                                                                                                                                                                                                   | `info`         | No       |

### Volumes

| Mount      | Description                                                                                                                                                                                                                                                                                                                            |
| ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/scandir` | Directory to scan for duplicate files. Must match the paths in `FCLONES_SCAN_PATHS` (space-separated for multiple mounts). The `group` action needs read access only; **`link`/`remove`/`dedupe` modify files here, so `/scandir` must be writable by the `user:` UID** (not a `:ro` mount) for those actions.                         |
| `/cache`   | fclones cache and state directory. **Must be writable by the UID set in `user:`** (the example uses `1000:1000`). The wrapper write-probes `/cache` at startup; if it is read-only or owned by another UID the container logs `cache directory verification failed uid=<n>` and exits (crash-looping under `restart: unless-stopped`). |

## Healthcheck

The built-in healthcheck (`/app/wrapper health`) checks for a marker file created after each successful scan and action phase. The container becomes unhealthy when fclones exits non-zero (e.g. scan path missing, permission denied, corrupted cache) or the action phase fails (e.g. hardlink across filesystems), or a scan lock cannot be acquired (e.g. `/cache` is full or read-only). It recovers automatically on the next successful scan — no restart required. In built-in mode the container begins unhealthy and transitions to healthy after the first successful scan completes, so size `healthcheck.start_period` accordingly for large filesystems where the initial scan may take minutes. In external mode the container starts healthy (idle, nothing has failed) and each triggered `scan` updates the marker.

The image bakes a 15s `start_period`, which suits small libraries and external mode. If your first built-in scan takes minutes, raise `start_period` in your own compose file so the container isn't reported unhealthy — and doesn't fire spurious alerts — during that initial scan:

```yaml
services:
  fclones:
    healthcheck:
      start_period: 10m # size to your library's first-scan duration
```

## Security

**No vulnerabilities found.** All scans clean.

| Tool                                                                | Result                              |
| ------------------------------------------------------------------- | ----------------------------------- |
| [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | No vulnerabilities in call graph    |
| [golangci-lint](https://golangci-lint.run/) (gosec, gocritic)       | 0 issues                            |
| [trivy](https://trivy.dev/)                                         | 0 vulnerabilities                   |
| [grype](https://github.com/anchore/grype)                           | 0 vulnerabilities                   |
| [gitleaks](https://github.com/gitleaks/gitleaks)                    | No secrets detected                 |
| [semgrep](https://semgrep.dev/)                                     | 2 info (false positives)            |
| [hadolint](https://github.com/hadolint/hadolint)                    | DL3008 in builder stage (discarded) |

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
are capped at 50 MB. Concurrent scans are guarded by an advisory
file lock (`flock` on `/cache/.fclones.lock`), which serialises
both the built-in ticker and externally triggered `scan`
invocations. Semgrep flags the distroless nonroot image as "missing
USER" (false positive, UID 65534 is baked in) and the
`/tmp/.healthy` marker (fixed path, single-process container).
Hadolint DL3008 applies to the Rust builder stage only, which is
discarded in the final image.

## Dependencies

| Dependency                | Source                                                           |
| ------------------------- | ---------------------------------------------------------------- |
| rust                      | [Rust](https://hub.docker.com/_/rust)                            |
| golang                    | [Go](https://hub.docker.com/_/golang)                            |
| Distroless static nonroot | [Distroless](https://github.com/GoogleContainerTools/distroless) |
| fclones                   | [GitHub](https://github.com/pkolaczk/fclones)                    |

Updated automatically via [Renovate](https://github.com/renovatebot/renovate); base images are pinned by digest and the upstream fclones artifact is integrity-pinned (tarball sha256 on amd64, commit on arm64). Builds carry signed SBOMs and provenance attestations verifiable with `gh attestation verify`.

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
