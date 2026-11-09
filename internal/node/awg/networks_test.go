package awg

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
)

func TestCollectCountsLoopbackEndpointEvents(t *testing.T) {
	for _, backend := range []string{"userspace", "kernel"} {
		t.Run(backend, func(t *testing.T) {
			agg := nets.New(2)
			db, err := netstest.LoopbackDB(64500)
			if err != nil {
				t.Fatal(err)
			}
			agg.SetTable(db, "fixture-sha")
			clk := &clock{t: time.Unix(1_800_000_000, 0)}
			fb := newFake()
			var backendImpl awgBackend = fb
			if backend == "userspace" {
				backendImpl = &countingFake{fakeBackend: fb, counts: map[[32]byte]handshakeCounts{}}
			} else {
				fb.name = "kernel"
			}
			e := newEngine(engine.Env{Now: clk.Now, Nets: agg}, backendImpl, BackendStatus{Mode: "auto", Name: backend, Version: fb.Version(), Available: true})
			e.portListening = func(uint16) bool { return true }
			t.Cleanup(func() { _ = e.Close(context.Background()) })

			c := cred("crd_alice", 0x0a, 5)
			c.UserID = "alice"
			mustApply(t, e, spec(t, "inb_de1", 51842, nil), c)
			if _, err := e.Collect(context.Background()); err != nil { // The initial peer snapshot is only a baseline.
				t.Fatal(err)
			}
			clk.Add(time.Second)
			fb.mutate(p1, func(d *fakeDev) {
				p := d.peers[pk(0x0a)]
				p.hs = clk.Now()
				p.ep = netip.MustParseAddrPort("127.0.0.1:51820")
			})
			if source, ok := backendImpl.(handshakeSource); ok {
				source.(*countingFake).add(pk(0x0a), 1, 0)
			}
			if _, err := e.Collect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if backend == "kernel" {
			} else {
				counter := backendImpl.(*countingFake)
				counter.add(pk(0x0a), 0, 1)
				clk.Add(10 * time.Second)
				if _, err := e.Collect(context.Background()); err != nil { // A response waits one round for a completed handshake.
					t.Fatal(err)
				}
				clk.Add(10 * time.Second)
				if _, err := e.Collect(context.Background()); err != nil { // An unconfirmed response becomes unfinished.
					t.Fatal(err)
				}
			}
			clk.Add(200 * time.Second)
			fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0a)].rx += 10 })
			if _, err := e.Collect(context.Background()); err != nil {
				t.Fatal(err)
			}

			networks, people := agg.Take()
			wantAttempts := uint32(2)
			wantUnfinished := uint32(2)
			if backend == "kernel" {
				wantUnfinished = 1 // Kernel stats see the connect and stall, but cannot observe unmatched responses.
			}
			if len(networks) != 1 || networks[0].Asn != 0 || networks[0].Attempts != wantAttempts || networks[0].Connects != 1 || networks[0].Unfinished != wantUnfinished || networks[0].Broken != 0 || networks[0].People != 1 {
				t.Fatalf("network counters = %+v", networks)
			}
			if len(people) != 1 || people[0].CredId != "crd_alice" || people[0].Asn != 64500 || people[0].Connects != 1 {
				t.Fatalf("person counters = %+v", people)
			}
		})
	}
}
