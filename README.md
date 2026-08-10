# CD211

[English](README_EN.md) | 简体中文

CD211 让 Sonarr 和 Radarr 能像使用 qBittorrent 一样，通过 CloudDrive2 使用 115 云下载。

Sonarr 或 Radarr 将磁力链接或 `.torrent` 文件发送给 CD211。CD211 发起 115 云下载，将完成后的内容复制到本地暂存目录，校验本地文件，然后将任务报告为已完成，供 Sonarr 或 Radarr 导入。

CD211 不是 BitTorrent 客户端，也不会做种。请仅在可信局域网中运行，不要将其 HTTP 端口暴露到公网。

## 功能

- 兼容 qBittorrent WebAPI 2.11，可接入 Sonarr 和 Radarr。
- 支持提交磁力链接和 `.torrent` 文件。
- 通过 CloudDrive2 执行 115 云下载和 NAS 文件复制。
- 仅在本地文件校验通过后才将任务报告为已完成。
- 可为不同分类配置独立的云端目录和本地暂存目录。
- 下载任务和设置在重启后仍会保留。
- Web 界面支持英文和简体中文。
- 支持下载筛选，并展示进度、文件列表、历史记录和错误详情。
- 支持开始、重试、取消、删除记录和删除本地文件。
- 无需重启 CD211 即可修改 CloudDrive2、路径、超时、分类和密码设置。
- 通过带签名的 Webhook 推送下载完成和失败事件，支持重试、死信和手动重新投递。
- 提供 `/healthz` 和 `/readyz` 端点用于容器健康检查。

删除下载任务不会删除其在 115 中的副本。

## 快速开始

### 1. 准备共享暂存目录

CloudDrive2、CD211、Sonarr 和 Radarr 必须将同一个宿主机目录挂载到相同的绝对路径。项目提供的 Compose 文件使用 `/downloads`：

```text
宿主机暂存目录 -> CloudDrive2 中的 /downloads
              -> CD211 中的 /downloads
              -> Sonarr 中的 /downloads
              -> Radarr 中的 /downloads
```

四个容器应使用同一个共享用户组。CloudDrive2 创建的文件必须允许组写入；使用其官方镜像时，请通过 `umask 0002` 启动。

### 2. 启动 CD211

```sh
mkdir -p cd211/downloads
cd cd211
curl -LO https://raw.githubusercontent.com/turygo/cd211/main/docker-compose.yml
docker compose up -d
```

默认 Compose 配置将 CD211 发布到 `8080` 端口，把 SQLite 数据存储在 `cd211_data` 卷中，并使用 `./downloads` 作为暂存目录。

如需让 CD211 以指定用户和用户组运行，请在启动前创建 `.env`：

```dotenv
PUID=99
PGID=100
```

CloudDrive2、Sonarr 和 Radarr 应使用相同的 `PGID`。

### 3. 完成首次运行设置

打开 `http://<cd211-host>:8080`。设置向导会要求填写：

1. 固定用户名 `admin` 的管理员密码。密码至少包含 8 个字符，系统没有默认密码。
2. CloudDrive2 的 gRPC 地址、用户名、密码和 TLS 模式。
3. 115 云下载根目录和共享暂存根目录。使用项目提供的 Compose 文件时，共享暂存根目录通常为 `/downloads`。
4. 云下载、复制和本地校验的超时时间。

完成向导后会进入**分类**页面。在这里，需要为 Sonarr 或 Radarr 的每个分类分别设置两个根目录下的子目录。

### 4. 注册分类

在 CD211 Web 界面中打开**分类**，注册 Sonarr 或 Radarr 使用的每个分类。

示例：

| 字段 | 电视剧示例 | 电影示例 |
|---|---|---|
| 名称 | `tv` | `movies` |
| 115 分类子目录 | `TV` | `Movies` |
| 共享暂存子目录 | `tv` | `movies` |
| 可用状态 | 已启用 | 已启用 |

