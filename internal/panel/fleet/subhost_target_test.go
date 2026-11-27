package fleet

import (
	"context"
	"strings"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The agent's own site is verified by the install's pin until the host reports one, then by the host's pin, then, once ACME
// issued, by its public certificate; the subdomain host's report is kept as the last one.
func TestSubHostTargetOfTheAgentsSite(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("host")
	if _, err := e.f.st.W.ExecContext(context.Background(), `UPDATE node SET address = '203.0.113.7' WHERE id = ?`, a.nodeID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bootstrap, reported := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if _, _, _, _, ok := e.f.SubHostTarget(ctx, a.nodeID, "site.example.com", ""); ok {
		t.Fatal("nothing to verify the host by: no dial")
	}
	host, sni, pin, public, ok := e.f.SubHostTarget(ctx, a.nodeID, "site.example.com", bootstrap)
	if !ok || host != "203.0.113.7" || sni != "site.example.com" || pin != bootstrap || public {
		t.Fatalf("bootstrap: %q %q %q %v %v", host, sni, pin, public, ok)
	}
	e.f.recordSubHost(a.nodeID, &agentv1.SubHostStatus{SitePinSha256: reported})
	if _, _, pin, _, _ := e.f.SubHostTarget(ctx, a.nodeID, "site.example.com", bootstrap); pin != reported {
		t.Fatalf("the reported pin must win: %q", pin)
	}
	e.f.recordSubHost(a.nodeID, &agentv1.SubHostStatus{SitePinSha256: reported, SiteCertPublic: true})
	if _, _, pin, public, ok := e.f.SubHostTarget(ctx, a.nodeID, "site.example.com", bootstrap); pin != "" || !public || !ok {
		t.Fatal("a public certificate is verified by the system roots, not a pin")
	}
	e.f.recordSubHost(a.nodeID, nil)
	if e.f.SubHostStatus(a.nodeID) != nil {
		t.Fatal("a node that reports no role has no status")
	}
}

// An agent that reconnects through the same subdomain: the old session unwinds after the new one noted its host, and must not
// erase the new session's entry (two equal host names are still two sessions).
func TestSessionHostSurvivesTheSessionItReplaced(t *testing.T) {
	e := newEnv(t)
	old := e.f.noteLinkHost("nod_a", "sub.example.com:443")
	replaced := e.f.noteLinkHost("nod_a", "Sub.Example.com.")
	old()
	if got := e.f.SessionHost("nod_a"); got != "sub.example.com" {
		t.Fatalf("after the old session ended: %q", got)
	}
	replaced()
	if got := e.f.SessionHost("nod_a"); got != "" {
		t.Fatalf("after the live session ended: %q", got)
	}
}
