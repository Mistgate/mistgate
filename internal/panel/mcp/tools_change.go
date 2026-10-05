package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// The change tools. The plan step reads what it needs to describe the change in the panel's own
// words (never an agent-supplied description) and applies the danger rules; the apply step makes exactly one call that
// changes something. A summary never embeds untrusted text: names go in the facts, marked untrusted.

const (
	dangerStepUp = "step_up" // the operation itself asks for a step-up
	dangerFleet  = "fleet"   // it changes what runs on the nodes
	dangerBulk   = "bulk"    // many users at once
)

func changeTools() []toolDef {
	var out []toolDef
	for _, g := range [][]toolDef{
		change(userCreate), change(userUpdate), change(userDisable), change(userEnable), change(userResetTraffic), change(deviceRevoke), change(alertMute),
		change(nodeFix), change(rolloutStart), change(nodeUpdateSchedule), change(nodeUpdateScheduleCancel), change(updateTimezone),
		change(rolloutPause), change(rolloutResume), change(rolloutCancel), change(nodeRollback),
	} {
		out = append(out, g...)
	}
	return out
}

func fmtBytes(n uint64) string {
	switch {
	case n == 0:
		return "unlimited"
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	}
	return strconv.FormatUint(n, 10) + " bytes"
}

func fmtTime(unix int64) string {
	if unix == 0 {
		return "never"
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 UTC")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// codedFact is a line in the panel's own words with the code and values the owner's UI words it by (Fact.Code); value
// stays the English line the agent reads.
func codedFact(key, value, code string, kv ...string) Fact {
	f := Fact{Key: key, Value: value, Code: code}
	if len(kv) > 0 {
		f.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Params[kv[i]] = kv[i+1]
		}
	}
	return f
}

// changeFact is "from -> to" of one field of a user update; from and to are the raw values (bytes, unix, a reset name).
func changeFact(key, value, from, to string) Fact {
	return codedFact(key, value, "change", "from", from, "to", to)
}

func quotaFact(bytes uint64, reset string) Fact {
	return codedFact("quota", fmtBytes(bytes)+", reset "+reset, "quota", "bytes", strconv.FormatUint(bytes, 10), "reset", reset)
}

func termFact(days uint32) Fact {
	if days == 0 {
		return codedFact("term", "never expires", "never")
	}
	return codedFact("term", plural(int(days), "day", "days"), "days", "n", strconv.Itoa(int(days)))
}

func nodesFact(key string, n *nodesArg) Fact {
	if w := n.word(); w != "all" {
		return codedFact(key, n.text(), "some", "n", w)
	}
	return codedFact(key, n.text(), "all")
}

func secondsFact(key string, s uint32) Fact {
	return codedFact(key, strconv.FormatUint(uint64(s), 10)+" s", "seconds", "n", strconv.FormatUint(uint64(s), 10))
}

func timeFact(key string, unix int64) Fact {
	return codedFact(key, fmtTime(unix), "time", "unix", strconv.FormatInt(unix, 10))
}

// uniqueIDs checks a list of ids: 1..maxIDs, well formed, without repeats (repeats are dropped).
func uniqueIDs(what string, ids []string, allowEmpty bool) ([]string, error) {
	if len(ids) == 0 && !allowEmpty {
		return nil, errors.New(what + ": at least one id")
	}
	if len(ids) > maxIDs {
		return nil, fmt.Errorf("%s: at most %d ids at once", what, maxIDs)
	}
	var out []string
	for _, id := range ids {
		if err := validID(what, id); err != nil {
			return nil, err
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

func quotaReset(s string) (adminv1.QuotaReset, error) {
	switch s {
	case "":
		return adminv1.QuotaReset_QUOTA_RESET_UNSPECIFIED, nil
	case "none":
		return adminv1.QuotaReset_QUOTA_RESET_NONE, nil
	case "day":
		return adminv1.QuotaReset_QUOTA_RESET_DAY, nil
	case "week":
		return adminv1.QuotaReset_QUOTA_RESET_WEEK, nil
	case "month":
		return adminv1.QuotaReset_QUOTA_RESET_MONTH, nil
	case "rolling_month":
		return adminv1.QuotaReset_QUOTA_RESET_ROLLING_MONTH, nil
	}
	return 0, errors.New("quota_reset: none, day, week, month or rolling_month")
}

type appsArg struct {
	Happ    bool `json:"happ"`
	Amnezia bool `json:"amnezia"`
}

type nodesArg struct {
	All     bool     `json:"all,omitempty" jsonschema:"true: every node, including nodes added later"`
	NodeIDs []string `json:"node_ids,omitempty"`
}

func (a *appsArg) proto() *adminv1.AppToggles {
	if a == nil {
		return nil
	}
	return &adminv1.AppToggles{Happ: a.Happ, Amnezia: a.Amnezia}
}

func (n *nodesArg) proto() *adminv1.NodeSelection {
	if n == nil {
		return nil
	}
	return &adminv1.NodeSelection{All: n.All, NodeIds: n.NodeIDs}
}

func (a *appsArg) check() error {
	if a != nil && !a.Happ && !a.Amnezia {
		return errors.New("apps: at least one app must be on")
	}
	return nil
}

func (n *nodesArg) check() error {
	if n == nil {
		return nil
	}
	if n.All && len(n.NodeIDs) > 0 {
		return errors.New("nodes: either all or a list, not both")
	}
	if !n.All && len(n.NodeIDs) == 0 {
		return errors.New("nodes: all=true or a list of node ids")
	}
	_, err := uniqueIDs("nodes.node_ids", n.NodeIDs, true)
	return err
}

func (a *appsArg) text() string {
	switch {
	case a == nil:
		return "default"
	case a.Happ && a.Amnezia:
		return "Happ and Amnezia"
	case a.Happ:
		return "Happ only"
	}
	return "Amnezia only"
}

func (n *nodesArg) text() string {
	switch {
	case n == nil || n.All:
		return "all nodes"
	}
	return plural(len(n.NodeIDs), "node", "nodes")
}

// code is the apps as the owner's UI words them: default, both, happ, amnezia.
func (a *appsArg) code() string {
	switch {
	case a == nil:
		return "default"
	case a.Happ && a.Amnezia:
		return "both"
	case a.Happ:
		return "happ"
	}
	return "amnezia"
}

// word is "all" or how many nodes.
func (n *nodesArg) word() string {
	if n == nil || n.All {
		return "all"
	}
	return strconv.Itoa(len(n.NodeIDs))
}

func checkName(s string) error {
	if clean(s, 1000) != strings.TrimSpace(s) || s == "" || len([]rune(s)) > 64 {
		return errors.New("name: 1 to 64 characters, no control characters")
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------------------------
// user_create

type userCreateArgs struct {
	ReasonField
	Name          string    `json:"name" jsonschema:"1 to 64 characters, unique ignoring case"`
	GroupID       string    `json:"group_id" jsonschema:"required by the panel: the group of the new user (users_search shows the group id of existing users)"`
	QuotaBytes    uint64    `json:"quota_bytes,omitempty" jsonschema:"traffic quota in bytes; 0 = unlimited"`
	QuotaReset    string    `json:"quota_reset,omitempty" jsonschema:"none, day, week, month (default) or rolling_month"`
	TermDays      uint32    `json:"term_days,omitempty" jsonschema:"valid for this many days from now; 0 = never expires"`
	DeviceLimit   uint32    `json:"device_limit,omitempty" jsonschema:"0 = the panel's default"`
	Apps          *appsArg  `json:"apps,omitempty" jsonschema:"absent = both apps on"`
	Nodes         *nodesArg `json:"nodes,omitempty" jsonschema:"absent = all nodes"`
	SpeedLimitBps uint64    `json:"speed_limit_bps,omitempty" jsonschema:"0 = no limit"`
	DNSPresetID   string    `json:"dns_preset_id,omitempty"`
}

var userCreate = changeSpec[userCreateArgs]{
	name: "user_create", min: ProfileOperator,
	procs: procs(adminv1connect.UserServiceListUsersProcedure, adminv1connect.UserServiceCreateUserProcedure),
	desc:  "Create a user. The subscription link is never shown to agents: the owner copies it in the admin panel.",
	plan: func(c *call, a userCreateArgs) (*planned, error) {
		if err := checkName(a.Name); err != nil {
			return nil, err
		}
		if _, err := quotaReset(a.QuotaReset); err != nil {
			return nil, err
		}
		if a.TermDays > 3650 || a.DeviceLimit > 100 {
			return nil, errors.New("term_days is at most 3650 and device_limit at most 100")
		}
		if err := validID("group_id", a.GroupID); err != nil {
			return nil, err
		}
		if a.DNSPresetID != "" {
			if err := validID("dns_preset_id", a.DNSPresetID); err != nil {
				return nil, err
			}
		}
		if err := a.Apps.check(); err != nil {
			return nil, err
		}
		if err := a.Nodes.check(); err != nil {
			return nil, err
		}
		r, err := c.cl.User.ListUsers(c.ctx, connect.NewRequest(&adminv1.ListUsersRequest{Query: a.Name, PageSize: maxUsersPage}))
		if err != nil {
			return nil, apiError(err)
		}
		for _, u := range r.Msg.GetUsers() {
			if strings.EqualFold(u.GetName(), a.Name) {
				return nil, errors.New("a user with this name already exists")
			}
		}
		reset := a.QuotaReset
		if reset == "" {
			reset = "month"
		}
		term := "never expires"
		if a.TermDays > 0 {
			term = plural(int(a.TermDays), "day", "days")
		}
		return &planned{
			Summary: fmt.Sprintf("Create one user with a %s quota (reset: %s), term: %s, on %s.", fmtBytes(a.QuotaBytes), reset, term, a.Nodes.text()),
			Facts: []Fact{
				{Key: "name", Value: nm(a.Name), Untrusted: true},
				quotaFact(a.QuotaBytes, reset),
				termFact(a.TermDays),
				codedFact("apps", a.Apps.text(), a.Apps.code()),
				nodesFact("nodes", a.Nodes),
			},
		}, nil
	},
	apply: func(c *call, a userCreateArgs, _ Plan) (done, error) {
		reset, _ := quotaReset(a.QuotaReset)
		r, err := c.cl.User.CreateUser(c.ctx, connect.NewRequest(&adminv1.CreateUserRequest{
			Name: a.Name, GroupId: a.GroupID, QuotaBytes: a.QuotaBytes, QuotaReset: reset, TermDays: a.TermDays,
			DeviceLimit: a.DeviceLimit, Apps: a.Apps.proto(), Nodes: a.Nodes.proto(), SpeedLimitBps: a.SpeedLimitBps, DnsPresetId: a.DNSPresetID,
		}))
		if err != nil {
			return done{}, apiError(err)
		}
		// The link is a credential: CreateUser does not return it to a token, and nothing of it is read here.
		return doneWith("user_created", fmt.Sprintf("User created (id %s). The subscription link is not shown to agents; the owner copies it in the admin panel.",
			r.Msg.GetUser().GetId())), nil
	},
}

// ---------------------------------------------------------------------------------------------------------------------
// user_update

type userUpdateArgs struct {
	ReasonField
	UserID           string    `json:"user_id"`
	Name             *string   `json:"name,omitempty"`
	SubscriptionName *string   `json:"subscription_name,omitempty" jsonschema:"name shown on the public subscription page; empty uses the account name"`
	GroupID          *string   `json:"group_id,omitempty" jsonschema:"a group id (users_search shows the group of each user)"`
	QuotaBytes       *uint64   `json:"quota_bytes,omitempty" jsonschema:"0 = unlimited"`
	QuotaReset       *string   `json:"quota_reset,omitempty" jsonschema:"none, day, week, month or rolling_month"`
	ExpiresUnix      *int64    `json:"expires_unix,omitempty" jsonschema:"unix seconds; 0 = never. A future date lifts 'expired'"`
	DeviceLimit      *uint32   `json:"device_limit,omitempty"`
	Apps             *appsArg  `json:"apps,omitempty"`
	Nodes            *nodesArg `json:"nodes,omitempty"`
	SpeedLimitBps    *uint64   `json:"speed_limit_bps,omitempty" jsonschema:"0 = remove the limit"`
	DNSPresetID      *string   `json:"dns_preset_id,omitempty" jsonschema:"empty string = inherit"`
}

func checkSubscriptionName(name string) error {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("subscription_name must be at most 64 characters without control characters")
	}
	return nil
}

var userUpdate = changeSpec[userUpdateArgs]{
	name: "user_update", min: ProfileOperator,
	procs: procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.UserServiceUpdateUserProcedure),
	desc:  "Change a user's account name, public subscription-page name, group, quota, expiry, device limit, apps, nodes, speed limit or DNS preset. Only the fields you give change.",
	plan: func(c *call, a userUpdateArgs) (*planned, error) {
		cur, err := c.getUser(a.UserID)
		if err != nil {
			return nil, err
		}
		u := cur.GetUser()
		var facts []Fact
		add := func(key, value string, untrusted bool) {
			facts = append(facts, Fact{Key: key, Value: value, Untrusted: untrusted})
		}
		change := func(key, value, from, to string) { facts = append(facts, changeFact(key, value, from, to)) }
		add("user", nm(u.GetName()), true)
		n := 0
		if a.Name != nil {
			if err := checkName(*a.Name); err != nil {
				return nil, err
			}
			add("name", nm(u.GetName())+" -> "+nm(*a.Name), true)
			n++
		}
		if a.SubscriptionName != nil {
			if err := checkSubscriptionName(*a.SubscriptionName); err != nil {
				return nil, err
			}
			add("subscription_name", nm(u.GetSubscriptionName())+" -> "+nm(strings.TrimSpace(*a.SubscriptionName)), true)
			n++
		}
		if a.GroupID != nil {
			if err := validID("group_id", *a.GroupID); err != nil {
				return nil, err
			}
			add("group", nm(u.GetGroupName())+" -> group id "+clean(*a.GroupID, 80), true)
			n++
		}
		if a.QuotaBytes != nil {
			change("quota", fmtBytes(u.GetQuotaBytes())+" -> "+fmtBytes(*a.QuotaBytes), strconv.FormatUint(u.GetQuotaBytes(), 10), strconv.FormatUint(*a.QuotaBytes, 10))
			n++
		}
		if a.QuotaReset != nil {
			if _, err := quotaReset(*a.QuotaReset); err != nil {
				return nil, err
			}
			was := enumName("QUOTA_RESET_", u.GetQuotaReset().String())
			change("quota_reset", was+" -> "+*a.QuotaReset, was, *a.QuotaReset)
			n++
		}
		if a.ExpiresUnix != nil {
			if *a.ExpiresUnix < 0 {
				return nil, errors.New("expires_unix cannot be negative")
			}
			change("expires", fmtTime(u.GetExpiresUnix())+" -> "+fmtTime(*a.ExpiresUnix), strconv.FormatInt(u.GetExpiresUnix(), 10), strconv.FormatInt(*a.ExpiresUnix, 10))
			n++
		}
		if a.DeviceLimit != nil {
			if *a.DeviceLimit > 100 {
				return nil, errors.New("device_limit is at most 100")
			}
			change("device_limit", fmt.Sprintf("%d -> %d", u.GetDeviceLimit(), *a.DeviceLimit), strconv.Itoa(int(u.GetDeviceLimit())), strconv.Itoa(int(*a.DeviceLimit)))
			n++
		}
		if a.Apps != nil {
			if err := a.Apps.check(); err != nil {
				return nil, err
			}
			was := &appsArg{Happ: u.GetApps().GetHapp(), Amnezia: u.GetApps().GetAmnezia()}
			change("apps", was.text()+" -> "+a.Apps.text(), was.code(), a.Apps.code())
			n++
		}
		if a.Nodes != nil {
			if err := a.Nodes.check(); err != nil {
				return nil, err
			}
			was := &nodesArg{All: u.GetNodes().GetAll(), NodeIDs: u.GetNodes().GetNodeIds()}
			change("nodes", was.text()+" -> "+a.Nodes.text(), was.word(), a.Nodes.word())
			n++
		}
		if a.SpeedLimitBps != nil {
			change("speed_limit", fmt.Sprintf("%d -> %d bit/s (0 = none)", u.GetSpeedLimitBps(), *a.SpeedLimitBps),
				strconv.FormatUint(u.GetSpeedLimitBps(), 10), strconv.FormatUint(*a.SpeedLimitBps, 10))
			n++
		}
		if a.DNSPresetID != nil {
			if *a.DNSPresetID != "" {
				if err := validID("dns_preset_id", *a.DNSPresetID); err != nil {
					return nil, err
				}
			}
			add("dns_preset", clean(u.GetDnsPresetId(), 80)+" -> "+clean(*a.DNSPresetID, 80), false)
			n++
		}
		if n == 0 {
			return nil, errors.New("nothing to change: give at least one field")
		}
		return &planned{Summary: "Change " + plural(n, "setting", "settings") + " of one user.", Facts: facts}, nil
	},
	apply: func(c *call, a userUpdateArgs, _ Plan) (done, error) {
		req := &adminv1.UpdateUserRequest{
			UserId: a.UserID, Name: a.Name, SubscriptionName: a.SubscriptionName, GroupId: a.GroupID, QuotaBytes: a.QuotaBytes, ExpiresUnix: a.ExpiresUnix,
			DeviceLimit: a.DeviceLimit, Apps: a.Apps.proto(), Nodes: a.Nodes.proto(), SpeedLimitBps: a.SpeedLimitBps, DnsPresetId: a.DNSPresetID,
		}
		if a.QuotaReset != nil {
			q, _ := quotaReset(*a.QuotaReset)
			req.QuotaReset = &q
		}
		if _, err := c.cl.User.UpdateUser(c.ctx, connect.NewRequest(req)); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("user_updated", "User updated."), nil
	},
}

// ---------------------------------------------------------------------------------------------------------------------
// user_disable, user_enable

type usersArgs struct {
	ReasonField
	UserIDs []string `json:"user_ids" jsonschema:"1 to 50 user ids"`
}

func usersPlan(enable bool) func(c *call, a usersArgs) (*planned, error) {
	return func(c *call, a usersArgs) (*planned, error) {
		ids, err := uniqueIDs("user_ids", a.UserIDs, false)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, id := range ids {
			r, err := c.getUser(id)
			if err != nil {
				return nil, err
			}
			if len(names) < 10 {
				names = append(names, nm(r.GetUser().GetName()))
			}
		}
		verb, effect, code := "Disable", "their connections end and their devices are dropped from the nodes", "disable"
		var danger []string
		if enable {
			verb, effect, code = "Enable", "they can connect again", "enable"
		} else if len(ids) > MaxUnattendedBulk {
			danger = []string{dangerBulk}
		}
		list := strings.Join(names, ", ")
		if len(ids) > len(names) {
			list += fmt.Sprintf(" and %d more", len(ids)-len(names))
		}
		return &planned{
			Summary: verb + " " + plural(len(ids), "user", "users") + ": " + effect + ".",
			Facts: []Fact{
				{Key: "count", Value: strconv.Itoa(len(ids))},
				{Key: "users", Value: list, Untrusted: true},
				codedFact("effect", effect, code),
			},
			Danger: danger, Params: usersArgs{UserIDs: ids},
		}, nil
	}
}

func usersApply(enable bool) func(c *call, a usersArgs, _ Plan) (done, error) {
	return func(c *call, a usersArgs, _ Plan) (done, error) {
		r, err := c.cl.User.SetUsersEnabled(c.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: a.UserIDs, Enabled: enable}))
		if err != nil {
			return done{}, apiError(err)
		}
		n := len(r.Msg.GetUsers())
		if enable {
			return doneWith("users_enabled", plural(n, "user", "users")+" enabled.", "n", strconv.Itoa(n)), nil
		}
		return doneWith("users_disabled", plural(n, "user", "users")+" disabled.", "n", strconv.Itoa(n)), nil
	}
}

var userDisable = changeSpec[usersArgs]{
	name: "user_disable", min: ProfileOperator, danger: true,
	procs: procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.UserServiceSetUsersEnabledProcedure),
	desc:  fmt.Sprintf("Disable up to 50 users: their connections end and their devices leave the nodes. More than %d users at once needs the owner's approval.", MaxUnattendedBulk),
	plan:  usersPlan(false), apply: usersApply(false),
}

var userEnable = changeSpec[usersArgs]{
	name: "user_enable", min: ProfileOperator,
	procs: procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.UserServiceSetUsersEnabledProcedure),
	desc:  "Enable up to 50 users that were disabled.",
	plan:  usersPlan(true), apply: usersApply(true),
}

