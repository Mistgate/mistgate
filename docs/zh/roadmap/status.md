---
title: 状态和路线图
description: 了解 Mistgate 已发布版本的功能、当前测试内容、后续计划、1.0 发布条件，以及当前版本的已知限制。
---

Mistgate 目前处于**测试版（Beta）**；当前发布版本为 `v0.1.85`。它已用于生产环境，但在 1.0 之前，API、存储设置和节点协议仍可能发生变化。本文档的其他部分仅描述当前代码的实际行为；计划集中在本页。计划没有日期：「下一步」和「以后」表示顺序，不代表时间安排。

```roadmap
- status: released
  title: 已发布
  meta: v0.1.0 – v0.1.85
  lead: 以下功能已包含在发布版本和文档中。先介绍当前版本带来的内容，再按管理后台的分区介绍其他内容。
  cards:
    - title: 当前版本的新内容
      icon: tag
      release: v0.1.85
      current: true
      points:
        - "**更新页面分为三层，并显示服务器的操作系统。** 顶部三张卡片：**此面板**、**节点 Agent** 和**服务器系统**。新的一层只读：发行版、内核、等待安装的更新和安全更新、是否需要重启，以及 AmneziaWG 模块是否已为新内核构建；暂时还不能从面板安装更新或重启。需要 Agent v0.1.85。参见[服务器操作系统](../operations/updates.md#服务器操作系统)。"
        - "**续传更新下载时立即占用名额。** 节点与面板的连接停滞时，续传曾要等待并被拒绝，在差的线路上会白白损失时间窗口中的数分钟。现在同一文件的续传会立即占用该节点的名额。参见[发布包的来源](../operations/updates.md#发布包的来源)。"
        - "**GitHub 作为备用更新来源。** 节点可以依次从多个来源完成更新下载；当 GitHub 上的发布由面板自己的密钥签名且文件相同时，面板会把 GitHub 作为第二来源提供。默认开启，可在**设置 → 系统 → 下载来源**中关闭；GitHub 能看到节点的地址。需要 Agent v0.1.85。参见[下载来源](../operations/updates.md#下载来源)。"
        - "**API 令牌的每小时用户更改限额。** 未经所有者的令牌每小时最多可收回或改写 10 人的访问，因此泄露的令牌无法一次停用所有人；创建、延期和启用用户不计入。所有者可用令牌上的开关解除限额；更新后，已有的令牌都受此限额约束。参见[限制](../reference/api.md#限制)。"
        - "**隧道内可访问节点自己的端口。** AmneziaWG 全隧道客户端现在可以访问同一节点上其他已发布的入站（Telegram 代理、其他端口上的 VLESS 和 Hysteria2），而不只是 TCP 443。需要 Agent v0.1.85。参见[故障排查](../operations/troubleshooting.md#vpn-开启后同一台服务器上的代理无法连接)。"
        - "**为 Mistgate 自有应用做的准备。** 面板新增了客户端 API：所有者开关、凭密钥注册设备、同步和退出。它默认关闭，现有应用不受任何影响。"
        - "**安全：我们的监听端口不再使用 Multipath TCP（v0.1.83）。** Go 默认以 MPTCP 打开每个监听端口，而 CVE-2026-98432（Linux 内核 TCP 中的漏洞，CVSS 9.8）已被证明可通过 MPTCP 监听端口远程获取 root。面板和节点 Agent（及其承载的 Xray 端口）现在只打开普通 TCP；请更新面板和节点。漏洞本身在内核中：另请设置 `net.mptcp.enabled=0`，并在发行版发布修补后的内核时安装。参见[故障排查](../operations/troubleshooting.md#multipath-tcp-与-cve-2026-98432)。"
    - title: 协议配置
      section: profiles
      points:
        - "[Hysteria2](../guide/hysteria2.md) 使用官方核心和 Salamander 混淆。"
        - "[AmneziaWG](../guide/amneziawg.md) 2.0 和 3.1，可运行于用户态或内核模块；每个节点可配置多个协议配置。"
        - "[VLESS](../guide/vless.md) 使用 xray-core：REALITY 可通过 XHTTP 或 TCP（RAW + Vision）运行，目标可以是节点自己的站点或其他站点；TLS 可通过 XHTTP 或 WebSocket 运行，支持直连或通过 CDN。"
        - "[同一节点上运行 VLESS 和 Hysteria2](../guide/vless-and-hysteria2.md)，包括 TCP 443。"
        - "[MTProto](../guide/mtproto.md)，实验性：带 Fake TLS 的 Telegram 应用代理，每人一个密钥。"
    - title: 节点
      section: nodes
      points:
        - "[通过 SSH 安装节点](../getting-started/ssh-install.md)，也可以通过 MCP 在所有者审批后安装：固定主机密钥，在更改前检查，安装任务可取消、重试，并能在中断后继续。"
        - "[用命令注册节点](../operations/troubleshooting.md#注册失败)：`mistgate-node enroll` 会解释 TLS 握手被拒绝的原因（面板前有 CDN 或反向代理）并指出解决问题的参数。"
        - "加密保存 SSH 凭据，并验证密码更改；面板自己的 SSH 密钥，在节点卡片中管理服务器的 `authorized_keys`，并为通过命令安装的节点连接访问。"
        - "[WARP 出口](../guide/warp.md)在 WARP 连接中断时会停止转发；所有节点都支持[BT 下载拦截](../guide/torrent-protection.md)。"
        - "[检测端口](../guide/nodes.md#端口)：从集群中的另一节点检测节点每个端口的 UDP 可达性和 TCP 连接。"
        - "[测量节点网络带宽](../guide/nodes.md#测量网络带宽)。"
        - "[每个节点的伪装站点](../guide/decoy-site.md#节点三种选项)：使用内置站点、面板站点或自己的站点压缩包。"
        - "[通过入口节点中转](../guide/relays.md)：启用前检查，每 5 分钟监测一次，订阅中提供该服务器的副本。"
        - "[IPv6 入口](../guide/nodes.md#ipv6-入口)：节点的服务器使用其某个 IPv6 地址，开启前从外部检测。"
        - "[资源与负载](../guide/nodes.md#负载)：节点和 Mistgate 自身占用多少服务器资源、显示还能容纳多少人的**负载**视图、单个节点的[调试抓取](../operations/troubleshooting.md#单个节点的调试抓取)，以及集群地图和图形化的入口。"
    - title: 用户
      section: users
      points:
        - "[用户和分组](../guide/users-and-groups.md)：支持按周期统计流量配额、有效期、[按设备 ID 区分的设备](../guide/users-and-groups.md#设备和设备上限)（设备上限有三种模式），以及每用户和每设备限速；分组还决定成员可以使用哪些节点。"
        - "用户标签页可按任意列排序并显示设备数，支持一次对多个用户执行批量操作。"
    - title: 订阅
      section: subscriptions
      points:
        - "每人一个[订阅链接](../guide/subscriptions.md)：为 Happ 等应用提供链接列表，为使用 mihomo 内核的应用提供 Mihomo YAML，为使用 Xray 和 sing-box 内核的应用提供 [Xray JSON 与 sing-box JSON](../guide/subscriptions.md#xray-json-与-sing-box-json)。"
        - "[订阅类应用的设备上限](../guide/subscriptions.md#订阅类应用的设备上限)有三种模式：**仅计数**、**限制**和**严格**。"
        - "[用户页面](../guide/user-page.md)：按平台提供说明、二维码、供 AmneziaVPN 使用的 `vpn://` 密钥，以及用户自己的设备；用户可以[重命名和删除](../guide/user-page.md#自助管理设备)这些设备，页面上还有复制链接的按钮。"
        - "提供 [DNS 预设](../guide/dns.md)，可用国家列表设置「直连」（中国：国内网站直连），以及[在应用中拦截广告](../guide/dns.md#广告和跟踪器)；用户可以在用户页面中为订阅类应用选择 DNS，并为密钥按服务器选择 DNS。"
        - "[自动选择服务器](../guide/subscriptions.md#自动选择服务器)：Mihomo 和 sing-box 配置中的 **Auto** 组以及 Xray JSON 中的 `Auto` 配置，让应用自动切换到可用的服务器。"
    - title: 运行状况
      section: health
      points:
        - "[客户端视角检测](../operations/health.md#客户端视角检测)：面板每 5 分钟像应用一样连接到服务器，包括 VLESS（REALITY 和 TLS，直连或通过 CDN）。"
        - "来自节点的[连接计数](../guide/nodes.md#协议配置)：连接次数、连接后立即断开、被拒绝的密钥、未完成的握手、在线时长和未知密钥，在用户页面按可归属的设备显示，在节点上按协议配置显示。统计 Hysteria2 和 AmneziaWG 的事件计数，并统计 VLESS 会话时长以及（v0.1.79 及以上版本 Agent 的）未知密钥；Hysteria2 还会记录连接如何结束（路径**冻结**、客户端**静默**或主动关闭）以及延迟和丢包；只有计数，保留 7 天（168 小时）。"
        - "[节点运行记录](../guide/nodes.md#运行记录)：每个节点 Agent 及其引擎 7 天内的警告和错误，不含客户端地址，所有管理员均可查看，也可通过 MCP 获取。"
        - "[节点诊断](../operations/health.md#节点诊断)：检查主机并安全修复问题，其中[**刷新服务单元**](../operations/health.md#修复)可在管理后台中重写 Agent 的 systemd 单元；服务器关闭自动安全更新时的警告，以及 Debian 和 Ubuntu 上的修复**开启安全更新**。"
        - "[可达性](../operations/reachability.md)：独立的部分，显示每个节点最近一周的情况；通过 Globalping 使用俄罗斯境内的探测点，使用 cheburcheck 的志愿者探测点和 bsbord 的移动 SIM 探测点（包括克里米亚，设有每月上限），比对 antifilter 封锁名单，从你在俄罗斯的节点运行 TSPU 探测，并接收 kl!ck 用户从自己网络发来的报告；每个来源单独开启，默认关闭；节点从该国无法连接时会打开告警；节点的 IPv6 可用时，也会按其 IPv6 地址检测。"
        - "[通过已建立隧道的响应时间与单独显示的连接时间](../operations/health.md#数字的含义)，以及[面板主机丢失 UDP 时的提示](../operations/health.md#面板服务器丢失-udp-时)。"
        - "在管理后台和 Telegram 中接收[告警](../operations/health.md#告警)。"
        - "[面板监视器](../operations/panel-watcher.md)：Cloudflare Worker 每分钟检查一次面板，并在其无响应时向 Telegram 发送消息。"
        - "[关闭告警](../operations/health.md#关闭)直到问题消失，以及节点诊断的 [21 项检查](../operations/health.md#检查项目)，其中包括 CPU steal、连接跟踪和 Agent 打开的文件。"
    - title: 更新
      section: updates
      points:
        - "[更新内容](../operations/updates.md#更新内容)：显示你当前版本与可用版本之间的发布说明，语言与界面一致。"
        - "[签名的节点 Agent 更新](../operations/updates.md)：一次更新一个节点，可立即更新或定时更新；更新后检查失败时会自动回滚。"
        - "面板可从已签名的 GitHub 发布版本自我更新；新面板无法持续运行时会自动回滚。"
        - "面板及其节点的[发布渠道](../operations/updates.md#发布渠道)（稳定版、测试版、开发版），以及面板自我更新前提供的备份。"
        - "[单行命令安装面板](../getting-started/install-panel.md#一行安装)，并验证发布签名。"
        - "[可复现发布版本](../operations/releases.md)，使用标签离线签名。"
        - "[适用于慢速链路的下载窗口](../operations/updates.md#单节点更新过程)，默认 30 分钟，并在滚动更新中显示每个节点的下载进度。"
    - title: 集成
      section: integrations
      points:
        - "[API 令牌](../reference/api.md)支持只读、操作员和管理员权限级别。"
        - "[MCP 服务器](../reference/mcp.md)支持计划 / 执行和所有者审批，现已包含日常更改、一次调用的读取、节点之间的探测、诊断和计划任务；[AI 智能体指南](../getting-started/ai-agents.md)提供可直接使用的提示词。"
        - "[Telegram 告警](../operations/telegram-alerts.md)会发送给关联聊天的管理员：包括运行状况、发布版本、备份失败、等待审批的计划和登录安全事件。"
        - "[助手](../guide/assistant.md)：与云端模型聊天并以卡片形式准备更改，含[定时回访](../guide/assistant.md#定时回访)和[计划任务](../guide/assistant.md#计划任务)。"
    - title: 设置
      section: settings
      points:
        - "管理后台、用户页面、节点安装页面、Telegram 消息和本文档提供英语、俄语和简体中文，管理后台和用户页面还提供波斯语和土耳其语；支持使用通行密钥或密码加身份验证器验证码登录，可选 Cloudflare Turnstile，并提供审计日志。参见[安全](../operations/security.md)。"
        - "代码审计（v0.1.84）检查了登录、订阅、节点连接、MCP 和助手；发现的问题均已修复。参见[安全](../operations/security.md)。"
        - "[伪装站点](../guide/decoy-site.md)会显示在面板地址上，向陌生人隐藏管理后台。"
        - "支持定时将[加密备份](../operations/backups.md)保存到 Cloudflare R2 并设置保留期限，或保存为所有者电脑上的文件；可在面板中恢复，或用安装程序在新服务器上恢复。"
        - "所有者可用的[面板日志、保留时间、导出和指标](../operations/troubleshooting.md#面板日志保留时间导出和指标)，以及可选的出现动画（测试版）。"
