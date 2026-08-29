# CD211

English | [简体中文](README.md)

<p align="center">
  <img src="docs/assets/cd211-dashboard.png" alt="CD211 download dashboard">
</p>

## Bring 115 offline downloads to Sonarr and Radarr

CD211 connects to Sonarr and Radarr through the qBittorrent Web API and hands magnet links or `.torrent` files to CloudDrive2. It waits for the 115 offline download, copies the content into an isolated per-hash workspace under the shared NAS staging root, verifies the local files, and only then reports the download as complete.

Keep the media automation workflow you already know. Replace brittle scripts with one Web UI where the whole download, copy, verification, and import handoff is visible and recoverable.

## Why CD211

- **Keep your existing workflow:** configure CD211 as a qBittorrent download client in Sonarr or Radarr.
- **Import only ready files:** a task completes only after the 115 download, NAS copy, and local verification all succeed.
- **See and recover failures:** inspect progress, files, history, and errors; start, retry, cancel, or clean up tasks from one dashboard.
- **Change settings without restarts:** manage CloudDrive2, storage paths, timeouts, categories, and credentials in the Web UI.
- **Connect your automation:** use the native API or signed webhooks with retries, dead-letter handling, and manual replay.
- **Run it continuously:** downloads and settings survive restarts in SQLite, with health endpoints for containers.

The Web UI is available in English and Simplified Chinese.

## How it works

```text
Sonarr / Radarr
      │  magnet link or .torrent
      ▼
    CD211 ──► CloudDrive2 ──► 115 offline download
      ▲                              │
      │      local verification ◄── NAS copy
      │
      └── reports completion only when files are ready
```

CD211 is not a BitTorrent client and does not seed. Removing a task from CD211 never deletes its copy in 115.

## Before you start

You need:

- CloudDrive2 with 115 mounted and its gRPC service reachable.
- Sonarr, Radarr, or an automation client using the native CD211 API.
- Docker Compose.
- One NAS staging directory shared by CloudDrive2, CD211, Sonarr, and Radarr.

> [!IMPORTANT]
> All four containers must mount the same host staging directory at the same absolute path. The default is `/downloads`. Files created by CloudDrive2 must also be group-writable.

```text
Host staging directory -> CloudDrive2: /downloads
                       -> CD211:       /downloads
                       -> Sonarr:      /downloads
                       -> Radarr:      /downloads
```

CD211 keeps the qBittorrent `save_path` as the logical category root; it is never replaced by the physical workspace. New downloads use an isolated physical workspace at `<save_path>/.cd211/<lowercase-hash>` and report the verified `content_path` inside that workspace. Legacy rows have an empty `WorkspacePath` and retain the historical shared layout. Because `.cd211/<hash>` is nested under the existing staging mount, no extra volume is needed; CloudDrive2, CD211, Sonarr, and Radarr must see it at the same absolute path.

To prevent cross-task deletion, CD211 rejects a workspace that overlaps another task's logical save path, and rejects conflicting workspace paths; multiple tasks may still share the same logical save-path parent.

`.cd211` is a globally reserved exact path component. A configured local root, download logical `save_path`, or category save root is invalid if either its clean absolute path or its fully resolved canonical path contains a component named exactly `.cd211`; internally generated `WorkspacePath` values are exempt. Near-names such as `.cd211-backup` remain valid. Ordinary symlink aliases without the reserved component also remain valid; distinct hashes stay isolated even when different logical roots resolve to the same physical save root. To prevent data loss, this rule intentionally rejects logical roots and symlink targets that may previously have been valid.

The upgrade SQL migration checks only the literal components of persisted paths. It aborts atomically before changing the schema or backfilling data if the exact `.cd211` component occurs in any category save path, or in the logical `save_path` of any download that is not deleted or is still retained; it cannot detect a legacy path whose symlink target resolves into `.cd211`. CD211 therefore runs a filesystem-aware canonical-path preflight before activating a runtime at startup or during a settings hot swap. The preflight checks the configured local root, every category save root, and the logical `save_path` of every download that is not `DELETED`, plus each `DELETED` download whose `content_path` is non-empty and whose file deletion was not requested. A legacy symlink alias that fully resolves through `.cd211` or outside the configured local root makes startup or settings apply fail; a failed hot swap leaves the current runtime active. Rename or repoint the offending symlink, or reconfigure and correct the corresponding local root, category, or stored download save path, then restart or apply Settings again. Deleted downloads that no longer retain local content are exempt only from these preflight checks; a later write to their logical save path is still rejected.

