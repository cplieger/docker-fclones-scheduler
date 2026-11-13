# Monitoring and alerts

This page covers what docker-fclones-scheduler reports, how to get alerts from it and who tells you when it stops. docker-fclones-scheduler writes one `run finished` record per run to its log and can serve a Prometheus metrics page. The same alerts ship twice, in [`alerts/promql.yaml`](../alerts/promql.yaml) for a metric store and in [`alerts/logql.yaml`](../alerts/logql.yaml) for Loki. Load one of the two.

## What you get

| You run | You get | You set up |
| --- | --- | --- |
| Prometheus, Mimir or VictoriaMetrics, with Alertmanager | Five alerts, `FclonesDown` included, and graphs of each run and the space it freed | `METRICS_LISTEN_ADDR`, a scrape job named `fclones`, and `alerts/promql.yaml` |
| Loki and Alertmanager, no metric store | Four alerts, all but `FclonesDown`, which `FclonesScanStalled` covers | `alerts/logql.yaml`, with `LOG_LEVEL` left at `info` |
| Grafana-managed alerting only | The same alerts, as Grafana rules | One of the two files imported into Grafana, with its data source |
| Docker or Unraid only | The container log and Docker's health status, which notify nobody | `METRICS_LISTEN_ADDR` and an uptime prober on `/healthz`, or something that watches Docker health |

## Metrics

Set `METRICS_LISTEN_ADDR` to an address such as `:9469`, and publish that port. The container then serves Prometheus metrics at `/metrics` and a health check at `/healthz` on it. Left unset, the image opens no port. The page has no login, so keep the port on your network. A port that another process holds stops the container at start with `cannot listen on METRICS_LISTEN_ADDR`.

With `SCAN_INTERVAL=0` the container exits after its one run, so the page exists only while that run lasts and `FclonesDown` would fire between runs. Use `alerts/logql.yaml` in that mode.

Every series starts with `fclones_scheduler_`. After a restart, the last run's time and verdict come back from `/cache`, and the counters start again from 0.

| Series | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `runs_total` | counter | `result` | Completed runs by how they ended |
| `last_run_result` | gauge | `result` | 1 for how the last completed run ended, 0 for the other results |
| `last_run_ok` | gauge | | 1 when the last completed run succeeded, else 0, the healthcheck's verdict |
| `last_run_timestamp_seconds` | gauge | | Unix time the last run completed, 0 before the first |
| `last_success_timestamp_seconds` | gauge | | Unix time the last run succeeded, 0 until a success is known |
| `phase_duration_seconds` | gauge | `phase` | How long `scan` or `action` took the last time it ran |
| `duplicate_groups` | gauge | | Duplicate groups in the last scan report |
| `duplicate_files` | gauge | | Duplicate files in the last scan report, keepers left out |
| `redundant_bytes` | gauge | | Bytes those duplicates take up |
| `action_summary_recognized` | gauge | | 0 when the last action's summary line was not recognized, else 1 |
| `files_processed_total` | counter | | Duplicate files the actions linked, removed or deduplicated |
| `reclaimed_bytes_total` | counter | | Bytes the actions freed, from fclones' rounded summary, an upper bound for `dedupe` |
| `info` | gauge | `mode`, `action` | Always 1, with labels that name the scheduling mode and `FCLONES_ACTION` |
| `run_deadline_timestamp_seconds` | gauge | | Unix time by which the next successful run is due: the last success, or the start when the record on `/cache` shows no success, plus twice `SCAN_INTERVAL` and twice `SCAN_TIMEOUT`. A failed run leaves it where it was. 0 with an outside scheduler, with `SCAN_INTERVAL=0` and with `SCAN_TIMEOUT=0` |
| `build_info` | gauge | `version` | Always 1. `version` is the release the image was built from |

`result` is one of `success`, `exec_error`, `timeout`, `decode_error`, `action_no_op`, `start_error` and `lock_error`. The page also carries the Go runtime and process families (`go_*`, `process_*`) under the names Go's official Prometheus client uses, so stock Go dashboards work.

No dashboard ships. These queries are a start in Grafana's Metrics Drilldown:

- Space freed per day: `increase(fclones_scheduler_reclaimed_bytes_total[1d])`
- Duplicates left after each scan: `fclones_scheduler_duplicate_files`
- Scan time: `fclones_scheduler_phase_duration_seconds{phase="scan"}`

## Logs

The container log is logfmt with UTC times, so a log collector needs no special parser. Every line of one run carries the same `scan_id`. Every failed, timed-out, skipped or interrupted run logs a line with an `outcome` field, so one query such as `outcome=~".+"` finds them all.

### The run finished record

Each completed run, scheduled or triggered, logs one `run finished` line at INFO. It is the only line the log alert rules read, so they need `LOG_LEVEL` at `info` or `debug`. A run skipped because another container held the scan lock, and a run a stop signal interrupted, write no record.

