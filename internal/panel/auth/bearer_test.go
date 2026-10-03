package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// --- helpers ---

// tokenEnv is a service with an owner, a cookie session and a handler stack behind RequireSession that records
// what the handlers would see.
type tokenEnv struct {
	t      *testing.T
	s      *Service
	st     *store.Store
	clock  *time.Time
	owner  store.Admin
	cookie string
	h      http.Handler
	seen   *seen
}

// seen is what the last request that reached the inner handler looked like.
type seen struct {
	calls     int
	admin     store.Admin
	principal Principal
	stepUpErr error
	body      []byte
}

func newTokenEnv(t *testing.T) *tokenEnv {
	t.Helper()
	s, st, clock := newTestService(t)
	owner := makeAdmin(t, s, st)
	c, err := s.newSession(context.Background(), owner.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sn := &seen{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sn.calls++
		sn.admin, _ = AdminFrom(r.Context())
		sn.principal = PrincipalFrom(r.Context())
		sn.stepUpErr = s.RequireStepUp(r.Context())
		sn.body, _ = readAll(r)
		// a module writing its own audit row with the request context: the source must follow the call
		st.Audit(r.Context(), s.now(), store.AuditEntry{Actor: sn.admin.ID, Action: "module_row", Result: "ok"})
		w.WriteHeader(http.StatusOK)
	})
	return &tokenEnv{t: t, s: s, st: st, clock: clock, owner: owner, cookie: c.Value, h: s.RequireSession(inner), seen: sn}
}

func readAll(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.Bytes(), err
}

// mkToken creates a token directly in the store and returns it with its secret.
func (e *tokenEnv) mkToken(name, profile string, rate int) (store.APIToken, string) {
	e.t.Helper()
	secret := NewTokenSecret()
	now := e.s.now()
	tok := store.APIToken{ID: store.NewID("tok_"), Name: name, Profile: profile, Hint: secret[len(secret)-4:], RatePerMin: rate,
		CreatedBy: e.owner.ID, CreatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)}
	if err := e.st.CreateAPIToken(context.Background(), tok, hashToken(secret)); err != nil {
		e.t.Fatal(err)
	}
	return tok, secret
}

type callOpts struct {
	ctx         context.Context // the in-process context (channel, grants); nil = a network request
	body        []byte
	contentType string
	headers     map[string]string
	remote      string
}

