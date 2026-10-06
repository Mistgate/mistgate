package mcp

import (
	"errors"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// The read tools. Each calls the panel's own procedures with the token's rights and returns a view
// from views.go. Arguments are ids and plain words, never URLs: no tool fetches anything the agent names.

var idShape = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)

func validID(what, s string) error {
	if !idShape.MatchString(s) {
		return errors.New(what + " is not a valid id")
	}
	return nil
}

// nodeRef finds a node by id or by exact name among the live nodes.
func (c *call) nodeRef(ref string) (id, name string, err error) {
	if ref == "" || len(ref) > 200 {
		return "", "", errors.New("node: give a node id or its exact name")
	}
	r, err := c.cl.Node.ListNodes(c.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		return "", "", apiError(err)
	}
	var byName []*adminv1.Node
	for _, n := range r.Msg.GetNodes() {
		if n.GetId() == ref {
			return n.GetId(), n.GetName(), nil
		}
		if n.GetName() == ref {
			byName = append(byName, n)
		}
	}
	switch len(byName) {
	case 0:
		return "", "", errors.New("node not found")
	case 1:
		return byName[0].GetId(), byName[0].GetName(), nil
	}
	return "", "", errors.New("several nodes have this name: use the node id")
}

func (c *call) getUser(id string) (*adminv1.GetUserResponse, error) {
	if err := validID("user_id", id); err != nil {
		return nil, err
	}
	r, err := c.cl.User.GetUser(c.ctx, connect.NewRequest(&adminv1.GetUserRequest{UserId: id}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg, nil
}

type noArgs struct{}

type nodeArg struct {
	Node string `json:"node" jsonschema:"node id or its exact name"`
}

type doctorArg struct {
	Node string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node"`
}

type doctorRefreshArg struct {
	Node    string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node (refresh needs a node)"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"true: ask the node's agent to run its checks now (reads the host only, waits up to 30 s); false: the last stored report"`
}

type usersSearchArg struct {
	Query     string `json:"query,omitempty" jsonschema:"part of a user name"`
	Filter    string `json:"filter,omitempty" jsonschema:"online, expiring or over_quota"`
	GroupID   string `json:"group_id,omitempty"`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"default 25, at most 50"`
	PageToken string `json:"page_token,omitempty" jsonschema:"next_page_token of the previous page"`
}

type userArg struct {
	UserID string `json:"user_id"`
}

type subPreviewArg struct {
	UserID string `json:"user_id"`
	Client string `json:"client,omitempty" jsonschema:"a client id from the client list, or a User-Agent string (at most 200 characters)"`
}

type alertsArg struct {
	Node           string `json:"node,omitempty" jsonschema:"node id or exact name"`
	IncludeHistory bool   `json:"include_history,omitempty"`
	WindowS        int    `json:"window_s,omitempty" jsonschema:"how far back the history goes, in seconds (default: the panel's)"`
}

type eventsArg struct {
	Node        string `json:"node,omitempty" jsonschema:"node id or exact name"`
	User        string `json:"user,omitempty" jsonschema:"user id"`
	MinSeverity string `json:"min_severity,omitempty" jsonschema:"info, warning or error"`
	Code        string `json:"code,omitempty" jsonschema:"only events with exactly this code (applied to the fetched page: use has_more and next_before_id to continue)"`
	BeforeID    int64  `json:"before_id,omitempty" jsonschema:"only events older than this id (next_before_id of the previous page)"`
	Limit       int    `json:"limit,omitempty" jsonschema:"default 50, at most 100"`
}

type checksArg struct {
	Node string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node"`
}

type auditArg struct {
	Source   string `json:"source,omitempty" jsonschema:"panel, bot, mcp or api"`
	Actor    string `json:"actor,omitempty" jsonschema:"keep entries whose actor id or name contains this (applied to the fetched page)"`
	Action   string `json:"action,omitempty" jsonschema:"keep entries whose action contains this (applied to the fetched page)"`
	BeforeID int64  `json:"before_id,omitempty" jsonschema:"only entries older than this id (next_before_id of the previous page)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"default 50, at most 100"`
}

func procs(p ...string) []string { return p }

func readTools() []toolDef {
	return []toolDef{
		readTool("fleet_status", ProfileReadonly, procs(adminv1connect.FleetServiceOverviewProcedure),
			"The fleet at a glance: every node (status, reason, online users, speed, CPU), totals, alert counts and the top consumers right now.",
			func(c *call, _ noArgs) (any, error) {
				r, err := c.cl.Fleet.Overview(c.ctx, connect.NewRequest(&adminv1.OverviewRequest{Range: adminv1.OverviewRange_OVERVIEW_RANGE_24H, EventLimit: 1}))
				if err != nil {
					return nil, apiError(err)
				}
				return fleetStatusView(r.Msg), nil
			}),

		readTool("node_get", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure),
			"One node: status and reason, host facts, inbounds with their state, up to 10 online users and the owner's notes. No addresses, keys or certificate pins.",
			func(c *call, in nodeArg) (any, error) {
				id, _, err := c.nodeRef(in.Node)
				if err != nil {
					return nil, err
				}
				r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				return nodeGetView(r.Msg), nil
			}),

		readTool("warp_status", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.WarpServiceGetWarpProcedure),
			"One node's WARP account and last agent report: whether it is enabled or paused, agent state, colo, handshake age, Cloudflare and other-site probes, registration time, refresh availability, and profiles using WARP egress. No addresses, keys or tokens.",
			func(c *call, in nodeArg) (any, error) {
				id, name, err := c.nodeRef(in.Node)
				if err != nil {
					return nil, err
				}
				r, err := c.cl.Warp.GetWarp(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.GetWarpRequest{NodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				return warpStatusView(id, name, r.Msg, c.e.now()), nil
			}),

		readTool("node_metrics", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure),
			"The node's current gauges (CPU, RAM, disk, network), traffic today, online users per protocol and today's top users. The panel keeps no per-node history, so there are no time series.",
			func(c *call, in nodeArg) (any, error) {
				id, _, err := c.nodeRef(in.Node)
				if err != nil {
					return nil, err
				}
				r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				return nodeMetricsView(r.Msg), nil
			}),

		// node_doctor: the stored report for everybody; operators and admins also get `refresh`.
		readTool("node_doctor", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.HealthServiceGetDoctorProcedure),
			"The doctor's report on a node, or on every node: the last stored result of its checks (never contacts a node), each with a fix id when one exists.",
			func(c *call, in doctorArg) (any, error) { return c.doctor(in.Node, false) }),
		readTool("node_doctor", ProfileOperator,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.HealthServiceGetDoctorProcedure, adminv1connect.HealthServiceRunDoctorProcedure),
			"The doctor's report on a node, or on every node: the last stored result of its checks, each with a fix id when one exists. With refresh=true the node's agent runs its checks now "+
				"(it only reads the host; changing anything is node_fix).",
			func(c *call, in doctorRefreshArg) (any, error) { return c.doctor(in.Node, in.Refresh) }),

		readTool("users_search", ProfileReadonly, procs(adminv1connect.UserServiceListUsersProcedure),
			"Find users by part of the name, by filter (online, expiring, over_quota) or group. Paged: pass next_page_token to continue.",
			func(c *call, in usersSearchArg) (any, error) {
				f, err := userFilter(in.Filter)
				if err != nil {
					return nil, err
				}
				if in.GroupID != "" {
					if err := validID("group_id", in.GroupID); err != nil {
						return nil, err
					}
				}
				size := in.PageSize
				if size <= 0 {
					size = defaultUsersPage
				}
				size = min(size, maxUsersPage)
				if len(in.Query) > 100 || len(in.PageToken) > 400 {
					return nil, errors.New("query or page_token too long")
				}
				r, err := c.cl.User.ListUsers(c.ctx, connect.NewRequest(&adminv1.ListUsersRequest{
					Filter: f, Query: in.Query, GroupId: in.GroupID, PageSize: uint32(size), PageToken: in.PageToken,
				}))
				if err != nil {
					return nil, apiError(err)
				}
				return usersView(r.Msg), nil
			}),

		readTool("groups_list", ProfileReadonly, procs(adminv1connect.GroupServiceListGroupsProcedure),
			"The user groups: id, name, the profile ids each group gives, the number of users and the group's DNS preset. Use a group id with users_search or user_create.",
			func(c *call, _ noArgs) (any, error) {
				r, err := c.cl.Group.ListGroups(c.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
				if err != nil {
					return nil, apiError(err)
				}
				return groupsView(r.Msg), nil
			}),

		readTool("user_get", ProfileReadonly, procs(adminv1connect.UserServiceGetUserProcedure),
			"One user: limits, status, devices, profiles and node access. No subscription link and no keys.",
			func(c *call, in userArg) (any, error) {
				r, err := c.getUser(in.UserID)
				if err != nil {
					return nil, err
				}
				return userGetView(r), nil
			}),

		readTool("user_traffic", ProfileReadonly, procs(adminv1connect.UserServiceGetUserProcedure),
			"A user's traffic: used and quota, the last 14 days and the split per node and protocol.",
			func(c *call, in userArg) (any, error) {
				r, err := c.getUser(in.UserID)
				if err != nil {
					return nil, err
				}
				return userTrafficView(r), nil
			}),

		readTool("user_devices", ProfileReadonly, procs(adminv1connect.UserServiceGetUserProcedure),
			"A user's devices: platform, model, last seen, online, and the AWG profile, version and tunnel address. Never a key or a config.",
			func(c *call, in userArg) (any, error) {
				r, err := c.getUser(in.UserID)
				if err != nil {
					return nil, err
				}
				return userDevicesView(r), nil
			}),

		readTool("subscription_preview", ProfileReadonly,
			procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.SubscriptionServiceListClientsProcedure, adminv1connect.SubscriptionServiceTestUserAgentProcedure),
			"What a user's subscription would look like to a client: which format a client (a client id, or a User-Agent) is served and which profiles and nodes the user's access gives. "+
				"It is not the rendered subscription and carries no link: the link is a credential and is never shown.",
			func(c *call, in subPreviewArg) (any, error) { return c.subscriptionPreview(in) }),

		readTool("alerts_list", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.HealthServiceListAlertsProcedure),
			"Active alerts, and with include_history the recently resolved ones. Titles and reasons are keys; parameters are as the panel reports them.",
			func(c *call, in alertsArg) (any, error) {
				req := &adminv1.ListAlertsRequest{}
				if in.Node != "" {
					id, _, err := c.nodeRef(in.Node)
					if err != nil {
						return nil, err
					}
					req.NodeId = id
				}
				if in.IncludeHistory && in.WindowS > 0 {
					req.HistoryWindowS = uint32(min(in.WindowS, 30*24*3600))
				}
				r, err := c.cl.Health.ListAlerts(c.ctx, connect.NewRequest(req))
				if err != nil {
					return nil, apiError(err)
				}
				return alertsView(r.Msg, in.IncludeHistory), nil
			}),

		readTool("events_search", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.FleetServiceListEventsProcedure),
			"The event feed, newest first, filtered by node, user, minimum severity or exact code. Paged with before_id.",
			func(c *call, in eventsArg) (any, error) {
				sev, err := eventSeverity(in.MinSeverity)
				if err != nil {
					return nil, err
				}
				req := &adminv1.ListEventsRequest{MinSeverity: sev, BeforeId: max(in.BeforeID, 0), Limit: uint32(listLimit(in.Limit))}
				if in.User != "" {
					if err := validID("user", in.User); err != nil {
						return nil, err
					}
					req.UserId = in.User
				}
				if in.Node != "" {
					id, _, err := c.nodeRef(in.Node)
					if err != nil {
						return nil, err
					}
					req.NodeId = id
				}
				if len(in.Code) > 80 {
					return nil, errors.New("code too long")
				}
				r, err := c.cl.Fleet.ListEvents(c.ctx, connect.NewRequest(req))
				if err != nil {
					return nil, apiError(err)
				}
				return eventsView(r.Msg.GetEvents(), in.Code, r.Msg.GetHasMore()), nil
			}),

		readTool("checks_results", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.HealthServiceGetChecksProcedure),
			"The health checks: a matrix of nodes by profile with the last result, the failure streak and the last 24 hours of passes and failures.",
			func(c *call, in checksArg) (any, error) {
				req := &adminv1.GetChecksRequest{}
				if in.Node != "" {
					id, _, err := c.nodeRef(in.Node)
					if err != nil {
						return nil, err
					}
					req.NodeId = id
				}
				r, err := c.cl.Health.GetChecks(c.ctx, connect.NewRequest(req))
				if err != nil {
					return nil, apiError(err)
				}
				return checksView(r.Msg), nil
			}),

		readTool("audit_search", ProfileAdmin, procs(adminv1connect.AuthServiceListAuditProcedure),
			"The audit log, newest first: who did what, from where (panel, bot, mcp, api). Needs the admin profile because it names admins, tokens and addresses. Paged with before_id.",
			func(c *call, in auditArg) (any, error) {
				src, err := auditSource(in.Source)
				if err != nil {
					return nil, err
				}
				if len(in.Actor) > 100 || len(in.Action) > 100 {
					return nil, errors.New("actor or action filter too long")
				}
				r, err := c.cl.Auth.ListAudit(c.ctx, connect.NewRequest(&adminv1.ListAuditRequest{
					Source: src, BeforeId: max(in.BeforeID, 0), PageSize: uint32(listLimit(in.Limit)),
				}))
				if err != nil {
					return nil, apiError(err)
				}
				v := &AuditV{Entries: []auditV{}}
				for _, e := range r.Msg.GetEntries() {
					if in.Actor != "" && !strings.Contains(e.GetActorId(), in.Actor) && !strings.Contains(e.GetActorName(), in.Actor) {
						continue
					}
					if in.Action != "" && !strings.Contains(e.GetAction(), in.Action) {
						continue
					}
					v.Entries = append(v.Entries, auditV{
						ID: e.GetId(), Time: e.GetTimeUnix(), Source: enumName("AUDIT_SOURCE_", e.GetSource().String()),
						ActorID: clean(e.GetActorId(), 80), ActorName: nm(e.GetActorName()), Action: clean(e.GetAction(), 80),
						Params: clean(e.GetParamsJson(), maxAuditParams), Result: clean(e.GetResult(), 60), IP: clean(e.GetIp(), 64),
					})
				}
				v.NextBeforeID = r.Msg.GetNextBeforeId()
				return v, nil
			}),

		readTool("updates_status", ProfileReadonly, procs(adminv1connect.UpdateServiceGetUpdatesProcedure),
			"Updates: the panel build, the signed bundle's status and version, each node's update state, and the active or last rollout.",
			func(c *call, _ noArgs) (any, error) {
				r, err := c.cl.Update.GetUpdates(c.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
				if err != nil {
					return nil, apiError(err)
				}
				return updatesView(r.Msg), nil
			}),
	}
}

func listLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	return min(n, maxListLimit)
}

func userFilter(s string) (adminv1.UserFilter, error) {
	switch s {
	case "":
		return adminv1.UserFilter_USER_FILTER_UNSPECIFIED, nil
	case "online":
		return adminv1.UserFilter_USER_FILTER_ONLINE, nil
	case "expiring":
		return adminv1.UserFilter_USER_FILTER_EXPIRING, nil
	case "over_quota":
		return adminv1.UserFilter_USER_FILTER_OVER_QUOTA, nil
	}
	return 0, errors.New("filter: online, expiring or over_quota")
}

func eventSeverity(s string) (adminv1.EventSeverity, error) {
	switch s {
	case "":
		return adminv1.EventSeverity_EVENT_SEVERITY_UNSPECIFIED, nil
	case "info":
		return adminv1.EventSeverity_EVENT_SEVERITY_INFO, nil
	case "warning":
		return adminv1.EventSeverity_EVENT_SEVERITY_WARNING, nil
	case "error":
		return adminv1.EventSeverity_EVENT_SEVERITY_ERROR, nil
	}
	return 0, errors.New("min_severity: info, warning or error")
}

func auditSource(s string) (adminv1.AuditSource, error) {
	switch s {
	case "":
		return adminv1.AuditSource_AUDIT_SOURCE_UNSPECIFIED, nil
	case "panel":
		return adminv1.AuditSource_AUDIT_SOURCE_PANEL, nil
	case "bot":
		return adminv1.AuditSource_AUDIT_SOURCE_BOT, nil
	case "mcp":
		return adminv1.AuditSource_AUDIT_SOURCE_MCP, nil
	case "api":
		return adminv1.AuditSource_AUDIT_SOURCE_API, nil
	}
	return 0, errors.New("source: panel, bot, mcp or api")
}

