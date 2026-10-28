package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// relayHost is the fake host of a node that can forward: it records every SetRelays and answers with err.
type relayHost struct {
	*fakeHost
	rmu         sync.Mutex
	unsupported bool
	err         error
	calls       [][]hostctl.Forward
}

func (h *relayHost) RelaySupported() bool { return !h.unsupported }

func (h *relayHost) SetRelays(_ context.Context, fs []hostctl.Forward) error {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.calls = append(h.calls, append([]hostctl.Forward(nil), fs...))
	return h.err
}

func (h *relayHost) setErr(err error) {
	h.rmu.Lock()
	h.err = err
	h.rmu.Unlock()
}

// last is the forwards of the latest SetRelays, nil when there was none.
func (h *relayHost) last() []hostctl.Forward {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	if len(h.calls) == 0 {
		return nil
	}
	return h.calls[len(h.calls)-1]
}

func (h *relayHost) callCount() int {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	return len(h.calls)
}

// relayLab is an agent that has not been started, on a host that can forward, with names that resolve from a table.
type relayLab struct {
	*harness
	host *relayHost

	mu    sync.Mutex
	names map[string]string // host name -> address ("" = does not resolve)
	asked map[string]int
	now   time.Time
}

func newRelayLab(t *testing.T) *relayLab {
	t.Helper()
	l := &relayLab{names: map[string]string{}, asked: map[string]int{}, now: time.Unix(1700000000, 0)}
	l.harness = newHarness(t, harnessOpts{
		noStart: true,
		extra:   map[string]engine.Factory{},
		wrapHost: func(f *fakeHost) hostctl.Host {
			f.ssh = []uint16{22, 2222}
			l.host = &relayHost{fakeHost: f}
			return l.host
		},
		tune: func(a *Agent) {
			a.relay.lookup = func(_ context.Context, host string) (netip.Addr, error) {
				l.mu.Lock()
				defer l.mu.Unlock()
				l.asked[host]++
				if ip := l.names[host]; ip != "" {
					return netip.MustParseAddr(ip), nil
				}
				return netip.Addr{}, errors.New("no such host")
			}
			a.relay.self = func(ip netip.Addr) bool { return ip == netip.MustParseAddr("192.0.2.99") }
			a.relay.clock = func() time.Time {
				l.mu.Lock()
				defer l.mu.Unlock()
				return l.now
			}
		},
	})
	t.Cleanup(l.a.closeEngines)
	return l
}

func (l *relayLab) setName(host, ip string) {
	l.mu.Lock()
	l.names[host] = ip
	l.mu.Unlock()
}

func (l *relayLab) advance(d time.Duration) {
	l.mu.Lock()
	l.now = l.now.Add(d)
	l.mu.Unlock()
}

func (l *relayLab) lookups(host string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked[host]
}

// sweep is what the worker does every 30 s: reconcile the applied model again.
func (l *relayLab) sweep() { l.a.reconcile(context.Background(), l.a.snapshotModel(), nil) }

func relay(id, network string, port uint32, to string, toPort uint32) *pb.RelayForward {
	return &pb.RelayForward{RelayId: id, Network: network, Port: port, ToHost: to, ToPort: toPort}
}

func relays(items ...*pb.RelayForward) *pb.RelayForwards { return &pb.RelayForwards{Items: items} }

func withRelays(ds *pb.DesiredState, r *pb.RelayForwards) *pb.DesiredState {
	ds.Relays = r
	return ds
}

func results(res *pb.ApplyResult) map[string]*pb.RelayResult {
	m := map[string]*pb.RelayResult{}
	for _, r := range res.Relays {
		m[r.RelayId] = r
	}
	return m
}

func apply(t *testing.T, l *relayLab, ds *pb.DesiredState) *pb.ApplyResult {
	t.Helper()
	res := l.a.applyDesired(context.Background(), ds)
	if res == nil {
		t.Fatal("the desired state was ignored")
	}
	return res
}

