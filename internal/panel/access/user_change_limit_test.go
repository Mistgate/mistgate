package access

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// These tests send real requests through auth.RequireSession into the user service, because the cap keys on the
// principal that middleware builds from the token row; nothing else can put a token principal into a context.

// inProcess serves a connect client from a handler without a socket, the way the MCP layer calls the API.
type inProcess struct {
	h      http.Handler
	secret string
	wrap   func(context.Context) context.Context
}

func (p inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.RemoteAddr = "192.0.2.10:4000"
	r.Header.Set("Authorization", "Bearer "+p.secret)
	if p.wrap != nil {
		r = r.WithContext(p.wrap(r.Context()))
	}
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	return w.Result(), nil
}

type capEnv struct {
	*fixture
	auth    *auth.Service
	h       http.Handler
	owner   string
	made    int
	secrets map[string]string
}

func newCapEnv(t *testing.T) *capEnv {
	t.Helper()
	f := newFixture(t)
	e := f.e
	authSvc, err := auth.New(e.st, auth.Config{RPID: "localhost", Origins: []string{"http://localhost:8081"}, Vault: e.s.vault},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e.sql(`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_carol', 'carol', 'owner', ?, 1)`, []byte("adm_carol"))
	mux := http.NewServeMux()
	path, uh := adminv1connect.NewUserServiceHandler(e.s)
	mux.Handle(path, uh)
	path, gh := adminv1connect.NewGroupServiceHandler(e.s)
	mux.Handle(path, gh)
	return &capEnv{fixture: f, auth: authSvc, h: authSvc.RequireSession(mux), owner: "adm_carol", secrets: map[string]string{}}
}

// token makes an admin-profile API token and returns its id and a client that calls as it.
func (c *capEnv) token(name string, allowBulk bool) (string, adminv1connect.UserServiceClient) {
	c.e.t.Helper()
	secret := auth.NewTokenSecret()
	sum := sha256.Sum256([]byte(secret))
	now := time.Now()
	tok := store.APIToken{
		ID: store.NewID("tok_"), Name: name, Profile: store.ProfileAdmin, Hint: secret[len(secret)-4:], RatePerMin: 600,
		CreatedBy: c.owner, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), AllowBulkUserChanges: allowBulk,
	}
	if err := c.e.st.CreateAPIToken(c.e.ctx, tok, sum[:]); err != nil {
		c.e.t.Fatal(err)
	}
	c.secrets[tok.ID] = secret
	return tok.ID, c.client(secret, nil)
}

func (c *capEnv) client(secret string, wrap func(context.Context) context.Context) adminv1connect.UserServiceClient {
	return adminv1connect.NewUserServiceClient(&http.Client{Transport: inProcess{h: c.h, secret: secret, wrap: wrap}}, "http://panel.test")
}

// users creates n enabled users and returns their ids.
func (c *capEnv) users(n int) []string {
	c.e.t.Helper()
	var ids []string
	for range n {
		c.made++
		ids = append(ids, c.e.user(fmt.Sprintf("u%02d", c.made), c.group, nil).User.Id)
	}
	return ids
}

// remaining is how many units the token may still spend this hour.
func (c *capEnv) remaining(tokenID string) int {
	c.e.t.Helper()
	d, err := c.e.s.limiter.Peek(c.e.ctx, tokenUserChangeWindow, tokenID)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return d.Remaining
}

func (c *capEnv) disabled(id string) bool {
	c.e.t.Helper()
	return must(c.e.s.GetUser(c.e.ctx, req(&adminv1.GetUserRequest{UserId: id}))).Msg.User.Status == adminv1.UserStatus_USER_STATUS_DISABLED
}

func (c *capEnv) audits(action string) []store.AuditRow {
	c.e.t.Helper()
	rows, err := c.e.st.ListAudit(c.e.ctx, "", 0, 500)
	if err != nil {
		c.e.t.Fatal(err)
	}
	var out []store.AuditRow
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func disable(ids ...string) *connect.Request[adminv1.SetUsersEnabledRequest] {
	return connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: ids, Enabled: false})
}

