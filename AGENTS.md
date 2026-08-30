# Repository Guidelines

## Project Overview

CD211 is a single-binary Go service that presents a qBittorrent WebAPI-compatible download client to Sonarr and Radarr. It accepts magnets and `.torrent` files, persists submissions in SQLite, asks CloudDrive2 to perform 115 offline downloads and copy results into NAS staging, verifies local content, then reports completion. It is not a BitTorrent client, does not seed, and has no Redis or external worker dependency.

## Architecture & Data Flow

`cmd/cd211` is the composition root. Production packages live under `internal/` and use explicit constructor injection with narrow capability interfaces.

```text
Sonarr/Radarr -> httpapi -> submission/torrentmeta -> SQLite
                                                   -> reconcile scheduler
                                                   -> CloudDrive2 offline task
                                                   -> NAS copy -> fsafe verification
                                                   -> durable projection -> qBT/Web/native API
                                                   -> outbox -> signed webhooks
```

- `internal/domain` owns durable models, legal state transitions, validation, and qBittorrent projections. Keep state rules centralized there.
- `internal/store` is authoritative. Reconciliation claims leased rows, performs remote/filesystem work outside transactions, then commits with compare-and-swap. Preserve claim/side-effect/commit ordering and idempotent remote rechecks.
- `internal/reconcile` advances accepted work through offline submission/waiting, copy submission/waiting, local verification, and completion, with explicit stop, retry, failure, cancellation, and removal paths.
- `internal/submission` is the shared magnet/torrent intake path. Category and logical save paths are frozen into each submission; physical work uses `<save_path>/.cd211/<lowercase hash>`.
- `internal/clouddrive` and `internal/fsafe` isolate gRPC and filesystem effects. Paths must be clean absolute paths under configured roots; `.cd211` is reserved.
- `internal/httpapi`, `internal/nativeapi`, and `internal/web` expose qBittorrent, native, setup, and operator surfaces. `internal/outbox` plus `internal/webhook` provide durable at-least-once notifications.
- `cmd/cd211/runtime.go` builds complete runtime generations and atomically swaps handlers after settings changes. Store, sessions, webhook dispatcher, and process logger outlive a generation; each generation owns its reconciler and CloudDrive connection.

## Key Directories

| Path | Purpose |
| --- | --- |
| `cmd/cd211/` | Startup, lifecycle, dependency assembly, runtime generation swaps. |
| `internal/domain/` | State machine, durable models, validation, API projections. |
| `internal/submission/` | Shared magnet and torrent intake workflow. |
| `internal/reconcile/` | Crash-safe leased scheduler and workflow transitions. |
| `internal/store/` | SQLite repository, migrations, queries, generated sqlc code. |
| `internal/httpapi/`, `internal/nativeapi/` | qBittorrent-compatible and native HTTP APIs. |
| `internal/web/` | Setup wizard, operator UI, templates, static assets, i18n. |
| `internal/clouddrive/`, `internal/fsafe/` | CloudDrive2 gRPC and root-constrained filesystem effects. |
| `internal/outbox/`, `internal/webhook/` | Durable events and signed webhook delivery. |
| `internal/{config,settings,creds,session,logging,server,torrentmeta}/` | Bootstrap flags, persisted settings, auth, logs, health, torrent parsing. |
| `scripts/` | Pinned protobuf generation and changelog extraction. |
| `third_party/clouddrive2/` | Vendored CloudDrive2 protobuf source. |
| `docs/` | Architecture invariants and UI design references. |

## Development Commands

Run from the repository root; `Makefile` is authoritative.

```sh
make build             # CGO-free binary: bin/cd211
make test              # GOTOOLCHAIN=go1.26.5 go test ./...
make lint              # golangci-lint run
make generate          # regenerate protobuf and sqlc outputs
make web-preview       # authenticated real Web UI preview on 127.0.0.1:18080
make image             # local cd211:local container image
make image-multiarch   # linux/amd64 + linux/arm64 via buildx/QEMU
docker compose up -d   # local container deployment
```

There is no dedicated production run, format, typecheck, coverage, or race target. After `make build`, a local production run requires an absolute database path:

```sh
./bin/cd211 --http-address :8080 --database-path "$PWD/.tmp/cd211.sqlite"
```

For authenticated Web UI changes, use `make web-preview`, then exercise the page in a real browser. Override with `make web-preview VERSION=v0.4.9 WEB_PREVIEW_ADDRESS=127.0.0.1:18081`. Do not add a production authentication bypass for previewing.

## Code Conventions & Common Patterns

