---
title: 命令行
description: mistgate 和 mistgate-node 的所有命令及其标志、环境变量、默认值和示例。
---

Mistgate 有两个二进制文件：`mistgate` 是面板，`mistgate-node` 是节点 Agent。两者都先接收命令，再接收标志。标志可以使用一个或两个连字符（`-data-dir` 或 `--data-dir`）；本页统一使用两个连字符。`<command> -h` 会显示该命令的标志。

许多标志可以回退到环境变量。命令行中给出的标志优先于环境变量。`serve` 和 `setup` 的所有标志与环境变量，以及数据目录布局，也见[配置](configuration.md)。

## mistgate

| 命令 | 功能 |
|---|---|
| `serve` | 运行面板。 |
| `setup` | 创建数据目录、主密钥和数据库；显示管理后台 URL 和一次性设置链接。 |
| `backup keygen` | 为面板加密备份生成私有恢复身份，并显示公开接收方。 |
| `backup restore` | 在离线状态下将下载的面板备份解密到新的数据目录。 |
| `auth turnstile off` | 关闭登录页和设置页上的 Cloudflare 人机验证。 |
| `auth reset-login` | 为管理员设置新密码和身份验证器应用，或添加密码登录方式。 |
| `mcp` | 为无法使用 HTTP 的智能体客户端提供连接面板 MCP 端点的本地 stdio 代理。 |
| `release keygen` | 生成所有者的发布密钥。 |
| `release build` | 以唯一可复现的方式构建发布二进制文件（CI 和 `make build` 都使用此命令）。 |
| `release sign` | 根据发布标签重新构建二进制文件和 Cloudflare 版发布包，然后为节点发布包、面板清单和 Cloudflare 版清单签名。 |
| `release trust-key` | 轮换密钥后，在面板服务器上将二进制文件中的发布密钥设为此安装使用的密钥。 |
| `edge deploy` | 将 Cloudflare 版（运行在 Cloudflare Workers 上的面板）部署到你的 Cloudflare 账户。预览版。 |
| `edge setup-link` | 从 `edge deploy` 写入的密钥文件中显示一次性设置链接。 |
| `version` | 显示版本、构建时间和发布密钥指纹。 |

`mistgate` 成功时以 0 退出；发生错误时以 1 退出（显示为 `mistgate: <error>`）；命令未知或缺少命令时以 2 退出。

此外还有一个隐藏命令 `panel-update-helper`。只有面板更新程序会以 root 身份启动它：面板本身以 root 运行时使用临时 systemd 单元，否则通过固定的 root 单元 `mistgate-panel-update.service` 启动。此命令不供手动运行；参见[更新](../operations/updates.md)。

### mistgate serve

在前台运行面板。使用 systemd 或其他进程管理器运行。除开发模式外，需要先通过 `mistgate setup` 安装。

```sh
mistgate serve --listen :443 --acme-domain panel.example.com
```