CD211 会将每个子目录与对应的已配置根目录拼接，并预览完整路径。如果 115 根目录为 `/115open/云下载`，共享暂存根目录为 `/downloads`，以上示例会分别解析为 `/115open/云下载/TV` 和 `/downloads/tv`。分类名称必须与 Sonarr 和 Radarr 中配置的值一致。

### 5. 将 CD211 添加到 Sonarr 和 Radarr

在 Sonarr 或 Radarr 中打开 **Settings → Download Clients → Add → qBittorrent**，然后填写：

| 字段 | 值 |
|---|---|
| Host | Sonarr/Radarr 能够访问 CD211 的主机名或 IP 地址 |
| Port | `8080` |
| Use SSL | 关闭；仅当你通过自己的反向代理提供 TLS 时开启 |
| Username | `admin` |
| Password | 首次设置时创建的管理员密码 |
| Category | 已在 CD211 中注册并启用的分类，例如 `tv` 或 `movies` |

运行 Sonarr/Radarr 连接测试，然后保存下载客户端。

## 配置

### Docker Compose

项目提供的 [`docker-compose.yml`](docker-compose.yml) 支持以下部署设置：

| 设置 | 默认值 | 修改方式 |
|---|---:|---|
| 对外 HTTP 端口 | `8080` | 修改 `ports` 的宿主机端口，例如 `8090:8080` |
| 进程用户 | `PUID=99` | 在 `.env` 中设置 `PUID` |
| 进程用户组 | `PGID=100` | 在 `.env` 中设置 `PGID`；所有需要访问暂存文件的服务应使用同一个用户组 |
| SQLite 存储 | 挂载到 `/data` 的命名卷 `cd211_data` | 如有需要，可将卷来源替换为宿主机本地目录 |
| 暂存存储 | 挂载到 `/downloads` 的 `./downloads` | 将 `./downloads` 替换为共享的宿主机暂存目录 |

SQLite 数据库路径必须位于支持 POSIX 锁的宿主机本地文件系统上。不要将 `/data` 放在 NFS 或 SMB 上。

`PUID` 和 `PGID` 由容器入口脚本处理，CD211 二进制文件本身不会读取环境变量。

### 启动参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--http-address` | `:8080` | `[host]:port` 格式的 HTTP 监听地址 |
| `--database-path` | `/data/cd211.sqlite` | SQLite 数据库的绝对路径 |

可通过 Compose 服务的 `command` 字段覆盖参数：

```yaml
services:
  cd211:
    command: ["cd211", "--http-address", ":8080", "--database-path", "/data/cd211.sqlite"]
```

### 应用设置

在首次运行设置期间，或之后通过 Web 界面的**设置**页面配置以下项目：

| 设置 | 默认值 | 说明 |
|---|---:|---|
| CloudDrive2 地址 | 无 | gRPC 地址，例如 `192.168.1.10:19798` |
| CloudDrive2 用户名 | 无 | CloudDrive2 账户用户名 |
| CloudDrive2 密码 | 无 | CloudDrive2 账户密码 |
| CloudDrive2 不安全连接 | 关闭 | CloudDrive2 提供无 TLS 的明文 gRPC 服务时启用 |
| 115 云下载根目录 | 无 | 已存在的 115 目录，各分类的下载目录将在其下创建 |
| 共享暂存根目录 | 无 | CloudDrive2、CD211、Sonarr 和 Radarr 以相同绝对路径共享的、已存在且可写的暂存目录；通常为 `/downloads` |
| 云下载超时 | `24h` | 115 云下载允许的最长时间 |
| 复制超时 | `72h` | CloudDrive2 复制允许的最长时间 |
| 校验超时 | `10m` | 本地文件校验允许的最长时间 |

超时时间使用 Go 时长格式，例如 `30m`、`24h` 或 `72h`。

保存应用设置时，CD211 会重新检查 CloudDrive2 连接和两个根目录。修改根目录会保留各分类的子目录，并为之后新提交的任务重新映射完整路径。已有下载任务继续使用其固定路径，CD211 不会移动其文件。

### 分类

每个分类包含：

