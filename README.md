# docker-fclones-scheduler

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/size.json)](https://github.com/cplieger/docker-fclones-scheduler/pkgs/container/docker-fclones-scheduler) [![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)](https://github.com/cplieger/docker-fclones-scheduler/pkgs/container/docker-fclones-scheduler) [![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)](https://github.com/cplieger/docker-fclones-scheduler/blob/main/Dockerfile) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/docker-fclones-scheduler/badges/mutation.json)](https://github.com/cplieger/docker-fclones-scheduler/issues?q=label%3Agremlins-tracker) [![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/docker-fclones-scheduler/releases)

<!-- hub-overview BEGIN -->
docker-fclones-scheduler runs the [fclones](https://github.com/pkolaczk/fclones) duplicate finder on a schedule, in a rootless container, and replaces the copies it finds with hardlinks or removes them. It has no web page and writes its results to its log.

## What it does

docker-fclones-scheduler gets back the disk space that duplicate files take, with no cron job to write:

- Scans your folders on the interval you set, 3 hours by default, or when your scheduler asks.
- Replaces each extra copy with a hardlink or a copy-on-write clone, deletes it, or only reports it.
- Logs each duplicate it found, up to 500 pairs per scan, and the space it freed.
- Marks itself unhealthy when a scan fails or an action changes no duplicate, until the next good scan.

## Who it is for

docker-fclones-scheduler is built for a media library or a file share on an always-on Linux machine with Docker, where the same file can land in several folders. It checks every setting before the first scan and, by default, refuses fclones options that run commands. You need a folder the container's user can write to, and hardlinks need the copies on one filesystem.

Two other projects suit a different setup:

- Consider [fclones-gui](https://github.com/pkolaczk/fclones-gui) if you want to pick by hand, in a desktop window, which copies to remove. It is the fclones author's interactive frontend.
- Consider [Krokiet](https://github.com/qarmin/czkawka) if you also want to find similar images, similar videos or music duplicates, in a desktop app.

docker-fclones-scheduler is free software under the Apache-2.0 license.
<!-- hub-overview END -->

## Quick start

The image is on GitHub Container Registry and Docker Hub, for `amd64` and `arm64`. This is the [`compose.yaml`](compose.yaml) in this repository.

```yaml
services:
  fclones:
    image: ghcr.io/cplieger/docker-fclones-scheduler:latest
    container_name: fclones
    restart: unless-stopped
    # Run "sudo mkdir -p /opt/appdata/fclones && sudo chown 1000:1000 /opt/appdata/fclones"
    # before the first start, or the container exits. If .env sets PUID and PGID, use those numbers.
    user: "${PUID:-1000}:${PGID:-1000}"

    environment:
      SCAN_INTERVAL: "1h"  # or 30m, 12h. "off" waits for an outside trigger and "0" scans once
      FCLONES_SCAN_PATHS: "/scandir"  # must match a volume target below
      FCLONES_ARGS: "--rf-over 1"  # report files that have more than one copy
      FCLONES_ACTION: "link"  # group (report only), link (hardlink), remove (delete) or dedupe (reflink)
      FCLONES_ACTION_ARGS: "--priority bottom"  # keep the first copy fclones lists, replace the others

    volumes:
      - "/path/to/media:/scandir"  # link, remove and dedupe change files here, so this user must be able to write to it
      - "/opt/appdata/fclones:/cache"  # the container exits at start if this user cannot write here
```

1. Create the cache folder and give it to user 1000 with `sudo mkdir -p /opt/appdata/fclones && sudo chown 1000:1000 /opt/appdata/fclones`. If `.env` sets `PUID` and `PGID`, use those numbers.
2. Replace `/path/to/media` with the folder to deduplicate. For `link`, `remove` and `dedupe`, that same user must be able to write to it.
3. For a first run that changes nothing, set `FCLONES_ACTION` to `"group"`. The log then lists the duplicates and the space you would get back.
4. Run `docker compose up -d`.

Run `docker logs fclones`. You should see a `scan complete` line with `groups=` and `duplicate_files=` counts. If you see `cache directory verification failed`, the cache folder does not belong to the container's user, so repeat step 1.

On Unraid, open the **Apps** tab, search for fclones-scheduler and click **Install**.

## Configuration reference

Settings are environment variables, read once at start, so recreate the container after a change. [Configuration](docs/configuration.md) covers the three scheduling modes, extra fclones options and a slow first scan.

| Variable | Description | Default |
| --- | --- | --- |
| `SCAN_INTERVAL` | Time between scans, such as `30m`, `1h` or `12h`. `off` waits for an outside trigger, and `0` scans once and exits | `3h` |
| `FCLONES_SCAN_PATHS` | Folders inside the container to scan, separated by spaces. Each one needs its own volume | `/scandir` |
| `FCLONES_ARGS` | Extra fclones options for the scan, as flags and their values. `--cache`, `-f` and `--format` are refused | _(unset)_ |
| `FCLONES_ACTION` | `group` only reports, `link` makes hardlinks, `remove` deletes, `dedupe` makes copy-on-write clones | `group` |
| `FCLONES_ACTION_ARGS` | Extra fclones options for the action, as flags and their values | _(unset)_ |
| `ALLOW_UNSAFE_ARGS` | `true` allows `--transform`, `--in-place` and `--no-copy`, which are refused otherwise | `false` |
| `SCAN_TIMEOUT` | Longest time the scan and the action may each run before the run is stopped and fails. `0` means no limit | `12h` |
| `LOG_LEVEL` | `debug`, `info`, `warn` or `error`. Any other value means `info` | `info` |

| Mount | Description |
| --- | --- |
| `/scandir` | The folder to scan, matching `FCLONES_SCAN_PATHS`. `link`, `remove` and `dedupe` change files here, so the container's user must be able to write to it |
| `/cache` | The fclones hash cache and the record of the last scan. The container exits at start when its user cannot write here |

## Security

The image opens no ports. An outside scheduler starts a scan with `docker exec fclones /app/wrapper scan`, through a local socket that only the container's own user can open. The container runs as the user that `user:` names, on a distroless base with no shell. Without a `user:` line it runs as UID 65532. `FCLONES_ACTION` must be one of the four actions.

The fclones options that run a command or change a file in place are refused unless `ALLOW_UNSAFE_ARGS` is `true`. Leave it `false` unless you need one of them, such as `--transform`. Options reach fclones as a list, with no shell to expand them. [Security](docs/security.md) has a hardened compose example and what the image contains.

## Troubleshooting

The healthcheck reads a file the container updates after each scan. Unhealthy means the last scan or action failed, or its report could not be read. It also means an action changed none of the duplicates the scan found, or the check of `/cache` at start failed. It turns healthy again after the next good scan, with no restart. With the built-in schedule, a container that has not scanned for twice `SCAN_INTERVAL` plus twice `SCAN_TIMEOUT` is unhealthy too. With the defaults that is 30 hours. [How it works](docs/how-it-works.md#health) has the full rules.

- The container restarts in a loop with `cache directory verification failed`. The cache folder does not belong to the container's user. Repeat step 1 of the quick start.
- A run fails with `action reclaimed nothing`. The container's user cannot write to the scanned folder, or, for `dedupe`, the filesystem has no reflink support.
- The container exits with `positional argument not allowed`. Give each pattern its own flag, as in `--name '*.mp4' --name '*.mkv'`.
- The container shows unhealthy during a long first scan. Raise `start_period`, as [Configuration](docs/configuration.md#a-slow-first-scan) shows.

## Monitoring

docker-fclones-scheduler writes logfmt lines with UTC times to its container log and has no metrics endpoint. Four Loki alert rules ship in [`alerts/logql.yaml`](alerts/logql.yaml). [Monitoring and alerts](docs/monitoring.md) lists the log lines and the rules and shows how to load them.

## Documentation

- [Configuration](docs/configuration.md) covers the scheduling modes, extra fclones options and a slow first scan.
- [How it works](docs/how-it-works.md) explains the schedule, the two phases of a run and the health rules.
- [Monitoring and alerts](docs/monitoring.md) lists the log lines and the alert rules.
- [Security](docs/security.md) has the hardened compose example and what the image contains.

## Credits

This project packages [fclones](https://github.com/pkolaczk/fclones) (MIT) into a container image. All credit for finding and removing duplicate files goes to the fclones maintainers.

## Contributing

Issues and pull requests are welcome. Please open an issue first for larger changes, and see [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE). The image carries the license text of every bundled component under `/usr/share/licenses/`.

The image packages [fclones](https://github.com/pkolaczk/fclones) (MIT) at the version pinned by `FCLONES_VERSION` in the Dockerfile: amd64 downloads and unpacks the upstream release tarball `https://github.com/pkolaczk/fclones/releases/download/<tag>/fclones-<version>-linux-musl-x86_64.tar.gz`, arm64 builds that same tag from source at a pinned commit. The build applies no patches, so this repository's Dockerfile plus the upstream sources it names is the complete recipe for the fclones binary the image ships. The license texts of the Rust crates compiled into the amd64 binary are kept under `licenses/crates/` in this repository, regenerated by `scripts/vendor-crate-licenses.sh`.