func TestRelaysAreCheckedAndReported(t *testing.T) {
	l := newRelayLab(t)
	l.setName("exit.example.com", "198.51.100.9")
	// inb_a listens on udp 443 with the hop range 20000-30000.
	ds := withRelays(fullState(1, inb("inb_a", 20000, 30000, cred("crd_a"))), relays(
		relay("rly_a_tcp", "tcp", 8443, "203.0.113.7", 443),
		relay("rly_b_named", "tcp", 9443, "exit.example.com", 443),
		relay("rly_c_udp", "udp", 9443, "203.0.113.7", 5353),
		relay("rly_d_private", "tcp", 9444, "10.0.0.5", 443),
		relay("rly_e_sshd", "tcp", 2222, "203.0.113.7", 443),
		relay("rly_f_inbound", "udp", 443, "203.0.113.7", 443),
		relay("rly_g_hop", "udp", 25000, "203.0.113.7", 443),
		relay("rly_h_dup", "tcp", 8443, "203.0.113.8", 443),
		relay("rly_i_self", "tcp", 9445, "192.0.2.99", 443),
		relay("rly_j_unresolved", "tcp", 9446, "nowhere.example.com", 443),
		relay("rly_k_low", "tcp", 80, "203.0.113.7", 443),
		relay("rly_l_v6", "tcp", 9447, "2001:db8::7", 443),
	))
	res := apply(t, l, ds)
	if res.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("status = %v (%s): a refused forward must not fail the state", res.Status, res.Error)
	}
	got := results(res)
	for id, want := range map[string]struct {
		ok   bool
		err  string // a part of it
		toIP string
	}{
		"rly_a_tcp":        {true, "", "203.0.113.7"},
		"rly_b_named":      {true, "", "198.51.100.9"},
		"rly_c_udp":        {true, "", "203.0.113.7"},
		"rly_d_private":    {false, "refused: 10.0.0.5 is not a public IPv4", "10.0.0.5"},
		"rly_e_sshd":       {false, "refused: port 2222 is the sshd port", "203.0.113.7"},
		"rly_f_inbound":    {false, "refused: udp port 443 is used by an inbound", "203.0.113.7"},
		"rly_g_hop":        {false, "refused: udp port 25000 is inside the hop range 20000-30000", "203.0.113.7"},
		"rly_h_dup":        {false, "refused: tcp port 8443 is already forwarded by rly_a_tcp", "203.0.113.8"},
		"rly_i_self":       {false, "refused: 192.0.2.99 is an address of this node", "192.0.2.99"},
		"rly_j_unresolved": {false, "resolve: no such host", ""},
		"rly_k_low":        {false, "refused: port 80 is below 1024", "203.0.113.7"},
		"rly_l_v6":         {false, "refused: 2001:db8::7 is not a public IPv4", "2001:db8::7"},
	} {
		r := got[id]
		if r == nil {
			t.Errorf("%s: no result", id)
			continue
		}
		if r.Ok != want.ok || r.ToIp != want.toIP || (want.err == "" && r.Error != "") || !strings.Contains(r.Error, want.err) {
			t.Errorf("%s: %+v, want ok=%v to_ip=%s error~%q", id, r, want.ok, want.toIP, want.err)
		}
	}
	if len(res.Relays) != 12 || !slices.IsSortedFunc(res.Relays, func(a, b *pb.RelayResult) int { return strings.Compare(a.RelayId, b.RelayId) }) {
		t.Errorf("results are not one per forward in id order: %v", res.Relays)
	}
	// The host got exactly the three that passed, resolved.
	want := []hostctl.Forward{
		{ID: "rly_a_tcp", Network: "tcp", Port: 8443, ToIP: netip.MustParseAddr("203.0.113.7"), ToPort: 443},
		{ID: "rly_b_named", Network: "tcp", Port: 9443, ToIP: netip.MustParseAddr("198.51.100.9"), ToPort: 443},
		{ID: "rly_c_udp", Network: "udp", Port: 9443, ToIP: netip.MustParseAddr("203.0.113.7"), ToPort: 5353},
	}
	if !reflect.DeepEqual(l.host.last(), want) {
		t.Errorf("the host was given %+v, want %+v", l.host.last(), want)
	}
	// The inbound is untouched by all of it, and the hash is the one the panel computes from the same message.
	if !l.eng.has("inb_a") {
		t.Error("the inbound did not start")
	}
	if res.StateHash != expectedHash(t, ds) {
		t.Errorf("state hash %s, the panel computes %s", res.StateHash, expectedHash(t, ds))
	}
	// Re-delivery of the same revision answers with the same results.
	again := l.a.applyDesired(context.Background(), &pb.DesiredState{Revision: 1, BaseRevision: 1})
	if again == nil || !reflect.DeepEqual(results(again), got) {
		t.Errorf("re-delivery: %+v", again)
	}
}

