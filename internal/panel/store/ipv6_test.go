package store

import "testing"

func TestUsableIPv6(t *testing.T) {
	for in, want := range map[string]string{
		"2001:db8::5": "2001:db8::5", " [2001:DB8:0:0::5] ": "2001:db8::5", "2001:db8:0:1::7": "2001:db8:0:1::7",
	} {
		if got, ok := UsableIPv6(in); !ok || got != want {
			t.Errorf("UsableIPv6(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "203.0.113.5", "::ffff:203.0.113.5", "fe80::1", "fd00::1", "::1", "::", "2001:db8::5/64", "example.com"} {
		if got, ok := UsableIPv6(in); ok {
			t.Errorf("UsableIPv6(%q) = %q, want a refusal", in, got)
		}
	}
}

func TestDoctorIPv6ReadsTheStateAndTheUplink(t *testing.T) {
	row := func(kv ...string) *DoctorRow {
		p := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = kv[i+1]
		}
		return &DoctorRow{CheckID: "ipv6", Params: p}
	}
	for _, tc := range []struct {
		name  string
		row   *DoctorRow
		state string
	}{
		{"no report", nil, IPv6Unknown},
		{"no ipv6", row("ipv6", "no"), IPv6None},
		{"broken", row("ipv6", "yes", "connect", "failed"), IPv6Broken},
		{"half a report", row("ipv6", "yes"), IPv6Unknown},
		{"works", row("ipv6", "yes", "connect", "ok"), IPv6OK},
	} {
		if got := DoctorIPv6(tc.row); got.State != tc.state {
			t.Errorf("%s: state %q, want %q", tc.name, got.State, tc.state)
		}
	}
	d := DoctorIPv6(row("ipv6", "yes", "connect", "ok", "iface", "eth0", "src", "2001:db8::5",
		"addrs", "2001:db8::5/64, fd00::1/64,203.0.113.5/24,2001:db8:0:1::7/128,junk"))
	if d.Iface != "eth0" || d.Src != "2001:db8::5" || len(d.Addrs) != 2 || d.Addrs[0].String() != "2001:db8::5/64" || d.Addrs[1].Bits() != 128 {
		t.Fatalf("uplink = %+v: the ULA, the IPv4 address and the junk are not addresses of the uplink", d)
	}
	if !d.Has("2001:db8::5") || !d.Has("2001:db8:0:1::7") || d.Has("2001:db8::9") || d.Has("fd00::1") || d.Has("junk") {
		t.Error("Has: only the addresses the doctor lists")
	}
	if got := d.FirstAddr(); got != "2001:db8::5" {
		t.Errorf("FirstAddr = %q: src first", got)
	}
	temporarySrc := DoctorIPv6(row("ipv6", "yes", "connect", "ok", "src", "2001:db8::9",
		"addrs", "2001:db8::5/64,2001:db8:0:1::7/128"))
	if temporarySrc.Has(temporarySrc.Src) {
		t.Error("Has accepted the outbound source that was not in the stable address list")
	}
	if got := temporarySrc.FirstAddr(); got != "2001:db8::5" {
		t.Errorf("FirstAddr = %q, want first stable address because src is temporary", got)
	}
	if got := DoctorIPv6(row("ipv6", "yes", "connect", "ok", "addrs", "2001:db8::7/64")).FirstAddr(); got != "2001:db8::7" {
		t.Errorf("FirstAddr without src = %q: the first stable address", got)
	}
	if got := DoctorIPv6(row("ipv6", "yes", "connect", "ok")).FirstAddr(); got != "" {
		t.Errorf("an agent that names nothing: %q", got)
	}
}