func (e *tokenEnv) call(path, secret string, o ...callOpts) *httptest.ResponseRecorder {
	e.t.Helper()
	var opt callOpts
	if len(o) > 0 {
		opt = o[0]
	}
	body := opt.body
	if body == nil {
		body = []byte("{}")
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if opt.ctx != nil {
		r = r.WithContext(opt.ctx)
	}
	ct := opt.contentType
	if ct == "" {
		ct = "application/json"
	}
	r.Header.Set("Content-Type", ct)
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range opt.headers {
		r.Header.Set(k, v)
	}
	if opt.remote != "" {
		r.RemoteAddr = opt.remote
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *tokenEnv) code(path, secret string, o ...callOpts) int {
	e.t.Helper()
	return e.call(path, secret, o...).Code
}

func (e *tokenEnv) audits(action string) []store.AuditRow {
	e.t.Helper()
	rows, err := e.st.ListAudit(context.Background(), "", 0, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.AuditRow
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

const (
	pGetUser      = adminv1connect.UserServiceGetUserProcedure // read
	pCreateUser   = adminv1connect.UserServiceCreateUserProcedure
	pListAudit    = adminv1connect.AuthServiceListAuditProcedure // owner level
	pDeleteUsers  = adminv1connect.UserServiceDeleteUsersProcedure
	pApplyFix     = adminv1connect.HealthServiceApplyFixProcedure
	pStartRollout = adminv1connect.UpdateServiceStartRolloutProcedure
)

func applyFixBody(t *testing.T, dry bool, ct string) []byte {
	t.Helper()
	m := &adminv1.ApplyFixRequest{NodeId: "nod_x", FixId: "restart_inbound", DryRun: dry}
	if ct == "application/proto" {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if dry {
		return []byte(`{"nodeId":"nod_x","fixId":"restart_inbound","dryRun":true}`)
	}
	return []byte(`{"nodeId":"nod_x","fixId":"restart_inbound","dryRun":false,"planId":"p"}`)
}

// --- section 3: the token path ---

func TestBearerRefusals(t *testing.T) {
	e := newTokenEnv(t)
	tok, secret := e.mkToken("ops", store.ProfileAdmin, 120)
	unauth := `{"code":"unauthenticated","message":"not signed in"}`

	for name, tc := range map[string]struct {
		headers map[string]string
		secret  string
		want    string
	}{
		"no header":                          {nil, "", unauth},
		"not a tk1 secret":                   {map[string]string{"Authorization": "Bearer abc"}, "", unauth},
		"the right length, the wrong prefix": {map[string]string{"Authorization": "Bearer xx1_" + secret[4:]}, "", unauth},
		"one character short":                {map[string]string{"Authorization": "Bearer " + secret[:len(secret)-1]}, "", unauth},
		"one character too long":             {map[string]string{"Authorization": "Bearer " + secret + "A"}, "", unauth},
		"a character outside the alphabet":   {map[string]string{"Authorization": "Bearer " + secret[:10] + "+" + secret[11:]}, "", unauth},
		"empty value":                        {map[string]string{"Authorization": "Bearer "}, "", unauth},
		"the scheme alone":                   {map[string]string{"Authorization": "Bearer"}, "", unauth},
		"two spaces":                         {map[string]string{"Authorization": "Bearer  " + secret}, "", unauth},
		"unknown but well formed":            {map[string]string{"Authorization": "Bearer " + NewTokenSecret()}, "", unauth},
	} {
		w := e.call(pGetUser, tc.secret, callOpts{headers: tc.headers})
		if w.Code != http.StatusUnauthorized || strings.TrimSpace(w.Body.String()) != tc.want {
			t.Errorf("%s: %d %q", name, w.Code, w.Body.String())
		}
	}
	if e.seen.calls != 0 {
		t.Fatalf("a refused request reached the handler %d times", e.seen.calls)
	}

	// two Authorization headers are refused, even if one is good
	r := httptest.NewRequest(http.MethodPost, pGetUser, strings.NewReader("{}"))
	r.Header.Add("Authorization", "Bearer "+secret)
	r.Header.Add("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("two headers: %d", w.Code)
	}

	// the scheme is case insensitive
	if got := e.code(pGetUser, "", callOpts{headers: map[string]string{"Authorization": "bearer " + secret}}); got != 200 {
		t.Errorf("lower-case scheme: %d", got)
	}

	// a bad token is 401 even with a valid cookie: no fallback to the session
	w = e.call(pGetUser, NewTokenSecret(), callOpts{headers: map[string]string{"Cookie": CookieName + "=" + e.cookie}})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad token with a good cookie: %d", w.Code)
	}
	// and a good token ignores the cookie: the principal is the token, never the owner
	e.call(pGetUser, secret, callOpts{headers: map[string]string{"Cookie": CookieName + "=" + e.cookie}})
	if !e.seen.principal.Token || e.seen.admin.ID != "token:"+tok.ID {
		t.Errorf("token and cookie together: %+v %+v", e.seen.principal, e.seen.admin)
	}

	// another scheme (a proxy's basic auth) leaves the cookie path alone
	if got := e.code(pGetUser, "", callOpts{headers: map[string]string{"Authorization": "Basic dTpw", "Cookie": CookieName + "=" + e.cookie}}); got != 200 {
		t.Errorf("basic auth plus cookie: %d", got)
	}
	if e.seen.principal.Token {
		t.Error("a cookie request became a token principal")
	}
	if got := e.code(pGetUser, "", callOpts{headers: map[string]string{"Authorization": "Basic dTpw"}}); got != 401 {
		t.Errorf("basic auth alone: %d", got)
	}
	// nothing of a refused request is audited under a token, and no secret is in the log of rows
	for _, row := range e.audits("call") {
		if strings.Contains(row.Params, secret) {
			t.Fatal("a secret reached the audit log")
		}
	}
}

func TestBearerRevokedAndExpired(t *testing.T) {
	e := newTokenEnv(t)
	revoked, rs := e.mkToken("gone", store.ProfileAdmin, 120)
	short, ss := e.mkToken("short", store.ProfileAdmin, 120)
	_, live := e.mkToken("live", store.ProfileAdmin, 120)

	if got := e.code(pGetUser, rs); got != 200 {
		t.Fatalf("live token: %d", got)
	}
	if _, err := e.st.RevokeAPIToken(context.Background(), revoked.ID, e.owner.ID, e.s.now()); err != nil {
		t.Fatal(err)
	}
	w := e.call(pGetUser, rs)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "token revoked") {
		t.Errorf("revoked: %d %q", w.Code, w.Body.String())
	}
	// the very next request after the revocation is refused, whatever the procedure
	for _, p := range []string{pGetUser, pCreateUser, pListAudit} {
		if got := e.code(p, rs); got != 401 {
			t.Errorf("revoked token on %s: %d", p, got)
		}
	}

	// expiry: up to, not including, expires_at
	*e.clock = short.ExpiresAt.Add(-time.Second)
	if got := e.code(pGetUser, ss); got != 200 {
		t.Fatalf("one second before expiry: %d", got)
	}
	*e.clock = short.ExpiresAt
	w = e.call(pGetUser, ss)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "token expired") {
		t.Errorf("expired: %d %q", w.Code, w.Body.String())
	}
	if got := e.code(pGetUser, live); got != 401 { // the same 90 days
		t.Errorf("another token of the same age: %d", got)
	}
}

func TestBearerRateLimit(t *testing.T) {
	e := newTokenEnv(t)
	_, slow := e.mkToken("slow", store.ProfileReadonly, 2) // burst 2, one more every 30 s
	_, other := e.mkToken("other", store.ProfileReadonly, 120)
	_, fast := e.mkToken("fast", store.ProfileReadonly, 600) // burst 30

	for i := range 2 {
		if got := e.code(pGetUser, slow); got != 200 {
			t.Fatalf("call %d: %d", i, got)
		}
	}
	w := e.call(pGetUser, slow)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" ||
		!strings.Contains(w.Body.String(), "resource_exhausted") {
		t.Fatalf("third call: %d retry-after %q %q", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	if got := e.code(pGetUser, other); got != 200 {
		t.Errorf("another token has its own bucket: %d", got)
	}
	*e.clock = e.clock.Add(31 * time.Second)
	if got := e.code(pGetUser, slow); got != 200 {
		t.Errorf("after the refill: %d", got)
	}
	// the bucket never holds more than min(30, rate): a quiet token is not owed a flood
	for i := range 30 {
		if got := e.code(pGetUser, fast); got != 200 {
			t.Fatalf("fast call %d: %d", i, got)
		}
	}
	if got := e.code(pGetUser, fast); got != http.StatusTooManyRequests {
		t.Errorf("31st call at once: %d", got)
	}
	// the MCP layer's own in-process calls were charged once, at /mcp: they skip the bucket
	mcpCtx := WithChannel(context.Background(), ChannelMCP)
	for i := range 40 {
		if got := e.code(pGetUser, fast, callOpts{ctx: mcpCtx}); got != 200 {
			t.Fatalf("in-process call %d: %d", i, got)
		}
	}
	// a 429 is on the record, once a minute per token: slow once, fast once
	limited := 0
	for _, r := range e.audits("call") {
		if r.Result == "limited" {
			limited++
		}
	}
	if limited != 2 {
		t.Errorf("limited rows: %d, want 2", limited)
	}
}

func TestBearerAllowListAndProfiles(t *testing.T) {
	e := newTokenEnv(t)
	_, ro := e.mkToken("ro", store.ProfileReadonly, 600)
	_, op := e.mkToken("op", store.ProfileOperator, 600)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)

	type row struct {
		path       string
		ro, op, ad int
	}
	for _, tc := range []row{
		{pGetUser, 200, 200, 200},                                     // a read on the list
		{adminv1connect.FleetServiceOverviewProcedure, 200, 200, 200}, // a read on the list
		{pCreateUser, 403, 200, 200},                                  // a write: operator and up
		{adminv1connect.HealthServiceMuteAlertProcedure, 403, 200, 200},
		{pListAudit, 403, 403, 200}, // owner level on the list: the admin profile only
		// closed to every profile, whatever the level
		{pDeleteUsers, 403, 403, 403},
		{adminv1connect.UserServiceGetSubscriptionLinkProcedure, 403, 403, 403},
		{adminv1connect.DeviceServiceGetDeviceConfigsProcedure, 403, 403, 403},
		{adminv1connect.NodeServiceCreateEnrollmentProcedure, 403, 403, 403},
		{adminv1connect.NodeServiceStreamLogsProcedure, 403, 403, 403},
		{adminv1connect.WarpServiceGetWarpProcedure, 403, 403, 403},
		{adminv1connect.InstanceServiceUpdateInstanceProcedure, 403, 403, 403},
		{adminv1connect.AuthServiceListSessionsProcedure, 403, 403, 403},
		{adminv1connect.AuthServiceMeProcedure, 403, 403, 403},
		{adminv1connect.ApiTokenServiceCreateApiTokenProcedure, 403, 403, 403},
		{adminv1connect.ApiTokenServiceListApiTokensProcedure, 403, 403, 403},
		{adminv1connect.ApprovalServiceApproveProcedure, 403, 403, 403},
		{adminv1connect.ApprovalServiceListApprovalsProcedure, 403, 403, 403},
		{adminv1connect.UpdateServiceRescanBundleProcedure, 403, 403, 403},
		{"/mistgate.admin.v1.FutureService/Call", 403, 403, 403}, // not even a procedure that exists
		// need the owner's approval: never over /api
		{pApplyFix, 403, 403, 403},
		{pStartRollout, 403, 403, 403},
		{adminv1connect.UpdateServiceRollbackNodeProcedure, 403, 403, 403},
	} {
		for who, pair := range map[string]struct {
			secret string
			want   int
		}{"readonly": {ro, tc.ro}, "operator": {op, tc.op}, "admin": {ad, tc.ad}} {
			if got := e.code(tc.path, pair.secret); got != pair.want {
				t.Errorf("%s as %s: %d, want %d", tc.path, who, got, pair.want)
			}
		}
	}
	// a refusal says why, in words that carry nothing of the data
	w := e.call(pDeleteUsers, ad)
	if !strings.Contains(w.Body.String(), `"permission_denied"`) || !strings.Contains(w.Body.String(), "not available to API tokens") {
		t.Errorf("refusal body: %q", w.Body.String())
	}
	w = e.call(pCreateUser, ro)
	if !strings.Contains(w.Body.String(), "profile") {
		t.Errorf("refusal body: %q", w.Body.String())
	}
	w = e.call(pStartRollout, ad)
	if !strings.Contains(w.Body.String(), "owner's approval") {
		t.Errorf("refusal body: %q", w.Body.String())
	}
	// the cookie path of the same owner is unchanged: everything an owner could do still works
	for _, p := range []string{pGetUser, pCreateUser, pListAudit, pDeleteUsers, pApplyFix} {
		r := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		r.AddCookie(&http.Cookie{Name: CookieName, Value: e.cookie})
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("owner cookie on %s: %d", p, w.Code)
		}
	}
}

func TestBearerPrincipalAndSyntheticAdmin(t *testing.T) {
	e := newTokenEnv(t)
	tok, secret := e.mkToken("Claude Ops", store.ProfileOperator, 120)

	e.call(pCreateUser, secret, callOpts{remote: "198.51.100.4:5555"})
	if e.seen.calls != 1 {
		t.Fatal("the call did not reach the handler")
	}
	a, p := e.seen.admin, e.seen.principal
	if a.ID != "token:"+tok.ID || a.DisplayName != "Claude Ops" || a.Role != store.RoleHelper {
		t.Errorf("synthetic admin: %+v", a)
	}
	if !p.Token || p.TokenID != tok.ID || p.Profile != store.ProfileOperator || p.Channel != ChannelAPI || p.Name != "Claude Ops" ||
		!p.ExpiresAt.Equal(tok.ExpiresAt.Truncate(time.Second)) && !p.ExpiresAt.Equal(tok.ExpiresAt) || p.ActorID() != "token:"+tok.ID {
		t.Errorf("principal: %+v", p)
	}
	// profile to role
	for profile, role := range map[string]string{store.ProfileReadonly: store.RoleReadonly, store.ProfileOperator: store.RoleHelper, store.ProfileAdmin: store.RoleOwner} {
		if got := profileRole(profile); got != role {
			t.Errorf("profile %s acts as %s, want %s", profile, got, role)
		}
	}

	// over the MCP channel the actor is mcp:<id>
	e.call(pGetUser, secret, callOpts{ctx: WithChannel(context.Background(), ChannelMCP)})
	if e.seen.admin.ID != "mcp:"+tok.ID || e.seen.principal.Channel != ChannelMCP {
		t.Errorf("mcp actor: %+v %+v", e.seen.admin, e.seen.principal)
	}
	// a network request cannot choose the channel: no header, no cookie, no query does it
	e.call(pGetUser+"?channel=mcp", secret, callOpts{headers: map[string]string{"X-Channel": "mcp", "Mcp-Channel": "mcp"}})
	if e.seen.principal.Channel != ChannelAPI {
		t.Errorf("a request chose its channel: %+v", e.seen.principal)
	}
	// the cookie path has no token principal
	if (PrincipalFrom(context.Background()) != Principal{}) {
		t.Error("a bare context has a principal")
	}
}

func TestBearerAuditTrail(t *testing.T) {
	e := newTokenEnv(t)
	tok, secret := e.mkToken("ops", store.ProfileOperator, 600)

	// a write is audited every time, with its status, the channel's source and the client address
	e.call(pCreateUser, secret, callOpts{remote: "198.51.100.4:5555"})
	e.call(pCreateUser, secret, callOpts{remote: "198.51.100.4:5555"})
	// a refusal too, each time
	e.call(pDeleteUsers, secret)
	e.call(pDeleteUsers, secret)
	// successful reads: once a minute per procedure
	for range 5 {
		e.call(pGetUser, secret)
	}
	rows := e.audits("call")
	count := func(proc, result string) int {
		n := 0
		for _, r := range rows {
			if strings.Contains(r.Params, `"procedure":"`+proc+`"`) && r.Result == result {
				n++
			}
		}
		return n
	}
	if count(pCreateUser, "ok") != 2 || count(pDeleteUsers, "denied") != 2 || count(pGetUser, "ok") != 1 {
		t.Fatalf("rows after the first minute: create %d, denied %d, reads %d (rows %+v)", count(pCreateUser, "ok"), count(pDeleteUsers, "denied"), count(pGetUser, "ok"), rows)
	}
	for _, r := range rows {
		if r.Actor != "token:"+tok.ID || r.ActorName != "ops" || r.Source != store.AuditAPI {
			t.Errorf("call row: %+v", r)
		}
		if strings.Contains(r.Params, "tk1_") {
			t.Errorf("a secret in the audit row: %s", r.Params)
		}
	}
	for _, r := range rows {
		if strings.Contains(r.Params, pCreateUser) && !strings.Contains(r.Params, `"status":200`) {
			t.Errorf("status missing: %s", r.Params)
		}
		if strings.Contains(r.Params, pCreateUser) && r.IP != "198.51.100.4" {
			t.Errorf("client address: %q", r.IP)
		}
		if strings.Contains(r.Params, pDeleteUsers) && !strings.Contains(r.Params, `"status":403`) {
			t.Errorf("refusal status: %s", r.Params)
		}
	}
	// a minute later the same read is audited again
	*e.clock = e.clock.Add(61 * time.Second)
	e.call(pGetUser, secret)
	if rows = e.audits("call"); count(pGetUser, "ok") != 2 {
		t.Errorf("a read a minute later: %d rows", count(pGetUser, "ok"))
	}
	// an error answer of a read is not "a successful read": it is always written
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	h := e.s.RequireSession(failing)
	for range 3 {
		r := httptest.NewRequest(http.MethodPost, pGetUser, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+secret)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	n := 0
	for _, r := range e.audits("call") {
		if r.Result == "error" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("error answers written: %d, want 3", n)
	}

	// the modules' own rows follow the call: actor from AdminFrom, source from the context
	var module []store.AuditRow
	for _, r := range e.audits("module_row") {
		module = append(module, r)
	}
	if len(module) == 0 {
		t.Fatal("the stand-in module wrote no row")
	}
	for _, r := range module {
		if r.Actor != "token:"+tok.ID || r.Source != store.AuditAPI {
			t.Errorf("module row: %+v", r)
		}
	}
	e.call(pGetUser, secret, callOpts{ctx: WithChannel(context.Background(), ChannelMCP)})
	last := e.audits("module_row")[0]
	if last.Actor != "mcp:"+tok.ID || last.Source != store.AuditMCP {
		t.Errorf("module row over mcp: %+v", last)
	}
	// and the cookie path's rows stay "panel"
	r := httptest.NewRequest(http.MethodPost, pGetUser, strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: CookieName, Value: e.cookie})
	e.h.ServeHTTP(httptest.NewRecorder(), r)
	if last := e.audits("module_row")[0]; last.Actor != e.owner.ID || last.Source != store.AuditPanel {
		t.Errorf("module row of a cookie session: %+v", last)
	}
}

func TestBearerLastUsedIsWrittenOncePerMinute(t *testing.T) {
	e := newTokenEnv(t)
	tok, secret := e.mkToken("ops", store.ProfileReadonly, 600)
	got := func() store.APIToken {
		x, err := e.st.GetAPIToken(context.Background(), tok.ID)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if !got().LastUsedAt.IsZero() {
		t.Fatal("used before use")
	}
	start := *e.clock
	e.call(pGetUser, secret, callOpts{remote: "198.51.100.4:1"})
	g := got()
	if !g.LastUsedAt.Equal(start.Truncate(time.Second)) || g.LastUsedIP != "198.51.100.4" || g.LastUsedVia != "api" {
		t.Fatalf("first use: %+v", g)
	}
	*e.clock = start.Add(30 * time.Second)
	e.call(pGetUser, secret, callOpts{remote: "198.51.100.9:1"})
	if g2 := got(); !g2.LastUsedAt.Equal(g.LastUsedAt) || g2.LastUsedIP != "198.51.100.4" {
		t.Errorf("a second use within the minute rewrote the row: %+v", g2)
	}
	*e.clock = start.Add(61 * time.Second)
	e.call(pGetUser, secret, callOpts{remote: "198.51.100.9:1", ctx: WithChannel(context.Background(), ChannelMCP)})
	if g3 := got(); g3.LastUsedIP != "198.51.100.9" || g3.LastUsedVia != "mcp" || !g3.LastUsedAt.Equal(start.Add(61*time.Second).Truncate(time.Second)) {
		t.Errorf("a use a minute later: %+v", g3)
	}
}

// --- step-up and the grants ---

func TestTokenNeverPassesStepUp(t *testing.T) {
	e := newTokenEnv(t)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)

	e.call(pGetUser, ad) // any allowed procedure: the stand-in handler asks for a step-up
	err := e.seen.stepUpErr
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "step-up is not available to API tokens") {
		t.Fatalf("RequireStepUp for a token: %v", err)
	}
	// the cookie path is as it was: a fresh session passes, one past the window does not
	r := httptest.NewRequest(http.MethodPost, pGetUser, strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: CookieName, Value: e.cookie})
	e.h.ServeHTTP(httptest.NewRecorder(), r)
	if e.seen.stepUpErr != nil {
		t.Errorf("a fresh session needs no step-up: %v", e.seen.stepUpErr)
	}
	*e.clock = e.clock.Add(StepUpWindow + time.Second)
	r = httptest.NewRequest(http.MethodPost, pGetUser, strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: CookieName, Value: e.cookie})
	e.h.ServeHTTP(httptest.NewRecorder(), r)
	if connect.CodeOf(e.seen.stepUpErr) != connect.CodePermissionDenied || !strings.Contains(e.seen.stepUpErr.Error(), "step-up required") {
		t.Errorf("a session past the window: %v", e.seen.stepUpErr)
	}
	// a context that names a token but holds no request never passes either
	ctx := context.WithValue(context.Background(), principalKey{}, Principal{Token: true, TokenID: "tok_x", Channel: ChannelMCP})
	if err := e.s.RequireStepUp(ctx); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("principal without a grant: %v", err)
	}
}

