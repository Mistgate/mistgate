package warp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestProbeMsgKeepsFailureCode(t *testing.T) {
	got := probeMsg(&agentv1.WarpProbeResult{FailureCode: "http_502"})
	if got == nil || got.FailureCode != "http_502" {
		t.Fatalf("probe failure code was lost: %v", got)
	}
}

func (e *env) register(node string) (*adminv1.RegisterWarpResponse, error) {
	r, err := e.rpc().RegisterWarp(e.ctx, connect.NewRequest(&adminv1.RegisterWarpRequest{NodeId: node, AcceptTos: true, TosUrlShown: TOSURL}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

// privateKeyOf records the account's private key as a secret the tests must never find in a log.
func (e *env) privateKeyOf(node string) string {
	e.t.Helper()
	sp, err := e.s.Spec(e.ctx, node)
	if err != nil || sp == nil {
		e.t.Fatalf("spec: %v %v", sp, err)
	}
	e.secret = append(e.secret, sp.PrivateKey)
	return sp.PrivateKey
}

func TestRegister(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")

	resp, err := e.register(node)
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Account
	if a.Source != adminv1.WarpSource_WARP_SOURCE_REGISTERED || !a.Enabled || !a.HasToken || a.AccountType != "free" ||
		a.EndpointV4 != "162.159.192.1" || a.AddressV4 != "172.16.0.2/32" || !strings.HasSuffix(a.AddressV6, "/128") ||
		a.TosUrl != TOSURL || a.TosAcceptedBy != "adm_test" || a.TosAcceptedUnix != e.now.Unix() || a.UseReserved ||
		len(a.Ports) != 4 || a.Ports[0] != 2408 || !strings.Contains(a.RegisteredWith, "v0a5641 1.1.1.1/6.38.9-5641") {
		t.Errorf("account = %+v", a)
	}
	if e.kicks != 1 {
		t.Errorf("desired state recompute asked %d times, want 1", e.kicks)
	}

	// The request is the one the Android app sends.
	if got := e.cf.callLog(); len(got) != 1 || got[0] != "POST /reg" {
		t.Fatalf("calls = %v", got)
	}
	h, body := e.cf.posts[0], e.cf.bodies[0]
	if h.Get("User-Agent") != Defaults().UserAgent || h.Get("CF-Client-Version") != "a-6.38.9-5641" ||
		h.Get("Content-Type") != "application/json; charset=UTF-8" || h.Get("Accept") != "" || h.Get("Authorization") != "" {
		t.Errorf("headers = %v", h)
	}
	if body["tunnel_type"] != "wireguard" || body["key_type"] != "curve25519" || body["tos"] != e.now.Format(time.RFC3339Nano) {
		t.Errorf("body = %v", body)
	}
	if pub, _ := body["key"].(string); !validKey(pub) {
		t.Errorf("registration key %v", body["key"])
	}

	// Stored sealed: the row carries no plaintext secret, the vault gives it back, the key matches what was registered.
	row, err := e.st.WarpAccount(e.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.SecretEnc), "SECRET") || strings.Contains(string(row.SecretEnc), "private_key") {
		t.Error("secret_enc is not sealed")
	}
	x, err := e.s.open(row)
	if err != nil || x.Token != "tok-SECRET-1" || x.RegID != "dev-SECRET-1" || x.License != "LIC-SECRET-1" || !validKey(x.PrivateKey) {
		t.Errorf("secrets %v %v", x, err)
	}
	if _, err := e.s.v.Open(row.SecretEnc, "nod_other"); err == nil {
		t.Error("secrets opened with another node id as AAD")
	}

	// Audit: who, which node, the terms URL; no secret.
	var found bool
	for _, r := range e.audits() {
		if r.Action == "warp_register" {
			found = true
			if r.Actor != "adm_test" || !strings.Contains(r.Params, TOSURL) || !strings.Contains(r.Params, node) || r.Result != "ok" {
				t.Errorf("audit row = %+v", r)
			}
		}
	}
	if !found {
		t.Errorf("no warp_register audit row in %v", e.auditActions())
	}

	// The node's WarpSpec: literals, /32 address, the private key, never the IPv6 endpoint.
	sp, err := e.s.Spec(e.ctx, node)
	if err != nil || sp == nil {
		t.Fatal(sp, err)
	}
	if !sp.Enabled || sp.PrivateKey != x.PrivateKey || sp.PeerPublicKey == "" || sp.EndpointV4 != "162.159.192.1" || sp.EndpointV6 != "" ||
		sp.AddressV4 != "172.16.0.2/32" || sp.MTU != 1280 || len(sp.Reserved) != 0 || sp.Backend != "auto" ||
		len(sp.Ports) != 4 || sp.Ports[0] != 2408 || sp.Ports[3] != 4500 {
		t.Errorf("spec = %+v", sp)
	}

	e.secret = append(e.secret, x.PrivateKey)
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if err != nil {
		t.Fatal(err)
	}
	e.noSecrets(resp, g.Msg)
}

func TestRegisterRefusals(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	old := e.addNode("old", "doctor/1") // no warp/1

	req := func(n string, accept bool, tos string) error {
		_, err := e.rpc().RegisterWarp(e.ctx, connect.NewRequest(&adminv1.RegisterWarpRequest{NodeId: n, AcceptTos: accept, TosUrlShown: tos}))
		return err
	}
	e.wantErr(req(node, false, TOSURL), connect.CodeInvalidArgument, "tos_not_accepted")
	e.wantErr(req(node, true, "http://example.com/terms"), connect.CodeInvalidArgument, "bad_tos_url")
	e.wantErr(req("", true, TOSURL), connect.CodeInvalidArgument, "node_id_required")
	e.wantErr(req("nod_nope", true, TOSURL), connect.CodeNotFound, "node_not_found")
	e.wantErr(req(old, true, TOSURL), connect.CodeFailedPrecondition, "agent too old")
	if n := len(e.cf.callLog()); n != 0 {
		t.Fatalf("Cloudflare was called %d times for refused requests", n)
	}

	// Step-up comes first: no node lookup, no call, no row.
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	e.wantErr(req(node, true, TOSURL), connect.CodePermissionDenied, "step-up required")
	if _, err := e.st.WarpAccount(e.ctx, node); !errors.Is(err, store.ErrNotFound) || len(e.cf.callLog()) != 0 {
		t.Fatalf("account after a refused step-up: %v", err)
	}
	e.stepUp = func(context.Context) error { return nil }

	// One account per node; a retired node takes none.
	if err := req(node, true, ""); err != nil { // an empty URL means the built-in one
		t.Fatal(err)
	}
	e.wantErr(req(node, true, TOSURL), connect.CodeFailedPrecondition, "account_exists")
	if n := e.cf.count("POST"); n != 1 {
		t.Errorf("POST /reg called %d times", n)
	}
	gone := e.addNode("gone")
	if err := e.st.RetireNode(e.ctx, gone, e.now); err != nil {
		t.Fatal(err)
	}
	e.wantErr(req(gone, true, TOSURL), connect.CodeFailedPrecondition, "node_retired")
}

func TestRegisterRateLimited(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")

	// 429 on every attempt: back off (1 s, 4 s), then the owner is told to import a profile; nothing is stored.
	e.cf.rate429 = 99
	_, err := e.register(node)
	e.wantErr(err, connect.CodeResourceExhausted, "cloudflare_rate_limited")
	if n := e.cf.count("POST"); n != 3 {
		t.Errorf("attempts = %d, want 1 + 2 retries", n)
	}
	if len(e.sleeps) != 2 || e.sleeps[0] != time.Second || e.sleeps[1] != 4*time.Second {
		t.Errorf("back-off = %v", e.sleeps)
	}
	if _, err := e.st.WarpAccount(e.ctx, node); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("account after 429: %v", err)
	}

	// One 429, then success: the retry makes the account.
	e.cf.mu.Lock()
	e.cf.rate429 = 1
	e.cf.mu.Unlock()
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.WarpAccount(e.ctx, node); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterKeepsGapAndCleansUp(t *testing.T) {
	e := newEnv(t)
	a, b := e.addNode("de1"), e.addNode("de2")
	if _, err := e.register(a); err != nil {
		t.Fatal(err)
	}
	if len(e.sleeps) != 0 {
		t.Fatalf("the first registration waited: %v", e.sleeps)
	}
	if _, err := e.register(b); err != nil {
		t.Fatal(err)
	}
	if len(e.sleeps) != 1 || e.sleeps[0] != 3*time.Second { // the clock did not move: the whole gap
		t.Errorf("gap before the second registration = %v", e.sleeps)
	}

	// A failure to store the account must not leave a device at Cloudflare (here the node vanishes mid-call).
	c := e.addNode("de3")
	e.cf.onRegister = func() {
		if _, err := e.st.W.ExecContext(e.ctx, `DELETE FROM node WHERE id = ?`, c); err != nil {
			t.Error(err)
		}
	}
	_, err := e.register(c)
	if err == nil {
		t.Fatal("registered for a node that is gone")
	}
	if !e.cf.reg(3).deleted {
		t.Error("the device made for the failed registration was left at Cloudflare")
	}
}

func TestImportedAndRegisteredRefresh(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	e.privateKeyOf(node)
	e.kicks = 0

	// Cloudflare moved the relay: refresh reads it back.
	e.cf.mu.Lock()
	e.cf.regs["dev-SECRET-1"].endpoint = "162.159.192.9:0"
	e.cf.mu.Unlock()
	r, err := e.rpc().RefreshWarp(e.ctx, connect.NewRequest(&adminv1.RefreshWarpRequest{NodeId: node}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Msg.Account.EndpointV4 != "162.159.192.9" || e.kicks != 1 {
		t.Errorf("after refresh: %+v, kicks %d", r.Msg.Account, e.kicks)
	}
	if got := e.cf.callLog(); got[len(got)-1] != "GET /reg/dev-SECRET-1" {
		t.Errorf("calls = %v", got)
	}
	e.noSecrets(r.Msg)

	// The node asked for it (RefreshByNode): an "attention" mark is cleared by a successful read.
	if err := e.s.NeedsAttention(e.ctx, node, "handshake_lost"); err != nil {
		t.Fatal(err)
	}
	g, _ := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if !g.Msg.NeedsAttention {
		t.Error("needs_attention not set")
	}
	if err := e.s.RefreshByNode(e.ctx, node); err != nil {
		t.Fatal(err)
	}
	g, _ = e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if g.Msg.NeedsAttention {
		t.Error("needs_attention survived a good refresh")
	}
	if !contains(e.auditActions(), "warp_refresh") {
		t.Errorf("audit = %v", e.auditActions())
	}

	// Cloudflare forgot the device: the account is marked, not touched.
	e.cf.mu.Lock()
	e.cf.regs["dev-SECRET-1"].deleted = true
	e.cf.mu.Unlock()
	_, err = e.rpc().RefreshWarp(e.ctx, connect.NewRequest(&adminv1.RefreshWarpRequest{NodeId: node}))
	e.wantErr(err, connect.CodeFailedPrecondition, "account_revoked")
	row, _ := e.st.WarpAccount(e.ctx, node)
	if row.Attention != "revoked" || row.EndpointV4 != "162.159.192.9" {
		t.Errorf("row after a revoked refresh: attention %q endpoint %q", row.Attention, row.EndpointV4)
	}
	g, _ = e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if !g.Msg.NeedsAttention {
		t.Error("a revoked account does not ask for attention")
	}

	// No account / no token.
	other := e.addNode("de2")
	_, err = e.rpc().RefreshWarp(e.ctx, connect.NewRequest(&adminv1.RefreshWarpRequest{NodeId: other}))
	e.wantErr(err, connect.CodeFailedPrecondition, "no_account")
	e.noSecrets(g.Msg)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	e.privateKeyOf(node)
	del := func(n, name string) (*adminv1.DeleteWarpResponse, error) {
		r, err := e.rpc().DeleteWarp(e.ctx, connect.NewRequest(&adminv1.DeleteWarpRequest{NodeId: n, ConfirmName: name}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	_, err := del(node, "de2")
	e.wantErr(err, connect.CodeInvalidArgument, "confirm_name_mismatch")
	if _, err := e.st.WarpAccount(e.ctx, node); err != nil {
		t.Fatal("account gone after a wrong confirmation")
	}
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	_, err = del(node, "de1")
	e.wantErr(err, connect.CodePermissionDenied, "step-up required")
	e.stepUp = func(context.Context) error { return nil }

	e.kicks = 0
	resp, err := del(node, "de1")
	if err != nil || !resp.RemoteDeleted {
		t.Fatalf("delete: %v %v", resp, err)
	}
	if !e.cf.reg(1).deleted || e.kicks != 1 {
		t.Errorf("remote deleted %v, kicks %d", e.cf.reg(1).deleted, e.kicks)
	}
	if _, err := e.st.WarpAccount(e.ctx, node); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("account after delete: %v", err)
	}
	if sp, err := e.s.Spec(e.ctx, node); sp != nil || err != nil {
		t.Errorf("spec after delete: %v %v", sp, err)
	}
	_, err = del(node, "de1")
	e.wantErr(err, connect.CodeFailedPrecondition, "no_account")
	for _, r := range e.audits() {
		if r.Action == "warp_delete" && (!strings.Contains(r.Params, `"remote_deleted":"true"`) || !strings.Contains(r.Params, TOSURL)) {
			t.Errorf("delete audit = %+v", r)
		}
	}
	if !contains(e.auditActions(), "warp_delete") {
		t.Error("no warp_delete audit row")
	}

	// Cloudflare not reachable: the local account goes anyway.
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	e.cf.srv.Close()
	resp, err = del(node, "de1")
	if err != nil || resp.RemoteDeleted {
		t.Fatalf("delete with Cloudflare down: %v %v", resp, err)
	}
	if _, err := e.st.WarpAccount(e.ctx, node); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("account after a delete with Cloudflare down: %v", err)
	}
	e.noSecrets(resp)
}

func TestPauseAndSpec(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	e.kicks = 0
	r, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: false}))
	if err != nil || r.Msg.Account.Enabled || e.kicks != 1 {
		t.Fatalf("pause: %v %v kicks %d", r, err, e.kicks)
	}
	sp, _ := e.s.Spec(e.ctx, node)
	if sp == nil || sp.Enabled || sp.PrivateKey == "" { // paused = present but disabled, the node tears the tunnel down
		t.Errorf("paused spec = %+v", sp)
	}
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: true})); err != nil {
		t.Fatal(err)
	}
	sp, _ = e.s.Spec(e.ctx, node)
	if !sp.Enabled {
		t.Error("resume did not enable")
	}
	_, err = e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: e.addNode("de2"), Enabled: true}))
	e.wantErr(err, connect.CodeFailedPrecondition, "no_account")
	if a := e.auditActions(); !contains(a, "warp_disable") || !contains(a, "warp_enable") {
		t.Errorf("audit = %v", a)
	}

	// The "reserved" bytes are stamped only when the account says so.
	row, _ := e.st.WarpAccount(e.ctx, node)
	row.UseReserved = true
	if got := specOf(row, "k").Reserved; string(got) != string([]byte{0x01, 0xb0, 0x9d}) { // "AbCd"
		t.Errorf("reserved = %x (client id %s)", got, base64.StdEncoding.EncodeToString(got))
	}
	row.ClientID = ""
	if got := specOf(row, "k").Reserved; len(got) != 0 {
		t.Errorf("reserved without a client id = %x", got)
	}
}

