---
title: 配置
description: mistgate serve 和 mistgate setup 的所有标志、对应的环境变量和默认值、其他可配置命令、节点 Agent 的标志，以及数据目录布局。
---

面板没有配置文件。`mistgate setup` 会将安装地址保存到数据库中一次；其他所有设置都通过 `mistgate serve` 的标志指定，并且每个标志都有对应的 `MISTGATE_*` 环境变量。本页列出这些内容、节点 Agent 的标志，以及数据目录中的文件。命令说明参见 [CLI](cli.md)。

## 标志与环境变量如何共同生效

- 命令行标志优先于对应的环境变量。
- 环境变量为空字符串时视为未设置，因此会使用默认值。例外为 `MISTGATE_SOURCE_URL`（设为空会隐藏源代码链接）和 `MISTGATE_ACME_HTTP`（设为空会关闭端口 80 的监听器）。
- 布尔变量（`MISTGATE_DEV`）接受任意大小写形式的 `1`、`true`、`yes` 或 `on`。
- 可重复指定的标志 `--acme-domain` 和 `--trusted-proxy` 也接受逗号分隔的值。对应的环境变量是逗号分隔的列表；仅当完全未指定标志时才会读取这些变量。

## `mistgate serve`

| 标志 | 环境变量 | 默认值 | 含义 |
|:--|:--|:--|:--|
| `--listen` | `MISTGATE_LISTEN` | `127.0.0.1:8080` | 公共监听器地址：伪装站点、订阅和用户页面、使用前缀或主机名模式的管理后台，以及（启用 TLS 时的）节点 Agent 接入端点。真实服务器上使用 `:443`。 |
| `--tls-cert` | `MISTGATE_TLS_CERT` | none | 公共监听器使用的 PEM 证书（含证书链）。文件变更后会重新读取，最多每 30 秒检查一次；若证书和密钥加载失败，会记录错误并继续使用旧证书。需要同时指定 `--tls-key`。 |
| `--tls-key` | `MISTGATE_TLS_KEY` | none | `--tls-cert` 对应的私钥。两者必须同时使用。 |
| `--acme-domain` | `MISTGATE_ACME_DOMAIN` | none | 为此主机名获取 Let’s Encrypt 证书。可重复指定。仅接受普通主机名，不接受通配符。通过公共监听器使用 TLS-ALPN-01 验证，因此该监听器必须能从端口 443 访问。使用此选项即表示接受 CA 的服务条款。若同时指定 `--tls-cert`，这些主机名使用 Let’s Encrypt 证书，其他名称使用静态证书。 |
| `--acme-email` | `MISTGATE_ACME_EMAIL` | none | Let’s Encrypt 的联系邮箱。可选。 |
| `--acme-http` | `MISTGATE_ACME_HTTP` | `:80` | 仅配合 `--acme-domain` 使用，用于 ACME HTTP-01 验证和从 HTTP 重定向到 HTTPS。其他主机名和未知路径会得到伪装站点的 404。传入空值 `--acme-http=` 或将 `MISTGATE_ACME_HTTP=` 设为空，可关闭此监听器。 |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | the address setup stored | 为管理后台单独提供普通 HTTP 监听器。仅适用于通过 `setup --admin-listen` 设置的安装；若使用私密前缀或主机名，则会报错。此项会覆盖已存储的地址，但通行密钥仍绑定到 setup 保存的端口。 |
| `--agent-listen` | `MISTGATE_AGENT_LISTEN` | none (`127.0.0.1:8082` with `--dev`) | 为节点 Agent 接入端点单独提供 TLS 监听器。未设置时，Agent 会使用带私密 TLS 名称的公共监听器，因此公共监听器必须启用 TLS。 |
| `--agent-addr` | `MISTGATE_AGENT_ADDR` | derived | Agent 要连接的 `host:port`，会写入新安装命令。未设置时：若 `--agent-listen` 指定了具体主机（非空、不是 `0.0.0.0` 或 `[::]`），则使用该值；否则使用公共 URL 的主机和端口（URL 未指定端口时使用 443）。已注册的节点会保留注册时使用的地址。 |
| `--trusted-proxy` | `MISTGATE_TRUSTED_PROXY` | none | 可信反向代理的 CIDR 或 IP；其 `X-Forwarded-For` 和 `Forwarded` 请求头会被采信。可重复指定。未设置时，无论请求头内容如何，TCP 对端都被视为客户端。 |
| `--decoy-dir` | `MISTGATE_DECOY_DIR` | the built-in page | 自有静态伪装站点的目录（`index.html`，可选的 `404.html`、`429.html`、`robots.txt`）。 |
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | 数据目录。目录必须已存在（先运行 `mistgate setup`）；每次启动 `serve` 时都会将其权限模式设为 0700。 |
| `--update-service` | `MISTGATE_UPDATE_SERVICE` | `mistgate.service` (none with `--dev`) | 面板自身的 systemd 单元；替换二进制文件前后，面板更新程序会停止并重新启动它。面板以 root 运行时，此值会决定更新程序以哪个名称启动临时辅助单元。面板以其他用户身份运行时，则会启动固定的 root 辅助单元 `mistgate-panel-update.service`，其命令行会指定单元和数据目录本身（若与你的配置不同，请编辑该单元）；参见[更新](../operations/updates.md)。使用 `--dev` 时，面板不会自更新。 |
| `--source-url` | `MISTGATE_SOURCE_URL` | `https://github.com/Mistgate/mistgate` | 此构建源代码的发布地址，会以「源代码」链接显示在管理后台的版本旁（AGPL-3.0 第 13 节）。派生版本（fork）应指向自己的仓库；设为空可隐藏链接。 |
| `--dev` | `MISTGATE_DEV` | off | 开发模式：数据位于 `./.data`（会同时创建主密钥）；使用普通 HTTP，伪装站点位于 `--listen`（`127.0.0.1:8080`），管理后台位于 `127.0.0.1:8081`，Agent 接入端点位于 `127.0.0.1:8082`；WebAuthn 使用 `localhost`；尚未创建管理员时，启动时会显示设置链接。地址信息不会保存。切勿在公共服务器上使用。 |

