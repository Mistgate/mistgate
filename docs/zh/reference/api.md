---
title: 管理 API
description: 管理后台使用的 Connect-RPC API、如何通过 curl 或生成的客户端调用、API 令牌及其权限级别，以及令牌可以和不可以执行的操作。
---

管理后台通过 Connect-RPC API 与面板通信。脚本也使用同一 API，并通过发送 API 令牌（而非会话 Cookie）遵循相同的角色检查、验证和审计日志规则。AI 智能体通过 [MCP 服务器](mcp.md)使用相同的令牌。

## API 的地址

每个方法都通过 POST 请求访问，地址为管理后台 URL 加上 `api/` 和方法路径：

```text
<admin URL>api/mistgate.admin.v1.<Service>/<Method>
https://panel.example.com/<prefix>/api/mistgate.admin.v1.UserService/ListUsers
```

管理后台 URL 是 `mistgate setup` 显示的地址（所有者可在**设置 → 域名**中查看）：使用私密前缀、私密主机名或单独监听器。API 只能通过该地址访问；在公共端访问相同路径时会进入伪装站点。

处理器使用 connect-go，因此 Connect 协议（JSON 或二进制 protobuf）、gRPC 和 gRPC-Web 都可使用。本页示例使用 JSON 格式的 Connect。

## 服务

API 定义位于 `proto/mistgate/admin/v1/`。生成的代码也在仓库中：Go 代码位于 `gen/mistgate/admin/v1`（包为 `adminv1` 和 `adminv1connect`），TypeScript 代码位于 `web/src/gen`。

| 服务 | 文件 | 涵盖内容 |
|---|---|---|
| `AuthService` | `auth.proto` | 登录、通行密钥、密码、会话、再次验证、审计日志、人机验证设置 |
| `InstanceService` | `instance.proto` | 品牌设置、管理后台和订阅地址 |
| `FleetService` | `fleet.proto` | 概览和事件流 |
| `NodeService` | `node.proto` | 节点：列表、详情、设置、安装命令、重启、日志、退役 |
| `ProfileService` | `profile.proto` | 协议、协议配置和节点上的协议配置 |
| `GroupService` | `group.proto` | 分组 |
| `UserService` | `user.proto` | 用户、流量、设备和订阅链接 |
| `DeviceService` | `device.proto` | AmneziaWG 设备及其密钥 |
| `SubscriptionService` | `subscription.proto` | 订阅设置和应用规则 |
| `DnsService` | `dns.proto` | DNS 预设、各节点在用户页面提供的预设，以及用户的选择 |
| `HealthService` | `health.proto` | 告警、客户端视角检测、诊断及其修复 |
| `UpdateService` | `update.proto` | 发布包、节点更新、定时计划和滚动更新、面板更新 |
| `ProvisioningService` | `provisioning.proto` | 通过 SSH 安装节点，以及节点已保存的 SSH 凭据 |
| `WarpService` | `warp.proto` | 节点的 WARP 账户 |
| `AwgService` | `awg.proto` | AmneziaWG 协议配置编辑器的辅助方法 |
| `BackupService` | `backup.proto` | 面板加密备份 |
| `TelegramService` | `telegram.proto` | Telegram 机器人和各管理员的聊天（仅所有者和已登录的管理员可用；API 令牌和 MCP 无法访问） |
| `ApiTokenService`, `ApprovalService` | `integrations.proto` | API 令牌和所有者审批 |
| `CheburcheckService`, `BsbordService` | `cheburcheck.proto`, `bsbord.proto` | [国家视角检测](../operations/reachability.md)的 cheburcheck 和 bsbord 来源：设置、状态和测试，bsbord 另有单元列表和价格查询（仅所有者；更改或断开来源以及价格查询需要再次验证；API 令牌和 MCP 无法访问） |

proto 文件中的注释是各字段的参考说明。约定（`common.proto`）：

- ID 是不透明的带前缀字符串：`nod_…`、`usr_…`、`prf_…`、`grp_…`、`dev_…`、`inb_…`（节点上的协议配置）。
- 字段名以 `_unix` 结尾的时间使用 Unix 秒数；0 表示未设置。字节计数器为无符号 64 位整数。
- JSON 中，字段名使用 lowerCamelCase（`pageSize`），枚举值使用其名称（`USER_FILTER_ONLINE`），64 位整数以字符串表示（输入时也接受数字）。
- 更新请求使用可选字段：未提供的字段保持不变。
- 错误由 Connect 代码和简短消息组成：`not_found`、`already_exists`、`invalid_argument`、`failed_precondition`、`aborted`（其他人已先行更改）、`permission_denied`、`unauthenticated`、`resource_exhausted`。

## API 令牌

所有者在 **集成 → API 令牌 → 新建令牌** 中创建令牌（需要再次验证身份）：

