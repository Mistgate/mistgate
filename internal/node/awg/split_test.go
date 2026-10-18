package awg

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/engine"
)

// splitBackend reports every peer with several allowed ips the way the kernel does when a dump message ends between
// them: the peer, then the same peer again with only its key, zero counters and the rest of its allowed ips.
type splitBackend struct{ *fakeBackend }

func (s splitBackend) Stats(ctx context.Context, name string) ([]awgcfg.PeerStat, error) {
	st, err := s.fakeBackend.Stats(ctx, name)
	var out []awgcfg.PeerStat
	for _, p := range st {
		if len(p.AllowedIPs) < 2 {
			out = append(out, p)
			continue
		}
		head, tail := p, awgcfg.PeerStat{PublicKey: p.PublicKey, AllowedIPs: p.AllowedIPs[1:]}
		head.AllowedIPs = p.AllowedIPs[:1]
		out = append(out, head, tail)
	}
	return out, err
}

func splitEngine(t *testing.T) (*Engine, *fakeBackend, *clock) {
	t.Helper()
	fb := newFake()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	e := newEngine(engine.Env{Now: clk.Now}, splitBackend{fb}, BackendStatus{Mode: "auto", Name: "kernel", Available: true})
	e.portListening = func(uint16) bool { return true }
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e, fb, clk
}

func TestObservedKeepsCredentialsWhenTheDumpSplitsAPeer(t *testing.T) {
	e, _, _ := splitEngine(t)
	var logs bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&logs, nil))
	mustApply(t, e, spec(t, "inb1", 51842, nil), cred("c1", 1, 5), cred("c2", 2, 6))
	got := e.Observed()
	if len(got) != 1 || len(got[0].Creds) != 2 {
		t.Fatalf("a split dump hides credentials from Observed: %+v", got)
	}
	if logs.Len() != 0 {
		t.Errorf("nothing is missing, nothing to warn about: %s", logs.String())
	}
}

func TestCollectDoesNotCountASplitPeerTwice(t *testing.T) {
	e, fb, clk := splitEngine(t)
	mustApply(t, e, spec(t, "inb1", 51842, nil), cred("a", 0x0a, 5))
	fb.mutate(p1, func(d *fakeDev) {
		p := d.peers[pk(0x0a)]
		p.rx, p.tx, p.hs = 1000, 4000, clk.Now().Add(-5*time.Second)
	})
	ctx := context.Background()
	c, _ := e.Collect(ctx)
	if len(c.Traffic) != 1 || c.Traffic[0].Up != 1000 || c.Traffic[0].Down != 4000 || len(c.Sessions) != 1 {
		t.Fatalf("first collect: %+v / %+v", c.Traffic, c.Sessions)
	}
	// the zero counters of the continuation must not look like a reset: the same totals again is no traffic
	if c, _ = e.Collect(ctx); len(c.Traffic) != 0 {
		t.Errorf("a split peer was counted twice: %+v", c.Traffic)
	}
}

func TestObservedWarnsOncePerApplyWhenACredentialIsMissing(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	var logs bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&logs, nil))
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("c1", 1, 5), cred("c2", 2, 6))
	fb.mutate(p1, func(d *fakeDev) { delete(d.peers, pk(2)) })

	for range 3 {
		if got := e.Observed(); len(got[0].Creds) != 1 {
			t.Fatalf("observed %d credentials, want 1", len(got[0].Creds))
		}
	}
	if n := strings.Count(logs.String(), "missing on the running interface"); n != 1 {
		t.Fatalf("want one warning per apply, got %d: %s", n, logs.String())
	}
	if l := logs.String(); !strings.Contains(l, "inbound=inb1") || !strings.Contains(l, "delivered=2") || !strings.Contains(l, "seen=1") {
		t.Errorf("warning lacks inbound or counts: %s", l)
	}

	// the next Apply (live update here) re-adds the peer and arms the warning again
	mustApply(t, e, s, cred("c1", 1, 5), cred("c2", 2, 6))
	fb.mutate(p1, func(d *fakeDev) { delete(d.peers, pk(2)) })
	e.Observed()
	if n := strings.Count(logs.String(), "missing on the running interface"); n != 2 {
		t.Errorf("a new apply must allow one more warning, got %d", n)
	}
}
