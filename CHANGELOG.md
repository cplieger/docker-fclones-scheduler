# Changelog

## 2026.04.23-f018499 (2026-04-23)

### Dependencies

- Update rust:1.95-trixie docker digest to e4f09e8
- Update third-party dependencies

## 2026.04.18-b5fa30d (2026-04-19)

### Dependencies

- Update rust:1.95-trixie docker digest to 0ccf3b8
- Update rust:1.95-trixie docker digest to 4a7e3a0

## 2026.04.17-588b64e (2026-04-17)

### Changed

- Minor code improvements and optimizations

## 2026.04.14-73e6ba8 (2026-04-16)

### Dependencies

- Update rust:1.94-trixie docker digest to 652612f

## 2026.04.13-98ff0b3 (2026-04-13)

### Fixed

- Fixed typo

### Changed

- Refactor(fclones): improve error handling, logging, and resource management
- Update Go toolchain configuration

### Dependencies

- Update go to v1.26.2
- Update golang:1.26-trixie docker digest to 1d414b0 (#170)
- Update golang:1.26-trixie docker digest to 503c84f (#176)
- Update golang:1.26-trixie docker digest to 6a60657
- Update golang:1.26-trixie docker digest to c0074c7
- Update rust:1.94-trixie docker digest to dbc91e2
- Update rust:1.94-trixie docker digest to e8e2bb5
- Update third-party dependencies

## 2026.04.08-a83d7e1 (2026-04-09)

### Dependencies

- Update rust:1.94-trixie docker digest to e8e2bb5

## 2026.04.07-4c68a23 (2026-04-08)

### Changed

- Update Go toolchain configuration

### Dependencies

- Update go to v1.26.2
- Update golang:1.26-trixie docker digest to 1d414b0 (#170)
- Update golang:1.26-trixie docker digest to 503c84f (#176)
- Update golang:1.26-trixie docker digest to 6a60657
- Update rust:1.94-trixie docker digest to dbc91e2
- Update third-party dependencies

## 2026.04.01-878c624 (2026-04-01)

### Added

- Enhance security with unsafe flag bypass and TOCTOU protection
- Test(fclones): add property-based and edge case tests
- Add Go app binaries to gitignore and document slog logging standard
- Migrate to structured logging and improve shell safety
- Add input validation and SSRF protection
- Test(apps): add comprehensive test coverage for identity loading and cert conversion
- Add automated public repository publishing workflow
- Ci(validate): Add golangci-lint linting checks for Go apps
- Docs(steering): Add unit testing guidelines for Go code
- Add CrowdSec IPS, Grafana alerting, Go tests, and steering docs
- Add healthchecks and improve service reliability across apps
- Add variable to surpress "no duplicates found"
- Fclones added action args
- Added fclones

### Fixed

- Improve startup health state and graceful shutdown handling
- Fix tar extraction strip-components level
- Security and healthcheck fixes
- App fixes and cleanup
- Fix alpine version parsing
- Fix //n escapes
- Cache fix attempt 4
- Cache fix attempt 3
- Update w cache fix
- Fix cache?
- Fix whitespaces
- Discord notification fixes
- Whitespace fix
- Fclones fixes
- Fixes to fclones + new test folder
- Fix whitespaces
- Fix arguments again
- Fix arguments
- Fix schedule
- Fix arm compilation speed
- Fix build error

### Changed

- Refactor(fclones): extract magic strings to constants and improve byte comparison
- Refactor(fclones): extract action args building into separate function
- Refactor(fclones): reorganize code structure and improve cache handling
- Style(fclones): remove extra blank lines
- Remove Discord webhook integration and migrate to structured logging
- Remove Discord webhook integration and migrate to Alloy/Loki alerting
- Refactor(fclones): extract action execution and notification logic
- Update service extensions from rootless-internal to rootless-proxy
- Update service extensions and dependency policies
- Enable unparam linter and refactor dangerous args check
- Consolidate age encryption hooks and re-encrypt all env files
- Test(apps): refactor test structure and improve helper functions
- Update health checks and standardize environment variable quoting
- Migrate custom Go apps to file-based health checks
- Docs(steering): Update collaboration, operations, and structure guidance
- Update encrypted environment files across all services
- Ci(workflows,dockerfiles): Refactor build and validation pipelines
- Refactor(apps): Simplify slice append and reorganize sops tests
- New approach to env file management for sops
- New approach to sops .env file naming
- Update version
- Cleanup and restructuring of all files
- Update to new versioning scheme
- Move frigate and fclones secrets to env files
- Revert to rolling image
- Update Docker base image to new version
- New version
- Change fclones schedule
- Remove temp implementation
- Change container name
- Update with single quote support for paths
- Support paths with spaces with single and double quotes
- New version
- Allow for multiple scan paths
- Fclones temp
- Stop starting notification
- Final deployement - sha
- Version update
- Version update
- New fclones version
- Updated fclones
- Attempt 2 at improved discord notifications
- New version
- Version update
- Update version with new schedul
- Pin fclones version
- Removed trailing space
- Removed go.sum dependency
- Change fclones to debian13
- Pin dependencies

### Dependencies

- Update gcr.io/distroless/static-debian13:nonroot Docker digest to 01e550f
- Update gcr.io/distroless/static-debian13:nonroot Docker digest to f512d81
- Update gcr.io/distroless/static-debian13:nonroot Docker digest to f9f84bd
- Update gcr.io/distroless/static-debian13:nonroot docker digest to 0376b51
- Update gcr.io/distroless/static-debian13:nonroot docker digest to e3f9456 (#136)
- Update go to v1.26.0
- Update golang:1.25-trixie Docker digest to 0032c99
- Update golang:1.25-trixie Docker digest to 04741b0
- Update golang:1.25-trixie Docker digest to 1763926
- Update golang:1.25-trixie Docker digest to dfdd969
- Update golang:1.25-trixie Docker digest to f6a22bd
- Update golang:1.25-trixie Docker digest to fb4b74a
- Update golang:1.25-trixie Docker digest to ff83f37
- Update golang:1.26-trixie docker digest to 03c59a6
- Update golang:1.26-trixie docker digest to 100774d
- Update golang:1.26-trixie docker digest to 4e603da
- Update golang:1.26-trixie docker digest to 96b2878
- Update golang:1.26-trixie docker digest to 9c51d8b
- Update golang:1.26-trixie docker digest to ce3f1c8
- Update rust:1.92-trixie Docker digest to bed2d7f
- Update rust:1.92-trixie Docker digest to f589233
- Update rust:1.93-trixie Docker digest to 07cfdaf
- Update rust:1.93-trixie Docker digest to 20d4b66
- Update rust:1.93-trixie Docker digest to 8030252
- Update rust:1.93-trixie Docker digest to bbde3ca
- Update rust:1.93-trixie Docker digest to c234989
- Update rust:1.93-trixie Docker digest to e35d0f6
- Update rust:1.93-trixie docker digest to 4e7968e
- Update rust:1.93-trixie docker digest to 51c04d7
- Update rust:1.93-trixie docker digest to ecbe59a
- Update rust:1.94-trixie docker digest to 335533f
- Update rust:1.94-trixie docker digest to 689fa5f
- Update rust:1.94-trixie docker digest to 72724f1
- Update rust:1.94-trixie docker digest to 7e322aa (#132)
- Update rust:1.94-trixie docker digest to c328b17
- Update rust:1.94-trixie docker digest to f17e723
- Update rust:1.94-trixie docker digest to f2a0f2b
- Update third-party dependencies

## 2026.03.22-2206ed5 (2026-03-23)

### Dependencies

- Update rust:1.94-trixie docker digest to f17e723

## 2026.03.21-1c2020a (2026-03-22)

### Added

- Enhance security with unsafe flag bypass and TOCTOU protection

### Dependencies

- Update golang:1.26-trixie docker digest to ce3f1c8

## 2026.03.17-513f6ed (2026-03-18)

### Dependencies

- Update golang:1.26-trixie docker digest to 96b2878
- Update rust:1.94-trixie docker digest to 72724f1

## 2026.03.17-8528746 (2026-03-17)

### Dependencies

- Update golang:1.26-trixie docker digest to 9c51d8b
- Update rust:1.94-trixie docker digest to c328b17
- Update third-party dependencies

## 2026.03.15-57781d1 (2026-03-16)

### Dependencies

- Update gcr.io/distroless/static-debian13:nonroot docker digest to e3f9456 (#136)

## 2026.03.14-94143c3 (2026-03-14)

### Added

- Test(fclones): add property-based and edge case tests

### Changed

- Refactor(fclones): extract magic strings to constants and improve byte comparison
- Refactor(fclones): extract action args building into separate function

## 2026.03.12-459a44f (2026-03-12)

### Fixed

- Improve startup health state and graceful shutdown handling

### Dependencies

- Update rust:1.94-trixie docker digest to 335533f
- Update rust:1.94-trixie docker digest to 7e322aa (#132)

## 2026.03.11-e73cc45 (2026-03-11)

### Added

- Add Go app binaries to gitignore and document slog logging standard
- Migrate to structured logging and improve shell safety

### Changed

- Refactor(fclones): reorganize code structure and improve cache handling
- Style(fclones): remove extra blank lines
- Remove Discord webhook integration and migrate to structured logging
- Remove Discord webhook integration and migrate to Alloy/Loki alerting

## 2026.03.07-2867a03 (2026-03-08)

### Added

- Add input validation and SSRF protection

### Changed

- Refactor action execution and notification logic

## 2026.03.07-9112d85 (2026-03-07)

### Added

- Minor healthcheck code improvements and optimizations

## 2026.03.06-8b4b086 (2026-03-06)

### Changed

- Update service extensions from rootless-internal to rootless-proxy
- Update service extensions and dependency policies

## 2026.03.04-174fdde (2026-03-04)

### Dependencies

- Update rust:1.93-trixie docker digest to ecbe59a

## 2026.03.03-cdb462e (2026-03-04)

- Initial release
