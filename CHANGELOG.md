# Changelog

All notable changes to CD211 are documented in this file.

Agents must add every repository change to the appropriate category under
`Unreleased`. Before tagging a release, move those entries into a versioned
section using the release version and date.

## [Unreleased]

### Changed

- Replaced download-list route strips with compact bilingual step/progress statuses and redesigned detail progress as a mutually exclusive three-segment track; terminal states remain task-level and completion keeps its state-badge confirmation.

### Fixed

- Kept Web UI and qBittorrent login cookies valid across settings hot swaps and service restarts by making their shared sliding 30-day sessions and logout revocation durable in SQLite.

## [0.3.10] - 2026-08-13

### Fixed

- Kept English and Chinese navigation, setup steps, action groups, and download states readable at narrow widths; download lists now use compact state labels while preserving full accessible descriptions.

## [0.3.9] - 2026-08-13

### Added

- Added live download updates on the Downloads dashboard and detail pages: authenticated ETag/304 conditional polling (2s while any task is active, 10s terminal-only, bounded backoff with immediate resume on visibility and online events), server-rendered row/detail fragments keyed by hash and durable row_version, real progress interpolation with left-to-right stage handoff, a completion confirmation with a 1.2s hold before active-view exit, and filter/search/pagination/history updates that preserve focus and scroll without full page reloads. Forms and navigation keep working without JavaScript.

### Changed

- Consolidated all JS-driven motion behind one shared module (`motion.js`) with a single timing policy; same-origin navigation now keeps the sidebar steady and slides only the main content while the active nav item stays in place, and the delete dialog uses a shared purposeful open/close path that is safe under rapid reopen. All motion honors `prefers-reduced-motion`.
- Settings test/save, category create/save, webhook enable/disable/test/replay, and API-token generate/rotate/revoke submissions mark the button busy and block duplicate submissions while the server's own response stays authoritative.
- Reworked first-run setup step navigation: step URLs keep browser Back/Forward on reached steps without regressing wizard state, directional transitions keep the step rail continuous, connection feedback and directory results transition in place, and failed connection/path tests retain entered values with an explicit busy state.
- Defined the pinned Light theme alongside Dark, added explicit English and Simplified Chinese font fallback tokens, and made shared typography and radius tokens resolve consistently in both themes.

### Fixed

- Show the fixed action-column shadow only while it overlaps horizontally scrollable table content, removing the segmented gray stripe from wide tables.
- Kept primary-button labels and current setup-step numbers white and readable on indigo backgrounds in Light mode, including after cached stylesheet upgrades.

## [0.3.8] - 2026-08-13

### Fixed

- Made light mode the default and applied saved theme preferences before styles load, preventing a dark-to-light flash during page and browser-history navigation.

## [0.3.7] - 2026-08-13

### Fixed

- Redirected authenticated operators away from the login page to the dashboard.

## [0.3.6] - 2026-08-12

### Added

- Added durable structured problem codes for download failures, with localized English/Chinese warnings for automatic retries that show the next retry time and corrective guidance for terminal failures.
- Added nullable `error_code` and `next_retry_at` fields to the native automation API query model.

### Changed

- Reworked copy submission retry: CloudDrive2 not-ready, unreachable, and authentication observations keep a download non-terminal with persisted backoff, and a phase deadline now maps to a specific terminal code instead of a generic timeout.
- Magnet submissions no longer depend on CloudDrive2 directory metadata: the verified local copy decides file-vs-directory, size, and content path before completion, while uploaded `.torrent` submissions keep strict expected verification.

### Fixed

- Fixed premature terminal failures when a finished 115 offline task is not yet accepted by the CloudDrive2 copy service: the download now retries with an actionable structured problem instead of failing immediately.
- Prevented CloudDrive2 folder creation on temporary, authentication, or rejected lookup errors; only a verified not-found lookup creates the leaf folder.

## [0.3.5] - 2026-08-12

### Added

- Added a system-aware light theme with an accessible dashboard toggle and saved user preference.

### Changed

- Shortened the dashboard CloudDrive2 status to compact localized labels while preserving the full accessible description.

## [0.3.4] - 2026-08-12

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