// planFor creates a plan of the token and drives it into the state wanted.
func (e *tokenEnv) planFor(tok store.APIToken, tool string, dangerous bool, state string) store.MCPPlan {
	e.t.Helper()
	ctx := context.Background()
	now := e.s.now()
	p := store.MCPPlan{TokenID: tok.ID, Tool: tool, ParamsJSON: `{"node":"nod_x"}`, ConfirmHash: hashToken(store.NewID("cf_")), FactsJSON: "[]",
		Summary: "s", Danger: `["fleet"]`, NeedsApproval: dangerous, Status: store.PlanPlanned, CreatedAt: now}
	if dangerous {
		p.Status = store.PlanAwaiting
	}
	if err := e.st.CreateMCPPlan(ctx, p, 20, 50); err != nil {
		e.t.Fatal(err)
	}
	got, err := e.st.MCPPlanByConfirm(ctx, tok.ID, p.ConfirmHash)
	if err != nil {
		e.t.Fatal(err)
	}
	h := sha256.Sum256([]byte(got.ParamsJSON))
	switch state {
	case "awaiting", "planned":
	case "applying", "applied":
		if dangerous {
			if _, err := e.st.DecideMCPPlan(ctx, got.ID, e.owner.ID, true, now); err != nil {
				e.t.Fatal(err)
			}
		}
		if _, err := e.st.BeginApply(ctx, got.ID, tok.ID, tool, h[:], got.ConfirmHash, now); err != nil {
			e.t.Fatal(err)
		}
		if state == "applied" {
			if err := e.st.FinishApply(ctx, got.ID, true, "ok", "", "", "", now); err != nil {
				e.t.Fatal(err)
			}
		}
	default:
		e.t.Fatalf("state %q", state)
	}
	return got
}