func TestRelaysLimitIsSixtyFour(t *testing.T) {
	l := newRelayLab(t)
	var items []*pb.RelayForward
	for i := 0; i < hostctl.MaxForwards+3; i++ {
		items = append(items, relay(fmt.Sprintf("rly_%03d", i), "tcp", uint32(2000+i), "203.0.113.7", 443))
	}
	res := apply(t, l, withRelays(fullState(1), relays(items...)))
	ok, refused := 0, 0
	for _, r := range res.Relays {
		if r.Ok {
			ok++
		} else if strings.Contains(r.Error, "at most 64") {
			refused++
		}
	}
	if ok != hostctl.MaxForwards || refused != 3 || len(l.host.last()) != hostctl.MaxForwards {
		t.Errorf("ok=%d refused=%d installed=%d, want 64, 3, 64", ok, refused, len(l.host.last()))
	}
}

// A FORWARD chain that drops: the host installs nothing, every forward says forward_drop, and the TCP port is NOT claimed, so
// the decoy site of a Hysteria2 inbound keeps answering on 443/tcp. Once the drop is gone the forward comes up by itself
// and takes the port.
func TestRelaysFailClosedKeepTheDecoyAndRecover(t *testing.T) {
	probe := newTCPClaimProbe()
	l := &relayLab{names: map[string]string{}, asked: map[string]int{}}
	l.harness = newHarness(t, harnessOpts{
		noStart: true,
		extra:   map[string]engine.Factory{"claims": probe.factory()},
		wrapHost: func(f *fakeHost) hostctl.Host {
			l.host = &relayHost{fakeHost: f, err: fmt.Errorf("%w: nft table filter drops forwarded packets by default", hostctl.ErrForwardDrop)}
			return l.host
		},
	})
	t.Cleanup(l.a.closeEngines)

	decoyHolder := inb("inb_h", 0, 0, cred("crd_h")) // the engine that owns a decoy on 443/tcp
	decoyHolder.Spec.Protocol = "claims"
	decoyHolder.Spec.Listen.Network = "udp"
	decoyHolder.Spec.Listen.Port = 8444
	ds := withRelays(fullState(1, decoyHolder), relays(
		relay("rly_t", "tcp", 443, "203.0.113.7", 443),
		relay("rly_u", "udp", 9443, "203.0.113.7", 5353),
	))
	res := apply(t, l, ds)
	for _, r := range res.Relays {
		if r.Ok || r.Error != "forward_drop" || r.ToIp != "203.0.113.7" {
			t.Errorf("%+v: want a forward_drop failure that still names the address", r)
		}
	}
	if claims := lastClaims(probe); len(claims) != 0 {
		t.Errorf("claims = %v while the forwards failed: the decoy would lose 443/tcp for nothing", claims)
	}
	if res.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || !probe.fakeEngine.has("inb_h") {
		t.Errorf("a failed forward must not touch the inbound: status %v", res.Status)
	}

	// The next sweep, with the drop gone.
	l.host.setErr(nil)
	l.sweep()
	for _, r := range l.a.relay.results {
		if !r.Ok || r.Error != "" {
			t.Errorf("after the drop is gone: %+v", r)
		}
	}
	if claims := lastClaims(probe); !reflect.DeepEqual(claims, map[uint16]string{443: "relay:rly_t"}) {
		t.Errorf("claims = %v, want 443 held by the TCP forward only (UDP has no decoy)", claims)
	}

	// And the forward is removed by a delta that sends an empty list: the table goes, the decoy gets 443 back.
	res = apply(t, l, &pb.DesiredState{Revision: 2, BaseRevision: 1, Relays: &pb.RelayForwards{}})
	if len(res.Relays) != 0 || len(l.host.last()) != 0 {
		t.Errorf("an empty list left %v results and %v on the host", res.Relays, l.host.last())
	}
	if claims := lastClaims(probe); len(claims) != 0 {
		t.Errorf("claims = %v after the forwards are gone", claims)
	}
}

