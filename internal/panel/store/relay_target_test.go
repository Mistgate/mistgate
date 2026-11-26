package store

import (
	"encoding/json"
	"testing"
)

func TestRelayForTargetFiltersOnlyStaleEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, to, key          string
		staleLeg, staleMonitor bool
	}{
		{"current", "203.0.113.10:443", "current", false, false},
		{"new_port", "203.0.113.10:8443", "next", true, true},
		{"new_credentials", "203.0.113.10:443", "next", false, true},
		{"invalid_target", "", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := `{"at":1,"network":"tcp","port":2443,"to":"203.0.113.10:443","leg":{"status":"fail","at":1},"path":{"status":"ok","at":1},"direct_speed":{"status":"ok","at":1},"direct_streak":2,"direct_streak_at":1,"monitor":{"at_unix":1,"target_key":"current","ok":false},"bridge":{"connected":true},"counters":{"bytes":123},"future_field":{"value":42}}`
			l := RelayLink{Relay: Relay{Network: "tcp", Port: 2443, CheckJSON: original}}
			filtered := l.ForTarget(tc.to, tc.key)
			c, _ := filtered.Check()
			if (c.Leg.Status == "") != tc.staleLeg || (c.Path.Status == "") != tc.staleLeg || (c.DirectSpeed.Status == "") != tc.staleLeg || (filtered.DirectStreak() == 0) != tc.staleLeg {
				t.Fatalf("wrong direct evidence: %+v", c)
			}
			if (c.Monitor == nil) != tc.staleMonitor {
				t.Fatalf("wrong monitor evidence: %+v", c.Monitor)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(filtered.CheckJSON), &fields); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"bridge", "counters", "future_field"} {
				if fields[k] == nil {
					t.Fatalf("lost independent field %s", k)
				}
			}
			if l.CheckJSON != original {
				t.Fatal("mutated original check")
			}
		})
	}
}

func TestRelayTargetFollowsRouteNotProfileRevision(t *testing.T) {
	l := RelayLink{Relay: Relay{ID: "r", Network: "tcp", Port: 3443}}
	in := FleetInboundRow{ID: "in", ProfileID: "p", Protocol: "vless", SpecVersion: 1}
	const addr = "2001:db8::1"
	to, key := l.Target(in, addr, 443)
	if to != "[2001:db8::1]:443" || key == "" {
		t.Fatal(to, key)
	}
	in.ProfileVersion++
	if s, k := l.Target(in, addr, 443); s != to || k != key {
		t.Fatal("renaming the profile invalidated its route")
	}
	if s, k := l.Target(in, addr, 8443); s == to || k == key {
		t.Fatal("new port kept old identity")
	}
	in.SpecVersion++
	if _, k := l.Target(in, addr, 443); k == key {
		t.Fatal("changed inbound kept old monitor")
	}
}