未指定 `--tls-cert` 和 `--acme-domain` 时，公共监听器使用普通 HTTP。只有在 TLS 由反向代理终止，并同时配置 `--trusted-proxy` 和 `--agent-listen` 时，这样做才有意义；参见[安装面板](../getting-started/install-panel.md)中的「位于反向代理之后」。

以下内置限制不是标志：每个客户端（一个 IPv4 地址或一个 IPv6 /64）在公共端每秒最多请求 10 次，突发上限为 60；在管理后台每秒最多请求 30 次，突发上限为 200；在 Agent 接入端点每秒最多请求 5 次，突发上限为 30。超出限制时会收到伪装站点样式的 429 页面。

## `mistgate setup`

每个安装只需运行一次 setup。首次运行时会保存以下地址；之后再次运行会保留这些地址、显示管理后台 URL，并在尚未创建管理员时生成新的一次性设置链接。

| 标志 | 环境变量 | 默认值 | 含义 |
|:--|:--|:--|:--|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` | 创建数据目录、主密钥和数据库的位置。 |
| `--public-url` | `MISTGATE_PUBLIC_URL` | none | 公共（伪装）站点的 URL，格式为 `http(s)://host[:port]`，例如 `https://panel.example.com`。这是订阅链接和 Agent 地址的基础地址。除非指定 `--admin-host` 或 `--admin-listen`，否则必须设置；始终建议设置。 |
| `--admin-host` | `MISTGATE_ADMIN_HOST` | none | 使用此私密主机名提供管理后台，而不是使用私密路径前缀。 |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | none | 使用单独的监听器提供管理后台，例如 `127.0.0.1:8081`。不能与 `--admin-host` 同时使用。 |
| `--rp-id` | `MISTGATE_RP_ID` | derived | WebAuthn 依赖方 ID。 |
| `--rp-origins` | `MISTGATE_RP_ORIGINS` | derived | WebAuthn 和管理后台状态变更请求允许使用的浏览器来源，使用逗号分隔。 |

setup 在各种模式下生成的值：

| 模式 | 管理后台 URL | WebAuthn RP ID | 允许的来源 |
|:--|:--|:--|:--|
| 私密前缀（未指定 `--admin-host` 或 `--admin-listen`） | `<public-url>/<24 random characters>/` | 公共 URL 的主机名 | 公共 URL 的协议、主机名和端口 |
| `--admin-host` | `https://<admin host>/`（若提供公共 URL，则使用其中的协议和端口） | 管理后台主机名 | 管理后台 URL 的来源 |
| `--admin-listen` | `http://localhost:<port>/` | `localhost` | `http://localhost:<port>` |