| 字段 | 规则 |
|---|---|
| 名称 | 1 到 64 个字符；在未吊销的令牌中不区分大小写且必须唯一。审计日志会显示此名称。 |
| 权限 | 令牌权限级别：**只读**、**操作员**或**管理员**。 |
| 有效期 | 默认 90 天，最长 365 天。令牌始终会过期；要续期，请创建新令牌并吊销旧令牌。 |
| 每分钟请求数 | 默认 120，范围为 1 到 600。 |

令牌由 `tk1_` 和其后的 43 个 URL 安全字符组成。令牌只显示一次；面板仅保存其 SHA-256 和最后四个字符（用于在列表中区分令牌）。同时最多可有 50 个未吊销的令牌。列表会显示每个令牌的上次使用时间、来源以及使用渠道（API 或 MCP）。

**吊销**会立即停用令牌：下一次请求将被拒绝，并取消该令牌通过 MCP 制定但尚未执行的变更计划。吊销令牌需要再次验证身份。

### 身份验证

使用 Bearer 方案，在一个 `Authorization` 请求头中发送令牌：

```text
Authorization: Bearer tk1_...
```

带有 Bearer 请求头的请求只根据令牌验证；不会检查 Cookie。拒绝响应：

| 状态码 | 正文中的 `code` | 消息 |
|---|---|---|
| 401 | `unauthenticated` | `not signed in`（令牌缺失、格式错误或未知）、`token revoked`、`token expired` |
| 403 | `permission_denied` | `this call is not available to API tokens`、`this token's profile cannot do this`、`this needs the owner's approval; it is not available over the API`、`this is used only while the MCP server makes a plan; it is not available over the API` |
| 429 | `resource_exhausted` | `too many requests for this token, slow down`，并带有 `Retry-After` |

## 调用 API

### 使用 curl

```sh
TOKEN=$(head -n1 ~/.config/mistgate/token)
ADMIN=https://panel.example.com/<prefix>/

curl -sS "${ADMIN}api/mistgate.admin.v1.UserService/ListUsers" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"filter": "USER_FILTER_ONLINE", "pageSize": 20}'
```

变更请求的形式相同。以下示例将两个用户的有效期延长 30 天（需要操作员权限级别或更高权限）：

```sh
curl -sS "${ADMIN}api/mistgate.admin.v1.UserService/ExtendUsers" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"userIds": ["usr_...", "usr_..."], "days": 30}'
```

发生错误时，响应会以 JSON 返回，并带有 HTTP 错误状态码：

```json
{"code": "permission_denied", "message": "this token's profile cannot do this"}
```

### 使用生成的客户端