| 设置 | 说明 |
|---|---|
| 名称 | Sonarr 或 Radarr 发送的值，例如 `tv` 或 `movies` |
| 115 分类子目录 | 已配置的 115 根目录下的相对云下载目标路径 |
| 共享暂存子目录 | 已配置的共享暂存根目录下的相对复制目标路径 |
| 可用状态 | 已禁用的分类会拒绝新的提交 |

请先在 CD211 Web 界面中配置分类，再在 Sonarr 或 Radarr 中使用。如果 Sonarr 或 Radarr 通过 qBittorrent API 创建了分类，请先在 CD211 中检查自动生成的路径，再开始使用。

### 凭据

- 用户名始终为 `admin`。
- 管理员密码在首次运行设置时创建，可以通过 Web 界面的**修改密码**页面更改。
- Web 界面和 Sonarr/Radarr 使用相同的凭据。
- 修改密码后，请同步更新 Sonarr 和 Radarr 中的 qBittorrent 下载客户端配置。

### Webhook

下载完成或失败时，CD211 可以向外部自动化服务或通知系统发送带签名的 Webhook 请求。Webhook 仅用于外部通知；Sonarr 和 Radarr 仍通过轮询和 Completed Download Handling 导入下载。

在 Web 界面的 **Webhook** 页面管理 Webhook 端点，在 **投递记录** 页面（`/webhook-deliveries`）查看投递历史。每个端点可以单独或同时订阅 `download.completed` 和 `download.failed` 事件，并支持创建、编辑、启用、停用、轮换签名密钥、删除、测试、筛选和重新投递死信。所有操作均受现有管理员会话和 CSRF 机制保护，不使用独立的角色或 API 凭据。

端点配置项：

- **名称**：用于区分端点的任意名称。
- **接收 URL**：必须是绝对 HTTP 或 HTTPS URL，且不能包含用户信息（userinfo）或片段标识符（fragment）。查询参数可以使用，但 Web 界面会隐藏其原始内容。CD211 不跟随重定向。
- **可选 Bearer 令牌**：通过 `Authorization: Bearer <token>` 请求头发送；编辑时留空可保留原值，也可通过专用控件清除。

每次投递均为 JSON 格式的 POST 请求，并携带 `X-CD211-Event`、`X-CD211-Event-ID`、`X-CD211-Timestamp`（Unix 秒）、`X-CD211-Signature` 以及可选的 `Authorization: Bearer <token>` 请求头。签名为 `v1=` 加上小写十六进制的 HMAC-SHA256(secret, `<timestamp>.<raw-body>`)。接收方必须先使用原始请求体验证签名，再解析请求体，并根据事件 ID 去重。事件封装格式为 `{id, type, schema_version, occurred_at, data}`；失败事件的错误信息已脱敏，不含提交 URI、Tracker 凭据、签名密钥或 Bearer 令牌。

安全要点：

- 签名密钥在创建或轮换时生成，仅通过带 `Cache-Control: no-store` 的响应显示一次，之后无法在 Web 界面中找回；Bearer 令牌也不会再次显示。
- URL、密钥和请求体不会写入日志。
- 仅配置可信的接收方 URL。CD211 有意允许访问私网或局域网地址，因此不提供用于限制目标地址的 SSRF 白名单。

重试与重新投递：

- 只有 2xx 响应视为成功；其他响应会按指数退避策略重试，退避间隔设有上限。重试期最长为 24 小时，之后进入死信状态。
- 死信投递可在 **投递记录** 页面手动重新投递（仅限已启用且未删除的端点）；重新投递时会沿用原事件 ID 和载荷，重新开始最长 24 小时的重试期，且不会创建重复的投递记录。
- 接收方必须根据事件 ID 做幂等处理。

`webhook.test` 事件只发送给所选端点，用于测试。

### 健康检查

```text
GET /healthz
GET /readyz
```

首次运行设置完成且本地根目录可用后，`/readyz` 才会返回成功。在设置完成前，qBittorrent API 请求会返回 `503`。
