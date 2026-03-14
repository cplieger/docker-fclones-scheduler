# Changelog

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