可以从模块 `github.com/mistgate/mistgate` 导入 Go 包；由于 API 在 1.0 之前仍可能变更，请固定到某个发布标签或提交。客户端基础 URL 为管理后台 URL 加上 `api`：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// bearer 为每个请求添加 API 令牌。
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func main() {
	raw, err := os.ReadFile(os.Getenv("MISTGATE_TOKEN_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	token := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	hc := &http.Client{Transport: bearer{token}}

	// `mistgate setup` 显示的管理后台 URL，后接「api」。
	base := "https://panel.example.com/<prefix>/api"
	users := adminv1connect.NewUserServiceClient(hc, base)

	resp, err := users.ListUsers(context.Background(), connect.NewRequest(&adminv1.ListUsersRequest{PageSize: 10}))
	if err != nil {
		log.Fatal(connect.CodeOf(err), err)
	}
	for _, u := range resp.Msg.GetUsers() {
		fmt.Println(u.GetId(), u.GetName(), u.GetStatus())
	}
}
```

对于其他语言，可使用 `buf generate` 和相应语言的 Connect 或 gRPC 插件，从 `proto/` 生成客户端。

## 令牌权限级别

令牌权限级别对应管理后台角色：**只读**对应只读角色，**操作员**对应协作者，**管理员**对应所有者。除角色权限外，令牌还只能调用固定允许列表中的方法（`internal/panel/auth/policy_tokens.go`）；无论权限级别如何，其他方法都会被拒绝。允许列表中的方法均不会返回凭据。

| 方法 | 只读 | 操作员 | 管理员 |
|---|---|---|---|
| `FleetService.Overview`, `FleetService.ListEvents` | 是 | 是 | 是 |
| `NodeService.ListNodes`, `NodeService.GetNode` | 是 | 是 | 是 |
| `NodeService.CheckPorts` | 否 | 是 | 是 |
| `HealthService.ListAlerts`, `GetChecks`, `GetDoctor` | 是 | 是 | 是 |
| `UserService.ListUsers`, `UserService.GetUser` | 是 | 是 | 是 |
| `GroupService.ListGroups` | 是 | 是 | 是 |
| `ProfileService.ListProfiles`, `ProfileService.GetProfile`（密钥已遮蔽） | 是 | 是 | 是 |
| `ProfileService.ListProtocols`, `ProfileService.PreviewProfile` | 是 | 是 | 是 |
| `SubscriptionService.GetSubscriptionSettings`, `ListClients`, `TestUserAgent` | 是 | 是 | 是 |
| `UpdateService.GetUpdates` | 是 | 是 | 是 |
| `UserService.CreateUser`, `UpdateUser`, `SetUsersEnabled`, `ExtendUsers`, `ResetUserTraffic`, `RevokeDevice` | 否 | 是 | 是 |
| `HealthService.MuteAlert`, `RunChecksNow`, `RunDoctor` | 否 | 是 | 是 |
| `AuthService.ListAudit` | 否 | 否 | 是 |
| `HealthService.ApplyFix` | 否 | 否 | 仅可通过已批准的 MCP 计划执行 |
| `UpdateService.StartRollout`, `PauseRollout`, `ResumeRollout`, `CancelRollout`, `RollbackNode` | 否 | 否 | 仅可通过已批准的 MCP 计划执行 |
| `UpdateService.ScheduleNodeUpdate`, `CancelNodeUpdateSchedule`, `SetUpdateTimezone` | 否 | 否 | 仅可通过已批准的 MCP 计划执行 |
| `ProvisioningService.ListNodeServerAccess`（连接元数据，不含密码） | 否 | 否 | 是 |
| `ProvisioningService.GetSSHFingerprint` | 否 | 否 | 仅在 MCP 服务器制定 `node_install` 计划时可用 |
| `ProvisioningService.StartNodeProvision`, `RotateNodeServerPassword` | 否 | 否 | 仅可通过已批准的 MCP 计划执行 |
| `SubscriptionService.UpdateSubscriptionSettings`（用户页面的一个应用） | 否 | 仅可通过已批准的 MCP 计划执行 | 仅可通过已批准的 MCP 计划执行 |
| `ProfileService.CreateProfile`, `UpdateProfile`, `DeleteProfile`, `CreateInbound`, `DeleteInbound`; `GroupService.UpdateGroup` | 否 | 否 | 仅可通过已批准的 MCP 计划执行 |

以下操作对所有令牌都不可用：删除用户、获取订阅链接、设备及其密钥、解除设备拉黑（`UserService.UnblockDevice`；令牌可以通过带 `block` 的 `RevokeDevice` 拉黑设备，但只有管理员能解除拉黑）；节点注册、设置、重启、日志和退役；准备 AmneziaWG 内核模块；管理员自己的 SSH 安装步骤（检查服务器、重试任务、显示或清除已保存的凭据）；创建分组、DNS 预设和订阅设置（通过已批准的计划更改用户页面的应用列表除外）；WARP、备份、面板更新和重新扫描发布包；品牌设置、令牌、审批；管理员自己的账户（通行密钥、密码、会话、再次验证）以及人机验证设置。

## 需要所有者批准的变更

令牌不能通过再次验证。需要再次验证或会更改节点运行内容的方法都不能由令牌直接调用；通过普通 API 调用时会返回 403 和消息 `this needs the owner's approval; it is not available over the API`。

这些方法只能通过持有管理员令牌的 [MCP 服务器](mcp.md)调用（用户页面的应用列表从操作员权限级别起即可调用）：智能体先制定变更计划，所有者在**集成 → 待你审批**中查看并批准计划（所有者需要自行再次验证），之后智能体会通过一次 apply 完成计划所需的调用。系统会在每次调用时检查审批；授权仅允许调用该计划列出的方法。计划会在创建 10 分钟后过期。

## 限制

- **每个令牌的每分钟请求数**：按令牌设置（1 到 600，默认 120），突发上限为 30。超出限制时返回 429 和 `Retry-After`。
- **管理后台每个客户端网络**：每秒 30 次请求，突发上限为 200；Cookie 和令牌使用相同限制。
- **请求大小**：根据服务不同，为 64 KiB 到 1 MiB。
- **有效期**：每个令牌都会过期（最长 365 天）。

## 审计

每次令牌调用都会向审计日志写入一行：调用 <procedure>、HTTP 状态码、客户端地址，以及操作者「API 令牌 <name>」（通过 MCP 调用时为「MCP 令牌 <name>」）。成功的读取操作每个方法每分钟最多记录一次；变更和拒绝响应每次都会记录。方法自身的日志（例如创建用户……）与管理员操作的记录方式相同。

## 响应中不会包含的内容

- 使用令牌调用 `CreateUser` 时，返回的用户信息不包含订阅链接和页面密码。令牌不能调用 `GetSubscriptionLink`。
- 对所有调用者，协议配置密钥都会以遮蔽形式返回（`••••`）。
- 令牌不能访问设备密钥和配置（`DeviceService`）及 WARP 账户。
- 设备 ID（HWID）既不会被保存，也不会被返回：`Device.fingerprint` 是其哈希的前六个字符，足以区分两部相同的手机。
- 令牌创建后不会再次返回密钥，即使所有者也无法查看。
- 审计参数绝不包含密钥。