| 标志 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `--listen` | `MISTGATE_LISTEN` | `127.0.0.1:8080` | 公共监听器的地址：伪装站点、订阅、管理后台（位于私密前缀或主机名下时）和 Agent 接入端点。 |
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | 数据目录。 |
| `--tls-cert`, `--tls-key` | `MISTGATE_TLS_CERT`, `MISTGATE_TLS_KEY` | none | 公共监听器使用的自有证书和密钥。必须同时提供。文件变更时会重新加载证书。 |
| `--acme-domain` | `MISTGATE_ACME_DOMAIN` (comma-separated) | none | 为此主机名获取 Let’s Encrypt 证书。可重复指定。公共监听器必须能从端口 443 访问。使用此选项即表示接受 CA 的服务条款。证书保存在 `<data-dir>/acme` 中。 |
| `--acme-email` | `MISTGATE_ACME_EMAIL` | none | Let’s Encrypt 的联系邮箱。 |
| `--acme-http` | `MISTGATE_ACME_HTTP` | `:80` | 配合 `--acme-domain` 使用，用于 ACME HTTP-01 验证和 http → https 重定向的监听器。设为空可关闭。 |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | the stored setting | 单独的管理后台监听器（普通 HTTP）。仅适用于通过 `setup --admin-listen` 设置的安装；其他情况会在启动时被拒绝。 |
| `--agent-listen` | `MISTGATE_AGENT_LISTEN` | none (`127.0.0.1:8082` with `--dev`) | 为节点 Agent 单独提供 TLS 监听器。未设置时，Agent 会使用带私密 TLS 名称的公共监听器，因此该监听器必须启用 TLS（使用 `--tls-cert` 或 `--acme-domain`）。 |
| `--agent-addr` | `MISTGATE_AGENT_ADDR` | `--agent-listen` when it names a host, else the public URL's host and port (443 when none) | Agent 要连接的 `host:port`；此值会写入安装命令。 |
| `--decoy-dir` | `MISTGATE_DECOY_DIR` | the built-in page | 放置自有伪装站点的目录。其中的 `404.html` 和 `429.html` 会替换内置错误页面。 |
| `--trusted-proxy` | `MISTGATE_TRUSTED_PROXY` (comma-separated) | none | 可信反向代理的 CIDR 或 IP；其 `X-Forwarded-For` 和 `Forwarded` 请求头会被采信。可重复指定。未设置时，TCP 对端就是客户端。 |
| `--update-service` | `MISTGATE_UPDATE_SERVICE` | `mistgate.service` (none with `--dev`) | 面板自身的 systemd 单元；面板更新程序在替换二进制文件前后会停止并重新启动它。面板以 root 运行时使用此值；面板以其他用户身份运行时，会启动固定的 root 辅助单元，命令行中会指定该单元本身。 |
| `--source-url` | `MISTGATE_SOURCE_URL` | `https://github.com/Mistgate/mistgate` | 此构建源代码的发布地址，会显示在管理后台版本旁（AGPL-3.0 第 13 节）。派生版本（fork）应指向自己的仓库；设为空可隐藏链接。 |
| `--dev` | `MISTGATE_DEV` (`1`, `true`, `yes`, `on`) | off | 开发模式，见下文。 |

**开发模式。** `--dev` 使用 `./.data`，通过普通 HTTP 提供服务：伪装站点位于 `127.0.0.1:8080`，管理后台位于 `127.0.0.1:8081`，Agent 接入端点位于 `127.0.0.1:8082`；WebAuthn 依赖方使用 `localhost`；无需运行 `setup`。尚未创建管理员时，会显示一个一次性设置链接（`http://localhost:8081/setup#…`，有效期为 30 分钟）。切勿在服务器上使用此模式。

### mistgate setup

准备新安装：创建数据目录（权限模式 0700）、主密钥和数据库，保存管理后台的访问地址，并显示如何访问。可以安全地再次运行：现有配置会保留；只有尚未创建管理员时才会签发新的设置链接（先前未使用的链接随即失效）。如果数据库已存在但缺少 `master.key`，命令会拒绝继续，并要求从备份中恢复密钥，而不是新建密钥。

```sh
mistgate setup --public-url https://panel.example.com
```

```text
Data dir:   /var/lib/mistgate
Admin URL:  https://panel.example.com/<secret prefix>/
Setup link: https://panel.example.com/<secret prefix>/setup#<token>
The link works once and expires in 30 minutes. Open it in a browser and create your admin (a passkey, or a password with an authenticator code).
```

| 标志 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` | 数据目录（数据库、主密钥）。 |
| `--public-url` | `MISTGATE_PUBLIC_URL` | none | 公共（伪装）站点的 URL，例如 `https://panel.example.com`。此 URL 也是订阅链接的基础地址。 |
| `--admin-host` | `MISTGATE_ADMIN_HOST` | none | 使用此私密主机名提供管理后台，而不是使用私密路径前缀。 |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | none | 使用单独的监听器提供管理后台，例如 `127.0.0.1:8081`。不能与 `--admin-host` 同时使用。 |
| `--rp-id` | `MISTGATE_RP_ID` | derived | WebAuthn 依赖方 ID。 |
| `--rp-origins` | `MISTGATE_RP_ORIGINS` | derived | 允许的 WebAuthn 来源，使用逗号分隔。 |

必须指定 `--public-url`、`--admin-host` 或 `--admin-listen` 之一。管理后台的访问地址如下：

| 指定参数 | 管理后台 URL | WebAuthn 依赖方 |
|---|---|---|
| `--public-url` only | `<public url>/<24 random characters>/` | the public host |
| `--admin-host admin-k7q2.example.com` | `https://admin-k7q2.example.com/` (scheme and port of `--public-url` when given) | the admin host |
| `--admin-listen 127.0.0.1:8081` | `http://localhost:8081/` | `localhost` |

setup 还会生成 Agent 接入端点的私密 TLS 名称和订阅链接的私密前缀。再次运行 `setup` 无法更改这些设置。