// ---------------------------------------------------------------------------------------------------------------------
// user_reset_traffic

var userResetTraffic = changeSpec[usersArgs]{
	name: "user_reset_traffic", min: ProfileOperator, danger: true,
	procs: procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.UserServiceResetUserTrafficProcedure),
	desc: fmt.Sprintf("Zero the traffic counter of the current quota period for up to 50 users (a user that hit the quota is active again). "+
		"History is kept; the counter cannot be restored. More than %d users at once needs the owner's approval.", MaxUnattendedBulk),
	plan: func(c *call, a usersArgs) (*planned, error) {
		ids, err := uniqueIDs("user_ids", a.UserIDs, false)
		if err != nil {
			return nil, err
		}
		var list []string
		var used uint64
		for _, id := range ids {
			r, err := c.getUser(id)
			if err != nil {
				return nil, err
			}
			used += r.GetUser().GetUsedBytes()
			if len(list) < 10 {
				list = append(list, nm(r.GetUser().GetName()))
			}
		}
		var danger []string
		if len(ids) > MaxUnattendedBulk {
			danger = []string{dangerBulk}
		}
		names := strings.Join(list, ", ")
		if len(ids) > len(list) {
			names += fmt.Sprintf(" and %d more", len(ids)-len(list))
		}
		return &planned{
			Summary: "Reset the traffic counter of " + plural(len(ids), "user", "users") + " to zero.",
			Facts: []Fact{
				{Key: "count", Value: strconv.Itoa(len(ids))},
				{Key: "users", Value: names, Untrusted: true},
				codedFact("used", fmtBytes(used)+" in all", "bytes", "bytes", strconv.FormatUint(used, 10)),
				codedFact("effect", "the used traffic of the current period becomes 0; a user stopped by the quota can connect again", "traffic_reset"),
			},
			Danger: danger, Params: usersArgs{UserIDs: ids},
		}, nil
	},
	apply: func(c *call, a usersArgs, _ Plan) (done, error) {
		r, err := c.cl.User.ResetUserTraffic(c.ctx, connect.NewRequest(&adminv1.ResetUserTrafficRequest{UserIds: a.UserIDs}))
		if err != nil {
			return done{}, apiError(err)
		}
		n := len(r.Msg.GetUsers())
		return doneWith("traffic_reset", "Traffic counter reset for "+plural(n, "user", "users")+".", "n", strconv.Itoa(n)), nil
	},
}