CD211 preserves directory ownership and group, and hardens modes before use: the logical save path is exactly `03770` (sticky and setgid), `.cd211` is exactly `02750` (owner-writable and group-traversable, not group-writable), and each hash workspace is exactly `02770` (setgid and group-writable). Persisted `save_path` and `WorkspacePath` remain the paths fixed at submission; symlinks are resolved only for internal boundary checks, and a later retarget outside the configured root fails closed without rewriting the saved paths.

Use the same `PGID` for CloudDrive2, CD211, Sonarr, and Radarr. When using the official CloudDrive2 image, start it with `umask 0002` so CD211 can manage copied files.

## Quick start

### 1. Start CD211

```sh
mkdir -p cd211/downloads
cd cd211
curl -LO https://raw.githubusercontent.com/turygo/cd211/main/docker-compose.yml
docker compose up -d
```

The default configuration:

- Serves the Web UI on port `8080`.
- Stores SQLite data in the `cd211_data` volume.
- Mounts `./downloads` as `/downloads` inside the container.

To choose the process user and group, create `.env` before startup:

```dotenv
PUID=99
PGID=100
```

### 2. Complete first-run setup

Open `http://<cd211-host>:8080` and follow the wizard:

1. Set a password for the fixed username `admin`. It must contain at least 8 characters; there is no default password.
2. Enter the CloudDrive2 gRPC address, username, password, and TLS mode.
3. Select the 115 offline download root and the shared NAS staging root. The default staging root is normally `/downloads`.
4. Set timeouts for the offline download, NAS copy, and local verification.

The wizard can browse and create both CloudDrive2 and local directories. It opens category setup when complete.

### 3. Register categories

Give every Sonarr or Radarr category a cloud and local subdirectory:

| Field | TV example | Movie example |
|---|---|---|
| Category name | `tv` | `movies` |
| 115 subdirectory | `TV` | `Movies` |
| Staging subdirectory | `tv` | `movies` |
| Status | Enabled | Enabled |

With `/115open/云下载` as the 115 root and `/downloads` as the shared staging root, these categories use `/115open/云下载/TV`, `/downloads/tv`, and the corresponding movie paths.

The category name must match the value in Sonarr or Radarr. Changing a root keeps existing tasks on their original paths and applies the new mapping to future tasks.

### 4. Add CD211 to Sonarr or Radarr

Open **Settings → Download Clients → Add → qBittorrent**:

| Field | Value |
|---|---|
| Host | A hostname or IP from which Sonarr or Radarr can reach CD211 |
| Port | `8080` |
| Use SSL | Off by default; enable only when your own reverse proxy provides TLS |
| Username | `admin` |
| Password | The password created during first-run setup |
| Category | An enabled CD211 category such as `tv` or `movies` |

Run the connection test and save. Sonarr or Radarr can now submit and track downloads as if CD211 were qBittorrent.

### 5. Connect ANI-RSS

Generate a qBittorrent API Key on CD211's **Settings** page. In ANI-RSS download settings, select qBittorrent, enter the CD211 address, paste the `qbt_` key into the qBittorrent password/API key field, and run the connection test.

Use a placeholder such as `qbt_<key>` in documentation, logs, and screenshots; never include a real credential.

## Automation

### qBittorrent Web API

`/api/v2` accepts the durable `SID` session cookie or `Authorization: Bearer qbt_<key>`. When an `Authorization` header is present, Bearer authentication takes precedence; malformed, unknown, or revoked keys return `403` and never fall back to a valid SID cookie. Without that header, the SID cookie is checked.

The independent `qbt_` key can be generated and revoked from Settings. SQLite stores its plaintext for the authenticated Settings page together with its SHA-256 digest and trailing hint; every request verifies the digest, so revocation takes effect immediately. The key authorizes only `/api/v2`; the `cd211_api_` token authorizes only `/api/v1`.