func lastClaims(p *tcpClaimProbe) map[uint16]string {
	var last map[uint16]string
	for _, c := range p.recordedCalls() {
		if c.kind == "claims" {
			last = c.claims
		}
	}
	return last
}

func TestRelaysKeepTheLastGoodAddressWhenResolvingFails(t *testing.T) {
	l := newRelayLab(t)
	l.setName("exit.example.com", "198.51.100.9")
	apply(t, l, withRelays(fullState(1), relays(relay("rly_a", "tcp", 8443, "exit.example.com", 443))))
	only := func() hostctl.Forward {
		t.Helper()
		fs := l.host.last()
		if len(fs) != 1 {
			t.Fatalf("host has %v", fs)
		}
		return fs[0]
	}
	if only().ToIP.String() != "198.51.100.9" || l.lookups("exit.example.com") != 1 {
		t.Fatalf("first resolve: %v, %d lookups", only().ToIP, l.lookups("exit.example.com"))
	}

	// Every sweep within five minutes is served from what was resolved.
	l.advance(4 * time.Minute)
	l.sweep()
	if l.lookups("exit.example.com") != 1 {
		t.Errorf("looked up again after 4 minutes: %d", l.lookups("exit.example.com"))
	}

	// After five minutes the name is looked up again, and a new address replaces the old one.
	l.setName("exit.example.com", "198.51.100.10")
	l.advance(2 * time.Minute)
	l.sweep()
	if only().ToIP.String() != "198.51.100.10" || l.lookups("exit.example.com") != 2 {
		t.Errorf("re-resolve: %v, %d lookups", only().ToIP, l.lookups("exit.example.com"))
	}

	// The resolver dies: the last good address stays, the forward stays ok, and it is not hammered.
	l.setName("exit.example.com", "")
	l.advance(6 * time.Minute)
	l.sweep()
	if only().ToIP.String() != "198.51.100.10" {
		t.Errorf("the address was dropped when the lookup failed: %v", only().ToIP)
	}
	if r := l.a.relay.results[0]; !r.Ok || r.ToIp != "198.51.100.10" || r.Error != "" {
		t.Errorf("result after a failed re-resolve: %+v", r)
	}
	n := l.lookups("exit.example.com")
	l.advance(10 * time.Second)
	l.sweep()
	l.sweep()
	if l.lookups("exit.example.com") != n {
		t.Errorf("a failing name was looked up on every sweep: %d -> %d", n, l.lookups("exit.example.com"))
	}
	// It is tried again after a minute, and recovers.
	l.setName("exit.example.com", "198.51.100.11")
	l.advance(time.Minute)
	l.sweep()
	if only().ToIP.String() != "198.51.100.11" {
		t.Errorf("did not recover: %v", only().ToIP)
	}
}

func TestRelayThatNeverResolvedFailsAndIsRetried(t *testing.T) {
	l := newRelayLab(t)
	res := apply(t, l, withRelays(fullState(1), relays(relay("rly_a", "tcp", 8443, "late.example.com", 443))))
	if r := results(res)["rly_a"]; r.Ok || !strings.Contains(r.Error, "resolve: ") || r.ToIp != "" {
		t.Fatalf("result: %+v", r)
	}
	if len(l.host.last()) != 0 {
		t.Fatalf("an unresolved forward reached the host: %v", l.host.last())
	}
	// A new revision within the minute does not ask again; the sweep after it does, and the forward comes up.
	apply(t, l, &pb.DesiredState{Revision: 2, BaseRevision: 1})
	if l.lookups("late.example.com") != 1 {
		t.Errorf("%d lookups, want 1 within the retry period", l.lookups("late.example.com"))
	}
	l.setName("late.example.com", "203.0.113.30")
	l.advance(time.Minute + time.Second)
	l.sweep()
	if r := l.a.relay.results[0]; !r.Ok || r.ToIp != "203.0.113.30" {
		t.Errorf("after the retry: %+v", r)
	}
}

