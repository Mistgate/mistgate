package awg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

func ruDirectFragment(t *testing.T, s Settings, f plugin.ClientFormat, direct bool) plugin.Fragment {
	t.Helper()
	in := renderInput(t, s, f)
	in.RUDirect = direct
	frag, ok := New().Render(in)
	if !ok {
		t.Fatalf("Render(%s) refused", f)
	}
	return frag
}

func confLine(conf, key string) string {
	for _, l := range strings.Split(conf, "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok && k == key {
			return v
		}
	}
	return ""
}

func TestRUDirectConfGolden(t *testing.T) {
	golden(t, "awg31.rudirect.conf", ruDirectFragment(t, fixture31(), plugin.FormatAWGConf, true).Data)
	conf := string(ruDirectFragment(t, fixture31(), plugin.FormatAWGConf, true).Data)
	if strings.Contains(confLine(conf, "Address"), ":") || strings.Contains(confLine(conf, "AllowedIPs"), ":") {
		t.Errorf("no IPv6 in the key: Address %q", confLine(conf, "Address"))
	}
	// everything but the two lines is the ordinary key
	plain := strings.Split(string(ruDirectFragment(t, fixture31(), plugin.FormatAWGConf, false).Data), "\n")
	for i, l := range strings.Split(conf, "\n") {
		if k, _, _ := strings.Cut(l, " = "); k != "Address" && k != "AllowedIPs" && l != plain[i] {
			t.Errorf("line %d differs: %q vs %q", i, l, plain[i])
		}
	}
}

// The vpn:// key holds the same list as its config text; the rest of last_config is the ordinary key's.
func TestRUDirectVpnKeyGolden(t *testing.T) {
	_, last, _ := decodeKey(t, ruDirectFragment(t, fixture31(), plugin.FormatAmneziaVPN, true))
	conf := string(ruDirectFragment(t, fixture31(), plugin.FormatAWGConf, true).Data)
	if last["config"] != conf {
		t.Error("config of the vpn:// key differs from the .conf")
	}
	var allowed []string
	for _, v := range last["allowed_ips"].([]any) {
		allowed = append(allowed, v.(string))
	}
	if strings.Join(allowed, ", ") != confLine(conf, "AllowedIPs") || len(allowed) != len(RUDirectAllowedIPs()) {
		t.Errorf("allowed_ips (%d entries) differ from the AllowedIPs of the config", len(allowed))
	}
	delete(last, "config")
	delete(last, "allowed_ips")
	js, err := json.MarshalIndent(last, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "awg31.rudirect.vpn.json", append(js, '\n'))
}

// The flag reaches only the two formats of the key; the Mihomo proxy keeps the whole route list and the address as they were.
func TestRUDirectLeavesOtherFormatsAlone(t *testing.T) {
	for _, f := range []plugin.ClientFormat{plugin.FormatMihomo, plugin.FormatMistgate} {
		in := renderInput(t, fixture31(), f)
		in.Capabilities = []string{"awg=3.1"}
		a, ok1 := New().Render(in)
		in.RUDirect = true
		b, ok2 := New().Render(in)
		if !ok1 || !ok2 || string(a.Data) != string(b.Data) {
			t.Errorf("%s changes with RUDirect", f)
		}
	}
}