func rename(id, name string) *connect.Request[adminv1.UpdateUserRequest] {
	return connect.NewRequest(&adminv1.UpdateUserRequest{UserId: id, Name: &name})
}

func TestTokenUserChangesCountEveryUserTouched(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(4)
	if err := c.e.st.Access().AddUsage(ctx, ids[0], 100); err != nil {
		t.Fatal(err)
	}
	if err := c.e.st.Access().AddUsage(ctx, ids[1], 100); err != nil {
		t.Fatal(err)
	}
	devs := must(c.e.s.GetUser(ctx, req(&adminv1.GetUserRequest{UserId: ids[3]}))).Msg.Devices
	if len(devs) == 0 {
		t.Fatal("the fixture user has no device")
	}
	id, api := c.token("ci", false)

	steps := []struct {
		name string
		do   func() error
		want int // units left after the call
	}{
		{"disable three users", func() error { _, err := api.SetUsersEnabled(ctx, disable(ids[:3]...)); return err }, 7},
		{"repeating disable is free", func() error { _, err := api.SetUsersEnabled(ctx, disable(ids[:3]...)); return err }, 7},
		{"enable them again", func() error {
			_, err := api.SetUsersEnabled(ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: ids[:3], Enabled: true}))
			return err
		}, 7}, // enabling gives access back: it is never counted
		{"repeating enable is free", func() error {
			_, err := api.SetUsersEnabled(ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: ids[:3], Enabled: true}))
			return err
		}, 7},
		{"reset the traffic of two users", func() error {
			_, err := api.ResetUserTraffic(ctx, connect.NewRequest(&adminv1.ResetUserTrafficRequest{UserIds: ids[:2]}))
			return err
		}, 5},
		{"repeating an empty traffic reset is free", func() error {
			_, err := api.ResetUserTraffic(ctx, connect.NewRequest(&adminv1.ResetUserTrafficRequest{UserIds: ids[:2]}))
			return err
		}, 5},
		{"revoke one device", func() error {
			_, err := api.RevokeDevice(ctx, connect.NewRequest(&adminv1.RevokeDeviceRequest{DeviceId: devs[0].Id}))
			return err
		}, 4},
		{"update one user", func() error { _, err := api.UpdateUser(ctx, rename(ids[0], "renamed")); return err }, 3},
		{"repeat the same update is free", func() error { _, err := api.UpdateUser(ctx, rename(ids[0], "renamed")); return err }, 3},
		{"a read is free", func() error {
			_, err := api.GetUser(ctx, connect.NewRequest(&adminv1.GetUserRequest{UserId: ids[0]}))
			return err
		}, 3},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if got := c.remaining(id); got != s.want {
			t.Fatalf("%s: %d units left, want %d", s.name, got, s.want)
		}
	}
}

func TestTokenUserChangeLimitUsesFixedWindows(t *testing.T) {
	c := newCapEnv(t)
	e := c.e
	ids := c.users(20)
	id, api := c.token("boundary", false)
	if _, err := api.SetUsersEnabled(e.ctx, disable(ids[0])); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(59*time.Minute + 59*time.Second)
	if _, err := api.SetUsersEnabled(e.ctx, disable(ids[1:10]...)); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(time.Second)
	if _, err := api.SetUsersEnabled(e.ctx, disable(ids[10:20]...)); err != nil {
		t.Fatalf("new fixed window did not allow ten users at its boundary: %v", err)
	}
	for _, userID := range ids {
		if !c.disabled(userID) {
			t.Errorf("user %s remains enabled after its allowed change", userID)
		}
	}
	if got := c.remaining(id); got != 0 {
		t.Fatalf("current fixed window has %d units remaining, want 0", got)
	}
}

