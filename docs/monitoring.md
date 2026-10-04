# Monitoring and alerts

This page lists the log lines docker-fclones-scheduler writes and the alert rules that ship with it, for readers who send container logs to Loki or a similar log store.

## What it logs

docker-fclones-scheduler has no metrics endpoint. Its state is in its container log, as logfmt lines with UTC times, so a log collector needs no special parser. Every line of one run carries the same `scan_id`. Every failed, timed-out, skipped or interrupted run logs a line with an `outcome` field, so one query such as `outcome=~".+"` finds them all. A successful run logs `scan complete`, and `action complete` when an action ran.

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

`scan complete` is logged after every scan, also one that found no duplicates. The `duplicate file` lines stop at 500 pairs or 64 KB per scan, taken from the first 100 groups. A `duplicate pairs truncated` line then gives the full totals. fclones' own messages reach the log with control characters and other unsafe characters shown as `\xNN`. A file name inside an fclones message therefore cannot fake or hide a log line.

With an outside scheduler, the `wrapper scan` command writes only its own short lines, `triggered scan accepted`, `triggered scan started` and `triggered scan complete` or `triggered scan failed`, to the log of whatever ran it. The scan's full output still goes to the container log.

## Alerting

Ship the container's logs to Loki, and load the rules in [`alerts/logql.yaml`](../alerts/logql.yaml) into [Loki's ruler](https://grafana.com/docs/loki/latest/alert/). Grafana Alloy's Docker log discovery ships the logs with no extra configuration. Firing alerts go through your Alertmanager like any Prometheus alert. The rules work with the built-in schedule and with an outside scheduler, because every scan runs in the container's main process and logs there.

| Alert | Fires when | Severity |
| --- | --- | --- |
| `FclonesScanStalled` | no `scan complete` line has arrived in 28h | warning |
| `FclonesLinkEstablished` | a `link` run replaced duplicates with hardlinks, with the count and the space freed | info |
| `FclonesActionReclaimedNothing` | a `link`, `remove` or `dedupe` run found duplicates and processed none of them | warning |
| `FclonesFormatDrift` | the container did not recognize the summary of the fclones action | warning |

The 28h window of `FclonesScanStalled` fits the default `SCAN_TIMEOUT` of 12h for each of the two phases plus the default `SCAN_INTERVAL` of 3h, with 1h to spare. Set it to two phase timeouts plus your interval, plus a margin. With `SCAN_INTERVAL=off`, use two phase timeouts plus the cadence of your scheduler. With `SCAN_INTERVAL=0`, drop the rule, because a container that exits after one scan is silent on purpose. With an outside scheduler you can also alert on your scheduler's own job result, because the `scan` command exits with the scan's result.

If you run `remove` or `dedupe` instead of `link`, change `action="link"` in `FclonesLinkEstablished` to match your `FCLONES_ACTION`.

Thresholds and the `severity` labels are starting points. Change the `container` selector to the label your log collector sets, such as `job` or `service`. Route by whatever labels your Alertmanager uses.
