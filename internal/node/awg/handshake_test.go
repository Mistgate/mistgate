package awg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// countingFake is the fake backend with the userspace backend's handshake counts (handshakeSource).
type countingFake struct {
	*fakeBackend
	hmu    sync.Mutex
	counts map[[32]byte]handshakeCounts
}

func (f *countingFake) add(key [32]byte, initiations, responses uint32) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	c := f.counts[key]
	c.initiations += initiations
	c.responses += responses
	f.counts[key] = c
}

func (f *countingFake) TakeHandshakes(_ string, keys [][32]byte) map[[32]byte]handshakeCounts {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	out := map[[32]byte]handshakeCounts{}
	for _, k := range keys {
		if c, ok := f.counts[k]; ok {
			out[k] = c
		}
	}
	f.counts = map[[32]byte]handshakeCounts{}
	return out
}

func handshakeLinks(c engine.Collected) map[string]plugin.LinkStat {
	out := map[string]plugin.LinkStat{}
	for _, s := range c.Links {
		out[s.CredID] = s
	}
	return out
}

// Initiations and responses go to the key; a response the device confirms (its last handshake moves) is matched, one that
// is still unconfirmed a round later is not. The kernel backend has no handshake source: nothing is counted.
func TestCollectHandshakeCounts(t *testing.T) {
	fb := &countingFake{fakeBackend: newFake(), counts: map[[32]byte]handshakeCounts{}}
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	e := newEngine(engine.Env{Now: clk.Now}, fb, BackendStatus{Mode: "auto", Name: "userspace", Version: fb.Version(), Available: true})
	e.portListening = func(uint16) bool { return true }
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	mustApply(t, e, spec(t, "inb1", 51842, nil), cred("a", 0x0a, 5), cred("b", 0x0b, 6))
	ctx := context.Background()
	if _, err := e.Collect(ctx); err != nil { // the baseline round
		t.Fatal(err)
	}

	// a: one initiation answered and confirmed in the same round; b: answered, not confirmed yet
	fb.add(pk(0x0a), 1, 1)
	fb.add(pk(0x0b), 1, 1)
	clk.Add(10 * time.Second)
	fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0a)].hs = clk.Now() })
	c, err := e.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := handshakeLinks(c)
	if a := got["a"]; a.Initiations != 1 || a.Responses != 1 || a.ResponsesUnmatched != 0 || a.Connects != 1 {
		t.Fatalf("a = %+v; want one initiation, one confirmed response, a connect", a)
	}
	if b := got["b"]; b.Initiations != 1 || b.Responses != 1 || b.ResponsesUnmatched != 0 {
		t.Fatalf("b = %+v; a response waits one round for its confirmation", b)
	}

	// b retries twice, nothing ever confirms: the first response is lost now, the two new ones wait
	fb.add(pk(0x0b), 2, 2)
	clk.Add(10 * time.Second)
	got = handshakeLinks(must(e.Collect(ctx)))
	if b := got["b"]; b.Initiations != 2 || b.Responses != 2 || b.ResponsesUnmatched != 1 {
		t.Fatalf("b = %+v; want the first response unmatched", b)
	}
	// then a handshake completes: of the two waiting responses the newer is the confirmed one, the other was lost
	clk.Add(10 * time.Second)
	fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0b)].hs = clk.Now() })
	got = handshakeLinks(must(e.Collect(ctx)))
	if b := got["b"]; b.ResponsesUnmatched != 1 || b.Responses != 0 || b.Connects != 1 {
		t.Fatalf("b = %+v; want one more unmatched response and the connect", b)
	}
	// a peer that initiated and got no answer at all: our side (initiations without responses)
	fb.add(pk(0x0a), 3, 0)
	clk.Add(10 * time.Second)
	got = handshakeLinks(must(e.Collect(ctx)))
	if a := got["a"]; a.Initiations != 3 || a.Responses != 0 || a.ResponsesUnmatched != 0 {
		t.Fatalf("a = %+v; want three initiations and no response", a)
	}

	// the plain fake (the kernel backend's shape) counts none of it
	plain, pfb, pclk := newTestEngine(t)
	mustApply(t, plain, spec(t, "inb1", 51842, nil), cred("a", 0x0a, 5))
	_, _ = plain.Collect(ctx)
	pclk.Add(10 * time.Second)
	pfb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0a)].hs = pclk.Now() })
	for _, s := range must(plain.Collect(ctx)).Links {
		if s.Initiations|s.Responses|s.ResponsesUnmatched != 0 {
			t.Fatalf("a backend without handshake counts reported some: %+v", s)
		}
	}
}

func must(c engine.Collected, err error) engine.Collected {
	if err != nil {
		panic(err)
	}
	return c
}

func TestHandshakeRound(t *testing.T) {
	for _, c := range []struct {
		name                   string
		pending, responses     uint32
		advanced               bool
		unmatched, nextPending uint32
	}{
		{"nothing", 0, 0, false, 0, 0},
		{"answered and confirmed", 0, 1, true, 0, 0},
		{"answered, waits", 0, 1, false, 0, 1},
		{"waited, confirmed now", 1, 0, true, 0, 0},
		{"waited in vain", 1, 2, false, 1, 2},
		{"retries, the last confirmed", 2, 1, true, 2, 0},
		{"the node initiated", 0, 0, true, 0, 0},
	} {
		unmatched, next := handshakeRound(c.pending, c.responses, c.advanced)
		if unmatched != c.unmatched || next != c.nextPending {
			t.Errorf("%s: unmatched %d, pending %d; want %d, %d", c.name, unmatched, next, c.unmatched, c.nextPending)
		}
	}
}

func TestHandshakeLogKinds(t *testing.T) {
	for format, want := range map[string]int{
		"%v - Received handshake initiation":                   1,
		"%v - ConsumeMessageInitiation: handshake replay @ %v": 1,
		"%v - ConsumeMessageInitiation: handshake flood":       1,
		"%v - Sending handshake response":                      2,
		"%v - Failed to create response message: %v":           -2,
		"%v - Failed to send handshake response: %v":           -2,
		"%v - Received handshake response":                     0,
		"Received invalid initiation message from %s":          0,
		"%v - Sending keepalive packet":                        0,
	} {
		if got := handshakeLogKind(format); got != want {
			t.Errorf("%q = %d, want %d", format, got, want)
		}
	}
}
