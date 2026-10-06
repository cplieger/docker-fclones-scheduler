# Security

This page covers what the container accepts and refuses, a hardened compose example, and what the image contains. It is for readers who want to lock the container down further than the quick start does.

## What the container accepts

The image opens no ports and runs no HTTP server. The `scan` command reaches the main process through a socket at `/tmp/fclones-wrapper.sock`, which only the container's own user can open and only from inside the container. The image runs as `nonroot`, UID 65532, unless `user:` sets another user, on a distroless base with no shell and no package manager.

`FCLONES_ACTION` must be `group`, `link`, `remove` or `dedupe`. `--transform`, `--in-place` and `--no-copy` are refused in `FCLONES_ARGS`, `FCLONES_ACTION_ARGS` and `FCLONES_SCAN_PATHS`, because `--transform` runs a command and the other two change files in place. Set `ALLOW_UNSAFE_ARGS=true` only when you need one of these options, such as `--transform` for content-aware deduplication.

The container sets `--cache` and the report format itself, so `--cache`, `-f` and `--format` are refused in `FCLONES_ARGS` whatever `ALLOW_UNSAFE_ARGS` says. A word that is not a flag or a flag's value is refused too, because fclones would read it as one more folder to scan. `link`, `remove` and `dedupe` would then change files outside `FCLONES_SCAN_PATHS`.

Options reach fclones as a list of arguments with no shell to expand them. The output the container captures from fclones is capped at 1 MB per stream, and the scan report is read as a stream, so memory stays bounded whatever the size of the report. A report that cannot be read fails the run.

## Hardened compose settings

These settings add to the quick start's `compose.yaml`. [Hardening a compose file](https://github.com/cplieger/docs/blob/main/docs/hardening.md) explains each setting. The container writes only to `/scandir`, `/cache` and `/tmp`, so the rest of its filesystem can be read-only, and it runs with every Linux capability dropped:

```yaml
services:
  fclones:
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - "no-new-privileges:true"
    tmpfs:
      - "/tmp:size=128m,mode=1777,noexec,nosuid,nodev"
```

The `/tmp` tmpfs holds the trigger socket, the health file and the home folder the image sets with `HOME=/tmp`.

## What the image contains

| Component | Source |
| --- | --- |
| Rust builder stage | [Rust](https://hub.docker.com/_/rust) |
| Go builder stage | [Go](https://hub.docker.com/_/golang) |
| Distroless static, nonroot | [Distroless](https://github.com/GoogleContainerTools/distroless) |
| fclones | [GitHub](https://github.com/pkolaczk/fclones) |

[Renovate](https://github.com/renovatebot/renovate) keeps these up to date. Base images are pinned by digest, and the fclones download is pinned too, by the tarball's sha256 on `amd64` and by commit on `arm64`, where the image builds fclones from source. Each image carries a signed SBOM and provenance attestations. [Reading the software bill of materials](https://github.com/cplieger/docs/blob/main/docs/images.md#reading-the-software-bill-of-materials) and [Checking with the GitHub CLI](https://github.com/cplieger/docs/blob/main/docs/images.md#checking-with-the-github-cli) show how to check the SBOM.

## Accepted scanner findings

Live scan results are on the repository's Security tab. Two findings are accepted:

- hadolint DL3008, unpinned `apt-get` packages, fires in the Rust builder stage, which is not part of the final image.
- semgrep flags the health file path under `/tmp`. It is a fixed marker file and holds no data.
