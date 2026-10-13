---
title: 安装面板
description: 下载或构建 Mistgate，使用 mistgate setup 准备面板，通过 systemd 运行，并首次登录。
---

本页介绍如何从一台全新的 Linux 服务器开始，运行面板并创建所有者账户。示例使用 `panel.example.com`；先阅读[要求](requirements.md)。面板服务器上的命令以 root 身份运行。能够通过 SSH 访问服务器的 AI 智能体也可以替你完成这些步骤：见 [AI 智能体指南](ai-agents.md)中的安装提示词。

> **注意：** 本页介绍在自有服务器上运行面板。若要改为在 Cloudflare Workers 上运行（预览版），无需服务器，参见[安装 Cloudflare 版](install-edge.md)。

## 一行安装

对于一台全新的 amd64 或 arm64 Linux 服务器（使用 systemd），以 root 身份运行以下命令。域名必须直接指向服务器，TCP 80 和 443 端口必须空闲。验证签名需要支持 `ssh-keygen -Y verify` 的 `openssh-client`，或 OpenSSL 3.2 及更高版本。安装器会下载最新发布版本，根据 `SHA256SUMS` 和已签名的面板清单检查二进制文件，然后运行 setup 并安装 systemd 服务。它不会更改防火墙规则；如果 UFW 或 firewalld 已启用，安装器会显示需要添加的规则。

```sh
curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
```

默认情况下，安装器会为 URL 中的主机名申请 Let’s Encrypt 证书。它会在更改前询问；非交互式运行时添加 `--yes`。使用 `--dry-run` 下载并验证发布版本，但不更改主机；使用 `--version vX.Y.Z` 安装指定版本。`--admin-host`、`--admin-listen`、`--acme-domain` 和 `--data-dir` 用于选择下文介绍的 setup 和服务选项。

安装完成后，在浏览器中打开一次性设置链接并创建所有者账户。链接 30 分钟后过期。然后通过**节点 → 添加节点**添加节点。

## 手动安装

### 1. 获取二进制文件

面板是适用于 amd64 或 arm64 的单个静态 Linux 二进制文件，已内置管理后台 Web 应用。除非有理由自行构建，否则优先使用官方发布版本。

#### 从 GitHub Releases 获取（推荐）

每个 [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest) 发布版本都包含面板（`mistgate-linux-amd64`、`mistgate-linux-arm64`）、节点 Agent（`mistgate-node-linux-amd64`、`mistgate-node-linux-arm64`）、`SHA256SUMS`、`BUILDINFO`，以及两份已签名清单：节点 Agent 使用 `manifest.json` 和 `manifest.sig`；面板使用 `panel-manifest.json`、`panel-manifest.sig` 和 `panel-manifest.sshsig`。官方二进制文件内置项目的发布密钥，因此使用这些文件安装的面板会自行下载已签名的节点 Agent，并可从后续发布版本自行更新（见[更新](../operations/updates.md)）。

在面板服务器上以 root 身份运行（`uname -m` 在 amd64 上输出 `x86_64`，在 arm64 上输出 `aarch64`）：

```sh
ARCH=amd64    # ARM 服务器上改为 arm64
cd /root
for f in "mistgate-linux-$ARCH" SHA256SUMS panel-manifest.json; do
  curl -fsSLO "https://github.com/Mistgate/mistgate/releases/latest/download/$f"
done
sha256sum --check --ignore-missing SHA256SUMS
grep -q "$(sha256sum "mistgate-linux-$ARCH" | cut -d' ' -f1)" panel-manifest.json && echo "listed in panel-manifest.json"
install -m 0755 "mistgate-linux-$ARCH" /usr/local/bin/mistgate
mistgate version
```

这些检查能证明什么，不能证明什么：

- `sha256sum --check` 可证明文件完整，并且与随发布版本发布的校验和一致。`SHA256SUMS` 由发布工作流生成，但没有签名，因此不能证明是谁构建了二进制文件。
- 相同的 SHA-256 值也列在 `panel-manifest.json` 中。维护者会在重新构建标签对应的所有二进制文件后，使用发布密钥离线签署该清单。一行安装器优先使用 `ssh-keygen -Y verify` 验证 `panel-manifest.sshsig`；如果不可用，再使用 OpenSSL 3.2 或更高版本验证 `panel-manifest.sig`。手动安装时，`grep` 只用于检查一致性。
- 从此以后，面板会自行检查签名：只有签名通过其编译时内置发布密钥的验证后，才会安装面板发布版本并向 Agent 提供节点包。`mistgate version` 会输出版本和该密钥的指纹。

