package awg

import (
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/awg/awgnl"
	"github.com/mistgate/mistgate/internal/node/engine"
)

func fakeProbes(kernelErr, userspaceErr error, limit int64) probes {
	return probes{
		kernel: func() (awgBackend, error) {
			if kernelErr != nil {
				return nil, kernelErr
			}
			f := newFake()
			f.name = "kernel"
			return f, nil
		},
		userspace: func() (awgBackend, error) {
			if userspaceErr != nil {
				return nil, userspaceErr
			}
			return newFake(), nil
		},
		release:  func() string { return "7.0.0-38-generic" },
		memLimit: func() int64 { return limit },
	}
}

func peersOf(n int) func() int { return func() int { return n } }

var quiet = slog.New(slog.DiscardHandler)

// "kernel" with a module that does not load runs on userspace and says so; the module, when it loads, is not a fallback.
func TestKernelModeFallsBackToUserspace(t *testing.T) {
	be, st := chooseBackend("kernel", quiet, fakeProbes(awgnl.ErrNoModule, nil, 600<<20), peersOf(10))
	if be == nil || !st.Available || st.Name != "userspace" || !st.Fallback || st.Mode != "kernel" || !strings.Contains(st.Reason, "not loaded") {
		t.Fatalf("be=%v st=%+v", be, st)
	}
	be, st = chooseBackend("kernel", quiet, fakeProbes(nil, nil, 600<<20), peersOf(10))
	if be == nil || st.Name != "kernel" || st.Fallback {
		t.Fatalf("a loaded module is not a fallback: %+v", st)
	}
	// "auto" has always fallen back and is not marked: nothing was asked of the module.
	_, st = chooseBackend("auto", quiet, fakeProbes(awgnl.ErrNoModule, nil, 600<<20), peersOf(10))
	if st.Name != "userspace" || st.Fallback {
		t.Fatalf("auto: %+v", st)
	}
	// Neither works: unavailable, the reason names both, and it is no fallback.
	be, st = chooseBackend("kernel", quiet, fakeProbes(awgnl.ErrNoModule, errors.New("/dev/net/tun is not usable"), 600<<20), peersOf(10))
	if be != nil || st.Available || st.Fallback || !strings.Contains(st.Reason, "not loaded") || !strings.Contains(st.Reason, "/dev/net/tun") {
		t.Fatalf("be=%v st=%+v", be, st)
	}
}

// The ceiling: peers x 26 KiB against a third of the memory limit. A 1 GiB node has GOMEMLIMIT 614 MiB (cmd/mistgate-node/unit.go),
// a third is 204 MiB, so 8060 devices fit and 8061 do not.
func TestFallbackRefusedWhenUserspaceWouldNotFit(t *testing.T) {
	limit := int64(614) << 20
	if !userspaceFits(8060, limit) || userspaceFits(8061, limit) {
		t.Fatalf("the ceiling is not 8060 devices: %v %v", userspaceFits(8060, limit), userspaceFits(8061, limit))
	}
	be, st := chooseBackend("kernel", quiet, fakeProbes(awgnl.ErrNoModule, nil, limit), peersOf(9000))
	if be != nil || st.Available || st.Fallback || !strings.Contains(st.Reason, ReasonTooManyForUserspace) || !strings.Contains(st.Reason, "not loaded") {
		t.Fatalf("be=%v st=%+v", be, st)
	}
	// Only "kernel" is held to it: auto and userspace were always userspace.
	if be, st := chooseBackend("auto", quiet, fakeProbes(awgnl.ErrNoModule, nil, limit), peersOf(9000)); be == nil || st.Name != "userspace" {
		t.Fatalf("auto is not held to the ceiling: %+v", st)
	}
	// No memory limit set (a lab): nothing to protect.
	if !userspaceFits(1_000_000, math.MaxInt64) || !userspaceFits(1_000_000, 0) {
		t.Fatal("no limit must not refuse")
	}
}

// The event comes once per engine build, with the reason and the running kernel, and not for a plain userspace choice.
func TestFallbackEventOncePerBuild(t *testing.T) {
	var got []engine.Event
	env := engine.Env{Log: quiet, Event: func(e engine.Event) { got = append(got, e) }}
	pr := fakeProbes(awgnl.ErrNoModule, nil, 600<<20)
	if _, err := NewWithOptions(env, Options{Backend: "kernel", Peers: peersOf(3), probe: &pr}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Code != EventBackendFallback || !got[0].Warning || got[0].Params["kernel"] != "7.0.0-38-generic" ||
		!strings.Contains(got[0].Params["reason"], "not loaded") {
		t.Fatalf("events: %+v", got)
	}
	got = nil
	if _, err := NewWithOptions(env, Options{Backend: "auto", probe: &pr}); err != nil || len(got) != 0 {
		t.Fatalf("auto raised an event: %v %+v", err, got)
	}
}