### mistgate backup

这两个命令用于[面板加密备份](../operations/backups.md)的离线操作。备份本身由运行中的面板创建（**设置 → 备份**）；这些命令不会连接面板或存储桶。

```text
mistgate backup keygen --identity-file FILE
mistgate backup restore --identity-file FILE --file BACKUP.tar.gz.age --data-dir NEW-DIR
```

| 标志 | 含义 |
|---|---|
| `keygen --identity-file` | 私有 age 恢复身份的写入位置。文件必须不存在，不会覆盖已有文件。缺少的父目录会以 0700 权限模式创建，文件权限为 0600。 |
| `restore --identity-file` | 由 `backup keygen` 生成的私有身份。 |
| `restore --file` | 从存储桶下载的加密备份（`.tar.gz.age`）。 |
| `restore --data-dir` | 用于恢复数据的新目录；该目录必须尚不存在。 |

所有标志都是必需的；没有对应的环境变量。**`backup keygen`** 只需运行一次，并且应在受信任的非面板服务器上运行。它会显示文件路径和要填入**设置 → 备份**的公开接收方（`age1…`）；请将身份文件离线保存。

**`backup restore`** 不需要网络或正在运行的面板。它会解密归档、根据归档清单检查每个文件、检查主密钥和数据库（如果归档由较新版本的 Mistgate 创建，而当前二进制文件不认识其中的数据库，则会拒绝恢复并提示创建归档的版本；数据库还必须通过完整性检查），将内容解压到目标目录旁的临时目录中，只有全部检查通过后才会将其重命名到目标位置。将 `serve --data-dir` 指向新目录；如果 systemd 通过 `LoadCredential=` 向面板提供 `master.key`，请先将该凭据替换为恢复出的密钥。

```sh
mistgate backup restore --identity-file ./mistgate-recovery.txt \
  --file ./backup.tar.gz.age --data-dir /var/lib/mistgate-restored
```

### mistgate auth

供面板服务器上的运维人员使用。面板停止或运行时都可以执行这两个命令：面板会在每次请求时读取这些设置。

```text
mistgate auth turnstile off [--data-dir DIR]
mistgate auth reset-login [<login>] [--admin ID] [--qr-invert] [--data-dir DIR]
```

| 标志 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | 数据目录。若目录中没有面板数据库，命令会拒绝执行。 |
| `--dev` | `MISTGATE_DEV` | off | 开发安装：数据位于 `./.data`。 |
| `--admin` | none | none | 仅供 `reset-login` 使用：要为其添加新密码登录方式的管理员 ID。 |
| `--qr-invert` | none | off | 仅供 `reset-login` 使用：为浅色终端背景绘制二维码。 |

**`auth turnstile off`** 会关闭登录页和设置页上的 Cloudflare 检查；密钥仍会保留。如果无法访问 Cloudflare 或密钥错误，可以用此命令恢复登录。可在**设置 → 安全**中重新启用。

手机丢失后，可通过 **`auth reset-login`** 恢复登录：

- `mistgate auth reset-login` 会列出面板管理员的 ID、名称、角色、登录名和通行密钥数量。
- `mistgate auth reset-login <login>` 会为该登录名设置新密码和新的身份验证器应用、解除锁定，并结束该管理员的所有会话。通行密钥会保留。
- `mistgate auth reset-login <new login> --admin <id>` 会为仅使用通行密钥的管理员添加密码登录方式（有多个管理员时用 `--admin` 选择）。

命令会显示登录名、新密码和身份验证器密钥：以终端绘制的二维码、每四个字符分组的文本，以及 `otpauth://` 链接形式提供。密码只显示一次。命令需要主密钥，因此应以有权读取数据目录的用户身份运行。这两个命令都会在审计日志中标记为「命令行」。

```sh
mistgate auth reset-login alice
```

### mistgate mcp

此本地 MCP 服务器通过标准输入和标准输出与只能启动命令的智能体客户端交互。它会将每条消息转发到面板的 MCP 端点（管理后台 URL 加上 `mcp`），自身不作任何判断：面板会根据令牌权限级别响应。

```sh
mistgate mcp --url https://panel.example.com/<prefix>/ --token-file ~/.config/mistgate/token
```