#### 从源代码构建

在装有[要求](requirements.md)中列出的工具的构建机器上运行：

```sh
git clone https://github.com/Mistgate/mistgate.git
cd mistgate
make build
scp bin/mistgate-linux-amd64 root@panel.example.com:/root/
ssh root@panel.example.com 'install -m 0755 /root/mistgate-linux-amd64 /usr/local/bin/mistgate'
```

`bin/` 目录也包含节点 Agent（`mistgate-node-linux-amd64`、`mistgate-node-linux-arm64`）；保留这些文件，以供[手动安装节点](add-node.md)时使用。

> **警告：** 不带 `RELEASE_KEY` 的构建没有发布密钥：节点 Agent 不会自行更新，面板无法安装面板发布版本，SSH 安装也没有可信的 Agent 包可安装。使用自己的密钥构建时，只信任你签名的发布版本。首次构建前创建密钥：见[发布与签名](../operations/releases.md)。

### 2. 选择访问管理后台的方式

公网地址会向所有访问者显示伪装站点。运行 `mistgate setup` 时，需要一次性选择以下三种管理后台访问方式之一：

| 模式 | `setup` 标志 | 管理后台地址 | 适用情形 |
|:--|:--|:--|:--|
| 私密路径前缀（默认） | `--public-url https://panel.example.com` | `https://panel.example.com/<secret>/`，路径由 24 个随机字符组成 | 最简单：一个主机名，一个证书。 |
| 私密主机名 | `--public-url https://panel.example.com --admin-host <secret host>` | `https://<secret host>/` | 希望管理后台位于难以猜到的主机名上。需要 DNS 记录和覆盖该名称的证书。 |
| 单独的监听器 | `--public-url https://panel.example.com --admin-listen 127.0.0.1:8081` | `http://localhost:8081/`，通过 SSH 隧道访问 | 管理后台完全不使用公网端口。 |

- 不符合管理后台地址的请求（前缀错误、未知主机名或未知路径）都会显示伪装站点，因此探测者无法区分接近正确的尝试和随机猜测。
- 始终传入 `--public-url`。它是所有订阅链接的基础地址，也是节点安装命令中使用的地址。不传入时，面板不会提供订阅链接；除非 `serve` 获得 `--agent-addr`，否则添加节点也会失败。
- `--admin-host` 和 `--admin-listen` 不能同时使用。
- 单独的管理后台监听器使用普通 HTTP。将它绑定到 loopback，并通过 SSH 访问：

  ```sh
  ssh -N -L 8081:127.0.0.1:8081 root@panel.example.com
  ```

  然后在浏览器中打开 `http://localhost:8081/`。本地也要使用相同端口：通行密钥会绑定到 setup 保存的 `http://localhost:<port>` 地址。
- `--rp-id` 和 `--rp-origins` 可以覆盖 setup 根据管理后台地址推导出的 WebAuthn 设置。通常无需使用这两个标志。

> **警告：** setup 会一次性保存这些地址。再次运行时会保留现有地址；Mistgate 没有命令可以在之后更改它们。继续前选好；重新开始意味着使用新的数据目录，并重新注册每个节点。

### 3. 运行 setup

```sh
mistgate setup --public-url https://panel.example.com
```

它会输出：

```text
Data dir:   /var/lib/mistgate
Admin URL:  https://panel.example.com/<secret>/
Setup link: https://panel.example.com/<secret>/setup#<token>
The link works once and expires in 30 minutes. Open it in a browser and create your admin (a passkey, or a password with an authenticator code).
```

setup 会执行以下操作：

1. 创建权限为 0700 的数据目录 `/var/lib/mistgate`（可使用 `--data-dir` 更改路径）。
2. 创建包含 32 个随机字节且权限为 0600 的 `master.key`。面板保存的所有密钥都由它加密。
3. 创建数据库 `mistgate.db` 并应用数据库结构。
4. 保存所选模式的地址，并生成另外两个密钥：节点 Agent 用于连接面板的 TLS 名称，以及订阅链接的私密路径前缀。
5. 输出有效期为 30 分钟的一次性设置链接。

