# How docker-fclones-scheduler works

This page explains what happens during a run, how the schedule survives a restart and when the container reports unhealthy. It is for readers who want to know why the container behaved as it did. The settings themselves are in [Configuration](configuration.md).

## One process runs every scan

The container's main process runs every scan, whichever mode `SCAN_INTERVAL` picks. Scheduled scans and scans started with `docker exec fclones /app/wrapper scan` wait in one queue and run strictly in order, so two scans never run at the same time. The queue holds 16 requests, and a request that arrives when it is full is refused at once. The `scan` command talks to the main process through a socket at `/tmp/fclones-wrapper.sock` that only the container's own user can open.

Because the main process runs every scan, all scan output and each run's `run finished` record reach the container log in every mode. The same alert rules therefore work whether the container schedules itself or an outside scheduler starts it. The same process serves the optional metrics page, so it counts every run too.

A lock file at `/cache/.fclones.lock` also guards the fclones cache against a scan from a different container, or a manual `docker run`, that shares the same `/cache` volume. Such a scan skips instead of writing to the cache at the same time. With the two long-running modes, that skip is not an error, and neither is a stop signal. Both exit 0.

## A run has two phases

Each run first scans with `fclones group`, using `FCLONES_SCAN_PATHS` and `FCLONES_ARGS`. The container adds `--cache`, so fclones keeps its file hashes on `/cache` between runs, and `-f json`, so the report is machine-readable. The report goes to a temporary file on `/cache`, and leftover report files from an interrupted run are removed at the next start.

The container reads the report strictly. A report that is cut short, has no statistics, has a group of fewer than two files, or whose group count does not match fails the run with `outcome=decode_error`. A change in fclones' report format therefore fails loudly instead of reporting zero duplicates. Fields the container does not know are ignored. It keeps at most 100 groups in memory for the log while it counts all of them.

When the scan found duplicates and `FCLONES_ACTION` is not `group`, the second phase passes the same report to `fclones link`, `remove` or `dedupe` with `FCLONES_ACTION_ARGS`. The container then reads the action's summary line for the number of files processed and the space freed. When it does not recognize that line, it logs `possible fclones format drift`. fclones opens each duplicate for writing and only warns when that fails. So an action that found duplicates and processed none of them fails the run with `action reclaimed nothing`. For `dedupe`, fclones reports the space freed as an upper bound, and the `action complete` line then carries `reclaimed_estimated=true`.

When the run ends, the container hands its verdict to the health file, the metrics and the `run finished` record in one step, so the three always agree. [Monitoring and alerts](monitoring.md#the-run-finished-record) lists the record's fields.

Each phase runs under `SCAN_TIMEOUT`, `12h` by default. A stop signal ends the running phase, and the run then counts as interrupted, not failed.

## The schedule

With the built-in schedule, the container writes the time and the result of each scheduled scan to `/cache/.docker-fclones-scheduler-last-run`. At start it reads that record:

- When the last scan finished less than one `SCAN_INTERVAL` ago, the startup scan is skipped, and the next scan comes one interval after the last one. A restart therefore neither adds a scan nor delays the next one.
- A failed scan also counts as the scan for its interval. A restart does not repeat it, and the next scheduled scan is the retry, because repeating a failed scan of several hours on every restart would cost the same hours again.
- With no record, or a record older than one interval, the container scans at once.

Only scheduled scans write the record. Scans started with `wrapper scan` and the one-scan mode leave it alone. Without a persistent `/cache` volume the record is lost on every recreate, and every start runs a scan.

## Health

The healthcheck runs `/app/wrapper health`, which reads the health file `/tmp/.healthy`. The main process updates it after each run. The container turns unhealthy when:

- fclones exits non-zero, for example because a scan path is missing, permission is denied or the cache is corrupt.
- The action phase fails, for example on a hardlink across filesystems.
- A `link`, `remove` or `dedupe` action finds duplicates and processes none of them.
- The report cannot be read.
- The check at start fails, for example because `/cache` is full or read-only.

It turns healthy again on the next successful scan, with no restart. A scan interrupted by a stop signal, and a scan skipped because another container held the lock, leave the health state as the last real run set it. During shutdown the container reports unhealthy.

The state at start follows the last completed run, scheduled or triggered, which the container records in `/cache/.docker-fclones-scheduler-last-completed`. A container whose last completed run succeeded starts healthy, and one whose last completed run failed starts unhealthy. So a good triggered run after a failed scheduled scan starts healthy, and a failed triggered run after a good scheduled scan starts unhealthy.

With the built-in schedule, a startup scan that is due overrides that record. The container then starts unhealthy, scans, and turns healthy when that scan succeeds. When the startup scan is skipped and the last completed run failed, the container stays unhealthy until the next good run, at most one `SCAN_INTERVAL` after the start for a scheduled one.

The built-in schedule also sets a deadline of `2 x SCAN_INTERVAL + 2 x SCAN_TIMEOUT` on the health file `/tmp/.healthy`. A health file older than that means the schedule has stopped, and the healthcheck reports unhealthy. With `SCAN_TIMEOUT=0` there is no deadline, because a run then has no time limit. With an outside scheduler, the container starts from the last completed run in the same way, and healthy when no run has completed yet. Each scan updates the health file, and no deadline applies.

With `METRICS_LISTEN_ADDR` set, `/healthz` on that address gives the same verdict as the healthcheck, deadline included, while `/tmp` is writable. [Knowing the app is alive](monitoring.md#knowing-the-app-is-alive) lists what it answers.