| Field | Present | Meaning |
| --- | --- | --- |
| `scan_id` | always | The run's id, shared by every line of the run |
| `trigger` | always | `startup` or `interval` for the built-in schedule, `external` for `wrapper scan`, `once` for `SCAN_INTERVAL=0` |
| `result` | always | How the run ended, one of the `result` values above |
| `ok` | always | 1 when the run succeeded, else 0 |
| `scan_duration_s` | once the scan ran | Scan time in whole seconds |
| `groups`, `duplicate_files`, `redundant_bytes` | once the report was read | The scan's duplicate groups, duplicate files and the bytes they take up |
| `action_duration_s` | once the action ran | Action time in whole seconds |
| `summary_recognized` | once the action's summary was read | 1 when the container recognized fclones' summary line, else 0 |
| `files_processed` | always | Duplicate files the action processed, 0 with no action |
| `reclaimed_bytes` | always | Bytes the action freed, 0 with no action |

### Other lines

These lines are for reading a run by hand. No alert rule reads them.

| Message | Level | Fields worth reading |
| --- | --- | --- |
| `container started (built-in scheduling)` or `(external scheduling)` | INFO | `mode`, `interval`, `target`, `action`, `startup_scan` |
| `scan starting` | INFO | `target`, `args`, `timeout` |
| `scan complete` | INFO | `duration_s`, `groups`, `duplicate_files`, `redundant_human`, `duplicates_found` |
| `duplicate file` | INFO | `group`, `keeper`, `duplicate`, `size` |
| `action complete` | INFO | `action`, `duration_s`, `files_deduped`, `bytes_reclaimed`, `reclaimed_human`, `reclaimed_estimated` |
| `action reclaimed nothing; failing the run` | ERROR | `action`, `files_deduped`, `result`, with `outcome=action_no_op` |
| `action summary not recognized, possible fclones format drift` | WARN | `action`, `result` |
| `scan failed`, `action failed` | ERROR | `error`, `stderr`, with `outcome=exec_error` |
| `scan timeout exceeded`, `action timeout exceeded` | ERROR | `timeout`, `duration`, with `outcome=timeout` |
| `fclones report decode failed; failing the run` | ERROR | `error`, with `outcome=decode_error` |
| `http` | INFO, or DEBUG for `/metrics` and `/healthz` | `method`, `path`, `status`, `duration_ms`, `request_id`, one line per request to the metrics listener |

On these lines `result` holds fclones' own summary text, not the run's result. The `duplicate file` lines stop at 500 pairs or 64 KB per scan, taken from the first 100 groups. A `duplicate pairs truncated` line then gives the full totals. fclones' own messages reach the log with control characters and other unsafe characters shown as `\xNN`. A file name inside an fclones message cannot fake or hide a log line.

With an outside scheduler, the `wrapper scan` command writes only its own short lines, `triggered scan accepted`, `triggered scan started` and `triggered scan complete` or `triggered scan failed`, to the log of whatever ran it. The scan's full output and its `run finished` record still go to the container log.

## Alerting

