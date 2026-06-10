# Contributing to docker-fclones-scheduler

A Go scheduler that wraps the [fclones](https://github.com/pkolaczk/fclones)
duplicate-file finder and ships as a distroless image. This guide covers the
bits a contributor needs that the
[org-wide defaults](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md)
don't: the package layout, the security guardrails you must not weaken, and
how to run the checks locally.

## Layout

The Go module is `github.com/cplieger/fclones-wrapper`; the built binary is
`wrapper`. The root `main` package is small and split by concern:

- `main.go` — composition root. `run(ctx)` wires config, the health marker,
  and two goroutines (a startup scan plus a `time.Ticker` loop). Shutdown is
  driven by `signal.NotifyContext` (SIGTERM/SIGINT) and a `sync.WaitGroup`
  that drains in-flight scans.
- `config.go` — environment loading (`loadConfig`), the `FCLONES_ACTION`
  allowlist (`parseAction`), and the dangerous-flag rejection
  (`rejectDangerousArgs`).
- `scheduler.go` — the two-phase run: `fclones group` scan
  (`buildScanArgs`) then the dedup action (`buildActionArgs` /
  `runFclonesAction`). `jobSlot` is a mutex that skips a scan if one is
  already in flight, so a slow scan never overlaps the next tick.
- `outcome.go` — `classifyExecOutcome` is the single source of truth for the
  success / timeout / shutdown / exec_error classification used by both
  phases.
- `health.go` — the marker path backing the `wrapper health` probe (from
  `github.com/cplieger/health`).

Three leaf packages under `internal/` hold the pure, well-tested logic:

- `internal/args` — quote-aware argument splitter (`args.Parse`). Env-var
  argument strings are split here, never handed to a shell.
- `internal/ioutil` — `FilteringWriter` (drops known fclones noise lines),
  `LimitedBuffer` (bounded stderr capture), and `ReadFileWithLimit`.
- `internal/parsing` — parsers for fclones report stats, duplicate groups,
  and the action summary line, plus human-byte formatting.

## Security guardrails (don't weaken these)

This container has no network listener and runs as `nonroot` on a distroless
base with no shell. The injection guardrails are load-bearing — keep them
intact when touching `config.go` or `scheduler.go`:

- Arguments reach `fclones` as explicit `exec.Command` arg lists via
  `args.Parse`. Never route argument strings through `sh -c` or any shell.
- `FCLONES_ACTION` is validated against the `group` / `remove` / `link` /
  `dedupe` allowlist in `parseAction`.
- `--command`, `--transform`, `--in-place`, and `--no-copy` are rejected in
  `rejectDangerousArgs` unless `FCLONES_ALLOW_UNSAFE=true`. New flag handling
  must preserve this opt-in gate.
- Memory is bounded on purpose: stderr capture (`stderrCapBytes`), the report
  read (`outputCapBytes`, 50 MB), and the duplicate-log detail
  (`logDetailCapBytes`). Don't read subprocess output unbounded.

## Conventions and gotchas

- Logs are slog logfmt to stderr (`key=value`). The `sloglint` linter is set
  to `kv-only`, so always use key/value pairs — `slog.Info("scan complete",
  "groups", n)`, never a formatted string.
- `main()` orchestration and the `exec.Command` calls to the `fclones` binary
  are intentionally not unit-tested (process-level I/O, validated by container
  logs and alerting). New logic in `internal/` or in config/arg parsing is
  expected to come with tests.
- Tests are property-based ([rapid](https://github.com/flyingmutant/rapid)) and
  table-driven, and live beside the code (`*_test.go`, `*_fuzz_test.go`).
  Property tests assert parsing never panics on arbitrary input and that
  config loading always yields a valid action.
- Fixed container paths (`/scandir`, `/cache`) are constants, not env vars —
  they are wired through volume mounts.

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
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md) —
never in a public issue.
