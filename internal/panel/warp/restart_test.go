package warp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// "Register again" for an account Cloudflare revoked is one step: a new device takes the old one's place, the old
// device is deleted at Cloudflare, and nothing stays half done.
func TestRegisterAgainReplacesTheAccount(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	oldKey := e.privateKeyOf(node)
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	again := func(replace bool) (*adminv1.RegisterWarpResponse, error) {
		r, err := e.rpc().RegisterWarp(e.ctx, connect.NewRequest(&adminv1.RegisterWarpRequest{NodeId: node, AcceptTos: true, TosUrlShown: TOSURL, ReplaceExisting: replace}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	_, err := again(false)
	e.wantErr(err, connect.CodeFailedPrecondition, "account_exists")

	e.kicks, e.steps = 0, 0
	resp, err := again(true)
	if err != nil {
		t.Fatal(err)
	}
	if e.steps != 1 {
		t.Errorf("step-up asked %d times, want 1", e.steps)
	}
	if !resp.Account.Enabled || e.kicks != 1 {
		t.Errorf("account %+v, kicks %d", resp.Account, e.kicks)
	}
	if !e.cf.reg(1).deleted || e.cf.reg(2) == nil || e.cf.reg(2).deleted {
		t.Errorf("old device deleted %v, new device %+v", e.cf.reg(1).deleted, e.cf.reg(2))
	}
	if newKey := e.privateKeyOf(node); newKey == oldKey {
		t.Error("the node still has the old key")
	}
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if err != nil || g.Msg.NeedsAttention {
		t.Errorf("the new account still needs attention: %+v %v", g, err)
	}
	if !contains(e.auditActions(), "warp_reregister") {
		t.Errorf("audit = %v", e.auditActions())
	}
	e.noSecrets(resp)

	// asked on a node without an account, it is a plain registration
	other := e.addNode("de2")
	e.kicks = 0
	r, err := e.rpc().RegisterWarp(e.ctx, connect.NewRequest(&adminv1.RegisterWarpRequest{NodeId: other, AcceptTos: true, ReplaceExisting: true}))
	if err != nil || r.Msg.Account == nil || e.kicks != 1 {
		t.Fatalf("replace without an account: %v %v", r, err)
	}
}

// applyAtOnce plays the node applying every new desired state at once, WARP paused or not as the account says.
func (e *env) applyAtOnce(node string) {
	n := 0
	e.onKick = func() {
		n++
		h := fmt.Sprintf("h%d", n)
		e.mu.Lock()
		defer e.mu.Unlock()
		if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET desired_hash = ?, applied_hash = ? WHERE id = ?`, h, h, node); err != nil {
			e.t.Error(err)
		}
		a, err := e.st.WarpAccount(e.ctx, node)
		e.paused = err == nil && !a.Enabled
	}
}

// The pause counts only when the node confirms a state with WARP paused. Here every kick lands as some other change the
// node applies (the hashes move, as with a recompute that still had WARP on): that is not the pause, the tunnel was never
// torn down, and the restart says so.
func TestRestartWarpNeedsThePauseItselfConfirmed(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	e.live[node] = true
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	n := 0
	e.onKick = func() {
		n++
		h := fmt.Sprintf("other%d", n)
		if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET desired_hash = ?, applied_hash = ? WHERE id = ?`, h, h, node); err != nil {
			e.t.Error(err)
		}
	}
	r, err := e.restart(node)
	if err != nil {
		t.Fatal(err)
	}
	if r.Confirmed || !r.Account.Enabled {
		t.Fatalf("restart: %+v", r)
	}
}

// The owner pauses WARP while a restart waits for the node: the restart's resume must not undo it. The owner's call waits
// for the restart to finish, then pauses.
func TestRestartWarpKeepsAPauseMadeMeanwhile(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	e.live[node] = true
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	owner := make(chan error, 1)
	started := false
	e.onKick = func() {
		if started {
			return
		}
		started = true // the restart's pause: the owner clicks "Pause" now
		go func() {
			_, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: false}))
			owner <- err
		}()
	}
	looked := false
	e.s.cfg.Sleep = func(context.Context, time.Duration) error {
		if !looked { // give the owner's call the time to get through, if anything lets it
			looked = true
			select {
			case err := <-owner:
				owner <- err
			case <-time.After(200 * time.Millisecond):
			}
		}
		return nil
	}
	if _, err := e.restart(node); err != nil {
		t.Fatal(err)
	}
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	if sp, _ := e.s.Spec(e.ctx, node); sp == nil || sp.Enabled {
		t.Fatalf("the owner paused WARP during the restart; the node is left with %+v", sp)
	}
}