所有模式下，setup 还会生成两个随机私密值：Agent 接入端点的私密 TLS 名称（16 个随机字符，作为公共 URL 主机名下的标签；若没有公共 URL，则位于管理后台主机名下；如果面板没有域名，则位于 `com` 下；它不需要 DNS 记录），以及订阅链接的私密路径前缀（24 个随机字符）。这些值都会存储在数据库中。之后没有命令可以更改这些值。

> **警告：** `setup` 和 `serve` 都会读取 `MISTGATE_ADMIN_LISTEN`。仅当安装使用单独的管理后台监听器时才设置此项。

## 面板的其他命令

**`mistgate auth turnstile off`** 和 **`mistgate auth reset-login`** 在面板服务器上运行，面板启动或停止时都可以执行：

| 标志 | 环境变量 | 默认值 | 含义 |
|:--|:--|:--|:--|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | 面板数据目录。目录中没有数据库时，命令会拒绝执行。 |
| `--dev` | `MISTGATE_DEV` | off | 使用开发数据目录 `./.data`。 |
| `--admin` | none | none | 仅供 `reset-login` 使用：当该管理员只使用通行密钥且面板上有多个管理员时，指定要添加密码登录方式的管理员。 |
| `--qr-invert` | none | off | 仅供 `reset-login` 使用：为浅色终端背景绘制二维码。 |

**`mistgate mcp`**，供 MCP 客户端使用的 stdio 代理：

| 标志 | 环境变量 | 默认值 | 含义 |
|:--|:--|:--|:--|
| `--url` | `MISTGATE_URL` | none | setup 显示的管理后台 URL，例如 `https://panel.example.com/<secret>/`。除非主机为 localhost，否则会拒绝普通 `http`。 |
| `--token-file` | `MISTGATE_TOKEN_FILE` | none | API 令牌（位于第一行）所在文件的路径。绝不会从命令行或环境变量读取令牌。 |

**`mistgate backup keygen`** 和 **`mistgate backup restore`** 用于[加密备份](../operations/backups.md)的离线操作，只接收标志，所有标志都是必需的，且没有环境变量：

| 标志 | 含义 |
|:--|:--|
| `keygen --identity-file` | 创建私有 age 恢复身份的位置（权限模式为 0600，不会覆盖已有文件）。 |
| `restore --identity-file` | 私有身份文件。 |
| `restore --file` | 下载的加密备份（`.tar.gz.age`）。 |
| `restore --data-dir` | 尚不存在的新目录。不会读取 `MISTGATE_DATA_DIR`。 |

**`mistgate release keygen`**、**`mistgate release build`** 和 **`mistgate release sign`** 只接收标志，不使用环境变量（`release trust-key` 还会读取 `MISTGATE_DATA_DIR` 和 `MISTGATE_DEV`，并接收 `--data-dir` 和 `--dev`）：参见 [CLI](cli.md) 和[发布版本和签名](../operations/releases.md)。

隐藏命令 **`mistgate panel-update-helper`** 接收 `--fetch-latest`（必需）、`--data-dir` 和 `--service`。只有面板更新程序会以 root 身份启动它；固定单元 `mistgate-panel-update.service` 使用 `--data-dir /var/lib/mistgate --service mistgate.service` 运行。

## 其他环境变量

| 变量 | 读取方 | 含义 |
|:--|:--|:--|
| `CREDENTIALS_DIRECTORY` | 面板 | 由 systemd 的 `LoadCredential=` 设置。此目录中有 `master.key` 时，会使用它，而不是 `<data-dir>/master.key`。注意，从 shell 运行的 `mistgate auth reset-login` 不会读取此目录。 |
| `MISTGATE_HEALTH_BLIP_WINDOW` | `mistgate serve` | Go 时长，用于缩短运行状况模块的闪断时间窗口。供端到端测试使用；请勿设置。 |
| `MISTGATE_UNIT_GEN` | `mistgate-node run` | systemd 单元的版本，由 `mistgate-node install` 写入单元。不要自行设置。 |
| `GOMEMLIMIT` | `mistgate-node run` | Go 内存限制，由 `install` 写入单元（约为 RAM 的 60%）。 |
| `MISTGATE_PANEL` | Vite 开发服务器 | 仅供开发使用：`pnpm dev` 代理管理 API 的目标地址（默认 `http://127.0.0.1:8081`）。与下文 Agent 使用的 `MISTGATE_PANEL` 不同。 |