func TestParams(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	get := func() *adminv1.GetWarpRegistrationParamsResponse {
		r, err := e.rpc().GetWarpRegistrationParams(e.ctx, connect.NewRequest(&adminv1.GetWarpRegistrationParamsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if g := get(); g.Customised || g.Params.ApiVersion != "v0a5641" || g.Defaults.TlsSpecId != "wgcf_v2.3.0" || g.Params.AutoReregister {
		t.Errorf("defaults = %+v", g)
	}
	upd := func(p *adminv1.WarpRegistrationParams) (*adminv1.UpdateWarpRegistrationParamsResponse, error) {
		r, err := e.rpc().UpdateWarpRegistrationParams(e.ctx, connect.NewRequest(&adminv1.UpdateWarpRegistrationParamsRequest{Params: p}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	for name, p := range map[string]*adminv1.WarpRegistrationParams{
		"version":     {ApiVersion: "../../x"},
		"header crlf": {UserAgent: "a\r\nX-Evil: 1"},
		"long ua":     {UserAgent: strings.Repeat("a", 201)},
		"non ascii":   {CfClientVersion: "é"},
		"spec":        {TlsSpecId: "chrome"},
	} {
		if _, err := upd(p); err == nil {
			t.Errorf("%s: accepted", name)
		} else if c, _ := code(err); c != connect.CodeInvalidArgument {
			t.Errorf("%s: code %v", name, c)
		}
	}
	if _, err := upd(nil); err == nil {
		t.Error("nil params accepted")
	}

	// A new API version is used by the next registration (the fake only answers the version it is told).
	e.cf.mu.Lock()
	e.cf.version = "v0a9999"
	e.cf.mu.Unlock()
	if _, err := e.register(node); err == nil {
		t.Fatal("registered against a version the API does not route")
	}
	r, err := upd(&adminv1.WarpRegistrationParams{ApiVersion: "v0a9999", UserAgent: "1.1.1.1/7.0.0-1 (Android 17.0.0)"})
	if err != nil || !r.Customised || r.Params.CfClientVersion != "a-6.38.9-5641" {
		t.Fatalf("update: %+v %v", r, err)
	}
	if g := get(); !g.Customised || g.Params.ApiVersion != "v0a9999" {
		t.Errorf("after update = %+v", g)
	}
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	if ua := e.cf.posts[len(e.cf.posts)-1].Get("User-Agent"); ua != "1.1.1.1/7.0.0-1 (Android 17.0.0)" {
		t.Errorf("user agent = %q", ua)
	}
	// Empty fields in an update mean "the built-in value": this is the reset.
	if r, err := upd(&adminv1.WarpRegistrationParams{}); err != nil || r.Customised {
		t.Errorf("reset: %+v %v", r, err)
	}

	// Switching auto_reregister on accepts the terms for future accounts: step-up. Off needs none.
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	_, err = upd(&adminv1.WarpRegistrationParams{AutoReregister: true})
	e.wantErr(err, connect.CodePermissionDenied, "step-up required")
	if g := get(); g.Params.AutoReregister {
		t.Error("auto_reregister switched on without a step-up")
	}
	if _, err := upd(&adminv1.WarpRegistrationParams{}); err != nil {
		t.Errorf("an update that leaves auto_reregister off needs no step-up: %v", err)
	}
	e.stepUp = func(context.Context) error { return nil }
	if r, err := upd(&adminv1.WarpRegistrationParams{AutoReregister: true}); err != nil || !r.Params.AutoReregister {
		t.Fatalf("switch on: %+v %v", r, err)
	}
	if !contains(e.auditActions(), "warp_params") {
		t.Error("no warp_params audit row")
	}
}

func TestAutoReregister(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	try := func() bool {
		e.t.Helper()
		ok, err := e.s.AutoReregister(e.ctx, node)
		if err != nil {
			e.t.Fatal(err)
		}
		return ok
	}
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	if try() || e.cf.count("POST") != 1 {
		t.Fatal("re-registered with auto_reregister off")
	}
	if _, err := e.rpc().UpdateWarpRegistrationParams(e.ctx, connect.NewRequest(&adminv1.UpdateWarpRegistrationParamsRequest{
		Params: &adminv1.WarpRegistrationParams{AutoReregister: true}})); err != nil {
		t.Fatal(err)
	}
	if err := e.s.NeedsAttention(e.ctx, node, ""); err != nil {
		t.Fatal(err)
	}
	if try() {
		t.Fatal("re-registered an account that needs no attention")
	}
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	if !try() {
		t.Fatal("did not re-register")
	}
	row, _ := e.st.WarpAccount(e.ctx, node)
	x, _ := e.s.open(row)
	if x.Token != "tok-SECRET-2" || row.Attention != "" || row.TOSAcceptedBy != "auto" || !e.cf.reg(1).deleted || e.cf.reg(2).deleted {
		t.Errorf("after re-registration: token %q attention %q tos by %q, old deleted %v", x.Token, row.Attention, row.TOSAcceptedBy, e.cf.reg(1).deleted)
	}
	var auto bool
	for _, r := range e.audits() {
		auto = auto || r.Action == "warp_reregister" && r.Actor == "system"
	}
	if !auto {
		t.Errorf("audit = %v", e.auditActions())
	}

	// At most once an hour per node.
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	if try() {
		t.Fatal("re-registered twice within an hour")
	}
	e.now = e.now.Add(61 * time.Minute)
	if !try() {
		t.Fatal("did not re-register after the hour")
	}

	// An imported account is never replaced by a registration.
	other := e.addNode("de2")
	e.dns["engage.cloudflareclient.com"] = []netip.Addr{netip.MustParseAddr("162.159.192.1")}
	if _, err := e.rpc().ImportWarp(e.ctx, connect.NewRequest(&adminv1.ImportWarpRequest{NodeId: other, ProfileConf: wgcfProfile})); err != nil {
		t.Fatal(err)
	}
	if err := e.s.NeedsAttention(e.ctx, other, "revoked"); err != nil {
		t.Fatal(err)
	}
	before := e.cf.count("POST")
	if ok, err := e.s.AutoReregister(e.ctx, other); ok || err != nil || e.cf.count("POST") != before {
		t.Errorf("imported account: %v %v", ok, err)
	}
}

func TestStateAndSummary(t *testing.T) {
	now := time.Unix(1_791_100_000, 0)
	row := func(enabled bool, st agentv1.WarpState, age time.Duration) *store.WarpAccountRow {
		a := &store.WarpAccountRow{NodeID: "n", Source: store.WarpImported, Enabled: enabled, AccountType: "free"}
		if st != 0 {
			a.HealthJSON = `{"state":"` + st.String() + `","colo":"FRA"}`
			a.HealthAt = now.Add(-age)
		}
		return a
	}
	for _, c := range []struct {
		name   string
		a      *store.WarpAccountRow
		online bool
		want   adminv1.WarpState
	}{
		{"none", nil, true, adminv1.WarpState_WARP_STATE_NOT_CONFIGURED},
		{"paused", row(false, agentv1.WarpState_WARP_STATE_UP, 0), true, adminv1.WarpState_WARP_STATE_DISABLED},
		{"never reported", row(true, 0, 0), true, adminv1.WarpState_WARP_STATE_UNKNOWN},
		{"node offline", row(true, agentv1.WarpState_WARP_STATE_UP, time.Second), false, adminv1.WarpState_WARP_STATE_UNKNOWN},
		{"stale", row(true, agentv1.WarpState_WARP_STATE_UP, time.Hour), true, adminv1.WarpState_WARP_STATE_UNKNOWN},
		{"up", row(true, agentv1.WarpState_WARP_STATE_UP, time.Second), true, adminv1.WarpState_WARP_STATE_UP},
		{"down", row(true, agentv1.WarpState_WARP_STATE_DOWN, time.Second), true, adminv1.WarpState_WARP_STATE_DOWN},
		{"starting", row(true, agentv1.WarpState_WARP_STATE_STARTING, time.Second), true, adminv1.WarpState_WARP_STATE_STARTING},
		{"unavailable", row(true, agentv1.WarpState_WARP_STATE_UNAVAILABLE, time.Second), true, adminv1.WarpState_WARP_STATE_UNAVAILABLE},
	} {
		if got := ViewState(c.a, c.online, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	s := Summary(row(true, agentv1.WarpState_WARP_STATE_UP, time.Second), true, now)
	if s.Colo != "FRA" || s.AccountType != "free" || s.Source != adminv1.WarpSource_WARP_SOURCE_IMPORTED {
		t.Errorf("summary = %+v", s)
	}
	if s := Summary(nil, true, now); s.State != adminv1.WarpState_WARP_STATE_NOT_CONFIGURED || s.Colo != "" {
		t.Errorf("summary of none = %+v", s)
	}
}

func TestStoreHealthAndPending(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	if err := e.s.StoreHealth(e.ctx, node, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP}); err != nil {
		t.Fatalf("health of a node without an account must be ignored: %v", err)
	}
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	if err := e.s.StoreHealth(e.ctx, node, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP, Colo: "FRA", WarpFlag: "on",
		ProbeCloudflareOk: true, ProbeOtherOk: true, LastHandshakeUnix: 42, RxBytes: 7, CheckedUnix: 99,
		ProbeCloudflare: &agentv1.WarpProbeResult{Ok: false, LatencyMs: 6000, AtUnix: 99}}); err != nil {
		t.Fatal(err)
	}
	e.live[node] = true
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if err != nil {
		t.Fatal(err)
	}
	h := g.Msg.Health
	if h == nil || h.State != adminv1.WarpState_WARP_STATE_UP || h.Colo != "FRA" || h.WarpFlag != "on" || !h.ProbeOtherOk || h.RxBytes != 7 ||
		h.ReportedUnix != e.now.Unix() || !g.Msg.AgentSupports || g.Msg.TosUrl != TOSURL || g.Msg.PendingApply {
		t.Errorf("GetWarp = %+v", g.Msg)
	}
	// per-probe results pass through; an unreported probe stays unset (the card then falls back to the *_ok flags)
	if p := h.ProbeCloudflare; p == nil || p.Ok || p.LatencyMs != 6000 || p.AtUnix != 99 || h.ProbeOther != nil || h.CheckedUnix != 99 {
		t.Errorf("probe results = %+v / %+v checked %d", h.ProbeCloudflare, h.ProbeOther, h.CheckedUnix)
	}

	// Pending: the node has a stream and has not applied what the panel last sent.
	if err := e.st.NodeDesired(e.ctx, node, 5, "hash-new", []byte(`{"r":5,"h":"hash-new"}`)); err != nil {
		t.Fatal(err)
	}
	if g, _ = e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node})); !g.Msg.PendingApply {
		t.Error("not pending while desired and applied differ")
	}
	e.live[node] = false
	if g, _ = e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node})); g.Msg.PendingApply {
		t.Error("pending for an offline node")
	}

	// A node that has no account answers with an empty account and the terms link.
	g, err = e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: e.addNode("de2")}))
	if err != nil || g.Msg.Account != nil || g.Msg.TosUrl != TOSURL {
		t.Errorf("GetWarp without an account = %+v %v", g, err)
	}
	_, err = e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: "nod_nope"}))
	e.wantErr(err, connect.CodeNotFound, "node_not_found")
}

// A 4xx from Cloudflare that is
// not 429 is a rejection, a dead host is "unreachable"; neither logs a secret.
func TestCloudflareErrors(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1")
	e.cf.mu.Lock()
	e.cf.version = "v0a0000" // the client asks for v0a5641: the fake answers 404 to everything
	e.cf.mu.Unlock()
	_, err := e.register(node)
	e.wantErr(err, connect.CodeUnavailable, "cloudflare_rejected")
	e.cf.srv.Close()
	_, err = e.register(node)
	e.wantErr(err, connect.CodeUnavailable, "cloudflare_unreachable")
	if strings.Contains(e.log.String(), "SECRET") {
		t.Error("log has a secret")
	}
}
