package mcp

import "testing"

// A view cut down to fit keeps the newest hours: they are what an agent asks about.
func TestShrinkKeepsTheNewestHours(t *testing.T) {
	hours := []linkHourV{{HourUnix: 1}, {HourUnix: 2}, {HourUnix: 3}, {HourUnix: 4}}
	v := &NodeConnectionsV{Inbounds: []inboundConnV{{Hours: append([]linkHourV(nil), hours...)}}}
	if !v.shrink() || len(v.Inbounds[0].Hours) != 2 || v.Inbounds[0].Hours[0].HourUnix != 3 || v.Inbounds[0].Hours[1].HourUnix != 4 {
		t.Fatalf("node hours after shrink = %+v", v.Inbounds[0].Hours)
	}
	u := &UserConnectionV{Rows: []deviceConnV{{Hours: append([]linkHourV(nil), hours...)}}}
	if !u.shrink() || len(u.Rows[0].Hours) != 2 || u.Rows[0].Hours[1].HourUnix != 4 {
		t.Fatalf("user hours after shrink = %+v", u.Rows[0].Hours)
	}
}
