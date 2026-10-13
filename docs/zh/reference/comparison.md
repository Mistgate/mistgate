---
title: 与其他面板对比
description: Mistgate 与 3x-ui、Marzban、PasarGuard、Remnawave、Hiddify Manager 和 s-ui 的对比、其他项目领先之处，以及截至 2026-10-09 核对过的来源。
---

本页将 Mistgate 与人们最常用来执行相同任务的自托管面板进行比较。每个单元格都在 2026-10-09 对照项目自己的 README、文档和发布说明核实；「?」表示项目未说明。项目变化很快，作出决定前请先查看来源；发现单元格内容过时时，请提交 issue。

## Mistgate 的不同之处

- **从面板添加节点。** 使用密码通过 SSH 连接：面板固定主机密钥、检查服务器、配置已启用的主机防火墙，并安装签名 Agent。其他面板需要手动添加节点：为每个节点运行完整面板、安装节点软件包或使用生成的 compose 文件。
- **无需 Docker 或数据库服务器。** 只需两个静态 Go 二进制文件、SQLite 和 systemd。
- **原生支持 AmneziaWG 协议。** 支持 2.0 和 3.1 版，每台设备使用独立密钥（手机丢失时只吊销对应密钥，不影响其他设备），并为 AmneziaVPN 提供 `vpn://` 密钥。3x-ui 也提供用户态的 AmneziaWG 3.1。
- **从客户端侧进行检测。** 面板像应用一样连接每个节点上的每项协议配置；每个节点运行诊断程序，修复操作由你确认；告警会开启、关闭并发送到 Telegram。
- **签名发布版本。** 使用离线密钥为可复现构建签名；面板和节点只安装签名构建。
- **默认隐藏。** 公共地址提供伪装站点；管理后台位于私密路径、主机名或监听器之后。
- **内置 MCP 服务器**供 AI 智能体使用，支持计划 / 执行和所有者审批。

## 其他项目领先之处

- **协议。** 大多数其他面板支持 VMess、Trojan、Shadowsocks、TUIC 和普通 WireGuard。Mistgate 支持 Hysteria2、VLESS（REALITY 通过 XHTTP 或带 Vision 的 TCP 传输；TLS 通过 XHTTP 或 WebSocket 传输，可直连或位于 CDN 之后）以及 AmneziaWG。
- **订阅格式。** 其他面板提供 Xray JSON、sing-box、Clash、Stash 或 Outline。Mistgate 提供 URI 列表、Mihomo YAML 和 AmneziaVPN 密钥；Xray JSON 和 sing-box **计划中**。
- **社群与成熟度。** 3x-ui 约有 47.7k 个 GitHub 星标，s-ui 约有 10.0k 个，Hiddify Manager 约有 9.3k 个。Mistgate 是处于早期发布阶段的新项目。
- **设备限制。** 3x-ui、PasarGuard 和 Remnawave 根据 HWID 限制设备数量。Mistgate 按设备计数。
- **Telegram。** 3x-ui、Marzban、PasarGuard 和 Hiddify Manager 提供完整的 Telegram 机器人。Mistgate 会发送告警；完整机器人**计划中**。
- **安装。** Mistgate 的一行安装程序会在安装面板前验证面板签名清单。

## 完整对照表