// ---------------------------------------------------------------------------------------------------------------------
// device_revoke

type deviceRevokeArgs struct {
	ReasonField
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id" jsonschema:"a device id from user_devices"`
}

var deviceRevoke = changeSpec[deviceRevokeArgs]{
	name: "device_revoke", min: ProfileOperator,
	procs: procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.UserServiceRevokeDeviceProcedure),
	desc:  "Revoke one device of a user: its credentials are dropped from every node and the device must be set up again.",
	plan: func(c *call, a deviceRevokeArgs) (*planned, error) {
		if err := validID("device_id", a.DeviceID); err != nil {
			return nil, err
		}
		r, err := c.getUser(a.UserID)
		if err != nil {
			return nil, err
		}
		for _, d := range r.GetDevices() {
			if d.GetId() != a.DeviceID {
				continue
			}
			return &planned{
				Summary: "Revoke one device of a user: its credentials leave every node.",
				Facts: []Fact{
					{Key: "user", Value: nm(r.GetUser().GetName()), Untrusted: true},
					{Key: "device", Value: nm(d.GetPlatform() + " " + d.GetModel()), Untrusted: true},
					timeFact("last_seen", d.GetLastSeenUnix()),
					codedFact("effect", "the device is disconnected and must be set up again", "device"),
				},
			}, nil
		}
		return nil, errors.New("this user has no such device")
	},
	apply: func(c *call, a deviceRevokeArgs, _ Plan) (done, error) {
		if _, err := c.cl.User.RevokeDevice(c.ctx, connect.NewRequest(&adminv1.RevokeDeviceRequest{DeviceId: a.DeviceID})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("device_revoked", "Device revoked."), nil
	},
}