func TestApprovedGrant(t *testing.T) {
	e := newTokenEnv(t)
	tok, secret := e.mkToken("ad", store.ProfileAdmin, 600)
	op, opSecret := e.mkToken("op", store.ProfileOperator, 600)
	other, otherSecret := e.mkToken("other", store.ProfileAdmin, 600)

	mcp := func(planID string) context.Context {
		return e.s.WithApprovedStepUp(WithChannel(context.Background(), ChannelMCP), planID)
	}
	try := func(path, secret string, ctx context.Context) int {
		e.seen.calls = 0
		w := e.call(path, secret, callOpts{ctx: ctx})
		if w.Code == 200 && e.seen.calls != 1 {
			t.Fatalf("%s: 200 without reaching the handler", path)
		}
		return w.Code
	}

	// an approved plan that is applying opens exactly its own procedure, and step-up inside it
	p := e.planFor(tok, "rollout_start", true, "applying")
	if got := try(pStartRollout, secret, mcp(p.ID)); got != 200 {
		t.Fatalf("the approved plan's own procedure: %d", got)
	}
	if e.seen.stepUpErr != nil {
		t.Errorf("RequireStepUp inside the approved apply: %v", e.seen.stepUpErr)
	}
	if e.seen.admin.ID != "mcp:"+tok.ID {
		t.Errorf("actor of the approved apply: %s", e.seen.admin.ID)
	}
	// ... and nothing else: not another procedure that needs approval, not a closed one
	for _, path := range []string{pApplyFix, adminv1connect.UpdateServiceRollbackNodeProcedure, adminv1connect.UpdateServiceCancelRolloutProcedure, pDeleteUsers, adminv1connect.WarpServiceDeleteWarpProcedure} {
		if got := try(path, secret, mcp(p.ID)); got != 403 {
			t.Errorf("a rollout_start grant on %s: %d", path, got)
		}
	}
	// the grant is useless without the MCP channel: a network request cannot carry one anyway, but the channel is checked too
	if got := try(pStartRollout, secret, e.s.WithApprovedStepUp(context.Background(), p.ID)); got != 403 {
		t.Errorf("grant without the mcp channel: %d", got)
	}
	// another token's plan is not mine
	if got := try(pStartRollout, otherSecret, mcp(p.ID)); got != 403 {
		t.Errorf("a plan of another token: %d", got)
	}
	_ = other
	// the operator profile cannot use a grant either: the level still applies
	po := e.planFor(op, "rollout_start", true, "applying")
	if got := try(pStartRollout, opSecret, mcp(po.ID)); got != 403 {
		t.Errorf("operator with an approved plan: %d", got)
	}

	// a plan that is only awaiting, or approved but not applying, opens nothing
	aw := e.planFor(tok, "rollout_start", true, "awaiting")
	if got := try(pStartRollout, secret, mcp(aw.ID)); got != 403 {
		t.Errorf("an awaiting plan: %d", got)
	}
	if _, err := e.st.DecideMCPPlan(context.Background(), aw.ID, e.owner.ID, true, e.s.now()); err != nil {
		t.Fatal(err)
	}
	if got := try(pStartRollout, secret, mcp(aw.ID)); got != 403 {
		t.Errorf("an approved plan that was not begun: %d", got)
	}
	// a safe plan (no human decision) never opens the step-up procedures
	safe := e.planFor(tok, "rollout_start", false, "applying")
	if got := try(pStartRollout, secret, mcp(safe.ID)); got != 403 {
		t.Errorf("a plan nobody approved: %d", got)
	}
	// a tool that is not made for the procedure gets no grant
	odd := e.planFor(tok, "user_update", true, "applying")
	if got := try(pStartRollout, secret, mcp(odd.ID)); got != 403 {
		t.Errorf("an unknown tool's plan: %d", got)
	}
	// unknown plan ids and empty ones
	for _, id := range []string{"pln_nope", ""} {
		if got := try(pStartRollout, secret, mcp(id)); got != 403 {
			t.Errorf("plan %q: %d", id, got)
		}
	}
	// the grant of another Service instance is not honoured
	s2, _, _ := newTestService(t)
	if got := try(pStartRollout, secret, s2.WithApprovedStepUp(WithChannel(context.Background(), ChannelMCP), p.ID)); got != 403 {
		t.Errorf("a grant minted by another service: %d", got)
	}

	// it dies the moment the apply finishes
	if err := e.st.FinishApply(context.Background(), p.ID, true, "done", "", "", "", e.s.now()); err != nil {
		t.Fatal(err)
	}
	if got := try(pStartRollout, secret, mcp(p.ID)); got != 403 {
		t.Errorf("after the apply finished: %d", got)
	}
	// a node fix: its own procedure under its own plan
	fix := e.planFor(tok, "node_fix", true, "applying")
	if got := try(pApplyFix, secret, mcp(fix.ID)); got != 200 {
		t.Errorf("approved node_fix: %d", got)
	}
	if got := try(pStartRollout, secret, mcp(fix.ID)); got != 403 {
		t.Errorf("node_fix grant on a rollout: %d", got)
	}
	// a token revoked mid-apply is refused at the door, grant or not
	if _, err := e.st.RevokeAPIToken(context.Background(), tok.ID, e.owner.ID, e.s.now()); err != nil {
		t.Fatal(err)
	}
	if got := try(pApplyFix, secret, mcp(fix.ID)); got != 401 {
		t.Errorf("revoked token with a running approved plan: %d", got)
	}
}

