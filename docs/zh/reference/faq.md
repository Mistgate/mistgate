---
title: 常见问题
description: 关于 Mistgate 常见问题的简短回答：Docker、内存、应用、规模、IPv6、Cloudflare、迁移、许可证以及问题反馈渠道。
---

## 为什么没有 Docker 镜像？

Mistgate 由两个静态 Go 二进制文件组成：内嵌 SQLite 数据库和管理后台的面板，以及节点 Agent。面板运行不需要其他组件。Agent 会直接管理主机：nftables 表、sysctl 设置、隧道接口、journald 容量上限、自己的强化 systemd 单元和自更新。在容器中运行仍然需要主机网络和广泛权限，因此 Agent 直接由 systemd 启动，并只获得少量能力。参见[系统要求](../getting-started/requirements.md)。

## 它会占用多少内存？

- **面板：** 在 1 vCPU / 1 GB 测试服务器上，空闲时私有内存占用为 16–20 MiB，用户数为 0 到 200（参见[基准测试](benchmarks.md)；长期运行且有流量、日志和缓存的安装会占用更多内存）。密码登录期间，生成密码哈希会短暂多占用 19 MiB。
- **节点 Agent：** systemd 单元会根据服务器 RAM 设置限制：Go 运行时的目标约为 RAM 的 60%；超过约 70% 时，systemd 会限制 Agent；超过约 85% 时，会停止 Agent。主机内存不足时，诊断中的**内存**检查会发出警告（参见[运行状况](../operations/health.md)）。

## 支持哪些应用？

每位用户只有一个订阅链接，各应用会获取自己能读取的格式：

- **Hysteria2 和 VLESS**：使用 Happ 等订阅类应用（获取 URI 列表），或使用基于 mihomo 核心的应用，例如 kl!ck、Clash Verge 或 FlClash（获取 Mihomo YAML 协议配置）。在浏览器中打开链接会进入该用户的页面，其中包含使用说明。
- **AmneziaWG**：使用 AmneziaVPN 应用，每台设备单独使用密钥；kl!ck 和其他基于 mihomo 核心的应用通过订阅获取配置。

带有自签名证书的 Hysteria2 协议配置已使用官方 Hysteria2 客户端和 Mihomo 验证；尚未使用 Happ 验证。更多订阅格式（Xray JSON、sing-box）**计划中**。参见[订阅](../guide/subscriptions.md)和[客户端应用](../guide/client-apps.md)：Mistgate 推荐在 Windows 和 macOS 上使用 kl!ck。

## 可以管理多少用户和节点？

用户数和节点数没有内置限制。Mistgate 面向由一个人管理的小型集群：使用一个面板和一个 SQLite 数据库，每个节点各自与面板保持一个连接。

## 支持 IPv6 吗？

- 节点不需要 IPv6。只有当节点有 IPv6 地址但无法向外连接时，诊断中的 **IPv6** 检查才会发出警告，因为收到 AAAA 记录的客户端会因此卡住。
- WARP 使用 IPv4 端点，因此没有 IPv6 的节点也可以使用 WARP。
- 面板会监听 `--listen` 指定的地址；`:443` 同时覆盖 IPv4 和 IPv6。
- 速率限制和登录锁定会将一个 IPv6 /64 视为一个客户端。

## 面板可以运行在 Cloudflare 代理之后吗？

伪装站点、管理后台和订阅服务可以放在 Cloudflare 代理之后，需要进行三项设置：

1. **证书。** 通过 `--acme-domain` 使用 Let’s Encrypt 时，面板必须可被直接访问。位于代理之后时，请通过 `--tls-cert` 和 `--tls-key` 为面板配置自有证书，例如 Cloudflare 源站证书。
2. **客户端地址。** 使用 `--trusted-proxy` 列出 Cloudflare 地址范围，使速率限制、会话和审计日志能够获取真实客户端地址。
3. **节点 Agent。** Agent 需要直接建立 TLS 连接：它们使用面板自有 CA 进行双向 TLS，并通过私密 TLS 名称选择该连接；终止 TLS 的代理会破坏此连接。请使用 `--agent-listen` 单独提供监听器，并将未经过代理的地址（IP 或仅 DNS 解析的主机名）填入 `--agent-addr`。先前注册的节点仍会连接其安装命令中的地址；请使用新的安装命令重新注册。

这样配置后，Cloudflare 会看到管理后台的流量，包括其私密地址。登录时的 Cloudflare Turnstile 检查与此无关，两种配置下都能使用（参见[安全](../operations/security.md)）。标志说明见[配置](configuration.md)；反向代理后的面板运行方式见[安装面板](../getting-started/install-panel.md)。

## 面板可以不使用服务器，直接运行在 Cloudflare Workers 上吗？

可以作为预览版使用：`mistgate edge deploy` 会将面板部署到 Cloudflare 账户中的两个 Worker 和一个 D1 数据库。它需要 Workers Paid 计划和 Cloudflare DNS 中的域名，目前尚未在真实账户上运行；此版本不支持备份、Telegram 告警、MCP、隧道检测和更新。参见[安装 Cloudflare 版](../getting-started/install-edge.md)。

## 可以从其他面板迁移吗？

没有迁移工具。请设置面板，将服务器添加为节点，然后重新创建用户。在运行过其他 VPN 服务栈的服务器上，诊断的**其他 VPN 的残留**和**非 Mistgate 防火墙规则**检查会显示遗留内容（x-ui、Xray、旧 WireGuard 接口、其他 nftables 规则）；是否移除由你决定。

## 有可以下载的发布版本吗？

有。[GitHub 发布版本](https://github.com/Mistgate/mistgate/releases/latest) 中的每个发布版本都包含适用于 amd64 和 arm64 的 Linux 面板和节点 Agent 二进制文件，以及供面板获取节点 Agent 并自更新的签名清单；参见[安装面板](../getting-started/install-panel.md)。如需其他平台，请使用 `make build` 从源代码构建。Mistgate 仍处于 1.0 之前：API、已存储的设置和节点协议都可能变更。

## AGPL 对我有什么影响？

Mistgate 仅依据 GNU Affero General Public License v3.0 授权。未修改软件并供自己和用户使用时，无需特别处理。如果你修改面板并让其他人通过网络使用你的版本，许可证第 13 节要求你向他们提供该版本的源代码。

管理后台会在版本旁显示**源代码**链接。默认指向项目仓库；分支版本可通过 `serve --source-url <url>`（或 `MISTGATE_SOURCE_URL`）将其指向自己的仓库。设为空可隐藏链接。本段仅为摘要，不构成法律意见；以仓库中的 `LICENSE` 文件为准。

## 在哪里报告错误和漏洞？

- **错误和问题：**在 GitHub 仓库中提交 issue。请附上面板版本（显示在管理后台，也可通过 `mistgate version` 查看）和节点 Agent 版本。
- **安全漏洞：**请私下报告，切勿在公开 issue 中披露：打开仓库的 **「安全」** 标签页，然后选择 **「报告漏洞」**。参见[安全](../operations/security.md)。


