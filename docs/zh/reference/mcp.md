---
title: MCP 服务器
description: 面向 AI 智能体的面板内置 MCP 服务器、客户端连接方式、全部工具、计划与执行流程，以及哪些变更需要等待所有者审批。
---

面板内置了模型上下文协议（MCP）服务器，AI 智能体可以读取集群信息并执行日常变更。它与[管理 API](api.md)使用相同的 API 令牌和方法：令牌的权限级别决定智能体能看到什么、可以执行什么，并且每次调用都会记入审计日志。变更不会通过一次调用直接生效：智能体先制定变更计划，再执行计划；风险最高的变更需要等待所有者在管理后台批准。

下方的 HTTP 端点仅在 VPS 版中提供。Cloudflare 版第 1 阶段不提供 `/mcp`；计划在第 2 阶段通过单独的 Worker 服务绑定提供支持。

## 端点

```text
<admin URL>mcp
https://panel.example.com/<prefix>/mcp
```

- 使用无状态 Streamable HTTP 和 JSON 响应。只提供工具，不提供资源、提示或采样。
- 仅在管理后台入口（私密前缀、主机名或监听器）上提供，绝不开放在公共端。
- 每个请求都必须包含 `Authorization: Bearer tk1_…`，并会重新验证：令牌被吊销或过期后，下一次请求就会被拒绝。这里没有可被接管的会话。
- `tools/list` 只会显示该令牌权限级别允许使用的工具。

**AI → MCP 与令牌** 会显示端点地址和适用于各类客户端的配置片段。

## 连接客户端

在 **AI → MCP 与令牌** 中创建令牌，并选择能够满足需求的最低权限级别（参见[令牌权限级别](api.md)）。然后：

**Claude Code** (Streamable HTTP):

```sh
claude mcp add --transport http mistgate https://panel.example.com/<prefix>/mcp --header "Authorization: Bearer <token>"
```

**任何支持 Streamable HTTP 的客户端**：

```json
{
  "mcpServers": {
    "mistgate": {
      "type": "http",
      "url": "https://panel.example.com/<prefix>/mcp",
      "headers": {
        "Authorization": "Bearer <token>"
      }
    }
  }
}
```

**只能启动命令的客户端**（例如 Claude Desktop）可在自己的计算机上使用 stdio 代理 `mistgate mcp`。将令牌保存为只有你能读取的文件中的第一行，然后：

```json
{
  "mcpServers": {
    "mistgate": {
      "command": "mistgate",
      "args": ["mcp", "--url", "https://panel.example.com/<prefix>/", "--token-file", "<path-to-token-file>"]
    }
  }
}
```

### stdio 代理

`mistgate mcp --url <admin URL> --token-file <file>` 会通过 stdin 和 stdout 运行本地 MCP 服务器，并将每条消息转发到面板端点。它不作任何判断、不缓存内容，也不识别工具：由面板负责响应。

- 令牌只从文件第一行读取，不会从命令行或环境变量读取，也绝不显示。其他人可以读取该文件时会显示警告（Windows 上不检查）。
- `--url` 必须使用 `https`，`localhost` 或回环地址除外。不会跟随重定向，因此令牌不会发送到其他地址。
- 面板拒绝令牌（令牌已过期、已吊销或权限级别不正确）时，代理会显示相应消息并停止。
- `--url` 和 `--token-file` 未指定时，会分别回退到 `MISTGATE_URL` 和 `MISTGATE_TOKEN_FILE`。

`mistgate` 可为 Linux、macOS 和 Windows 构建：运行 `go build ./cmd/mistgate` 会生成适用于运行智能体的计算机的二进制文件。参见 [CLI](cli.md)。

## 限制

| 限制 | 数值 |
|---|---|
| 每个令牌同时运行的工具调用数 | 4（超过限制时返回 429 和 `at most 4 tool calls at a time per token`） |
| 每分钟请求数 | 该令牌自身的限制 |
| 请求正文大小 | 3 MiB |
| 单个工具结果 | 32 KiB；长列表会逐步减半，直到符合限制，并标记为已截断 |
| 每次调用的时限 | 读取和制定计划为 30 s，执行为 90 s |
| 未完成计划数 | 每个令牌 20 个；整个面板中等待所有者审批的计划最多 50 个 |
| 计划中保存的参数 | 8 KiB（上传伪装站点发布包时为 3 MiB） |

## 工具

### 读取工具

读取工具不会修改任何内容。参数仅接受 ID 和普通文本，不接受 URL：工具不会获取智能体指定的任何内容。节点可使用其 ID 或精确名称指定。「权限级别」列显示可以使用该工具的最低权限级别；更高权限级别同样可以使用。

