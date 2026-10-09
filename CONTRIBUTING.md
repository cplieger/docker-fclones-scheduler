# Contributing to docker-fclones-scheduler

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

A change to the fclones version in the `Dockerfile` moves every item below in the same pull request. The build stops at the first stale pin, so fixing them one by one costs a failed build each.

- `ARG FCLONES_REF` and `ARG FCLONES_COMMIT` sit under one `# renovate:` marker, and a Renovate bump moves both. A hand bump sets `FCLONES_COMMIT` to what `git ls-remote https://github.com/pkolaczk/fclones.git "refs/tags/<tag>^{}"` prints. The arm64 build refuses a tag that points elsewhere.
- `ARG FCLONES_SHA256_AMD64` and `ARG FCLONES_LICENSE_SHA256`. A Renovate bump recomputes both from their `# repin:` markers. When you bump by hand, recompute both yourself.
- `internal/fclonesflags/flags.txt`. The image build fails when the new fclones adds or removes an option, and names each one. Add one that runs a command or changes files in place to `dangerousFlags` in `config.go`, then regenerate the file with the command the failure prints.
- `licenses/crates/`. A Renovate bump runs `sh scripts/vendor-crate-licenses.sh` in the same commit. When you bump by hand, run it and commit what it writes. The arm64 build refuses to build when the crates it collects differ from that folder's `MANIFEST`.
- The decoders in `internal/parsing`. Check `fclones/src/report.rs` at the new tag for the JSON report shape, and that the actions still print the `Processed ... reclaimed ...` line `ParseActionSummary` reads. A changed line fails the image smoke test but only warns at runtime.
- `rejectPositionalArgs` in `config.go` assumes every repeatable fclones option takes one value per occurrence. Check `fclones/src/config.rs` for `num_args` or `value_delimiter`. A greedy option would make it refuse valid settings.

A change to `dangerousFlags` also changes the list of refused options in the `ALLOW_UNSAFE_ARGS` row of `README.md`, in `docs/configuration.md` and in `docs/hardening.md`. Users read those pages to learn what the container refuses.