func TestPlanningGrantOpensOnlyTheDryRun(t *testing.T) {
	e := newTokenEnv(t)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)
	_, op := e.mkToken("op", store.ProfileOperator, 600)
	planning := WithPlanning(WithChannel(context.Background(), ChannelMCP))

	for _, ct := range []string{"application/proto", "application/json", "application/json; charset=utf-8"} {
		e.seen.calls = 0
		w := e.call(pApplyFix, ad, callOpts{ctx: planning, body: applyFixBody(t, true, strings.Split(ct, ";")[0]), contentType: ct})
		if w.Code != 200 || e.seen.calls != 1 {
			t.Errorf("dry run as %s: %d", ct, w.Code)
		}
		// the handler got the body untouched
		var m adminv1.ApplyFixRequest
		if strings.HasPrefix(ct, "application/proto") {
			if err := proto.Unmarshal(e.seen.body, &m); err != nil || !m.DryRun || m.NodeId != "nod_x" {
				t.Errorf("handler body (%s): %v %+v", ct, err, &m)
			}
		}
		// the real thing is refused: only a dry run is planning
		e.seen.calls = 0
		if got := e.code(pApplyFix, ad, callOpts{ctx: planning, body: applyFixBody(t, false, strings.Split(ct, ";")[0]), contentType: ct}); got != 403 || e.seen.calls != 0 {
			t.Errorf("dry_run=false under planning as %s: %d", ct, got)
		}
	}
	// no dry_run field at all is false
	if got := e.code(pApplyFix, ad, callOpts{ctx: planning, body: []byte(`{"nodeId":"nod_x"}`)}); got != 403 {
		t.Errorf("empty request under planning: %d", got)
	}
	// garbage, an unknown content type and a compressed body are not dry runs
	for name, o := range map[string]callOpts{
		"garbage":       {ctx: planning, body: []byte("not json")},
		"text":          {ctx: planning, body: applyFixBody(t, true, "application/json"), contentType: "text/plain"},
		"gzip":          {ctx: planning, body: applyFixBody(t, true, "application/json"), headers: map[string]string{"Content-Encoding": "gzip"}},
		"connect+proto": {ctx: planning, body: applyFixBody(t, true, "application/proto"), contentType: "application/connect+proto"},
	} {
		if got := e.code(pApplyFix, ad, o); got != 403 {
			t.Errorf("%s under planning: %d", name, got)
		}
	}
	// planning opens nothing else
	for _, p := range []string{pStartRollout, adminv1connect.UpdateServiceRollbackNodeProcedure, adminv1connect.UpdateServicePauseRolloutProcedure, pDeleteUsers} {
		if got := e.code(p, ad, callOpts{ctx: planning, body: applyFixBody(t, true, "application/json")}); got != 403 {
			t.Errorf("planning on %s: %d", p, got)
		}
	}
	// without the MCP channel (a script) planning is nothing
	if got := e.code(pApplyFix, ad, callOpts{ctx: WithPlanning(context.Background()), body: applyFixBody(t, true, "application/json")}); got != 403 {
		t.Errorf("planning without the channel: %d", got)
	}
	// the level still applies: an operator token cannot ask for a fix plan
	if got := e.code(pApplyFix, op, callOpts{ctx: planning, body: applyFixBody(t, true, "application/json")}); got != 403 {
		t.Errorf("operator under planning: %d", got)
	}
	// planning does not pass step-up either
	e.seen.stepUpErr = nil
	e.code(pApplyFix, ad, callOpts{ctx: planning, body: applyFixBody(t, true, "application/json")})
	if connect.CodeOf(e.seen.stepUpErr) != connect.CodePermissionDenied {
		t.Errorf("step-up under planning: %v", e.seen.stepUpErr)
	}
}

