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
	var coverage digestTestCoverage
	for iteration := 0; iteration < 250; iteration++ {
		old := randomDigestTestState(rng)
		mutation := mutateDigestTestState(rng, old, iteration)
		next := mutation.state
		coverage.add(countDigestTestCoverage(old, next))
		coverage.timezoneNoops += mutation.timezoneNoops
		coverage.emptyDataNoops += mutation.emptyDataNoops
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
	for _, check := range []struct {
		name  string
		count int
	}{
		{"unchanged credentials", coverage.unchangedCredentials},
		{"added credentials", coverage.addedCredentials},
		{"removed credentials", coverage.removedCredentials},
		{"added inbounds", coverage.addedInbounds},
		{"removed inbounds", coverage.removedInbounds},
		{"changed specs", coverage.changedSpecs},
		{"equivalent timezone changes", coverage.timezoneNoops},
		{"nil/empty Data changes", coverage.emptyDataNoops},
	} {
		if check.count == 0 {
			t.Errorf("property generator did not cover %s", check.name)
		}
	}
}

func TestCredentialHashMatchesWireFields(t *testing.T) {
	base := plugin.UserCred{
		CredID: "crd_base", UserID: "usr_base", DeviceID: "dev_base", Data: json.RawMessage(`{"auth":"base"}`),
		RateLimitBps: 1000, ValidUntil: time.Unix(2_000_000_000, 0).UTC(),
	}
	baseHash := credentialHash(base)
	for name, mutate := range map[string]func(*plugin.UserCred){
		"credential id": func(c *plugin.UserCred) { c.CredID += "_changed" },
		"user id":       func(c *plugin.UserCred) { c.UserID += "_changed" },
		"device id":     func(c *plugin.UserCred) { c.DeviceID += "_changed" },
		"data":          func(c *plugin.UserCred) { c.Data = json.RawMessage(`{"auth":"changed"}`) },
		"rate limit":    func(c *plugin.UserCred) { c.RateLimitBps++ },
		"expiry second": func(c *plugin.UserCred) { c.ValidUntil = c.ValidUntil.Add(time.Second) },
	} {
		changed := base
		mutate(&changed)
		if got := credentialHash(changed); got == baseHash {
			t.Errorf("%s changed a wire field without changing the credential hash", name)
		}
	}

	for name, equivalent := range map[string]plugin.UserCred{
		"subsecond expiry": func() plugin.UserCred {
			c := base
			c.ValidUntil = c.ValidUntil.Add(123 * time.Millisecond)
			return c
		}(),
		"expiry location": func() plugin.UserCred {
			c := base
			c.ValidUntil = c.ValidUntil.In(time.FixedZone("other", 3600))
			return c
		}(),
	} {
		if got := credentialHash(equivalent); got != baseHash {
			t.Errorf("%s changed the credential hash for equivalent wire fields", name)
		}
	}
	emptyData := base
	emptyData.Data = nil
	emptyDataHash := credentialHash(emptyData)
	emptyData.Data = []byte{}
	if got := credentialHash(emptyData); got != emptyDataHash {
		t.Error("nil and empty data changed the credential hash for equivalent wire fields")
	}
	zeroExpiry, unixEpoch := base, base
	zeroExpiry.ValidUntil = time.Time{}
	unixEpoch.ValidUntil = time.Unix(0, 0).UTC()
	if credentialHash(zeroExpiry) != credentialHash(unixEpoch) {
		t.Error("zero expiry and unix epoch changed the credential hash for equivalent wire fields")
	}
}

func TestDiffStateIgnoresSubsecondExpiryChanges(t *testing.T) {
	cred := plugin.UserCred{
		CredID: "crd_expiring", UserID: "usr_example", DeviceID: "dev_example",
		Data: json.RawMessage(`{"auth":"synthetic"}`), ValidUntil: time.Unix(2_000_000_000, 0).UTC(),
	}
	old := &nodeState{in: map[string]*inboundState{
		"inb_example": {specHash: "spec", creds: []plugin.UserCred{cred}},
	}}
	nextCred := cred
	nextCred.ValidUntil = nextCred.ValidUntil.Add(500 * time.Millisecond)
	next := &nodeState{in: map[string]*inboundState{
		"inb_example": {specHash: "spec", creds: []plugin.UserCred{nextCred}},
	}}
	changed, removed := diffState(sentDigestFor(old, 1), next)
	if len(changed) != 0 || len(removed) != 0 {
		t.Fatalf("subsecond expiry change sent a delta: changed=%v removed=%v", changed, removed)
	}

	nextCred.ValidUntil = nextCred.ValidUntil.Add(time.Second)
	next.in["inb_example"].creds[0] = nextCred
	changed, removed = diffState(sentDigestFor(old, 1), next)
	if len(changed) != 1 || len(removed) != 0 || len(changed[0].Creds) != 1 {
		t.Fatalf("expiry second change did not send one credential upsert: changed=%v removed=%v", changed, removed)
	}
}

