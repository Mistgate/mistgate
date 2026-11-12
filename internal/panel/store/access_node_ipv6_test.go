//go:build !js

package store

import (
	"context"
	"testing"
)

// The access view of a node carries what the client API needs for ipv6_egress, read in the same query as the node: the switch, the
// doctor's ipv6 check and whether an enabled WARP account has an IPv6 address.
func TestAccessNodeIPv6Facts(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_1', 'de1', 'de1.example.com', 'active', 1)`)
	n, err := st.Access().Node(ctx, "nod_1")
	if err != nil {
		t.Fatal(err)
	}
	if !n.ClientIPv6 || n.IPv6.State != "" || n.IPv6.Works() || n.WarpIPv6 {
		t.Fatalf("a node nothing is known of: %+v", n)
	}
	exec(`UPDATE node SET client_ipv6 = 0 WHERE id = 'nod_1'`)
	exec(`INSERT INTO doctor_result (node_id, check_id, status, params_json, received_unix) VALUES ('nod_1', 'ipv6', 1, '{"ipv6":"yes","connect":"ok","addrs":"2001:db8::5/64"}', 1)`)
	exec(`INSERT INTO warp_account (node_id, source, secret_enc, peer_public_key, endpoint_v4, address_v4, address_v6, enabled, created_at, updated_at)
		VALUES ('nod_1', 'imported', X'01', 'peer', '203.0.113.1', '172.16.0.2/32', '2606:4700:110:8a::2/128', 1, 1, 1)`)
	nodes, err := st.Access().Nodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes: %v %v", nodes, err)
	}
	if n := nodes[0]; n.ClientIPv6 || !n.IPv6.Works() || !n.WarpIPv6 {
		t.Errorf("after the writes: %+v", n)
	}
	exec(`UPDATE warp_account SET enabled = 0`)
	exec(`UPDATE doctor_result SET params_json = '{"ipv6":"yes","connect":"failed"}'`)
	if n, _ := st.Access().Node(ctx, "nod_1"); n.WarpIPv6 || n.IPv6.State != IPv6Broken {
		t.Errorf("a disabled account and a broken uplink: %+v", n)
	}
}
