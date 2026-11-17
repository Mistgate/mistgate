package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/mistgate/mistgate/internal/node/nftmissing"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func countCode(codes []string, code string) int {
	n := 0
	for _, c := range codes {
		if c == code {
			n++
		}
	}
	return n
}

// A server without nft fails the baseline at start and port hopping at every sweep. The agent says it once, as one log
// line and one node event, keeps the other failures, and says once more when nft is there and the steps went through.
func TestMissingNftIsSaidOnceAndWhenItIsBack(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) { c.Log = slog.New(slog.NewTextHandler(logs, nil)) }})
	missing := fmt.Errorf("%w: exec: \"nft\": executable file not found in $PATH", nftmissing.ErrMissing)
	h.host.baseErr = errors.Join(errors.New("sysctl net.core.rmem_max: read-only"), fmt.Errorf("ssh guard: %w", missing))
	h.host.hopErr = missing
	h.start()
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 20000, 29999, cred("crd_a"))))
	h.panel.nextApply()

	eventually(t, func() bool { return len(h.host.hops()) >= 5 }, "five sweeps with port hopping failing")
	codes := h.panel.eventCodes()
	if n := countCode(codes, "nft_missing"); n != 1 {
		t.Fatalf("nft_missing was raised %d times, want once: %v", n, codes)
	}
	if h.panel.hasEvent("hop_failed") || h.panel.hasEvent("nft_restored") {
		t.Fatalf("a missing nft must not read as a hop failure, nor as restored: %v", codes)
	}
	out := logs.String()
	if n := strings.Count(out, "nft is not installed on this server"); n != 1 {
		t.Fatalf("the warning was logged %d times, want once:\n%s", n, out)
	}
	if strings.Contains(out, "port hopping not installed") {
		t.Fatalf("the error line of every sweep is back:\n%s", out)
	}
	if !strings.Contains(out, "host baseline not fully applied") || !strings.Contains(out, "read-only") || strings.Contains(out, "executable file not found") {
		t.Fatalf("the baseline must keep its other failure and drop the nft part:\n%s", out)
	}

	h.host.mu.Lock()
	h.host.hopErr = nil
	h.host.mu.Unlock()
	eventually(t, func() bool { return h.panel.hasEvent("nft_restored") }, "nft_restored event")
	if n := countCode(h.panel.eventCodes(), "nft_restored"); n != 1 {
		t.Fatalf("nft_restored was raised %d times, want once", n)
	}
	if !strings.Contains(logs.String(), "nft is available again") {
		t.Fatalf("the recovery is not in the log:\n%s", logs.String())
	}
}

// Another failure of port hopping is still an error line and an event at every sweep.
func TestOtherHopFailuresAreStillLogged(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, harnessOpts{cfg: func(c *Config) { c.Log = slog.New(slog.NewTextHandler(logs, nil)) }})
	h.waitConnected()
	h.host.hopErr = errors.New("nft: Operation not supported")
	h.panel.push(fullState(1, inb("inb_1", 20000, 29999, cred("crd_a"))))
	h.panel.nextApply()
	eventually(t, func() bool { return len(h.host.hops()) >= 3 }, "sweeps")
	if h.panel.hasEvent("nft_missing") || !h.panel.hasEvent("hop_failed") || strings.Count(logs.String(), "port hopping not installed") < 2 {
		t.Fatalf("events %v, log:\n%s", h.panel.eventCodes(), logs.String())
	}
}

// The tunnel firewall needs nft too, and its sweep fails at every pass: one log line and one nft_missing event, the
// tunnel itself stays blocked until nft is there.
func TestMissingNftOnTheTunnelFirewallIsSaidOnce(t *testing.T) {
	logs := &syncBuffer{}
	x := newL3Tuned(t, 3, false, func(a *Agent) { a.log = slog.New(slog.NewTextHandler(logs, nil)) })
	x.waitConnected()
	missing := fmt.Errorf("%w: exec: \"nft\": executable file not found in $PATH", nftmissing.ErrMissing)
	x.tun.setErr(missing)
	x.host.mu.Lock()
	x.host.hopErr = missing // one binary: every firewall step of the server fails
	x.host.mu.Unlock()
	r := mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))))
	if x.awg.has("inb_a") || !strings.Contains(r.Inbounds[0].Error, "tunnel firewall") {
		t.Fatalf("an interface started without its isolation rules: %v", r)
	}
	failures := func() int {
		x.ord.mu.Lock()
		defer x.ord.mu.Unlock()
		n := 0
		for _, c := range x.ord.log {
			if c == "tunnels:error" {
				n++
			}
		}
		return n
	}
	eventually(t, func() bool { return failures() >= 4 }, "four sweeps with the tunnel firewall failing")
	if c := countCode(x.panel.eventCodes(), "nft_missing"); c != 1 {
		t.Fatalf("nft_missing was raised %d times, want once: %v", c, x.panel.eventCodes())
	}
	if strings.Contains(logs.String(), "tunnel firewall not installed") || strings.Count(logs.String(), "nft is not installed on this server") != 1 {
		t.Fatalf("the error line of every sweep is back:\n%s", logs.String())
	}
}
