# docker-fclones-scheduler

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/size.json)](https://github.com/cplieger/docker-fclones-scheduler/pkgs/container/docker-fclones-scheduler)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/coverage.json)](https://github.com/cplieger/docker-fclones-scheduler/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/mutation.json)](https://github.com/cplieger/docker-fclones-scheduler/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13204/badge)](https://www.bestpractices.dev/projects/13204)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/docker-fclones-scheduler/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/docker-fclones-scheduler)
[![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/docker-fclones-scheduler/releases)

Find and deduplicate files on a schedule, reclaiming wasted disk space automatically.

## What it does

Wraps the fclones duplicate file finder in a Go scheduler daemon with
interval-based scheduling and a CLI health probe. Supports the group,
link, remove, and dedupe actions with configurable arguments. Reports scan
statistics including duplicates found, space reclaimable, and files
processed. All output goes to stdout/stderr for collection by log
aggregators (Alloy, Promtail, etc.) and alerting via Grafana or similar.

- Mount your media directory and schedule periodic scans; fclones finds duplicates and can replace them with hardlinks or remove them entirely
- Built-in scheduler, or hand scheduling to an external scheduler (cron, Ofelia, etc.) via the `scan` subcommand
- Built-in Docker healthcheck with automatic recovery

### Why this design

- **Scheduler your way:** ships with a self-contained interval scheduler, so no external cron, systemd timer, or orchestrator-level scheduling is needed. If you already run a central scheduler (Ofelia, cron), set `SCAN_INTERVAL=off` and trigger scans with `docker exec fclones /app/wrapper scan` instead
- **One owner for every run:** the daemon executes every scan, serialized in one queue, so every run's logs land on the container's own log stream in both scheduling modes and the same alert rules work everywhere
- **Machine-readable report contract:** the scan consumes fclones' JSON report with a strict decoder, so an upstream output-format change fails the run loudly instead of silently zeroing the duplicate stats your alerting reads
- **Distroless and rootless:** runs as `nonroot` (UID 65532) on `gcr.io/distroless/static-debian13` with no shell or package manager
- **Dangerous flags blocked by default:** `--transform`, `--in-place`, and `--no-copy` are rejected unless you explicitly opt in with `ALLOW_UNSAFE_ARGS=true`, preventing command injection via environment variables
- **Structured logs:** logfmt with UTC timestamps, so log lines are zone-stable regardless of the container's `TZ` and alerting needs no custom exporter

## Quick start

The image is published to both GHCR (`ghcr.io/cplieger/docker-fclones-scheduler`) and Docker Hub (`cplieger/docker-fclones-scheduler`); identical contents, use whichever you prefer.

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/docker-fclones-scheduler:latest
    container_name: fclones
    restart: unless-stopped
    # Override with PUID/PGID in .env; defaults to 1000:1000.
    user: "${PUID:-1000}:${PGID:-1000}"  # match your host user

    environment:
      SCAN_INTERVAL: "1h"  # Go duration (e.g. 1h, 30m, 12h)
      FCLONES_SCAN_PATHS: "/scandir"
      FCLONES_ARGS: "--rf-over 1"
      FCLONES_ACTION: "link"  # group (report), link (hardlink), remove (delete), or dedupe (reflink/copy-on-write)
      FCLONES_ACTION_ARGS: "--priority bottom"

    volumes:
      - "/path/to/media:/scandir"
      - "/opt/appdata/fclones:/cache"
```

## Scheduling modes

The container runs in one of three modes, selected by `SCAN_INTERVAL`.

### Built-in scheduler (default)

Set `SCAN_INTERVAL` to a positive Go duration (`1h`, `30m`, `12h`, …). The container runs a scan at startup and then every interval. This is the zero-dependency default; nothing else is required. An unset, unparseable, or negative value falls back to the `3h` default cadence in this mode (a negative value is treated as a typo and logged as a warning).

### External scheduler

Set `SCAN_INTERVAL=off` (alias: `disabled`). The container stays running but idle, and you trigger each scan out-of-band by exec'ing the `scan` subcommand:

```bash
docker exec fclones /app/wrapper scan
```

The `scan` subcommand submits one run request to the daemon, blocks until the
scan finishes, and exits non-zero on failure. The run executes inside the
daemon, so its full output lands on the container's log stream; the trigger
sees only lifecycle lines and the result. The daemon updates the same health
marker the healthcheck reports. Example with
[Ofelia](https://github.com/mcuadros/ofelia) labels:

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/docker-fclones-scheduler:latest
    container_name: fclones
    restart: unless-stopped
    user: "${PUID:-1000}:${PGID:-1000}"
    environment:
      SCAN_INTERVAL: "off"   # disable built-in loop; Ofelia drives it
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

Runs never overlap: the daemon executes scheduled ticks and triggered scans
strictly in order from one queue, and a trigger that arrives mid-run queues
behind it. An advisory file lock on `/cache/.fclones.lock` additionally
guards against a scan from a _different_ container or a manual `docker run`
sharing the same `/cache` volume; such a run skips instead of corrupting the
shared fclones cache. Ofelia's `no-overlap` is still recommended to avoid
queuing redundant triggers. The exec must run as the container's own user
(the trigger socket is owner-only); a mismatched exec user fails loudly at
connect.

### Run once

Set `SCAN_INTERVAL=0` (or `0s`). The container runs exactly one scan and dedup action, then exits, and the exit code is the job result: non-zero if the scan failed, timed out, was interrupted (SIGTERM/SIGINT) before it finished, or was skipped because another process held the `/cache` scan lock (logged with `outcome=skipped`). This suits a batch or one-shot context (a Kubernetes `Job`, a CI step, or a manual `docker run --rm`) where an external system decides when to run again: a run that never completed a scan surfaces as a failure, so the orchestrator retries rather than recording success. In the long-running modes a SIGTERM is a clean shutdown and a lock conflict a benign no-op, both exiting 0.

## Configuration reference

### Environment variables

| Variable | Description | Default | Required |
| --- | --- | --- | --- |
| `SCAN_INTERVAL` | Built-in scan interval as a Go duration (e.g. `1h`, `30m`, `12h`); the first scan runs at startup. Set to `off` (or `disabled`) to idle and trigger scans externally, or to `0` (or `0s`) to run a single scan and exit (see [Scheduling modes](#scheduling-modes)). Falls back to `3h` on an unset, unparseable, or negative value. | `3h` | No |
| `FCLONES_SCAN_PATHS` | Paths inside the container to scan for duplicates. Must match the volume mounts. Multiple paths can be space-separated (e.g. `/media /photos`), each requiring a corresponding volume mount. | `/scandir` | No |
| `FCLONES_ARGS` | Extra arguments passed to the `fclones group` scan phase. Flags and their values only; a bare token is rejected at startup (see [Passing extra fclones arguments](#passing-extra-fclones-arguments)). The wrapper owns `--cache` and the report format (`-f json`); passing `--cache`, `-f`, or `--format` here is rejected at startup. | `(none)` | No |
| `FCLONES_ACTION` | Dedup action after scan: `group` (report only), `link` (hardlink), `remove` (delete), or `dedupe` (reflink/copy-on-write) | `group` | No |
| `FCLONES_ACTION_ARGS` | Extra arguments for the dedup action phase. Flags and their values only, same rule as `FCLONES_ARGS`. | `(none)` | No |
| `ALLOW_UNSAFE_ARGS` | Set to `true` to allow dangerous flags (`--transform`, `--in-place`, `--no-copy`) | `false` | No |
| `SCAN_TIMEOUT` | Per-phase timeout (Go duration) applied to each fclones scan and action phase. A phase exceeding it is terminated and the run is marked unhealthy. Set to `0` for no timeout (the phase runs until it finishes or the container stops). Raise for large filesystems whose initial scan can exceed 12h. | `12h` | No |
| `LOG_LEVEL` | slog level: `debug`, `info`, `warn`/`warning`, or `error`. Unrecognized values fall back to `info`. | `info` | No |

### Passing extra fclones arguments

`FCLONES_ARGS` and `FCLONES_ACTION_ARGS` carry flags and their values only.
Scan paths belong in `FCLONES_SCAN_PATHS`, so the wrapper rejects a bare
(non-flag) token at startup and names it in the error.

Every repeatable fclones filter (`--name`, `--path`, `--exclude`,
`--keep-name`, `--keep-path`) takes **one value per flag**. fclones reads a
second bare pattern as another input path, resolves it against the container
working directory, and fails the scan with `Can't access '/app/<pattern>'`:

```
# Works, one flag per pattern
FCLONES_ARGS: "--name '*.mp4' --name '*.mkv'"

# Works, one glob for both extensions
FCLONES_ARGS: "--name '*.{mp4,mkv}'"

# Rejected at startup, '*.mkv' is a bare token that fclones reads as a path
FCLONES_ARGS: "--name '*.mp4' '*.mkv'"
```

The multi-value example in the [fclones
README](https://github.com/pkolaczk/fclones#finding-files) has the same
defect, so a config copied from there needs the repeated flag too.

### Volumes

| Mount | Description |
| --- | --- |
| `/scandir` | Directory to scan for duplicate files. Must match the paths in `FCLONES_SCAN_PATHS` (space-separated for multiple mounts). The `group` action needs read access only; **`link`/`remove`/`dedupe` modify files here, so `/scandir` must be writable by the `user:` UID** (not a `:ro` mount) for those actions. |
| `/cache` | fclones cache and state directory. **Must be writable by the UID set in `user:`** (the example uses `1000:1000`). The wrapper write-probes `/cache` at startup; if it is read-only or owned by another UID the container logs `cache directory verification failed uid=<n>` and exits (crash-looping under `restart: unless-stopped`). |

## Alerting

docker-fclones-scheduler has no metrics endpoint; its operational state is in
its logs. Ship the container's logs to Loki (Grafana Alloy's Docker log
discovery does this with no configuration) and evaluate these with
[Loki's ruler](https://grafana.com/docs/loki/latest/alert/); firing alerts
deliver through your Alertmanager exactly like Prometheus metric alerts.

```yaml
groups:
  - name: docker-fclones-scheduler
    rules:
      - alert: FclonesLinkEstablished
        expr: |
          sum by (files_deduped, reclaimed_human) (
            count_over_time(
              {container="fclones"} |= "action complete" | logfmt | action="link" | files_deduped > 0 [15m]
            )
          ) > 0
        for: 0m
        labels:
          severity: info
        annotations:
          summary: "fclones linked {{ $labels.files_deduped }} duplicate files (reclaimed {{ $labels.reclaimed_human }})"
          description: >
            fclones established hardlinks for {{ $labels.files_deduped }} duplicate
            files, reclaiming {{ $labels.reclaimed_human }}. The individual linked
            paths are in the same run's `duplicate file` log lines (capped at 500
            pairs / 64 KB), viewable in Loki: {container="fclones"} |= "duplicate
            file" (filter by the run's scan_id). Success notification, no action
            required.
      - alert: FclonesFormatDrift
        expr: |
          sum(count_over_time({container="fclones"} |= "possible fclones format drift" [2h])) > 0
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "fclones output-format drift detected (action stats may be unreliable)"
          description: >
            The wrapper logged "possible fclones format drift": the action
            phase's summary line was not recognized, so the files_deduped and
            bytes_reclaimed stats on the "action complete" line may read zero
            even though work happened. The action itself still runs and the
            run still exits 0, so a job-failure or container-restart alert
            would not catch this. (Scan-report drift is covered separately:
            the JSON report is decoded strictly, and an unreadable report
            fails the run with outcome=decode_error.) Check the fclones
            version against the wrapper's summary parser.
```

Thresholds and the `severity` labels are starting points. Adjust the
`container` selector (or `job` / `service`, depending on your log collector) to
your deployment; if you run `remove` or `dedupe` instead of `link`, change
`action="link"` in the first rule to match your `FCLONES_ACTION`. Route by
whatever labels your Alertmanager uses.

These rules work in **both scheduling modes**: every run executes in the
daemon, so its logs always land under the app's own container name. Under
`SCAN_INTERVAL=off` the exec'd `scan` client emits only its own lifecycle
lines (queued / started / result) to the trigger's log (an Ofelia job log,
for example) and exits with the run's result, so you can additionally alert
on your scheduler's own job outcome for extra failure coverage.

## Healthcheck

The built-in healthcheck (`/app/wrapper health`) checks a marker file the daemon maintains after each run. The container becomes unhealthy when fclones exits non-zero (e.g. scan path missing, permission denied, corrupted cache), the action phase fails (e.g. hardlink across filesystems), the report cannot be decoded, or startup verification fails (e.g. `/cache` is full or read-only). It recovers automatically on the next successful scan; no restart is required.

In built-in mode the container begins unhealthy, transitions to healthy after the first successful scan completes, and arms a freshness deadline of `2 x SCAN_INTERVAL + 2 x SCAN_TIMEOUT`: a marker that old means the interval loop is wedged, so the probe reports unhealthy and Docker restarts the container. The deadline is disabled automatically when `SCAN_TIMEOUT=0`, since the worst-case run duration is then unbounded. In external mode the container starts healthy (idle, nothing has failed), each triggered run updates the marker, and no deadline applies.

The image bakes a 15s `start_period`, which suits small libraries and external mode. If your first built-in scan takes minutes, raise `start_period` in your own compose file so the container isn't reported unhealthy, and doesn't fire spurious alerts, during that initial scan:

```yaml
services:
  fclones:
    healthcheck:
      start_period: 10m # size to your library's first-scan duration
```

## Security

No network listener, no HTTP server, no exposed ports: the trigger socket is
a local unix socket (`/tmp/fclones-wrapper.sock`), owner-only and reachable
only from inside the container. The container runs as `nonroot` on a
distroless base image with no shell.

The `FCLONES_ACTION` env var is validated against an allowlist, and the
dangerous flags (`--transform`, `--in-place`, `--no-copy`) are
blocked by default to prevent command injection via env vars; set
`ALLOW_UNSAFE_ARGS=true` only if you need `--transform` for content-aware
deduplication. Wrapper-owned fclones flags (`--cache`, `-f`/`--format`) are
rejected in `FCLONES_ARGS` at startup so user args cannot break the report
contract.

Arguments reach fclones as explicit argument lists with no shell expansion.
Captured subprocess output is size-capped, and the scan report is decoded
with a strict streaming JSON decoder: memory stays bounded regardless of
report size, and a malformed report fails the run. Runs are serialized by
the daemon's single queue; an advisory file lock on `/cache` additionally
guards the shared cache against scans from other containers or manual
`docker run` invocations.

Accepted scanner findings, all false positives: semgrep flags the missing
`USER` directive (the distroless base bakes in UID 65532) and the `/tmp`
health-marker path (a fixed marker file, not sensitive data), and hadolint
DL3008 fires in the Rust builder stage, which is discarded from the final
image. Live scan results are on the repository's Security tab.

## Dependencies

| Dependency | Source |
| --- | --- |
| rust | [Rust](https://hub.docker.com/_/rust) |
| golang | [Go](https://hub.docker.com/_/golang) |
| Distroless static nonroot | [Distroless](https://github.com/GoogleContainerTools/distroless) |
| fclones | [GitHub](https://github.com/pkolaczk/fclones) |

Updated automatically via [Renovate](https://github.com/renovatebot/renovate); base images are pinned by digest and the upstream fclones artifact is integrity-pinned (tarball sha256 on amd64, commit on arm64). Builds carry signed SBOMs and provenance attestations verifiable with `gh attestation verify`.

## Credits

This project packages [fclones](https://github.com/pkolaczk/fclones) (MIT) into a container image. All credit for the core functionality goes to the upstream maintainers.

## Contributing

Issues and pull requests are welcome. Please open an issue first for
larger changes so the approach can be discussed before implementation.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
