package fleet

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestFoldRateLimits(t *testing.T) {
	for _, tc := range []struct{ device, person, want uint64 }{
		{0, 0, 0},
		{0, 500, 500},   // only the person limit: per device for an old agent
		{500, 0, 500},   // only the device limit
		{500, 900, 500}, // never faster than the device limit
		{900, 500, 500}, // the person limit is the smaller
		{700, 700, 700},
	} {
		if got := foldRateLimits(tc.device, tc.person); got != tc.want {
			t.Errorf("foldRateLimits(%d, %d) = %d, want %d", tc.device, tc.person, got, tc.want)
		}
	}
}

func TestCredentialHashSeesThePersonLimit(t *testing.T) {
	c := plugin.UserCred{CredID: "crd_1", UserID: "usr_1", DeviceID: "dev_1", Data: json.RawMessage(`{"a":1}`), RateLimitBps: 1000}
	withPerson := c
	withPerson.UserRateLimitBps = 5000
	if credentialHash(c) == credentialHash(withPerson) {
		t.Fatal("a change of the person limit must change the credential digest, or it is never sent")
	}
	withMore := withPerson
	withMore.UserRateLimitBps = 6000
	if credentialHash(withPerson) == credentialHash(withMore) {
		t.Fatal("a new value of the person limit must change the digest")
	}
}

// A person with two devices on one node: an agent that lists "user-rate-limit/1" gets the device limit and the person
// limit apart, any other gets the smaller of the two on every device and the node page says the person limit is not
// applied. A change of either limit reaches a connected agent as a credential upsert, and the agent computes the state
// hash the panel announced (connectCaps checks it) with the field in it.
func TestPersonLimitOnlyForAgentsThatShareABucket(t *testing.T) {
	x, a := newL3Env(t)
	var mu sync.Mutex
	device, person := uint64(2_000_000), uint64(1_000_000)
	set := func(d, p uint64) {
		mu.Lock()
		device, person = d, p
		mu.Unlock()
	}
	x.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		mu.Lock()
		defer mu.Unlock()
		hy := plugin.InboundSpec{ID: "inb_hy", Protocol: "fakehy", ProfileID: "prf_hy", Version: 1, Enabled: true,
			Listen: plugin.Listen{Network: "udp", Port: 4443}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "x"}, Egress: "direct", Settings: json.RawMessage(`{}`)}
		cred := func(id string) plugin.UserCred {
			return plugin.UserCred{CredID: id, UserID: "usr_pat", DeviceID: "dev_" + id, Data: json.RawMessage(`{"a":1}`), RateLimitBps: device, UserRateLimitBps: person}
		}
		return []statehash.Inbound{{Spec: hy, Creds: []plugin.UserCred{cred("crd_phone"), cred("crd_laptop")}}}, nil
	}
	service := nodeService{x.f}
	supported := func() bool {
		t.Helper()
		resp, err := service.GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Node.UserRateLimitSupported
	}

	// An agent from before the person limit: per device, the smaller limit, nothing in the new field.
	_, old, _ := connectCaps(a, "old", "doctor/1")
	for _, id := range []string{"crd_phone", "crd_laptop"} {
		if c := old.in["inb_hy"].creds[id]; c.RateLimitBps != 1_000_000 || c.UserRateLimitBps != 0 {
			t.Errorf("old agent, %s: device %d person %d, want 1000000 and 0", id, c.RateLimitBps, c.UserRateLimitBps)
		}
	}
	if supported() {
		t.Error("the node page must say the node does not apply the person limit")
	}

	// An agent with the capability: both limits as they are, and the node page does not warn.
	c, m, _ := connectCaps(a, "new", "doctor/1", capUserRateLimit)
	for _, id := range []string{"crd_phone", "crd_laptop"} {
		if got := m.in["inb_hy"].creds[id]; got.RateLimitBps != 2_000_000 || got.UserRateLimitBps != 1_000_000 {
			t.Errorf("new agent, %s: device %d person %d, want 2000000 and 1000000", id, got.RateLimitBps, got.UserRateLimitBps)
		}
	}
	if !supported() {
		t.Error("the node lists user-rate-limit/1: it applies the person limit")
	}

	// Changing the person limit, then the device limit, is a delta with both credentials each time.
	for _, step := range []struct{ device, person uint64 }{{2_000_000, 3_000_000}, {500_000, 3_000_000}} {
		set(step.device, step.person)
		x.f.StateChanged()
		ds := c.desired()
		if !m.apply(ds) {
			t.Fatal("the delta did not apply")
		}
		if len(ds.Inbounds) != 1 || len(ds.Inbounds[0].Creds) != 2 {
			t.Fatalf("limits %v: delta = %v", step, ds)
		}
		for _, cr := range ds.Inbounds[0].Creds {
			if cr.RateLimitBps != step.device || cr.UserRateLimitBps != step.person {
				t.Errorf("limits %v: sent device %d person %d", step, cr.RateLimitBps, cr.UserRateLimitBps)
			}
		}
		if m.hash() != ds.StateHash {
			t.Fatalf("limits %v: the agent computes %s, the panel announced %s", step, m.hash(), ds.StateHash)
		}
		c.send(0, applied(ds, m.hash()))
	}
	// Removing the person limit sends a delta too (a credential that lost the field is not "unchanged").
	set(500_000, 0)
	x.f.StateChanged()
	ds := c.desired()
	if len(ds.Inbounds) != 1 || len(ds.Inbounds[0].Creds) != 2 || ds.Inbounds[0].Creds[0].UserRateLimitBps != 0 {
		t.Fatalf("removing the person limit: delta = %v", ds)
	}
}