## 节点 Agent：`mistgate-node`

| 命令和标志 | 环境变量 | 默认值 | 含义 |
|:--|:--|:--|:--|
| `enroll --panel` | `MISTGATE_PANEL` | none | 面板地址，格式为 `host:port`。 |
| `enroll --sni` | `MISTGATE_AGENT_SNI` | none | 面板为 Agent 设置的私密 TLS 名称。 |
| `enroll --ca-sha256` | `MISTGATE_CA_SHA256` | none | 面板 CA 证书的 SHA-256 指纹，共 64 位十六进制数字（冒号和大小写会被忽略）。 |
| `enroll --token` | `MISTGATE_ENROLL_TOKEN` | none | 一次性注册令牌。使用环境变量可避免令牌出现在进程列表中。 |
| `enroll --token-stdin` | none | off | 从 stdin 读取令牌（单行，最多 256 字节）。不能与 `--token` 或 `MISTGATE_ENROLL_TOKEN` 同时使用。 |
| `enroll --resume-key` | none | off | 将新密钥保存在 `enroll-pending.pem` 中，直到注册成功；重试时继续使用该密钥。SSH 安装会使用此项。 |
| `enroll --force` | none | off | 替换已有身份。 |
| `enroll`, `install`, `run --state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | Agent 状态目录。三个命令都使用同一目录。 |
| `run --link-url` | `MISTGATE_LINK_URL` | empty | 已签名 WebSocket 链接的可选基础地址，格式为 `wss://host/<secret-prefix>`。面板声明支持时，Agent 会使用此链接；否则使用 mTLS。每个面板都会提供并声明此链接；只有设置了此值的 Agent 才会使用。如果无法建立链接，Agent 会回退到 mTLS，并在 10 分钟后再次尝试该链接。除非配置此值，否则现有节点会继续使用 mTLS。安装命令不会设置此项。 |
| `install --bin` | none | `/usr/local/bin/mistgate-node` | 二进制文件的存放位置；当前运行的可执行文件位于其他位置时会复制到此处。Agent 会获得对此目录的写入权限（用于自更新）。 |
| `install --no-start` | none | off | 写入并启用单元，但不启动它。 |
| `run --log-level` | `MISTGATE_LOG_LEVEL` | `info` | 可选值为 `debug`、`info`、`warn` 或 `error`。 |
| `run --log-format` | `MISTGATE_LOG_FORMAT` | `text` | 可选值为 `text` 或 `json`。 |
| `awg prepare-kernel --yes` | none | off | 执行计划。不带此项时只显示计划。 |
| `awg prepare-kernel --verify-only` | none | off | 只检查已安装的模块。 |
| `awg prepare-kernel --status-file` | none | none | 报告进度的位置；由 Agent 使用。 |
| `awg prepare-kernel --timeout` | none | `45m` | 本次运行的最长时限。 |

`cleanup-net` 和 `version` 不接收标志。节点的其他所有设置（地址、国家、DNS、超时、AmneziaWG 后端、WARP）都在管理后台中配置，并通过连接发送给 Agent。

## 面板的数据目录

默认目录为 `/var/lib/mistgate`。目录权限为 0700；面板创建的所有文件仅允许其所有者读取。

