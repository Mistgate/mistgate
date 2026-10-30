package fleet

import (
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// UpdateIpv6Entry (ipv6_entry_rpc.go) against fake agents: the target is tcpCheckTarget's node with a doctor that lists
// 2001:db8::5, and sender1 can send over IPv6.

type entryEnv struct {
	*l3Env
	target     *agent
	targetConn *conn
	senderConn *conn
}

func newEntryEnv(t *testing.T) *entryEnv {
	t.Helper()
	x, target, targetConn := ipv6Target(t)
	_, senderConn := x.v6Peer("sender1")
	x.doctorIPv6(target.nodeID, v6Works(v6Entry+"/64"))
	return &entryEnv{l3Env: x, target: target, targetConn: targetConn, senderConn: senderConn}
}

func (e *entryEnv) call(role string, action adminv1.Ipv6EntryAction, address string) (*adminv1.UpdateIpv6EntryResponse, error) {
	ctx := auth.WithAdmin(e.ctx, store.Admin{ID: "adm_" + role, Role: role})
	r, err := nodeService{e.f}.UpdateIpv6Entry(ctx, connect.NewRequest(&adminv1.UpdateIpv6EntryRequest{NodeId: e.target.nodeID, Action: action, Address: address}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

var (
	actEnable  = adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_ENABLE
	actDisable = adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_DISABLE
)

// start runs an owner's call in the background.
func (e *entryEnv) start(action adminv1.Ipv6EntryAction, address string) <-chan entryDone {
	out := make(chan entryDone, 1)
	go func() {
		r, err := e.call(store.RoleOwner, action, address)
		out <- entryDone{r, err}
	}()
	return out
}

type entryDone struct {
	resp *adminv1.UpdateIpv6EntryResponse
	err  error
}

func (e *entryEnv) await(ch <-chan entryDone) *adminv1.UpdateIpv6EntryResponse {
	e.t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			e.t.Fatalf("UpdateIpv6Entry: %v", r.err)
		}
		return r.resp
	case <-time.After(15 * time.Second):
		e.t.Fatal("UpdateIpv6Entry did not return")
	}
	return nil
}

// passingCheck plays a check by the peer: the UDP half and the TCP half, all clean.
func (e *entryEnv) passingCheck() {
	driveUDP6(e.targetConn, e.senderConn, 300)
	probe := waitTCPProbe(e.senderConn)
	replyPortCommand(e.senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "ours", "t8443": "ours"}})
}

func (e *entryEnv) entry() string {
	e.t.Helper()
	n, err := e.st.Node(e.ctx, e.target.nodeID)
	if err != nil {
		e.t.Fatal(err)
	}
	return n.IPv6Entry
}

