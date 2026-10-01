package access

import (
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"strings"
	"testing"
)

// A profile whose settings cannot be built must not silently vanish from the node: the inbound is
// marked failed with the reason, so the node page shows it, and the healthy inbounds keep working.
func TestDesiredSurfacesBuildFailure(t *testing.T) {
	f := newFixture(t)
	e := f.e
	good := e.profile("hy2 good", "")
	goodIn := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: good.Id, NodeId: f.nodeID, PortOverride: 8443}))).Msg.Inbound

	// Corrupt the first profile behind the plugin's back (valid JSON, but not an object the plugin accepts).
	e.sql(`UPDATE profile SET settings_json = '[1]' WHERE id = ?`, f.profile)

	ins := must(e.s.Desired(e.ctx, f.nodeID))
	if len(ins) != 1 || ins[0].Spec.ID != goodIn.Id {
		t.Fatalf("desired = %d inbounds, want only the healthy one (%s)", len(ins), goodIn.Id)
	}
	bad := must(e.st.Access().Inbound(e.ctx, f.inbound))
	if bad.State != "failed" || !strings.Contains(bad.LastError, "cannot build inbound") {
		t.Fatalf("broken inbound state=%q last_error=%q, want failed + reason", bad.State, bad.LastError)
	}
	ok := must(e.st.Access().Inbound(e.ctx, goodIn.Id))
	if ok.State == "failed" {
		t.Fatalf("healthy inbound was marked failed: %q", ok.LastError)
	}

	// The same error again is not written again (Desired runs on every change and stats tick).
	e.sql(`UPDATE inbound SET updated_at = 12345 WHERE id = ?`, f.inbound)
	must(e.s.Desired(e.ctx, f.nodeID))
	if again := must(e.st.Access().Inbound(e.ctx, f.inbound)); again.UpdatedAt.Unix() != 12345 {
		t.Fatalf("an unchanged build error rewrote the row (updated_at %v)", again.UpdatedAt)
	}
}