func BenchmarkDiffStateOneCredentialChange(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("users_%d", count), func(b *testing.B) {
			initialCreds := make([]plugin.UserCred, count)
			for i := range initialCreds {
				initialCreds[i] = plugin.UserCred{
					CredID: fmt.Sprintf("crd_%05d", i), UserID: "usr_example", DeviceID: fmt.Sprintf("dev_%05d", i),
					Data: json.RawMessage(`{"auth":"synthetic"}`), RateLimitBps: 1_000_000,
					ValidUntil: time.Unix(2_000_000_000, 0).UTC(),
				}
			}
			initial := &nodeState{in: map[string]*inboundState{
				"inb_example": {specHash: "spec", creds: initialCreds},
			}}
			nextCreds := append([]plugin.UserCred(nil), initialCreds...)
			nextCreds[count/2].Data = json.RawMessage(`{"auth":"changed"}`)
			next := &nodeState{in: map[string]*inboundState{
				"inb_example": {specHash: "spec", creds: nextCreds},
			}}
			old := sentDigestFor(initial, 1)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				diffState(old, next)
			}
		})
	}
}

type digestMutation struct {
	state          *nodeState
	timezoneNoops  int
	emptyDataNoops int
}

type digestTestCoverage struct {
	unchangedCredentials int
	addedCredentials     int
	removedCredentials   int
	addedInbounds        int
	removedInbounds      int
	changedSpecs         int
	timezoneNoops        int
	emptyDataNoops       int
}

func (c *digestTestCoverage) add(other digestTestCoverage) {
	c.unchangedCredentials += other.unchangedCredentials
	c.addedCredentials += other.addedCredentials
	c.removedCredentials += other.removedCredentials
	c.addedInbounds += other.addedInbounds
	c.removedInbounds += other.removedInbounds
	c.changedSpecs += other.changedSpecs
	c.timezoneNoops += other.timezoneNoops
	c.emptyDataNoops += other.emptyDataNoops
}

func countDigestTestCoverage(old, next *nodeState) digestTestCoverage {
	coverage := digestTestCoverage{unchangedCredentials: countUnchangedDigestTestCredentials(old, next)}
	for inboundID, oldInbound := range old.in {
		nextInbound, ok := next.in[inboundID]
		if !ok {
			coverage.removedInbounds++
			continue
		}
		if oldInbound.specHash != nextInbound.specHash {
			coverage.changedSpecs++
			continue
		}
		oldByID := make(map[string]struct{}, len(oldInbound.creds))
		nextByID := make(map[string]struct{}, len(nextInbound.creds))
		for _, cred := range oldInbound.creds {
			oldByID[cred.CredID] = struct{}{}
		}
		for _, cred := range nextInbound.creds {
			nextByID[cred.CredID] = struct{}{}
		}
		for id := range oldByID {
			if _, ok := nextByID[id]; !ok {
				coverage.removedCredentials++
			}
		}
		for id := range nextByID {
			if _, ok := oldByID[id]; !ok {
				coverage.addedCredentials++
			}
		}
	}
	for inboundID := range next.in {
		if _, ok := old.in[inboundID]; !ok {
			coverage.addedInbounds++
		}
	}
	return coverage
}

func countUnchangedDigestTestCredentials(old, next *nodeState) int {
	unchanged := 0
	for inboundID, nextInbound := range next.in {
		oldInbound, ok := old.in[inboundID]
		if !ok || oldInbound.specHash != nextInbound.specHash {
			continue
		}
		oldByID := make(map[string]plugin.UserCred, len(oldInbound.creds))
		for _, cred := range oldInbound.creds {
			oldByID[cred.CredID] = cred
		}
		for _, cred := range nextInbound.creds {
			if oldCred, ok := oldByID[cred.CredID]; ok && legacySameCred(oldCred, cred) {
				unchanged++
			}
		}
	}
	return unchanged
}

