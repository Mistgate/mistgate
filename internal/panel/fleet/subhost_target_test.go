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
