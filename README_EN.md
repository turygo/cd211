# CD211

English | [简体中文](README.md)

CD211 lets Sonarr and Radarr use 115 offline downloads through CloudDrive2 as if they were using qBittorrent.

Sonarr or Radarr sends a magnet link or `.torrent` file to CD211. CD211 starts the 115 offline download, copies the finished content to a local staging directory, verifies the local files, and then reports the download as complete so Sonarr or Radarr can import it.

CD211 is not a BitTorrent client and does not seed. Run it on a trusted LAN; do not expose its HTTP port to the public Internet.

## Features

- qBittorrent WebAPI 2.11-compatible integration for Sonarr and Radarr.
- Magnet link and `.torrent` file submissions.
- 115 offline downloads and NAS copies through CloudDrive2.
- Local file verification before a download is reported as complete.
- Category-specific cloud folders and local staging directories.
- Persistent downloads and settings across restarts.
- English and Simplified Chinese Web UI.
- Download filtering, progress, file lists, history, and error details.
- Start, retry, cancel, remove-record, and remove-local-files actions.
- Editable CloudDrive2, path, timeout, category, and password settings without restarting CD211.
- Signed webhook notifications for completed and failed downloads, with retry, dead-letter, and manual replay.
- Native automation API with a single global API token: magnet/torrent submission, status queries, terminal wait, and completed/failed event pull.
- `/healthz` and `/readyz` endpoints for container health checks.

Removing a download never deletes its copy in 115.

## Quick start

### 1. Prepare the shared staging directory

CloudDrive2, CD211, Sonarr, and Radarr must mount the same host directory at the same absolute path. The supplied Compose file uses `/downloads`:

```text
Host staging directory -> /downloads in CloudDrive2
                       -> /downloads in CD211
                       -> /downloads in Sonarr
                       -> /downloads in Radarr
```

Use a shared group for all four containers. CloudDrive2 must create files with group write permission; when using its official image, start it with `umask 0002`.

### 2. Start CD211

```sh
mkdir -p cd211/downloads
cd cd211
curl -LO https://raw.githubusercontent.com/turygo/cd211/main/docker-compose.yml
docker compose up -d
```

The default Compose configuration publishes CD211 on port `8080`, stores SQLite data in the `cd211_data` volume, and uses `./downloads` as the staging directory.

If CD211 should run as a specific user and group, create `.env` before starting it:

```dotenv
PUID=99
PGID=100
```

Use the same `PGID` for CloudDrive2, Sonarr, and Radarr.

### 3. Complete first-run setup

Open `http://<cd211-host>:8080`. The setup wizard asks for:

1. An operator password for the fixed username `admin`. The password must contain at least 8 characters. There is no default password.
2. The CloudDrive2 gRPC address, username, password, and TLS mode.
3. A 115 offline download root and a shared staging root. With the supplied Compose file, the shared staging root is normally `/downloads`.
4. Offline, copy, and local verification timeouts.

Finishing the wizard opens **Categories**, where each Sonarr or Radarr category receives a child folder below both roots.

### 4. Register categories

Open **Categories** in the CD211 Web UI and register each category used by Sonarr or Radarr.

Example:

| Field | TV example | Movie example |
|---|---|---|
| Name | `tv` | `movies` |
| 115 category subfolder | `TV` | `Movies` |
| Shared staging subfolder | `tv` | `movies` |
| Availability | Enabled | Enabled |

CD211 combines each subfolder with its configured root and previews the full path. With a 115 root of `/115open/云下载` and a shared staging root of `/downloads`, the examples resolve to `/115open/云下载/TV` and `/downloads/tv`. Category names must match the values configured in Sonarr and Radarr.

### 5. Add CD211 to Sonarr and Radarr

In Sonarr or Radarr, open **Settings → Download Clients → Add → qBittorrent** and enter:

| Field | Value |
|---|---|
| Host | A hostname or IP address from which Sonarr/Radarr can reach CD211 |
| Port | `8080` |
| Use SSL | Off, unless TLS is provided by your own reverse proxy |
| Username | `admin` |
| Password | The operator password created during setup |
| Category | A category registered and enabled in CD211, such as `tv` or `movies` |

Run the Sonarr/Radarr connection test, then save the download client.

## Configuration

### Docker Compose

The supplied [`docker-compose.yml`](docker-compose.yml) supports these deployment settings:

| Setting | Default | How to change it |
|---|---:|---|
| Published HTTP port | `8080` | Change the host side of `ports`, for example `8090:8080` |
| Process user | `PUID=99` | Set `PUID` in `.env` |
| Process group | `PGID=100` | Set `PGID` in `.env`; use the same group for all services that access staging files |
| SQLite storage | Named volume `cd211_data` mounted at `/data` | Replace the volume source with a host-local directory if required |
| Staging storage | `./downloads` mounted at `/downloads` | Replace `./downloads` with the shared host staging directory |

The SQLite path must be on a host-local filesystem with POSIX locking. Do not put `/data` on NFS or SMB.

`PUID` and `PGID` are handled by the container entrypoint. The CD211 binary itself does not read environment variables.

### Startup flags

| Flag | Default | Description |
|---|---|---|
| `--http-address` | `:8080` | HTTP listen address in `[host]:port` format |
| `--database-path` | `/data/cd211.sqlite` | Absolute path to the SQLite database |

Override flags with the Compose service `command` field:

```yaml
services:
  cd211:
    command: ["cd211", "--http-address", ":8080", "--database-path", "/data/cd211.sqlite"]
```

### Application settings

Configure these values during first-run setup or later from **Settings** in the Web UI:

| Setting | Default | Description |
|---|---:|---|
| CloudDrive2 address | None | gRPC address such as `192.168.1.10:19798` |
| CloudDrive2 username | None | CloudDrive2 account username |
| CloudDrive2 password | None | CloudDrive2 account password |
| Insecure CloudDrive2 connection | Off | Enable when CloudDrive2 serves plaintext gRPC without TLS |
| 115 offline download root | None | Existing 115 directory below which category download folders are created |
| Shared staging root | None | Existing writable staging directory shared at the same absolute path by CloudDrive2, CD211, Sonarr, and Radarr; normally `/downloads` |
| Offline timeout | `24h` | Maximum time allowed for a 115 offline download |
| Copy timeout | `72h` | Maximum time allowed for a CloudDrive2 copy |
| Verify timeout | `10m` | Maximum time allowed for local file verification |

Timeouts use Go duration notation, for example `30m`, `24h`, or `72h`.

Saving application settings rechecks the CloudDrive2 connection and both root directories. Changing a root preserves every category subfolder and remaps its full path for future submissions. Existing downloads keep their frozen paths, and CD211 never moves their files.

### Categories

Each category contains:

| Setting | Description |
|---|---|
| Name | The value sent by Sonarr or Radarr, such as `tv` or `movies` |
| 115 category subfolder | The relative offline-download destination below the configured 115 root |
| Shared staging subfolder | The relative copy destination below the configured shared staging root |
| Availability | Disabled categories reject new submissions |

Configure categories in the CD211 Web UI before using them in Sonarr or Radarr. If Sonarr or Radarr creates a category through the qBittorrent API, review its generated paths in CD211 before using it.

### Credentials

- The username is always `admin`.
- The operator password is created during first-run setup and can be changed from **Change password** in the Web UI.
- The same credentials are used by the Web UI and Sonarr/Radarr.
- After changing the password, update the qBittorrent download client entry in Sonarr and Radarr.
- The automation API uses its own global API token, separate from the admin password (see Automation API below): the system generates it and shows it once, rotation invalidates the old token immediately, revocation disables the API, and the token never expires.

### Automation API

CD211 also exposes a native automation API (`/api/v1`), separate from the qBittorrent-compatible surface, for submitting downloads, querying status, waiting for a terminal state, and pulling completed/failed events. Every request must carry an `Authorization: Bearer <token>` header; only Bearer authentication is accepted, and the Web UI's SID sessions and admin password do not apply.

**Settings** in the Web UI manages the single global API token:

- The system generates **one** global token, which can be generated, rotated, or revoked at any time. A generated or rotated token is shown exactly once from a `Cache-Control: no-store` page and can never be recovered afterwards.
- Rotation invalidates the old token immediately; revocation disables the API. The token **does not expire**.
- SQLite stores only the token's SHA-256 digest and a trailing hint, never the original token.
- A missing or invalid token returns `401`; `/api/v1` returns `503` until first-run setup is complete.