// ---------------------------------------------------------------------------------------------------------------------
// alert_mute

type alertMuteArgs struct {
	ReasonField
	AlertID   string `json:"alert_id"`
	DurationS uint32 `json:"duration_s" jsonschema:"seconds to mute for, at most 604800 (7 days); 0 unmutes"`
}

var alertMute = changeSpec[alertMuteArgs]{
	name: "alert_mute", min: ProfileOperator,
	procs: procs(adminv1connect.HealthServiceListAlertsProcedure, adminv1connect.HealthServiceMuteAlertProcedure),
	desc:  "Mute an active alert for a while, or unmute it (duration 0). A muted alert stays on the list but stops notifying.",
	plan: func(c *call, a alertMuteArgs) (*planned, error) {
		if err := validID("alert_id", a.AlertID); err != nil {
			return nil, err
		}
		if a.DurationS > 604800 {
			return nil, errors.New("duration_s is at most 604800")
		}
		r, err := c.cl.Health.ListAlerts(c.ctx, connect.NewRequest(&adminv1.ListAlertsRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		for _, al := range r.Msg.GetActive() {
			if al.GetId() != a.AlertID {
				continue
			}
			what := "for " + plural(int(a.DurationS), "second", "seconds")
			verb := "Mute"
			duration := secondsFact("duration", a.DurationS)
			if a.DurationS == 0 {
				verb, what = "Unmute", "now"
				duration = codedFact("duration", "0 s", "unmute")
			}
			kind, sev := enumName("ALERT_KIND_", al.GetKind().String()), enumName("ALERT_SEVERITY_", al.GetSeverity().String())
			return &planned{
				Summary: verb + " one active alert " + what + ".",
				Facts: []Fact{
					codedFact("alert", kind+", "+sev, "alert", "kind", kind, "severity", sev),
					{Key: "node", Value: nm(al.GetNodeName()), Untrusted: true},
					duration,
				},
			}, nil
		}
		return nil, errors.New("no such active alert")
	},
	apply: func(c *call, a alertMuteArgs, _ Plan) (done, error) {
		if _, err := c.cl.Health.MuteAlert(c.ctx, connect.NewRequest(&adminv1.MuteAlertRequest{AlertId: a.AlertID, DurationS: a.DurationS})); err != nil {
			return done{}, apiError(err)
		}
		if a.DurationS == 0 {
			return doneWith("alert_unmuted", "Alert unmuted."), nil
		}
		return doneWith("alert_muted", "Alert muted.", "seconds", strconv.FormatUint(uint64(a.DurationS), 10)), nil
	},
}

// ---------------------------------------------------------------------------------------------------------------------
// node_fix

type nodeFixArgs struct {
	ReasonField
	Node   string            `json:"node" jsonschema:"node id or exact name"`
	FixID  string            `json:"fix_id" jsonschema:"a fix id from the node's doctor report (node_doctor)"`
	Params map[string]string `json:"params,omitempty" jsonschema:"the fix's parameters, if the doctor item listed any"`
}

// fixFact names the fix for the owner: its id as the code, and for a restart of one profile on the node, which one
// (its name is data, so it is an untrusted param).
func (c *call) fixFact(nodeID, fixID string, params map[string]string) Fact {
	f := codedFact("fix", clean(fixID, 60), clean(fixID, 60))
	inb := params["inbound_id"]
	if fixID != "restart_inbound" || inb == "" {
		return f
	}
	r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: nodeID}))
	if err != nil {
		return f // the plan still says what the fix is; the dry run below has the last word
	}
	for _, in := range r.Msg.GetInbounds() {
		if in.GetId() == inb {
			f.Params = map[string]string{"profile": nm(in.GetProfileName()), "port": strconv.Itoa(int(in.GetPort()))}
			f.UntrustedParams = []string{"profile"}
		}
	}
	return f
}

