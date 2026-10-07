package fleet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
)

func TestDiffStateDigestMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(0x6b3))
	for iteration := 0; iteration < 250; iteration++ {
		old, next := randomDigestTestState(rng), randomDigestTestState(rng)
		gotInbounds, gotRemoved := diffState(sentDigestFor(old, uint64(iteration+1)), next)
		wantInbounds, wantRemoved := legacyDiffState(old, next)
		if len(gotInbounds) != len(wantInbounds) {
			t.Fatalf("iteration %d changed inbound count = %d, want %d", iteration, len(gotInbounds), len(wantInbounds))
		}
		for i := range gotInbounds {
			if !proto.Equal(gotInbounds[i], wantInbounds[i]) {
				t.Fatalf("iteration %d changed inbound %d differs:\ngot:  %v\nwant: %v", iteration, i, gotInbounds[i], wantInbounds[i])
			}
		}
		if !slices.Equal(gotRemoved, wantRemoved) {
			t.Fatalf("iteration %d removed inbounds = %v, want %v", iteration, gotRemoved, wantRemoved)
		}
	}
}

func TestWarpHashTellsSpecsApartAsDeepEqual(t *testing.T) {
	cases := [][2]*plugin.WarpSpec{
		{nil, nil},
		{{Enabled: true, PrivateKey: "key", Ports: []uint16{2408}, Reserved: []byte{1, 2, 3}}, {Enabled: true, PrivateKey: "key", Ports: []uint16{2408}, Reserved: []byte{1, 2, 3}}},
		{{Ports: nil, Reserved: nil}, {Ports: []uint16{}, Reserved: []byte{}}},
		{{Enabled: true, PrivateKey: "key"}, {Enabled: true, PrivateKey: "changed"}},
	}
	for i, pair := range cases {
		got := warpHash(pair[0]) == warpHash(pair[1])
		want := reflect.DeepEqual(pair[0], pair[1])
		if got != want {
			t.Errorf("case %d same hash = %v, want %v", i, got, want)
		}
	}
}

func TestSentDigestJSONSizeForLargeNode(t *testing.T) {
	state := &nodeState{in: make(map[string]*inboundState, 4), hash: "0123456789abcdef0123456789abcdef"}
	for inbound := 0; inbound < 4; inbound++ {
		id := fmt.Sprintf("inb_%08d", inbound)
		entry := &inboundState{specHash: "0123456789abcdef", creds: make([]plugin.UserCred, 300)}
		for cred := range entry.creds {
			entry.creds[cred] = plugin.UserCred{CredID: fmt.Sprintf("crd_%012d", cred), UserID: "usr_example", DeviceID: "dev_example",
				Data: json.RawMessage(`{"v":"synthetic"}`), RateLimitBps: 1_000_000, ValidUntil: time.Unix(2_000_000_000, 0).UTC()}
		}
		sort.Slice(entry.creds, func(i, j int) bool { return entry.creds[i].CredID < entry.creds[j].CredID })
		state.in[id] = entry
	}
	encoded, err := json.Marshal(sentDigestFor(state, 42))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent digest JSON for 4 inbounds and 300 credentials each: %d bytes", len(encoded))
}

func randomDigestTestState(rng *rand.Rand) *nodeState {
	state := &nodeState{in: make(map[string]*inboundState)}
	for inbound := 0; inbound < 7; inbound++ {
		if rng.Intn(4) == 0 {
			continue
		}
		id := fmt.Sprintf("inb_%d", inbound)
		version := rng.Intn(3)
		entry := &inboundState{
			spec: plugin.InboundSpec{ID: id, Protocol: "fakehy", ProfileID: fmt.Sprintf("profile-%d", inbound), Version: uint64(version), Enabled: true,
				Listen: plugin.Listen{Network: "udp", Port: uint16(4000 + inbound)}, Settings: json.RawMessage(fmt.Sprintf(`{"version":%d}`, version))},
			specHash: fmt.Sprintf("spec-%d", version),
		}
		for cred := 0; cred < 7; cred++ {
			if rng.Intn(3) == 0 {
				continue
			}
			validUntil := time.Time{}
			if rng.Intn(3) != 0 {
				seconds := int64(rng.Intn(1_000_000))
				validUntil = time.Unix(seconds, int64(rng.Intn(1_000_000_000))).In(time.FixedZone("test", (rng.Intn(25)-12)*3600))
			}
			entry.creds = append(entry.creds, plugin.UserCred{
				CredID: fmt.Sprintf("cred-%d", cred), UserID: fmt.Sprintf("user-%d", rng.Intn(5)), DeviceID: fmt.Sprintf("device-%d", rng.Intn(5)),
				Data: json.RawMessage(fmt.Sprintf(`{"v":%d}`, rng.Intn(5))), RateLimitBps: uint64(rng.Intn(3_000_000)), ValidUntil: validUntil,
			})
		}
		sort.Slice(entry.creds, func(i, j int) bool { return entry.creds[i].CredID < entry.creds[j].CredID })
		state.in[id] = entry
	}
	return state
}

// legacyDiffState is the pre-digest implementation retained as the property test oracle.
func legacyDiffState(old, next *nodeState) (changed []*agentv1.InboundState, removed []string) {
	for _, id := range next.ids() {
		n := next.in[id]
		o, ok := old.in[id]
		if !ok || o.specHash != n.specHash {
			changed = append(changed, &agentv1.InboundState{InboundId: id, Spec: specProto(n.spec), CredsReplace: true, Creds: credsProto(n.creds)})
			continue
		}
		oldByID := make(map[string]plugin.UserCred, len(o.creds))
		for _, c := range o.creds {
			oldByID[c.CredID] = c
		}
		var up []*agentv1.Credential
		seen := make(map[string]bool, len(n.creds))
		for _, c := range n.creds {
			seen[c.CredID] = true
			if oc, ok := oldByID[c.CredID]; !ok || !legacySameCred(oc, c) {
				up = append(up, credProto(c))
			}
		}
		var rm []string
		for _, c := range o.creds {
			if !seen[c.CredID] {
				rm = append(rm, c.CredID)
			}
		}
		if len(up) > 0 || len(rm) > 0 {
			changed = append(changed, &agentv1.InboundState{InboundId: id, Creds: up, RemovedCredIds: rm})
		}
	}
	for _, id := range old.ids() {
		if _, ok := next.in[id]; !ok {
			removed = append(removed, id)
		}
	}
	return changed, removed
}

func legacySameCred(a, b plugin.UserCred) bool {
	return a.CredID == b.CredID && a.UserID == b.UserID && a.DeviceID == b.DeviceID &&
		bytes.Equal(a.Data, b.Data) && a.RateLimitBps == b.RateLimitBps && a.ValidUntil.Equal(b.ValidUntil)
}
