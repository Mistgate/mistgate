package fleet

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// node.update records which fields the request carried, and the values of the short non-secret settings only.
func TestUpdateNodeAuditNamesTheFields(t *testing.T) {
	x, a := newL3Env(t)
	service := nodeService{x.f}
	owner := auth.WithAdmin(x.ctx, store.Admin{ID: "adm_alice", Role: store.RoleOwner})
	_, err := service.UpdateNode(owner, connect.NewRequest(&adminv1.UpdateNodeRequest{
		NodeId: a.nodeID, DnsResolvers: &adminv1.DnsResolvers{Values: []string{"203.0.113.9"}}, Notes: ptr("moved"),
		BandwidthMbps: ptr(uint32(500)), TorrentBlockerEnabled: ptr(false),
	}))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := x.st.ListAuditQuery(x.ctx, store.AuditQuery{Actions: []string{"node.update"}, Limit: 5})
	if err != nil || len(rows) == 0 {
		t.Fatalf("audit rows = %v, %v", rows, err)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(rows[0].Params), &params); err != nil {
		t.Fatal(err)
	}
	if params["fields"] != "notes,dns_resolvers,torrent_blocker_enabled,bandwidth_mbps" || params["bandwidth_mbps"] != "500" || params["torrent_blocker"] != "off" {
		t.Fatalf("params = %v", params)
	}
	for k, v := range params {
		if v == "203.0.113.9" || v == "moved" {
			t.Fatalf("%s carries a value that should stay out of the audit: %q", k, v)
		}
	}
}