var nodeFix = changeSpec[nodeFixArgs]{
	name: "node_fix", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure, adminv1connect.HealthServiceApplyFixProcedure),
	desc:  "Apply one of the doctor's fixes on a node. Always needs the owner's approval in the admin panel. The plan is the node's own dry run.",
	plan: func(c *call, a nodeFixArgs) (*planned, error) {
		if err := validID("fix_id", a.FixID); err != nil {
			return nil, err
		}
		if len(a.Params) > maxParams {
			return nil, errors.New("too many params")
		}
		for k, v := range a.Params {
			if len(k) > 40 || len(v) > 200 {
				return nil, errors.New("a param is too long")
			}
		}
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		// The dry run changes nothing: it is the only call that runs under the planning grant, and it always says dry_run.
		r, err := c.cl.Health.ApplyFix(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.ApplyFixRequest{
			NodeId: id, FixId: a.FixID, Params: a.Params, DryRun: true,
		}))
		if err != nil {
			return nil, apiError(err)
		}
		if e := r.Msg.GetError(); e != "" || r.Msg.GetPlanId() == "" {
			return nil, errors.New("the node cannot plan this fix: " + clean(e, 200))
		}
		fp := r.Msg.GetPlan()
		return &planned{
			Summary: "Apply one fix on one node, as the doctor planned it. The owner has to approve it first.",
			Facts: []Fact{
				{Key: "node", Value: nm(name), Untrusted: true},
				c.fixFact(id, a.FixID, a.Params),
				{Key: "detail", Value: clean(fp.GetDetail(), 300), Untrusted: true},
				{Key: "drops_sessions", Value: strconv.FormatBool(fp.GetDisruptive())},
			},
			Danger: []string{dangerFleet}, InnerRef: r.Msg.GetPlanId(),
			Params: nodeFixArgs{Node: id, FixID: a.FixID, Params: a.Params},
		}, nil
	},
	apply: func(c *call, a nodeFixArgs, pl Plan) (done, error) {
		r, err := c.cl.Health.ApplyFix(c.ctx, connect.NewRequest(&adminv1.ApplyFixRequest{
			NodeId: a.Node, FixId: a.FixID, Params: a.Params, DryRun: false, PlanId: pl.InnerRef,
		}))
		if err != nil {
			return done{}, apiError(err)
		}
		if e := r.Msg.GetError(); e != "" || !r.Msg.GetApplied() {
			return done{}, failure("fix_not_applied", "the node did not apply the fix: "+clean(e, 200))
		}
		n := strconv.Itoa(int(r.Msg.GetAffected()))
		return doneWith("fix_applied", fmt.Sprintf("Fix applied (affected: %s).", n), "n", n), nil
	},
}

// ---------------------------------------------------------------------------------------------------------------------
// rollouts and rollback

func (c *call) updates() (*adminv1.GetUpdatesResponse, error) {
	r, err := c.cl.Update.GetUpdates(c.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg, nil
}

var rolloutDanger = []string{dangerStepUp, dangerFleet}

func updateTimezoneName(offset int32) string {
	sign := "+"
	n := int(offset)
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, n/60, n%60)
}

func validUpdateTimezone(offset int32) bool {
	return offset >= -12*60 && offset <= 14*60 && offset%15 == 0
}

func scheduleFactTime(local string, offset int32) (string, error) {
	if !validUpdateTimezone(offset) || len(local) != len("2006-01-02T15:04") {
		return "", errors.New("invalid local date, time or timezone")
	}
	zoneName := updateTimezoneName(offset)
	t, err := time.ParseInLocation("2006-01-02T15:04", local, time.FixedZone(zoneName, int(offset)*60))
	if err != nil || t.Format("2006-01-02T15:04") != local {
		return "", errors.New("invalid local date or time")
	}
	return t.Format("2006-01-02 15:04") + " " + zoneName, nil
}

