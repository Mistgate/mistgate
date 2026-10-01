package fleet

import (
	"fmt"
	"time"
)

// Fixture layout (node A is the enrolled fake agent, node B a database-only node):
//
//	groups   g1 = {P1 fakehy (happ), P2 fakewg (amnezia)}   g2 = {P1}
//	inbounds I1 = P1@A, I2 = P2@A, I3 = P1@B
//	users    alice  g1 all nodes, both apps
//	         bob    g1 node B only                     -> never on A
//	         carol  g1 disabled                        -> excluded
//	         dave   g1 expired                         -> excluded
//	         erin   g1 happ only, expires in 1000 s    -> I1 with valid_until
//	         frank  g1 amnezia only, speed limit       -> I2 with rate limit
//	         gina   g2 (group has no P2)               -> I1 only
//	         hank   g1 both apps, quota 1000 bytes     -> I1, I2
//
// alice also has a revoked credential that must never be served.
type fixtureIDs struct {
	nodeA, nodeB string
	i1, i2, i3   string
	erinExpires  int64
}

type testUser struct {
	name, group        string
	disabled           bool
	status             string
	happ, amnezia, all bool
	quota, speed       int64
	expires            *int64
	nodes              []string
}

func (e *env) addUser(u testUser) {
	e.t.Helper()
	id := "usr_" + u.name
	now := time.Now().Unix()
	if u.status == "" {
		u.status = "active"
	}
	var expires any
	if u.expires != nil {
		expires = *u.expires
	}
	e.exec(`INSERT INTO user (id, name, group_id, disabled, status, app_happ, app_amnezia, all_nodes, quota_bytes, period_start,
		expires_at, speed_limit_bps, sub_token_hash, sub_token_enc, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, u.name, u.group, u.disabled, u.status, u.happ, u.amnezia, u.all, u.quota, now, expires, u.speed, sha(u.name), []byte("x"), now)
	for _, n := range u.nodes {
		e.exec(`INSERT INTO user_node (user_id, node_id) VALUES (?, ?)`, id, n)
	}
	e.exec(`INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at, platform, model) VALUES (?,?,?,?,?,?,?)`,
		"dev_"+u.name, id, now, now, now, "ios", "iPhone")
	for proto, suffix := range map[string]string{"fakehy": "hy", "fakewg": "wg"} {
		e.exec(`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES (?,?,?,?,?,?,?)`,
			fmt.Sprintf("crd_%s_%s", u.name, suffix), "dev_"+u.name, id, proto, []byte("x"), fmt.Sprintf(`{"v":"%s-%s"}`, u.name, suffix), now)
	}
}

func (e *env) fixture(nodeA string) fixtureIDs {
	e.t.Helper()
	now := time.Now().Unix()
	ids := fixtureIDs{nodeA: nodeA, nodeB: "nod_b", i1: "inb_1", i2: "inb_2", i3: "inb_3", erinExpires: now + 1000}
	e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES (?, 'nodeb', 'b.example.com', 'active', ?)`, ids.nodeB, now)
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_1', 'g1', ?), ('grp_2', 'g2', ?)`, now, now)

	// P1 keeps an x-secret in the vault; the node must receive it merged into the settings.
	secrets := e.v.Seal([]byte(`{"/obfs/password":"pw-secret"}`), "prf_1")
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, secrets_enc, version, created_at, updated_at)
		VALUES ('prf_1', 'fakehy', 'p1', '{"port":443,"obfs":{"type":"salamander"}}', ?, 3, ?, ?)`, secrets, now, now)
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_2', 'fakewg', 'p2', '{"port":51820}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO user_group_profile (group_id, profile_id) VALUES ('grp_1','prf_1'), ('grp_1','prf_2'), ('grp_2','prf_1')`)
	for _, in := range [][3]string{{ids.i1, "prf_1", nodeA}, {ids.i2, "prf_2", nodeA}, {ids.i3, "prf_1", ids.nodeB}} {
		e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES (?,?,?,1,?,?)`, in[0], in[1], in[2], now, now)
	}

	erin := ids.erinExpires
	for _, u := range []testUser{
		{name: "alice", group: "grp_1", happ: true, amnezia: true, all: true},
		{name: "bob", group: "grp_1", happ: true, amnezia: true, nodes: []string{ids.nodeB}},
		{name: "carol", group: "grp_1", happ: true, amnezia: true, all: true, disabled: true, status: "disabled"},
		{name: "dave", group: "grp_1", happ: true, amnezia: true, all: true, status: "expired"},
		{name: "erin", group: "grp_1", happ: true, all: true, expires: &erin},
		{name: "frank", group: "grp_1", amnezia: true, all: true, speed: 1_000_000},
		{name: "gina", group: "grp_2", happ: true, amnezia: true, all: true},
		{name: "hank", group: "grp_1", happ: true, amnezia: true, all: true, quota: 1000},
	} {
		e.addUser(u)
	}
	e.exec(`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, revoked_at)
		VALUES ('crd_alice_old', 'dev_alice', 'usr_alice', 'fakehy', x'00', '{"v":"old"}', ?, ?)`, now, now)
	return ids
}