// --- the allow-list itself ---

func TestTokenAllowList(t *testing.T) {
	for path, access := range tokenProcedures {
		if _, ok := procedureLevels[path]; !ok {
			t.Errorf("the allow-list names %s, which has no level (not a procedure?)", path)
		}
		if access != TokenAccessDirect && access != TokenAccessApproved {
			t.Errorf("%s: access %d", path, access)
		}
		if got, ok := TokenAccess(path); !ok || got != access {
			t.Errorf("TokenAccess(%s) = %d %v", path, got, ok)
		}
		// a token cannot pass step-up, so a procedure that needs it may never be open to a bare token
		if NeedsStepUp(path) && access == TokenAccessDirect {
			t.Errorf("%s needs a step-up and is open to tokens directly", path)
		}
	}
	for path := range stepUpProcedures {
		if _, ok := procedureLevels[path]; !ok {
			t.Errorf("stepUpProcedures names %s, which has no level", path)
		}
	}
	// the exact set that goes through the owner's approval
	approved := map[string]bool{}
	for path, access := range tokenProcedures {
		if access == TokenAccessApproved {
			approved[path] = true
		}
	}
	if len(approved) != 8 {
		t.Errorf("%d procedures need approval, want 8: %v", len(approved), approved)
	}
	// every grant is for a procedure that is on the list as approved
	for tool, path := range grantProcedures {
		if !approved[path] {
			t.Errorf("tool %s may open %s, which is not an approved procedure", tool, path)
		}
	}
	for path := range approved {
		found := false
		for _, p := range grantProcedures {
			found = found || p == path
		}
		if !found {
			t.Errorf("%s needs approval but no tool can be granted it", path)
		}
	}
	// nothing that hands out a secret, a key or a link is on the list; nor are the owner's own services
	for _, path := range []string{
		adminv1connect.UserServiceGetSubscriptionLinkProcedure,
		adminv1connect.UserServiceDeleteUsersProcedure,
		adminv1connect.DeviceServiceCreateAwgDeviceProcedure,
		adminv1connect.DeviceServiceGetDeviceConfigsProcedure,
		adminv1connect.DeviceServiceRotateDeviceKeysProcedure,
		adminv1connect.DeviceServiceRenameDeviceProcedure,
		adminv1connect.NodeServiceCreateEnrollmentProcedure,
		adminv1connect.NodeServiceRetireNodeProcedure,
		adminv1connect.NodeServiceStreamLogsProcedure,
		adminv1connect.NodeServiceUpdateNodeProcedure,
		adminv1connect.NodeServiceRestartInboundsProcedure,
		adminv1connect.NodeServicePrepareAwgKernelProcedure, // installs packages on the node: never through a token or MCP
		adminv1connect.ProfileServiceListProtocolsProcedure,
		adminv1connect.ProfileServicePreviewProfileProcedure,
		adminv1connect.ProfileServiceCreateProfileProcedure,
		adminv1connect.ProfileServiceUpdateProfileProcedure,
		adminv1connect.ProfileServiceDeleteProfileProcedure,
		adminv1connect.ProfileServiceCreateInboundProcedure,
		adminv1connect.ProfileServiceUpdateInboundProcedure,
		adminv1connect.ProfileServiceDeleteInboundProcedure,
		adminv1connect.ProfileServiceTwinProfileProcedure,
		adminv1connect.GroupServiceCreateGroupProcedure,
		adminv1connect.GroupServiceUpdateGroupProcedure,
		adminv1connect.GroupServiceDeleteGroupProcedure,
		adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure,
		adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure,
		adminv1connect.UpdateServiceRescanBundleProcedure,
		adminv1connect.InstanceServiceGetInstanceProcedure,
		adminv1connect.InstanceServiceUpdateInstanceProcedure,
		adminv1connect.AuthServiceGetSecuritySettingsProcedure,
		adminv1connect.AuthServiceUpdateSecuritySettingsProcedure,
		adminv1connect.AuthServiceMeProcedure,
		adminv1connect.AuthServiceListPasskeysProcedure,
		adminv1connect.AuthServiceListSessionsProcedure,
		adminv1connect.AuthServiceBeginStepUpProcedure,
		adminv1connect.AuthServiceFinishStepUpProcedure,
		adminv1connect.AuthServiceGetPasswordLoginProcedure,
		adminv1connect.AuthServiceChangePasswordProcedure,
		adminv1connect.AuthServiceBeginTotpEnrollmentProcedure,
		adminv1connect.AuthServiceFinishTotpEnrollmentProcedure,
		adminv1connect.ApiTokenServiceCreateApiTokenProcedure,
		adminv1connect.ApiTokenServiceListApiTokensProcedure,
		adminv1connect.ApiTokenServiceRevokeApiTokenProcedure,
		adminv1connect.ApprovalServiceListApprovalsProcedure,
		adminv1connect.ApprovalServiceApproveProcedure,
		adminv1connect.ApprovalServiceRejectProcedure,
	} {
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s is on the token allow-list", path)
		}
	}
	for path := range tokenProcedures {
		for _, svc := range []string{"WarpService", "DeviceService", "DnsService", "InstanceService", "ApiTokenService", "ApprovalService", "AwgService"} {
			if strings.Contains(path, "."+svc+"/") {
				t.Errorf("%s: %s is closed to tokens as a whole", path, svc)
			}
		}
		if strings.Contains(path, "AuthService") && path != adminv1connect.AuthServiceListAuditProcedure {
			t.Errorf("%s: of AuthService only ListAudit may be open to tokens", path)
		}
	}
	// the helpers agree with the policy table
	if ProcedureRole(pGetUser) != store.RoleReadonly || ProcedureRole(pCreateUser) != store.RoleHelper ||
		ProcedureRole(pListAudit) != store.RoleOwner || ProcedureRole("/nope.Service/X") != store.RoleOwner {
		t.Error("ProcedureRole")
	}
	if !NeedsStepUp(pStartRollout) || NeedsStepUp(pApplyFix) || NeedsStepUp(pGetUser) {
		t.Error("NeedsStepUp")
	}
	// Step-up procedures include the protected handlers, password changes, authenticator enrollment, login captcha,
	// and the three backup operations that reveal or write sensitive infrastructure data.
	if len(stepUpProcedures) != 27 {
		t.Errorf("%d step-up procedures, want 27", len(stepUpProcedures))
	}
}