type nodeUpdateScheduleArgs struct {
	ReasonField
	NodeID                string `json:"node_id" jsonschema:"the node id from updates_status"`
	LocalDatetime         string `json:"local_datetime" jsonschema:"the desired date and time in YYYY-MM-DDTHH:mm format, interpreted in the panel's configured UTC offset"`
	TimezoneOffsetMinutes int32  `json:"timezone_offset_minutes,omitempty" jsonschema:"-"`
	ExpectedVersion       string `json:"expected_version,omitempty" jsonschema:"-"`
	ExpectedBuilt         int64  `json:"expected_built,omitempty" jsonschema:"-"`
}

var nodeUpdateSchedule = changeSpec[nodeUpdateScheduleArgs]{
	name: "node_update_schedule", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.UpdateServiceScheduleNodeUpdateProcedure),
	desc:  "Schedule the currently trusted signed agent bundle for one node. The update starts only after the selected time and the owner approves this exact plan.",
	plan: func(c *call, a nodeUpdateScheduleArgs) (*planned, error) {
		if err := validID("node_id", a.NodeID); err != nil {
			return nil, err
		}
		u, err := c.updates()
		if err != nil {
			return nil, err
		}
		bundle := u.GetBundle()
		if bundle.GetStatus() != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED {
			return nil, errors.New("the update bundle is not trusted: nothing can be scheduled")
		}
		var node *adminv1.NodeUpdate
		for _, n := range u.GetNodes() {
			if n.GetNodeId() == a.NodeID {
				node = n
				break
			}
		}
		if node == nil {
			return nil, errors.New("node_id is not a node of this panel")
		}
		if !node.GetSupportsUpdate() || node.GetBuilt() >= bundle.GetBuilt() ||
			(node.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED &&
				node.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK &&
				node.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_FAILED &&
				node.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OFFLINE) {
			return nil, errors.New("this node does not have an eligible older agent")
		}
		offset := u.GetScheduleTimezoneOffsetMinutes()
		at, err := scheduleFactTime(a.LocalDatetime, offset)
		if err != nil {
			return nil, err
		}
		scheduledAt, err := time.ParseInLocation("2006-01-02T15:04", a.LocalDatetime, time.FixedZone(updateTimezoneName(offset), int(offset)*60))
		if err != nil || u.GetNowUnix() == 0 {
			return nil, errors.New("the local date, time or panel clock is invalid")
		}
		panelNow := time.Unix(u.GetNowUnix(), 0)
		if scheduledAt.Before(panelNow.Add(time.Minute)) || scheduledAt.After(panelNow.Add(365*24*time.Hour)) {
			return nil, errors.New("schedule time must be at least one minute and at most one year in the future")
		}
		facts := []Fact{
			{Key: "node", Value: nm(node.GetName()), Untrusted: true},
			{Key: "version", Value: clean(bundle.GetVersion(), 60)},
			{Key: "scheduled_at", Value: at},
			codedFact("timezone", updateTimezoneName(offset), "utc_offset", "minutes", strconv.Itoa(int(offset))),
			codedFact("effect", "the node restarts its agent; the panel runs the update health gate and automatic rollback", "restart"),
		}
		if node.GetScheduledUnix() > 0 {
			facts = append(facts, codedFact("existing_schedule", "the node's previous schedule will be replaced", "replace"))
		}
		params := a
		params.TimezoneOffsetMinutes = offset
		params.ExpectedVersion = bundle.GetVersion()
		params.ExpectedBuilt = bundle.GetBuilt()
		return &planned{
			Summary: "Schedule the signed agent update for one node at " + at + ". The exact version and time zone are pinned in the plan; the owner must approve before it is saved.",
			Facts:   facts, Danger: rolloutDanger, Params: params,
		}, nil
	},
	apply: func(c *call, a nodeUpdateScheduleArgs, _ Plan) (done, error) {
		u, err := c.updates()
		if err != nil {
			return done{}, err
		}
		if u.GetBundle().GetVersion() != a.ExpectedVersion || u.GetBundle().GetBuilt() != a.ExpectedBuilt || u.GetScheduleTimezoneOffsetMinutes() != a.TimezoneOffsetMinutes {
			return done{}, failure("changed_since_plan", "the release or schedule time zone changed after planning; make a new plan")
		}
		r, err := c.cl.Update.ScheduleNodeUpdate(c.ctx, connect.NewRequest(&adminv1.ScheduleNodeUpdateRequest{
			NodeId: a.NodeID, LocalDatetime: a.LocalDatetime, TimezoneOffsetMinutes: a.TimezoneOffsetMinutes,
			ExpectedVersion: a.ExpectedVersion, ExpectedBuilt: a.ExpectedBuilt,
		}))
		if err != nil {
			return done{}, apiError(err)
		}
		return doneWith("node_update_scheduled", "Node update scheduled for "+strconv.FormatInt(r.Msg.GetScheduledUnix(), 10)+".", "node_id", a.NodeID, "version", a.ExpectedVersion), nil
	},
}

type nodeUpdateScheduleCancelArgs struct {
	ReasonField
	NodeID string `json:"node_id" jsonschema:"the node id from updates_status"`
}

var nodeUpdateScheduleCancel = changeSpec[nodeUpdateScheduleCancelArgs]{
	name: "node_update_schedule_cancel", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.UpdateServiceCancelNodeUpdateScheduleProcedure),
	desc:  "Cancel one node's pending agent update schedule. An update already in progress is not interrupted.",
	plan: func(c *call, a nodeUpdateScheduleCancelArgs) (*planned, error) {
		if err := validID("node_id", a.NodeID); err != nil {
			return nil, err
		}
		u, err := c.updates()
		if err != nil {
			return nil, err
		}
		for _, n := range u.GetNodes() {
			if n.GetNodeId() != a.NodeID {
				continue
			}
			if n.GetScheduledUnix() == 0 {
				return nil, errors.New("this node has no pending update schedule")
			}
			return &planned{Summary: "Cancel the pending update schedule for one node. The already installed agent stays unchanged.", Facts: []Fact{
				{Key: "node", Value: nm(n.GetName()), Untrusted: true},
				{Key: "scheduled_version", Value: clean(n.GetScheduledVersion(), 60)},
				timeFact("scheduled_at", n.GetScheduledUnix()),
			}, Danger: rolloutDanger, Params: a}, nil
		}
		return nil, errors.New("node_id is not a node of this panel")
	},
	apply: func(c *call, a nodeUpdateScheduleCancelArgs, _ Plan) (done, error) {
		r, err := c.cl.Update.CancelNodeUpdateSchedule(c.ctx, connect.NewRequest(&adminv1.CancelNodeUpdateScheduleRequest{NodeId: a.NodeID}))
		if err != nil {
			return done{}, apiError(err)
		}
		if !r.Msg.GetCancelled() {
			return done{}, failure("changed_since_plan", "the schedule has already been removed; check the current update status")
		}
		return doneWith("node_update_schedule_cancelled", "Node update schedule cancelled.", "node_id", a.NodeID), nil
	},
}