| 工具 | 权限级别 | 返回内容 |
|---|---|---|
| `fleet_status` | 只读 | 所有节点的状态、原因、在线用户、速度和 CPU；总计、告警数量及当前流量最高的用户。 |
| `fleet_digest` | 只读 | 一次调用查看集群问题：`hours`（1 到 72，默认 24）；所有计数只在该窗口内。`problem_nodes` 为每个节点给出原因列表（`code`、`count`、`last_at`）：带时长的中断和抖动（`minutes`，以及按窗口截取的 `minutes_in_window`）、窗口内打开或仍未关闭的告警（按类型）、按来源统计的日志警告和错误、检查失败轮次、`path_lossy` 等警告事件、国家判定变化、更新失败和回滚，以及当前不在线的节点。`quiet_nodes` 列出其余节点。`totals` 是集群总计；`traffic_hourly` 和 `online_hourly` 是 `fleet_status` 省略的每小时序列，问题节点附带 `spark_bytes_24h`。检查历史按小时统计，因此与窗口重叠的桶会计入。`unavailable` 列出未能读取的部分。 |
| `user_diagnose` | 只读 | 一次调用查明某人为何无法连接：`user_id` 和可选的 `hours`（1 到 168，默认 24）。用户及其访问权限（状态、到期、配额、设备上限和设备）、其订阅提供的节点和配置（仅结构，同 `subscription_preview`）、按设备和节点根据连接计数给出的判定（`ok`、`rejected`、`unanswered`、`answers_lost`、`stalled`、`throttled`、`frozen`、`lossy`、`slow`、`no_attempts`）及 `signals`、与此人相关的告警和事件、其所用节点的状态（状态、国家判定、检查失败），以及简短的机器可读 `findings` 列表（`device_limit_reached`、`expired`、`disabled`、`over_quota`、`never_connected`、`no_attempts_in_window`、`rejects_auth`、`handshake_unanswered`（AmneziaWG：节点不应答，我方问题）、`handshake_answers_lost`（节点应答了但应答没有到达：回程被切断）、`stalls`、`tcp_freeze_16k`（VLESS 连接在最初 8–32 KiB 后停止：TCP 限速）、`freezes`、`high_loss`、`high_rtt`、`node_down`、`node_blocked_in_country`、`no_node_access`、`no_profiles`、`device_blocked`、`no_counters`），每项带 `severity`、`evidence` 引用和 `detail`；`verdict` 是最严重的一项。使用 `links/3` 的 Agent 时，每个连接还带 `tcp_stalls`、`tcp_stalls_16k`、`initiations`、`responses` 和 `responses_unmatched`，`not_known` 列出节点无法判断的那些（旧 Agent、内核版 AWG 后端）。只有计数：没有密钥、链接、页面密码或客户端地址。 |
| `timeline` | 只读 | 发生了什么，最新在前，合并为一个规范化列表 `{at, kind, severity, code, node, subject, summary_key, params, ref}`，来源包括事件、告警打开与解除、节点日志（每个节点、来源和小时一项）、Agent 重启（`restart`）、滚动更新，以及仅管理员配置文件可见的审计日志（已移除地址）。`node` 或 `user` 缩小范围；用 `hours`（1 到 168，默认 24）或 `from` 与 `to`（unix 秒）设置窗口；可用 `kinds`、`min_severity` 和 `changes_only`（审计、滚动更新、`state_applied`、配置、中继、设置和更新事件）过滤。`limit`（默认 50，最多 100）和 `next_cursor` 以同一顺序遍历所有来源。`partial_sources` 列出触及读取上限的来源。 |
| `node_get` | 只读 | 单个节点：状态和原因、主机信息、入站及其状态、`direct_in_subscriptions`（是否在订阅中列出节点地址）和 `cdn`（客户端是否通过 CDN 主机连接）、最多 10 位在线用户、所有者备注，以及 `ipv6`：其 [IPv6 入口](../guide/nodes.md#ipv6-入口)是否开启、节点能否接受入口（`unavailable_reason`：`no_ipv6`、`ipv6_broken`、`agent_old`、`not_checked` 或 `main_is_ipv6`）和最近一次地址检测。不含地址、密钥或证书固定指纹。 |
| `nodes_get` | 只读 | 一次调用返回多个节点的 `node_get`：`nodes` 是节点 id 或准确名称的列表，留空 = 所有未退役的节点（最多 50 个）。结果过大时先去掉在线用户，再去掉整个节点，并设置 `truncated`。 |
| `warp_status` | 只读 | 单个节点是否有 WARP 账户，以及账户是否已启用或暂停；Agent 状态、colo、距上次握手的时间、Cloudflare 和其他站点的探测状态与延迟、注册时间、是否有刷新令牌，以及经 WARP 出口的协议配置。不含地址、密钥、账户 ID、许可证或令牌。 不带 `node` 时：为每个未退役的节点返回一行同样的精简结果（`unavailable` 列出没有应答的节点）。 |
| `relays_list` | 只读 | [中转](../guide/relays.md)：入口节点上把出口节点的某个服务器原样转发出去的端口。可选的 `node` 只保留以它为任一端的中转。每条中转包含：入口和出口节点、出口的服务器（协议配置、协议、网络）、入口和出口上的端口、`enabled`、`state`（`pending`、`active`、`failed`；`last_error_code` 是 Agent 错误信息的第一个词）、`exit_live`、`agent_supported`，以及 `last_check`：链路（入口能否连到出口的服务器）和路径（第三个节点能否经入口连到它），各自为 `ok`、`fail` 或 `skipped` 并带代码、发出探测的节点，UDP 还有发送和统计到的数据包数。读取的是已保存的数据，不会联系节点。不含地址。 |
| `relay_candidates` | 只读 | 针对某个入口节点（`node`）：它可以中转的其他节点的服务器。每个服务器包含：`inbound_id`（`relay_plan` 所接收的值）、出口节点、协议配置、协议、网络和端口；`unsupported`（`cdn`、`acme_ip`、`awg`、`build_failed`；为空表示可以中转）；`hop`（服务器带跳跃范围，中转只传递一个端口）；`relay_id`（该入口已在中转它）；`exit_country_check`（国家检测如何看待出口：`ok`、`partial`、`blocked`、`listed` 或 `no_data`，以及它在哪些网络中不可达）；`entry_reaches_exit`（`reach`、`partial`、`blocked` 或 `no_data`，TSPU 探测从该入口运行过时才知道）。`entry_country_check` 是对入口自身的同样检查。`no_data` 表示未知，不表示没问题。不含地址。 |
| `node_metrics` | 只读 | 节点当前的 CPU、RAM、磁盘和网络数据、今日流量、各协议的在线用户，以及今日流量最高的用户。带 `hours`（1 到 720）时还返回按小时的主机负载（CPU 均值与峰值、softirq、RAM、网络、会话；超过 96 小时的窗口按 `bucket_hours` 分组）以及整个窗口的峰值及其所在小时。面板保留 90 天。 对于上报这些数据的 Agent，`resources` 还包含 CPU steal 和 I/O 等待、交换分区、连接跟踪表（条目数、上限、使用率）、TCP 和 UDP 套接字、网卡链路速率以及 Agent 进程本身（CPU、内存、goroutine、线程、打开的文件数与上限）；没有该字段说明 Agent 太旧，无法上报。不带 `node` 时：为每个未退役的节点返回一行当前指标（不含用户排行和历史；已知时包含 steal、conntrack 使用率以及 Agent 的 CPU 和内存）。随时间变化的负载和余量请用 `capacity_report`。 |
| `node_runtime` | 只读 | `node`（留空 = 所有节点）。节点实际运行的内容与面板期望的对照：期望与已应用的版本号和哈希（12 个字符）、`in_sync`、`drift` 及起始时间、最近一次应用，以及每个 inbound 的 `enabled`（属于期望状态）、`spec_version`、已应用的规格哈希、已应用与实时状态、最近错误和证书到期时间。`attention` 列出不一致之处：`applied_not_desired`（孤儿 inbound 的样子）、`desired_not_applied`、`failed`、`unknown_running`。不返回地址、证书指纹或密钥；不会联系节点。 |
| `panel_logs` | 管理员 | 面板自身最近的日志记录（内存环形缓冲，保留最近 5000 条，重启后丢失），最新的在前；地址、秘密名称和形似密钥的值已移除，属性只保留白名单中的键。筛选：`min_level`（`debug`、`info`、`warn`、`error`，默认 `info`）、`minutes`（最多 10080）、`query`（消息或属性值的子串，最长 100 个字符，在清理后的文本中匹配）、`component`（`health`、`fleet`、`http` 等）、`code`（精确的错误码）、`request_id`（管理界面在请求失败时显示的代码）、`limit`（默认 100，最多 500）和 `cursor`（上一页的 `next_cursor`）。`level`、`buffered` 和 `capacity` 说明缓冲区的状态。消息来自面板自己的代码和它处理过的数据：属于不可信文本。 |
| `capacity_report` | 只读 | 每个节点的负载程度以及还能容纳多少人：`hours`（1 到 720，默认 168）和可选的 `node`（只返回该节点，并附带集群汇总）。每个节点：`status`（`ok`、`busy`、`overloaded`，无数据时为 `unknown`）、最先耗尽的 `bottleneck`（`cpu`、`steal`、`bandwidth`、`ram`、`conntrack`、`disk`）、高峰小时（每小时 CPU 均值最高的小时）及其 CPU 和人数、在线人数峰值、每小时 CPU 峰值的第 95 百分位（`cpu_peak_p95_pct`，最高的）和第 75 百分位（`cpu_peak_typical_pct`，大多数小时都低于它：状态看的是它，仅凭峰值永远不会把节点判为过载）、steal p95、conntrack 和 RAM 峰值、交换分区、磁盘、流量峰值与节点带宽之比（`capacity_source`：所有者设置的带宽，否则为网卡链路速率）、Agent 自身的 CPU 和内存及其每日增长、按小时最小二乘拟合得出的 `cpu_per_100_people_pct` 和 `mbps_per_person`（数据不足以拟合时 `fit` 为 `few_hours`、`flat` 或 `no_trend`）、`headroom_people`（高峰小时 CPU 达到 80% 之前还能增加的人数）以及 `recommendations`（`code`、`severity`、`params`；`cpu_busy` 的 `target` 是余量最大的节点）。`summary`：各状态的节点数、总余量、负载最高和最空闲的节点。缺少的值表示未知（Agent 太旧，无法上报），而不是零。见[负载](../guide/nodes.md#负载)。 |
| `panel_resources` | 只读 | 面板自身当前的资源占用：进程（占整台机器的 CPU 百分比、常驻内存、Go 堆和总内存、goroutine、线程、打开的文件数与上限、运行时长）、所在机器（CPU 繁忙、steal 和 I/O 等待、load、RAM、交换分区、数据目录所在磁盘）、数据库（文件和 WAL 大小、页数和空闲页数；最多每分钟测量一次）、已连接的 Agent 数与节点总数，以及 `admin_api`：最近 5 分钟管理 API 的调用（请求数、服务器和客户端错误、p95 毫秒）。 |
| `retention_status` | 管理员 | 面板保存所存数据的时长：每项设置（`journal_days`、`events_info_days`、`events_warn_days`、`audit_days`、`alerts_days`、`check_samples_hours`、`check_daily_days`、`host_history_days`、`traffic_bucket_days`、`node_traffic_hour_days`）的当前值、默认值、范围和单位。 |
| `node_debug_logs` | 管理员 | 节点的调试抓取（用 `node_debug_capture_plan` / `_apply` 开始），最早的记录在前。指定抓取（`capture_id`）或节点（它最新的抓取）；两者都不给则返回面板保存的抓取列表（24 小时），最新的在前。抓取会给出 `state`：`running`（时间到了记录才会到达）、`done`，或 `expired`（时间已过而节点始终没有发送）。筛选项：`min_level`（`debug`、`info`、`warn`、`error`）、`source`、`query`（消息或属性值的子串，最多 100 个字符，在清理后的文本上匹配）、`limit`（默认 100，最多 500）和 `cursor`（上一次的 `next_cursor`）。记录在节点和面板中都经过清理：地址为 `<ip>`，没有机密名称和密钥，只有一份简短的属性列表。消息来自节点的引擎：属于不可信文本。 `kernel` 来源会读取从抓取开始前 30 分钟到抓取结束期间的内核日志；防火墙数据包日志只计数，不保存。对于使用内核后端的 AWG 节点，选择 `awg` 会在抓取期间启用模块调试，并在摘要中列出 5 秒内未得到响应的握手发起次数。|
| `tasks_list` | 操作员 | 所有者的[计划任务](../guide/assistant.md#计划任务)：每个任务的 `id`、`name`、`kind`（`check` 或 `assistant`）、`enabled`、`schedule`（`every 15m`、`daily 07:30`、`weekly mon 07:30`）、`next_run_unix`、`running`、`last_run`（`status` 为 `ok`、`problem`、`failed`、`skipped` 或 `running`，以及 `reason` 代码和一行 `summary`）、`problem` 及其摘要（`scheduled_check` 告警已打开）和 `fail_streak`（连续三次即打开 `scheduled_task_failing`）。`timezone_offset_minutes` 是面板时区。名称和摘要是不可信文本。Cloudflare 版没有此功能。 |
| `task_get` | 操作员 | 一个任务的全部内容（`task_id`）：检查任务的 `steps`（`tool`、`args`、`conditions`、`error_is_problem`），助手任务的 `prompt` 和 `conversation_id`，`notify_telegram`，以及最近 5 次运行。 |
| `task_runs` | 操作员 | 任务的运行记录，最新的在前：`task_id`、`limit`（1 到 50，默认 10；面板保留最近 50 次，最多 30 天）。每次运行有 `trigger`（`schedule`、`manual`，或面板停机时错过的 `missed`）、`status`、`reason`（`tool_failed`、`provider_unreachable`、`budget_reached`、`no_verdict`、`interrupted` 等）、`summary`；检查任务有 `steps`（`tool`、`ok`、`problem`、`error`、条件的 `matches` 和结果开头）；助手任务有 `conversation_id`、`provider`、`model`、`usd` 和 `fell_back`。 |
| `task_run_now` | 操作员 | `task_id`：在计划之外立即开始一次运行，并立刻返回 `run_id`；用 `task_runs` 读取结果。同一任务同时只运行一次（`task_running`）；手动运行检查任务最多每分钟一次，助手任务最多每五分钟一次（`too_soon`）。助手任务的运行从助手的月度预算中扣费。 |
| `path_loss_matrix` | 只读 | `hours`（1 到 720，默认 24）。端口检测中丢包的节点间 UDP 路径：发送方、目标、端口、发送数、到达数、`loss_pct`、同一目标上到达正常的另一个发送方和 `open_ports`（途中的过滤），以及每个目标有多少发送方丢包，用来区分节点问题和路径问题。只显示节点名称。 |
| `access_report` | 只读 | 一次调用查明哪些服务器对哪些人打不开、该怎么改。`hours`（1 到 168，默认 24），可选 `country`、`group`（id 或准确名称）、`node`、`protocol`。`matrix`：按节点与服务器（协议、传输、端口、CDN）给出尝试过和成功的设备与人数、`success_pct`（VLESS 没有尝试计数器，故无此项）、在计数器允许范围内拆分的拒绝、卡顿、冻结与丢包，以及分配了该服务器却从未连接的人，并按组细分。`country`：国家判定、TSPU 端口、不通与可达的运营商，以及按运营商和服务器类型汇总的 kl!ck 报告。`block_diagnosis` 与 `findings`：带证据引用及受影响用户和组数的机器可读代码。`recommendations`：按排名，每条有类型、目标、原因、预期效果以及执行它的 `plan_tool`（`relay_plan`、`profile_update_plan`、`group_update_plan`、`profile_create_plan`、`node_ipv6_plan`、`node_update_plan`）或 `next_tool`。使用 `links/3` 的 Agent 时，`reject_split` 还给出原因：`no_reply`、`cert_refused`、`alerts_sent`、`alerts_recv`、`reality_fallback`、`reality_auth_failed`、`answers_lost` 和 `unanswered`；VLESS 单元带 `tcp_stalls` 和 `tcp_stalls_16k`，`signals` 中包括 `tcp_freeze_16k`、`answers_lost` 和 `cert_refused`。规则固定，缺失的信号标为未知。只有计数和名称。 |
| `block_diagnosis` | 只读 | 节点是怎样被封的，而不仅是是否被封：可选 `node`、`country`、`hours`。按节点、端口或协议给出类别、置信度、证据引用、运营商与地区、未知项以及带 plan 工具的下一步：`ip_blocked`、`port_blocked`、`tcp_throttled`、`udp_blocked`、`protocol_fingerprinted`、`whitelist_mode`、`operator_specific`、`regional`、`not_blocked_but_broken`、`unknown`。使用 `links/3` 的 Agent 时，原因计数能决定更多：`tcp_throttled` 也可依据节点自身在 8–32 KiB 后停滞的 VLESS 连接；当节点已应答而应答丢失时（Hysteria2 `hs_unfinished`、AmneziaWG `responses_unmatched`），`protocol_fingerprinted` 和 `udp_blocked` 更有把握；REALITY 已应答却始终未完成的握手说明在 ClientHello 之后被切断；`not_blocked_but_broken`（带 `detail.reason`）还涵盖应用拒绝节点证书、节点不应答 AmneziaWG 握手发起，以及所有人都无法通过 REALITY 认证。没有这些计数时（旧 Agent、内核版 AWG 后端），类别会说明哪些是未知的。仅用已存数据，不联系任何节点。 |
| `node_doctor` | 只读 | 单个节点或所有节点最近保存的诊断报告，以及修复 ID。从操作员权限级别起，还可设置 `refresh: true`：让节点立即运行检查（只读取主机信息，最多等待 30 s）。 带 `refresh: true` 且不带 `node` 时，每个已连接的节点都会运行自己的检查（一次最多 24 个节点）；`kept_last_report` 列出保留上一份报告的节点：它们离线，或其代理版本过旧。 |
| `node_ports` | 只读 | 已保存的 UDP 投递检查、TCP 可达性，以及每个端口上的入站/协议配置：`direct_in_subscriptions` 表示是否列出节点地址，`cdn` 表示客户端是否通过 CDN 主机连接。操作员权限可设置 `check: true` 运行有时限的检查（约 15 s）。 不带 `node` 时：为每个未退役的节点返回一行精简结果，只含已保存的结果（`check` 需要指定节点）。 |
| `checks_run_now` | 操作员 | 立即运行健康检查而不等下一轮，并按 `checks_results` 的格式返回被检测 inbound 的最新结果。`node`：单个节点，留空 = 所有节点；`inbound`：只看该 inbound。面板对每个 inbound 至多每 30 秒运行一轮：过早再次请求会被拒绝，并提示改用 `checks_results`。最长等待 25 秒；`complete: false` 表示仍有轮次未结束。 |
| `path_probe` | 操作员 | 立即在你的两个节点之间发送测试流量：`from`、`to`（节点 id 或准确名称），`kind` 为 `udp`、`tcp` 或 `tls`，可选 `ports`（目标自己的部分端口；目标未提供的端口会被拒绝）。`udp`：每个端口 150 个带标记的数据报，返回发送数、到达数和结论（`ok`、`lossy`、`broken`）。`tcp`：对目标提供的每个 TCP 端口做普通连接，并给出毫秒数。`tls`：使用目标自己的名称和证书指纹握手（只有真正的监听者才是 `ours`），并给出毫秒数。`mtu_sweep: true`（或 `sizes`，最多 5 个 64 到 1472 的载荷大小）在一个端口上对每个大小发送十个禁止分片的数据报，并返回 `largest_size`，即至少一半到达的最大大小。目标始终是集群中的节点：无法指定主机或 URL。发送方需要支持探测的代理（否则为 `agent_too_old`），`udp` 还要求目标节点在线。有速率限制：同一节点对同时只能有一个探测，每个节点 5 分钟内最多 6 次，每个令牌 10 分钟内最多 12 次。结果保存 7 天。不是只读工具：它会发送流量。只返回节点名称，绝不返回地址。Cloudflare 版不提供。 |
| `node_trace` | 操作员 | 从你的一个节点（`from`）到另一个节点（`to`）做 TTL 递增的 TCP SYN 路由追踪：每一跳的往返时间以及路由器是否应答；目标那一跳显示节点名称，其余为 `hop N`（不返回第三方路由器的地址）。与该节点对上一次追踪比较（保留最近 20 次）：`diff` 和 `changed_at_hop` 说明路由变长、变短或某一跳不再应答。长度和应答模式不变的路由变化会显示为未变化。限制与 `path_probe` 相同；仅限 Linux 节点。 |
| `node_dns_check` | 操作员 | 节点自己的解析器：医生检查所用控制域名的查询耗时（`p50_ms`、`p95_ms`）和失败数，最新一次运行、最近 24 次运行以及 `trend`（`steady`、`slower`、`faster`，不足四次为 `unknown`）。除非 `stored_only` 为 true，否则立即运行医生的解析器检查；已保存的医生结果也会并入历史。限制与 `path_probe` 相同。 |
| `users_search` | 只读 | 按名称片段、`filter`（`online`、`expiring`、`over_quota`）或 `group_id` 搜索用户；使用 `page_token` 分页。 |
| `groups_list` | 只读 | 分组及其协议配置 ID、用户数量和 DNS 预设。 |
| `group_get` | 只读 | 单个分组：协议配置 ID、排除的节点、DNS 预设、颜色、用户，以及它当前提供的服务器总量。分组 API 没有备注和配额字段。 |
| `dns_presets_list` | 只读 | DNS 预设、解析器和分流规则、实际用户数量、是否为默认、提供商目录以及客户端传输支持情况。 |
| `dns_preset_get` | 只读 | 按 ID 返回单个 DNS 预设，含解析器和分流规则、用户数量以及是否为默认。 |
| `protocols_list` | 只读 | 协议插件、客户端应用、精简设置结构（包括关键字段和敏感字段）以及默认设置。敏感默认值会被遮蔽。对尚属新协议、还没有在实际环境中得到验证的协议（MTProto），会设置 `experimental`。 |
| `profiles_list` | 只读 | 协议配置摘要，包括协议、节点数和用户数、警告、订阅格式、版本以及授予访问权限的分组。 |
| `profile_get` | 只读 | 单个协议配置的摘要和已遮蔽敏感字段的设置；入站包含节点 ID 和名称、生效及覆盖后的端口和服务器名称、启用状态和运行状态、`direct_in_subscriptions`（是否在订阅中列出节点地址）和 `cdn`（客户端是否通过 CDN 主机连接）；还包括授予访问权限的分组。参数为 `profile_id`。 |
| `user_get` | 只读 | 单个用户：限制（含两种限速）、状态、四个应用开关（`app_happ`、`app_amnezia`、`app_telegram`、`app_mistgate`）、设备、协议配置（每个都带有包含它的订阅格式）和节点访问权限。不含订阅链接或密钥。 |
| `user_traffic` | 只读 | 已用流量和配额、最近 14 天的数据，以及按节点和协议划分的流量。 |
| `user_devices` | 只读 | 设备的类型、平台、型号、系统版本、应用、流量、最后在线时间、在线状态及所在节点、设备 ID 的简短指纹；AmneziaWG 还会返回协议配置和隧道地址；已拉黑的设备单独列出。绝不返回密钥或配置文件。 |
| `subscription_preview` | 只读 | 客户端（客户端 ID 或 User-Agent）会获取的格式，以及该用户有权访问的协议配置（每个都带有包含它的订阅格式）和节点。不返回订阅内容或链接。 |
| `subscription_settings_get` | 只读 | 每位用户看到的订阅页面：应用、页面选项、按 User-Agent 下发的有序规则、实例 DNS 默认值、链接应用的 DNS 推荐、链接设备数量限制模式、链接中的 AmneziaWG 和自动选择服务器开关、Happ 延迟检测设置、服务器名称模板、标题、公告、联系链接和更新间隔。另含共享链接用户和链接 AmneziaWG 密钥的实时数量。不含任何用户的链接或密钥。 |
| `alerts_list` | 只读 | 活跃告警；设置 `include_history` 后也会返回已关闭的告警（`window_s` 最长为 30 天）。 `min_severity`（`info`、`warning`、`critical`）和 `kind` 可缩小列表范围。 |
| `health_summary` | 只读 | 简要统计：已部署的合成检查按最新状态的数量，以及活跃告警按严重程度的数量。单条记录请用 `alerts_list` 或 `checks_results`。 |
| `reachability_history` | 只读 | 可达性页面的国家、地址族、活跃来源，以及每个节点在 1 到 7 天内按来源给出的判定时间线。`family` 为 4（IPv4）或 6（IPv6）。 |
| `events_search` | 只读 | 按节点、用户、`min_severity`（`info`、`warning`、`error`）或精确 `code` 搜索事件流，`hours` 只保留最近 N 小时（1 到 168）；使用 `before_id` 分页。 `family`（`profiles` 或 `agent`）选择节点上与配置相关的事件（引擎、配置、跳转、凭据），或选择其余所有事件。 |
| `checks_results` | 只读 | 客户端视角检测：按协议配置列出节点、最近一次结果、连续失败次数和最近 24 小时的历史记录。含通过已建立隧道的响应时间、连接时间和最近一小时的中位数（均在面板服务器上测得）；Hysteria2 还有 `udp_sent` 和 `udp_lost`（面板自身 QUIC 客户端在最近一轮发送和丢失的数据包数，去往节点的方向）、`udp_sent_hour`、`udp_lost_hour`，以及 `node_sent_hour`、`node_lost_hour`（返回方向，节点自己的统计）；面板服务器在到某些节点的路上丢失 UDP 时（这些节点的 UDP 轮次比到同一节点的 VLESS 慢 600 毫秒或以上）返回 `udp_loss`，并带有 `lost_permille`（去往）和 `node_lost_permille`（返回）。开启国家视角检测后（见[可达性](../operations/reachability.md)），每行带有 `country_check`：该国境内探测点给出的最新结论及所用网络，以及结论的 `confidence`（`eyeball`：家庭和移动网络；`dc_only`：仅数据中心；`listed`：仅依据封锁名单）。结论可能是 `partial`：部分家庭或移动网络从该节点什么也收不到，却能连到您的其他节点，而其他网络可以连上它。开启 Globalping 后，`probes` 列出被询问的网络，每个带有 `eyeball`（家庭或移动探测点），在 `partial` 轮中还有 `dark`（它从这个节点什么也没收到，却连到了另一个节点）；`no_probe` 给出该轮没有在线家庭或移动探测点的运营商。开启[封锁名单](../operations/reachability.md#封锁名单)后，还带有 `listed_by`（`antifilter_ipsum`、`antifilter_community`）、`cdn_provider` 和 `cdn_region`，结论也可能是 `listed`：地址在名录中、未经实测，且从不触发告警。开启 [TSPU 探测](../operations/reachability.md#tspu-探测)后，`country_check` 还带有 `sources`（`globalping`、`tspu`、`lists`）和构成结论的两个来源各自的结论（`globalping_verdict`、`tspu_verdict`）；`tspu` 是探测节点对该节点的观察（`probe_node`、`at_unix`、`dpi_hop`，以及 `ports`，每项为 `blocked`、`open`、`refused` 或 `timeout`），始终是数据中心视角（`dc_only`）。开启 [cheburcheck](../operations/reachability.md#cheburcheck) 或 [bsbord](../operations/reachability.md#bsbord) 后，`sources` 还会写出 `cheburcheck`、`cheburcheck_own` 和 `bsbord`，`country_check` 也带有覆盖所有来源的两个列表：`source_verdicts`，每个来源的最新有效结论，含 `source`、`verdict`、`reason`（没有结论的原因或它看到的情况，例如 `too_few_probes`、`no_probe`、`timeout` 或 `no_tcp_port`）、`eyeball` 和 `at_unix`，`unknown` 也包括在内；以及 `vantages`，这些结论背后的网络、单元或探测点，每项含 `source`、`key`、`network`、`region`、`asn`、`kind`（`home`、`mobile`、`dc`、`unknown`）、`state`（`reach`、`dark`、`whitelist`、`uncertain`）、`at_unix`、`dpi_hop`、`icmp_ok` 和 `tcp_ok`；Globalping 的探测点和 TSPU 探测的探测节点也在其中。结论由所有有效来源合并而成（[规则](../operations/reachability.md#来源如何并入结论)）：只要有可达的又有无应答的，就是 `partial`；置信度 `conflict` 表示端到端看到的完全封锁与仅由路径类来源（TSPU 探测、cheburcheck）看到的可达相左，此时封锁仍是结论。开启 Globalping 的 IPv6 检测后，`country_check.ipv6` 是同一节点按其 IPv6 地址测得的结果，结构相同（唯一来源是 Globalping；上面的字段仍是 IPv4）：未被测量的节点为 `unknown`，`reason` 为 `no_ipv6`、`ipv6_broken`（节点有地址，但 IPv6 在服务商处不通）、`agent_old`、`not_checked` 或 `no_probes`（Globalping 在该国没有支持 IPv6 的探测点）。名称和地区来自第三方，视为数据。顶层的 `tspu_probe` 是探测节点最近一次运行：`status`（`ok`、`control_failed`、`no_dpi_hop`、`busy`、`failed`）、`at_unix`、`probe_node`、`dpi_hop`、`blocked_ref`、`nodes_checked`、`detail`，以及 `no_dpi_hop` 时的 `reason`（`refs_pass`：被封锁的地址能从探测节点打开，其路径上没有 TSPU；`no_hops`：路径上的路由器没有应答；原因未知时不返回此字段）。 每个单元格都有 `inbound_id` 和 `stats_24h`：最近 24 小时内隧道建立成功的轮次中，`connect_ms` 与 `response_ms` 的 p50、p95，以及 `jitter_ms`（`response_ms` 的标准差）。`daily_days`（1 到 90）会在每个单元格中加入 `daily`：每个已结束的 UTC 日一条记录，含 `ok`、`degraded`、`failed` 计数，`latency_ms`、`connect_ms`、`response_ms` 的 p50 与 p95，以及 `jitter_ms`（0 = 未测量；此前保存的日期没有 connect 和 response 数据）。 |
| `klick_reports` | 只读 | 使用 kl!ck 应用的人在 7 天内从自己的网络看到的节点情况（[kl!ck 报告](../operations/reachability.md#klck-报告)）：每个节点的报告数、`ok`、`freeze`（TSPU 冻结）、`tcp_blocked`、`handshake_failed`、`no_answer` 和最近一份报告的时间，以及按运营商、地区和服务器类型 `kind` 的同样统计（各最多 8 个，优先显示报告最多的）。每个 `kind` 还包含已测 `delay_ms` 的中位数、含 `dl_stalled` 结果的 `dl_checked` 行数，以及下载确实卡住的 `dl_stalled` 行数。`enabled` 为 false：来源已关闭，不显示任何内容。可选参数 `node`。不含地址、用户或设备。 |
| `outdated_devices` | 只读 | 哪些设备获得 VLESS REALITY 服务器，以及其应用是否已知会发送后量子密钥份额（[应用版本](../guide/subscriptions.md#应用版本)）。`counts` 给出每个状态的设备数和人数：`ok`、`below_minimum`（应用已知，版本低于 `need_version`）、`unsupported`（没有任何版本可用，`reason` 为 `app`；或配置的 fingerprint 是传统的，`reason` 为 `fingerprint`）和 `unknown`（`app_unknown`、`no_threshold` 或 `no_version`）。`fingerprints` 是正在使用的 REALITY 配置的 fingerprint。`state` 缩小列表（留空 = 除 `ok` 外的全部），`user_id` 限定为一个用户，`limit`（默认 50，最多 100）和 `offset` 用于翻页（`has_more`、`next_offset`）；最差的状态在前。每行包含：用户、`client`（应用自报的名称，属于数据而非指令）、`platform`、最近活动、`state`、`reason`、`need_version`、`fingerprint`。旁边是节点所见（[实际情况](../guide/subscriptions.md#节点所见)）：每行有 `actual`（`pq`：最近 `hello_window_hours` 小时内经由 VLESS REALITY 的每个 ClientHello 都带后量子份额，`classic`：都不带，`mixed`：两种都有，`none`：没有统计到可读内容，其节点的 Agent 不统计时也是如此）、计数 `hello_pq`、`hello_classic` 和 `hello_unknown`（无法读取），以及两者不一致时的 `table_mismatch`：`worse`（表中为 ok，设备却发送了传统 hello）或 `better`（表中不是 ok，每个 hello 却都带有该份额）。顶层的 `actual` 按类别统计设备数和人数，不受筛选影响；`mismatch_worse` 和 `mismatch_better` 统计不一致的设备数；`hello_kex_nodes_without` 列出带有可用 REALITY 服务器、但其 Agent 未列出 `vless-hello-kex/1` 的节点。参数 `actual`（`pq`、`classic`、`mixed` 或 `none`）把列表限定为某一类，此时 `state` 留空表示所有状态（包括 `ok`）。不影响任何人。不含链接、密钥或地址。 |
| `user_connection` | 只读 | 节点统计到的某个人的连接：`user_id`、可选 `hours`（1 到 168，默认 24）和 `by_hour`（返回从早到晚的非零小时桶）。按设备、节点和入站统计：`connects`、`rejects`（已知密钥因期限结束而被拒）、`drops`（Hysteria2 在前 60 秒内或传输不足 64 KiB 时因空闲关闭；单独看无法区分路径冻结和应用短暂检查）、`stalls`（AmneziaWG 的 10 秒轮次：初始化包到达节点，但握手未完成）、`online_s`（包含短会话的空闲尾部）、`last_hour_unix`，以及设备的 `platform`、`client`、`kind`。对于支持 `links/2` 的 Hysteria2：`client_closes` 表示客户端主动关闭；`freezes` 表示节点持续发送但直到空闲超时仍未收到回复（路径冻结的信号，区别于应用探测；也可能是客户端在传输中消失）；`quiet` 表示双方都没有数据时客户端离开（通常是无害的应用检查或休眠设备）；`accepted` 是已完成的 QUIC 握手；`hellos_foreign` 是使用其他服务器名称的 ClientHello；这两个入站级计数不会归属到单个设备。`rtt_ms` 是 `rtt_samples` 个平滑 RTT 样本的均值；`loss_permille` 是节点判定每千个已发送包（`pkts_sent`）中丢失的包数。`hours` 包含同样的计数，零值字段会省略。AWG、VLESS 和不支持 `links/2` 的 Hysteria2 节点会将 `links/2` 字段置零，因此零值没有意义。使用 `links/3` 的 Agent 时，行中还有该密钥的握手原因：`tcp_stalls` 和 `tcp_stalls_16k`（基于 REALITY TCP 的 VLESS：客户端不再确认数据的连接，以及其中在 8–32 KiB 后停滞的，即 TSPU 冻结）和 `initiations`、`responses`、`responses_unmatched`（使用 userspace 后端的 AmneziaWG：到达节点的握手发起、节点的应答、设备始终未确认的应答）；`unsupported` 列出这五项中该行不可能有的（因协议、传输、AWG 后端或旧 Agent）。带 `vless-hello-kex/1` 的 Agent 的 VLESS REALITY 行还有 `hello_pq`、`hello_classic` 和 `hello_unknown`：设备的 ClientHello 按密钥交换分类（说明见 `fleet_connections`）。`nodes_without_counters` 列出可用但代理不发送 `links/1` 计数的节点。只有计数，不含地址和单次连接时间。 |
| `node_connections` | 只读 | 节点统计到的连接尝试：`node`（ID 或精确名称）、可选 `hours`（1 到 168，默认 24）和 `by_hour`（按时间升序返回非零小时桶）。对于 VLESS，支持 `vless-unknown-keys/1` 的 Agent 会报告 `unknown_keys`；旧 Agent 将其标记为不支持。按入站汇总所有密钥：`connects`、`rejects`、`drops`、`stalls`、`online_s`、`unknown_keys`、`hellos`、`handshakes`、`keys_stalled`、`keys_tried`、`unsupported`、`clients`（应用和平台，最多 8 个，按活动量排序）、`hours`；AWG 还可能有 `awg`（最近健康报告中的 UDP 包计数、报告时间、backend 和在线 peer）。对于支持 `links/2` 的 Hysteria2：`client_closes` 表示客户端主动关闭；`freezes` 表示节点持续发送但直到空闲超时仍未收到回复（路径冻结的信号，区别于应用探测；也可能是客户端在传输中消失）；`quiet` 表示双方都没有数据时客户端离开（通常是无害的应用检查或休眠设备）；`accepted` 是已完成的 QUIC 握手；`hellos_foreign` 是使用其他服务器名称的 ClientHello。`rtt_ms` 是 `rtt_samples` 个平滑 RTT 样本的均值；`loss_permille` 是节点判定每千个已发送包（`pkts_sent`）中丢失的包数。`unsupported` 列出此协议或代理不报告的计数器，这些零值没有意义。`supported` 为 false 表示代理不发送 `links/1`，此时 `inbounds` 为空。应用明细包含 `rtt_ms` 和 `loss_permille`，但固定格式不提供每个应用的 `rtt_samples` 或 `pkts_sent` 分母。使用 `links/3` 的 Agent 时，在引擎能看到的地方给出握手失败的原因：`hs_unfinished`（节点已应答 ClientHello，但握手始终未完成）、`tls_alerts_sent`、`tls_alerts_recv`、`tls_cert_refused`（应用拒绝了节点的证书：我方问题）、`reality_fallback` 和 `auth_failed`（VLESS REALITY：作为非客户端转给目标站点；使用本服务器名称但密钥或 short id 错误）、`tcp_stalls` 和 `tcp_stalls_16k`（VLESS），以及 `initiations`、`responses`、`responses_unmatched`（AmneziaWG，仅 userspace 后端：内核后端无法区分）；VLESS 还统计 `hellos`、`hellos_foreign`，基于 REALITY TCP 时还统计 `connects`。`unsupported` 也会列出 VLESS 传输或 AWG 后端看不到的计数。不含地址。 |
| `fleet_connections` | 只读 | 按名称汇总所有未退役节点的 `node_connections` 入站计数：可选 `hours`（1 到 168，默认 24）、`supported` 和 `inbounds`。有健康报告时，AWG 入站还会包含 `awg`。此汇总不含 `clients` 或小时桶。Drops 是 Hysteria2 前 60 秒内或 64 KiB 前的空闲关闭；单独看无法区分路径冻结和应用短检查。对于支持 `links/2` 的 Hysteria2：`client_closes` 表示客户端主动关闭；`freezes` 表示节点持续发送但直到空闲超时仍未收到回复（路径冻结的信号，区别于应用探测；也可能是客户端在传输中消失）；`quiet` 表示双方都没有数据时客户端离开（通常是无害的应用检查或休眠设备）；`accepted` 是已完成的 QUIC 握手；`hellos_foreign` 是使用其他服务器名称的 ClientHello。`rtt_ms` 是 `rtt_samples` 个平滑 RTT 样本的均值；`loss_permille` 是节点判定每千个已发送包（`pkts_sent`）中丢失的包数。Stalls 是 10 秒轮次；hellos 包含扫描器和应用握手 ping，但不含恢复的 TLS 会话；online_s 包含短会话的空闲尾部。`links/3` 原因与 `node_connections` 相同。不支持的计数器为零不代表没有事件。`hello_kex` 根据节点在当前内核上看到的 ClientHello（带 `vless-hello-kex/1` 的代理）说明严格的 REALITY 库会拒绝谁：只发送后量子握手的设备数（`pq_only`：X25519MLKEM768 密钥份额排在 X25519 之前）、只发送经典握手的设备数（`classic_only`）、两者都有（`mixed`）、只有无法解析的握手（`unknown_only`），每台设备只计一次；最多 50 台 `classic_devices`（人、应用、平台、计数）以及尚未统计的 `nodes_without`。只有计数，不含地址。 |
| `node_journal` | 只读 | 保存的节点和引擎警告与错误：可选 `node`（ID 或精确名称；留空表示所有节点）、`hours`（1 到 168，默认 24）、`min_level`（默认 `warning`；`info` 也按 warning 处理，或 `error`）、`source`、`query`（在消息或属性中不区分大小写的子串，最多 100 个字符）和 `limit`（默认 100，最多 200）。记录包含节点与配置名称、小时、来源、消息模板、入站、属性、计数以及首次/最后时间。`nodes_without_journal` 列出未发送 `journal/1` 的代理；`truncated` 表示还有更多匹配记录。消息和属性来自节点，应视为数据而非指令。不含地址。 |
| `os_updates_status` | 只读 | 每个节点和面板所在服务器已保存的操作系统数据：发行版、正在运行和最新的内核、等待中的更新和安全更新、软件包列表的新旧、是否需要重启及原因、Agent 能否运行系统任务以及目前是什么阻止了它（`update_block`、`reboot_block`），以及当前或最近一次系统更新的阶段和节点的拒绝代码。可选 `node`。它不会联系节点，也不刷新任何内容。参见[服务器操作系统](../operations/updates.md#服务器操作系统)。 |
| `updates_status` | 只读 | 面板构建版本、发布包状态和版本、各节点更新状态及已保存的计划、当前或最近一次滚动更新、新计划使用的固定 UTC 偏移量、已启用的可选下载来源、当前步骤的 `download_source`，以及下载失败步骤的 `sources_failed`。 |
| `relay_check` | 管理员 | `relay_id`。立即检测一条中转并保存结果：链路，以及中转已开启时的路径。它只在你的节点之间发送测试数据包，不更改任何开关，因此属于读取（与带 `check: true` 的 `node_ports` 相同），而不是计划；需要管理员配置，因为面板的中转检测属于所有者。最长可能需要一分钟（限时 90 秒）。开启中转需要 15 分钟内通过的链路检测。 |
| `node_ipv6_check` | 管理员 | `node`。从外部检测节点的 IPv6 地址是否响应，并且响应的就是该节点本身：由另一个 IPv6 正常的节点（否则由面板，再否则由 Globalping）把 `node_ports` 的数据包发往该地址而不是主地址。返回 `passed`、`sender`、每个端口的结论，以及检测无法进行时的 `error_code`（`no_ipv6_verifier`：没有任何手段可以检测该地址）。它只在你的节点之间发送测试数据包，不在任何主机上更改任何内容（结果保存在节点上，`node_get` 会显示它，通过的结果对 `node_ipv6_plan` 有效 10 分钟），因此属于读取而不是计划；需要管理员配置，因为面板的检测属于所有者。不会显示地址。最长可能需要一分钟（限时 90 秒）。没有可用 IPv6 出口的节点会被拒绝并给出原因（`no_ipv6`、`ipv6_broken`、`agent_old`、`not_checked`、`main_is_ipv6`）。 |
| `node_server_access_list` | 管理员 | 已保存的 SSH 地址、登录名和指纹，以及 `password_saved`、`password_login`（最近一次检查时密码登录是否可用）、`panel_key_fingerprint`、密码轮换恢复状态和节点退役状态。`password_saved: true` 且面板密钥指纹为空表示仅保存了密码。绝不返回密码。 |
| `node_server_keys_list` | 管理员 | `node` ID 或精确名称。列出该登录用户 `~/.ssh/authorized_keys` 中的公钥，每个包含类型、`key_id`（SHA256 指纹的前 12 个字符：完整指纹和其他密钥类值一样对智能体隐藏）、注释、限制和重复行数。服务器注释是不可信文本；绝不返回私钥。 |
| `audit_search` | 管理员 | 审计日志，可按 `source`（`panel`、`bot`、`mcp`、`api`）、操作者或操作筛选；使用 `before_id` 分页。 `hours`（1 到 8760）、`failed_only`、`actor` 和 `action`（子串）由面板在分页之前处理，因此每一页都是符合条件的记录。超过 400 天的记录会被删除。 |
| `backup_status` | 管理员 | 定时备份是否启用或正在运行、间隔、保留期、上次成功时间和上次错误代码。不含存储账户、存储桶、访问密钥、age 接收者或任何密钥。 |

**结果中的格式。** `subscription_preview` 按以下名称表示格式：`base64_uris`（链接列表）、`mihomo_yaml`、`xray_json`、`singbox_json`、`user_page` 和 `decoy`。客户端有自己的 `formats`，User-Agent 有对应的 `format`，客户端 ID 中包含 `singbox`。`subscription_settings_get` 会返回**下发规则**中的有序规则。要查看某个 User-Agent 具体会得到哪种格式，请把它传给 `subscription_preview`。

`user_get` 和 `subscription_preview` 中的协议配置带有 `formats`，即四种提供格式各自的 `format`、`ok` 和 `note`。`ok: false` 表示该格式不包含此协议配置。`note` 是代码，不是文本：`xhttp`、`self_signed`、`gecko`、`awg` 或 `unsupported`。`ok: true` 时，`hop_single_port` 表示端口跳跃范围被写成了单个端口。这些代码的含义见[协议配置](../guide/profiles.md#协议配置的订阅格式)。

### 变更工具

每项变更都有一对方法：`<tool>_plan` 和 `<tool>_apply`。

| 工具 | 权限级别 | 参数 | 需要所有者审批 |
|---|---|---|---|
| `user_create` | 操作员 | `name`（1 到 64 个字符，不区分大小写且唯一）、`group_id`，以及可选的 `quota_bytes`、`quota_reset`（`none`、`day`、`week`、`month`（默认值）、`rolling_month`）、`term_days`（最多 3650）、`device_limit`（最多 100）、`apps`（`happ`、`amnezia`、`telegram`）、`nodes`（`all` 或 `node_ids`）、`speed_limit_bps`（每人：其所有设备在同一节点上合计）、`device_speed_limit_bps`（每台设备）、`dns_preset_id` | 否 |
| `user_update` | 操作员 | `user_id`，可选的 `subscription_name`（最多 64 个字符；为空时使用 `name`），以及需要更改的字段（同上，但使用 `expires_unix` 替代 `term_days`；计划会同时写明两种限速：每人在单个节点上的限速和每台设备的限速） | 否 |
| `user_disable` | 操作员 | `user_ids`（1 到 50） | 超过 3 位用户时 |
| `user_enable` | 操作员 | `user_ids`（1 到 50） | 否 |
| `user_reset_traffic` | 操作员 | `user_ids`（1 到 50） | 超过 3 位用户时 |
| `device_revoke` | 操作员 | `user_id`、`device_id`，可选 `block`（在所有者于管理后台解除拉黑前，这个设备 ID 无法再次注册；报告其他 ID 的应用算作新设备；只有会发送设备 ID 的应用才能被拉黑） | 否 |
| `alert_mute` | 操作员 | `alert_id`、`duration_s`（最多 604800；0 表示取消静默） | 否 |
| `alert_close` | 操作员 | `alert_id`；关闭告警且不发送通知，同一问题持续期间保持关闭 | 否 |
| `subscription_app_upsert` | 操作员 | `platform`（`ios`、`android`、`windows`、`macos`、`linux`）和 `name` 用于指定应用；更改现有应用时只传入变更字段；新应用至少要传入 `kind`：`kind`（`happ` 接收订阅链接，`amnezia` 接收 AmneziaWG 密钥）、`download_url`、`add_link_template`（占位符 `{url}`、`{url_enc}`、`{name_enc}`）、`description`（最多 80 个字符）、`recommended` | 始终 |
| `subscription_app_remove` | 操作员 | `platform`、`name` | 始终 |
| `subscription_rules_set` | 操作员 | 完整的有序 `rules` 列表，每项为 `ua_contains` 加 `format`（`base64_uris`、`user_page`、`decoy`、`mihomo_yaml`、`xray_json`、`singbox_json`）；第一个匹配项生效 | 始终 |
| `subscription_texts_set` | 操作员 | 仅需更改的字段：`title`、`announcement`、`support_url`、`update_interval_hours`、`server_name_template` | 始终 |
| `subscription_link_limits_set` | 操作员 | `mode`（`track`、`enforce`、`strict`）、`link_awg`（在链接应用中包含 AmneziaWG）、`link_auto_select`（在 Mihomo、sing-box 和 Xray JSON 中把 Auto 放在最前），或任意组合。严格模式的计划会报告将被拒绝的共享链接用户；关闭 `link_awg` 会报告下次拉取时将被移除的链接密钥（Mistgate 应用的密钥不在其中） | 始终 |
| `subscription_page_set` | 操作员 | 仅需更改的字段：`show_announcement`、`show_support`、`show_qr`、`device_self_service`、`page_password`、`dns_choice`、`happ_ping`（`off`、`tcp`）和 `happ_ping_on_open` | 始终 |
| `dns_settings_set` | 操作员 | `default_preset_id`、有序的 `link_preset_ids` 和 `link_default_preset_id`；只需提供要更改的字段 | 始终 |
| `group_create` | 操作员 | `name`，可选 `profile_ids`、`dns_preset_id`（留空则使用实例默认值）、`color` 和 `disabled_node_ids`。分组没有备注和配额字段 | 否 |
| `group_update` | 操作员 | `group_id` 以及仅需更改的字段：`name`、`profile_ids`、`dns_preset_id`、`color`、`disabled_node_ids` | 始终 |
| `group_delete` | 操作员 | 空分组的 `group_id`。仍有成员的分组会被拒绝；请先用 `user_update` 移动成员 | 否 |
| `dns_preset_create` | 管理员 | `name`、`description`、`servers`，可选 `split`、`ipv4_only`、`split_direct`、`preferred_transport`（`plain`、`dot`、`doh`）、`block_ads` | 始终 |
| `dns_preset_update` | 管理员 | `preset_id` 以及可编辑字段：`name`、`description`、`servers`、`split`、`ipv4_only`、`split_direct`、`preferred_transport`、`block_ads` | 始终 |
| `dns_preset_delete` | 管理员 | `preset_id`；内置预设和实例默认预设会被拒绝，用户和分组将回退到实例默认值 | 始终 |
| `profile_create` | 管理员 | 计划：`protocol`、`name`、可选 JSON `settings`（缺少的字段使用协议默认值）、`node_ids`、现有 `group_ids`、`port_overrides`（节点 ID 到端口）、`allow_lossy_port`；执行：仅 `confirm_token`。对于标记为 `x-secret` 的敏感字段，省略该字段、使用 `"••••"` 保留原值，或使用 `"$generate"`；不得传入明文值 | 始终 |
| `profile_update` | 管理员 | 计划：`profile_id`、可选 `name`、RFC 7386 JSON Merge Patch `settings_patch`（省略的敏感字段会保留原值；拒绝补丁中的 `null` 值）、`allow_lossy_port`；执行：仅 `confirm_token`。敏感字段遵循相同规则：省略、`"••••"` 或 `"$generate"` | 始终 |
| `profile_deploy` | 管理员 | 计划：`profile_id`、`add_node_ids`、`remove_node_ids`、现有 `add_group_ids`、`remove_group_ids`，可选 `port_overrides` 和 `allow_lossy_port`；执行：仅 `confirm_token` | 始终 |
| `node_fix` | 管理员 | `node`、诊断报告中的 `fix_id`；检查项列出参数时还需提供 `params` | 始终 |
| `node_decoy` | 管理员 | `node`、`mode`（`builtin`、`panel`、`own`）、`bundle_base64`（最多 2 MiB 的 base64 ZIP 或 TAR；`own` 模式下必需） | 始终 |
| `node_update` | 管理员 | `node` 以及仅需更改的字段：`name`、`country_code`、`location`、`provider`、`notes`，以及各入站的 `direct_in_subscriptions` 开关。不接受地址和运行时设置 | 始终 |
| `node_ipv6` | 管理员 | 计划：`node`、`action`（`enable` 或 `disable`）；应用：仅 `confirm_token`。开启或关闭节点的 [IPv6 入口](../guide/nodes.md#ipv6-入口)：在每个人的订阅里，节点的每个服务器旁多一个服务器「<节点> · IPv6」。`enable` 会先从外部检测该地址（参见 `node_ipv6_check`），只有通过才开启入口；无法接受入口的节点在计划阶段就会被拒绝并给出原因。计划会写明节点以及哪些协议配置的服务器会得到副本，绝不显示地址；该地址就是服务器用于出站 IPv6 的那个。 | 始终 |
| `node_awg_backend` | 操作员 | 计划：`node`、`backend`（`auto`、`kernel` 或 `userspace`）；应用：仅 `confirm_token`。设置节点运行 AmneziaWG 的方式。计划会显示当前设置、现在运行的后端、节点是否处于[用户态回退](../guide/amneziawg.md#模块无法加载时)、在线设备数、重连提示（接口会重建，设备约 20 秒内重新连接），以及节点切到用户态时的内存估算（每台设备 26 KiB）。它不会构建内核模块。Agent 太旧、无法遵循该设置的节点会在应用时被拒绝。 | 始终 |
| `path_speed` | 管理员 | 计划：`from`、`to`（节点 id 或完整名称），可选 `mb`（8 到 32，默认 16）和 `direction`（`down` 表示 `from` 从 `to` 拉取数据，默认；`up` 表示推送）；应用：仅 `confirm_token`。两个节点之间有上限的吞吐测试：`to` 为一个连接打开短时 TCP 监听（一次性令牌，最多 30 秒后关闭）并提供数据，`from` 传输并测量。应用结果给出整个传输的 Mbit/s、连接时间、首字节时间（`down`）和时长，保留 7 天。它会让大量流量经过两个节点的链路，所以总是等待所有者批准，就像面板自己的带宽测试仅限所有者一样。上限：最多 32 MB 和 30 秒，每个节点同一时间一次测试，每个节点每天 6 次、每个令牌每天 12 次。目标只能是机群节点，绝不是公共服务器；两个代理都需要 `speed/1`（否则 `agent_too_old`），且目标的测试端口必须能被发送方访问。计划和结果只显示节点名称，不显示地址。Cloudflare 版本不可用。 | 总是 |
| `warp_restart` | 管理员 | `node` ID 或精确名称；计划会列出使用 WARP 出口且可能中断几秒的协议配置；执行时只接收 `confirm_token` | 否 |
| `warp_reregister` | 管理员 | `node` ID 或精确名称，以及可选的 `reason`（最多 300 个字符）；执行时只接收 `confirm_token`。计划会显示 Cloudflare 服务条款链接；所有者批准即表示代表此账户接受条款，并将其替换为新的匿名设备，从而更改 WARP 出口 IP | 始终 |
| `relay` | 管理员 | 计划参数：`action` 及其参数；执行时只接收 `confirm_token`。`create`：`node`（入口，ID 或精确名称）、`inbound_id`（来自 `relay_candidates`），以及可选的 `port`（443 或 1024 到 65535；省略时，若出口的端口在入口上空闲则用它，否则用一个空闲端口）。`enable`、`disable` 和 `delete`：`relay_id`。`port`：`relay_id` 和 `port`。计划会不带地址地说明这项更改：入口、出口、服务器、端口、国家检测如何看待两端、最近一次检测，以及用户会看到什么 | 始终 |
| `rollout_start` | 管理员 | 计划：`nodes`（`updates_status` 中 1 到 20 个节点 ID；也接受旧参数 `node_ids`，但不能同时提供）；执行：仅 `confirm_token`。计划显示受信任的签名版本、金丝雀顺序（在线人数较少优先，然后是入站较少）、批次大小，以及每个节点的名称、Agent 版本和在线人数；计划固定精确节点集合及发布包版本/构建号 | 始终（step-up） |
| `os_rollout_start` | 管理员 | 计划：`node_ids`（`os_updates_status` 中的 1–20 个 id）、`mode`（`security`、`all`：安装普通软件包和安全更新，或 `reboot_only`）、可选 `batch_size`（1–5，默认 1）和 `reboot`（软件包模式用 `never` 或 `if_required`；`reboot_only` 用 `if_required` 或 `always`）；应用：仅 `confirm_token`。计划固定 id、模式、重启策略和批次大小。计划显示金丝雀和顺序（先在线人数较少，再 inbound 较少），以及每个不可信节点名称、待处理/安全更新、下一个内核、是否需要重启、在线人数和重启阻塞原因；还会列出当前会被跳过的节点。会警告 unattended-upgrades 自动重启和缺失的 AmneziaWG 模块。 | 始终（step-up） |
| `os_rollout_cancel` | 管理员 | `os_updates_status` 中的 `rollout_id`。关闭已停止的系统更新（其告警随之清除）或尚未开始的更新；节点上正在运行的任务无法停止，会被拒绝 | 始终（step-up） |
| `os_rollout_pause` | 管理员 | 计划：`os_updates_status` 中的 `rollout_id`；应用：仅 `confirm_token`。已在节点上运行的任务会完成并通过健康检查后再暂停；后续任务等待 | 始终（step-up） |
| `os_rollout_resume` | 管理员 | 计划：`rollout_id`，可选 `skip_failed`；应用：仅 `confirm_token`。默认将失败步骤重试一次；设置 `skip_failed` 则跳过失败步骤并继续 | 始终（step-up） |
| `node_update_schedule` | 管理员 | 计划参数：`node_id`、`local_datetime`（使用 `updates_status` 中的偏移量，格式为 `YYYY-MM-DDTHH:mm`，至少提前一分钟且最多提前一年）；计划会固定受信任的版本和偏移量 | 始终 |
| `node_update_schedule_cancel` | 管理员 | `node_id` | 始终 |
| `update_timezone` | 管理员 | `timezone_offset_minutes`（UTC 固定偏移量，范围为 -720 到 840，每次递增 15 分钟；`180` 表示 GMT+3） | 始终 |
| `update_download_timeout` | 管理员 | `timeout_seconds`（300 到 7200 秒；只影响新发送的更新，已经开始的步骤保留发送时捕获的窗口） | 始终 |
| `log_level` | 管理员 | `level`（`debug`、`info`、`warn` 或 `error`）。设置面板的日志级别，直到下次重启，之后回到 `--log-level` / `MISTGATE_LOG_LEVEL`。不删除任何内容。 | 否 |
| `node_debug_capture` | 管理员 | `node`、`level`（`debug` 或 `info`，默认 `debug`）、`sources`（最多 8 个日志来源，如 `agent`、`hysteria2`、`xray`、`awg`、`kernel`；为空表示全部）、`duration_s`（1 至 900，默认 300）。节点必须在线，且其 Agent 声明了 `debug/1`；每个节点同一时间只有一个抓取。节点为这些来源提高日志级别，保存最多 20000 条记录（4 MiB），时间到了把清理后的记录发出，并自行恢复级别。用 `node_debug_logs` 读取结果。写入审计日志：谁请求的、节点、来源和时长。 `kernel` 来源会读取从抓取开始前 30 分钟到抓取结束期间的内核日志；防火墙数据包日志只计数，不保存。对于使用内核后端的 AWG 节点，选择 `awg` 会在抓取期间启用模块调试，并在摘要中列出 5 秒内未得到响应的握手发起次数。`kernel` 来源需要 `debug/2`，其他来源仍使用 `debug/1`。| 否 |
| `retention` | 管理员 | `values`：设置名称及其新值（名称、单位和范围见 `retention_status`）。更短的时间会在下一次每小时清理时永久删除更早的行。 | 总是（需 step-up） |
| `task_create` | 管理员 | 计划：`name`、`kind`（`check` 或 `assistant`）、`schedule`（`every 15m`、`every 2h`、`daily 07:30`、`weekly mon 07:30`，按面板时区；检查任务最多每 5 分钟一次，助手任务每 15 分钟一次），检查任务的 `steps`（1 到 8 步：`tool`、`args`、由 `path`、`op` 和 `value` 组成的 `conditions`、`error_is_problem`），助手任务的 `prompt`（最多 4000 个字符），`notify_telegram`、`disabled`；apply：只需 `confirm_token`。计划按面板自己的规则检查任务：检查可以调用哪些工具（操作员配置的读取和探测工具，不含计划、apply 和任务工具）、按各工具的 schema 检查参数、条件、最小间隔，以及名称、参数和提示中没有机密（`secret_in_task`）。最多 50 个任务。任务属于所有者，其运行使用操作员配置。 | 总是 |
| `task_update` | 管理员 | 计划：`task_id` 和要修改的字段（`name`、`schedule`、`steps` 替换全部旧步骤、`prompt`、`notify_telegram`、`enabled`）；类型不变。修改后的任务从干净状态开始：已打开的告警关闭。若在 apply 之前任务已在面板中被修改，apply 返回 `changed_since_plan`。 | 总是 |
| `task_delete` | 管理员 | 计划：`task_id`。删除任务及其运行记录，已打开的告警关闭；助手任务的对话仍归所有者。 | 总是 |
| `rollout_pause`, `rollout_resume`, `rollout_cancel` | 管理员 | `updates_status` 中的 `rollout_id` | 始终 |
| `node_rollback` | 管理员 | `node` | 始终 |
| `node_install` | 管理员 | 计划参数：`host`、`port`（默认 22）、`username`（root 或具有免密 sudo 权限的用户）、节点 `name`（2 到 24 个字母、数字或连字符）、`address`，以及可选的 `country_code`、`location`、`provider`；执行时只接收 `confirm_token`。计划只读取服务器的公开主机密钥。所有者在审批页面确认主机密钥，并选择面板密钥或输入 SSH 密码；密码安装默认同时添加面板密钥 | 始终 |
| `node_server_password_rotate` | 管理员 | 计划参数：运行中节点的 `node` ID 或精确名称（已退役的节点会被拒绝）；执行时只接收 `confirm_token`。面板自行生成新密码，绝不返回密码；所有者可在节点的**服务器**标签页中查看 | 始终 |
| `node_server_key_add` | 管理员 | 计划参数：`node`，以及一行公开 `public_key` 或 `panel_key: true`；执行时只接收 `confirm_token`。所有者先审批密钥类型、指纹和不可信注释。绝不接受私钥。 | 始终 |
| `node_server_keys_add` | 管理员 | 计划：`nodes`（1 到 20 个节点 ID）以及一行公开 `public_key` 或 `panel_key: true`；执行：仅 `confirm_token`。所有者一次审批覆盖所有节点。计划显示密钥指纹、不可信注释，以及已存在该密钥并会跳过的节点；执行结果逐节点返回 `added`、`already_present` 或带错误代码的 `failed`。绝不接受私钥 | 始终（step-up） |
| `node_server_key_remove` | 管理员 | 计划参数：`node` 和 `key`，即 `node_server_keys_list` 返回的 `key_id`（或完整的 SHA256 指纹）；执行时只接收 `confirm_token`。只有面板确认已保存密码可用后，才能移除面板密钥。 | 始终 |

对于 `create` 和 `enable`，`relay_apply` 遵循面板自己的顺序：先创建中转（关闭状态），检测链路，再开启，然后由面板从外部检测路径。检测未通过的中转保持关闭，执行失败并给出阶段（`leg` 或 `path`）及其代码；迁移已开启中转的端口会重新检测路径，失败则关闭中转。第二个服务器会在订阅下次刷新时出现或消失。请参阅[中转](../guide/relays.md)。

每个 `_plan` 还接收 `reason`：由智能体自行撰写，最多 300 个字符，并以引文形式显示给所有者。没有工具会接收或返回服务器密码：所有者在审批页面选择面板密钥或输入安装密码，轮换后的密码由面板自行生成。`user_create` 绝不会返回新用户的订阅链接；所有者需在管理后台中复制链接。

`os_rollout_start_plan` 支持 1 到 20 个节点，并须经所有者批准后才会安装或重启。金丝雀是在线人数最少、其次 inbound 最少的节点；只有上一批通过检查，才会启动下一批。`busy`、`apt_lock`、`too_soon` 和 `reboot_limit` 会等待并重试最多两小时，然后以 `try_later` 跳过；硬性阻塞会立即跳过。真实失败会暂停运行。继续时会重试失败步骤一次，或用 `skip_failed` 跳过并继续；取消不会中断节点上正在运行的任务。节点重启时，其上的人会断线约两分钟。不能绕过节点自己的检查。面板服务器不在此功能范围内。

`rollout_start_plan` 会分阶段立即更新 1 到 20 个选定节点。每个节点都必须运行支持自更新且早于签名发布包的在线 Agent（版本过旧、更新失败或已回滚）；已有滚动更新正在运行或暂停时会拒绝启动。面板先选择在线人数最少的节点作为金丝雀，然后按入站数排序；少于 5 个节点时批次大小为 1，否则为 2。计划固定精确节点集合以及签名发布包的版本和构建号；如果选定节点或发布包在执行前发生变化，执行会失败，需要重新制定计划。所有者批准后，`node_update_schedule_plan` 会为一个节点保存未来的更新计划；制定计划时节点可以离线，新计划会替换该节点的旧计划。保存的任务会固定到相应签名发布版本和 UTC 固定偏移量。到期时如果节点离线，面板最多等待两小时让节点重新连接，之后会将任务标记为错过，绝不会自行启动任务；若签名发布包发生变化，面板会保留任务并继续显示，不会替换为其他发布版本。使用 `node_update_schedule_cancel_plan` 可取消待执行任务。更改 `update_timezone` 只影响新计划；现有任务会保留已保存的时间点和偏移量。`update_download_timeout` 只改变新发送步骤的下载窗口；支持该能力的 Agent 会在命令中收到新值，旧 Agent 继续使用内置限制，已经开始的步骤会保留发送时捕获的窗口。

`subscription_app_upsert` 和 `subscription_app_remove` 会更改每位用户都能看到的页面上的一个应用，按平台和名称（不区分大小写）查找（同一平台上有两个应用同名时会拒绝执行：请在管理后台中更改）。新应用会添加到列表末尾。字段值的验证方式与保存**订阅 → 用户页面**时完全相同：下载链接必须是 http(s)；添加链接模板必须以应用自己的协议开头（`happ://add/{url}`、`myclient://add?url={url_enc}&name={name_enc}`），或使用 http(s)，不得使用 `javascript:`、`data:` 等协议；只能使用已知占位符；应用最多 30 个。此外，智能体提供的文本不能包含不可见格式字符。计划会读取并记住完整设置的状态；如果在执行前有人保存了订阅设置，执行会失败并返回「the subscription settings changed after the plan」，不会覆盖任何内容：请重新制定计划。在 `subscription_settings_get` 和智能体收到的计划副本中，链接查询字符串和较长的路径片段会显示为 `[redacted]`，与其他结果相同；所有者审批卡片会显示完整值。

Telegram 代理是 `user_create` 和 `user_update` 的 `apps` 中的三个开关之一。省略的字段保持用户原有的值，所以 `{"apps": {"telegram": true}}` 只更改 Telegram。开启会生成用户的 Telegram 密钥，关闭则吊销它；计划会把 Telegram 代理列在应用之中。`happ` 和 `amnezia` 中至少要保持开启一个：Telegram 是额外增加的。密钥本身没有对应的工具，也没有工具返回 `tg://proxy` 链接：重新签发或吊销密钥，以及读取链接，都由所有者在管理后台中完成（[MTProto](../guide/mtproto.md)）。

Mistgate 应用是 `apps` 的第四个字段（`mistgate`，默认开启）。只有 `happ` 开启时它才会工作。计划会把它作为单独的事实 `app_mistgate` 列出，所需的审批与其他用户更改相同。`client_api_settings` 还会读取有多少人开启了应用、有多少设备已用密钥连接（`people`、`people_on`、`devices`）。

## 制定计划并执行

对于某些在任何操作开始前发生的临时拒绝，计划会保持 `planned` 或 `approved` 状态，直到原定到期时间；响应会说明 `busy`、原因代码以及何时可以使用同一个 `confirm_token` 重试。计划到期时间不会延长，成功执行的计划不能再次执行，其他失败仍会将计划标记为 `failed`。

1. 智能体使用参数调用 `<tool>_plan`。面板会验证参数、读取所需信息，并用自己的措辞描述变更。此时不会产生任何变更。返回结果：

   ```json
   {
     "plan_id": "pln_...",
     "summary": "Disable 5 users: their connections end and their devices are dropped from the nodes.",
     "facts": [{"key": "count", "value": "5"}, {"key": "users", "value": "...", "untrusted": true}],
     "needs_approval": true,
     "danger": ["bulk"],
     "expires_in_s": 600,
     "confirm_token": "cf_...",
     "next": "..."
   }
   ```

2. 智能体向服务对象展示计划并等待对方同意。若 `needs_approval` 为 true，还需等待所有者批准。
3. 智能体只使用 `confirm_token` 调用 `<tool>_apply`。面板会按照已存储的参数执行一次，并返回 `plan_id`、`status: "applied"` 和一行结果。

确认令牌：

- 只能使用一次，有效期为 10 分钟，并且只适用于制定该计划的令牌和对应工具；
- 不包含参数：从制定计划到执行计划，参数不会改变；
- 再次执行已完成的计划会返回相同结果；其他计划、工具或 API 令牌的确认令牌会返回「unknown confirm token」。

执行失败时会说明原因：「waiting for the owner to approve plan pln_…; it expires at …」、「rejected by the owner」、「expired: make a new plan」、「already running」、「failed: …. Make a new plan.」、「the token was revoked」或「timeout, outcome unknown: check before retrying」。

### 哪些计划需要等待所有者审批

符合以下任一条件时，计划需要所有者审批（`danger` 列表）：

| 代码 | 含义 | 工具 |
|---|---|---|
| `step_up` | 操作本身要求在管理后台再次确认。 | 滚动更新工具、`os_rollout_start`、`os_rollout_cancel`、`os_rollout_pause`、`os_rollout_resume`、`node_rollback`、`node_update_schedule` 及其取消工具、`node_awg_backend`、`update_timezone`、`update_download_timeout`、`retention`、`node_install`、`node_server_password_rotate`、`node_server_key_add`、`node_server_keys_add`、`node_server_key_remove` |
| `fleet` | 会更改节点上运行的内容或移除用户访问权限。 | `node_fix`、`node_decoy`、`node_ipv6`、`relay`、`group_update`、滚动更新工具、`os_rollout_start`、`os_rollout_cancel`、`os_rollout_pause`、`os_rollout_resume`、`node_rollback`、`node_update_schedule` 及其取消工具、`node_install`、`node_server_password_rotate`、`node_server_key_add`、`node_server_keys_add`、`node_server_key_remove`；所有协议配置创建、更新和部署计划 |
| `bulk` | 一个计划影响多个目标，或一次影响超过 3 位用户。 | 多节点 `rollout_start`、多节点 `node_server_keys_add`、`user_disable`、`user_reset_traffic` |
| `user_change_limit` | 加上此计划，令牌将超出每小时用户更改限额（10 次，见[限制](api.md#限制)），因此计划不会被拒绝，而是等待所有者批准。卡片会显示限额已用多少。 | `user_disable`、`user_reset_traffic`、`user_update`、`device_revoke` |
| `traffic` | 它会在节点之间发送大量测试流量。 | `path_speed` |
| `user_page` | 会更改用户能看到或收到的共享订阅设置或数据。 | `subscription_app_upsert`、`subscription_app_remove`、`subscription_rules_set`、`subscription_texts_set`、`subscription_link_limits_set`、`subscription_page_set`、`dns_settings_set`、`dns_preset_create`、`dns_preset_update`、`dns_preset_delete`、`node_update` |
| `schedule` | 会让面板自动地一次又一次运行工具或助手。 | `task_create`、`task_update`、`task_delete` |

`group_update` 使用 `fleet`，因为更改分组的协议配置或禁用节点可能改变其成员的访问权限。分组中仍有成员时，`group_delete` 会拒绝执行。`node_update` 仅限元数据和面向订阅的开关；它的所有更改都需要所有者审批，因为底层的节点更新 API 还接受会改变运行状态的字段，而此工具不开放这些字段。

计划会在保存前验证设置和新增节点。每项协议配置变更都需要所有者在管理后台审批。`profile_update` 和 `profile_deploy` 使用创建计划时读取的配置版本；执行前版本如有变化，会返回 `changed_since_plan`。部署时会将分组关联与其当前状态合并；后续步骤失败时，响应会报告已完成的步骤。`profile_create` 失败时会回滚新建配置和入站；删除配置时也会一并移除分组关联。敏感值不会出现在配置读取结果、计划事实、存储的计划参数或审计记录中。

### 所有者在哪里审批

此类计划会显示在管理后台的 **AI → 等待你处理** 中，并带有角标；如果所有者已绑定 Telegram，面板还会发送一条计划等待审批的通知，其中包含指向该页面的链接（只能在管理后台中作出决定：参见[Telegram 告警](../operations/telegram-alerts.md)）。所有者会看到面板对变更的描述、风险提示、智能体的理由（标记为「由智能体撰写，面板未做核实。」）和剩余时间，然后选择：

- **批准**：要求所有者再次使用通行密钥或身份验证器验证码。之后智能体只能执行此计划。
- **拒绝**：智能体执行计划时会失败，并返回「rejected by the owner」。

对于 `node_install`，审批卡片会显示面板读取到的 SHA-256 主机密钥指纹和密钥类型（智能体收到的计划副本会遮蔽指纹，与其他密钥类值相同）。所有者需要将它与可信副本比较、勾选确认，并选择面板如何登录：**面板密钥**（卡片会显示面板的公钥，该公钥必须已在服务器上；计划指明使用面板密钥时才会出现这个选项）或**密码**（所有者在此处输入服务器的 SSH 密码；安装时默认还会把面板密钥添加到服务器，此后面板通过密钥登录）。在主机密钥确认之前，以及使用密码时在密码输入之前，**批准**按钮会保持禁用。面板会将这一选择（以及输入的密码）密封到该计划中；智能体执行时只携带确认令牌，并且只会使用该选择和所有者确认过的主机密钥安装一次。密码绝不会经过智能体、其对话记录或模型服务商。密码轮换也有相同的所有者审批边界：面板生成新密码并且绝不返回。

令牌永远不能批准自己制定的计划。未作决定的计划会在创建 10 分钟后过期。**最近的决定**会保留审批记录和结果：已完成、出错、已过期、已取消（令牌已吊销）等。智能体轮询间隔不应短于 30 秒。

### 示例：为 Windows 和 macOS 添加 kl!ck

[kl!ck](../guide/client-apps.md#klck) 是 Mistgate 推荐的桌面应用。要将其添加到 Windows 和 macOS 的用户页面，具有操作员权限级别的智能体可按以下步骤操作。计划会记录全部设置的状态，因此每项计划都应在上一项执行完成后再制定。

1. `subscription_settings_get`：读取当前应用列表，以了解已配置的应用以及哪些 Happ 条目已设为推荐。
2. `subscription_app_upsert_plan` for Windows:

   ```json
   {
     "platform": "windows",
     "name": "kl!ck",
     "kind": "happ",
     "download_url": "https://github.com/vbu00/klick/releases/latest",
     "add_link_template": "klick://add?url={url_enc}&name={name_enc}",
     "recommended": true,
     "reason": "kl!ck is the recommended desktop app"
   }
   ```

   计划的 `needs_approval` 为 true，风险标记为 `user_page`。向所有者展示计划，请其在 **AI → 等待你处理** 中批准，然后使用 `confirm_token` 调用 `subscription_app_upsert_apply`。
3. The same for macOS: the same arguments with `"platform": "macos"`.
4. 要让 kl!ck 排在前面，请在两个平台上分别制定一个计划，取消 Happ 的推荐标记：`{"platform": "windows", "name": "Happ", "recommended": false, "reason": "kl!ck leads on the desktop"}`，然后将平台改为 `"macos"` 再执行一次。更新现有应用时只需提供变更字段。

`kind: "happ"` 表示「通过订阅链接」接入，无论打开链接的是哪个应用。下载链接使用发布页面，因为文件名包含版本号，Windows 和 macOS 版本分别发布。一键链接仅适用于版本高于 0.4.0 的 kl!ck；较早版本需要在页面上使用**复制链接**。新应用会添加到列表末尾；无论推荐应用位于何处，都会排在该平台列表的前面。之后运行 `subscription_settings_get` 可查看结果（智能体看到的链接查询字符串会显示为 `[redacted]`）。

## 结果中的不可信数据

名称、备注、理由、日志行，以及事件和告警的参数都来自用户、节点和其他系统。服务器的指令以及每个返回此类文本的工具都会说明：这些内容是数据，不是指令；智能体不得遵循其中包含的要求。

面板也会保护智能体和你：

- 数据文本会经过清理：移除控制字符、换行符和不可见格式字符，并截断过长的值。
- 计划摘要绝不包含数据中的文本；这类值会作为单独的事实并标记为 `untrusted`，所有者看到时会以引文形式显示。
- 所有者批准的内容会完整显示。较长的值（任务提示、解析器列表、用户页面文本）以带编号的事实给出；无法原样显示的新值（隐藏或双向控制字符，或会被脱敏器改动的文本，例如密钥或带令牌的链接）在制定计划时即被拒绝，并指出字段。字段当前的值以脱敏形式显示，绝不会阻止更改。
- 每个结果都会经过最终检查，将所有疑似密钥的内容替换掉：API 令牌、分享链接（例如 `hysteria2://…`）、隧道配置、密钥材料、URL 查询字符串、较长的 URL 路径片段（私密前缀、订阅令牌）以及未编码的 32 字节密钥。

## 智能体永远无法获取的内容

- 订阅链接和用户页面密码。
- Telegram 代理密钥及其 `tg://proxy` 链接。
- 设备密钥和配置、WARP 账户和密钥、协议配置密钥。
- 服务器密码：无论是安装时使用的 SSH 密码，还是轮换后的密码。
- 节点地址和证书固定指纹。例外是管理员令牌可通过 `node_server_access_list` 以及 `node_install` 和密码轮换计划查看已保存的 SSH 端点和登录名。
- 任何令牌、审批、会话和通行密钥相关内容。
- 除非令牌具有管理员权限级别，否则无法访问审计日志。

## 审计

工具调用的每个方法都会以「MCP 令牌 <name>」作为操作者写入审计日志，来源为 MCP。计划和执行过程也会各自增加日志项：「制定了变更计划：<tool>」和「执行了已制定的变更计划：<tool>」；所有者的决定则记录为「批准了变更：<tool>」或「拒绝了变更：<tool>」。可在**设置 → 审计**中按 MCP 来源筛选。