func TestTokenCallLargerThanLimitReturnsNoRetryAndClearError(t *testing.T) {
	c := newCapEnv(t)
	ids := c.users(TokenUserChangeLimit + 1)
	id, api := c.token("oversize", false)
	_, err := api.SetUsersEnabled(c.e.ctx, disable(ids...))
	wantCode(t, err, connect.CodeResourceExhausted)
	for _, want := range []string{"token_user_change_limit", "asked=11", "retry_after_seconds=0", "reason=over_hourly_limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("oversize refusal %q lacks %q", err, want)
		}
	}
	for _, userID := range ids {
		if c.disabled(userID) {
			t.Errorf("oversize refusal disabled %s", userID)
		}
	}
	if got := c.remaining(id); got != TokenUserChangeLimit {
		t.Fatalf("oversize refusal spent units: %d remain", got)
	}
}

func TestMemoryUndoToZeroKeepsWindowStart(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := securitylimit.NewMemory(func() time.Time { return clock }, 0)
	ctx := context.Background()
	first, err := limiter.RecordN(ctx, tokenUserChangeWindow, "tok_alice", TokenUserChangeLimit)
	if err != nil || !first.Allowed {
		t.Fatalf("reserve initial window: %+v, %v", first, err)
	}
	clock = clock.Add(time.Second)
	if err := limiter.UndoN(ctx, tokenUserChangeWindow, "tok_alice", TokenUserChangeLimit, first.WindowStart); err != nil {
		t.Fatal(err)
	}
	if got := limiter.Size(tokenUserChangeWindow.Name); got != 1 {
		t.Fatalf("undo to zero removed the window, size = %d", got)
	}
	clock = clock.Add(30 * time.Minute)
	second, err := limiter.RecordN(ctx, tokenUserChangeWindow, "tok_alice", TokenUserChangeLimit)
	if err != nil || !second.Allowed || !second.WindowStart.Equal(first.WindowStart) {
		t.Fatalf("reservation after refund moved the window start: %+v, %v", second, err)
	}
	clock = clock.Add(31 * time.Minute)
	next, err := limiter.RecordN(ctx, tokenUserChangeWindow, "tok_alice", 1)
	if err != nil || !next.Allowed || !next.WindowStart.Equal(clock) {
		t.Fatalf("fixed window did not expire from its first change: %+v, %v", next, err)
	}
}

