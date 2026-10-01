package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// rpcEnv is a service with an owner session (fresh step-up) and the two RPC services.
type rpcEnv struct {
	*tokenEnv
	tok *tokenRPC
	apr *approvalRPC
	ctx context.Context // the owner's request context, with a fresh step-up
}

func newRPCEnv(t *testing.T) *rpcEnv {
	e := newTokenEnv(t)
	return &rpcEnv{tokenEnv: e, tok: &tokenRPC{e.s}, apr: &approvalRPC{e.s}, ctx: authed(t, e.s, e.cookie)}
}

// stale is the owner's context once the step-up window has passed.
func (e *rpcEnv) stale() context.Context {
	*e.clock = e.clock.Add(StepUpWindow + time.Minute)
	return authed(e.t, e.s, e.cookie)
}

func create(e *rpcEnv, ctx context.Context, m *adminv1.CreateApiTokenRequest) (*adminv1.CreateApiTokenResponse, error) {
	r, err := e.tok.CreateApiToken(ctx, connect.NewRequest(m))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func TestCreateApiToken(t *testing.T) {
	e := newRPCEnv(t)
	ctx := e.ctx
	now := e.s.now()

	r, err := create(e, ctx, &adminv1.CreateApiTokenRequest{Name: "  claude-ops  ", Profile: adminv1.TokenProfile_TOKEN_PROFILE_OPERATOR})
	if err != nil {
		t.Fatal(err)
	}
	tok := r.Token
	if !secretRe.MatchString(r.Secret) {
		t.Fatalf("secret shape: %q", r.Secret)
	}
	if tok.Name != "claude-ops" || tok.Profile != adminv1.TokenProfile_TOKEN_PROFILE_OPERATOR || tok.RateLimitPerMin != 120 ||
		tok.Hint != r.Secret[len(r.Secret)-4:] || tok.CreatedByName != e.owner.DisplayName || tok.RevokedUnix != 0 || tok.LastUsedUnix != 0 {
		t.Fatalf("token: %+v", tok)
	}
	if want := now.Add(90 * 24 * time.Hour).Unix(); tok.ExpiresUnix != want {
		t.Errorf("default lifetime: expires %d, want %d (90 days)", tok.ExpiresUnix, want)
	}
	// the secret is stored as its hash only: nowhere in the database, nowhere in the audit log
	var n int
	for _, q := range []string{
		`SELECT count(*) FROM api_token WHERE secret_hash = ?`,
	} {
		if err := e.st.W.QueryRow(q, hashToken(r.Secret)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("hash of the secret in the table: %d %v", n, err)
		}
	}
	rows, err := e.st.W.Query(`SELECT * FROM api_token`)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		for i, v := range vals {
			if strings.Contains(fmt.Sprint(v), r.Secret) || strings.Contains(string(asBytes(v)), r.Secret) {
				t.Errorf("the secret is stored in column %s", cols[i])
			}
		}
	}
	rows.Close()
	a := e.audits("token_create")
	if len(a) != 1 || a[0].Actor != e.owner.ID || a[0].Source != store.AuditPanel || strings.Contains(a[0].Params, r.Secret) ||
		!strings.Contains(a[0].Params, tok.Id) || !strings.Contains(a[0].Params, `"ttl_days":90`) || !strings.Contains(a[0].Params, `"profile":"operator"`) {
		t.Errorf("audit: %+v", a)
	}
	// and the secret works
	if got := e.code(pCreateUser, r.Secret); got != 200 {
		t.Errorf("the new token: %d", got)
	}

	// explicit values
	r2, err := create(e, ctx, &adminv1.CreateApiTokenRequest{Name: "nightly", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY, TtlDays: 365, RateLimitPerMin: 600})
	if err != nil || r2.Token.RateLimitPerMin != 600 || r2.Token.ExpiresUnix != now.Add(365*24*time.Hour).Unix() {
		t.Fatalf("explicit values: %+v %v", r2, err)
	}
	if r2.Secret == r.Secret {
		t.Fatal("two tokens share a secret")
	}

	bad := map[string]*adminv1.CreateApiTokenRequest{
		"empty name":         {Name: "", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY},
		"blank name":         {Name: "   ", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY},
		"65 characters":      {Name: strings.Repeat("a", 65), Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY},
		"control character":  {Name: "a\nb", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY},
		"invalid utf-8":      {Name: "a\xffb", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY},
		"no profile":         {Name: "x"},
		"366 days":           {Name: "x", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY, TtlDays: 366},
		"rate above 600":     {Name: "x", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY, RateLimitPerMin: 601},
		"an unknown profile": {Name: "x", Profile: adminv1.TokenProfile(9)},
		"a huge lifetime":    {Name: "x", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY, TtlDays: 1 << 30},
	}
	for name, m := range bad {
		if _, err := create(e, ctx, m); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 64 characters of any kind fit, counted in characters
	if _, err := create(e, ctx, &adminv1.CreateApiTokenRequest{Name: strings.Repeat("я", 64), Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); err != nil {
		t.Errorf("64 multi-byte characters: %v", err)
	}
	// the name is unique among the live tokens, ignoring case
	if _, err := create(e, ctx, &adminv1.CreateApiTokenRequest{Name: "CLAUDE-OPS", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("duplicate name: %v", err)
	}
	// "never expires" does not exist: nothing in the table has expires_at 0
	if err := e.st.W.QueryRow(`SELECT count(*) FROM api_token WHERE expires_at <= created_at`).Scan(&n); err != nil || n != 0 {
		t.Errorf("tokens without a lifetime: %d %v", n, err)
	}
	if got := len(e.audits("token_create")); got != 3 {
		t.Errorf("audit rows of the rejected requests: %d creates in all", got)
	}
}

func asBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case sql.RawBytes:
		return x
	}
	return nil
}

func TestCreateApiTokenCap(t *testing.T) {
	e := newRPCEnv(t)
	for i := range store.MaxLiveTokens {
		if _, err := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: fmt.Sprintf("t%02d", i), Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, err := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: "one-more", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("51st token: %v", err)
	}
}

func TestTokenManagementNeedsStepUpAndTheOwner(t *testing.T) {
	e := newRPCEnv(t)
	r, err := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: "ops", Profile: adminv1.TokenProfile_TOKEN_PROFILE_ADMIN})
	if err != nil {
		t.Fatal(err)
	}
	id := r.Token.Id

	stale := e.stale() // the session proved a factor 6 minutes ago
	if _, err := create(e, stale, &adminv1.CreateApiTokenRequest{Name: "late", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "step-up") {
		t.Errorf("create without a fresh step-up: %v", err)
	}
	if _, err := e.tok.RevokeApiToken(stale, connect.NewRequest(&adminv1.RevokeApiTokenRequest{Id: id})); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "step-up") {
		t.Errorf("revoke without a fresh step-up: %v", err)
	}
	if got, _ := e.st.GetAPIToken(context.Background(), id); got.Revoked() {
		t.Fatal("the token was revoked without a step-up")
	}
	// listing needs none
	if l, err := e.tok.ListApiTokens(stale, connect.NewRequest(&adminv1.ListApiTokensRequest{})); err != nil || len(l.Msg.Tokens) != 1 {
		t.Errorf("list without a step-up: %v", err)
	}

	// a helper or a read-only admin, even if the router let them in
	for _, role := range []string{store.RoleHelper, store.RoleReadonly} {
		a := insertAdmin(t, e.st, role)
		c, _ := e.s.newSession(context.Background(), a.ID, "", "")
		ctx := authed(t, e.s, c.Value)
		if _, err := create(e, ctx, &adminv1.CreateApiTokenRequest{Name: "x", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s creates a token: %v", role, err)
		}
		if _, err := e.tok.ListApiTokens(ctx, connect.NewRequest(&adminv1.ListApiTokensRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s lists tokens: %v", role, err)
		}
	}
	// a token principal is refused by the handlers themselves, whatever the router did: the synthetic owner of an
	// admin-profile token is not the owner at the keyboard
	tctx := context.WithValue(context.Background(), adminKey{}, store.Admin{ID: "token:" + id, DisplayName: "ops", Role: store.RoleOwner})
	tctx = context.WithValue(tctx, principalKey{}, Principal{Token: true, TokenID: id, Profile: store.ProfileAdmin, Channel: ChannelAPI})
	if _, err := create(e, tctx, &adminv1.CreateApiTokenRequest{Name: "mine", Profile: adminv1.TokenProfile_TOKEN_PROFILE_ADMIN}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a token creates a token: %v", err)
	}
	if _, err := e.tok.ListApiTokens(tctx, connect.NewRequest(&adminv1.ListApiTokensRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a token lists tokens: %v", err)
	}
	if _, err := e.tok.RevokeApiToken(tctx, connect.NewRequest(&adminv1.RevokeApiTokenRequest{Id: id})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a token revokes a token: %v", err)
	}
	if _, err := e.apr.ListApprovals(tctx, connect.NewRequest(&adminv1.ListApprovalsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a token lists approvals: %v", err)
	}
	if _, err := e.apr.Approve(tctx, connect.NewRequest(&adminv1.ApproveRequest{Id: "pln_x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a token approves: %v", err)
	}
	if _, err := e.apr.Reject(tctx, connect.NewRequest(&adminv1.RejectRequest{Id: "pln_x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a token rejects: %v", err)
	}
}

func TestRevokeApiToken(t *testing.T) {
	e := newRPCEnv(t)
	r, err := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: "ops", Profile: adminv1.TokenProfile_TOKEN_PROFILE_ADMIN})
	if err != nil {
		t.Fatal(err)
	}
	tokRow, _ := e.st.GetAPIToken(context.Background(), r.Token.Id)
	plan := e.planFor(tokRow, "rollout_start", true, "awaiting")

	if got := e.code(pGetUser, r.Secret); got != 200 {
		t.Fatalf("before the revoke: %d", got)
	}
	rv, err := e.tok.RevokeApiToken(e.ctx, connect.NewRequest(&adminv1.RevokeApiTokenRequest{Id: r.Token.Id}))
	if err != nil || rv.Msg.Token.RevokedUnix == 0 {
		t.Fatalf("revoke: %+v %v", rv, err)
	}
	// the very next request is refused, and the open plan is cancelled
	if got := e.code(pGetUser, r.Secret); got != 401 {
		t.Errorf("after the revoke: %d", got)
	}
	if p, _ := e.st.GetMCPPlan(context.Background(), plan.ID); p.Status != store.PlanCancelled {
		t.Errorf("plan after the revoke: %s", p.Status)
	}
	// idempotent, and the first revocation stays
	rv2, err := e.tok.RevokeApiToken(e.ctx, connect.NewRequest(&adminv1.RevokeApiTokenRequest{Id: r.Token.Id}))
	if err != nil || rv2.Msg.Token.RevokedUnix != rv.Msg.Token.RevokedUnix {
		t.Errorf("second revoke: %+v %v", rv2, err)
	}
	if _, err := e.tok.RevokeApiToken(e.ctx, connect.NewRequest(&adminv1.RevokeApiTokenRequest{Id: "tok_nothere"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := e.tok.RevokeApiToken(e.ctx, connect.NewRequest(&adminv1.RevokeApiTokenRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("no id: %v", err)
	}
	if a := e.audits("token_revoke"); len(a) != 2 || a[0].Actor != e.owner.ID || !strings.Contains(a[0].Params, r.Token.Id) {
		t.Errorf("audit: %+v", a)
	}
	// the name can be used again
	if _, err := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: "ops", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}); err != nil {
		t.Errorf("reusing the name of a revoked token: %v", err)
	}
}

func TestListApiTokens(t *testing.T) {
	e := newRPCEnv(t)
	a, _ := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: "first", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY})
	*e.clock = e.clock.Add(time.Minute)
	b, _ := create(e, e.ctx, &adminv1.CreateApiTokenRequest{Name: "second", Profile: adminv1.TokenProfile_TOKEN_PROFILE_ADMIN, TtlDays: 1})
	e.call(pGetUser, a.Secret, callOpts{remote: "198.51.100.4:1", ctx: WithChannel(context.Background(), ChannelMCP)})
	e.tok.RevokeApiToken(e.ctx, connect.NewRequest(&adminv1.RevokeApiTokenRequest{Id: b.Token.Id}))

	l, err := e.tok.ListApiTokens(e.ctx, connect.NewRequest(&adminv1.ListApiTokensRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if l.Msg.NowUnix != e.s.now().Unix() || len(l.Msg.Tokens) != 2 || l.Msg.Tokens[0].Name != "second" || l.Msg.Tokens[1].Name != "first" {
		t.Fatalf("list: %+v", l.Msg)
	}
	first, second := l.Msg.Tokens[1], l.Msg.Tokens[0]
	if first.LastUsedVia != adminv1.TokenChannel_TOKEN_CHANNEL_MCP || first.LastUsedIp != "198.51.100.4" || first.LastUsedUnix == 0 || first.RevokedUnix != 0 {
		t.Errorf("first: %+v", first)
	}
	if second.RevokedUnix == 0 || second.LastUsedVia != adminv1.TokenChannel_TOKEN_CHANNEL_UNSPECIFIED || second.Profile != adminv1.TokenProfile_TOKEN_PROFILE_ADMIN {
		t.Errorf("second: %+v", second)
	}
	// no secret, no hash in what the list says
	if strings.Contains(fmt.Sprint(l.Msg), a.Secret) || strings.Contains(fmt.Sprint(l.Msg), b.Secret) {
		t.Error("a secret in the list")
	}
}

// --- approvals ---

func (e *rpcEnv) approvals(awaitingOnly bool) *adminv1.ListApprovalsResponse {
	e.t.Helper()
	r, err := e.apr.ListApprovals(e.ctx, connect.NewRequest(&adminv1.ListApprovalsRequest{AwaitingOnly: awaitingOnly}))
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Msg
}

func TestApprovals(t *testing.T) {
	e := newRPCEnv(t)
	tok, secret := e.mkToken("ops", store.ProfileAdmin, 600)
	mk := func(tool string) store.MCPPlan {
		p := store.MCPPlan{TokenID: tok.ID, Tool: tool, ParamsJSON: `{"node":"n"}`, ConfirmHash: hashToken(store.NewID("cf_")),
			FactsJSON: `[{"key":"node","value":"de1 <b>x</b>","untrusted":true},{"key":"version","value":"1.2.3","untrusted":false}]`,
			Summary:   "s", Danger: `["fleet","step_up"]`, Reason: "the agent says so", NeedsApproval: true, Status: store.PlanAwaiting, CreatedAt: e.s.now()}
		if err := e.st.CreateMCPPlan(context.Background(), p, 20, 50); err != nil {
			t.Fatal(err)
		}
		got, err := e.st.MCPPlanByConfirm(context.Background(), tok.ID, p.ConfirmHash)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	p1 := mk("rollout_start")
	*e.clock = e.clock.Add(time.Second)
	p2 := mk("node_fix")
	safe := e.planFor(tok, "user_update", false, "planned") // never listed: it needs no one

	l := e.approvals(false)
	if l.Awaiting != 2 || len(l.Approvals) != 2 || l.Approvals[0].Id != p1.ID || l.Approvals[1].Id != p2.ID {
		t.Fatalf("list: %+v", l)
	}
	a := l.Approvals[0]
	if a.State != adminv1.ApprovalState_APPROVAL_STATE_AWAITING || a.Tool != "rollout_start" || a.TokenName != "ops" || a.TokenId != tok.ID ||
		a.TokenProfile != adminv1.TokenProfile_TOKEN_PROFILE_ADMIN || a.Reason != "the agent says so" ||
		len(a.Danger) != 2 || a.Danger[0] != "fleet" || len(a.Facts) != 2 || !a.Facts[0].Untrusted || a.Facts[1].Untrusted ||
		a.Facts[0].Key != "node" || a.Facts[0].Value != "de1 <b>x</b>" || a.ExpiresUnix != a.CreatedUnix+600 {
		t.Fatalf("approval: %+v", a)
	}
	if l.NowUnix != e.s.now().Unix() {
		t.Error("now_unix")
	}
	for _, x := range l.Approvals {
		if x.Id == safe.ID {
			t.Error("a safe plan is in the owner's inbox")
		}
	}

	// the agent cannot apply before the owner decides
	h := sha256Sum(p1.ParamsJSON)
	if _, err := e.st.BeginApply(context.Background(), p1.ID, tok.ID, p1.Tool, h, p1.ConfirmHash, e.s.now()); !errors.Is(err, store.ErrPlanState) {
		t.Fatalf("apply before approval: %v", err)
	}

	// approving needs a fresh step-up
	stale := e.stale()
	if _, err := e.apr.Approve(stale, connect.NewRequest(&adminv1.ApproveRequest{Id: p1.ID})); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "step-up") {
		t.Fatalf("approve without a fresh step-up: %v", err)
	}
	if got, _ := e.st.GetMCPPlan(context.Background(), p1.ID); got.Status != store.PlanAwaiting {
		t.Fatalf("the plan moved without a step-up: %s", got.Status)
	}
	// ... but the plan is expired by now (6 minutes + 1 s is inside 10): still awaiting. Approve with a new session.
	c, _ := e.s.newSession(context.Background(), e.owner.ID, "", "")
	fresh := authed(t, e.s, c.Value)
	ap, err := e.apr.Approve(fresh, connect.NewRequest(&adminv1.ApproveRequest{Id: p1.ID}))
	if err != nil || ap.Msg.Approval.State != adminv1.ApprovalState_APPROVAL_STATE_APPROVED || ap.Msg.Approval.DecidedByName != e.owner.DisplayName || ap.Msg.Approval.DecidedUnix == 0 {
		t.Fatalf("approve: %+v %v", ap, err)
	}
	// it is decided once
	if _, err := e.apr.Approve(fresh, connect.NewRequest(&adminv1.ApproveRequest{Id: p1.ID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("approve twice: %v", err)
	}
	if _, err := e.apr.Reject(fresh, connect.NewRequest(&adminv1.RejectRequest{Id: p1.ID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("reject after approve: %v", err)
	}
	// now the agent may apply, and only now
	if _, err := e.st.BeginApply(context.Background(), p1.ID, tok.ID, p1.Tool, h, p1.ConfirmHash, e.s.now()); err != nil {
		t.Fatalf("apply after approval: %v", err)
	}
	// reject needs no step-up (the context is stale again by the time we use it)
	rj, err := e.apr.Reject(stale, connect.NewRequest(&adminv1.RejectRequest{Id: p2.ID}))
	if err != nil || rj.Msg.Approval.State != adminv1.ApprovalState_APPROVAL_STATE_REJECTED {
		t.Fatalf("reject: %+v %v", rj, err)
	}
	h2 := sha256Sum(p2.ParamsJSON)
	if _, err := e.st.BeginApply(context.Background(), p2.ID, tok.ID, p2.Tool, h2, p2.ConfirmHash, e.s.now()); !errors.Is(err, store.ErrPlanState) {
		t.Errorf("apply after reject: %v", err)
	}

	// audit: who decided what, no secrets
	for action, plan := range map[string]store.MCPPlan{"approval_approve": p1, "approval_reject": p2} {
		rows := e.audits(action)
		if len(rows) != 1 || rows[0].Actor != e.owner.ID || rows[0].Source != store.AuditPanel ||
			!strings.Contains(rows[0].Params, plan.ID) || !strings.Contains(rows[0].Params, tok.ID) || !strings.Contains(rows[0].Params, plan.Tool) {
			t.Errorf("%s audit: %+v", action, rows)
		}
	}
	// unknown ids, empty ids
	if _, err := e.apr.Approve(fresh, connect.NewRequest(&adminv1.ApproveRequest{Id: "pln_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("approve unknown: %v", err)
	}
	if _, err := e.apr.Reject(fresh, connect.NewRequest(&adminv1.RejectRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("reject without an id: %v", err)
	}
	// the history shows what happened; the badge counts only what waits
	l = e.approvals(false)
	if l.Awaiting != 0 || len(l.Approvals) != 2 {
		t.Fatalf("history: %+v", l)
	}
	if l := e.approvals(true); len(l.Approvals) != 0 {
		t.Errorf("awaiting only: %+v", l)
	}
	_ = secret
}

func TestApprovalsExpire(t *testing.T) {
	e := newRPCEnv(t)
	tok, _ := e.mkToken("ops", store.ProfileAdmin, 600)
	p := e.planFor(tok, "node_fix", true, "awaiting")
	if l := e.approvals(true); l.Awaiting != 1 {
		t.Fatalf("awaiting: %d", l.Awaiting)
	}
	// ten minutes from the plan: the owner is too late, and the list already says so
	*e.clock = e.clock.Add(store.PlanTTL)
	if l := e.approvals(false); l.Awaiting != 0 || len(l.Approvals) != 1 || l.Approvals[0].State != adminv1.ApprovalState_APPROVAL_STATE_EXPIRED {
		t.Fatalf("after ten minutes: %+v", l)
	}
	c, _ := e.s.newSession(context.Background(), e.owner.ID, "", "")
	fresh := authed(t, e.s, c.Value)
	if _, err := e.apr.Approve(fresh, connect.NewRequest(&adminv1.ApproveRequest{Id: p.ID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("approve an expired plan: %v", err)
	}
	if _, err := e.apr.Reject(fresh, connect.NewRequest(&adminv1.RejectRequest{Id: p.ID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("reject an expired plan: %v", err)
	}
	// a plan whose token was revoked cannot be approved either
	p2 := e.planFor(tok, "rollout_start", true, "awaiting")
	e.st.RevokeAPIToken(context.Background(), tok.ID, e.owner.ID, e.s.now())
	if _, err := e.apr.Approve(fresh, connect.NewRequest(&adminv1.ApproveRequest{Id: p2.ID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("approve a plan of a revoked token: %v", err)
	}
	if l := e.approvals(false); l.Awaiting != 0 {
		t.Errorf("awaiting after the revoke: %d", l.Awaiting)
	}
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
