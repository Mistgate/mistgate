package warp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/nftmissing"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

// A server without nft fails the routing reassert at every health check. The log says so once; another error, and the
// same one after the command was there again, are still said.
func TestMissingNftIsLoggedOnceNotEveryCheck(t *testing.T) {
	logs := &lockedBuffer{}
	pl := &fakePlane{}
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	m := newManager(Options{
		Log: slog.New(slog.NewTextHandler(logs, nil)), Now: clk.now, AllowPrivate: true,
	}, pl, &fakeProbe{flag: "on", colo: "FRA"})
	ctx := context.Background()
	if err := m.Apply(ctx, spec()); err != nil {
		t.Fatal(err)
	}
	tick := func() {
		clk.add(40 * time.Second)
		pl.mu.Lock()
		pl.hs = clk.now()
		pl.mu.Unlock()
		m.poll(ctx)
	}
	const line = "warp routing reassert failed"

	pl.reassertErr = fmt.Errorf("%w: masquerade for WARP cannot be set up", nftmissing.ErrMissing)
	for range 5 {
		tick()
	}
	if got := logs.count(line); got != 1 {
		t.Fatalf("the missing nft was logged %d times in 5 checks, want once", got)
	}

	pl.reassertErr = errors.New("netlink: busy")
	tick()
	tick()
	if got := logs.count(line); got != 3 {
		t.Fatalf("a different error must be logged every time: %d lines", got)
	}

	pl.reassertErr = nil
	tick()
	pl.reassertErr = fmt.Errorf("%w: masquerade for WARP cannot be set up", nftmissing.ErrMissing)
	tick()
	tick()
	if got := logs.count(line); got != 4 {
		t.Fatalf("the missing nft after it worked was not logged again: %d lines", got)
	}
}
