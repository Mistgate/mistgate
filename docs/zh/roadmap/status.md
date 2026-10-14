---
title: 状态和路线图
description: 了解 Mistgate 已发布版本的功能、当前测试内容、后续计划、1.0 发布条件，以及当前版本的已知限制。
---

Mistgate 目前处于**测试版（Beta）**；当前发布版本为 `v0.1.40`。它已用于生产环境，但在 1.0 之前，API、存储设置和节点协议仍可能发生变化。本文档的其他部分仅描述当前代码的实际行为；计划集中在本页。计划没有日期：「下一步」和「以后」表示顺序，不代表时间安排。

```roadmap
- status: released
  title: 已发布
  meta: v0.1.0 – v0.1.40
  lead: 以下功能已包含在发布版本和文档中。先介绍当前版本带来的内容，再按管理后台的分区介绍其他内容。
  cards:
    - title: 当前版本的新内容
      icon: sparkle
      release: v0.1.40
      current: true
      points:
        - "**Xray JSON 与 sing-box JSON**，与链接列表和 Mihomo YAML 并列。Xray JSON 只按你添加的规则下发，例如 `v2rayng`；面板会获得规则 `sing-box` → sing-box JSON，供官方 sing-box 应用使用。参见 [Xray JSON 与 sing-box JSON](../guide/subscriptions.md#xray-json-与-sing-box-json)。"
        - "每张协议配置卡片和编辑器中都有**订阅格式**：服务器被某种格式排除时，标记会说明原因。参见[协议配置的订阅格式](../guide/profiles.md#协议配置的订阅格式)。"
        - "链接末尾的 `?format=` 可为单次请求指定格式，便于在写规则前先试用应用。参见[下发规则](../guide/subscriptions.md#下发规则)。"
        - "**Happ 6** 显示为 Happ 6.0.0，后台的设备行以系统开头。"
    - title: 协议配置
      section: profiles
      points:
        - "[Hysteria2](../guide/hysteria2.md) 使用官方核心和 Salamander 混淆。"
        - "[AmneziaWG](../guide/amneziawg.md) 2.0 和 3.1，可运行于用户态或内核模块；每个节点可配置多个协议配置。"
        - "[VLESS](../guide/vless.md) 使用 xray-core：REALITY 可通过 XHTTP 或 TCP（RAW + Vision）运行，目标可以是节点自己的站点或其他站点；TLS 可通过 XHTTP 或 WebSocket 运行，支持直连或通过 CDN。"
        - "[同一节点上运行 VLESS 和 Hysteria2](../guide/vless-and-hysteria2.md)，包括 TCP 443。"
    - title: 节点
      section: nodes
      points:
        - "[通过 SSH 安装节点](../getting-started/ssh-install.md)，也可以通过 MCP 在所有者审批后安装：固定主机密钥，在更改前检查，安装任务可取消、重试，并能在中断后继续。"
        - "加密保存 SSH 凭据，并验证密码更改。"
        - "[WARP 出口](../guide/warp.md)在 WARP 连接中断时会停止转发；所有节点都支持[BT 下载拦截](../guide/torrent-protection.md)。"
        - "[检测端口](../guide/nodes.md#端口)：从集群中的另一节点检测节点每个端口的 UDP 可达性和 TCP 连接。"
        - "[测量节点网络带宽](../guide/nodes.md#测量网络带宽)。"
        - "[每个节点的伪装站点](../guide/decoy-site.md#节点三种选项)：使用内置站点、面板站点或自己的站点压缩包。"
    - title: 用户
      section: users
      points:
        - "[用户和分组](../guide/users-and-groups.md)：支持按周期统计流量配额、有效期、[按设备 ID 区分的设备](../guide/users-and-groups.md#设备和设备上限)（设备上限有三种模式），以及每用户和每设备限速。"
        - "用户标签页可按任意列排序并显示设备数，支持一次对多个用户执行批量操作。"
    - title: 订阅
      section: subscriptions
      points:
        - "每人一个[订阅链接](../guide/subscriptions.md)：为 Happ 等应用提供链接列表，为使用 mihomo 内核的应用提供 Mihomo YAML，为使用 Xray 和 sing-box 内核的应用提供 [Xray JSON 与 sing-box JSON](../guide/subscriptions.md#xray-json-与-sing-box-json)。"
        - "[订阅类应用的设备上限](../guide/subscriptions.md#订阅类应用的设备上限)有三种模式：**仅计数**、**限制**和**严格**。"
        - "[用户页面](../guide/user-page.md)：按平台提供说明、二维码、供 AmneziaVPN 使用的 `vpn://` 密钥，以及用户自己的设备；用户可以[重命名和删除](../guide/user-page.md#自助管理设备)这些设备，页面上还有复制链接的按钮。"
        - "提供 [DNS 预设](../guide/dns.md)，并可让用户在用户页面中为每台服务器选择 DNS。"
    - title: 运行状况
      section: health
      points:
        - "[客户端视角检测](../operations/health.md#客户端视角检测)：面板每 5 分钟像应用一样连接到服务器，包括 VLESS（REALITY 和 TLS，直连或通过 CDN）。"
        - "[节点诊断](../operations/health.md#节点诊断)：检查主机并安全修复问题，其中[**刷新服务单元**](../operations/health.md#修复)可在管理后台中重写 Agent 的 systemd 单元。"
        - "在管理后台和 Telegram 中接收[告警](../operations/health.md#告警)。"
        - "[面板监视器](../operations/panel-watcher.md)：Cloudflare Worker 每分钟检查一次面板，并在其无响应时向 Telegram 发送消息。"
    - title: 更新
      section: updates
      points:
        - "[更新内容](../operations/updates.md#更新内容)：显示你当前版本与可用版本之间的发布说明，语言与界面一致。"
        - "[签名的节点 Agent 更新](../operations/updates.md)：一次更新一个节点，可立即更新或定时更新；更新后检查失败时会自动回滚。"
        - "面板可从已签名的 GitHub 发布版本自我更新；新面板无法持续运行时会自动回滚。"
        - "[单行命令安装面板](../getting-started/install-panel.md#一行安装)，并验证发布签名。"
        - "[可复现发布版本](../operations/releases.md)，使用标签离线签名。"
    - title: 集成
      section: integrations
      points:
        - "[API 令牌](../reference/api.md)支持只读、操作员和管理员权限级别。"
        - "[MCP 服务器](../reference/mcp.md)支持计划 / 执行和所有者审批；[AI 智能体指南](../getting-started/ai-agents.md)提供可直接使用的提示词。"
        - "[Telegram 告警](../operations/telegram-alerts.md)会发送给关联聊天的管理员：包括运行状况、发布版本、备份失败、等待审批的计划和登录安全事件。"
    - title: 设置
      section: settings
      points:
        - "管理后台、用户页面、节点安装页面、Telegram 消息和本文档提供英语、俄语和简体中文；支持使用通行密钥或密码加身份验证器验证码登录，可选 Cloudflare Turnstile，并提供审计日志。参见[安全](../operations/security.md)。"
        - "[伪装站点](../guide/decoy-site.md)会显示在面板地址上，向陌生人隐藏管理后台。"
        - "支持定时将[加密备份](../operations/backups.md)保存到 Cloudflare R2、设置保留期限，并离线恢复。"