// The forwards are part of the persisted state: an agent that starts without the panel installs them, resolving its names
// itself, and does not wait for anyone.
func TestRelaysStartFromPersistedState(t *testing.T) {
	l := newRelayLab(t)
	l.setName("exit.example.com", "198.51.100.9")
	m := newModel()
	m.revision = 5
	m.relays = []plugin.RelayForward{
		{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443},
		{ID: "rly_b", Network: "udp", Port: 9443, ToHost: "exit.example.com", ToPort: 5353},
	}
	if err := saveState(l.dir, m); err != nil {
		t.Fatal(err)
	}
	l.a.restore(context.Background())
	want := []hostctl.Forward{
		{ID: "rly_a", Network: "tcp", Port: 8443, ToIP: netip.MustParseAddr("203.0.113.7"), ToPort: 443},
		{ID: "rly_b", Network: "udp", Port: 9443, ToIP: netip.MustParseAddr("198.51.100.9"), ToPort: 5353},
	}
	if !reflect.DeepEqual(l.host.last(), want) {
		t.Errorf("restored: the host was given %+v, want %+v", l.host.last(), want)
	}
	if cur := l.a.snapshotModel(); cur.revision != 5 || len(cur.relays) != 2 {
		t.Errorf("model after restore: revision %d, %d relays", cur.revision, len(cur.relays))
	}
	// What was saved is what is read back, hash included.
	l.a.persistModel(l.a.snapshotModel(), false)
	back, err := loadState(l.dir, l.a.knows)
	if err != nil || !reflect.DeepEqual(back.relays, m.relays) || back.hash() != m.hash() {
		t.Errorf("state file round trip: %v %+v", err, back)
	}
}

func TestRelaysOnAHostThatCannotForward(t *testing.T) {
	// The plain fake host has no RelayHost at all (the stub of other systems); an unsupported Linux host says so too.
	h := newHarness(t, harnessOpts{noStart: true})
	t.Cleanup(h.a.closeEngines)
	res := h.a.applyDesired(context.Background(), withRelays(fullState(1, inb("inb_a", 0, 0, cred("crd_a"))), relays(relay("rly_a", "tcp", 8443, "203.0.113.7", 443))))
	if r := results(res)["rly_a"]; r == nil || r.Ok || r.Error != "unsupported_host" {
		t.Errorf("result: %+v", r)
	}
	if res.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || !h.eng.has("inb_a") {
		t.Errorf("the inbound must run all the same: %v", res.Status)
	}

	l := newRelayLab(t)
	l.host.unsupported = true
	res = apply(t, l, withRelays(fullState(1), relays(relay("rly_a", "tcp", 8443, "203.0.113.7", 443))))
	if r := results(res)["rly_a"]; r == nil || r.Ok || r.Error != "unsupported_host" || l.host.callCount() != 0 {
		t.Errorf("unsupported host: %+v, %d calls", r, l.host.callCount())
	}
}

func TestRelayCapabilityOnlyWithAHostThatForwards(t *testing.T) {
	plain := newHarness(t, harnessOpts{noStart: true})
	t.Cleanup(plain.a.closeEngines)
	if slices.Contains(plain.a.capabilities(), plugin.CapRelayForward) {
		t.Error("a host that cannot forward announces the capability")
	}
	l := newRelayLab(t)
	if !slices.Contains(l.a.capabilities(), "relay-forward/1") {
		t.Errorf("capabilities = %v", l.a.capabilities())
	}
	l.host.unsupported = true
	if slices.Contains(l.a.capabilities(), "relay-forward/1") {
		t.Error("a host without nft announces the capability")
	}
}

