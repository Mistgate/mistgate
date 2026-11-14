package subs_test

import "testing"

// A device that was complete before a relay existed gets the key of the new path with its next sync: the Mistgate app must
// not have to enrol again, and the direct key must stay the one it had.
func TestSyncAWGRelayAddedLaterIssuesThePathKey(t *testing.T) {
	e := newSyncEnv(t, nil)
	if _, err := e.st.W.Exec(`UPDATE node SET name = 'de1', address = '192.0.2.10', country_code = 'DE' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at)
		VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 'relay-forward/1', 2)`); err != nil {
		t.Fatal(err)
	}
	_, token := e.person("alice", nil)
	device := e.enrolDevice(token, hwidA)
	first := e.mustSync(device.Key)
	before := first.paths("awg")
	if len(before) != 1 || before[0].Via != "direct" {
		t.Fatalf("AWG paths before the relay = %+v", before)
	}

	if _, err := e.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_awg_msk1', ?, 'nod_msk1', 'udp', 8443, 1, 'active', ?, 1, 1)`, e.inboundOf(e.awg),
		`{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		t.Fatal(err)
	}
	after := e.mustSync(device.Key).paths("awg")
	if len(after) != 2 || after[0].Via != "direct" || after[1].Via != "relay" {
		t.Fatalf("AWG paths after the relay = %+v", after)
	}
	if after[0].Settings["private_key"] != before[0].Settings["private_key"] || after[1].Settings["private_key"] == before[0].Settings["private_key"] {
		t.Errorf("the direct key changed or the relay path reuses it: %v / %v", after[0].Settings["private_key"], after[1].Settings["private_key"])
	}
}