妥善保管管理后台 URL。再次运行 setup 是安全的：它会保留现有配置；只要尚未创建管理员，就会输出新的设置链接。

### 4. 选择证书

| 选项 | `serve` 标志 | 说明 |
|:--|:--|:--|
| Let’s Encrypt | `--acme-domain panel.example.com` | 面板会自行获取并续期证书（公网监听器使用 TLS-ALPN-01，因此必须能通过端口 443 访问）。端口 `:80` 上的第二个监听器会响应 HTTP-01，并将普通 HTTP 重定向到 HTTPS（`--acme-http`；空值会关闭此功能）。证书缓存于 `<data-dir>/acme`。`--acme-email` 可添加联系邮箱。使用此功能即表示接受 Let’s Encrypt 服务条款。 |
| 自有证书 | `--tls-cert /path/fullchain.pem --tls-key /path/privkey.pem` | 面板会提供你的 PEM 文件，并在证书文件更改时重新读取（最多每 30 秒检查一次），因此其他工具续期后无需重启。 |
| 两者 | 同时使用两组标志 | `--acme-domain` 列出的名称使用 Let’s Encrypt 证书；其他名称使用你的证书。适用于由你自己的通配符证书覆盖的私密管理后台主机名。 |

`--acme-domain` 只接受普通主机名，不接受通配符。可以重复使用此标志指定多个名称。

> **注意：** 私密管理后台主机名的 Let’s Encrypt 证书会将该名称写入公共证书透明度日志。要保密，使用自己的通配符证书覆盖该名称。

### 5. 伪装站点

如果没有其他设置，面板会显示内置的「即将推出」页面。所有安装都提供相同页面，因此容易被识别；建议使用自己的站点。将静态文件放在一个目录中，并传入 `--decoy-dir`：

- `/` 和所有目录都会提供各自的 `index.html`。
- 如果存在 `404.html` 和 `429.html`，则会分别替换内置的「未找到」和「请求过多」页面。所有未知路径、错误的管理后台前缀和未知订阅链接都会显示相同的 404 页面。
- 如果没有 `robots.txt`，则使用内置文件拒绝已知的 AI 爬虫。
- 只响应 GET 和 HEAD 请求。名称以点开头的文件和目录不会提供服务；响应不包含 Last-Modified 或 ETag 标头。

面板只会读取此目录。使用下方的 systemd 服务单元时，将该目录放在 `/home` 和 `/root` 之外（例如 `/srv/mistgate-decoy`）。[自有伪装站点](../guide/decoy-site.md)说明了合适站点的特点，并提供让 AI 智能体生成独特站点的提示词。

### 6. 在前台试运行

```sh
mistgate serve --listen :443 --acme-domain panel.example.com
```

`--listen` 默认为 `127.0.0.1:8080`，因此在真实服务器上传入 `--listen :443`。日志写入 stderr；检查公网监听器的 `listening` 行，以及端口 80 的 `acme-http` 行。确认运行正常后，按 Ctrl+C 停止。

### 7. 使用 systemd 运行

将以下内容保存为 `/etc/systemd/system/mistgate.service`，并将 `ExecStart` 改为你所选的标志：

```ini /etc/systemd/system/mistgate.service
[Unit]
Description=Mistgate panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mistgate serve --listen :443 --acme-domain panel.example.com
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

# 面板只写入自己的数据目录，只需绑定端口 443 和 80。
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
ProtectSystem=strict
ReadWritePaths=/var/lib/mistgate
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectClock=yes
ProtectControlGroups=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectHostname=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
```

```sh
systemctl daemon-reload
systemctl enable --now mistgate
journalctl -u mistgate -f
```

这些设置的原因：

- `mistgate setup` 以 root 身份运行，因此数据目录归 root 所有，服务也以 root 身份运行。除绑定低端口所需能力外，其他能力都会移除；只有 `/var/lib/mistgate` 可写。使用其他 `--data-dir` 时，修改 `ReadWritePaths`。
- 面板收到 SIGTERM 后会正常停止。设置 `Restart=on-failure` 后，systemd 会在监听器出错时重启面板；手动停止时不会重启。
- 每个标志都有对应的 `MISTGATE_*` 环境变量，因此可以将标志改为 `Environment=` 行或 `EnvironmentFile=`。见[配置](../reference/configuration.md)。
- 使用 `--decoy-dir`、`--tls-cert` 或 `--tls-key` 时，只需确保相关文件可读。

