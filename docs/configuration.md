# Configuration

This page covers the three scheduling modes, extra fclones options, time limits, a slow first scan and the log settings, which need more than one table row. The [Configuration reference](../README.md#configuration-reference) in the README lists every variable with its default.

## Scheduling

`SCAN_INTERVAL` picks one of three modes. [How it works](how-it-works.md#the-schedule) explains what each mode does across restarts.

### Built-in schedule

Set `SCAN_INTERVAL` to a duration such as `30m`, `1h` or `12h`. The container scans when it starts, then once per interval. Nothing else needs to run. An unset value, a value it cannot read or a negative value means the default of `3h`, and a negative value also logs a warning.

The container keeps a record of its last scheduled scan on `/cache`. When that scan finished less than one interval ago, a restarted container skips the startup scan, and the next scan comes one interval after the last one. Without a persistent `/cache` volume, every start runs a scan.

The built-in schedule counts intervals from the last scan. To scan at a time of day, use an outside scheduler.

### An outside scheduler

Set `SCAN_INTERVAL` to `off`, or `disabled`. The container stays up and waits, and each scan starts when something runs this command:

```bash
docker exec fclones /app/wrapper scan
```

The command waits until the scan ends and exits non-zero when the scan fails. The scan itself runs in the container, so its full output goes to the container log, and the command prints only that the scan was queued, started and finished. Run the command as the container's own user, which `docker exec` does by default. Another user is refused when it connects.

This example runs a scan every 6 hours with [Ofelia](https://github.com/mcuadros/ofelia) labels:

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/docker-fclones-scheduler:latest
    container_name: fclones
    restart: unless-stopped
    user: "${PUID:-1000}:${PGID:-1000}"
    environment:
      SCAN_INTERVAL: "off"  # Ofelia starts each scan
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

Scans never overlap. A request that arrives during a scan waits for it, so Ofelia's `no-overlap` only saves you from queueing a scan you do not need. If you stop the `scan` command with Ctrl+C or a signal, it exits non-zero, and the container still finishes the scan it accepted. A scheduler that kills the command on its own timeout therefore records a failed job while the scan goes on.

### One scan, then exit

Set `SCAN_INTERVAL` to `0`, or `0s`. The container runs one scan and its action, then exits, and the exit code is the result. It exits non-zero when the scan failed or timed out, when a stop signal arrived before the scan finished, or when another container holds the scan lock on the same `/cache`. That last case logs `outcome=skipped`. This mode suits a one-off `docker run --rm`, a CI step or a Kubernetes `Job`, where the system that started it decides when to run again.

With the two long-running modes, a stop signal is a clean shutdown and exits 0.

## Extra fclones options

`FCLONES_ARGS` goes to the scan and `FCLONES_ACTION_ARGS` to the action, the `fclones link`, `remove` or `dedupe` step. Both take flags and their values only. Folders to scan belong in `FCLONES_SCAN_PATHS`, so a word that is not a flag or a flag's value stops the container at start, and the error names it.

fclones reads one value per flag. `--name`, `--path`, `--exclude`, `--keep-name` and `--keep-path` each need a flag for every pattern:

```text
# Works, one flag per pattern
FCLONES_ARGS: "--name '*.mp4' --name '*.mkv'"

# Works, one pattern for both extensions
FCLONES_ARGS: "--name '*.{mp4,mkv}'"

# Refused at start, because fclones would read '*.mkv' as a folder to scan
FCLONES_ARGS: "--name '*.mp4' '*.mkv'"
```

The [fclones README](https://github.com/pkolaczk/fclones#finding-files) shows several patterns after one `--name`. Inside this container, repeat the flag instead.

The container sets `--cache` and the report format itself, so `--cache`, `-f` and `--format` in `FCLONES_ARGS` stop it at start. `--transform`, `--in-place` and `--no-copy` stop it too, unless `ALLOW_UNSAFE_ARGS` is `true`. Only `ALLOW_UNSAFE_ARGS=true`, in any letter case, turns that check off.

To see what an action would do without changing a file, add `--dry-run` to `FCLONES_ACTION_ARGS`. fclones then prints the operations it would run. The container does not recognize that summary, so it logs a `possible fclones format drift` warning for the run.

## Time limits

`SCAN_TIMEOUT` applies to the scan and to the action separately, `12h` each by default. A phase that runs longer is stopped and the run fails. Raise it for a large library whose first scan takes longer than 12 hours, or set `0` for no limit. A negative or unreadable value stops the container at start.

## A slow first scan

The image's healthcheck allows 15 seconds after start, which suits a small library and the outside-scheduler mode. With the built-in schedule and no recent successful scan on record, the container is unhealthy until its first scan succeeds. If that scan takes minutes, raise `start_period` in your compose file so the container is not reported unhealthy, and no alert fires, during the first scan:

```yaml
services:
  fclones:
    healthcheck:
      start_period: 10m  # how long your first scan takes
```

## Logs

`LOG_LEVEL` takes `debug`, `info`, `warn` or `warning`, and `error`. Any other value means `info` and logs a warning. Log lines are logfmt with UTC times whatever `TZ` is set to, so the container needs no `TZ` setting.