func (e *entryEnv) audits(action string) []string {
	e.t.Helper()
	rows, err := e.st.R.QueryContext(e.ctx, `SELECT params FROM audit WHERE action = ? ORDER BY id`, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestEnableRunsTheCheckItselfThenSwitchesOnAndAuditsIt(t *testing.T) {
	e := newEntryEnv(t)
	ch := e.start(actEnable, "")
	e.passingCheck()
	r := e.await(ch)
	if r.ErrorCode != "" || r.Node.GetIpv6().GetEntry() != v6Entry || !r.Check.GetPassed() || r.Check.GetSender() != "sender1" || r.Check.GetAddress() != v6Entry {
		t.Fatalf("response = %+v", r)
	}
	if got := e.entry(); got != v6Entry {
		t.Fatalf("stored entry %q", got)
	}
	n := r.Node.Ipv6
	if !n.EntrySupported || n.UnavailableReason != "" || n.Src != v6Entry || len(n.Addresses) != 1 || n.Addresses[0].Address != v6Entry || n.Addresses[0].PrefixLen != 64 ||
		n.LastCheck.GetCheckedUnix() == 0 || len(n.LastCheck.GetVerdicts()) != 3 {
		t.Errorf("Node.ipv6 = %+v", n)
	}
	rows := e.audits("node.ipv6_entry")
	if len(rows) != 1 || !strings.Contains(rows[0], `"action":"on"`) || !strings.Contains(rows[0], `"address":"`+v6Entry+`"`) {
		t.Fatalf("audit = %v", rows)
	}
	// the list and the single node say the same
	list := mustListNodes(t, e)
	if got := list[e.target.nodeID].GetIpv6(); got.GetEntry() != v6Entry || got.GetLastCheck().GetSender() != "sender1" {
		t.Errorf("ListNodes ipv6 = %+v", got)
	}
}

func mustListNodes(t *testing.T, e *entryEnv) map[string]*adminv1.Node {
	t.Helper()
	r, err := nodeService{e.f}.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*adminv1.Node{}
	for _, n := range r.Msg.Nodes {
		out[n.Id] = n
	}
	return out
}

func TestEnableUsesAFreshCheckAndRunsAnotherWhenItIsOld(t *testing.T) {
	e := newEntryEnv(t)
	// an "only check" first: it is kept with the node
	done := make(chan *adminv1.CheckIpv6Response, 1)
	go func() {
		r, err := nodeService{e.f}.CheckIpv6(auth.WithAdmin(e.ctx, store.Admin{ID: "adm_o", Role: store.RoleOwner}),
			connect.NewRequest(&adminv1.CheckIpv6Request{NodeId: e.target.nodeID}))
		if err != nil {
			t.Error(err)
			done <- nil
			return
		}
		done <- r.Msg
	}()
	e.passingCheck()
	if r := <-done; r == nil || !r.Passed {
		t.Fatalf("check = %+v", r)
	}
	if e.entry() != "" {
		t.Fatal("a check alone turned the entry on")
	}

	// the check is on record; the call right after it needs no packets
	n, _ := e.st.Node(e.ctx, e.target.nodeID)
	if c, ok := n.IPv6().CheckOf(v6Entry); !ok || !c.Pass || c.Sender != "sender1" {
		t.Fatalf("kept check = %+v %v", c, ok)
	}
	r := e.await(e.start(actEnable, v6Entry))
	if r.ErrorCode != "" || e.entry() != v6Entry || !r.Check.GetPassed() {
		t.Fatalf("enable with a fresh check: %+v (entry %q)", r, e.entry())
	}
	assertNoPortCommands(t, e.targetConn, e.senderConn)
	if _, err := e.call(store.RoleOwner, actDisable, ""); err != nil {
		t.Fatal(err)
	}

	// a check made nine minutes ago still counts, one made eleven minutes ago does not (the call checks again)
	old := func(age time.Duration) {
		t.Helper()
		c, _ := n.IPv6().CheckOf(v6Entry)
		c.At = e.clock.now().Add(-age).Unix()
		if err := e.st.RecordIPv6Check(e.ctx, e.target.nodeID, c); err != nil {
			t.Fatal(err)
		}
	}
	old(9 * time.Minute)
	if r := e.await(e.start(actEnable, v6Entry)); r.ErrorCode != "" || e.entry() != v6Entry {
		t.Fatalf("enable with a nine-minute-old check: %+v (entry %q)", r, e.entry())
	}
	assertNoPortCommands(t, e.targetConn, e.senderConn)
	if _, err := e.call(store.RoleOwner, actDisable, ""); err != nil {
		t.Fatal(err)
	}
	old(11 * time.Minute)
	ch := e.start(actEnable, v6Entry)
	e.passingCheck()
	if r := e.await(ch); r.ErrorCode != "" || e.entry() != v6Entry {
		t.Fatalf("enable with an old check: %+v (entry %q)", r, e.entry())
	}
}

func TestEnableThatFailsTheCheckChangesNothing(t *testing.T) {
	e := newEntryEnv(t)
	ch := e.start(actEnable, "")
	driveUDP6(e.targetConn, e.senderConn, 300)
	probe := waitTCPProbe(e.senderConn)
	replyPortCommand(e.senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "6", "t443": "ours", "t8443": "timeout"}})
	r := e.await(ch)
	if r.ErrorCode != "check_failed" || r.Check.GetPassed() || r.Node.GetIpv6().GetEntry() != "" || e.entry() != "" {
		t.Fatalf("response = %+v (entry %q)", r, e.entry())
	}
	var timeout bool
	for _, v := range r.Check.GetVerdicts() {
		timeout = timeout || (v.Port == 8443 && v.Verdict == "timeout")
	}
	if !timeout {
		t.Errorf("the verdict of the port that failed is not in the answer: %+v", r.Check)
	}
	if rows := e.audits("node.ipv6_entry"); len(rows) != 0 {
		t.Errorf("a refused change was audited as a change: %v", rows)
	}
	// the failed check is kept (the node's card shows it) but a failed one is never taken for a fresh pass
	n, _ := e.st.Node(e.ctx, e.target.nodeID)
	if c, ok := n.IPv6().CheckOf(v6Entry); !ok || c.Pass {
		t.Errorf("kept check = %+v %v", c, ok)
	}
}