| 标志 | 环境变量 | 含义 |
|---|---|---|
| `--url` | `MISTGATE_URL` | `mistgate setup` 显示的管理后台 URL。除非主机是 `localhost` 或回环地址，否则会拒绝普通 `http`。URL 不能包含凭据、查询字符串或片段。 |
| `--token-file` | `MISTGATE_TOKEN_FILE` | API 令牌（`tk1_…`）所在文件的路径，令牌必须位于第一行。令牌只会从文件读取，不会从命令行或环境变量读取，也绝不显示。其他人可读取此文件时会显示警告。 |

面板拒绝令牌（令牌已过期、已吊销或权限级别不正确）时，代理会停止。stdout 仅包含协议消息；其他所有内容都写入 stderr。参见 [MCP](mcp.md)。

### mistgate release keygen

```sh
mistgate release keygen --out ~/mistgate-release.key
```

| 标志 | 含义 |
|---|---|
| `--out` | 私钥文件的写入位置。文件必须不存在，不会覆盖已有密钥。文件权限为 0600。 |

显示公钥及其指纹。将公钥写入构建命令：`RELEASE_KEY=<public key> make build`。请离线保存并备份密钥文件。参见[发布版本和签名](../operations/releases.md)。

### mistgate release build

```text
mistgate release build --version V [--built UNIX] [--key PUBLIC] [--source DIR] NAME... --out DIR
```

| 标志 | 默认值 | 含义 |
|---|---|---|
| `--version` | none | 写入二进制文件的版本，例如 `v0.1.4`。 |
| `--built` | commit time of `--source` | 源代码提交的 Unix 时间；用于确定发布顺序。 |
| `--key` | none | 发布公钥（base64）。未提供时，二进制文件无法自更新。 |
| `--source` | `.` | 源代码检出目录。 |
| `--out` | none | 二进制文件的输出目录。 |

`NAME` 可以是 `mistgate-linux-amd64`、`mistgate-linux-arm64`、`mistgate-node-linux-amd64`、`mistgate-node-linux-arm64` 或 `mistgate-edge.tar.gz`（Cloudflare 版：将五个角色的 wasm 文件、使用固定版本 esbuild 打包的六个 Worker 脚本、`web/dist` 和 `edge.json` 放入确定性 tarball；除 SPA 外，还需在 `edge/worker` 中运行 `pnpm install --frozen-lockfile`）。构建可复现：使用 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false`、`go.mod` 中指定的 Go 工具链（本地 Go 版本不同时会下载指定版本）、`GOAMD64=v1` / `GOARM64=v8.0`，并且 `-ldflags` 仅设置版本、构建时间和密钥。面板二进制文件内嵌 `web/dist`：先构建 SPA（`cd web && pnpm install --frozen-lockfile && pnpm build`）。发布工作流和 `make build` 都会调用此命令；命令会显示每个二进制文件的 SHA-256。

### mistgate release sign

```text
mistgate release sign --key FILE --version V [--built UNIX] [--expires 30d] [--source DIR] BINARY... --out DIR
```

| 标志 | 默认值 | 含义 |
|---|---|---|
| `--key` | none | 由 `release keygen` 生成的发布私钥文件。 |
| `--version` | none | 发布标签，例如 `v0.1.4`。 |
| `--built` | commit time of the tag | 若提供，必须是该标签对应提交的时间。 |
| `--expires` | `30d` | 清单在多久内可用于安装：可以用天数（`30d`）或 Go 时长（`720h`）表示。 |
| `--source` | `.` | `--version` 标签的干净检出目录（若跟踪文件有本地修改则会拒绝执行）。 |
| `--out` | none | 清单、清单签名和二进制文件副本的输出目录。 |

二进制文件命名为 `<name>-<os>-<arch>`：`mistgate-node-linux-*` 放入节点发布包（`manifest.json`、`manifest.sig`），`mistgate-linux-*` 放入面板清单（`panel-manifest.json`、`panel-manifest.sig`，使用单独的 Ed25519 签名上下文，因此两种签名不能相互验证）。签名前，命令会使用 `release build` 和 `--key` 中的公钥部分，根据 `--source` 重新构建每个二进制文件；只要有一个二进制文件与构建结果存在任何字节差异，命令就会拒绝签名。签名对象是标签实际构建出的文件，而不是 CI 运行器上传的文件。若同时提供 `mistgate-edge.tar.gz`，也会按同样方式重新构建，并使用第三个签名上下文为其生成独立的 `edge-manifest.json` 和 `edge-manifest.sig`。标志可以放在二进制文件之后。命令会验证生成的文件，并显示每个文件的大小和密钥指纹。使用任何版本在本地构建的签名程序都可以运行：`go build -o mistgate ./cmd/mistgate`。

```sh
mistgate release sign --key ~/mistgate-release.key --version v0.1.4 --source ~/src/mistgate \
  downloaded/mistgate-node-linux-amd64 downloaded/mistgate-node-linux-arm64 \
  downloaded/mistgate-linux-amd64 downloaded/mistgate-linux-arm64 --out signed
