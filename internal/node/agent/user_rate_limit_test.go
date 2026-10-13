package agent

import (
	"slices"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
)

type capsEngine struct {
	*fakeEngine
	caps engine.Capabilities
}

func (e capsEngine) Capabilities() engine.Capabilities { return e.caps }

// "user-rate-limit/1" is listed only when the engines that shape per credential also share a bucket per person.
func TestCapabilitiesAdvertiseUserRateLimitOnlyWhenEnginesApplyIt(t *testing.T) {
	both := engine.Capabilities{RateLimitPerCred: true, RateLimitPerUser: true}
	deviceOnly := engine.Capabilities{RateLimitPerCred: true}
	none := engine.Capabilities{}
	for _, tc := range []struct {
		name    string
		engines map[string]engine.Capabilities
		want    bool
	}{
		{"hysteria2 and vless share", map[string]engine.Capabilities{"hysteria2": both, "vless": both}, true},
		{"awg shapes nothing and does not count", map[string]engine.Capabilities{"hysteria2": both, "awg": none}, true},
		{"one engine shapes per credential only", map[string]engine.Capabilities{"hysteria2": both, "vless": deviceOnly}, false},
		{"no engine shares", map[string]engine.Capabilities{"hysteria2": deviceOnly}, false},
		{"awg alone", map[string]engine.Capabilities{"awg": none}, false},
		{"no engines", nil, false},
	} {
		engines := map[string]engine.Engine{}
		for proto, caps := range tc.engines {
			engines[proto] = capsEngine{newFakeEngine(proto), caps}
		}
		a := &Agent{engines: engines}
		if got := slices.Contains(a.capabilities(), "user-rate-limit/1"); got != tc.want {
			t.Errorf("%s: user-rate-limit/1 listed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCredentialKeepsThePersonLimitOnTheWire(t *testing.T) {
	in := &pb.Credential{CredId: "crd_1", UserId: "usr_1", DataJson: "{}", RateLimitBps: 1000, UserRateLimitBps: 5000}
	c, err := credFromPB(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimitBps != 1000 || c.UserRateLimitBps != 5000 {
		t.Fatalf("credFromPB: device %d person %d", c.RateLimitBps, c.UserRateLimitBps)
	}
	if out := credToPB(c); out.RateLimitBps != 1000 || out.UserRateLimitBps != 5000 {
		t.Fatalf("credToPB: device %d person %d", out.RateLimitBps, out.UserRateLimitBps)
	}
}
