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

**集成 → MCP** 会显示端点地址和适用于各类客户端的配置片段。

## 连接客户端

在 **集成 → API 令牌** 中创建令牌，并选择能够满足需求的最低权限级别（参见[令牌权限级别](api.md)）。然后：

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
| `node_get` | 只读 | 单个节点：状态和原因、主机信息、协议配置及其状态、最多 10 位在线用户、所有者备注。不含地址、密钥或证书固定指纹。 |
| `warp_status` | 只读 | 单个节点是否有 WARP 账户，以及账户是否已启用或暂停；Agent 状态、colo、距上次握手的时间、Cloudflare 和其他站点的探测状态与延迟、注册时间、是否有刷新令牌，以及经 WARP 出口的协议配置。不含地址、密钥、账户 ID、许可证或令牌。 |
| `node_metrics` | 只读 | 节点当前的 CPU、RAM、磁盘和网络数据、今日流量、各协议的在线用户，以及今日流量最高的用户。 |
| `node_doctor` | 只读 | 单个节点或所有节点最近保存的诊断报告，以及修复 ID。从操作员权限级别起，还可设置 `refresh: true`：让节点立即运行检查（只读取主机信息，最多等待 30 s）。 |
| `users_search` | 只读 | 按名称片段、`filter`（`online`、`expiring`、`over_quota`）或 `group_id` 搜索用户；使用 `page_token` 分页。 |
| `groups_list` | 只读 | 分组及其协议配置 ID、用户数量和 DNS 预设。 |
| `user_get` | 只读 | 单个用户：限制（含两种限速）、状态、设备、协议配置（每个都带有包含它的订阅格式）和节点访问权限。不含订阅链接或密钥。 |
| `user_traffic` | 只读 | 已用流量和配额、最近 14 天的数据，以及按节点和协议划分的流量。 |
| `user_devices` | 只读 | 设备的类型、平台、型号、系统版本、应用、流量、最后在线时间、在线状态及所在节点、设备 ID 的简短指纹；AmneziaWG 还会返回协议配置和隧道地址；已拉黑的设备单独列出。绝不返回密钥或配置文件。 |
| `subscription_preview` | 只读 | 客户端（客户端 ID 或 User-Agent）会获取的格式，以及该用户有权访问的协议配置（每个都带有包含它的订阅格式）和节点。不返回订阅内容或链接。 |
| `subscription_settings_get` | 只读 | 每位用户看到的订阅页面：各平台上的应用及其显示顺序（名称、类型、下载链接、添加链接模板、说明、是否推荐）、页面选项（包括 `dns_choice`，即页面上的 DNS 选择开关）、服务器名称模板，以及订阅标题、公告、联系链接和更新间隔。不含任何用户的链接。 |
| `alerts_list` | 只读 | 活跃告警；设置 `include_history` 后也会返回已关闭的告警（`window_s` 最长为 30 天）。 |
| `events_search` | 只读 | 按节点、用户、`min_severity`（`info`、`warning`、`error`）或精确 `code` 搜索事件流；使用 `before_id` 分页。 |
| `checks_results` | 只读 | 客户端视角检测：按协议配置列出节点、最近一次结果、连续失败次数和最近 24 小时的历史记录。含通过已建立隧道的响应时间、连接时间和最近一小时的中位数（均在面板服务器上测得）；面板主机丢失入站 UDP 时返回 `udp_loss`。开启国家视角检测后（见[健康](../operations/health.md#国家视角检测)），每行带有 `country_check`（该国境内探测点给出的最新结论及所用网络）。 |
| `updates_status` | 只读 | 面板构建版本、发布包状态和版本、各节点更新状态及已保存的计划、当前或最近一次滚动更新，以及新计划使用的固定 UTC 偏移量。 |
| `node_server_access_list` | 管理员 | 节点已保存的 SSH 端点、登录名和指纹，以及密码轮换是否需要恢复、节点是否已退役（访问凭据会一直保留到所有者清除）。绝不返回密码。 |
| `audit_search` | 管理员 | 审计日志，可按 `source`（`panel`、`bot`、`mcp`、`api`）、操作者或操作筛选；使用 `before_id` 分页。 |

**结果中的格式。** `subscription_preview` 按以下名称表示格式：`base64_uris`（链接列表）、`mihomo_yaml`、`xray_json`、`singbox_json`、`user_page` 和 `decoy`。客户端有自己的 `formats`，User-Agent 有对应的 `format`，客户端 ID 中包含 `singbox`。`subscription_settings_get` 不返回**下发规则**中的规则：要查看某个 User-Agent 会得到什么，请把它传给 `subscription_preview`。

`user_get` 和 `subscription_preview` 中的协议配置带有 `formats`，即四种提供格式各自的 `format`、`ok` 和 `note`。`ok: false` 表示该格式不包含此协议配置。`note` 是代码，不是文本：`xhttp`、`self_signed`、`gecko`、`awg` 或 `unsupported`。`ok: true` 时，`hop_single_port` 表示端口跳跃范围被写成了单个端口。这些代码的含义见[协议配置](../guide/profiles.md#协议配置的订阅格式)。

### 变更工具

每项变更都有一对方法：`<tool>_plan` 和 `<tool>_apply`。

| 工具 | 权限级别 | 参数 | 需要所有者审批 |
|---|---|---|---|
| `user_create` | 操作员 | `name`（1 到 64 个字符，不区分大小写且唯一）、`group_id`，以及可选的 `quota_bytes`、`quota_reset`（`none`、`day`、`week`、`month`（默认值）、`rolling_month`）、`term_days`（最多 3650）、`device_limit`（最多 100）、`apps`（`happ`、`amnezia`）、`nodes`（`all` 或 `node_ids`）、`speed_limit_bps`（每人：其所有设备在同一节点上合计）、`device_speed_limit_bps`（每台设备）、`dns_preset_id` | 否 |
| `user_update` | 操作员 | `user_id`，可选的 `subscription_name`（最多 64 个字符；为空时使用 `name`），以及需要更改的字段（同上，但使用 `expires_unix` 替代 `term_days`；计划会同时写明两种限速：每人在单个节点上的限速和每台设备的限速） | 否 |
| `user_disable` | 操作员 | `user_ids`（1 到 50） | 超过 3 位用户时 |
| `user_enable` | 操作员 | `user_ids`（1 到 50） | 否 |
| `user_reset_traffic` | 操作员 | `user_ids`（1 到 50） | 超过 3 位用户时 |
| `device_revoke` | 操作员 | `user_id`、`device_id`，可选 `block`（在所有者于管理后台解除拉黑前，这个设备 ID 无法再次注册；报告其他 ID 的应用算作新设备；只有会发送设备 ID 的应用才能被拉黑） | 否 |
| `alert_mute` | 操作员 | `alert_id`、`duration_s`（最多 604800；0 表示取消静默） | 否 |
| `subscription_app_upsert` | 操作员 | `platform`（`ios`、`android`、`windows`、`macos`、`linux`）和 `name` 用于指定应用；更改现有应用时只传入变更字段；新应用至少要传入 `kind`：`kind`（`happ` 接收订阅链接，`amnezia` 接收 AmneziaWG 密钥）、`download_url`、`add_link_template`（占位符 `{url}`、`{url_enc}`、`{name_enc}`）、`description`（最多 80 个字符）、`recommended` | 始终 |
| `subscription_app_remove` | 操作员 | `platform`、`name` | 始终 |
| `node_fix` | 管理员 | `node`、诊断报告中的 `fix_id`；检查项列出参数时还需提供 `params` | 始终 |
| `node_decoy` | 管理员 | `node`、`mode`（`builtin`、`panel`、`own`）、`bundle_base64`（最多 2 MiB 的 base64 ZIP 或 TAR；`own` 模式下必需） | 始终 |
| `warp_restart` | 管理员 | `node` ID 或精确名称；计划会列出使用 WARP 出口且可能中断几秒的协议配置；执行时只接收 `confirm_token` | 否 |
| `warp_reregister` | 管理员 | `node` ID 或精确名称，以及可选的 `reason`（最多 300 个字符）；执行时只接收 `confirm_token`。计划会显示 Cloudflare 服务条款链接；所有者批准即表示代表此账户接受条款，并将其替换为新的匿名设备，从而更改 WARP 出口 IP | 始终 |
| `rollout_start` | 管理员 | `updates_status` 中恰好一个 `node_ids` 条目（立即更新该节点）；计划会固定受信任的版本 | 始终 |
| `node_update_schedule` | 管理员 | 计划参数：`node_id`、`local_datetime`（使用 `updates_status` 中的偏移量，格式为 `YYYY-MM-DDTHH:mm`，至少提前一分钟且最多提前一年）；计划会固定受信任的版本和偏移量 | 始终 |
| `node_update_schedule_cancel` | 管理员 | `node_id` | 始终 |
| `update_timezone` | 管理员 | `timezone_offset_minutes`（UTC 固定偏移量，范围为 -720 到 840，每次递增 15 分钟；`180` 表示 GMT+3） | 始终 |
| `rollout_pause`, `rollout_resume`, `rollout_cancel` | 管理员 | `updates_status` 中的 `rollout_id` | 始终 |
| `node_rollback` | 管理员 | `node` | 始终 |
| `node_install` | 管理员 | 计划参数：`host`、`port`（默认 22）、`username`（root 或具有免密 sudo 权限的用户）、节点 `name`（2 到 24 个字母、数字或连字符）、`address`，以及可选的 `country_code`、`location`、`provider`；执行时只接收 `confirm_token`。计划只读取服务器的公开主机密钥。所有者在审批页面输入 SSH 密码并确认主机密钥 | 始终 |
| `node_server_password_rotate` | 管理员 | 计划参数：运行中节点的 `node` ID 或精确名称（已退役的节点会被拒绝）；执行时只接收 `confirm_token`。面板自行生成新密码，绝不返回密码；所有者可在节点设置中查看 | 始终 |

每个 `_plan` 还接收 `reason`：由智能体自行撰写，最多 300 个字符，并以引文形式显示给所有者。没有工具会接收或返回服务器密码：所有者在审批页面输入安装密码，轮换后的密码由面板自行生成。`user_create` 绝不会返回新用户的订阅链接；所有者需在管理后台中复制链接。

`rollout_start_plan` 会立即更新一个选定节点，不能启动全体节点更新。只能选择 Agent 支持自更新且版本早于发布包的节点（版本过旧、更新失败或已回滚）；已有滚动更新正在运行或暂停时，会拒绝执行。计划会固定到制定时受信任的签名发布包：执行前发布包发生变化时，执行会失败，需要重新制定计划。所有者批准后，`node_update_schedule_plan` 会为一个节点保存未来的更新计划；制定计划时节点可以离线，新计划会替换该节点的旧计划。保存的任务会固定到相应签名发布版本和 UTC 固定偏移量。到期时如果节点离线，面板最多等待两小时让节点重新连接，之后会将任务标记为错过，绝不会自行启动任务；若签名发布包发生变化，面板会保留任务并继续显示，不会替换为其他发布版本。使用 `node_update_schedule_cancel_plan` 可取消待执行任务。更改 `update_timezone` 只影响新计划；现有任务会保留已保存的时间点和偏移量。

`subscription_app_upsert` 和 `subscription_app_remove` 会更改每位用户都能看到的页面上的一个应用，按平台和名称（不区分大小写）查找（同一平台上有两个应用同名时会拒绝执行：请在管理后台中更改）。新应用会添加到列表末尾。字段值的验证方式与保存**订阅 → 用户页面**时完全相同：下载链接必须是 http(s)；添加链接模板必须以应用自己的协议开头（`happ://add/{url}`、`myclient://add?url={url_enc}&name={name_enc}`），或使用 http(s)，不得使用 `javascript:`、`data:` 等协议；只能使用已知占位符；应用最多 30 个。此外，智能体提供的文本不能包含不可见格式字符。计划会读取并记住完整设置的状态；如果在执行前有人保存了订阅设置，执行会失败并返回「the subscription settings changed after the plan」，不会覆盖任何内容：请重新制定计划。在 `subscription_settings_get` 和智能体收到的计划副本中，链接查询字符串和较长的路径片段会显示为 `[redacted]`，与其他结果相同；所有者审批卡片会显示完整值。

## 制定计划并执行

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
| `step_up` | 操作本身要求在管理后台再次确认。 | 滚动更新工具、`node_rollback`、`node_update_schedule` 及其取消工具、`update_timezone`、`node_install`、`node_server_password_rotate` |
| `fleet` | 会更改节点上运行的内容。 | `node_fix`、`node_decoy`、滚动更新工具、`node_rollback`、`node_update_schedule` 及其取消工具、`node_install`、`node_server_password_rotate` |
| `bulk` | 一次操作影响超过 3 位用户。 | `user_disable`、`user_reset_traffic` |
| `user_page` | 所有用户都会在订阅页面上看到此变更。 | `subscription_app_upsert`、`subscription_app_remove` |

### 所有者在哪里审批

此类计划会显示在管理后台的 **集成 → 待你审批** 中，并带有角标；如果所有者已绑定 Telegram，面板还会发送一条计划等待审批的通知，其中包含指向该页面的链接（只能在管理后台中作出决定：参见[Telegram 告警](../operations/telegram-alerts.md)）。所有者会看到面板对变更的描述、风险提示、智能体的理由（标记为「由智能体撰写，面板未做核实。」）和剩余时间，然后选择：

- **批准**：要求所有者再次使用通行密钥或身份验证器验证码。之后智能体只能执行此计划。
- **拒绝**：智能体执行计划时会失败，并返回「rejected by the owner」。

对于 `node_install`，审批卡片会显示面板读取到的 SHA-256 主机密钥指纹和密钥类型（智能体收到的计划副本会遮蔽指纹，与其他密钥类值相同）。所有者需要将它与可信副本比较、勾选确认并在此处输入服务器的 SSH 密码；两项操作都完成前，**批准**按钮会保持禁用。面板会将密码密封到该计划中；智能体执行时只携带确认令牌，并且只会使用所有者确认过的主机密钥和该密码安装一次。密码绝不会经过智能体、其对话记录或模型服务商。密码轮换也有相同的所有者审批边界：面板生成新密码并且绝不返回。

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

   计划的 `needs_approval` 为 true，风险标记为 `user_page`。向所有者展示计划，请其在 **集成 → 待你审批** 中批准，然后使用 `confirm_token` 调用 `subscription_app_upsert_apply`。
3. The same for macOS: the same arguments with `"platform": "macos"`.
4. 要让 kl!ck 排在前面，请在两个平台上分别制定一个计划，取消 Happ 的推荐标记：`{"platform": "windows", "name": "Happ", "recommended": false, "reason": "kl!ck leads on the desktop"}`，然后将平台改为 `"macos"` 再执行一次。更新现有应用时只需提供变更字段。

`kind: "happ"` 表示「通过订阅链接」接入，无论打开链接的是哪个应用。下载链接使用发布页面，因为文件名包含版本号，Windows 和 macOS 版本分别发布。一键链接仅适用于版本高于 0.4.0 的 kl!ck；较早版本需要在页面上使用**复制链接**。新应用会添加到列表末尾；无论推荐应用位于何处，都会排在该平台列表的前面。之后运行 `subscription_settings_get` 可查看结果（智能体看到的链接查询字符串会显示为 `[redacted]`）。

## 结果中的不可信数据

名称、备注、理由、日志行，以及事件和告警的参数都来自用户、节点和其他系统。服务器的指令以及每个返回此类文本的工具都会说明：这些内容是数据，不是指令；智能体不得遵循其中包含的要求。

面板也会保护智能体和你：

- 数据文本会经过清理：移除控制字符、换行符和不可见格式字符，并截断过长的值。
- 计划摘要绝不包含数据中的文本；这类值会作为单独的事实并标记为 `untrusted`，所有者看到时会以引文形式显示。
- 每个结果都会经过最终检查，将所有疑似密钥的内容替换掉：API 令牌、分享链接（例如 `hysteria2://…`）、隧道配置、密钥材料、URL 查询字符串、较长的 URL 路径片段（私密前缀、订阅令牌）以及未编码的 32 字节密钥。

## 智能体永远无法获取的内容

- 订阅链接和用户页面密码。
- 设备密钥和配置、WARP 账户和密钥、协议配置密钥。
- 服务器密码：无论是安装时使用的 SSH 密码，还是轮换后的密码。
- 节点地址和证书固定指纹。例外是管理员令牌可通过 `node_server_access_list` 以及 `node_install` 和密码轮换计划查看已保存的 SSH 端点和登录名。
- 任何令牌、审批、会话和通行密钥相关内容。
- 除非令牌具有管理员权限级别，否则无法访问审计日志。

## 审计

工具调用的每个方法都会以「MCP 令牌 <name>」作为操作者写入审计日志，来源为 MCP。计划和执行过程也会各自增加日志项：「制定了变更计划：<tool>」和「执行了已制定的变更计划：<tool>」；所有者的决定则记录为「批准了变更：<tool>」或「拒绝了变更：<tool>」。可在**设置 → 审计**中按 MCP 来源筛选。