type updateTimezoneArgs struct {
	ReasonField
	TimezoneOffsetMinutes int32 `json:"timezone_offset_minutes" jsonschema:"fixed UTC offset for new node-update schedules, in minutes east of UTC; common values are -720 through 840"`
}

var updateTimezone = changeSpec[updateTimezoneArgs]{
	name: "update_timezone", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.UpdateServiceSetUpdateTimezoneProcedure),
	desc:  "Set the panel's fixed UTC offset for entering new node-update schedules. Existing scheduled instants keep their saved offset.",
	plan: func(c *call, a updateTimezoneArgs) (*planned, error) {
		if !validUpdateTimezone(a.TimezoneOffsetMinutes) {
			return nil, errors.New("timezone_offset_minutes must be from -720 to 840 in 15-minute steps")
		}
		u, err := c.updates()
		if err != nil {
			return nil, err
		}
		return &planned{Summary: "Change the fixed UTC offset used to enter future node-update schedules. Existing schedules will keep their current instant and display offset.", Facts: []Fact{
			codedFact("from", updateTimezoneName(u.GetScheduleTimezoneOffsetMinutes()), "utc_offset", "minutes", strconv.Itoa(int(u.GetScheduleTimezoneOffsetMinutes()))),
			codedFact("to", updateTimezoneName(a.TimezoneOffsetMinutes), "utc_offset", "minutes", strconv.Itoa(int(a.TimezoneOffsetMinutes))),
		}, Danger: []string{dangerStepUp}, Params: a}, nil
	},
	apply: func(c *call, a updateTimezoneArgs, _ Plan) (done, error) {
		r, err := c.cl.Update.SetUpdateTimezone(c.ctx, connect.NewRequest(&adminv1.SetUpdateTimezoneRequest{TimezoneOffsetMinutes: a.TimezoneOffsetMinutes}))
		if err != nil {
			return done{}, apiError(err)
		}
		return doneWith("update_timezone_changed", "Schedule time zone set to "+updateTimezoneName(r.Msg.GetTimezoneOffsetMinutes())+".", "offset_minutes", strconv.Itoa(int(r.Msg.GetTimezoneOffsetMinutes()))), nil
	},
}