| 路径 | 内容 | 是否含密钥 |
|:--|:--|:--|
| `mistgate.db` | SQLite 数据库：包含设置和地址、管理员与会话、节点及其证书、节点已保存的 SSH 凭据、协议配置、分组、用户、设备、凭据、流量、事件、告警、滚动更新和更新计划、备份设置、API 令牌及审计日志。数据库中的密钥（面板 CA 密钥、身份验证器密钥、订阅令牌、设备密钥、WARP 密钥、SSH 密码、备份存储桶的机密密钥等）使用主密钥加密；密码存储为 argon2id 哈希，API 和注册令牌存储为 SHA-256 哈希。 | 是 |
| `mistgate.db-wal`, `mistgate.db-shm` | SQLite 写前日志及其索引。它们属于数据库的一部分：复制时应一并复制，或者先停止面板。 | 是 |
| `master.key` | 32 个随机字节，权限模式为 0600。用于加密所有已存储的密钥（XChaCha20-Poly1305），并派生从不存储的用户页面密码。如果组用户或其他用户可以读取此文件，面板会拒绝启动。 | 最敏感的文件 |
| `release.pub` | 此安装的发布公钥（ed25519）；用于验证 `dist/` 中的节点发布包。首次使用密钥构建面板时会写入此文件；后续使用同一密钥的构建会继续使用它；不带密钥的构建也会继续使用它；若构建中内置了其他密钥，则在 `mistgate release trust-key` 替换此文件前不会信任任何发布包。面板发布版本只使用编译在二进制文件中的密钥验证，绝不使用此文件。 | 否 |
| `dist/` | 节点更新用的签名发布包：`manifest.json`、`manifest.sig` 和 `mistgate-node-linux-amd64` / `-arm64`。你可以将其复制到此处，面板也会自动放入：每 10 分钟检查最新的稳定版 GitHub Release，并安装通过 `release.pub` 验证的较新发布包（滚动更新期间除外）。面板每分钟重新扫描此目录。 | 否 |
| `panel-update.request` | 所有者确认的面板发布版本（版本号和 SHA-256），留给更新辅助程序处理，之后由辅助程序删除。 | 否 |
| `panel-update.new` | 更新辅助程序下载的新面板二进制文件，并根据已签名的面板清单验证；更新成功后会删除。 | 否 |
| `.mistgate-backup-*/` | 正在运行的备份的工作目录：包含数据库快照和加密归档。备份结束时会删除（若崩溃后残留，则在下次备份时删除），绝不会纳入备份。 | 是 |
| `acme/` | 仅在使用 `--acme-domain` 时存储 Let’s Encrypt 账户密钥和证书。丢失后会自动重新生成。 | 是 |

面板更新程序还会在数据目录和二进制文件旁写入备份：`<data-dir>.panel-update-backup.tar.gz`（例如 `/var/lib/mistgate.panel-update-backup.tar.gz`，权限模式为 0600）是完整的数据目录，在替换二进制文件前、面板停止时创建。更新失败时会恢复该备份，并且只保留最新一份。此归档包含 `master.key` 和数据库，因此应按保护数据目录的方式保护它。之前的二进制文件会保存为 `<binary>.prev`（例如 `/usr/local/bin/mistgate.prev`），回滚的二进制文件会保存为 `<binary>.failed`。

每当 `serve`、`setup`、`auth` 命令或 `backup restore` 打开数据库时，都会自动向前应用数据库迁移。

> **危险：** 丢失 `master.key` 后，所有加密密钥都会无法读取，包括面板 CA 密钥和节点已保存的 SSH 密码；丢失整个目录则意味着必须重新注册所有节点。请按[安装面板](../getting-started/install-panel.md)第 9 步所述做好备份，并设置[加密备份](../operations/backups.md)。

## 节点的状态目录

默认目录为 `/var/lib/mistgate-node`，目录权限为 0700，文件权限为 0600。

| 路径 | 内容 |
|:--|:--|
| `identity.pem` | 节点的私钥和面板 CA 签发的证书，保存在同一文件中。文件存在表示节点「已注册」。 |
| `enroll-pending.pem` | 使用 `--resume-key` 发起注册（SSH 安装）时生成的私钥，只保留到注册成功为止。 |
| `ca.pem` | 面板 CA：Agent 唯一信任的证书颁发机构。 |
| `agent.json` | 面板地址、私密 TLS 名称和节点 ID。 |
| `state.json` | 最近应用的状态，因此即使面板不可用，服务器也会在节点重启后恢复运行。文件中包含协议配置密钥（混淆密码、服务器密钥、WARP 密钥）和节点验证用户所需的内容（Hysteria2 令牌哈希、AmneziaWG 公钥和预共享密钥），绝不包含客户端私钥或原始令牌。 |
| `certs/` | 节点上服务器使用的证书：Let’s Encrypt 状态和自签名证书。 |
| `last_version` | 上次启动的版本。 |
| `update.*`、`awg-prepare.json` | 自更新进行中的标记和内核模块构建状态；仅在相关操作期间存在。 |














