# Changelog

All notable changes to CD211 are documented in this file.

Agents must add every repository change to the appropriate category under
`Unreleased`. Before tagging a release, move those entries into a versioned
section using the release version and date.

## [Unreleased]

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