type rolloutStartArgs struct {
	ReasonField
	// No batch size: one node is its own canary, and StartRollout's batch limit has nothing to split.
	NodeIDs []string `json:"node_ids" jsonschema:"exactly one node id from updates_status to update now"`
	// The bundle of the plan, pinned so the owner approves one exact release (set by the plan, not the agent).
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"-"`
	ExpectedBuilt   int64  `json:"expected_built,omitempty" jsonschema:"-"`
}

var rolloutStart = changeSpec[rolloutStartArgs]{
	name: "rollout_start", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.UpdateServiceStartRolloutProcedure),
	desc:  "Update one selected node now to the signed bundle. For a future time use node_update_schedule. Needs the owner's approval in the admin panel.",
	plan: func(c *call, a rolloutStartArgs) (*planned, error) {
		ids, err := uniqueIDs("node_ids", a.NodeIDs, true)
		if err != nil {
			return nil, err
		}
		if len(ids) != 1 {
			return nil, errors.New("node_ids must contain exactly one node; schedule or update nodes individually")
		}
		u, err := c.updates()
		if err != nil {
			return nil, err
		}
		if b := u.GetBundle(); b.GetStatus() != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED {
			return nil, errors.New("the update bundle is not trusted (status " + enumName("BUNDLE_STATUS_", b.GetStatus().String()) + "): nothing to roll out")
		}
		if r := u.GetRollout(); r.GetStatus() == adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING || r.GetStatus() == adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED {
			return nil, errors.New("a rollout is already active: pause or cancel it first")
		}
		var chosen *adminv1.NodeUpdate
		for _, n := range u.GetNodes() {
			if n.GetNodeId() == ids[0] {
				chosen = n
				break
			}
		}
		if chosen == nil {
			return nil, errors.New("node_id is not a node of this panel")
		}
		if !chosen.GetSupportsUpdate() || chosen.GetBuilt() >= u.GetBundle().GetBuilt() ||
			(chosen.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED &&
				chosen.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK &&
				chosen.GetState() != adminv1.NodeUpdateState_NODE_UPDATE_STATE_FAILED) {
			return nil, errors.New("the selected node has no supported online agent older than this bundle")
		}
		return &planned{
			Summary: "Update node " + nm(chosen.GetName()) + " to the signed agent bundle now. The owner has to approve it first.",
			Facts: []Fact{
				{Key: "version", Value: clean(u.GetBundle().GetVersion(), 60)},
				{Key: "node", Value: nm(chosen.GetName()), Untrusted: true},
				codedFact("effect", "the selected node restarts its agent and passes the update health gate", "restart"),
			},
			Danger: rolloutDanger, Params: rolloutStartArgs{NodeIDs: ids, ExpectedVersion: u.GetBundle().GetVersion(), ExpectedBuilt: u.GetBundle().GetBuilt()},
		}, nil
	},
	apply: func(c *call, a rolloutStartArgs, _ Plan) (done, error) {
		u, err := c.updates()
		if err != nil {
			return done{}, err
		}
		if r := u.GetRollout(); r.GetStatus() == adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING || r.GetStatus() == adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED {
			return done{}, failure("changed_since_plan", "changed since the plan (a rollout is active now): make a new plan")
		}
		if u.GetBundle().GetVersion() != a.ExpectedVersion || u.GetBundle().GetBuilt() != a.ExpectedBuilt {
			return done{}, failure("changed_since_plan", "the release changed after planning; make a new plan")
		}
		r, err := c.cl.Update.StartRollout(c.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{
			NodeIds: a.NodeIDs, ExpectedVersion: a.ExpectedVersion, ExpectedBuilt: a.ExpectedBuilt,
		}))
		if err != nil {
			return done{}, apiError(err)
		}
		n := len(r.Msg.GetRollout().GetSteps())
		return doneWith("rollout_started", fmt.Sprintf("Rollout %s started on %s.", clean(r.Msg.GetRollout().GetId(), 60), plural(n, "node", "nodes")), "n", strconv.Itoa(n)), nil
	},
}

type rolloutIDArgs struct {
	ReasonField
	RolloutID string `json:"rollout_id" jsonschema:"the rollout id from updates_status"`
}

// pastTense of the rollout actions, for the agent's lines and the outcome codes.
var pastTense = map[string]string{"pause": "paused", "resume": "resumed", "cancel": "cancelled"}

// rolloutAction builds pause, resume and cancel: they differ in the states they fit and the procedure they call.
func rolloutAction(name, verb, proc, desc string, fits func(adminv1.RolloutStatus) bool, doIt func(c *call, id string) error) changeSpec[rolloutIDArgs] {
	find := func(c *call, id string) (*adminv1.Rollout, error) {
		if err := validID("rollout_id", id); err != nil {
			return nil, err
		}
		u, err := c.updates()
		if err != nil {
			return nil, err
		}
		r := u.GetRollout()
		if r == nil || r.GetId() != id {
			return nil, errors.New("no such rollout (only the active or last rollout is known)")
		}
		if !fits(r.GetStatus()) {
			return nil, errors.New("the rollout is " + enumName("ROLLOUT_STATUS_", r.GetStatus().String()) + ": it cannot be " + pastTense[verb] + " now")
		}
		return r, nil
	}
	return changeSpec[rolloutIDArgs]{
		name: name, min: ProfileAdmin, danger: true,
		procs: procs(adminv1connect.UpdateServiceGetUpdatesProcedure, proc), desc: desc + " Needs the owner's approval in the admin panel.",
		plan: func(c *call, a rolloutIDArgs) (*planned, error) {
			r, err := find(c, a.RolloutID)
			if err != nil {
				return nil, err
			}
			finished := 0
			for _, s := range r.GetSteps() {
				switch s.GetState() {
				case adminv1.StepState_STEP_STATE_PASSED, adminv1.StepState_STEP_STATE_FAILED, adminv1.StepState_STEP_STATE_ROLLED_BACK, adminv1.StepState_STEP_STATE_SKIPPED:
					finished++
				}
			}
			return &planned{
				Summary: "Ask to " + verb + " the running staged update. The owner has to approve it first.",
				Facts: []Fact{
					{Key: "rollout", Value: clean(r.GetId(), 60)},
					{Key: "version", Value: clean(r.GetToVersion(), 60)},
					codedFact("progress", fmt.Sprintf("%d of %d nodes finished", finished, len(r.GetSteps())), "progress",
						"done", strconv.Itoa(finished), "total", strconv.Itoa(len(r.GetSteps()))),
					codedFact("action", verb, verb),
				},
				Danger: rolloutDanger,
			}, nil
		},
		apply: func(c *call, a rolloutIDArgs, _ Plan) (done, error) {
			if _, err := find(c, a.RolloutID); err != nil {
				return done{}, failure("changed_since_plan", "changed since the plan: make a new plan ("+err.Error()+")")
			}
			if err := doIt(c, a.RolloutID); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("rollout_"+pastTense[verb], "Rollout "+pastTense[verb]+"."), nil
		},
	}
}

var (
	rolloutPause = rolloutAction("rollout_pause", "pause", adminv1connect.UpdateServicePauseRolloutProcedure, "Pause the running rollout: nodes already updated stay, the rest wait.",
		func(s adminv1.RolloutStatus) bool { return s == adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING },
		func(c *call, id string) error {
			_, err := c.cl.Update.PauseRollout(c.ctx, connect.NewRequest(&adminv1.PauseRolloutRequest{RolloutId: id}))
			return err
		})
	rolloutResume = rolloutAction("rollout_resume", "resume", adminv1connect.UpdateServiceResumeRolloutProcedure, "Resume a paused rollout.",
		func(s adminv1.RolloutStatus) bool { return s == adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED },
		func(c *call, id string) error {
			_, err := c.cl.Update.ResumeRollout(c.ctx, connect.NewRequest(&adminv1.ResumeRolloutRequest{RolloutId: id}))
			return err
		})
	rolloutCancel = rolloutAction("rollout_cancel", "cancel", adminv1connect.UpdateServiceCancelRolloutProcedure, "Cancel the rollout: nodes not yet updated are skipped.",
		func(s adminv1.RolloutStatus) bool {
			return s == adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING || s == adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED
		},
		func(c *call, id string) error {
			_, err := c.cl.Update.CancelRollout(c.ctx, connect.NewRequest(&adminv1.CancelRolloutRequest{RolloutId: id}))
			return err
		})
)

type nodeRollbackArgs struct {
	ReasonField
	Node string `json:"node" jsonschema:"node id or exact name"`
}

var nodeRollback = changeSpec[nodeRollbackArgs]{
	name: "node_rollback", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.UpdateServiceRollbackNodeProcedure),
	desc:  "Roll one node back to the version it ran before its last update. Needs the owner's approval in the admin panel.",
	plan: func(c *call, a nodeRollbackArgs) (*planned, error) {
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		u, err := c.updates()
		if err != nil {
			return nil, err
		}
		for _, n := range u.GetNodes() {
			if n.GetNodeId() != id {
				continue
			}
			prev := Fact{Key: "previous_version", Value: clean(n.GetLastUpdate().GetFromVersion(), 60)}
			if prev.Value == "" {
				prev = codedFact("previous_version", "unknown", "unknown")
			}
			return &planned{
				Summary: "Roll one node back to its previous agent version. The owner has to approve it first.",
				Facts: []Fact{
					{Key: "node", Value: nm(name), Untrusted: true},
					{Key: "current_version", Value: clean(n.GetVersion(), 60)},
					prev,
					codedFact("effect", "the node restarts its agent", "restart"),
				},
				Danger: rolloutDanger, Params: nodeRollbackArgs{Node: id},
			}, nil
		}
		return nil, errors.New("node not found in the update list")
	},
	apply: func(c *call, a nodeRollbackArgs, _ Plan) (done, error) {
		if _, err := c.cl.Update.RollbackNode(c.ctx, connect.NewRequest(&adminv1.RollbackNodeRequest{NodeId: a.Node})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("rollback_started", "Rollback started."), nil
	},
}