- status: now
  title: 进行中
  meta: v0.1.34 – v0.1.81
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
    - title: 在生产环境中测试国家视角检测
      section: health
      text: "所有来源默认关闭。请先开启其中一个，观察「从俄罗斯访问」列，再决定是否依赖它的告警：Globalping 探测点不属于面板，而 `listed`（在名录中）只是名录中的标记，不是实测结果。参见[可达性](../operations/reachability.md)。"
    - title: 在生产环境中使用中转
      section: nodes
      text: "v0.1.65 新增。面板从境外检测中转，因此只有境内用户才会遇到的卡顿会出现在 kl!ck 报告和 bsbord 中，而不是面板自己的检测里。参见 [中转](../guide/relays.md)。"
    - title: 在生产环境中使用 IPv6 入口
      section: nodes
      text: "v0.1.66 新增，管理后台中的卡片自 v0.1.67 起提供。只适用于主机商那边 IPv6 正常的节点。境内用户能否经由它连接，会最先体现在 kl!ck 报告中。参见 [IPv6 入口](../guide/nodes.md#ipv6-入口)。"
    - title: 在生产环境中使用设备端广告拦截
      section: subscriptions
      text: "v0.1.67 新增。Happ 使用它自带 geo 文件中的列表，默认来自 Loyalsoldier，其中约有 195,000 个域名：在 iPhone 上确认 Happ 使用该预设能正常运行后，再把它给更多人。参见 [广告和跟踪器](../guide/dns.md#广告和跟踪器)。"
    - title: 在生产环境中使用安全更新修复
      section: health
      text: "v0.1.67 新增。它会在服务器上安装软件包：先在一个节点上使用，查看那里的诊断结果，再用于其他节点。参见 [修复](../operations/health.md#修复)。"
    - title: 在生产环境中使用分组可用节点
      section: users
      text: "v0.1.68 新增。为分组关闭某个节点会立即对组内所有人生效，即使某人的用户卡片上也勾选了该节点，该节点上的协议配置也不再提供给他。参见 [用户与分组](../guide/users-and-groups.md#分组标签页)。"
    - title: 在生产环境中使用 MTProto
      section: profiles
      text: "v0.1.69 新增，实验性。它尚未在真实封锁环境中验证过：先把链接给一两个人，确认 Telegram 能从他们的网络连上，再大范围分发。参见 [MTProto](../guide/mtproto.md)。"
    - title: 在生产环境中使用助手
      section: integrations
      text: "v0.1.81 新增。它会把对话和工具读取的内容发给你选择的提供商，所以连接提供商之前请先看清它的数据去向，并设置月度预算。先在**谁可以使用**中选择**仅我自己**，按下**应用**之前先读一遍计划卡片的措辞。参见[提供商与数据去向](../guide/assistant.md#提供商与数据去向)。"
    - title: 在生产环境中测试封锁诊断
      section: health
      text: "v0.1.81 新增。`block_diagnosis` 和 `access_report` 对节点的计数和国家视角检测应用固定规则；两种原因看起来一样时，它们会如实说明，并标明低置信度。每次发布涉及诊断的版本之前，模拟封锁实验环境都会检验其中七类（地址、端口、UDP、TCP 冻结、白名单、单一运营商、节点故障）；协议识别和地区只在人为构造的计数上测试过。握手计数需要 Agent v0.1.81。迁移用户之前，请先读一遍它列出的依据。参见[助手](../guide/assistant.md#排查哪些连不上)。"