```

完整发布流程参见[发布版本和签名](../operations/releases.md)。

### mistgate release trust-key

```text
mistgate release trust-key [--data-dir DIR] [--dev]
```

轮换发布密钥并安装使用新密钥构建的面板后，以面板服务用户或 root 身份在面板服务器上运行。此命令会将编译在该二进制文件中的密钥写入 `<data-dir>/release.pub`，并显示新旧指纹。在此之前，如果面板内置密钥与 `release.pub` 不同，面板会记录错误且不信任任何发布包。之后请重启面板。`--data-dir` 会回退到 `MISTGATE_DATA_DIR`，再回退到 `/var/lib/mistgate`；`--dev`（或 `MISTGATE_DEV`）表示使用 `./.data`。未编译发布密钥的二进制文件会拒绝执行。

### mistgate edge

```text
mistgate edge deploy --host HOST [--name NAME] [--d1-location LOC] [--bundle DIR | --version vX.Y.Z] [--resume] [--dry-run] [--insecure-skip-signature]
mistgate edge deploy --host HOST [--name NAME] --rollback [--dry-run]
mistgate edge setup-link --file FILE
```

`edge deploy` 会创建 D1 数据库和 Cloudflare 版的六个 Worker 脚本，将 `HOST` 绑定为自定义域名，并显示管理后台地址和一次性设置链接。它只从 `CLOUDFLARE_API_TOKEN` 读取 API 令牌，并将新面板的密钥写入当前目录中的 `mistgate-edge-<name>.json`：请备份该文件。`--dry-run` 只读取 Cloudflare 信息并显示计划；`--resume` 会继续此前中断的运行。若不带 `--resume` 再次运行，则会更新部署并只上传有变化的脚本；`--rollback` 会让每个脚本回到上次更新之前的版本。`edge setup-link` 会显示密钥文件中保存的设置链接。所有标志、令牌权限和首次登录方式见[安装 Cloudflare 版](../getting-started/install-edge.md)。

### mistgate version

依次显示 `mistgate <version>`；如果二进制文件构建时记录了构建时间，接着显示 `built <time>`；最后显示 `release key <fingerprint>` 或 `release key: none (unsigned build, nodes are updated by hand)`。

## mistgate-node

| 命令 | 功能 |
|---|---|
| `enroll` | 使用一次性令牌换取节点证书。 |
| `install` | 写入加固配置的 systemd 单元，并启用和启动它。 |
| `run` | 在前台运行 Agent。 |
| `cleanup-net` | 移除 Agent 的隧道接口、WARP 路由和 nftables 表。 |
| `awg prepare-kernel` | 安装 AmneziaWG 内核模块。 |
| `version` | 显示版本、构建时间和发布密钥指纹。 |

`enroll` 和 `run` 都使用 `--state-dir`；两者必须指定同一目录。退出代码：成功为 0，错误为 1，用法错误为 2；节点尚未注册时 `run` 返回 78（此时 systemd 不会重启 Agent）。

在添加节点时，面板会显示完整的安装命令：

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

### mistgate-node enroll

| 标志 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `--panel` | `MISTGATE_PANEL` | none | 面板地址，格式为 `host:port`。 |
| `--sni` | `MISTGATE_AGENT_SNI` | none | 面板 Agent 接入端点的私密 TLS 名称。 |
| `--ca-sha256` | `MISTGATE_CA_SHA256` | none | 面板 CA 证书的 SHA-256 指纹（64 位十六进制数字）。Agent 只凭此指纹信任面板。 |
| `--token` | `MISTGATE_ENROLL_TOKEN` | none | 一次性注册令牌。使用环境变量可避免令牌出现在进程列表中。 |
| `--token-stdin` | none | off | 改为从 stdin 读取令牌（单行，最多 256 字节）：令牌不会出现在进程列表或 shell 历史记录中。不能与 `--token` 或 `MISTGATE_ENROLL_TOKEN` 同时使用。 |
| `--state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | 状态目录。 |
| `--force` | none | off | 替换现有身份。 |
| `--resume-key` | none | off | 在状态目录中将新私钥保存在 `enroll-pending.pem`，直到注册成功；下次尝试时继续使用该密钥，以便重试中断的注册：10 分钟内使用相同令牌和密钥重复请求时，面板会返回相同证书。与 `--force` 一起使用时会生成新密钥。 |

