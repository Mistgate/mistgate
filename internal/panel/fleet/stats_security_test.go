package fleet

import (
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func securityStatsFixture(t *testing.T) (*env, *agent, string, string) {
	t.Helper()
	e := newEnv(t)
	de1 := e.enroll("de1")
	now := time.Now().Unix()
	e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nl1', 'nl1', 'nl1.example.com', 'active', ?)`, now)
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_security', 'security', ?)`, now)
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_security', 'fakehy', 'security', '{"port":443}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO user_group_profile (group_id, profile_id) VALUES ('grp_security', 'prf_security')`)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at)
		VALUES ('inb_de1', 'prf_security', ?, ?, ?), ('inb_nl1', 'prf_security', 'nl1', ?, ?)`, de1.nodeID, now, now, now, now)
	e.addUser(testUser{name: "alice", group: "grp_security", happ: true, all: true, speed: 1_000_000})
	e.addUser(testUser{name: "bob", group: "grp_security", happ: true, nodes: []string{"nl1"}})
	return e, de1, "inb_de1", "inb_nl1"
}

func sendSecurityStats(t *testing.T, c *conn, seq uint64, deltas ...*agentv1.TrafficDelta) {
	t.Helper()
	now := time.Now().Unix()
	c.send(seq, statsBatch(now-10, now, deltas, nil))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= seq })
}

func TestStatsBookOnlyUsersOfThisNode(t *testing.T) {
	e, de1, inDe1, _ := securityStatsFixture(t)
	c, _, _ := connectFull(de1, "stats-security")
	// Alice is on every node, bob only on nl1: de1 cannot book bob's credential, whatever it reports.
	sendSecurityStats(t, c, 1,
		&agentv1.TrafficDelta{CredId: "crd_alice_hy", InboundId: inDe1, BytesUp: 1000},
		&agentv1.TrafficDelta{CredId: "crd_bob_hy", InboundId: inDe1, BytesUp: 123},
	)
	if got := e.count(`SELECT used_bytes FROM user WHERE id = 'usr_bob'`); got != 0 {
		t.Fatalf("foreign bob credential booked %d bytes on de1", got)
	}
	if got := e.count(`SELECT used_bytes FROM user WHERE id = 'usr_alice'`); got != 1000 {
		t.Fatalf("alice's credential booked %d bytes, want 1000", got)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_rejected'`, de1.nodeID); got != 1 {
		t.Fatalf("foreign credential events = %d, want one", got)
	}
	// Once a minute: a second foreign batch right after adds no second event.
	sendSecurityStats(t, c, 2, &agentv1.TrafficDelta{CredId: "crd_bob_hy", InboundId: inDe1, BytesUp: 1})
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_rejected'`, de1.nodeID); got != 1 {
		t.Fatalf("foreign credential events = %d after a second batch, want still one", got)
	}
}

// A person who was just disabled, a device that was just revoked and a person moved off the node still have the last batch
// of their traffic booked, and none of it is an anomaly for the journal.
func TestStatsTailOfRemovedAccessIsBookedWithoutEvents(t *testing.T) {
	e, de1, inDe1, _ := securityStatsFixture(t)
	c, _, _ := connectFull(de1, "stats-tail")
	now := time.Now().Unix()
	e.exec(`UPDATE user SET status = 'disabled', disabled = 1 WHERE id = 'usr_alice'`)
	e.exec(`UPDATE device_credential SET revoked_at = ? WHERE id = 'crd_alice_hy'`, now)
	e.exec(`UPDATE device SET revoked_at = ? WHERE id = 'dev_alice'`, now)
	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, inDe1)
	sendSecurityStats(t, c, 1, &agentv1.TrafficDelta{CredId: "crd_alice_hy", InboundId: inDe1, BytesUp: 700})
	if got := e.count(`SELECT used_bytes FROM user WHERE id = 'usr_alice'`); got != 700 {
		t.Fatalf("tail of a disabled person booked %d bytes, want 700", got)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_rejected'`, de1.nodeID); got != 0 {
		t.Fatalf("an ordinary removal made %d stats_rejected events, want none", got)
	}
	// Bob never was on de1: his credential is refused. Once the panel has accepted traffic of his there (as if he had been
	// moved off de1 since), his tail counts.
	sendSecurityStats(t, c, 2, &agentv1.TrafficDelta{CredId: "crd_bob_hy", InboundId: inDe1, BytesUp: 50})
	if got := e.count(`SELECT used_bytes FROM user WHERE id = 'usr_bob'`); got != 0 {
		t.Fatalf("bob, never on de1, booked %d bytes", got)
	}
	e.exec(`INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES ('usr_bob', ?, 'fakehy', ?, 1, 1)`,
		de1.nodeID, now-now%3600)
	sendSecurityStats(t, c, 3, &agentv1.TrafficDelta{CredId: "crd_bob_hy", InboundId: inDe1, BytesUp: 60})
	if got := e.count(`SELECT used_bytes FROM user WHERE id = 'usr_bob'`); got != 60 {
		t.Fatalf("bob's tail after being on de1 booked %d bytes, want 60", got)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_rejected'`, de1.nodeID); got != 1 {
		t.Fatalf("stats_rejected events = %d, want 1 (bob's foreign batch only)", got)
	}
}

// Alice's limit is 1 Mbps. The ceiling is twice the limit over the interval plus 16 MiB: an honest burst passes whole, and a
// delta above it is counted at the ceiling instead of being dropped.
func TestStatsAboveSpeedLimitCeilingIsCappedNotDropped(t *testing.T) {
	e, de1, inDe1, _ := securityStatsFixture(t)
	c, _, _ := connectFull(de1, "stats-ceiling")
	alice := func() int64 { return e.count(`SELECT used_bytes FROM user WHERE id = 'usr_alice'`) }
	sendSecurityStats(t, c, 1, &agentv1.TrafficDelta{CredId: "crd_alice_hy", InboundId: inDe1, BytesUp: 1_250_001})
	if got := alice(); got != 1_250_001 {
		t.Fatalf("one byte above the limit over ten seconds booked %d, want all of it", got)
	}
	const ceiling = 2*1_250_000 + 16<<20
	sendSecurityStats(t, c, 2, &agentv1.TrafficDelta{CredId: "crd_alice_hy", InboundId: inDe1, BytesUp: 1 << 30})
	if got := alice() - 1_250_001; got != ceiling {
		t.Fatalf("1 GiB over ten seconds booked %d, want the ceiling %d", got, ceiling)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_rejected'`, de1.nodeID); got != 0 {
		t.Fatalf("a capped delta made %d stats_rejected events, want none", got)
	}
}
