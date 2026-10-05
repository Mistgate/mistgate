---
title: Admin API
description: The Connect-RPC API behind the admin, how to call it with curl or a generated client, API tokens and their profiles, and what a token may and may not do.
---

The admin UI talks to the panel through a Connect-RPC API. Scripts use the same API, with the same role checks, validation and audit log, by sending an API token instead of a session cookie. AI agents use the same tokens through the [MCP server](mcp.md).

## Where the API lives

Every procedure is a POST to the admin URL plus `api/` and the procedure path:

```text
<admin URL>api/mistgate.admin.v1.<Service>/<Method>
https://panel.example.com/<prefix>/api/mistgate.admin.v1.UserService/ListUsers
```

The admin URL is the one `mistgate setup` printed (Settings → Domains shows it to the owner): with a secret prefix, a secret host or a separate listener. The API is only reachable there; on the public side the same path gets the decoy site.

The handlers are connect-go handlers, so the Connect protocol (JSON or binary protobuf), gRPC and gRPC-Web all work. The examples here use Connect with JSON.

## Services

The API is defined in `proto/mistgate/admin/v1/`. Generated code is in the repository: Go in `gen/mistgate/admin/v1` (packages `adminv1` and `adminv1connect`), TypeScript in `web/src/gen`.

| Service | File | What it covers |
|---|---|---|
| `AuthService` | `auth.proto` | Sign-in, passkeys, password, sessions, step-up, the audit log, captcha settings |
| `InstanceService` | `instance.proto` | Brand settings, the admin and subscription addresses |
| `FleetService` | `fleet.proto` | The overview and the event feed |
| `NodeService` | `node.proto` | Nodes: list, details, settings, install commands, restarts, logs, retirement |
| `ProfileService` | `profile.proto` | Protocols, profiles and profiles on nodes |
| `GroupService` | `group.proto` | Groups |
| `UserService` | `user.proto` | Users, their traffic, devices and subscription link |
| `DeviceService` | `device.proto` | AmneziaWG devices and their keys |
| `SubscriptionService` | `subscription.proto` | Subscription settings and app rules |
| `DnsService` | `dns.proto` | DNS presets |
| `HealthService` | `health.proto` | Alerts, client-eye checks, the doctor and its fixes |
| `UpdateService` | `update.proto` | Release bundle, node updates, schedules and rollouts, panel updates |
| `ProvisioningService` | `provisioning.proto` | Installing nodes over SSH and the saved SSH access of nodes |
| `WarpService` | `warp.proto` | WARP accounts of nodes |
| `AwgService` | `awg.proto` | Helpers of the AmneziaWG profile editor |
| `BackupService` | `backup.proto` | Encrypted panel backups |
| `ApiTokenService`, `ApprovalService` | `integrations.proto` | API tokens and the owner's approvals |

The comments in the proto files are the reference for every field. Conventions (`common.proto`):

- Ids are opaque prefixed strings: `nod_…`, `usr_…`, `prf_…`, `grp_…`, `dev_…`, `inb_…` (a profile on a node).
- Times are Unix seconds in fields ending in `_unix`; 0 means unset. Byte counters are unsigned 64-bit.
- In JSON, field names are lowerCamelCase (`pageSize`), enums are their names (`USER_FILTER_ONLINE`) and 64-bit integers are strings (numbers are accepted on input).
- Updates use optional fields: a field that is absent stays unchanged.
- Errors are Connect codes with short messages: `not_found`, `already_exists`, `invalid_argument`, `failed_precondition`, `aborted` (somebody changed it first), `permission_denied`, `unauthenticated`, `resource_exhausted`.

## API tokens

The owner creates tokens in **Integrations → API tokens → New token** (a fresh step-up is needed):

| Field | Rule |
|---|---|
| Name | 1 to 64 characters, unique among the live tokens ignoring case. It is what the audit log shows. |
| Access | The profile: **Read only**, **Operator** or **Admin**. |
| Lifetime | 90 days by default, at most 365. A token always expires; to renew one, make a new token and revoke the old one. |
| Requests per minute | 120 by default, 1 to 600. |

The secret looks like `tk1_` followed by 43 URL-safe characters. It is shown once; the panel keeps only its SHA-256 and the last four characters (to tell tokens apart in the list). At most 50 live tokens exist at a time. The list shows when and from where each token was last used, and through which channel (the API or MCP).

**Revoke** stops a token at once: its next request is refused and the changes it planned through MCP and did not apply are cancelled. Revoking needs a step-up.

### Authentication

Send the token in one `Authorization` header with the Bearer scheme:

```text
Authorization: Bearer tk1_...
```

A request with a Bearer header is judged by the token alone; a cookie is never looked at. Refusals:

| Status | Body `code` | Message |
|---|---|---|
| 401 | `unauthenticated` | `not signed in` (missing, malformed or unknown token), `token revoked`, `token expired` |
| 403 | `permission_denied` | `this call is not available to API tokens`, `this token's profile cannot do this`, `this needs the owner's approval; it is not available over the API`, `this is used only while the MCP server makes a plan; it is not available over the API` |
| 429 | `resource_exhausted` | `too many requests for this token, slow down`, with `Retry-After` |

## Calling the API

### With curl

