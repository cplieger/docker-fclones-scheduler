# How docker-fclones-scheduler works

This page explains what happens during a run, how the schedule survives a restart and when the container reports unhealthy. It is for readers who want to know why the container behaved as it did. The settings themselves are in [Configuration](configuration.md).

## One process runs every scan

The container's main process runs every scan, whichever mode `SCAN_INTERVAL` picks. Scheduled scans and scans started with `docker exec fclones /app/wrapper scan` wait in one queue and run strictly in order, so two scans never run at the same time. The queue holds 16 requests, and a request that arrives when it is full is refused at once. The `scan` command talks to the main process through a socket at `/tmp/fclones-wrapper.sock` that only the container's own user can open.

Because the main process runs every scan, all scan output reaches the container log in every mode, and the same alert rules work whether the container schedules itself or an outside scheduler starts it.

A lock file at `/cache/.fclones.lock` also guards the fclones cache against a scan from a different container, or a manual `docker run`, that shares the same `/cache` volume. Such a scan skips instead of writing to the cache at the same time. With the two long-running modes, that skip is not an error, and neither is a stop signal. Both exit 0.

## A run has two phases

Each run first scans with `fclones group`, using `FCLONES_SCAN_PATHS` and `FCLONES_ARGS`. The container adds `--cache`, so fclones keeps its file hashes on `/cache` between runs, and `-f json`, so the report is machine-readable. The report goes to a temporary file on `/cache`, and leftover report files from an interrupted run are removed at the next start.

The container reads the report strictly. A report that is cut short, has no statistics, has a group of fewer than two files, or whose group count does not match fails the run with `outcome=decode_error`. A change in fclones' report format therefore fails loudly instead of reporting zero duplicates. Fields the container does not know are ignored. It keeps at most 100 groups in memory for the log while it counts all of them.

When the scan found duplicates and `FCLONES_ACTION` is not `group`, the second phase passes the same report to `fclones link`, `remove` or `dedupe` with `FCLONES_ACTION_ARGS`. The container then reads the action's summary line for the number of files processed and the space freed. When it does not recognize that line, it logs `possible fclones format drift`. fclones opens each duplicate for writing and only warns when that fails. So an action that found duplicates and processed none of them fails the run with `action reclaimed nothing`. For `dedupe`, fclones reports the space freed as an upper bound, and the `action complete` line then carries `reclaimed_estimated=true`.

Each phase runs under `SCAN_TIMEOUT`, `12h` by default. A stop signal ends the running phase, and the run then counts as interrupted, not failed.

## The schedule

With the built-in schedule, the container writes the time and the result of each scheduled scan to `/cache/.docker-fclones-scheduler-last-run`. At start it reads that record:

- When the last scan finished less than one `SCAN_INTERVAL` ago, the startup scan is skipped, and the next scan comes one interval after the last one. A restart therefore neither adds a scan nor delays the next one.
- A failed scan also counts as the scan for its interval. A restart does not repeat it, and the next scheduled scan is the retry, because repeating a failed scan of several hours on every restart would cost the same hours again.
- With no record, or a record older than one interval, the container scans at once.

Only scheduled scans write the record. Scans started with `wrapper scan` and the one-scan mode leave it alone. Without a persistent `/cache` volume the record is lost on every recreate, and every start runs a scan.

## Health

The healthcheck runs `/app/wrapper health`, which reads a file the main process updates after each run. The container turns unhealthy when:

- fclones exits non-zero, for example because a scan path is missing, permission is denied or the cache is corrupt.
- The action phase fails, for example on a hardlink across filesystems.
- A `link`, `remove` or `dedupe` action finds duplicates and processes none of them.
- The report cannot be read.
- The check at start fails, for example because `/cache` is full or read-only.

It turns healthy again on the next successful scan, with no restart. A scan interrupted by a stop signal, and a scan skipped because another container held the lock, leave the health state as the last real run set it. During shutdown the container reports unhealthy.

With the built-in schedule, the state at start follows the record on `/cache`. A successful scan younger than `SCAN_INTERVAL` means the container starts healthy and skips the startup scan. No record, or an old one, means it starts unhealthy, scans, and turns healthy when that scan succeeds. After a failed scan and a restart, it skips the startup scan and starts unhealthy, and it stays unhealthy until the next scheduled scan succeeds, at most one `SCAN_INTERVAL` after the start.

The built-in schedule also sets a deadline of `2 x SCAN_INTERVAL + 2 x SCAN_TIMEOUT` on that file. A file older than that means the schedule has stopped, and the healthcheck reports unhealthy. With `SCAN_TIMEOUT=0` there is no deadline, because a run then has no time limit. With an outside scheduler, the container starts healthy because nothing has failed yet, each scan updates the file, and no deadline applies.