func mutateDigestTestState(rng *rand.Rand, old *nodeState, iteration int) digestMutation {
	next := &nodeState{in: make(map[string]*inboundState), warp: old.warp, hash: old.hash, withheld: slices.Clone(old.withheld)}
	var mutation digestMutation
	for _, inboundID := range old.ids() {
		oldInbound := old.in[inboundID]
		if rng.Intn(8) == 0 {
			continue
		}
		entry := *oldInbound
		entry.creds = make([]plugin.UserCred, 0, len(oldInbound.creds)+1)
		if rng.Intn(5) == 0 {
			entry.spec.Version++
			entry.specHash = fmt.Sprintf("%s-mutated-%d", entry.specHash, iteration)
		}
		for _, original := range oldInbound.creds {
			cred := original
			switch rng.Intn(10) {
			case 0:
				continue
			case 1:
				mutateDigestTestCredential(rng, &cred)
			case 2:
				switch rng.Intn(2) {
				case 0:
					if !cred.ValidUntil.IsZero() {
						_, offset := cred.ValidUntil.Zone()
						cred.ValidUntil = cred.ValidUntil.In(time.FixedZone("alternate", offset+3600))
						mutation.timezoneNoops++
					} else if cred.Data == nil {
						cred.Data = json.RawMessage{}
						mutation.emptyDataNoops++
					} else if len(cred.Data) == 0 {
						cred.Data = nil
						mutation.emptyDataNoops++
					}
				case 1:
					if cred.Data == nil {
						cred.Data = json.RawMessage{}
						mutation.emptyDataNoops++
					} else if len(cred.Data) == 0 {
						cred.Data = nil
						mutation.emptyDataNoops++
					} else if !cred.ValidUntil.IsZero() {
						_, offset := cred.ValidUntil.Zone()
						cred.ValidUntil = cred.ValidUntil.In(time.FixedZone("alternate", offset+3600))
						mutation.timezoneNoops++
					}
				}
			}
			entry.creds = append(entry.creds, cred)
		}
		if rng.Intn(4) == 0 {
			entry.creds = append(entry.creds, plugin.UserCred{CredID: fmt.Sprintf("cred-added-%d-%s", iteration, inboundID),
				UserID: "user-added", DeviceID: "device-added", Data: json.RawMessage(`{"v":"added"}`)})
		}
		sort.Slice(entry.creds, func(i, j int) bool { return entry.creds[i].CredID < entry.creds[j].CredID })
		next.in[inboundID] = &entry
	}
	if rng.Intn(4) == 0 {
		id := fmt.Sprintf("inb-added-%d", iteration)
		cred := plugin.UserCred{CredID: fmt.Sprintf("cred-added-inbound-%d", iteration), UserID: "user-added", DeviceID: "device-added",
			Data: json.RawMessage(`{"v":"added-inbound"}`)}
		next.in[id] = &inboundState{spec: plugin.InboundSpec{ID: id, Protocol: "fakehy", Enabled: true}, specHash: "added-spec", creds: []plugin.UserCred{cred}}
	}
	mutation.state = next
	return mutation
}

func mutateDigestTestCredential(rng *rand.Rand, cred *plugin.UserCred) {
	switch rng.Intn(5) {
	case 0:
		cred.UserID += "-changed"
	case 1:
		cred.DeviceID += "-changed"
	case 2:
		cred.Data = json.RawMessage(`{"v":"changed"}`)
	case 3:
		cred.RateLimitBps++
	case 4:
		if cred.ValidUntil.IsZero() {
			cred.ValidUntil = time.Unix(1, 0).UTC()
		} else {
			cred.ValidUntil = cred.ValidUntil.Add(time.Second)
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
			data := json.RawMessage(fmt.Sprintf(`{"v":%d}`, rng.Intn(5)))
			switch rng.Intn(3) {
			case 0:
				data = nil
			case 1:
				data = json.RawMessage{}
			}
			validUntil := time.Time{}
			if rng.Intn(3) != 0 {
				seconds := int64(rng.Intn(1_000_000))
				validUntil = time.Unix(seconds, int64(rng.Intn(1_000_000_000))).In(time.FixedZone("test", (rng.Intn(25)-12)*3600))
			}
			entry.creds = append(entry.creds, plugin.UserCred{
				CredID: fmt.Sprintf("cred-%d", cred), UserID: fmt.Sprintf("user-%d", rng.Intn(5)), DeviceID: fmt.Sprintf("device-%d", rng.Intn(5)),
				Data: data, RateLimitBps: uint64(rng.Intn(3_000_000)), ValidUntil: validUntil,
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