- Use standard Go naming: exported `PascalCase`, unexported `camelCase`; preserve project acronyms such as `QBT`, `QBTKey`, and `CD2`.
- Format Go with `gofmt`. Wrap errors with context using `fmt.Errorf("...: %w", err)` and classify with `errors.Is` or existing typed/sentinel errors. Return sanitized stable HTTP errors at trust boundaries.
- Pass `context.Context` through I/O and honor deadlines/cancellation. Background work belongs to explicit runtime, scheduler, or dispatcher lifecycles; do not add detached goroutines.
- Prefer constructors such as `New`, `Open`, and `Dial`, narrow local interfaces (`Repository`, `CloudDrive`, `Filesystem`, `Clock`, `Waker`), and direct injection. Do not add a DI container or parallel abstraction.
- Keep SQLite authoritative. Never hold a database transaction across network or filesystem work. Preserve leases, CAS commits, retry idempotency, and durable outbox ordering.
- Persist only SHA-256 SID digests; raw SIDs remain client-side. Preserve credential, URL, cookie, magnet tracker/passkey, and log redaction boundaries.
- Never hand-edit `internal/clouddrive/pb/` or `internal/store/sqlc/`; edit proto, migrations, or queries and run `make generate`.
- Web UI changes must follow `docs/ref/linear-design-tokens.md`; reuse its spacing, typography, radii, color, border, and shadow tokens rather than creating another visual system.
- Repository content is English except new `CHANGELOG.md` entries, which are Simplified Chinese. Every agent-authored change set must add a concise observable-outcome entry under `Unreleased` using `Added`, `Changed`, `Fixed`, `Removed`, or `Security`.

## Important Files

- `cmd/cd211/main.go`: flags, process logging, store/session initialization, setup/runtime selection, serving, shutdown.
- `cmd/cd211/runtime.go`: production dependency graph and atomic runtime generation activation.
- `internal/domain/state.go`: authoritative state and transition rules.
- `internal/submission/service.go`: common submission pipeline.
- `internal/reconcile/reconcile.go`: claims, retries, side effects, and transition commits.
- `internal/store/store.go`, `internal/store/repository.go`: SQLite lifecycle and durable operations.
- `internal/httpapi/handler.go`, `internal/nativeapi/handler.go`, `internal/web/handler.go`: public HTTP surfaces.
- `internal/fsafe/fsafe.go`: workspace containment, verification, and deletion safety.
- `internal/settings/settings.go`: persisted runtime settings and validation.
- `Makefile`, `go.mod`, `.golangci.yml`, `sqlc.yaml`: toolchain and generation contract.
- `Dockerfile`, `docker-compose.yml`, `docker-entrypoint.sh`: deployment, volumes, identity, health checks.
- `README.md`: operator workflow; `docs/design.md`: detailed invariants; `CHANGELOG.md`: release notes.

## Runtime/Tooling Preferences

- Use Go Modules with language version `1.26.0` and pinned toolchain `go1.26.5`. There is no Node/Bun package manager or frontend build step.
- Production builds must remain `CGO_ENABLED=0`. SQLite uses `modernc.org/sqlite` and must live on a host-local POSIX-locking filesystem, not NFS/SMB.
- `make lint` requires external golangci-lint; CI uses v2.11 with `errcheck`, `govet`, `staticcheck`, and `unused`.
- `make generate` installs pinned generators under `.tools/`. Protoc 35.1 installation supports Darwin arm64, Linux amd64, and Linux arm64 and requires `curl`, `unzip`, and `shasum` or `sha256sum`.
- The binary reads bootstrap settings only from `--http-address` and `--database-path`; service settings live in SQLite. Docker alone consumes `PUID`/`PGID`.
- CloudDrive2, CD211, Sonarr, and Radarr must mount staging at the same absolute path with compatible group permissions. Default container paths are `/data` and `/downloads`.
- Treat the service as trusted-LAN only. Do not expose its HTTP port publicly; complete first-run setup immediately because no default admin password exists.

## Testing & QA

Tests use Go's standard `testing` package beside production code. Follow existing table-driven `t.Run`, direct `t.Fatal`/`t.Errorf`, `t.Helper`, deterministic clocks, and same-package fixtures; do not introduce an assertion framework.

- Run all tests with `make test`; CI runs `go test ./... -count=1` plus lint and a CGO-free build.
- Store tests use temporary real SQLite databases; HTTP tests use `httptest`; CloudDrive2 contracts use in-memory gRPC `bufconn`; filesystem tests use `t.TempDir` and real path/symlink fixtures.
- Reuse existing harnesses and fakes. Key contracts live in `internal/httpapi/contract_test.go`, `internal/nativeapi/handler_test.go`, `internal/clouddrive/grpc_contract_test.go`, `internal/web/handler_test.go`, `internal/store/repository_test.go`, and `internal/reconcile/reconcile_test.go`.
- Put tests at the observable-contract owner: state/workflow in domain or reconcile; persistence/CAS/migrations in store; protocol behavior in the relevant API package; path safety in fsafe.
- `internal/web/web_preview_test.go` is opt-in and long-running. `make web-preview` starts the real authenticated handler with temporary seeded data for browser verification.
- No coverage threshold, race target, fuzz suite, or benchmark policy is configured.
