package fleet

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// The panel's own TLS check of a REALITY port is a handshake with the profile's name and no key, which REALITY counts as a wrong
// key. Those checks must not show up as "wrong REALITY key"; a stranger's attempt still does.
func TestOwnTLSProbesOfARealityPortAreNotWrongKeys(t *testing.T) {
	x, target, targetConn := tcpCheckTarget(t)
	x.f.probeTCP = func(context.Context, string, []tcpcheck.Target) (string, map[uint16]string, error) {
		return "4", map[uint16]string{80: "ours", 443: "ours", 8443: "ours"}, nil
	}
	node, err := x.st.Node(x.ctx, target.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	const checks = 3
	for range checks { // the panel is the only sender here; each run arms port 80 first
		done := make(chan struct{})
		go func() {
			defer close(done)
			x.f.runTCPCheck(x.ctx, node, true)
		}()
		mark := waitHTTPMark(targetConn)
		replyPortCommand(targetConn, mark.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"http01": "serving"}})
		<-done
	}

	// The node counted every check (a hello, a fallback, a failed authentication) and one stranger with a wrong key, plus a
	// stranger that asked for another name (a fallback only).
	targetConn.send(1, linksBatch(&agentv1.LinkCounters{InboundId: "inb_vless", Hellos: checks + 2, RealityFallback: checks + 2, AuthFailed: checks + 1}))
	targetConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 1 })
	sum := func(column string) int64 {
		return x.count(`SELECT COALESCE(SUM(` + column + `), 0) FROM link_hour WHERE inbound_id = 'inb_vless' AND cred_id = ''`)
	}
	if got := sum("auth_failed"); got != 1 {
		t.Errorf("auth_failed = %d after %d own checks and one stranger, want 1", got, checks)
	}
	if got := sum("reality_fallback"); got != 2 {
		t.Errorf("reality_fallback = %d, want 2 (the stranger's and the other name's)", got)
	}
	if got := sum("hellos"); got != checks+2 {
		t.Errorf("hellos = %d, want %d (a hello is a hello)", got, checks+2)
	}

	// The checks are used up: the next wrong keys all count.
	targetConn.send(2, linksBatch(&agentv1.LinkCounters{InboundId: "inb_vless", RealityFallback: 2, AuthFailed: 2}))
	targetConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 2 })
	if got := sum("auth_failed"); got != 3 {
		t.Errorf("auth_failed = %d after two more strangers, want 3", got)
	}
}

// A check that never reached the node leaves nothing to take off a stranger's attempt later.
func TestOwnProbeCreditsExpireAndAreNeverMoreThanAskedFor(t *testing.T) {
	var o ownProbes
	t0 := time.Unix(1_800_000_000, 0)
	o.add("inb_a", t0)
	o.add("inb_a", t0.Add(time.Minute))
	if got := o.take("inb_a", 1, t0.Add(2*time.Minute+30*time.Second)); got != 1 {
		t.Errorf("took %d, want 1: only the second check is still fresh", got)
	}
	if got := o.take("inb_a", 5, t0.Add(2*time.Minute+30*time.Second)); got != 0 {
		t.Errorf("took %d, want 0: the second check is used up", got)
	}
	if got := o.take("inb_b", 5, t0); got != 0 {
		t.Errorf("took %d for an inbound that was never checked", got)
	}
	for range 3 * ownProbeMax {
		o.add("inb_c", t0)
	}
	if got := o.take("inb_c", 1_000_000, t0); got != ownProbeMax {
		t.Errorf("took %d, want the cap %d", got, ownProbeMax)
	}
}
