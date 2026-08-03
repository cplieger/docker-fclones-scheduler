# Contributing to docker-fclones-scheduler

A Go scheduler that wraps the [fclones](https://github.com/pkolaczk/fclones)
duplicate-file finder and ships as a distroless image. This guide covers the
bits a contributor needs that the
[org-wide defaults](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md)
don't: the package layout, the security guardrails you must not weaken, and
how to run the checks locally.

## Layout

The Go module is `github.com/cplieger/docker-fclones-scheduler`; the built
binary is `wrapper`. The root `main` package is small and split by concern:

- `main.go`: dispatch (`health` probe, the `scan` trigger client, the
  default long-running process) and the composition root `run`, which wires
  the health marker (`health.NewMarker` from `github.com/cplieger/health`)
  and dispatches on `config.Mode` (derived from `FCLONES_INTERVAL`):
  `runOnce` performs a single direct scan+action and exits
  (`FCLONES_INTERVAL=0`), while the built-in and external modes hand every
  run to the daemon.
- `daemon.go`: the single owner of scan execution in the long-running
  modes (the shared single-owner scheduler shape, matching
  `docker-renovate-scheduler` / `docker-rsync-scheduler`): one executor
  goroutine (running `trigger.Execute` over the daemon's `run` callback)
  serves a bounded FIFO of trigger requests; the
  built-in ticker (`startTicker`, `scheduler.RunLoop`) and every socket
  client submit to the same queue. Shutdown is driven by
  `signal.NotifyContext` (SIGTERM/SIGINT): admission stops, the in-flight
  run is SIGTERM'd via its context, and queued requests get explicit
  cancellation results. The trigger plumbing itself (the bounded FIFO
  queue with exactly one result per accepted request and no coalescing, the
  owner-only unix-socket server at `/tmp/fclones-wrapper.sock`, and the
  newline-JSON wire frames `queued`/`started`/`done`) is the
  `scheduler` library's broker (`scheduler/v3/trigger`, payload
  `struct{}`: a scan takes no arguments); `daemon.go` wires it and owns
  the policy (executor semantics, log wording).
- `client.go`: the thin synchronous `scan` client (exit code = run
  result; never touches the marker): an adapter over `trigger.Submit`
  owning this app's lifecycle log lines and exit-code mapping.
- `health.go`: the marker path, the probe's freshness policy
  (`probeOptions`: built-in mode arms `health.WithMaxAge`), the
  `healthController` (the marker's single writer in the daemon modes, with
  a shutdown drain latch), and `jobHealthSignal` (skip/interrupt carve-outs).
- `config.go`: environment loading (`loadConfig`), logger setup
  (`setupLogger`), the `FCLONES_ACTION` allowlist (`parseAction`), the
  dangerous-flag rejection (`rejectDangerousArgs`), and the `FCLONES_INTERVAL`
  interpreter (`parseInterval`, delegating to `scheduler.ParseInterval` with
  `WithZeroAsOnce`, maps `Schedule.Mode` → `runMode`: a positive duration runs
  built-in, `off`/`disabled` idles, `0`/`0s` runs once, and an empty,
  unparseable, or negative value warns and falls back to the `3h` default
  cadence rather than disabling scans).
- `scheduler.go`: the two-phase run (`runFclonesJob`): `fclones group
  … -f json` scan (`buildScanArgs`) then the dedup action (`buildActionArgs`
  / `runFclonesAction`), each subprocess built via the `scheduler` library's
  `NewCommandRunner`. The scan report is decoded strictly
  (`parsing.DecodeReport`); a report the wrapper cannot read fails the run
  loudly (`outcome=decode_error`) instead of degrading stats silently.
- In-process runs are serialized by the daemon's single executor; the
  `flock` from the `scheduler` library (`scheduler.TryLock` on
  `/cache/.fclones.lock`) remains solely as the cross-container guard (a
  manual `docker run` sharing the same `/cache` volume skips rather than
  corrupting the shared cache). There is no in-repo `lock.go`.
- `outcome.go`: `classifyExecOutcome` is the single source of truth for the
  success / timeout / shutdown / exec_error classification used by both
  phases.

Three leaf packages under `internal/` hold the pure, well-tested logic:

- `internal/args`: quote-aware argument splitter (`args.Parse`). Env-var
  argument strings are split here, never handed to a shell.
- `internal/ioutil`: `FilteringWriter` (drops known fclones noise lines)
  and `LimitedBuffer` (bounded stderr capture).
- `internal/parsing`: the strict streaming JSON scan-report decoder
  (`DecodeReport`), the text action-summary parser (`ParseActionSummary`),
  and human-byte formatting.

## Security guardrails (don't weaken these)

This container has no network listener and runs as `nonroot` on a distroless
base with no shell. The injection guardrails are load-bearing; keep them
intact when touching `config.go` or `scheduler.go`:

- Arguments reach `fclones` as explicit `exec.Command` arg lists via
  `args.Parse`. Never route argument strings through `sh -c` or any shell.
- `FCLONES_ACTION` is validated against the `group` / `remove` / `link` /
  `dedupe` allowlist in `parseAction`.
- `--command`, `--transform`, `--in-place`, and `--no-copy` are rejected in
  `rejectDangerousArgs` unless `FCLONES_ALLOW_UNSAFE=true`. New flag handling
  must preserve this opt-in gate.
- When the `FCLONES_VERSION` Renovate PR bumps the version, four coupled
  artifacts must move in lockstep (each fail-closes the build if stale, so a
  broken build after a bump usually means one was missed). The first is
  automated; the other three still need a human on every bump:
  1. `ARG FCLONES_SHA256_AMD64` (Dockerfile): recomputed in the bump PR itself
     by the repin `postUpgradeTasks` script, which reads the `# repin:` marker
     above the ARG for the release-asset URL. Only touch it by hand if the PR
     body reports an artifact problem for that task.
  2. `ARG FCLONES_COMMIT` (Dockerfile): set to the commit the new
     `FCLONES_VERSION` tag dereferences to (`git rev-parse <tag>`); tags are
     mutable, so the arm64 source build pins the commit.
  3. The `Audited against fclones <version>;` comment in `config.go`: diff
     `fclones group --help` and `fclones <action> --help` for any new flag
     that executes an external command or mutates files in-place, extend
     `dangerousFlags` accordingly, and bump the audit comment to the new
     version (the go-builder grep gate refuses to build until it matches).
  4. The `internal/parsing` decoders: re-verify the JSON report schema at
     the new tag (`fclones/src/report.rs`: the `header`/`groups` shape,
     `ReportHeader.stats` remaining `Option<FileStats>`, `FileGroup`'s
     `file_len`/`files` fields) and the text action-summary wording
     (`Processed … reclaimed …`; see Conventions and gotchas).
- Memory is bounded on purpose: per-stream capture (`streamCapBytes`,
  bounding fclones' stderr and the action phase's stdout) and the
  duplicate-log detail (`logDetailCapBytes`). The scan report needs no cap:
  `DecodeReport` streams it group by group, retaining at most
  `maxLoggedGroups`. Don't read subprocess output unbounded.

## Conventions and gotchas

- Logs are slog logfmt to stderr (`key=value`), with UTC timestamps via
  `slogx` (its `UTCTime` `ReplaceAttr`, so the image needs no `TZ` and embeds
  no `time/tzdata`). The `sloglint` linter is set to `kv-only`, so always use
  key/value pairs: `slog.Info("scan complete", "groups", n)`, never a
  formatted string.
- `main()` orchestration and the `exec.Command` calls to the `fclones` binary
  are intentionally not unit-tested (process-level I/O, validated by container
  logs and alerting). New logic in `internal/` or in config/arg parsing is
  expected to come with tests.
- Tests are property-based ([rapid](https://github.com/flyingmutant/rapid)) and
  table-driven, and live beside the code (`*_test.go`, `*_fuzz_test.go`).
  Property tests assert parsing never panics on arbitrary input and that
  config loading always yields a valid action.
- Fixed container paths (`/scandir`, `/cache`) are constants, not env vars;
  they are wired through volume mounts.
- fclones **report-format drift** fails loudly by design. The scan consumes
  fclones' machine-readable JSON report (`-f json`, wrapper-owned flag);
  `parsing.DecodeReport` is strict (missing header/stats, a malformed or
  truncated document, a group without a keeper+duplicate, or a group count
  disagreeing with the header are all errors), so an upstream format change
  is a failed run with `outcome=decode_error`, never silently zeroed stats.
  The one surviving text surface is the ACTION summary line
  (`ParseActionSummary`): an unrecognized summary logs the
  "possible fclones format drift" warning (`resolveActionSummary`) that the
  documented alert rule catches. When you bump `FCLONES_VERSION`, re-verify
  both surfaces per the version-bump checklist above; the loud decode and
  the drift warning are runtime backstops, not substitutes for that
  re-audit.

## Running checks locally

Requires Go (see the version in `go.mod`) and `golangci-lint` v2.

```sh
go build ./...
go test ./...                       # unit + property + table-driven tests
golangci-lint run                   # lint (config in .golangci.yaml)
golangci-lint fmt                   # apply gofumpt (extra-rules) + gci ordering
```

`golangci-lint run` reports unformatted files as issues, so run
`golangci-lint fmt` before pushing. To exercise a fuzz target:

```sh
go test -run '^$' -fuzz FuzzParse -fuzztime 30s ./internal/args
```

To build the multi-stage image (Rust builder for `fclones`, Go builder, then
distroless):

```sh
docker build -t fclones-scheduler .
```

## Commits and PRs

This repo uses [Conventional Commits](https://www.conventionalcommits.org/)
parsed by git-cliff to generate release notes, so the commit subject becomes a
changelog line: `feat:` (Added), `fix:` (Fixed), `sec:` (Security),
`chore(deps):` (Dependencies). Keep changes focused and open an issue first for
larger ones. Open the PR against `main` and fill in the template.

## Conduct & security

By participating you agree to the
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report vulnerabilities via the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md),
never in a public issue.
