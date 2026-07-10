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

- `main.go` — composition root. Dispatches the `health` and `scan`
  subcommands and the default long-running daemon, and wires config plus the
  health marker (`health.NewMarker` from `github.com/cplieger/health`). The
  daemon dispatches on `config.Mode` (derived from `FCLONES_INTERVAL`):
  `runBuiltin` (a startup scan plus a `scheduler.RunLoop` interval loop), `runExternal` (idle
  until signalled, scans triggered out-of-band), and `runOnce` (a single
  scan+action, then exit — `FCLONES_INTERVAL=0`); `runScan` is the separate
  one-shot `scan`-subcommand path. Shutdown is driven by `signal.NotifyContext`
  (SIGTERM/SIGINT) and a `sync.WaitGroup` that drains in-flight scans.
- `config.go` — environment loading (`loadConfig`), logger setup
  (`setupLogger`), the `FCLONES_ACTION` allowlist (`parseAction`), the
  dangerous-flag rejection (`rejectDangerousArgs`), and the `FCLONES_INTERVAL`
  interpreter (`parseInterval`, delegating to `scheduler.ParseInterval` with
  `WithZeroAsOnce`, maps `Schedule.Mode` → `runMode`: a positive duration runs
  built-in, `off`/`disabled` idles, `0`/`0s` runs once, and an empty,
  unparseable, or negative value warns and falls back to the `3h` default
  cadence rather than disabling scans).
- `scheduler.go` — the two-phase run (`runFclonesJob`): `fclones group` scan
  (`buildScanArgs`) then the dedup action (`buildActionArgs` /
  `runFclonesAction`), each subprocess built via the `scheduler` library's
  `NewCommandRunner`. The action-decision logic (`shouldRunAction` /
  `suspectDrift`) tolerates fclones report-format drift; see Conventions and
  gotchas.
- The overlap `flock` comes from the `scheduler` library (`scheduler.TryLock`
  on `/cache/.fclones.lock`, called from `scheduler.go`) — one mechanism
  covering both the in-process built-in loop and cross-process external `scan`
  invocations; a racing scan skips rather than blocking, so it never overlaps
  the next run or corrupts the shared cache. There is no in-repo `lock.go`
  (removed when the app adopted `scheduler`).
- `outcome.go` — `classifyExecOutcome` is the single source of truth for the
  success / timeout / shutdown / exec_error classification used by both
  phases.

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
- When the `FCLONES_VERSION` Renovate PR bumps the version, four coupled
  artifacts must move in lockstep (each fail-closes the build if stale, so a
  broken build after a bump usually means one was missed):
  1. `ARG FCLONES_SHA256_AMD64` (Dockerfile) — recompute as the sha256 of the
     new `fclones-<version>-linux-musl-x86_64.tar.gz` release asset.
  2. `ARG FCLONES_COMMIT` (Dockerfile) — set to the commit the new
     `FCLONES_VERSION` tag dereferences to (`git rev-parse <tag>`); tags are
     mutable, so the arm64 source build pins the commit.
  3. The `Audited against fclones <version>;` comment in `config.go` — diff
     `fclones group --help` and `fclones <action> --help` for any new flag
     that executes an external command or mutates files in-place, extend
     `dangerousFlags` accordingly, and bump the audit comment to the new
     version (the go-builder grep gate refuses to build until it matches).
  4. The `internal/parsing` report parsers — re-check them against the new
     fclones report format (see the report-format drift note in Conventions and
     gotchas).
- Memory is bounded on purpose: per-stream capture (`streamCapBytes`, bounding
  fclones' stderr and the action phase's stdout), the report
  read (`outputCapBytes`, 50 MB), and the duplicate-log detail
  (`logDetailCapBytes`). Don't read subprocess output unbounded.

## Conventions and gotchas

- Logs are slog logfmt to stderr (`key=value`), with UTC timestamps via `slogx` (its `UTCTime` `ReplaceAttr`, so the image needs no `TZ` and embeds no `time/tzdata`). The `sloglint` linter is set
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
- fclones **report-format drift** is handled defensively. If a scan parses but
  `internal/parsing` finds no duplicate groups while fclones' own report
  disagrees (it reported a positive group count, or its `# Total:` line is
  missing or no longer matches), the pure `suspectDrift` predicate
  (`scheduler.go`, table-tested) makes `shouldRunAction` run the action against
  the full report via stdin instead of skipping dedup, so an upstream
  output-format change can't silently stop reclaiming space while the run still
  reports healthy. `ParseStats` exposes `TotalParsed` so a missing or changed
  `# Total:` line is distinguishable from a genuine zero. When you bump
  `FCLONES_VERSION`, re-check the `internal/parsing` parsers against the new
  report format; `suspectDrift` is the runtime backstop, not a substitute for
  that re-audit.

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
