package protocols

import (
	"encoding/json"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

type fakeProto struct {
	Protocol
	id string
}

func (f fakeProto) ID() string { return f.id }
func (f fakeProto) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{{Client: plugin.ClientHapp}}
}

func TestRegistry(t *testing.T) {
	r, err := NewRegistry(fakeProto{id: "b"}, fakeProto{id: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if l := r.List(); len(l) != 2 || l[0].ID() != "a" || l[1].ID() != "b" {
		t.Errorf("list order wrong")
	}
	if _, ok := r.Get("a"); !ok {
		t.Error("a missing")
	}
	if _, ok := r.Get("zzz"); ok {
		t.Error("unknown id found")
	}
	if _, err := NewRegistry(fakeProto{id: "a"}, fakeProto{id: "a"}); err == nil {
		t.Error("duplicate accepted")
	}
	if _, err := NewRegistry(fakeProto{}); err == nil {
		t.Error("empty id accepted")
	}
	if !AllowedForApps(fakeProto{}, plugin.ClientAmnezia, plugin.ClientHapp) || AllowedForApps(fakeProto{}, plugin.ClientAmnezia) {
		t.Error("AllowedForApps wrong")
	}
}

type tunnelProto struct{ fakeProto }

func (tunnelProto) PerDevice() bool { return true }
func (tunnelProto) MinClients(json.RawMessage) []ClientReq {
	return []ClientReq{{Client: plugin.ClientAmnezia, App: "AmneziaVPN", Min: "5.0.1.5"}}
}

func TestOptionalInterfaces(t *testing.T) {
	if IsPerDevice(fakeProto{}) || MinClientsOf(fakeProto{}, nil) != nil {
		t.Error("a plugin without the optional interfaces is not per-device and has no requirements")
	}
	if !IsPerDevice(tunnelProto{}) {
		t.Error("PerDevice not seen")
	}
	if got := MinClientsOf(tunnelProto{}, nil); len(got) != 1 || got[0].Min != "5.0.1.5" {
		t.Errorf("MinClientsOf = %v", got)
	}
}

const testSchema = `{"properties":{"a":{"x-secret":true},"o":{"properties":{"pw":{"x-secret":true,"x-critical":true},"t":{"x-critical":true}}}}}`

func TestFlaggedPointers(t *testing.T) {
	s, err := FlaggedPointers([]byte(testSchema), "x-secret")
	if err != nil || len(s) != 2 || s[0] != "/a" || s[1] != "/o/pw" {
		t.Errorf("secrets = %v, %v", s, err)
	}
	c, _ := FlaggedPointers([]byte(testSchema), "x-critical")
	if len(c) != 2 || c[0] != "/o/pw" || c[1] != "/o/t" {
		t.Errorf("critical = %v", c)
	}
	if _, err := FlaggedPointers([]byte("{"), "x"); err == nil {
		t.Error("bad schema accepted")
	}
}

func TestSecretsRoundTrip(t *testing.T) {
	ptrs := []string{"/o/pw"}
	merged := json.RawMessage(`{"n":443,"o":{"pw":"s3cret","t":"x"}}`)
	pub, secrets, err := SplitSecrets(merged, ptrs)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != `{"n":443,"o":{"t":"x"}}` || secrets["/o/pw"] != "s3cret" {
		t.Errorf("split: %s %v", pub, secrets)
	}
	back, err := MergeSecrets(pub, secrets)
	if err != nil || string(back) != `{"n":443,"o":{"pw":"s3cret","t":"x"}}` {
		t.Errorf("merge: %s %v", back, err)
	}
	masked, _ := MaskSecrets(back, ptrs)
	if string(masked) != `{"n":443,"o":{"pw":"••••","t":"x"}}` {
		t.Errorf("mask: %s", masked)
	}
	// Masking a document without the secret adds nothing.
	if m, _ := MaskSecrets(pub, ptrs); string(m) != string(pub) {
		t.Errorf("mask invented a secret: %s", m)
	}
}

func TestResolveInput(t *testing.T) {
	ptrs := []string{"/o/pw"}
	base := json.RawMessage(`{"n":1,"o":{"pw":"stored","t":"x"}}`)
	fresh := json.RawMessage(`{"n":1,"o":{"pw":"fresh","t":"x"}}`)
	cases := []struct{ in, want string }{
		{`{"n":2}`, `{"n":2,"o":{"pw":"stored","t":"x"}}`},                 // omitted keys keep the base
		{`{"o":{"pw":"••••"}}`, `{"n":1,"o":{"pw":"stored","t":"x"}}`},     // mask keeps the stored secret
		{`{"o":{"pw":""}}`, `{"n":1,"o":{"pw":"fresh","t":"x"}}`},          // empty regenerates
		{`{"o":{"pw":"$generate"}}`, `{"n":1,"o":{"pw":"fresh","t":"x"}}`}, // marker regenerates
		{`{"o":{"pw":"mine"}}`, `{"n":1,"o":{"pw":"mine","t":"x"}}`},       // explicit value wins
	}
	for _, c := range cases {
		got, err := ResolveInput(json.RawMessage(c.in), base, fresh, ptrs)
		if err != nil || string(got) != c.want {
			t.Errorf("ResolveInput(%s) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
	// Mask without a stored secret (create) falls back to the generated one.
	got, _ := ResolveInput(json.RawMessage(`{"o":{"pw":"••••"}}`), fresh, fresh, ptrs)
	if string(got) != `{"n":1,"o":{"pw":"fresh","t":"x"}}` {
		t.Errorf("create with mask: %s", got)
	}
	if _, err := ResolveInput(json.RawMessage(`[1]`), base, fresh, ptrs); err == nil {
		t.Error("non-object accepted")
	}
}

func TestChangedPointers(t *testing.T) {
	a := json.RawMessage(`{"p":1,"o":{"t":"x"}}`)
	b := json.RawMessage(`{"p":1,"o":{"t":"y"}}`)
	got := ChangedPointers(a, b, []string{"/p", "/o/t", "/missing"})
	if len(got) != 1 || got[0] != "/o/t" {
		t.Errorf("changed = %v", got)
	}
}