```sh
TOKEN=$(head -n1 ~/.config/mistgate/token)
ADMIN=https://panel.example.com/<prefix>/

curl -sS "${ADMIN}api/mistgate.admin.v1.UserService/ListUsers" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"filter": "USER_FILTER_ONLINE", "pageSize": 20}'
```

A change looks the same. Extending two users by 30 days (operator profile or higher):

```sh
curl -sS "${ADMIN}api/mistgate.admin.v1.UserService/ExtendUsers" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"userIds": ["usr_...", "usr_..."], "days": 30}'
```

An error comes back as JSON with an HTTP error status:

```json
{"code": "permission_denied", "message": "this token's profile cannot do this"}
```

### With a generated client

The Go packages are importable from the module `github.com/mistgate/mistgate`; pin a release tag or a commit, since the API may still change before 1.0. The client's base URL is the admin URL plus `api`:

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

// bearer adds the API token to every request.
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

	// The admin URL that `mistgate setup` printed, plus "api".
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

For another language, generate a client from `proto/` with `buf generate` and the Connect or gRPC plugin of that language.

## Token profiles

A profile acts as an admin role: **Read only** as read-only, **Operator** as helper, **Admin** as owner. On top of the role, a token may call only the procedures on a fixed allow-list (`internal/panel/auth/policy_tokens.go`); everything else is refused whatever the profile. Nothing on the list returns a credential.

| Procedure | Read only | Operator | Admin |
|---|---|---|---|
| `FleetService.Overview`, `FleetService.ListEvents` | yes | yes | yes |
| `NodeService.ListNodes`, `NodeService.GetNode` | yes | yes | yes |
| `HealthService.ListAlerts`, `GetChecks`, `GetDoctor` | yes | yes | yes |
| `UserService.ListUsers`, `UserService.GetUser` | yes | yes | yes |
| `GroupService.ListGroups` | yes | yes | yes |
| `ProfileService.ListProfiles`, `ProfileService.GetProfile` (secrets masked) | yes | yes | yes |
| `SubscriptionService.ListClients`, `SubscriptionService.TestUserAgent` | yes | yes | yes |
| `UpdateService.GetUpdates` | yes | yes | yes |
| `UserService.CreateUser`, `UpdateUser`, `SetUsersEnabled`, `ExtendUsers`, `ResetUserTraffic`, `RevokeDevice` | no | yes | yes |
| `HealthService.MuteAlert`, `RunChecksNow`, `RunDoctor` | no | yes | yes |
| `AuthService.ListAudit` | no | no | yes |
| `HealthService.ApplyFix` | no | no | only through an approved MCP plan |
| `UpdateService.StartRollout`, `PauseRollout`, `ResumeRollout`, `CancelRollout`, `RollbackNode` | no | no | only through an approved MCP plan |
| `UpdateService.ScheduleNodeUpdate`, `CancelNodeUpdateSchedule`, `SetUpdateTimezone` | no | no | only through an approved MCP plan |
| `ProvisioningService.ListNodeServerAccess` (connection metadata, never a password) | no | no | yes |
| `ProvisioningService.GetSSHFingerprint` | no | no | only while the MCP server makes a `node_install` plan |
| `ProvisioningService.StartNodeProvision`, `RotateNodeServerPassword` | no | no | only through an approved MCP plan |

Closed to every token: deleting users, the subscription link, devices and their keys, node enrollment, settings, restarts, logs and retirement, preparing the AmneziaWG kernel module, the admin's own SSH install steps (checking a server, retrying a job, revealing or forgetting saved access), every change to profiles, groups, DNS presets and subscription settings, WARP, backups, panel updates and bundle rescans, brand settings, tokens, approvals, the admins' own account (passkeys, password, sessions, step-up) and the captcha settings.

## Changes that need the owner

A token never passes step-up. Procedures that need a step-up, or that change what runs on the nodes, are never callable with a token directly; over the plain API they answer 403 "this needs the owner's approval; it is not available over the API".

They are reachable only through the [MCP server](mcp.md) with an Admin token: the agent plans the change, the owner reads the plan in **Integrations → Waiting for you** and approves it (with a step-up of their own), and only then does the agent's apply make that one call. The approval is checked in the database on every use and opens only the procedure of that plan. A plan expires 10 minutes after it was made.

## Limits

- **Requests per minute per token**: as set on the token (1 to 600, default 120), with a burst of up to 30. Over the limit: 429 with `Retry-After`.
- **Per client network** on the admin surface: 30 requests per second with a burst of 200, for cookies and tokens alike.
- **Request size**: 64 KiB to 1 MiB depending on the service.
- **Lifetime**: every token expires (at most 365 days).

## Audit

Every token call writes a row to the audit log: "called <procedure>", the HTTP status, the client address, and the actor "API token <name>" (or "MCP token <name>" through MCP). Successful reads are written at most once a minute per procedure; changes and refusals every time. The rows of the procedure itself (for example "created the user …") are written as for an admin.

## What responses never contain

- `CreateUser` called with a token returns the user without the subscription link and the page password. `GetSubscriptionLink` is closed to tokens.
- Profile secrets come back masked (`••••`), for every caller.
- Device keys and configurations (`DeviceService`) and WARP accounts are closed to tokens.
- Token secrets are never returned after creation, not even to the owner.
- Audit parameters never contain secrets.