func TestEnableWithNobodyToCheckIsRefusedWithoutChangingAnything(t *testing.T) {
	x, target, _ := ipv6Target(t) // nobody else is connected
	x.doctorIPv6(target.nodeID, v6Works(v6Entry+"/64"))
	e := &entryEnv{l3Env: x, target: target}
	r := e.await(e.start(actEnable, ""))
	if r.ErrorCode != "no_ipv6_verifier" || r.Node.GetIpv6().GetEntry() != "" || e.entry() != "" {
		t.Fatalf("response = %+v", r)
	}
}

func TestDisableTakesTheEntryOffAndIsIdempotent(t *testing.T) {
	e := newEntryEnv(t)
	ch := e.start(actEnable, "")
	e.passingCheck()
	e.await(ch)

	r, err := e.call(store.RoleOwner, actDisable, "")
	if err != nil || r.ErrorCode != "" || r.Node.GetIpv6().GetEntry() != "" || e.entry() != "" {
		t.Fatalf("disable: %+v %v", r, err)
	}
	if r.Node.GetIpv6().GetLastCheck().GetPassed() != true {
		t.Errorf("the last check is kept to show: %+v", r.Node.GetIpv6())
	}
	// turning off what is off, and turning on what is on, change and audit nothing more
	if _, err = e.call(store.RoleOwner, actDisable, ""); err != nil {
		t.Fatal(err)
	}
	rows := e.audits("node.ipv6_entry")
	if len(rows) != 2 || !strings.Contains(rows[1], `"action":"off"`) || !strings.Contains(rows[1], `"address":"`+v6Entry+`"`) {
		t.Fatalf("audit = %v", rows)
	}
}

func TestEnableWhileOnIsIdempotentForTheSameAddressAndRefusedForAnother(t *testing.T) {
	e := newEntryEnv(t)
	ch := e.start(actEnable, "")
	e.passingCheck()
	e.await(ch)
	r, err := e.call(store.RoleOwner, actEnable, v6Entry)
	if err != nil || r.ErrorCode != "" || e.entry() != v6Entry {
		t.Fatalf("again: %+v %v", r, err)
	}
	assertNoPortCommands(t, e.targetConn, e.senderConn)
	if rows := e.audits("node.ipv6_entry"); len(rows) != 1 {
		t.Errorf("audit = %v", rows)
	}
	e.doctorIPv6(e.target.nodeID, v6Works(v6Entry+"/64,"+v6Other+"/128"))
	if _, err = e.call(store.RoleOwner, actEnable, v6Other); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "ipv6_entry_on") {
		t.Errorf("another address while on: %v", err)
	}
	if e.entry() != v6Entry {
		t.Errorf("entry %q", e.entry())
	}
}