// A helper cannot do what only the owner may through the token services, with a cookie either (HTTP, loopback).
func TestTokenServicesOwnerOnlyOverHTTP(t *testing.T) {
	s, st, _ := newTestService(t)
	owner := makeAdmin(t, s, st)
	helper, readonly := insertAdmin(t, st, store.RoleHelper), insertAdmin(t, st, store.RoleReadonly)
	cookie := func(a store.Admin) string {
		c, err := s.newSession(context.Background(), a.ID, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return c.Value
	}
	mux := http.NewServeMux()
	p1, h1 := s.TokenHandler()
	p2, h2 := s.ApprovalHandler()
	mux.Handle(p1, h1)
	mux.Handle(p2, h2)
	srv := httptest.NewServer(s.RequireSession(mux)) // httptest listens on 127.0.0.1
	defer srv.Close()
	do := func(path, cookieValue, bearer string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if cookieValue != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: cookieValue})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	e := &tokenEnv{t: t, s: s, st: st, owner: owner}
	_, adminToken := e.mkToken("ad", store.ProfileAdmin, 600)
	for _, p := range []string{
		adminv1connect.ApiTokenServiceListApiTokensProcedure, adminv1connect.ApiTokenServiceCreateApiTokenProcedure,
		adminv1connect.ApiTokenServiceRevokeApiTokenProcedure, adminv1connect.ApprovalServiceListApprovalsProcedure,
		adminv1connect.ApprovalServiceApproveProcedure, adminv1connect.ApprovalServiceRejectProcedure,
	} {
		if got := do(p, cookie(helper), ""); got != 403 {
			t.Errorf("helper on %s: %d", p, got)
		}
		if got := do(p, cookie(readonly), ""); got != 403 {
			t.Errorf("readonly on %s: %d", p, got)
		}
		if got := do(p, "", adminToken); got != 403 {
			t.Errorf("an admin-profile token on %s: %d", p, got)
		}
		if got := do(p, "", ""); got != 401 {
			t.Errorf("nobody on %s: %d", p, got)
		}
	}
	if got := do(adminv1connect.ApiTokenServiceListApiTokensProcedure, cookie(owner), ""); got != 200 {
		t.Errorf("owner lists tokens: %d", got)
	}
}