| | Mistgate | 3x-ui | Marzban | PasarGuard | Remnawave | Hiddify Manager | s-ui |
|:--|:--|:--|:--|:--|:--|:--|:--|
| 后端 | Go | Go | Python、React | Python、React | NestJS（TypeScript） | Python | Go |
| 数据库 | SQLite | SQLite 或 PostgreSQL | SQLite、MySQL 或 MariaDB | SQLite、MySQL、MariaDB、PostgreSQL 或 TimescaleDB | PostgreSQL、Redis | MySQL、Redis | SQLite |
| 许可证 | AGPL-3.0 | GPL-3.0 | AGPL-3.0 | AGPL-3.0 | AGPL-3.0 | GPL-3.0 | GPL-3.0 |
| Docker | 不使用：systemd、静态二进制文件 | 可选 | 部分：安装程序使用 Compose；文档提供手动安装步骤 | 部分：Compose | 面板和节点都需要 | 可选 | 可选 |
| 一行安装程序 | 是 | 是 | 是 | 是 | 否：Compose、社群脚本 | 是 | 是 |
| 内存 | 面板：空闲时 16–20 MiB，实测¹；节点：SSH 安装程序需要 256 MB | 空闲时 68–74 MiB，实测¹ | ? | 空闲时 262–265 MiB，实测¹；文档说明：最低 1 GB，建议 2 GB | 空闲时 426–454 MiB，实测¹；文档说明：面板最低 2 GB，建议 4 GB；节点最低 1 GB | ? | ? |
| 核心 | Hysteria2；VLESS 使用 xray-core；AmneziaWG 使用用户态或内核 | Xray、进程内的 TUIC、AmneziaWG | Xray | Xray、WireGuard 核心；已宣布推出 sing-box 节点 | Xray | Xray 和 sing-box | sing-box |
| VLESS REALITY / XHTTP | 是 / 是 | 是 / 是 | 是 / ? | 是 / ? | 是 / 是 | 是 / 是 | ?（源代码中包含 VLESS） |
| VMess、Trojan、Shadowsocks | 否 | 是 | 是 | 是 | 部分：Trojan 和 Shadowsocks；未列出 VMess | 是 | 是（源代码中包含） |
| Hysteria2 | 是 | 是 | ? | 是 | 部分：仅在 Xray JSON 客户端配置中提供 | 是 | 是（源代码中包含） |
| TUIC | 否 | 是（v5） | ? | ? | ? | 是 | 是（源代码中包含） |
| WireGuard | 否 | 是 | ? | 是 | 部分：透传 | 是 | ? |
| AmneziaWG | 是：2.0 和 3.1，每台设备单独使用密钥 | 是：用户态的 3.1 | ? | ? | ? | ? | ? |
| 多个节点 | 是：Agent 使用双向 TLS | 是：主面板管理其他 3x-ui 面板 | 是：marzban-node | 是 | 是 | 是：父子面板 | ? |
| 添加节点的方式 | 从面板通过 SSH 添加，或使用一次性命令 | 手动：每个节点运行完整的 3x-ui 面板 | 手动 | 手动 | 手动：使用生成的 compose 文件 | 手动：使用子面板 | ? |
| 订阅格式 | URI 列表、Mihomo YAML、AmneziaVPN `vpn://` 密钥、网页；Xray JSON 和 sing-box **计划中** | base64、Xray JSON、Clash/Mihomo | V2ray、Clash、ClashMeta | V2ray、Clash、ClashMeta、sing-box、Outline | Xray JSON、base64、Mihomo、Stash、Clash、sing-box | sub、sub64、Xray、sing-box、Clash、ClashMeta、WireGuard | 链接、sing-box、Clash.Meta |
| 每用户流量与有效期 | 是 | 是 | 是 | 是 | 是 | 是 | 是 |
| 设备限制 | 部分：按设备数量限制，不按 HWID | 是：HWID 和 IP | ? | 是：HWID | 是：HWID | ? | ? |
| Telegram 机器人 | 部分：告警；完整机器人**计划中** | 是（也支持 Discord） | 是 | 是 | 部分：通知、Telegram 登录 | 是 | ? |
| API | 是：Connect、只读 / 操作员 / 管理员令牌 | 是：REST、范围受限的令牌 | 是：REST、webhook | 是：REST、webhook | 是：OpenAPI、范围受限的令牌、webhook | 是 | 是 |
| AI 智能体（MCP） | 是：内置，支持计划 / 执行和所有者审批 | ? | ? | ? | 否：没有官方服务器；社群提供了相关服务器 | ? | ? |
| WARP 出口 | 是：按协议配置设置，故障时停止转发（不回退直连） | 是 | ? | 部分：提供指南 | 部分：社群方案 | 是 | 是（源代码中包含） |
| 运行状况和告警 | 是：客户端视角检测、带修复的节点诊断、告警生命周期、Telegram | 是：Telegram 告警、可选的隧道监视器 | 部分 | 部分 | 是：状态、Prometheus、流量告警 | 部分 | ? |
| BT 下载拦截 | 是：在内核中拦截；事件会记录匹配内容和目标端口，不记录地址 | ? | ? | ? | 是：通过插件实现 | ? | ? |
| 伪装站点 | 是 | ? | ? | ? | ? | ? | ? |
| 签名发布版本 | 是：使用离线密钥，可复现构建，节点只安装签名版本 | 部分：`.sha256` 文件 | ? | ? | ? | ? | ? |
| Cloudflare Workers 版 | 预览：[尚未在真实 Cloudflare 账户上运行](../getting-started/install-edge.md) | 否 | 否 | 否 | 否 | 否 | 否 |
| GitHub 星标数 | 1 | 约 47.7k | 约 7.4k | 约 2.7k | 约 5.2k | 约 9.3k | 约 10.0k |
| 最新版本 | v0.1.32 | v3.9.0（2026-10-03） | v0.8.4（2025-01-09） | v5.4.1（2026-09-12） | 3.4.5（2026-10-06） | v13.0.3（2026-09-26） | v1.6.4（2026-10-07） |

