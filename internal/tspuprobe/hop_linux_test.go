//go:build linux

package tspuprobe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// The real socket against loopback: what a SYN gets back when the "network" is this host. A Time Exceeded needs a
// router in the way; that is proved by scripts/e2e/tspu.sh in namespaces.
func TestSynHopOnLoopback(t *testing.T) {
	if !Supported() {
		t.Skip("this kernel refuses IP_TTL or IP_RECVERR on a TCP socket")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	open := netip.MustParseAddrPort(l.Addr().String())
	closedL, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := netip.MustParseAddrPort(closedL.Addr().String())
	closedL.Close()

	if h, err := synHop(context.Background(), open, 64, time.Second); err != nil || h != hopOpen {
		t.Fatalf("listening port: %v, %v, want hopOpen", h, err)
	}
	if h, err := synHop(context.Background(), closed, 64, time.Second); err != nil || h != hopRefused {
		t.Fatalf("closed port: %v, %v, want hopRefused", h, err)
	}
}

func TestSynHopStopsWhenTheContextDoes(t *testing.T) {
	if !Supported() {
		t.Skip("unsupported")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// a cancelled run sends nothing (this test must not put a packet on the network of the machine it runs on)
	if h, err := synHop(ctx, netip.MustParseAddrPort("192.0.2.1:443"), 1, 5*time.Second); !errors.Is(err, context.Canceled) || h != hopSilent {
		t.Fatalf("synHop = %v, %v, want the context error", h, err)
	}
}
