# Repository Guidelines

## Project Overview

CD211 is a qBittorrent WebAPI-compatible download client for Sonarr and Radarr. It accepts magnets or `.torrent` files, persists work in SQLite, asks CloudDrive2 to run a 115 offline download and copy the result to NAS staging, then reports completion only after local verification. It is a single Go binary: not a BitTorrent client, does not seed, and has no external queue or Redis dependency.

## Architecture & Data Flow

The composition root is `cmd/cd211`. Production packages live under `internal/` and use explicit constructor injection with narrow capability interfaces rather than a DI container.

```text
Sonarr/Radarr -> qBittorrent HTTP API -> torrentmeta validation
              -> SQLite submission -> reconcile scheduler
              -> CloudDrive2 offline task -> NAS copy -> fsafe verification
              -> durable state -> qBittorrent projection / Web UI
```

- `internal/domain` owns download states, legal transitions, models, and qBittorrent projections. Keep transition rules centralized there.
- `internal/store` is the source of truth. Reconciliation uses leases plus compare-and-swap commits; preserve claim/remote-operation/commit ordering and idempotent remote rechecks.
- `internal/reconcile` advances `StateAccepted -> StateSubmittingOffline -> StateWaitingOffline -> StateSubmittingCopy -> StateWaitingCopy -> StateVerifyingLocal -> StateCompleted`, with explicit stopped, failure, cancellation, and removal branches.
- `internal/clouddrive` and `internal/fsafe` isolate gRPC and filesystem side effects. Paths must be clean absolute paths under configured roots.
- `internal/httpapi` implements qBittorrent WebAPI 2.11; `internal/web` provides the server-rendered setup and operator UI. Both use the same SQLite-backed SID session service.
- `cmd/cd211/runtime.go` builds complete runtime generations and atomically swaps handlers when persisted settings change. The SQLite store and session service outlive generations; persisted session records also survive process restarts that reopen the same database.

## Key Directories

| Path | Purpose |
| --- | --- |
| `cmd/cd211/` | Process entry point, lifecycle, and dependency assembly. |
| `internal/domain/` | State machine, durable models, and API projections. |
| `internal/reconcile/` | Crash-safe scheduler and workflow transitions. |
| `internal/store/` | SQLite repository, embedded migrations, SQL queries, and generated sqlc code. |
| `internal/httpapi/` | qBittorrent-compatible API, authentication, and request handling. |
| `internal/web/` | Setup wizard, operator UI, templates, static assets, and i18n. |
| `internal/clouddrive/` | CloudDrive2 client and generated protobuf bindings. |
| `internal/fsafe/` | Root-constrained local verification and deletion. |
| `internal/{config,settings,creds,session,server,torrentmeta}/` | Bootstrap flags, persisted settings, credentials, sessions, health endpoints, and torrent parsing. |
| `scripts/` | Pinned protoc installation and protobuf generation. |
| `third_party/clouddrive2/` | Vendored CloudDrive2 protobuf source. |
| `docs/` | Architecture/invariants and UI design references. |

## Development Commands

Run from the repository root; the `Makefile` is authoritative.

```sh
make build             # CGO-free binary at bin/cd211
make test              # GOTOOLCHAIN=go1.26.5 go test ./...
make lint              # golangci-lint run
make generate          # regenerate protobuf and sqlc outputs
make image             # build cd211:local with Docker
make image-multiarch   # build linux/amd64 and linux/arm64 (requires buildx/QEMU)
docker compose up -d   # local container deployment
```

There is no dedicated run, format, typecheck, coverage, or race target. After `make build`, run locally with an absolute database path, for example:

```sh
./bin/cd211 --http-address :8080 --database-path "$PWD/.tmp/cd211.sqlite"
```

## Code Conventions & Common Patterns

- Use standard Go naming: exported `PascalCase`, unexported `camelCase`, and capability-oriented interfaces such as `Repository`, `CloudDrive`, `Filesystem`, `Clock`, and `Waker`.
- Format Go changes with `gofmt`; no repository-specific formatter is configured. Keep imports and generated code Go-tool compatible.
- Wrap errors with context using `fmt.Errorf("...: %w", err)` and classify with `errors.Is`. CloudDrive failures use structured error kinds; runtime logging uses JSON `slog`.
- Pass `context.Context` through I/O and apply deadlines/cancellation. Background work is owned by explicit runtime/scheduler lifecycles; do not add detached goroutines.
- Prefer constructor injection and small local interfaces. Tests commonly inject functions, fake clocks, fake repositories, fake RPC clients, and temporary stores.
- Persist shared SID sessions in SQLite by SHA-256 digest only; raw SIDs remain client-side. Only bounded login-attempt tracking and bans remain in process memory.
- Categories and paths are frozen into a submission; later settings changes affect future submissions only.
- Never hand-edit generated files in `internal/clouddrive/pb/` or `internal/store/sqlc/`; change the proto, migrations, or queries and run `make generate`.
- Preserve security boundaries: redact magnet trackers/passkeys from errors, constrain filesystem operations to configured roots, and do not make CloudDrive2 availability part of SQLite liveness.
- For every Web UI change under `internal/web/`, follow `docs/ref/linear-design-tokens.md`: preserve the dark-first high-density layout, Inter/monospace typography, 4px spacing grid, 2-6px dominant radii, restrained indigo accents, and subtle border/shadow elevation. Reuse its tokens; do not invent a parallel visual system.