- status: now
  title: 进行中
  meta: v0.1.34 – v0.1.40
  lead: 最新发布版本添加的功能正在生产环境中测试；测试结果用于改进功能和修正文档。
  cards:
    - title: 在生产环境中测试 VLESS
      section: profiles
      text: "v0.1.34 起支持 REALITY，v0.1.35 起支持以节点自己的站点为目标，v0.1.36 起支持 TLS 和 CDN。尚未通过真实 CDN 测试 VLESS：端到端测试中由节点自身模拟 CDN。参见 [VLESS 通过 CDN 接入](../guide/vless.md#vless-经-cdn-连接)。"
    - title: 在生产环境中测试按设备 ID 区分的设备
      section: subscriptions
      text: "现有面板运行在**仅计数**模式，因此你可以在切换到**限制**之前先看到谁超出了上限。参见[如何从「仅计数」切换到「限制」](../guide/subscriptions.md#如何从仅计数切换到限制)。"
    - title: 在生产环境中测试 Xray JSON 与 sing-box JSON
      section: subscriptions
      text: "默认没有规则下发 Xray JSON：请为需要的应用添加规则，或先在其链接末尾加上 `?format=xray-json` 试用。参见[下发规则](../guide/subscriptions.md#下发规则)。"
    - title: 在每个节点上使用伪装站点
      section: nodes
      text: "每个节点都可以使用内置站点、面板站点或自己的站点压缩包。v0.1.36 之前创建的节点会继续使用内置页面，直到你选择其他选项。参见[自己的伪装站点](../guide/decoy-site.md#节点三种选项)。"
