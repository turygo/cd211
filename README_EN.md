# CD211

English | [简体中文](README.md)

<p align="center">
  <img src="docs/assets/cd211-dashboard.png" alt="CD211 download dashboard">
</p>

## Bring 115 offline downloads to Sonarr and Radarr

CD211 connects to Sonarr and Radarr through the qBittorrent Web API and hands magnet links or `.torrent` files to CloudDrive2. It waits for the 115 offline download, copies the content to a shared NAS directory, verifies the local files, and only then reports the download as complete.

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

## Automation

### Native API

Generate the global API token on the **Settings** page, then call `/api/v1` with `Authorization: Bearer <token>`:

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/downloads` | Submit a magnet link or torrent file |
| `GET /api/v1/downloads/{hash}` | Read download status |
| `GET /api/v1/downloads/{hash}/wait` | Wait for completion, failure, cancellation, or deletion |
| `GET /api/v1/events` | Pull completed and failed download events |

The API accepts magnets as JSON and torrent files as multipart data. The event feed uses opaque cursors and at-least-once delivery; deduplicate events by ID.

The token is shown only when generated or rotated. Rotation immediately invalidates the previous token, and revocation disables the API. The native API does not expose retry, cancel, or delete endpoints; use the Web UI for those controls.

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
- Categories, administrator password, API token, and webhooks.

Durations use Go syntax such as `30m`, `24h`, or `72h`.

## Operating boundaries

- Run CD211 only on a trusted LAN. Do not expose its HTTP port directly to the public Internet.
- The Web UI and Sonarr or Radarr share the `admin` credentials; the native API uses a separate global token.
- The global API token does not expire and is not designed for public or multi-tenant deployments.
- CD211 does not download BitTorrent data, contact trackers, or seed. It only coordinates 115 offline downloads, NAS copies, and status reporting.
- Removing a task never deletes files in 115; deleting local files is an explicit choice in the task action.

## Health checks

```text
GET /healthz
GET /readyz
```

`/healthz` reports process liveness. `/readyz` succeeds only after first-run setup is complete and the local root is available. Until then, qBittorrent API requests return `503`.
