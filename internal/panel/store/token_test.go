package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Unix(1_800_000_000, 0).UTC()

func hashOf(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func mkAdmin(t *testing.T, s *Store, id, name string) {
	t.Helper()
	execT(t, s, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, 'owner', x'01', 1)`, id, name)
}

func newTok(t *testing.T, s *Store, name, profile string) APIToken {
	t.Helper()
	tok := APIToken{ID: NewID("tok_"), Name: name, Profile: profile, Hint: "wxyz", RatePerMin: 120, CreatedBy: "adm_a",
		CreatedAt: t0, ExpiresAt: t0.Add(90 * 24 * time.Hour)}
	if err := s.CreateAPIToken(context.Background(), tok, hashOf("secret-"+tok.ID)); err != nil {
		t.Fatalf("create token %q: %v", name, err)
	}
	return tok
}

func TestAPITokenLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")

	tok := newTok(t, s, "Claude-Ops", ProfileOperator)
	got, err := s.APITokenBySecretHash(ctx, hashOf("secret-"+tok.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID || got.Name != "Claude-Ops" || got.Profile != ProfileOperator || got.CreatedByName != "Ada" ||
		got.RatePerMin != 120 || !got.CreatedAt.Equal(t0) || !got.ExpiresAt.Equal(tok.ExpiresAt) ||
		got.Revoked() || !got.LastUsedAt.IsZero() || got.Hint != "wxyz" {
		t.Fatalf("token read back: %+v", got)
	}
	if got.Expired(t0) || !got.Expired(tok.ExpiresAt) {
		t.Fatal("Expired: the token lives up to, not including, expires_at")
	}
	if _, err := s.APITokenBySecretHash(ctx, hashOf("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown secret: %v", err)
	}
	if _, err := s.GetAPIToken(ctx, "tok_none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}

	// names are unique among the unrevoked tokens, ignoring case
	dup := APIToken{ID: NewID("tok_"), Name: "claude-OPS", Profile: ProfileReadonly, Hint: "aaaa", RatePerMin: 10, CreatedBy: "adm_a", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := s.CreateAPIToken(ctx, dup, hashOf("dup")); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}

	// use
	if err := s.TouchAPIToken(ctx, tok.ID, t0.Add(time.Minute), "203.0.113.7", "mcp"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAPIToken(ctx, tok.ID)
	if !got.LastUsedAt.Equal(t0.Add(time.Minute)) || got.LastUsedIP != "203.0.113.7" || got.LastUsedVia != "mcp" {
		t.Fatalf("last use: %+v", got)
	}

	// revoke: idempotent, keeps the first revoker, frees the name
	r1, err := s.RevokeAPIToken(ctx, tok.ID, "adm_a", t0.Add(2*time.Minute))
	if err != nil || !r1.Revoked() || r1.RevokedBy != "adm_a" {
		t.Fatalf("revoke: %+v %v", r1, err)
	}
	r2, err := s.RevokeAPIToken(ctx, tok.ID, "adm_b", t0.Add(time.Hour))
	if err != nil || !r2.RevokedAt.Equal(r1.RevokedAt) || r2.RevokedBy != "adm_a" {
		t.Fatalf("second revoke changed the record: %+v %v", r2, err)
	}
	if _, err := s.RevokeAPIToken(ctx, "tok_none", "adm_a", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	if err := s.CreateAPIToken(ctx, dup, hashOf("dup")); err != nil {
		t.Fatalf("the name of a revoked token is free again: %v", err)
	}
	// the revoked row is still found by its secret (the caller says "revoked")
	if got, err := s.APITokenBySecretHash(ctx, hashOf("secret-"+tok.ID)); err != nil || !got.Revoked() {
		t.Fatalf("revoked token by secret: %+v %v", got, err)
	}

	// list: newest first, revoked included
	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 2 || list[0].ID != dup.ID || list[1].ID != tok.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestAPITokenCap(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	var first APIToken
	for i := range MaxLiveTokens {
		tok := newTok(t, s, fmt.Sprintf("t%d", i), ProfileReadonly)
		if i == 0 {
			first = tok
		}
	}
	over := APIToken{ID: NewID("tok_"), Name: "one-more", Profile: ProfileReadonly, Hint: "aaaa", RatePerMin: 10, CreatedBy: "adm_a", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := s.CreateAPIToken(ctx, over, hashOf("over")); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("51st token: %v", err)
	}
	if _, err := s.RevokeAPIToken(ctx, first.ID, "adm_a", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(ctx, over, hashOf("over")); err != nil {
		t.Fatalf("after a revoke there is room: %v", err)
	}
	// expired tokens do not count against the cap: a much later "now" frees every slot
	late := over
	late.ID, late.Name, late.CreatedAt, late.ExpiresAt = NewID("tok_"), "late", t0.Add(200*24*time.Hour), t0.Add(210*24*time.Hour)
	if err := s.CreateAPIToken(ctx, late, hashOf("late")); err != nil {
		t.Fatalf("expired tokens must not hold the cap: %v", err)
	}
}

func TestListAuditNamesTokenActors(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "claude-ops", ProfileAdmin)
	for _, actor := range []string{"token:" + tok.ID, "mcp:" + tok.ID, "adm_a", "token:tok_gone", "cli"} {
		if err := s.Audit(ctx, t0, AuditEntry{Actor: actor, Action: "x", Source: AuditAPI}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListAudit(ctx, "", 0, 10)
	if err != nil || len(rows) != 5 {
		t.Fatalf("%v %v", rows, err)
	}
	want := map[string]string{"token:" + tok.ID: "claude-ops", "mcp:" + tok.ID: "claude-ops", "adm_a": "Ada", "token:tok_gone": "", "cli": ""}
	for _, r := range rows {
		if r.ActorName != want[r.Actor] {
			t.Errorf("actor %s: name %q, want %q", r.Actor, r.ActorName, want[r.Actor])
		}
	}
}

func TestAuditSourceFromContext(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src := func(c context.Context, e AuditEntry) string {
		t.Helper()
		if err := s.Audit(c, t0, e); err != nil {
			t.Fatal(err)
		}
		rows, _ := s.ListAudit(ctx, "", 0, 1)
		return rows[0].Source
	}
	if got := src(ctx, AuditEntry{Actor: "a", Action: "x"}); got != AuditPanel {
		t.Errorf("no context source: %s", got)
	}
	if got := src(WithAuditSource(ctx, AuditMCP), AuditEntry{Actor: "a", Action: "x"}); got != AuditMCP {
		t.Errorf("context source: %s", got)
	}
	if got := src(WithAuditSource(ctx, AuditAPI), AuditEntry{Actor: "a", Action: "x", Source: AuditBot}); got != AuditBot {
		t.Errorf("an explicit source wins: %s", got)
	}
	if got := src(WithAuditSource(ctx, "bogus"), AuditEntry{Actor: "a", Action: "x"}); got != AuditPanel {
		t.Errorf("an unknown source is ignored (the CHECK would refuse the row): %s", got)
	}
}

// ---- plans ----

func newPlan(t *testing.T, s *Store, tokID, tool string, dangerous bool, at time.Time) MCPPlan {
	t.Helper()
	p := MCPPlan{TokenID: tokID, Tool: tool, ParamsJSON: `{"a":1}`, ConfirmHash: hashOf("cf-" + NewID("")), FactsJSON: `[{"key":"node","value":"n","untrusted":true}]`,
		Summary: "s", Danger: `["fleet"]`, Reason: "because", NeedsApproval: dangerous, Status: PlanPlanned, CreatedAt: at}
	if dangerous {
		p.Status = PlanAwaiting
	}
	if err := s.CreateMCPPlan(context.Background(), p, 20, 50); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	got, err := s.MCPPlanByConfirm(context.Background(), tokID, p.ConfirmHash)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func planStatus(t *testing.T, s *Store, id string) string {
	t.Helper()
	p, err := s.GetMCPPlan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

func stateOf(err error) string {
	var pe *PlanStateError
	if errors.As(err, &pe) {
		return pe.Status
	}
	return "<" + fmt.Sprint(err) + ">"
}

func TestPlanApplyStateMachine(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)
	other := newTok(t, s, "other", ProfileAdmin)

	// a safe plan applies at once; the apply is bound to token, tool and arguments
	p := newPlan(t, s, tok.ID, "user_update", false, t0)
	if p.Status != PlanPlanned || !p.ExpiresAt.Equal(t0.Add(PlanTTL)) || p.TokenName != "ops" || p.TokenProfile != ProfileAdmin || p.Reason != "because" {
		t.Fatalf("new plan: %+v", p)
	}
	if _, err := s.BeginApply(ctx, p.ID, other.ID, "user_update", p.ParamsHash, p.ConfirmHash, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("another token's apply: %v", err)
	}
	if _, err := s.BeginApply(ctx, p.ID, tok.ID, "user_create", p.ParamsHash, p.ConfirmHash, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("another tool's apply: %v", err)
	}
	if _, err := s.BeginApply(ctx, p.ID, tok.ID, "user_update", p.ParamsHash, hashOf("wrong"), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong confirm hash: %v", err)
	}
	if _, err := s.BeginApply(ctx, p.ID, tok.ID, "user_update", hashOf("other args"), p.ConfirmHash, t0); err == nil || errors.Is(err, ErrPlanState) {
		t.Errorf("wrong params hash: %v", err)
	}
	got, err := s.BeginApply(ctx, p.ID, tok.ID, "user_update", p.ParamsHash, p.ConfirmHash, t0.Add(time.Minute))
	if err != nil || got.Status != PlanApplying {
		t.Fatalf("apply: %+v %v", got, err)
	}
	if _, err := s.BeginApply(ctx, p.ID, tok.ID, "user_update", p.ParamsHash, p.ConfirmHash, t0.Add(time.Minute)); stateOf(err) != PlanApplying {
		t.Errorf("second apply: %v", err)
	}
	if err := s.FinishApply(ctx, p.ID, true, "user updated", "", "user_updated", "", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fin, _ := s.GetMCPPlan(ctx, p.ID)
	if fin.Status != PlanApplied || fin.Result != "user updated" || !fin.AppliedAt.Equal(t0.Add(2*time.Minute)) || fin.OutcomeCode != "user_updated" || fin.OutcomeParams != "{}" {
		t.Fatalf("finished: %+v", fin)
	}
	if err := s.FinishApply(ctx, p.ID, true, "again", "", "", "", t0); stateOf(err) != PlanApplied {
		t.Errorf("finishing twice: %v", err)
	}
	if _, err := s.BeginApply(ctx, p.ID, tok.ID, "user_update", p.ParamsHash, p.ConfirmHash, t0.Add(3*time.Minute)); stateOf(err) != PlanApplied {
		t.Errorf("apply after applied: %v", err)
	}

	// a failure is recorded
	f := newPlan(t, s, tok.ID, "user_update", false, t0)
	if _, err := s.BeginApply(ctx, f.ID, tok.ID, "user_update", f.ParamsHash, f.ConfirmHash, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishApply(ctx, f.ID, false, "", "boom", "no_trusted_bundle", `{"detail":"x"}`, t0); err != nil {
		t.Fatal(err)
	}
	if ff, _ := s.GetMCPPlan(ctx, f.ID); ff.Status != PlanFailed || ff.Error != "boom" || ff.OutcomeCode != "no_trusted_bundle" || ff.OutcomeParams != `{"detail":"x"}` {
		t.Fatalf("failed plan: %+v", ff)
	}

	// a plan is dead after its ten minutes, even before the sweep ran
	e := newPlan(t, s, tok.ID, "user_update", false, t0)
	if _, err := s.BeginApply(ctx, e.ID, tok.ID, "user_update", e.ParamsHash, e.ConfirmHash, t0.Add(PlanTTL)); stateOf(err) != PlanExpired {
		t.Errorf("apply at expiry: %v", err)
	}
	if _, err := s.BeginApply(ctx, e.ID, tok.ID, "user_update", e.ParamsHash, e.ConfirmHash, t0.Add(PlanTTL-time.Second)); err != nil {
		t.Errorf("apply one second before expiry: %v", err)
	}
}

// What the owner entered when approving (the node_install password) goes to that plan's apply once, and never outlives
// a plan that can no longer be applied.
func TestOwnerSecretIsTakenOnceAndNeverOutlivesThePlan(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)
	other := newTok(t, s, "other", ProfileAdmin)
	secretOf := func(id string) []byte {
		var b []byte
		if err := s.R.QueryRow(`SELECT owner_secret FROM mcp_plan WHERE id = ?`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	p := newPlan(t, s, tok.ID, "node_install", true, t0)
	if _, err := s.ApproveMCPPlanWithSecret(ctx, p.ID, "adm_a", []byte("sealed"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeMCPPlanOwnerSecret(ctx, p.ID, tok.ID, "node_install"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taken before the apply began: %v", err)
	}
	if _, err := s.BeginApply(ctx, p.ID, tok.ID, p.Tool, p.ParamsHash, p.ConfirmHash, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tok, tool string }{{other.ID, "node_install"}, {tok.ID, "node_fix"}} {
		if _, err := s.TakeMCPPlanOwnerSecret(ctx, p.ID, c.tok, c.tool); !errors.Is(err, ErrNotFound) {
			t.Fatalf("taken by %s/%s: %v", c.tok, c.tool, err)
		}
	}
	if got, err := s.TakeMCPPlanOwnerSecret(ctx, p.ID, tok.ID, "node_install"); err != nil || string(got) != "sealed" {
		t.Fatalf("take = %q, %v", got, err)
	}
	if _, err := s.TakeMCPPlanOwnerSecret(ctx, p.ID, tok.ID, "node_install"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taken twice: %v", err)
	}

	// an apply that never took it clears it when it finishes
	f := newPlan(t, s, tok.ID, "node_install", true, t0)
	if _, err := s.ApproveMCPPlanWithSecret(ctx, f.ID, "adm_a", []byte("sealed"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginApply(ctx, f.ID, tok.ID, f.Tool, f.ParamsHash, f.ConfirmHash, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishApply(ctx, f.ID, false, "", "boom", "", "", t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if secretOf(f.ID) != nil {
		t.Fatal("a finished apply kept the owner secret")
	}

	// an approved plan nobody applied: the sweep clears it once it expires
	x := newPlan(t, s, tok.ID, "node_install", true, t0)
	if _, err := s.ApproveMCPPlanWithSecret(ctx, x.ID, "adm_a", []byte("sealed"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExpireMCPPlans(ctx, t0.Add(5*time.Minute)); err != nil || secretOf(x.ID) == nil {
		t.Fatalf("the sweep cleared a plan still open: %v", err)
	}
	if _, err := s.ExpireMCPPlans(ctx, t0.Add(PlanTTL+time.Second)); err != nil || secretOf(x.ID) != nil {
		t.Fatalf("an expired plan kept the owner secret: %v", err)
	}
}

func TestDangerousPlanNeedsTheOwner(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)

	apply := func(p MCPPlan, at time.Time) error {
		_, err := s.BeginApply(ctx, p.ID, tok.ID, p.Tool, p.ParamsHash, p.ConfirmHash, at)
		return err
	}
	p := newPlan(t, s, tok.ID, "rollout_start", true, t0)
	if p.Status != PlanAwaiting || !p.NeedsApproval {
		t.Fatalf("%+v", p)
	}
	if err := apply(p, t0.Add(time.Second)); stateOf(err) != PlanAwaiting {
		t.Fatalf("apply before the owner decided: %v", err)
	}
	ap, err := s.DecideMCPPlan(ctx, p.ID, "adm_a", true, t0.Add(time.Minute))
	if err != nil || ap.Status != PlanApproved || ap.DecidedBy != "adm_a" || ap.DecidedByName != "Ada" || !ap.DecidedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("approve: %+v %v", ap, err)
	}
	if _, err := s.DecideMCPPlan(ctx, p.ID, "adm_a", false, t0.Add(time.Minute)); stateOf(err) != PlanApproved {
		t.Fatalf("a decided plan cannot be decided again: %v", err)
	}
	if err := apply(p, t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("apply after approval: %v", err)
	}

	// rejected
	r := newPlan(t, s, tok.ID, "node_fix", true, t0)
	rj, err := s.DecideMCPPlan(ctx, r.ID, "adm_a", false, t0.Add(time.Minute))
	if err != nil || rj.Status != PlanRejected {
		t.Fatalf("reject: %+v %v", rj, err)
	}
	if err := apply(r, t0.Add(2*time.Minute)); stateOf(err) != PlanRejected {
		t.Fatalf("apply after reject: %v", err)
	}

	// the owner is too late: 10 minutes from the plan
	late := newPlan(t, s, tok.ID, "node_rollback", true, t0)
	if _, err := s.DecideMCPPlan(ctx, late.ID, "adm_a", true, t0.Add(PlanTTL)); stateOf(err) != PlanExpired {
		t.Fatalf("approve at expiry: %v", err)
	}
	if _, err := s.DecideMCPPlan(ctx, "pln_none", "adm_a", true, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve unknown: %v", err)
	}
	// an approval that is not for a dangerous plan cannot happen: a safe plan is never awaiting
	safe := newPlan(t, s, tok.ID, "user_update", false, t0)
	if _, err := s.DecideMCPPlan(ctx, safe.ID, "adm_a", true, t0.Add(time.Second)); stateOf(err) != PlanPlanned {
		t.Fatalf("deciding a safe plan: %v", err)
	}
}

func TestRevokeCancelsOpenPlans(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)
	keep := newTok(t, s, "keep", ProfileAdmin)

	planned := newPlan(t, s, tok.ID, "user_update", false, t0)
	awaiting := newPlan(t, s, tok.ID, "rollout_start", true, t0)
	approved := newPlan(t, s, tok.ID, "node_fix", true, t0)
	if _, err := s.DecideMCPPlan(ctx, approved.ID, "adm_a", true, t0); err != nil {
		t.Fatal(err)
	}
	running := newPlan(t, s, tok.ID, "user_update", false, t0)
	if _, err := s.BeginApply(ctx, running.ID, tok.ID, "user_update", running.ParamsHash, running.ConfirmHash, t0); err != nil {
		t.Fatal(err)
	}
	applied := newPlan(t, s, tok.ID, "user_update", false, t0)
	s.BeginApply(ctx, applied.ID, tok.ID, "user_update", applied.ParamsHash, applied.ConfirmHash, t0)
	s.FinishApply(ctx, applied.ID, true, "ok", "", "", "", t0)
	others := newPlan(t, s, keep.ID, "user_update", false, t0)

	if _, err := s.RevokeAPIToken(ctx, tok.ID, "adm_a", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		planned.ID: PlanCancelled, awaiting.ID: PlanCancelled, approved.ID: PlanCancelled,
		running.ID: PlanApplying, applied.ID: PlanApplied, others.ID: PlanPlanned,
	} {
		if got := planStatus(t, s, id); got != want {
			t.Errorf("plan %s after revoke: %s, want %s", id, got, want)
		}
	}
	if _, err := s.BeginApply(ctx, planned.ID, tok.ID, "user_update", planned.ParamsHash, planned.ConfirmHash, t0.Add(time.Minute)); stateOf(err) != PlanCancelled {
		t.Errorf("apply of a cancelled plan: %v", err)
	}
	// a revoked token cannot make new plans
	np := MCPPlan{TokenID: tok.ID, Tool: "user_update", ParamsJSON: "{}", ConfirmHash: hashOf("n"), Status: PlanPlanned, CreatedAt: t0.Add(time.Minute)}
	if err := s.CreateMCPPlan(ctx, np, 20, 50); stateOf(err) != PlanCancelled {
		t.Errorf("plan by a revoked token: %v", err)
	}
	// and the owner cannot approve one that slipped through: a plan whose token is revoked stays undecided
	if _, err := s.DecideMCPPlan(ctx, awaiting.ID, "adm_a", true, t0.Add(time.Minute)); stateOf(err) != PlanCancelled {
		t.Errorf("approve of a cancelled plan: %v", err)
	}
}

func TestPlanCaps(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	a, b := newTok(t, s, "a", ProfileAdmin), newTok(t, s, "b", ProfileAdmin)
	mk := func(tok APIToken, dangerous bool, at time.Time) error {
		p := MCPPlan{TokenID: tok.ID, Tool: "t", ParamsJSON: "{}", ConfirmHash: hashOf(NewID("c")), Status: PlanPlanned, CreatedAt: at}
		if dangerous {
			p.Status, p.NeedsApproval = PlanAwaiting, true
		}
		return s.CreateMCPPlan(ctx, p, 3, 4)
	}
	for range 3 {
		if err := mk(a, false, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := mk(a, false, t0); !errors.Is(err, ErrTooManyPlans) {
		t.Fatalf("4th open plan of a token: %v", err)
	}
	if err := mk(b, false, t0); err != nil {
		t.Fatalf("another token has its own room: %v", err)
	}
	// plans past their expiry do not count
	if err := mk(a, false, t0.Add(PlanTTL)); err != nil {
		t.Fatalf("the old plans expired, there is room: %v", err)
	}
	// awaiting: 4 in the whole panel
	for i := range 4 {
		tok := []APIToken{a, b}[i%2]
		if err := mk(tok, true, t0.Add(time.Hour)); err != nil {
			t.Fatalf("awaiting %d: %v", i, err)
		}
	}
	c := newTok(t, s, "c", ProfileAdmin)
	if err := mk(c, true, t0.Add(time.Hour)); !errors.Is(err, ErrTooManyPlans) {
		t.Fatalf("5th awaiting plan in the panel: %v", err)
	}
	if err := mk(c, false, t0.Add(time.Hour)); err != nil {
		t.Fatalf("a safe plan is not held back by the inbox cap: %v", err)
	}
}

func TestBeginApplyRace(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)
	p := newPlan(t, s, tok.ID, "user_update", false, t0)
	var wins, state atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.BeginApply(ctx, p.ID, tok.ID, p.Tool, p.ParamsHash, p.ConfirmHash, t0)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrPlanState):
				state.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || state.Load() != 15 {
		t.Fatalf("%d winners, %d refused: want exactly one winner", wins.Load(), state.Load())
	}
}

func TestDecideRace(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)
	p := newPlan(t, s, tok.ID, "node_fix", true, t0)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.DecideMCPPlan(ctx, p.ID, "adm_a", i%2 == 0, t0); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("%d decisions took effect, want one", ok.Load())
	}
}

func TestExpireMCPPlans(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)

	planned := newPlan(t, s, tok.ID, "user_update", false, t0)
	awaiting := newPlan(t, s, tok.ID, "rollout_start", true, t0)
	approved := newPlan(t, s, tok.ID, "node_fix", true, t0)
	s.DecideMCPPlan(ctx, approved.ID, "adm_a", true, t0)
	fresh := newPlan(t, s, tok.ID, "user_update", false, t0.Add(8*time.Minute))
	stuck := newPlan(t, s, tok.ID, "user_update", false, t0)
	s.BeginApply(ctx, stuck.ID, tok.ID, stuck.Tool, stuck.ParamsHash, stuck.ConfirmHash, t0.Add(time.Minute))
	young := newPlan(t, s, tok.ID, "user_update", false, t0.Add(4*time.Minute))
	s.BeginApply(ctx, young.ID, tok.ID, young.Tool, young.ParamsHash, young.ConfirmHash, t0.Add(5*time.Minute))
	done := newPlan(t, s, tok.ID, "user_update", false, t0)
	s.BeginApply(ctx, done.ID, tok.ID, done.Tool, done.ParamsHash, done.ConfirmHash, t0)
	s.FinishApply(ctx, done.ID, true, "ok", "", "", "", t0)

	n, err := s.ExpireMCPPlans(ctx, t0.Add(PlanTTL+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// planned, awaiting and approved expired; stuck (began 9 min 1 s ago) and young (5 min 1 s ago) are past 5 minutes.
	if n != 5 {
		t.Errorf("first sweep touched %d rows, want 5", n)
	}
	for id, want := range map[string]string{
		planned.ID: PlanExpired, awaiting.ID: PlanExpired, approved.ID: PlanExpired,
		fresh.ID: PlanPlanned, stuck.ID: PlanFailed, young.ID: PlanFailed, done.ID: PlanApplied,
	} {
		if got := planStatus(t, s, id); got != want {
			t.Errorf("plan %s: %s, want %s", id, got, want)
		}
	}
	if sp, _ := s.GetMCPPlan(ctx, stuck.ID); sp.Error == "" || sp.OutcomeCode != "interrupted" {
		t.Error("a stuck apply says why it failed")
	}
	// the sweep never resumes an apply: a late FinishApply is refused
	if err := s.FinishApply(ctx, stuck.ID, true, "late", "", "", "", t0.Add(time.Hour)); !errors.Is(err, ErrPlanState) {
		t.Errorf("finishing an interrupted apply: %v", err)
	}
	// finished plans older than 30 days go away, open ones stay until they are finished
	n, err = s.ExpireMCPPlans(ctx, t0.Add(PlanKeep+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMCPPlan(ctx, done.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a 30 day old finished plan is deleted: %v", err)
	}
	if n == 0 {
		t.Error("the second sweep reported nothing")
	}
}

func TestListApprovals(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	tok := newTok(t, s, "ops", ProfileAdmin)

	safe := newPlan(t, s, tok.ID, "user_update", false, t0) // never listed
	_ = safe
	old := newPlan(t, s, tok.ID, "rollout_start", true, t0.Add(1*time.Minute))
	newer := newPlan(t, s, tok.ID, "node_fix", true, t0.Add(2*time.Minute))
	rej := newPlan(t, s, tok.ID, "node_rollback", true, t0.Add(3*time.Minute))
	s.DecideMCPPlan(ctx, rej.ID, "adm_a", false, t0.Add(3*time.Minute))
	apd := newPlan(t, s, tok.ID, "rollout_pause", true, t0.Add(4*time.Minute))
	s.DecideMCPPlan(ctx, apd.ID, "adm_a", true, t0.Add(4*time.Minute))

	rows, awaiting, err := s.ListApprovals(ctx, t0.Add(5*time.Minute), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.ID+":"+r.Status)
	}
	want := []string{old.ID + ":awaiting", newer.ID + ":awaiting", apd.ID + ":approved", rej.ID + ":rejected"}
	if awaiting != 2 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("awaiting %d, rows %v, want %v", awaiting, got, want)
	}
	if rows[0].TokenName != "ops" || rows[0].TokenProfile != ProfileAdmin || rows[3].DecidedByName != "Ada" {
		t.Fatalf("names: %+v %+v", rows[0], rows[3])
	}
	// awaiting only
	rows, awaiting, _ = s.ListApprovals(ctx, t0.Add(5*time.Minute), true, 0)
	if len(rows) != 2 || awaiting != 2 {
		t.Fatalf("awaiting only: %d rows, %d", len(rows), awaiting)
	}
	// past the expiry the waiting ones are history, shown as expired, and the badge count drops
	rows, awaiting, _ = s.ListApprovals(ctx, t0.Add(1*time.Minute+PlanTTL), false, 0)
	if awaiting != 1 || rows[0].ID != newer.ID {
		t.Fatalf("after the first expiry: %d awaiting, first %s", awaiting, rows[0].ID)
	}
	for _, r := range rows[1:] {
		if r.ID == old.ID && r.Status != PlanExpired {
			t.Errorf("an unanswered approval past its time shows %s", r.Status)
		}
	}
	// the history limit applies to the history only
	rows, _, _ = s.ListApprovals(ctx, t0.Add(5*time.Minute), false, 1)
	if len(rows) != 3 {
		t.Fatalf("history limit 1: %d rows", len(rows))
	}
}