Endpoints:

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/downloads` | Submit a download with JSON `{"magnet":"...","category":"movies","stopped":false}`, or a multipart form with the `torrent` file field and optional `category`/`stopped` fields |
| `GET /api/v1/downloads/{hash}` | Query download status (40-character hexadecimal hash, normalized to lowercase) |
| `GET /api/v1/downloads/{hash}/wait?timeout=1s..25s` | Wait for a terminal state; `204` on timeout |
| `GET /api/v1/events` | Pull `download.completed`/`download.failed` events with `cursor`/`types`/`hash`/`limit`/`wait` parameters |

A submission answers `{"created","download"}`: `201` with a `Location` header for a new or revived row, `200` for an existing active download (no field is modified). The download model carries `hash`/`name`/`category`/`state`/`progress`/`version`/`terminal`/`outcome`/`content_path`/`total_size`/`error`/`created_at`/`updated_at`/`completed_at`/`links`. Terminal outcomes are `completed`/`failed`/`cancelled`/`deleted`; `STOPPED` and all in-progress states are non-terminal.

`wait` defaults to `timeout=25s` and accepts `1s` through `25s`. Once the download is terminal it answers `200` with the full model; on timeout it answers `204` with an empty body and the `X-CD211-Download-Version` response header.

Events answer `{"items":[{"cursor","event"}],"next_cursor","has_more"}`. `cursor` is an opaque, versioned base64url cursor; clients persist only the string. Omitting it starts at the oldest retained event; `latest` starts after the current high-water. `types` filters `download.completed` and `download.failed`, while `hash` selects one download; `limit` defaults to `100` and is capped at `500`, and `wait` accepts `0s` through `25s`. Pagination advances over the monotonic event sequence and skips filtered events. Delivery is at-least-once; clients must deduplicate by event ID, and error information in failed events is sanitized, including paths frozen at submission time.

The native API provides only the submit, query, wait, and event-pull operations above — there are **no** control endpoints for retry, cancel, delete, or category mutation. Like the webhook channel, it is for trusted-LAN use: it shares the single-token deployment boundary and offers no abuse resistance for public or multi-tenant exposure.

### Webhooks

When a download completes or fails, CD211 can notify external automation or notification systems with signed webhooks. Webhooks are for external notifications only; Sonarr and Radarr still import downloads through polling and Completed Download Handling.

Manage named endpoints on the **Webhooks** page and delivery history on the **Delivery history** page (`/webhook-deliveries`). Each endpoint subscribes independently to `download.completed` and/or `download.failed`, with create, edit, enable/disable, HMAC secret rotation, delete, test, filters, and dead-letter replay. All actions use the existing authenticated admin session and CSRF protections; there is no separate role or API credential.

Configuring an endpoint requires:

- **Name** — any identifier.
- **Receiver URL** — an absolute HTTP/HTTPS URL without userinfo or fragment. Query parameters are allowed and delivered to the receiver, but the Web UI hides their original contents. CD211 does not follow redirects.
- **Optional bearer token** — sent as `Authorization: Bearer <token>`; leaving it blank on edit preserves the stored value, and a dedicated control clears it.

Every request is a JSON POST carrying `X-CD211-Event`, `X-CD211-Event-ID`, `X-CD211-Timestamp` (Unix seconds), `X-CD211-Signature`, and an optional `Authorization: Bearer <token>` header. The signature is `v1=` plus lowercase hex HMAC-SHA256(secret, `<timestamp>.<raw-body>`). Receivers must verify the signature against the exact raw body before parsing and deduplicate by event ID. The event envelope is `{id, type, schema_version, occurred_at, data}`; error information in failed events is sanitized — submission URIs, tracker passkeys, endpoint secrets, and bearer tokens are never included.

Security:

- The signing secret is generated on create and rotation, shown once from a no-store response, and cannot be recovered through the UI; the bearer token is never redisplayed.
- URLs, secrets, and request bodies are never logged.
- Configure only trusted receiver URLs. CD211 intentionally supports private/LAN addresses and provides no public-webhook SSRF allowlist.

Retry and replay:

- Only a 2xx response counts as success; other responses retry with bounded exponential backoff for up to 24 hours, then move to dead-letter.
- Dead-letter deliveries can be replayed manually from the **Delivery history** page (only for enabled, non-deleted endpoints); replay reuses the original event ID and payload with a fresh 24-hour window and does not create a duplicate delivery.
- Consumers must stay idempotent by event ID.

`webhook.test` events are sent only to the selected endpoint for testing.

### Health checks

```text
GET /healthz
GET /readyz
```

`/readyz` becomes successful after first-run setup is complete and the local root is available. Until setup is complete, qBittorrent API requests return `503`.