- status: next
  title: 下一步
  lead: 队列中的下一批计划。
  cards:
    - title: Mistgate 客户端
      icon: monitor
      text: "我们自己的桌面应用，先做 Windows 版。它直接与面板通信，而不只是通过订阅链接；自己支持 Hysteria2 和 AmneziaWG，内置 TSPU 及其他封锁的检测器，并在用户同意后把调试报告发送到面板。面板侧已就绪且默认关闭：所有者在**订阅 → 下发规则**中开启后，应用即可凭密钥注册设备并直接获取服务器；现有应用不受任何影响。应用本身尚未发布，在此之前用户继续使用现有应用连接：参见[客户端应用](../guide/client-apps.md)。"
    - title: 从另一个节点分发更新
      section: updates
      text: "与面板连接较差的节点，从舰队中已下载好更新文件的相邻节点获取该文件。目前备用来源只有 GitHub。参见[下载来源](../operations/updates.md#下载来源)。"
    - title: 连接来自哪个运营商
      section: reachability
      text: "按小时统计每个连接所属的网络（ASN），绝不记录地址：按运营商汇总（默认开启）和按人记录的模式（默认关闭），都保留 7 天。这样诊断能区分不同运营商。目前面板只能从国家检测的探测点和 kl!ck 用户的报告中得知运营商。参见[助手](../guide/assistant.md#排查哪些连不上)。"
    - title: 一键更换 IPv6 地址
      section: nodes
      text: "IPv6 入口和通过 IPv6 的可达性检测自 v0.1.65 起已在发布版本中。节点 Agent 已能在服务器的出口网卡上保留一个额外地址；缺少的是面板这一半：一键用服务器自己 /64 网段中的新地址替换入口地址。参见 [IPv6 入口](../guide/nodes.md#ipv6-入口)。"
    - title: 你的用户在哪里
      section: settings
      text: "首次设置和设置中的地区：DNS 预设、直连列表、推荐应用和可达性检测都跟随你选择的国家。与中国相关的部分已经有了（v0.1.64 起「国内网站直连」；v0.1.78 起节点诊断中的 AliDNS、DNSPod 和百度检查）；地区设置本身还没有。"
    - title: 看起来像普通自托管服务的伪装站
      section: settings
      text: "内置的伪装站：状态页、文件分享或某个服务的登录页样式，每个安装都不同，包括 404、429 和 robots.txt 的响应，扫描器无法识别；在那里登录总是失败，也不会保存任何内容。"
    - title: 让 Cloudflare 版具备与 VPS 版相同的功能
      icon: layers
      text: "为六个 Worker 的版本补齐目前尚未提供的功能："
      points:
        - "加密备份和恢复；"
        - "MCP、助手、计划任务和新 API 令牌；"
        - "探测、调试抓取、面板日志和负载视图；"
        - "客户端视角隧道检测；"
        - "从管理后台更新面板；"
        - "实时节点日志和节点自有站点伪装。"
    - title: 通过 cloud-init 安装
      section: settings
      text: "从云服务器的 user-data 无人值守地安装面板和节点，并提供常见云平台的现成示例。"