// What gives access (create, extend, enable) and what changes nothing is free: a billing script that repeats itself
// does not run into the limit.
func TestTokenCreateExtendEnableAndRepeatedCallsAreFree(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(4)
	for _, id := range ids[1:3] {
		if err := c.e.st.Access().SetUserExpiry(ctx, id, c.e.clock.Add(30*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	id, api := c.token("sync", false)
	for range 12 {
		if _, err := api.SetUsersEnabled(ctx, disable(ids[0])); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.remaining(id); got != 9 {
		t.Fatalf("repeated disable left %d units, want 9", got)
	}
	if _, err := api.SetUsersEnabled(ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: ids[:1], Enabled: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := api.ResetUserTraffic(ctx, connect.NewRequest(&adminv1.ResetUserTrafficRequest{UserIds: ids[3:4]})); err != nil {
		t.Fatal(err)
	}
	user := must(c.e.s.GetUser(ctx, req(&adminv1.GetUserRequest{UserId: ids[3]}))).Msg.User
	if _, err := api.UpdateUser(ctx, connect.NewRequest(&adminv1.UpdateUserRequest{UserId: ids[3], Name: &user.Name})); err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != 9 {
		t.Fatalf("enable plus no-op calls left %d units, want 9", got)
	}
	if _, err := api.ExtendUsers(ctx, connect.NewRequest(&adminv1.ExtendUsersRequest{UserIds: ids, Days: 7})); err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != 9 {
		t.Fatalf("extending two expiring users left %d units, want 9", got)
	}
	if _, err := api.ExtendUsers(ctx, connect.NewRequest(&adminv1.ExtendUsersRequest{UserIds: ids, Days: 7})); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateUser(ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "fresh", GroupId: c.group})); err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != 9 {
		t.Fatalf("second extension and user creation left %d units, want 9", got)
	}
}

func TestTokenDeleteGroupMoveCountsEveryMember(t *testing.T) {
	c := newCapEnv(t)
	e := c.e
	ids := c.users(12)
	if len(c.desiredCreds()) != len(ids) {
		t.Fatalf("setup: %d users with credentials, want %d", len(c.desiredCreds()), len(ids))
	}
	id, _ := c.token("group-move", false)
	groups := adminv1connect.NewGroupServiceClient(&http.Client{Transport: inProcess{h: c.h, secret: c.secrets[id]}}, "http://panel.test")
	empty, err := groups.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "void"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = groups.DeleteGroup(e.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: c.group, MoveUsersTo: empty.Msg.Group.Id}))
	wantCode(t, err, connect.CodeResourceExhausted)
	for _, want := range []string{"token_user_change_limit", "asked=12", "limit=10", "retry_after_seconds=0", "reason=over_hourly_limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("group-move refusal %q lacks %q", err, want)
		}
	}
	if len(c.desiredCreds()) != len(ids) {
		t.Fatalf("refused group move left %d user credentials, want %d", len(c.desiredCreds()), len(ids))
	}
	from, err := e.st.Access().Group(e.ctx, c.group)
	if err != nil || from.UserCount != len(ids) {
		t.Fatalf("source group after refusal: %+v, %v", from, err)
	}
	to, err := e.st.Access().Group(e.ctx, empty.Msg.Group.Id)
	if err != nil || to.UserCount != 0 {
		t.Fatalf("empty group after refusal: %+v, %v", to, err)
	}
	if got := c.remaining(id); got != TokenUserChangeLimit {
		t.Fatalf("oversize move spent units: %d remain", got)
	}
	rows := c.audits("user_change_limit")
	if len(rows) != 1 || rows[0].Actor != "token:"+id || !strings.Contains(rows[0].Params, adminv1connect.GroupServiceDeleteGroupProcedure) ||
		!strings.Contains(rows[0].Params, `"used":0`) || !strings.Contains(rows[0].Params, `"asked":12`) {
		t.Fatalf("group move refusal audit = %+v", rows)
	}
	events, _, err := e.st.Events(e.ctx, store.EventFilter{Limit: 10, Match: &store.CodeMatch{Codes: []string{"user_change_limit"}}})
	if err != nil || len(events) != 1 || events[0].Params["token_id"] != id || events[0].Params["procedure"] != adminv1connect.GroupServiceDeleteGroupProcedure {
		t.Fatalf("group move refusal events = %+v, %v", events, err)
	}
}