## Changelog

- Every agent-authored change set must update `CHANGELOG.md` in the same change. Add a concise entry under `Unreleased` for code, documentation, configuration, CI, and repository-guidance changes; changelog-only edits are exempt.
- Use the existing `Added`, `Changed`, `Fixed`, `Removed`, or `Security` categories. Describe observable outcomes rather than commit messages or implementation details.
- Before creating a version tag, move all `Unreleased` entries into `## [X.Y.Z] - YYYY-MM-DD` and leave an empty `Unreleased` section. `scripts/extract-changelog.sh CHANGELOG.md vX.Y.Z` must succeed; the publish workflow rejects a tag without a matching non-empty section.

## Important Files

- `cmd/cd211/main.go`: startup flags, store/session initialization, setup mode, HTTP serving, shutdown.
- `cmd/cd211/runtime.go`: production dependency graph and runtime generation swaps.
- `internal/domain/state.go`: authoritative state and transition rules.
- `internal/reconcile/reconcile.go`: scheduler interfaces, claims, retries, and transition execution.
- `internal/store/store.go` and `internal/store/repository.go`: SQLite lifecycle and durable operations.
- `internal/httpapi/app.go`: qBittorrent routes and API limits.
- `internal/settings/settings.go`: persisted runtime settings and validation.
- `Makefile`, `go.mod`, `.golangci.yml`, and `sqlc.yaml`: toolchain and development contract.
- `Dockerfile`, `docker-compose.yml`, and `docker-entrypoint.sh`: deployment, volumes, identity, and health checks.
- `README.md`: operator workflow and supported configuration; `CHANGELOG.md`: versioned release notes; `docs/design.md`: detailed architecture and invariants.

## Runtime/Tooling Preferences

- Use Go Modules with Go `1.26.x`; `go.mod` and build scripts pin toolchain `go1.26.5`. There is no Node/Bun package manager.
- Production builds must remain `CGO_ENABLED=0`. SQLite uses `modernc.org/sqlite` and must live on a host-local filesystem with POSIX locking, not NFS/SMB.
- `make lint` requires an externally installed golangci-lint; CI uses v2.11 with `errcheck`, `govet`, `staticcheck`, and `unused`.
- `make generate` installs pinned generators under `.tools/`. The protoc installer supports Darwin arm64, Linux amd64, and Linux arm64 and requires `curl`, `unzip`, and a SHA-256 utility.
- The binary reads bootstrap configuration only from `--http-address` and `--database-path`; it reads no environment variables. Docker `PUID`/`PGID` are consumed only by the entrypoint.
- Default container paths are `/data` and `/downloads`. CloudDrive2, CD211, Sonarr, and Radarr must see the staging directory at the same absolute path and compatible group permissions.
- Treat the service as trusted-LAN only; do not expose its HTTP port publicly. There is no default admin password, so complete first-run setup immediately.

## Testing & QA

Tests use the standard `testing` package and live beside production packages as `*_test.go`. Use `TestXxx`, table-driven `t.Run`, and direct `t.Fatal`/`t.Errorf` assertions; no third-party assertion framework is present.

- Run all tests with `make test`; CI uses `go test ./... -count=1` to avoid cached results.
- Pure packages use unit tests and controllable fakes. Store tests use temporary SQLite databases; HTTP tests use `httptest`; CloudDrive2 contract tests use an in-memory gRPC `bufconn` server.
- Key contract coverage: `internal/httpapi/contract_test.go`, `internal/clouddrive/grpc_contract_test.go`, `internal/web/handler_test.go`, and `internal/reconcile/reconcile_test.go`.
- Add tests at the layer owning the observable contract. State changes belong in domain/reconcile tests; API behavior belongs in HTTP contract tests; persistence/CAS behavior belongs in store tests.
- The CI gate is lint + `go test ./... -count=1` + a CGO-free build. No coverage threshold, race target, fuzz suite, or benchmark policy is configured.