// doctor reads the stored report, or (refresh) asks the node's agent to run it now.
func (c *call) doctor(node string, refresh bool) (any, error) {
	id := ""
	if node != "" {
		var err error
		if id, _, err = c.nodeRef(node); err != nil {
			return nil, err
		}
	}
	if refresh {
		if id == "" {
			return nil, errors.New("refresh needs a node")
		}
		r, err := c.cl.Health.RunDoctor(c.ctx, connect.NewRequest(&adminv1.RunDoctorRequest{NodeId: id}))
		if err != nil {
			return nil, apiError(err)
		}
		return doctorView(r.Msg.GetNodes(), true), nil
	}
	r, err := c.cl.Health.GetDoctor(c.ctx, connect.NewRequest(&adminv1.GetDoctorRequest{NodeId: id}))
	if err != nil {
		return nil, apiError(err)
	}
	return doctorView(r.Msg.GetNodes(), false), nil
}

func (c *call) subscriptionPreview(in subPreviewArg) (any, error) {
	u, err := c.getUser(in.UserID)
	if err != nil {
		return nil, err
	}
	if len(in.Client) > 200 {
		return nil, errors.New("client: at most 200 characters")
	}
	cl, err := c.cl.Subs.ListClients(c.ctx, connect.NewRequest(&adminv1.ListClientsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	v := &SubscriptionPreviewV{
		User: userView(u.GetUser()), Profiles: profileRefs(u.GetProfiles()),
		Note: "Format and access only: the subscription itself and its link are not shown to agents.",
	}
	for _, n := range nodeAccessViews(u.GetNodeAccess()) {
		if n.Selected {
			v.Nodes = append(v.Nodes, n)
		}
	}
	ua := in.Client
	known := false
	for _, ci := range cl.Msg.GetClients() {
		cv := clientView(ci)
		if in.Client == "" {
			v.Clients = append(v.Clients, cv)
		} else if ci.GetId() == in.Client {
			v.Client, known = &cv, true
		}
	}
	if ua != "" && !known {
		t, err := c.cl.Subs.TestUserAgent(c.ctx, connect.NewRequest(&adminv1.TestUserAgentRequest{UserAgent: ua}))
		if err != nil {
			return nil, apiError(err)
		}
		v.UserAgent = clean(ua, 200)
		v.Serve = &serveV{RuleIndex: t.Msg.GetRuleIndex(), Format: subFormat(t.Msg.GetFormat()), Browser: t.Msg.GetBrowser()}
	}
	return v, nil
}