func TestOnlyTheOwnerChangesTheEntryAndTheActionIsChecked(t *testing.T) {
	e := newEntryEnv(t)
	for _, role := range []string{store.RoleHelper, store.RoleReadonly} {
		for _, action := range []adminv1.Ipv6EntryAction{actEnable, actDisable} {
			if _, err := e.call(role, action, ""); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Errorf("%s %v: %v, want PERMISSION_DENIED", role, action, err)
			}
		}
	}
	svc := nodeService{e.f}
	if _, err := svc.UpdateIpv6Entry(e.ctx, connect.NewRequest(&adminv1.UpdateIpv6EntryRequest{NodeId: e.target.nodeID, Action: actEnable})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("no admin: %v", err)
	}
	if _, err := e.call(store.RoleOwner, adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_UNSPECIFIED, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unspecified action: %v", err)
	}
	if _, err := e.call(store.RoleOwner, actEnable, v6Other); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an address the node does not hold: %v", err)
	}
	if e.entry() != "" {
		t.Fatal("a refused call changed the node")
	}
	missing := auth.WithAdmin(e.ctx, store.Admin{ID: "adm_o", Role: store.RoleOwner})
	if _, err := svc.UpdateIpv6Entry(missing, connect.NewRequest(&adminv1.UpdateIpv6EntryRequest{NodeId: "nod_missing", Action: actDisable})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("missing node: %v", err)
	}
}

// Two requests at once with a fresh check on record: one switches the entry on and is audited, the other finds it done.
func TestTwoEnablesAtOnceChangeTheNodeOnce(t *testing.T) {
	e := newEntryEnv(t)
	if err := e.st.RecordIPv6Check(e.ctx, e.target.nodeID, store.IPv6Check{Addr: v6Entry, At: e.clock.now().Unix(), By: "nod_x", Sender: "de1", Pass: true,
		Verdicts: []store.IPv6Verdict{{Net: "tcp", Port: 443, Verdict: "ours"}}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := e.call(store.RoleOwner, actEnable, "")
			if err == nil && r.ErrorCode != "" {
				err = connect.NewError(connect.CodeUnknown, nil)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a request failed: %v", err)
		}
	}
	if e.entry() != v6Entry {
		t.Fatalf("entry %q", e.entry())
	}
	if rows := e.audits("node.ipv6_entry"); len(rows) != 1 {
		t.Fatalf("%d audit rows for one change: %v", len(rows), rows)
	}
	assertNoPortCommands(t, e.targetConn, e.senderConn)
}

// A node that cannot take an entry says why in the list, and a node that has one can always turn it off.
func TestNodeIpv6SaysWhyTheEntryIsUnavailable(t *testing.T) {
	e := newEntryEnv(t)
	check := func(want string) {
		t.Helper()
		if got := mustListNodes(t, e)[e.target.nodeID].GetIpv6(); got.GetUnavailableReason() != want || got.GetEntrySupported() != (want == "") {
			t.Errorf("unavailable_reason %q supported %v, want %q", got.GetUnavailableReason(), got.GetEntrySupported(), want)
		}
	}
	check("")
	e.doctorIPv6(e.target.nodeID, map[string]string{"ipv6": "no"})
	check("no_ipv6")
	e.doctorIPv6(e.target.nodeID, map[string]string{"ipv6": "yes", "connect": "failed"})
	check("ipv6_broken")
	e.doctorIPv6(e.target.nodeID, map[string]string{"ipv6": "yes", "connect": "ok"})
	check("agent_old")
	e.doctorIPv6(e.target.nodeID, v6Works(v6Entry+"/64"))
	e.exec(`UPDATE node SET address = '2001:db8::1' WHERE id = ?`, e.target.nodeID)
	check("main_is_ipv6")
	// with an entry on and the IPv6 broken, turning it off still works
	e.exec(`UPDATE node SET address = '203.0.113.10', ipv6_entry = ? WHERE id = ?`, v6Entry, e.target.nodeID)
	e.doctorIPv6(e.target.nodeID, map[string]string{"ipv6": "yes", "connect": "failed"})
	if r, err := e.call(store.RoleOwner, actDisable, ""); err != nil || r.Node.GetIpv6().GetEntry() != "" {
		t.Fatalf("disable on a node whose IPv6 broke: %+v %v", r, err)
	}
}
