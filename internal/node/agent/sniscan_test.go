package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/sniscan"
)

// sni-scan/1: SniScan (agent.proto "NEIGHBOR SITES"). The scanner itself is tested in internal/node/sniscan; here the
// command: the capability, the answer, the refusals and that a scan lives and dies with the session.

func sniMessage(id, address string) *pb.ConnectResponse {
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_SniScan{SniScan: &pb.SniScan{RequestId: id, Address: address}}}
}

func askSni(h *harness, id, address string) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(sniMessage(id, address))
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("scan answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func TestSniScanCapabilityIsAdvertised(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if got := (<-h.panel.hellos).Capabilities; !contains(got, "sni-scan/1") {
		t.Fatalf("capabilities %v do not list sni-scan/1", got)
	}
}

func TestSniScanAnswersWithCountsAndCandidates(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	var got netip.Addr
	h.a.sniScan.scan = func(_ context.Context, addr netip.Addr) (*sniscan.Result, error) {
		got = addr
		return &sniscan.Result{Addresses: 253, Open: 41, TLS13: 19, MLKEM: 7, Valid: 4, Partial: true,
			Candidates: []sniscan.Candidate{{IP: "192.0.2.20", Name: "shop.example.com", DNS: sniscan.DNSHere, H2: true, SingleSite: true, HTTP: 200, Ms: 12, Days: 60}}}, nil
	}
	r := askSni(h, "scan", "192.0.2.10")
	if !r.Ok || got != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("answer %+v, scanned %v", r, got)
	}
	for k, v := range map[string]string{"addresses": "253", "open": "41", "tls13": "19", "mlkem": "7", "valid": "4", "partial": "1"} {
		if r.Params[k] != v {
			t.Errorf("params[%q] = %q, want %q", k, r.Params[k], v)
		}
	}
	var cands []map[string]any
	if err := json.Unmarshal([]byte(r.Params["candidates"]), &cands); err != nil || len(cands) != 1 || cands[0]["name"] != "shop.example.com" || cands[0]["ip"] != "192.0.2.20" {
		t.Fatalf("candidates %q: %v", r.Params["candidates"], err)
	}
	if len(r.Params) != 7 {
		t.Errorf("params %v", r.Params)
	}
}

func TestSniScanRefusals(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	for _, addr := range []string{"", "not an address", "2001:db8::1", "192.0.2.0/24", "192.0.2.10 "} {
		h.a.sniScan.scan = func(context.Context, netip.Addr) (*sniscan.Result, error) {
			t.Errorf("%q reached the scanner", addr)
			return nil, nil
		}
		if r := askSni(h, "bad", addr); r.Ok || r.Error != "bad_params" {
			t.Errorf("address %q: %+v, want bad_params", addr, r)
		}
	}
	for want, err := range map[string]error{
		"not_my_address": sniscan.ErrNotMyAddress, "no_roots": sniscan.ErrNoRoots, "bad_params": sniscan.ErrBadAddress,
		"failed: some error": errors.New("some\nerror"),
	} {
		h.a.sniScan.scan = func(context.Context, netip.Addr) (*sniscan.Result, error) { return nil, err }
		if r := askSni(h, "err", "192.0.2.10"); r.Ok || r.Error != want {
			t.Errorf("error %v: %+v, want %q", err, r, want)
		}
	}
}

func TestSniScanIsOneAtATimeAndNotDuringASpeedTest(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	entered, release := make(chan struct{}), make(chan struct{})
	h.a.sniScan.scan = func(context.Context, netip.Addr) (*sniscan.Result, error) {
		close(entered)
		<-release
		return &sniscan.Result{}, nil
	}
	h.panel.send(sniMessage("first", "192.0.2.10"))
	<-entered
	if r := askSni(h, "second", "192.0.2.10"); r.Ok || r.Error != "busy" {
		t.Fatalf("a second scan: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "first" || !r.Ok || r.Params["candidates"] != "[]" {
		t.Fatalf("the first scan: %+v", r)
	}

	h.a.measuring.Store(true)
	h.a.sniScan.scan = func(context.Context, netip.Addr) (*sniscan.Result, error) {
		t.Error("scanned during a bandwidth test")
		return &sniscan.Result{}, nil
	}
	if r := askSni(h, "during", "192.0.2.10"); r.Ok || r.Error != "busy" {
		t.Fatalf("a scan during a test: %+v", r)
	}
	h.a.measuring.Store(false)
}

func TestSniScanEndsWithTheStreamAndSendsNothing(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	entered, ended := make(chan struct{}), make(chan error, 1)
	h.a.sniScan.scan = func(ctx context.Context, _ netip.Addr) (*sniscan.Result, error) {
		close(entered)
		<-ctx.Done()
		ended <- ctx.Err()
		return nil, ctx.Err()
	}
	h.panel.send(sniMessage("cut", "192.0.2.10"))
	<-entered
	h.panel.dropConn()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the scan ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the scan outlived the stream")
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.a.sniScan.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the scan is still marked as running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
