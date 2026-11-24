package subs

import (
	"strings"
	"testing"
)

// The owner's choice of the seconds between the Auto group's checks reaches all three documents (the default, 300 s, is "5m" in the
// older tests and the fixtures).
func TestAutoIntervalReachesEveryFormat(t *testing.T) {
	for _, tc := range []struct {
		secs     int
		mihomo   string
		duration string
	}{{0, "interval: 300", "5m"}, {60, "interval: 60", "1m"}, {600, "interval: 600", "10m"}} {
		mh, err := (mihomoProfile{title: "Example VPN", lines: []string{fixture(t, hy2Line), fixture(t, hy2Line)}, names: []string{"de1", "nl1"}, autoSelect: true, autoSecs: tc.secs}).build()
		if err != nil || !strings.Contains(string(mh), tc.mihomo+"\n") {
			t.Errorf("mihomo %d: err %v, want %q in\n%s", tc.secs, err, tc.mihomo, mh)
		}
		sb, err := (singboxProfile{title: "Example VPN", lines: []string{fixture(t, singboxFragments[0]), fixture(t, singboxFragments[0])}, names: []string{"de1", "nl1"}, autoSelect: true, autoSecs: tc.secs}).build()
		if err != nil || !strings.Contains(string(sb), `"interval":"`+tc.duration+`"`) {
			t.Errorf("sing-box %d: err %v, want %s in\n%s", tc.secs, err, tc.duration, sb)
		}
		xr, err := (xrayProfile{lines: fixtures(t, xrayFragments), names: []string{"a", "b", "c"}, autoSelect: true, autoSecs: tc.secs}).build()
		if err != nil || !strings.Contains(string(xr), `"probeInterval":"`+tc.duration+`"`) {
			t.Errorf("xray %d: err %v, want %s", tc.secs, err, tc.duration)
		}
	}
}
