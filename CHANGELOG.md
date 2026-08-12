# Changelog

All notable changes to CD211 are documented in this file.

Agents must add every repository change to the appropriate category under
`Unreleased`. Before tagging a release, move those entries into a versioned
section using the release version and date.

## [Unreleased]

### Added

- Added safe per-task pause and resume controls, with explicit record-only or record-and-local-files deletion choices.
- Added download name/hash search and 25-row pagination that preserve the active list filters after task actions.

### Changed

- Kept long download names to one line with ellipsis truncation and full-title hover text.

## [0.3.3] - 2026-08-12

### Changed

- Reworked the Chinese and English READMEs around user value, current setup, automation features, and a new dashboard hero image.
- Updated Docker CI and publishing actions to Node.js 24-based major versions.

### Fixed

- Filled unknown download sizes from CloudDrive2 offline task metadata without overwriting known totals.

## [0.3.2] - 2026-08-11

### Fixed

- Added a forward migration for webhook outbox databases missing the domain event sequence, preserving existing events, delivery references, and feed cursors.
- Logged the underlying webhook repository failure instead of a literal placeholder.

## [0.3.1] - 2026-08-11

### Changed

- Separated API token management from the main settings save actions.
- Clarified webhook endpoint setup with authentication guidance, event payload examples, and a dedicated delivery test action.

## [0.3.0] - 2026-08-10

### Added

- Added a native automation API secured by a single system-generated global API token, with JSON magnet and multipart torrent submission, status queries, terminal wait, and a completed/failed event pull feed.

### Changed

- Index-backed completed/failed event scans and sanitized, path-safe error output for the native automation API.

## [0.2.3] - 2026-08-10

### Added

- Added CloudDrive2 and local directory pickers to the first-run setup flow, including directory creation.
- Added signed outbound webhook notifications for completed and failed downloads: a transactional outbox, per-endpoint subscriptions, HMAC signing, bounded retry for up to 24 hours, dead-letter, and manual replay from delivery history.

### Changed

- Focused the README on operator setup and runtime configuration.
- Added repository guidance for contributors and coding agents.
- Made versioned `CHANGELOG.md` entries the source for GitHub release notes.
- Made category paths relative to their storage roots, with automatic remapping when roots change and guided category setup after onboarding.

### Fixed

- Made the download dashboard default to all qBittorrent-visible tasks and show localized state labels.
- Prevented startup permission hardening from invalidating SQLite WAL locks.
- Included CloudDrive2's rejection message in failed NAS copy diagnostics.

## [0.2.2] - 2026-08-09

### Changed

- Redesigned first-run setup as a clearer four-step wizard with responsive desktop and mobile layouts.
- Added visible step progress, improved form grouping, and a cleaner final configuration review.

### Fixed

- Made localization and static asset routes available during setup so language switching, styling, and scripts work before setup completes.