- status: next
  title: 下一步
  lead: 队列中的下一批计划。
  cards:
    - title: 在真实账户上运行 Cloudflare 版
      icon: globe
      tag: 预览版
      text: "首次在真实 Cloudflare 账户上运行 `mistgate edge deploy`。目前只在模拟的 Cloudflare 环境中测试过命令和面板。参见[安装 Cloudflare 版](../getting-started/install-edge.md)。"
    - title: 让 Cloudflare 版具备与 VPS 版相同的功能
      icon: layers
      text: "补齐目前尚未提供的功能："
      points:
        - "加密备份和恢复；"
        - "Telegram 告警；"
        - "MCP 和新 API 令牌；"
        - "客户端视角隧道检测；"
        - "从管理后台更新节点和面板；"
        - "实时节点日志、节点自有站点伪装以及 WARP 注册。"
- status: later
  title: 以后
  lead: 已计划，但尚未进入下一批队列。
  cards:
    - title: 提供更多功能的 Telegram 机器人
      section: integrations
      text: "目前机器人只发送告警并关联管理员聊天。未来计划：通过 Telegram 管理集群，并为用户提供机器人。参见 [Telegram 告警](../operations/telegram-alerts.md)。"
    - title: 订阅镜像
      section: subscriptions
      text: "在其他地址保存订阅副本，以便面板地址无法连接时，应用仍能获取订阅。"
    - title: 在 VPS 版和 Cloudflare 版之间迁移
      icon: refresh
      text: "在两个版本之间迁移用户、节点和订阅链接，同时保持链接可用。目前要使用另一个版本，需要重新配置集群。参见[迁移](../getting-started/install-edge.md#在-vps-面板和-cloudflare-版之间迁移)。"
- status: goal
  title: 迈向 1.0
  lead: 1.0 的发布条件。达到这些条件前仍可能有不兼容变更。
  cards:
    - title: 固定节点协议和存储设置
      text: "不同 1.x 版本的面板和 Agent 可以互相理解；面板数据可以在不同版本间迁移，无需手动操作。"
    - title: 测试旧版本升级
      text: "每个发布版本都通过自动升级测试：从旧版本升级包含数据且连接了节点的面板。"
    - title: 稳定的 API
      text: "1.x 版本内的 API、API 令牌和 MCP 工具不会发生不兼容变更。参见 [API](../reference/api.md) 和 [MCP](../reference/mcp.md)。"
```

## 已知限制

- 如果节点 Agent 没有报告所需能力，客户端视角检测会跳过 VLESS。v0.1.34 之前的 Agent 没有 VLESS 引擎；TLS 协议配置还需要 `vless-tls/1`（v0.1.36 或更高版本）。
- **检测端口**只从一个位置检测：集群中的另一节点或面板主机。对于 VLESS 端口，它会使用预期名称执行 TLS 握手，不会执行 REALITY 握手。
- SSH 安装只支持密码登录（包括 keyboard-interactive），不支持密钥登录。此类服务器请使用[手动安装](../getting-started/add-node.md)。
- API 令牌和 MCP 无法创建协议配置或分组，也无法将协议配置部署到节点；这些操作由所有者在管理后台执行。
- 备份只能保存到 Cloudflare R2；恢复时必须离线运行 `mistgate backup restore` 命令，并指定新的数据目录。
- 面板不会创建 Cloudflare 账户、存储桶或令牌；你需要提供一个仅限存储桶且具有读取、写入和删除权限的令牌。
- Cloudflare 版仍处于预览阶段，尚未提供部分功能（参见[列表](../getting-started/install-edge.md#此版本中尚不可用的功能)），也无法在 Cloudflare 版和 VPS 面板之间迁移集群。
- 设备 ID 由应用自己报告。拉黑会拒绝该 ID；报告其他 ID 的应用算作新设备，并占用一个空闲名额。要让被转给他人的链接失效，还需要重新签发一个新链接。
- **每用户限速**按节点计算，因此同时连接两个节点的用户最多可获得上限的 2 倍。v0.1.39 之前的节点 Agent 会把每台设备限制在两个限速中较小的一个。
- 在 Cloudflare 版中，面板记录的已发送到节点的内容摘要必须放进 D1 的一行（2 MB），约合每个节点 38,000 个凭据。参见[此版本的限制](../getting-started/install-edge.md#此版本中尚不可用的功能)。
- sing-box JSON 没有 XHTTP，也无法按指纹校验自签名的 Hysteria2 证书；Gecko 和 AmneziaWG 只通过 Mihomo YAML 下发。格式无法承载的服务器不会出现在该格式中。参见[各格式支持哪些协议配置](../guide/subscriptions.md#各格式支持哪些协议配置)。
- 部分默认值偏向俄罗斯用户（俄罗斯节点使用 Yandex DNS、诊断所用的检测域名、DNS 分流预设）；这些都可以在设置中修改。
