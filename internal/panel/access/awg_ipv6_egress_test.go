package access

import (
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
)

// The .conf and the vpn:// key of an AmneziaWG device follow the subscription's rule (ipv6Egress): the tunnel's IPv6 address is in
// them only where the exit is known to give the client IPv6. AllowedIPs keep ::/0 either way.
func TestAWGDeviceConfigsFollowTheExitsIPv6(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	set := func(doctor string, clientIPv6 int) {
		t.Helper()
		e.sql(`DELETE FROM doctor_result WHERE node_id = ? AND check_id = 'ipv6'`, f.nodeID)
		if doctor != "" {
			e.sql(`INSERT INTO doctor_result (node_id, check_id, status, params_json, received_unix) VALUES (?, 'ipv6', 1, ?, 1)`, f.nodeID, doctor)
		}
		e.sql(`UPDATE node SET client_ipv6 = ? WHERE id = ?`, clientIPv6, f.nodeID)
	}
	read := func() (conf, key string) {
		t.Helper()
		dev := f.add(f.user, "")
		c := dev.Configs[0]
		raw, err := vpnkey.Decode(c.VpnKey)
		if err != nil {
			t.Fatal(err)
		}
		return c.Conf, string(raw)
	}
	has6 := func(s string) bool { return strings.Contains(s, "/128") }

	for _, c := range []struct {
		name   string
		doctor string
		on     int
		want   bool
	}{
		{"not reported yet", "", 1, false},
		{"no IPv6 at the hoster", `{"ipv6":"no"}`, 1, false},
		{"broken IPv6", `{"ipv6":"yes","connect":"failed"}`, 1, false},
		{"IPv6 for clients off", `{"ipv6":"yes","connect":"ok","src":"2001:db8::5","addrs":"2001:db8::5/64"}`, 0, false},
		{"working", `{"ipv6":"yes","connect":"ok","src":"2001:db8::5","addrs":"2001:db8::5/64"}`, 1, true},
	} {
		set(c.doctor, c.on)
		conf, key := read()
		if has6(conf) != c.want || has6(key) != c.want {
			t.Errorf("%s: IPv6 address in the .conf = %v, in the key = %v, want %v\n%s", c.name, has6(conf), has6(key), c.want, conf)
		}
		if !strings.Contains(conf, "AllowedIPs = 0.0.0.0/0, ::/0") {
			t.Errorf("%s: AllowedIPs lost ::/0:\n%s", c.name, conf)
		}
		if !strings.Contains(conf, "Address = 10.66.4.") {
			t.Errorf("%s: no IPv4 address:\n%s", c.name, conf)
		}
	}
}
