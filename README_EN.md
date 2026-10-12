# CD211

English | [简体中文](README.md)

Use **115 offline downloads** for **instant results when a resource is already cached in the cloud**. CD211 replaces your qBittorrent download client, with CloudDrive2 copying the files to your NAS. Manage progress, retries, and settings in the Web UI.

<p align="center">
  <img src="docs/assets/cd211-dashboard.png" alt="CD211 download dashboard">
</p>

## Prerequisites

- CloudDrive2 with 115 mounted and gRPC access enabled.
- Docker.
- One NAS staging directory shared by CloudDrive2, CD211, and tools that need to read the downloaded files, mounted at the **same path** in each container, such as `/downloads`.

## Quick start

### 1. Start CD211

```sh
docker run -d --name cd211 --restart unless-stopped -p 8080:8080 -e PUID=99 -e PGID=100 -v cd211_data:/data -v "$PWD/downloads:/downloads" turygo/cd211:latest
```

CloudDrive2 must use the same `PGID` and start with `umask 0002` so all services can manage the staging files.

### 2. Complete setup

Open `http://<cd211-host>:8080` and follow the wizard to set the `admin` password, CloudDrive2 connection, 115 download directory, and staging directory. There is no default password.

Then create categories, such as `tv` for series and `movies` for films, and choose cloud and staging subdirectories for each.

### 3. Configure your download client

Add a download client in your tool, select **qBittorrent** as the type, and enter the CD211 connection details:

| Field | Value |
|---|---|
| Host | CD211 hostname or IP |
| Port | `8080` |
| Use SSL | Off (enable when using an HTTPS reverse proxy) |
| Username | `admin` |
| Password | The password you just set |
| Category | An enabled CD211 category, such as `tv` or `movies` |

Run the connection test and save. You can now submit downloads.

Tools that support API key authentication can also use a qBittorrent API key generated in CD211 Settings.

[Changelog](CHANGELOG.md)