Load `alerts/promql.yaml` into Prometheus or the Mimir ruler, or `alerts/logql.yaml` into Loki's ruler, not both. They hold the same alerts under the same names, so both loaded would notify twice. `FclonesDown` exists only in `alerts/promql.yaml`. In `alerts/logql.yaml`, `FclonesScanStalled` covers a stopped container. [Loading metric alert rules](https://github.com/cplieger/docs/blob/main/docs/monitoring.md#loading-metric-alert-rules) and [Loading an app's alert rules](https://github.com/cplieger/docs/blob/main/docs/monitoring.md#loading-an-apps-alert-rules) show how. Both files work with the built-in schedule and with an outside scheduler, because every run happens in the container's main process.

| Alert | Fires when | Severity |
| --- | --- | --- |
| `FclonesDown` | Prometheus has not scraped the metrics page for 5 minutes | warning |
| `FclonesRunFailed` | the last completed run failed, including an action that processed none of the duplicates it found | warning |
| `FclonesScanStalled` | in `alerts/promql.yaml`, no run has succeeded by `fclones_scheduler_run_deadline_timestamp_seconds` plus 15 minutes, or, while that deadline is 0, no run has finished for 30 hours. In `alerts/logql.yaml`, no run has finished for 30 hours | warning |
| `FclonesFormatDrift` | the container did not recognize the summary line of the last fclones action | warning |
| `FclonesSpaceReclaimed` | runs in the last 30 hours processed duplicate files, with the count in its value | info |

Thresholds and the `severity` labels are starting points. The 30-hour windows are twice `SCAN_INTERVAL` plus twice `SCAN_TIMEOUT` at the defaults, so match them to yours. In `alerts/promql.yaml` the window of `FclonesScanStalled` is written as `108000` seconds and applies only while the run deadline reads 0, so with the built-in schedule that rule needs no change. Change the `container` selector to the label your log collector sets, and keep the scrape job name `fclones` that `FclonesDown` reads.

### Notes on each alert

`FclonesRunFailed` reads the latest run's verdict, so it fires at the first failed run and resolves at the next good one. The record's `result` field, or the `fclones_scheduler_last_run_result` series, names the cause, and the run's ERROR line has the detail. `action_no_op` means fclones opened none of the duplicates for writing. fclones only warns when that fails and then exits 0, so the container fails the run itself. The usual causes are a scanned folder the container's user cannot write to, or, for `dedupe`, a filesystem with no reflink support.

`FclonesScanStalled` in `alerts/promql.yaml` has two arms, and `fclones_scheduler_run_deadline_timestamp_seconds` picks one. While the deadline is above 0, which is the built-in schedule with a `SCAN_TIMEOUT` above 0, the alert fires 15 minutes after the deadline passes. The container works the deadline out from `SCAN_INTERVAL` and `SCAN_TIMEOUT`, so this arm has no window to change. Only a successful run moves the deadline, so runs that keep failing cross it too, beside `FclonesRunFailed`. One failed run between two good ones does not, because the deadline holds two intervals. While the deadline reads 0, the alert fires when no run has finished for 30 hours, or, before the first run, 30 hours after the process started. Load the rule file of the release you run, because an older image serves no deadline and neither arm can fire.

The log rule, and the second metric arm, fire on silence. They cannot tell a stuck schedule from a container that never started or runs under another name, or from logs that stopped reaching Loki, so rule those out first. With `SCAN_TIMEOUT=0`, a phase has no time limit and no window is safe. With `SCAN_INTERVAL=off`, use two phase timeouts plus the cadence of your scheduler. With `SCAN_INTERVAL=0`, drop the rule, because a container that exits after one run is silent on purpose. A restart adds nothing to the gap, because the records on `/cache` keep the schedule in step.

`FclonesFormatDrift` matters because the action still runs and the run still succeeds, so `FclonesRunFailed` does not catch it. Only the files and bytes reclaimed read wrong. A `--dry-run` in `FCLONES_ACTION_ARGS` causes it on purpose, because fclones then prints what it would do. The scan report is covered separately. It is read strictly, and an unreadable report fails the run with `result=decode_error`.

`FclonesSpaceReclaimed` is a success notice at `info`, for every action that changes files. The count is in its value and its annotation, so each count does not open a new alert. The space freed is in each record's `reclaimed_bytes`, or `fclones_scheduler_reclaimed_bytes_total`. The changed paths are in the same run's `duplicate file` lines. Find them with `{container="fclones"} |= "duplicate file"` and filter by the run's `scan_id`.

## Knowing the app is alive

| Route | Method | Port | Login | Answers |
| --- | --- | --- | --- | --- |
| `/healthz` | `GET`, `HEAD` | `METRICS_LISTEN_ADDR`, unset by default | none | `200 {"status":"ok"}`, or `503 {"status":"unhealthy","reasons":[...]}` |
| `/metrics` | `GET` | the same port | none, so keep the port on your network | Prometheus text format |

**What healthy means.** The last completed run succeeded, the check of `/cache` at start passed, and the container is not shutting down. With the built-in schedule, the container is also unhealthy from start until a due startup scan succeeds. It is unhealthy too when no run has finished for twice `SCAN_INTERVAL` plus twice `SCAN_TIMEOUT`, 30 hours at the defaults. `/healthz` names which of these failed in `reasons`. [How it works](how-it-works.md#health) explains how the container decides.

**Docker health.** The image checks itself every 30 seconds with `/app/wrapper health`, which reads the health file the app refreshes after each run at `/dev/shm/docker-fclones-scheduler.healthy`. If that file cannot be written, the check passes and the log says so once, while `/healthz` on the metrics listener still gives the true answer. Docker marks the container unhealthy after 3 failures, and allows 15 seconds after a start. Docker's health status notifies nobody on its own, and Docker does not restart an unhealthy container.

**Who is told.**

| You run | A dead app | A wedged app |
| --- | --- | --- |
| Prometheus with this app's rules | `FclonesDown` | `FclonesScanStalled`, and `/healthz` turns 503 |
| Loki only | `FclonesScanStalled` | the same |
| Docker or Unraid only | an uptime prober on `/healthz`, or something that watches Docker health | the same |

**Probing from outside.** The app opens no port unless `METRICS_LISTEN_ADDR` is set, so nothing outside the container can probe it until then. Watch Docker health, or set `METRICS_LISTEN_ADDR` and probe its `/healthz`. Check `GET http://<host>:<port>/healthz` and pass on status 200 only, without matching the body. Use a 5-second timeout, check every 60 seconds, and alert after 3 failures. With the built-in schedule and no recent good run on `/cache`, wait as long as your first scan takes after a first start. A Kubernetes liveness probe that must ignore readiness uses a `tcpSocket` probe on the port.

**With an outside scheduler**, no deadline applies, because the container cannot know your schedule. Alert on your scheduler's own job result: `wrapper scan` exits non-zero when the scan fails. **With `SCAN_INTERVAL=0`**, the exit code is the result: 0 for a good run, non-zero for a failed, timed-out or interrupted run, or one skipped because another container held the scan lock. `METRICS_LISTEN_ADDR` still opens the port in this mode, but `/healthz` and `/metrics` answer only while the one run lasts, so a prober or a scrape sees the container as down between runs.