func TestGroupUpdateCountsMembersOnlyWhenAccessSetsChange(t *testing.T) {
	before := store.AccessGroup{UserCount: 7, ProfileIDs: []string{"prf_a"}, DisabledNodeIDs: []string{"nod_a"}}
	cases := []struct {
		name            string
		profiles        *[]string
		disabledNodeIDs *[]string
		dryRun          bool
		want            int
	}{
		{name: "profile set changes", profiles: &[]string{"prf_b"}, want: 7},
		{name: "available node set changes", disabledNodeIDs: &[]string{"nod_b"}, want: 7},
		{name: "unchanged sets", profiles: &[]string{"prf_a"}, disabledNodeIDs: &[]string{"nod_a"}},
		{name: "name-only change", want: 0},
		{name: "dry-run", profiles: &[]string{"prf_b"}, dryRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupUpdateUserChangeCount(before, tc.profiles, tc.disabledNodeIDs, tc.dryRun); got != tc.want {
				t.Fatalf("group update count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTokenUserChangeOverTheLimitIsRefusedWhole(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(12)
	id, api := c.token("ci", false)

	// Eight used, then four asked: nothing of the four may change, not even the two that would still fit.
	if _, err := api.SetUsersEnabled(ctx, disable(ids[:8]...)); err != nil {
		t.Fatal(err)
	}
	_, err := api.SetUsersEnabled(ctx, disable(ids[8:12]...))
	wantCode(t, err, connect.CodeResourceExhausted)
	for _, want := range []string{"token_user_change_limit", "limit=10", "used=8", "asked=4", "retry_after_seconds=3600"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	for _, uid := range ids[8:12] {
		if c.disabled(uid) {
			t.Fatalf("user %s was changed by a refused call", uid)
		}
	}
	if got := c.remaining(id); got != 2 {
		t.Fatalf("a refused call spent units: %d left, want 2", got)
	}

	// The tenth unit is allowed, the eleventh is not, for every counted call.
	if _, err := api.UpdateUser(ctx, rename(ids[8], "ten")); err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateUser(ctx, rename(ids[9], "nine")); err != nil {
		t.Fatal(err)
	}
	if err := c.e.st.Access().AddUsage(ctx, ids[10], 100); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"update":  second(api.UpdateUser(ctx, rename(ids[10], "eleven"))),
		"disable": second(api.SetUsersEnabled(ctx, disable(ids[10]))),
		"reset":   second(api.ResetUserTraffic(ctx, connect.NewRequest(&adminv1.ResetUserTrafficRequest{UserIds: ids[10:11]}))),
		"revoke":  second(api.RevokeDevice(ctx, connect.NewRequest(&adminv1.RevokeDeviceRequest{DeviceId: "dev_none"}))),
	} {
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Errorf("%s past the limit: %v", name, err)
		}
	}
	if got := must(c.e.s.GetUser(ctx, req(&adminv1.GetUserRequest{UserId: ids[10]}))).Msg.User.Name; got != "u11" {
		t.Fatalf("a refused update renamed the user to %q", got)
	}

	// Each refusal is audited with the token as the actor; the panel event is written once an hour.
	refused := c.audits("user_change_limit")
	if len(refused) != 5 { // the four above and the four-user call
		t.Fatalf("%d refusal audit rows, want 5", len(refused))
	}
	r := refused[len(refused)-1] // newest first, so this is the four-user call
	if r.Result != "refused" || r.Actor != "token:"+id || !strings.Contains(r.Params, adminv1connect.UserServiceSetUsersEnabledProcedure) ||
		!strings.Contains(r.Params, `"limit":10`) || !strings.Contains(r.Params, `"used":8`) || !strings.Contains(r.Params, `"asked":4`) {
		t.Errorf("first refusal audit row = %+v", r)
	}
	events, _, err := c.e.st.Events(ctx, store.EventFilter{Limit: 10, Match: &store.CodeMatch{Codes: []string{"user_change_limit"}}})
	if err != nil || len(events) != 1 || events[0].Severity != 2 || events[0].Params["token_id"] != id {
		t.Fatalf("refusal events = %+v, %v; want one warning for the token", events, err)
	}

	// An hour later the units are free again, and a new refusal writes a new event.
	c.e.clock = c.e.clock.Add(time.Hour + time.Second)
	if _, err := api.SetUsersEnabled(ctx, disable(ids[8:12]...)); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

func TestTokenUserChangeRefusalEventOncePerHour(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(2)
	_, api := c.token("ci", false)
	for range 10 {
		if _, err := api.UpdateUser(ctx, rename(ids[0], "x"+fmt.Sprint(c.e.clock.UnixNano()))); err != nil {
			t.Fatal(err)
		}
		c.e.clock = c.e.clock.Add(time.Second)
	}
	count := func() int {
		events, _, err := c.e.st.Events(ctx, store.EventFilter{Limit: 10, Match: &store.CodeMatch{Codes: []string{"user_change_limit"}}})
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	for range 3 {
		second(api.SetUsersEnabled(ctx, disable(ids[1])))
	}
	if got := count(); got != 1 {
		t.Fatalf("%d events for three refusals in one hour, want 1", got)
	}
	// The ten units expired with the hour, so the next call is allowed and writes no event.
	c.e.clock = c.e.clock.Add(time.Hour)
	if _, err := api.SetUsersEnabled(ctx, disable(ids[1], ids[0])); err != nil {
		t.Fatalf("after the window reset: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("%d events after the window reset, want still 1", got)
	}
}

func TestTokenExemptFromTheLimitChangesFreely(t *testing.T) {
	c := newCapEnv(t)
	ids := c.users(12)
	_, api := c.token("bulk", true)
	if _, err := api.SetUsersEnabled(c.e.ctx, disable(ids...)); err != nil {
		t.Fatalf("an exempt token disabling twelve users: %v", err)
	}
	for _, id := range ids {
		if !c.disabled(id) {
			t.Fatalf("user %s is still enabled", id)
		}
	}
}

func TestAdminSessionIsNotLimited(t *testing.T) {
	c := newCapEnv(t)
	ids := c.users(12)
	// No token principal in the context is how an admin's cookie session reaches the service.
	if _, err := c.e.s.SetUsersEnabled(c.e.ctx, disable(ids...)); err != nil {
		t.Fatalf("an admin disabling twelve users: %v", err)
	}
	if len(c.audits("user_change_limit")) != 0 {
		t.Fatal("an admin call was audited as a refusal")
	}
}

func TestAssistantIsNotLimited(t *testing.T) {
	c := newCapEnv(t)
	e := c.e
	ids := c.users(12)
	e.sql(`INSERT INTO assistant_settings(id, enabled, audience) VALUES (1, 1, 3)`)
	hidden, err := e.st.EnsureAssistantToken(e.ctx, c.owner, store.ProfileAdmin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	secret := auth.NewTokenSecret() // the hidden identity is checked by id, the secret only has to be well formed
	api := c.client(secret, func(ctx context.Context) context.Context {
		return auth.WithAssistant(ctx, hidden.ID, store.ProfileAdmin, c.owner, "carol")
	})
	if _, err := api.SetUsersEnabled(e.ctx, disable(ids...)); err != nil {
		t.Fatalf("the assistant disabling twelve users: %v", err)
	}
}

func TestOwnerApprovedPlanApplyIsNotLimitedAndSpendsNothing(t *testing.T) {
	c := newCapEnv(t)
	e := c.e
	ids := c.users(17)
	id, plain := c.token("ci", false)

	// An approved plan that is applying, as the MCP layer sets it up for the apply of an owner-approved tool.
	apply := func(tool string) context.Context {
		t.Helper()
		confirm := sha256.Sum256([]byte(store.NewID("cf_")))
		now := time.Now()
		p := store.MCPPlan{TokenID: id, Tool: tool, ParamsJSON: `{}`, FactsJSON: "[]", Summary: "s", Danger: `["bulk"]`, ConfirmHash: confirm[:],
			NeedsApproval: true, Status: store.PlanAwaiting, CreatedAt: now}
		if err := e.st.CreateMCPPlan(e.ctx, p, 20, 50); err != nil {
			t.Fatal(err)
		}
		got, err := e.st.MCPPlanByConfirm(e.ctx, id, confirm[:])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.DecideMCPPlan(e.ctx, got.ID, c.owner, true, now); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(got.ParamsJSON))
		if _, err := e.st.BeginApply(e.ctx, got.ID, id, tool, hash[:], got.ConfirmHash, now); err != nil {
			t.Fatal(err)
		}
		return c.auth.WithApprovedStepUp(auth.WithChannel(e.ctx, auth.ChannelMCP), got.ID)
	}
	if _, err := plain.SetUsersEnabled(e.ctx, disable(ids[:10]...)); err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != 0 {
		t.Fatalf("%d units left, want 0", got)
	}

	// The same token with the grant of its own tool's plan changes six more users and spends nothing.
	approved := func(grant context.Context) adminv1connect.UserServiceClient {
		return c.client(c.secrets[id], func(context.Context) context.Context {
			return grant // the grant lives under a key only auth can read, so it replaces the request's context
		})
	}
	if _, err := approved(apply("user_disable")).SetUsersEnabled(e.ctx, disable(ids[10:16]...)); err != nil {
		t.Fatalf("approved user_disable apply: %v", err)
	}
	if got := c.remaining(id); got != 0 {
		t.Fatalf("the approved apply spent units: %d left", got)
	}

	// The grant of one tool does not open another tool's procedure.
	_, err := approved(apply("user_update")).SetUsersEnabled(e.ctx, disable(ids[16]))
	wantCode(t, err, connect.CodeResourceExhausted)
	_, err = approved(apply("user_disable")).UpdateUser(e.ctx, rename(ids[0], "other"))
	wantCode(t, err, connect.CodeResourceExhausted)
}

func TestFailedTokenCallSpendsNothing(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(2)
	first := must(c.e.s.GetUser(ctx, req(&adminv1.GetUserRequest{UserId: ids[0]}))).Msg.User.Name
	id, api := c.token("ci", false)

	// The name is taken and the device does not exist: both fail after the cap was consulted.
	wantCode(t, second(api.UpdateUser(ctx, rename(ids[1], first))), connect.CodeAlreadyExists)
	wantCode(t, second(api.RevokeDevice(ctx, connect.NewRequest(&adminv1.RevokeDeviceRequest{DeviceId: "dev_none"}))), connect.CodeNotFound)
	// A user id nobody has touches no one, so it asks for no unit.
	second(api.SetUsersEnabled(ctx, disable("usr_none")))
	if got := c.remaining(id); got != TokenUserChangeLimit {
		t.Fatalf("failed calls left %d units, want %d", got, TokenUserChangeLimit)
	}
}

// brokenLimiter fails every operation, as an unreachable Durable Object would.
type brokenLimiter struct{ securitylimit.Limiter }

func (brokenLimiter) Peek(context.Context, securitylimit.Window, string) (securitylimit.Decision, error) {
	return securitylimit.Decision{}, errors.New("limiter down")
}
func (brokenLimiter) Record(context.Context, securitylimit.Window, string) (securitylimit.Decision, error) {
	return securitylimit.Decision{}, errors.New("limiter down")
}
func (brokenLimiter) RecordN(context.Context, securitylimit.Window, string, int) (securitylimit.Decision, error) {
	return securitylimit.Decision{}, errors.New("limiter down")
}

func TestLimiterErrorRefusesATokenButNotAnAdmin(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(2)
	_, api := c.token("ci", false)
	c.e.s.limiter = brokenLimiter{}

	wantCode(t, second(api.SetUsersEnabled(ctx, disable(ids[0]))), connect.CodeUnavailable)
	wantCode(t, second(api.UpdateUser(ctx, rename(ids[0], "nope"))), connect.CodeUnavailable)
	if c.disabled(ids[0]) {
		t.Fatal("a call refused for a broken limiter changed the user")
	}
	if len(c.audits("user_change_limit")) != 2 {
		t.Fatalf("limiter failures wrote %d refusal audit rows, want 2", len(c.audits("user_change_limit")))
	}
	for _, row := range c.audits("user_change_limit") {
		var params map[string]any
		if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
			t.Fatal(err)
		}
		if _, ok := params["used"]; ok || params["reason"] != "limiter_unavailable" {
			t.Errorf("limiter failure audit params = %v, want no used and reason=limiter_unavailable", params)
		}
	}
	if _, err := c.e.s.SetUsersEnabled(ctx, disable(ids[0])); err != nil {
		t.Fatalf("an admin with a broken limiter: %v", err)
	}
}
