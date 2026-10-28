---
title: Mistgate 文档
description: 文档包含哪些内容、在哪里查找，以及阅读顺序。
---

Mistgate 是供你管理自己的 VPN 集群的自托管面板：一个二进制文件用于面板，一个用于节点 Agent；Hysteria2、VLESS REALITY 和 AmneziaWG 共用一份订阅。本文档说明代码目前的行为；仍在开发的功能会标为 **计划中**。

## 文档内容

| 部分 | 内容 |
|:--|:--|
| 入门 | 基本概念、所需条件，以及从一台空服务器到首位用户获得可用订阅的步骤。 |
| 使用指南 | 管理后台中的日常操作：节点、协议配置、协议、WARP、用户、订阅、客户端应用、用户页面和 DNS。 |
| 运维 | 保持集群健康、更新和安全，以及发生故障时的处理方法。 |
| 路线图 | 已完成的内容、正在测试的内容、后续计划和已知限制。 |
| 参考 | 准确的命令、标志、环境变量、API、MCP 服务器，以及各部分的配合方式。 |

## 新管理员的阅读顺序

1. [概览](getting-started/overview.md)和[系统要求](getting-started/requirements.md)：了解面板、节点和协议配置是什么，以及需要哪些服务器。
2. 从官方发布版本[安装面板](getting-started/install-panel.md)，手动安装，或使用 [AI 智能体指南](getting-started/ai-agents.md)中的安装提示词。也可以部署到 [Cloudflare Workers](getting-started/install-edge.md)（预览版），无需自备服务器。
3. [通过 SSH 安装节点](getting-started/ssh-install.md)，或使用自行运行的命令[添加节点](getting-started/add-node.md)。
4. [首批用户](getting-started/first-users.md)：在节点上部署协议配置、创建分组和用户，并发送订阅链接。
5. 向其他人提供链接前，先了解[运行状况](operations/health.md)、[安全](operations/security.md)和[加密备份](operations/backups.md)。
6. 按需阅读使用指南页面；编写脚本或自动化时查阅参考。

## 所有页面

### 入门

- [概览](getting-started/overview.md)：Mistgate 是什么，以及其基本概念：面板、节点、协议配置、节点上的服务器、用户、分组、设备、订阅和用户页面。
- [系统要求](getting-started/requirements.md)：面板和节点的要求，以及从源代码构建所需的条件。
- [安装面板](getting-started/install-panel.md)：从一台全新的 Linux 服务器到在管理后台创建所有者账户。
- [安装 Cloudflare 版](getting-started/install-edge.md)：使用 `mistgate edge deploy` 在 Cloudflare Workers 上运行面板，而非使用服务器（预览版）。
- [添加节点](getting-started/add-node.md)：添加节点的两种方式、手动安装命令以及移除节点。
- [通过 SSH 安装节点](getting-started/ssh-install.md)：面板自行安装 Agent、安装任务和已保存的 SSH 凭据。
- [AI 智能体指南](getting-started/ai-agents.md)：用于安装面板、通过 MCP 管理集群和参与代码开发的现成提示词。
- [首批用户](getting-started/first-users.md)：在节点上部署协议配置、将其提供给分组、创建用户并发送链接。

### 使用指南

- [节点](guide/nodes.md)：节点设置、状态以及 Agent 在主机上的工作。
- [协议配置](guide/profiles.md)：协议配置是什么，以及它如何成为节点上的服务器。
- [Hysteria2](guide/hysteria2.md)：Hysteria2 协议配置设置、证书和混淆。
- [VLESS REALITY](guide/vless.md)：TCP 和 XHTTP 上的 VLESS 协议配置、何时选择它而不是 Hysteria2、如何选择 REALITY 目标、端口和应用。
- [同一节点上的 VLESS 与 Hysteria2](guide/vless-and-hysteria2.md)：分别由什么监听端口、VLESS 占用 TCP 443 时 Hysteria2 站点和 Let’s Encrypt 会发生什么，以及如何让少数用户试用 VLESS。
- [AmneziaWG](guide/amneziawg.md)：AmneziaWG 2.0 和 3.1 协议配置、用户态或内核模块、设备和密钥。
- [WARP](guide/warp.md)：通过 Cloudflare WARP 转发协议配置的流量。
- [经入口节点中转](guide/relays.md)：中转何时能帮到网络封锁了节点的用户、它解决不了什么、入口能看到什么，以及如何添加。
- [用户和分组](guide/users-and-groups.md)：用户、分组、设备、流量限制和有效期。
- [订阅](guide/subscriptions.md)：每人一个链接，以及每个应用会获得哪种格式。
- [客户端应用](guide/client-apps.md)：各平台使用哪些应用、每个应用会收到什么、一键添加链接、kl!ck，以及如何在管理后台和通过 MCP 设置应用。
- [用户页面](guide/user-page.md)：用户自己的页面，包含说明、二维码和流量信息。
- [DNS](guide/dns.md)：用户和分组的 DNS 预设，以及节点的 DNS。
- [BT 下载拦截](guide/torrent-protection.md)：逐节点的 BitTorrent 拦截功能、拦截内容及事件显示的信息。
- [自有伪装站点](guide/decoy-site.md)：用自己的小型站点替换内置伪装页面，并提供让 AI 智能体生成独特站点的现成提示词。

### 运维

- [运行状况](operations/health.md)：客户端视角检测、节点诊断和告警。
- [可达性](operations/reachability.md)：能否从某个国家境内访问节点：Globalping、封锁名单、TSPU 探测、每个节点的最近一周和告警。
- [Telegram 告警](operations/telegram-alerts.md)：机器人、绑定聊天、发送内容和对象，以及面板监视器。
- [面板监视器](operations/panel-watcher.md)：Cloudflare 每分钟检查一次面板，并通过 Telegram 报告服务中断。
- [更新](operations/updates.md)：节点自更新、立即或定时更新节点、回滚以及更新面板。
- [发布版本和签名](operations/releases.md)：发布密钥、可重现构建、签名和发布，以及自建发布包。
- [加密备份](operations/backups.md)：配置 R2、妥善保管离线恢复私钥并恢复面板。
- [安全](operations/security.md)：管理后台登录、角色、再次验证、会话、审计、伪装站点和隐藏的管理后台、数据目录和备份，以及恢复访问权限。
- [故障排查](operations/troubleshooting.md)：常见问题及其原因查找方法。

### 路线图

- [状态与路线图](roadmap/status.md)：当前版本的功能、后续计划和已知限制。

### 参考

- [命令行](reference/cli.md)：所有 `mistgate` 和 `mistgate-node` 命令及标志。
- [配置](reference/configuration.md)：所有 `serve` 和 `setup` 标志、每个 `MISTGATE_*` 环境变量以及数据目录布局。
- [API](reference/api.md)：Connect API、API 令牌及其权限级别。
- [MCP](reference/mcp.md)：MCP 服务器、stdio 代理、工具、计划 / 执行和审批。
- [架构](reference/architecture.md)：面板、各 Agent 和客户端之间的通信方式。
- [常见问题](reference/faq.md)：常见问题的简短解答。
- [与其他面板的比较](reference/comparison.md)：Mistgate 与 3x-ui、Marzban、PasarGuard、Remnawave、Hiddify Manager 和 s-ui 的比较及来源。
- [基准测试](reference/benchmarks.md)：在同一台小型服务器上比较 Mistgate、3x-ui、Remnawave 和 PasarGuard 的内存、CPU、启动时间和负载，并说明方法和限制。