¹ 本项目在一台测试机上测量：1 vCPU / 1 GB 服务器上，面板进程组的匿名私有内存；空闲状态、0 到 200 个用户、没有节点连接（3x-ui 包含其 Xray 核心和 fail2ban）。未测量 Marzban、Hiddify Manager 和 s-ui。

在运行 VPN 核心、数据库并承载 Hysteria2 流量的 2 vCPU / 2 GB 服务器上，Mistgate（面板和节点）空闲时占用 57 MiB，在「高负载日」占用 80 MiB（64 条隧道流、每秒 300 个订阅请求、每秒添加一个用户并移除一个用户）；3x-ui 的对应数据为 74 和 129 MiB，PasarGuard 为 297 和 347 MiB，Remnawave 为 761 和 1,114 MiB。测量方法、版本以及 Mistgate 较慢的部分见[基准测试](benchmarks.md)。端到端测试会检查面板空闲内存是否低于 80 MB，以及节点 Agent 是否低于 100 MB。

Marzban 目前只接受维护（上次发布于 2025 年 1 月）；其用户正在转向 PasarGuard。PasarGuard 是在独立仓库中开发的后继项目，并非 GitHub 分支。Marzneshin 已停止活跃开发。

## 来源

于 2026-10-09 核对。检查了各项目的 README、文档和发布说明，以及当天从 GitHub API 获取的星标数和最新版本。

| 项目 | 仓库 | 文档 |
|:--|:--|:--|
| Mistgate | [Mistgate/mistgate](https://github.com/Mistgate/mistgate) | 本文档 |
| 3x-ui | [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) | [docs.sanaei.dev](https://docs.sanaei.dev/) |
| Marzban | [Gozargah/Marzban](https://github.com/Gozargah/Marzban) | [gozargah.github.io/marzban](https://gozargah.github.io/marzban/) |
| PasarGuard | [PasarGuard/panel](https://github.com/PasarGuard/panel) | [docs.pasarguard.org](https://docs.pasarguard.org) |
| Remnawave | [remnawave/panel](https://github.com/remnawave/panel) | [docs.rw](https://docs.rw) |
| Hiddify Manager | [hiddify/Hiddify-Manager](https://github.com/hiddify/Hiddify-Manager) | [hiddify.com](https://hiddify.com) |
| s-ui | [alireza0/s-ui](https://github.com/alireza0/s-ui) | [the wiki](https://github.com/alireza0/s-ui/wiki) |