- status: later
  title: 以后
  lead: 已计划，但尚未进入下一批队列。
  cards:
    - title: 社区维护的路由列表
      section: subscriptions
      text: "由社区维护的现成规则集（俄罗斯：完整的本地网站直连列表，以及移动网络白名单模式），会自动更新。"
    - title: 分流规则中可用任意 geosite 和 geoip 类别
      section: subscriptions
      text: "分流规则编辑器会提示列表中的所有类别，而不只是国家。"
    - title: 按节点选择协议内核
      section: updates
      text: "协议内核（Xray、Hysteria2）内置于节点 Agent 中，因此目前节点运行的是其 Agent 构建时所带的内核。如果某个内核将来的改动使旧客户端无法连接，且无法在服务器端兼容，面板将提供两个 Agent 构建版本（旧内核与新内核），并允许为每个节点选择其一，使用同样的签名发布与回滚流程。在此之前，新内核会带着旧客户端所需的兼容性发布。"
    - title: 更多协议
      section: profiles
      text: "候选：TUIC、NaiveProxy 和 MASQUE。每个都通过协议接口接入同一个订阅链接。"
    - title: 提供更多功能的 Telegram 机器人
      section: integrations
      text: "目前机器人只发送告警并关联管理员聊天。未来计划：通过 Telegram 管理集群，并为用户提供机器人。参见 [Telegram 告警](../operations/telegram-alerts.md)。"
    - title: 订阅镜像
      section: subscriptions
      text: "在其他地址保存订阅副本，以便面板地址无法连接时，应用仍能获取订阅。"
    - title: 在 VPS 版和 Cloudflare 版之间迁移
      icon: refresh
      text: "在两个版本之间迁移用户、节点和订阅链接，同时保持链接可用。目前要使用另一个版本，需要重新配置集群。参见[迁移](../getting-started/install-edge.md#在-vps-面板和-cloudflare-版之间迁移)。"
    - title: 在真实账户上运行 Cloudflare 版
      icon: globe
      tag: 预览版
      text: "首次在真实 Cloudflare 账户上运行 `mistgate edge deploy`：六个 Worker、带域名的路由器和 `--rollback`。目前只在模拟的 Cloudflare 环境中测试过命令和面板。参见[安装 Cloudflare 版](../getting-started/install-edge.md)。"
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
- SSH 安装使用面板自己的密钥、密码，或一次性使用所有者自己的私钥登录，节点卡片可以管理 `authorized_keys`。在 sshd 中关闭密码登录和轮换面板密钥，目前尚未提供。见[通过 SSH 安装节点](../getting-started/ssh-install.md)。
- 备份可保存到 Cloudflare R2 或所有者电脑上的文件。恢复功能在 VPS 版中提供（面板和安装程序）；Cloudflare 版的数据保存在 D1 中，暂不支持恢复。
- 面板不会创建 Cloudflare 账户、存储桶或令牌；你需要提供一个仅限存储桶且具有读取、写入和删除权限的令牌。
- Cloudflare 版仍处于预览阶段，尚未在真实 Cloudflare 账户上试过，也尚未提供部分功能（参见[列表](../getting-started/install-edge.md#此版本中尚不可用的功能)），也无法在 Cloudflare 版和 VPS 面板之间迁移集群。
- 设备 ID 由应用自己报告。拉黑会拒绝该 ID；报告其他 ID 的应用算作新设备，并占用一个空闲名额。要让被转给他人的链接失效，还需要重新签发一个新链接。
- **每用户限速**按节点计算，因此同时连接两个节点的用户最多可获得上限的 2 倍。v0.1.39 之前的节点 Agent 会把每台设备限制在两个限速中较小的一个。
- 在 Cloudflare 版中，面板记录的已发送到节点的内容摘要必须放进 D1 的一行（2 MB），约合每个节点 38,000 个凭据。参见[此版本的限制](../getting-started/install-edge.md#此版本中尚不可用的功能)。
- sing-box JSON 没有 XHTTP，也无法按指纹校验自签名的 Hysteria2 证书；Gecko 和 AmneziaWG 只通过 Mihomo YAML 下发。格式无法承载的服务器不会出现在该格式中。参见[各格式支持哪些协议配置](../guide/subscriptions.md#各格式支持哪些协议配置)。
- 国家视角检测只从少数探测点观察，而不是从每个网络观察，封锁名单也只覆盖俄罗斯。`listed`（在名录中）表示地址在名录里，不表示它的流量被丢弃。
- 部分默认值因国家而异（俄罗斯使用 Yandex DNS，中国使用 AliDNS 和 DNSPod，诊断使用各国对应的检测域名，DNS 分流预设）；这些都可以在设置中修改。
- 助手会把对话、它的指令、你的备注和工具读取的内容发给你连接的云端提供商，因此适用该提供商的数据政策。你填写的价格只是估算，月度预算按它计算。参见[提供商与数据去向](../guide/assistant.md#提供商与数据去向)。
- 助手、MCP、计划任务、探测、测速、调试抓取、面板日志、面板资源和负载视图仅在 VPS 版中提供。参见[清单](../getting-started/install-edge.md#此版本中尚不可用的功能)。
- 诊断的好坏取决于计数。VLESS 没有尝试次数的计数，内核版 AmneziaWG 后端分不清握手的发起和应答；两种原因看起来一样时，`block_diagnosis` 会如实说明并标明低置信度。早于 v0.1.81 的 Agent 不会统计握手为什么失败，也不统计节点机器缺什么：面板会显示「未知」。
- 计划任务遵循面板固定的 UTC 偏移，不会自动跟随夏令时：本地时间改变时请重新设置偏移。