`/api/v2` implements the ANI-RSS compatibility subset: seven read endpoints accept both GET and POST, with durable add/start, tags, file priorities, file renames, disabled-only automatic management, and save-location updates. It is not the full qBittorrent API and does not implement BitTorrent transfer, seeding, bandwidth controls, or a `/torrents/resume` alias.

### Native API

Generate the global `cd211_api_` Automation Token on the Web UI **Settings** page, then call `/api/v1` with `Authorization: Bearer <cd211_api-token>`:

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/downloads` | Submit a magnet link or torrent file |
| `GET /api/v1/downloads/{hash}` | Read download status |
| `GET /api/v1/downloads/{hash}/wait` | Wait for completion, failure, cancellation, or deletion |
| `GET /api/v1/events` | Pull completed and failed download events |

The API accepts magnets as JSON and torrent files as multipart data. The event feed uses opaque cursors and at-least-once delivery; deduplicate events by ID.

The Automation Token is shown only when generated or rotated. Rotation immediately invalidates the previous token, and revocation disables the API. A `cd211_api_` token is valid only for `/api/v1`, not `/api/v2`; the independent `qbt_` key is likewise not valid for `/api/v1`. The native API does not expose retry, cancel, or delete endpoints; use the Web UI for those controls.

### Webhooks

CD211 can send HMAC-SHA256-signed webhooks when a download completes or fails. Each endpoint selects its own events and supports:

- Optional Bearer authentication.
- Test delivery from the Web UI.
- Exponential-backoff retries for up to 24 hours.
- Dead-letter records and manual replay.
- Delivery history, status filtering, and sanitized errors.

Verify `X-CD211-Signature` against the raw request body and deduplicate deliveries by `X-CD211-Event-ID`.

## Configuration reference

### Docker Compose

| Setting | Default | How to change it |
|---|---:|---|
| HTTP port | `8080` | Change the host side of `ports` |
| Process user | `PUID=99` | Set `PUID` in `.env` |
| Process group | `PGID=100` | Set `PGID` in `.env` |
| SQLite storage | `cd211_data` volume at `/data` | Replace it with a host-local directory |
| Staging directory | `./downloads:/downloads` | Use the host directory shared by all four services |

The SQLite database must be on a host-local filesystem with POSIX locking. Do not put `/data` on NFS or SMB.

### Startup flags

| Flag | Default | Description |
|---|---|---|
| `--http-address` | `:8080` | HTTP listen address in `[host]:port` form |
| `--database-path` | `/data/cd211.sqlite` | Absolute SQLite database path |

The CD211 binary reads no environment variables. `PUID` and `PGID` are used only by the container entrypoint to drop process privileges.

### Application settings

The following settings are editable in the Web UI and apply immediately to new tasks:

- CloudDrive2 address, username, password, and TLS mode.
- 115 offline download root and shared staging root.
- Offline download timeout (`24h` by default).
- NAS copy timeout (`72h` by default).
- Local verification timeout (`10m` by default).
- Categories, administrator password, `cd211_api_` Automation Token, `qbt_` qBittorrent API key, and webhooks.

Durations use Go syntax such as `30m`, `24h`, or `72h`.

## Operating boundaries

- Run CD211 only on a trusted LAN. Do not expose its HTTP port directly to the public Internet.
- The Web UI and Sonarr or Radarr can share the `admin` credentials; qBittorrent `/api/v2` clients can instead use an independent `qbt_` key. The native `/api/v1` surface accepts only the separate `cd211_api_` Automation Token.
- Neither key expires, and neither is designed for public or multi-tenant deployments.
- CD211 does not download BitTorrent data, contact trackers, or seed. It only coordinates 115 offline downloads, NAS copies, and status reporting.
- Removing a task never deletes files in 115; deleting local files is an explicit choice in the task action.

## Health checks

```text
GET /healthz
GET /readyz
```

`/healthz` reports process liveness. `/readyz` succeeds only after first-run setup is complete and the local root is available. Until then, qBittorrent API requests return `503`.