func (e *env) restart(node string) (*adminv1.RestartWarpResponse, error) {
	r, err := e.rpc().RestartWarp(e.ctx, connect.NewRequest(&adminv1.RestartWarpRequest{NodeId: node}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

// "Restart WARP" pauses, waits for the node to apply the pause, and resumes: the node's tunnel comes up again from
// scratch. The resume goes out whatever happens in between.
func TestRestartWarp(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	_, err := e.restart(node)
	e.wantErr(err, connect.CodeFailedPrecondition, "node_offline")
	e.live[node] = true
	_, err = e.restart(node)
	e.wantErr(err, connect.CodeFailedPrecondition, "no_account")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}

	e.applyAtOnce(node)
	e.kicks = 0
	r, err := e.restart(node)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Confirmed || !r.Account.Enabled || e.kicks != 2 {
		t.Fatalf("restart: %+v kicks %d", r, e.kicks)
	}
	if !contains(e.auditActions(), "warp_restart") {
		t.Errorf("audit = %v", e.auditActions())
	}

	// the node does not answer and the browser goes away right after the pause: after the polls the resume goes out anyway
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.onKick = cancel
	e.sleeps = nil
	resp, err := e.rpc().RestartWarp(ctx, connect.NewRequest(&adminv1.RestartWarpRequest{NodeId: node}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Confirmed || !resp.Msg.Account.Enabled {
		t.Fatalf("unanswered restart: %+v", resp.Msg)
	}
	if len(e.sleeps) != 80 || e.sleeps[0] != restartPoll {
		t.Errorf("waited %d x %v", len(e.sleeps), e.sleeps)
	}
	sp, _ := e.s.Spec(e.ctx, node)
	if sp == nil || !sp.Enabled {
		t.Errorf("the node is left with %+v", sp)
	}

	// a paused account is resumed, not restarted
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: false})); err != nil {
		t.Fatal(err)
	}
	_, err = e.restart(node)
	e.wantErr(err, connect.CodeFailedPrecondition, "account_paused")
}

// GetWarp names the profiles that exit through WARP on the node, with how many are connected through them: the
// pause dialog says who loses the connection.
func TestGetWarpNamesTheWarpProfiles(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ id, name, egress string }{{"w", "hy2 · WARP · 8443", "warp"}, {"d", "hy2 · 443", "direct"}, {"x", "old", "warp"}} {
		if _, err := e.st.W.ExecContext(e.ctx, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, 'hysteria2', ?, ?, 1, 1)`,
			"prf_"+p.id, p.name, fmt.Sprintf(`{"egress":%q}`, p.egress)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.W.ExecContext(e.ctx, `INSERT INTO inbound (id, profile_id, node_id, state, created_at, updated_at) VALUES (?, ?, ?, 'active', 1, 1)`,
			"inb_"+p.id, "prf_"+p.id, node); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE inbound SET enabled = 0 WHERE id = 'inb_x'`); err != nil {
		t.Fatal(err)
	}
	e.online = map[string]int{"inb_w": 3, "inb_d": 5}
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, in := range g.Msg.Inbounds {
		got = append(got, fmt.Sprintf("%s %s %d", in.InboundId, in.ProfileName, in.Online))
	}
	if strings.Join(got, ";") != "inb_w hy2 · WARP · 8443 3" {
		t.Fatalf("inbounds %v", got)
	}
}