#### 在反向代理后运行

如果同一主机上的其他 Web 服务器必须占用端口 443，Mistgate 可以在它后面运行：

- 在 loopback 上运行无 TLS 的公网监听器（`--listen 127.0.0.1:8080`，不传入 `--acme-domain`、`--tls-cert`），让代理为 `panel.example.com` 终止 TLS，并原样传递 Host 标头和路径。
- 添加 `--trusted-proxy 127.0.0.1`，这样速率限制、会话和审计日志可以从 `X-Forwarded-For` 或 `Forwarded` 获取真实客户端地址。
- 节点 Agent 必须从面板本身获取 TLS。为它们单独配置一个监听器，并将其地址放入安装命令：`--agent-listen :8443 --agent-addr panel.example.com:8443`。代理不能处理该端口。

这样，Agent 端口会作为独立端口，只响应私密 TLS 名称；在共用的 443 端口上，它会隐藏在伪装站点后面。条件允许时，优先使用上面的直接配置。

### 8. 首次登录

在浏览器中打开第 3 步生成的设置链接，包括 `#` 后面的部分。

1. **语言。** 选择英语、俄语或简体中文。之后可以更改。
2. **创建管理员。** LOGIN 字段（默认为 `admin`）是你的用户名，也是面板中显示的名称。然后：
   - **创建通行密钥**：浏览器会要求使用指纹、人脸、PIN 或安全密钥进行验证。密钥保留在你的设备上。推荐使用此方式。
   - **改用密码 + 身份验证器验证码**：设置至少 12 个字符的密码，然后使用身份验证器应用（Google Authenticator、1Password、Aegis 等）扫描二维码，输入 6 位验证码，并按**创建管理员**。
3. **完成。** 选择**添加第一个节点**或**先随便看看，以后再说**。

第一个管理员是所有者。设置向导还会为首批用户创建一个名为「所有人」的空分组。

- 用户名长度为 3 到 64 个字符，只能包含 a–z、0–9 和 `. _ @ -`。
- 只有创建管理员时才会消耗该链接。若链接过期或发生错误，再次运行 `mistgate setup` 获取新链接。
- 通行密钥仅适用于 setup 保存的管理后台地址。「浏览器拒绝了此地址的通行密钥」表示打开页面时使用了其他地址。

之后，你可以在管理后台 URL 上使用**使用通行密钥登录**，或选择**其他方式**，输入用户名、密码和验证码。一个小时内输错密码五次后，该用户名会锁定 15 分钟。如果手机或通行密钥丢失，[安全](../operations/security.md)页面介绍了如何使用 `mistgate auth reset-login`。

### 9. 立即备份

面板的所有数据都位于数据目录中：

| 文件 | 内容 |
|:--|:--|
| `mistgate.db`、`mistgate.db-wal`、`mistgate.db-shm` | 数据库：管理员、节点、协议配置、用户、设备、流量、事件和审计日志。数据库中的密钥由主密钥加密。 |
| `master.key` | 用于加密已保存密钥的密钥，其中包括每个节点都信任的面板 CA 密钥。 |
| `acme/` | Let’s Encrypt 账户和证书（仅使用 `--acme-domain` 时）。 |

现在就复制一份。复制前先停止面板，以确保数据库一致：

```sh
systemctl stop mistgate
tar czf mistgate-backup-$(date +%F).tgz -C /var/lib mistgate
systemctl start mistgate
```

> **警告：** 备份副本包含主密钥：持有它的人可以读取面板的所有密钥。对副本加密并将其保存在服务器之外。如果数据目录丢失，就必须重新注册所有节点，所有用户、链接和密钥也都会丢失。

定期备份时，在**设置 → 备份**中启用[加密备份](../operations/backups.md)：面板会为你的离线恢复密钥加密一致性快照，并按计划上传到 Cloudflare R2 存储桶。如果你使用自己的发布密钥构建面板，也要将密钥文件离线备份。

## 后续阅读

[添加节点](add-node.md)：通过 SSH 自动安装，或使用你自行运行的命令安装。
