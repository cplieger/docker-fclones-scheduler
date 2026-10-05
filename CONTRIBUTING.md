# Contributing to docker-fclones-scheduler

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

A change to `FCLONES_VERSION` in the `Dockerfile` moves every item below in the same pull request. The build stops at the first stale pin, so fixing them one by one costs a failed build each.

- `ARG FCLONES_SHA256_AMD64` and `ARG FCLONES_LICENSE_SHA256`. A Renovate bump recomputes both from their `# repin:` markers. When you bump by hand, recompute both yourself.
- `ARG FCLONES_COMMIT`, by hand on every bump, with the `git ls-remote` command in its comment. The arm64 build compiles that commit and refuses a tag that points elsewhere.
- The `Audited against fclones <version>;` comment above `dangerousFlags` in `config.go`. Read `fclones group --help` and the help of each action for a new flag that runs a command or changes files in place, add any you find to `dangerousFlags`, then bump the comment.
- `licenses/crates/`. Run `sh scripts/vendor-crate-licenses.sh` and commit what it writes. The arm64 build refuses to build when the crates it collects differ from that folder's `MANIFEST`.
- The decoders in `internal/parsing`. Check `fclones/src/report.rs` at the new tag for the JSON report shape, and that the actions still print the `Processed ... reclaimed ...` line `ParseActionSummary` reads. A changed line fails the image smoke test but only warns at runtime.
- `rejectPositionalArgs` in `config.go` assumes every repeatable fclones option takes one value per occurrence. Check `fclones/src/config.rs` for `num_args` or `value_delimiter`. A greedy option would make it refuse valid settings.

A change to `dangerousFlags` also changes the list of refused options in the `ALLOW_UNSAFE_ARGS` row of `README.md`, in `docs/configuration.md` and in `docs/hardening.md`. Users read those pages to learn what the container refuses.

List only flags the pinned fclones has in `dangerousFlags`. An entry for a flag fclones does not have blocks nothing, and the docs then promise a refusal that never happens.