必须提供 `--panel`、`--sni`、`--ca-sha256` 和令牌（`--token`、`MISTGATE_ENROLL_TOKEN` 或 `--token-stdin`）；缺少任一项时，`enroll` 都会以 2 退出。令牌只能使用一次，并且必须在面板显示的有效期内使用（默认一小时）。面板的 [SSH 安装](../getting-started/ssh-install.md)会通过 SSH 使用相同的 `enroll` 和 `install` 命令，令牌通过 stdin 传入，并带有 `--token-stdin --resume-key`。成功后会显示节点 ID 和下一步：

```text
enrolled as nod_… with panel.example.com:443; state in /var/lib/mistgate-node
next: mistgate-node install   (or, in the foreground: mistgate-node run)
```

### mistgate-node install

仅限 Linux；注册后以 root 身份运行。

| 标志 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `--state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | 状态目录；必须与 `enroll` 使用的目录相同。 |
| `--bin` | none | `/usr/local/bin/mistgate-node` | 二进制文件的存放位置。如果当前运行的二进制文件位于其他位置，则会复制到此处。 |
| `--no-start` | none | off | 写入并启用单元，但不启动它。 |

此命令会写入 `/etc/systemd/system/mistgate-node.service`，然后运行 `systemctl daemon-reload`、`enable` 和 `restart`。该单元会：

- 以 root 身份运行 Agent，仅授予两项能力（网络管理和绑定低端口），并将系统设为只读，但状态目录、二进制文件所在目录（供自更新使用）、`/etc/sysctl.d` 和 `/etc/systemd/journald.conf.d` 除外；
- 根据服务器 RAM 设置内存限制；
- Agent 发生故障后 5 秒重启，但退出代码为 78 时不重启；
- 每次启动前，若新更新导致程序反复崩溃，则恢复之前的二进制文件；每次停止后都会运行 `cleanup-net`。

替换为新二进制文件后再次运行 `install`，即可手动更新节点（参见[更新](../operations/updates.md)）。路径只能包含字母、数字和 `_ . / -`。

### mistgate-node run

在前台运行 Agent，也就是执行服务单元的操作；可用于不通过 systemd 试运行 Agent。

| 标志 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `--state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | 状态目录。 |
| `--log-level` | `MISTGATE_LOG_LEVEL` | `info` | 可选值为 `debug`、`info`、`warn` 或 `error`。 |
| `--log-format` | `MISTGATE_LOG_FORMAT` | `text` | 可选值为 `text` 或 `json`。日志写入 stderr。 |

状态目录中没有身份时，命令以 78 退出；面板将节点退役时，以 0 退出。

### mistgate-node cleanup-net

不接收参数。移除 Agent 的隧道接口、隧道 nftables 表，以及 WARP 设备、路由、规则和表（如果存在）。不需要状态目录或面板。服务单元每次停止后都会运行此命令；Agent 启动时会重新创建这些内容。

### mistgate-node awg prepare-kernel

在节点上安装 AmneziaWG 内核模块。仅限 Linux，以 root 身份运行。默认的用户态后端无需内核模块。

```sh
mistgate-node awg prepare-kernel          # 输出计划，不执行任何操作
mistgate-node awg prepare-kernel --yes    # 执行命令
```

| 标志 | 默认值 | 含义 |
|---|---|---|
| `--yes` | off | 执行命令。不带此标志时只显示计划。 |
| `--verify-only` | off | 只检查已安装的模块（已加载、接口版本正确、已配置为在启动时加载）。 |
| `--status-file` | none | 将进度和结果写入此文件（面板要求 Agent 准备模块时使用）。 |
| `--timeout` | `45m` | 本次运行的最长时限。 |

它会使用 apt 安装软件包（非交互式；如其他包管理器持有锁，最多等待 30 分钟）。支持 Debian 和 Ubuntu；不支持容器；必须关闭 Secure Boot 并使用 systemd。参见 [AmneziaWG](../guide/amneziawg.md)。

### mistgate-node version

显示 `mistgate-node <version>`、`built <unix time>`，以及 `release key <fingerprint>` 或 `release key none (unsigned build: update by hand)`。