// DesiredState.relays: presence replaces the list (an empty list too), absence in a delta keeps it, absence in a full state
// clears it. The hash follows.
func TestRelayListMergeSemantics(t *testing.T) {
	known := func(string) bool { return true }
	two := relays(relay("rly_b", "udp", 443, "203.0.113.7", 5353), relay("rly_a", "tcp", 8443, "203.0.113.7", 443))

	full, err := merge(newModel(), withRelays(&pb.DesiredState{Revision: 1}, two), known)
	if err != nil || len(full.relays) != 2 || full.relays[0].ID != "rly_a" {
		t.Fatalf("full state: %v %+v (want sorted by id)", err, full.relays)
	}
	// A delta without the field keeps the list.
	kept, err := merge(full, &pb.DesiredState{Revision: 2, BaseRevision: 1}, known)
	if err != nil || !reflect.DeepEqual(kept.relays, full.relays) {
		t.Fatalf("delta without relays: %v %+v", err, kept.relays)
	}
	// A delta with an empty list clears it; that is not the same message as the one above.
	cleared, err := merge(full, &pb.DesiredState{Revision: 2, BaseRevision: 1, Relays: &pb.RelayForwards{}}, known)
	if err != nil || len(cleared.relays) != 0 {
		t.Fatalf("delta with an empty list: %v %+v", err, cleared.relays)
	}
	// A delta with a list replaces it whole.
	replaced, err := merge(full, &pb.DesiredState{Revision: 2, BaseRevision: 1, Relays: relays(relay("rly_c", "tcp", 9443, "203.0.113.9", 443))}, known)
	if err != nil || len(replaced.relays) != 1 || replaced.relays[0].ID != "rly_c" {
		t.Fatalf("delta with a list: %v %+v", err, replaced.relays)
	}
	// A full state without the field means no forwards.
	fresh, err := merge(full, &pb.DesiredState{Revision: 3}, known)
	if err != nil || len(fresh.relays) != 0 {
		t.Fatalf("full state without relays: %v %+v", err, fresh.relays)
	}
	// The input model is never modified.
	if len(full.relays) != 2 {
		t.Error("merge changed the model it was given")
	}

	// The hash: no forwards is the hash from before forwards existed; forwards move it; the panel's function agrees.
	if kept.hash() == cleared.hash() || cleared.hash() != fresh.hash() {
		t.Error("hash: the list is not part of it, or an empty list differs from none")
	}
	if fresh.hash() != statehash.StateWarp(nil, nil) || fresh.hash() != statehash.State(nil) {
		t.Error("hash without forwards changed")
	}
	if full.hash() != statehash.StateRelays(nil, nil, []plugin.RelayForward{
		{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443},
		{ID: "rly_b", Network: "udp", Port: 443, ToHost: "203.0.113.7", ToPort: 5353},
	}) {
		t.Error("the model hash is not the shared one")
	}

	// What cannot be represented rejects the message and changes nothing.
	for name, bad := range map[string]*pb.RelayForwards{
		"no id":        relays(relay("", "tcp", 8443, "203.0.113.7", 443)),
		"nil item":     {Items: []*pb.RelayForward{nil}},
		"repeated id":  relays(relay("rly_a", "tcp", 8443, "203.0.113.7", 443), relay("rly_a", "udp", 8444, "203.0.113.7", 443)),
		"port too big": relays(relay("rly_a", "tcp", 70000, "203.0.113.7", 443)),
		"to_port big":  relays(relay("rly_a", "tcp", 8443, "203.0.113.7", 65536)),
	} {
		if _, err := merge(full, &pb.DesiredState{Revision: 2, BaseRevision: 1, Relays: bad}, known); !errors.Is(err, errRejected) {
			t.Errorf("%s: err = %v, want a rejection", name, err)
		}
	}
}

// What the agent persists carries the list, and a state without one stays byte for byte what it was.
func TestRelaySnapshot(t *testing.T) {
	m := newModel()
	m.revision = 3
	if m.snapshot().Relays != nil {
		t.Error("a model without forwards persists a list")
	}
	m.relays = []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443}}
	if got := m.snapshot().Relays; got == nil || len(got.Items) != 1 || got.Items[0].RelayId != "rly_a" || got.Items[0].ToPort != 443 {
		t.Errorf("snapshot relays = %v", got)
	}
}
