//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestTokenUserChangeBudgetMakesFourthDisableWaitForOwner(t *testing.T) {
	e := newTestEnv(t)
	id, secret := e.token(ProfileOperator)
	s := e.session(secret)
	ids := []string{"usr_n0", "usr_n1", "usr_n2"}
	for i := 0; i < 3; i++ {
		p := planOf(t, s, "user_disable", map[string]any{"user_ids": ids})
		if p.NeedsApproval {
			t.Fatalf("disable plan %d unexpectedly needs approval: %+v", i+1, p)
		}
		applyOf(t, s, "user_disable", p.ConfirmToken)
	}
	if used := e.userChanges[id]; used != 9 {
		t.Fatalf("used changes after three plans = %d, want 9", used)
	}

	fourth := planOf(t, s, "user_disable", map[string]any{"user_ids": ids})
	if !fourth.NeedsApproval || len(fourth.Danger) != 1 || fourth.Danger[0] != dangerUserChangeLimit {
		t.Fatalf("fourth disable plan = %+v, want token-limit owner approval", fourth)
	}
	if !hasBudgetFact(fourth.Facts, 9) {
		t.Fatalf("fourth plan omits the 9-of-10 reason: %+v", fourth.Facts)
	}
	if got := applyError(t, s, "user_disable", fourth.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("unapproved fourth apply: %q", got)
	}
	approvePlan(t, e, fourth)
	applyOf(t, s, "user_disable", fourth.ConfirmToken)
	if used := e.userChanges[id]; used != 9 {
		t.Fatalf("approved apply spent unattended budget: used %d, want 9", used)
	}
}

func TestTokenUserUpdateEleventhPlanWaitsForOwner(t *testing.T) {
	e := newTestEnv(t)
	id, secret := e.token(ProfileOperator)
	s := e.session(secret)
	for i := 1; i <= 10; i++ {
		p := planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "quota_bytes": uint64(1<<30 + i)})
		if p.NeedsApproval {
			t.Fatalf("update plan %d unexpectedly needs approval: %+v", i, p)
		}
		applyOf(t, s, "user_update", p.ConfirmToken)
	}
	if used := e.userChanges[id]; used != 10 {
		t.Fatalf("used changes after ten updates = %d, want 10", used)
	}

	eleventh := planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "quota_bytes": uint64(1<<30 + 11)})
	if !eleventh.NeedsApproval || len(eleventh.Danger) != 1 || eleventh.Danger[0] != dangerUserChangeLimit {
		t.Fatalf("eleventh update plan = %+v, want token-limit owner approval", eleventh)
	}
	if !hasBudgetFact(eleventh.Facts, 10) {
		t.Fatalf("eleventh plan omits the 10-of-10 reason: %+v", eleventh.Facts)
	}
	if got := applyError(t, s, "user_update", eleventh.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("unapproved eleventh apply: %q", got)
	}
	approvePlan(t, e, eleventh)
	applyOf(t, s, "user_update", eleventh.ConfirmToken)
	if used := e.userChanges[id]; used != 10 {
		t.Fatalf("approved update spent unattended budget: used %d, want 10", used)
	}
}

func hasBudgetFact(facts []Fact, used int) bool {
	for _, fact := range facts {
		if fact.Key == "user_change_limit" && fact.Code == "user_change_limit" &&
			fact.Params["used"] == fmt.Sprint(used) && fact.Params["limit"] == "10" {
			return true
		}
	}
	return false
}

func TestTokenUserChangeLimitRefusalAtApplyKeepsCodeAndWaitsForOwner(t *testing.T) {
	err := connect.NewError(connect.CodeResourceExhausted,
		errors.New("token_user_change_limit: asked=3&limit=10&retry_after_seconds=1800&used=9"))
	got := apiError(err)
	var outcome *outcomeError
	if !errors.As(got, &outcome) {
		t.Fatalf("apiError = %T %v, want outcome error", got, got)
	}
	if outcome.out.Code != "token_user_change_limit" {
		t.Fatalf("outcome code = %q, want token_user_change_limit", outcome.out.Code)
	}
	for key, want := range map[string]string{"asked": "3", "limit": "10", "retry_after_seconds": "1800", "used": "9"} {
		if outcome.out.Params[key] != want {
			t.Errorf("outcome param %s = %q, want %q", key, outcome.out.Params[key], want)
		}
	}
	if !strings.Contains(got.Error(), "Make a new plan; it will wait for the owner to approve it.") {
		t.Fatalf("agent error = %q, want a new plan that waits for the owner", got)
	}
}

func TestPlanCarriesBothBulkAndTokenLimitDangers(t *testing.T) {
	e := newTestEnv(t)
	id, secret := e.token(ProfileOperator)
	e.userChanges[id] = 9
	p := planOf(t, e.session(secret), "user_disable", map[string]any{
		"user_ids": []string{"usr_n0", "usr_n1", "usr_n2", "usr_n3"},
	})
	if !p.NeedsApproval || len(p.Danger) != 2 || !hasDanger(p.Danger, dangerBulk) || !hasDanger(p.Danger, dangerUserChangeLimit) {
		t.Fatalf("plan dangers = %v, want bulk and user_change_limit", p.Danger)
	}
	if !hasBudgetFact(p.Facts, 9) {
		t.Fatalf("plan omits coded token limit fact: %+v", p.Facts)
	}
}

func hasDanger(dangers []string, want string) bool {
	for _, danger := range dangers {
		if danger == want {
			return true
		}
	}
	return false
}

// Enabling gives access back: however many people it names, the plan needs nobody.
func TestEnablingManyUsersNeedsNoApproval(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	p := planOf(t, e.session(secret), "user_enable", map[string]any{"user_ids": []string{"usr_n0", "usr_n1", "usr_n2", "usr_n3"}})
	if p.NeedsApproval {
		t.Errorf("enabling four users waits for the owner: %v", p.Danger)
	}
}
