package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
)

// The names the host layer, the doctor and the unit cleanup use for what the engines create are written in three
// packages; this is the one place that sees all of them.
func TestNamesAreTheSameEverywhere(t *testing.T) {
	if hostctl.NftWarpTable != warp.NftTable {
		t.Errorf("hostctl calls the WARP table %q, warp %q", hostctl.NftWarpTable, warp.NftTable)
	}
	if hostctl.WarpIface != warp.DefaultIface {
		t.Errorf("hostctl calls the WARP device %q, warp %q", hostctl.WarpIface, warp.DefaultIface)
	}
	if hostctl.TunnelIface(51842) != awg.IfaceName(51842) {
		t.Errorf("hostctl calls the tunnel %q, awg %q", hostctl.TunnelIface(51842), awg.IfaceName(51842))
	}
}

// Generation 3 loosens exactly one thing, the device policy, and keeps every other right the unit had.
func TestUnitGen3LoosensOnlyTheDevice(t *testing.T) {
	u, err := renderUnit("/usr/local/bin/mistgate-node", "/var/lib/mistgate-node", 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE\n", "NoNewPrivileges=yes\n", "ProtectSystem=strict\n",
		"ProtectKernelModules=yes\n", "ProtectKernelLogs=yes\n", "ProtectClock=yes\n", "RestrictNamespaces=yes\n",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK\n", "SystemCallFilter=@system-service\n", "UMask=0077\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lost %q", want)
		}
	}
	if strings.Contains(u, "PrivateDevices=yes") {
		t.Error("PrivateDevices=yes hides /dev/net/tun")
	}
	if n := strings.Count(u, "DeviceAllow="); n != 1 || !strings.Contains(u, "DevicePolicy=closed") {
		t.Errorf("the device policy must be closed with exactly one allowed device (DeviceAllow lines: %d)", n)
	}
	// ExecStopPost is ignorable ("-") and takes the same binary path as ExecStart, which is checked against unsafe characters.
	if !strings.Contains(u, "\nExecStopPost=-/usr/local/bin/mistgate-node cleanup-net\n") {
		t.Error("no ExecStopPost cleanup")
	}
}

func awgEnv() engine.Env { return engine.Env{Log: slog.New(slog.DiscardHandler)} }

// The wrapper builds the real engine late, with the setting of that moment, and rebuilds it when the setting changes.
func TestAwgEngineWrapperFollowsTheBackendSetting(t *testing.T) {
	mode := "userspace"
	w := newAwgEngine(awgEnv(), func() string { return mode })
	if w.Protocol() != "awg" {
		t.Fatal(w.Protocol())
	}
	// Nothing is built by the readers the agent calls on every batch.
	if _, err := w.Collect(context.Background()); err != nil || w.Observed() != nil || w.Health() != nil || w.AwgHealth() != nil || w.peek() != nil {
		t.Fatal("a reader built the engine")
	}
	if n, err := w.Kick(context.Background(), []string{"c"}); n != 0 || err != nil || w.Remove(context.Background(), "x") != nil {
		t.Fatal("kick/remove of an unbuilt engine")
	}
	// First use builds it with the setting of the moment.
	st := w.BackendStatus()
	if st.Mode != "userspace" || w.peek() == nil {
		t.Fatalf("status = %+v", st)
	}
	// The same setting: nothing to do. A changed one: the engine is dropped and the agent is told to apply again.
	if w.NodeSettings(context.Background(), &pb.NodeSettings{AwgBackend: "userspace"}) {
		t.Error("an unchanged setting asked for a reset")
	}
	mode = "kernel"
	if !w.NodeSettings(context.Background(), &pb.NodeSettings{AwgBackend: "kernel"}) || w.peek() != nil {
		t.Fatal("a changed setting did not drop the engine")
	}
	if st := w.BackendStatus(); st.Mode != "kernel" {
		t.Errorf("rebuilt with %q", st.Mode)
	}
	// Garbage from a newer panel is "auto", never a failure to serve.
	if got := normMode("quantum"); got != "auto" {
		t.Errorf("normMode(quantum) = %q", got)
	}
	// An engine that was never rebuilt for the same effective mode is not reset by an empty setting versus "auto".
	mode = ""
	w = newAwgEngine(awgEnv(), func() string { return mode })
	_ = w.BackendStatus()
	if w.NodeSettings(context.Background(), &pb.NodeSettings{AwgBackend: "auto"}) || w.NodeSettings(context.Background(), &pb.NodeSettings{}) {
		t.Error("auto and empty are the same mode")
	}
	if err := w.Close(context.Background()); err != nil || w.peek() != nil {
		t.Fatal("close")
	}
}

// Hello asks every engine for its version on every connect, before NodeSettings arrive: for awg that must not build the engine
// (a backend probe, with a setting that is about to change), and after a build it names the backend.
func TestAwgEngineVersionDoesNotProbe(t *testing.T) {
	w := newAwgEngine(awgEnv(), func() string { return "" })
	if v := w.Version(); v == "" || w.peek() != nil {
		t.Fatalf("Version() = %q, built = %v", v, w.peek() != nil)
	}
	_ = w.BackendStatus()
	if w.peek() == nil || w.Version() != w.peek().Version() {
		t.Error("after the build Version must name the backend")
	}
}

// The wiring of this build: the engines, the WARP manager, and an egress "warp" that is an error, never the direct one,
// while the node has no WARP account.
func TestWireHasTheL3PartsAndNoSilentDirectExit(t *testing.T) {
	hooks := &agentHooks{}
	cfg, engines, err := wire(t.TempDir(), slog.New(slog.DiscardHandler), hooks)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"hysteria2", "awg"} {
		if engines[p] == nil {
			t.Errorf("no %s engine in this build", p)
		}
	}
	if cfg.Warp == nil {
		t.Fatal("no WARP manager")
	}
	if e, err := cfg.Egress("direct"); err != nil || e == nil {
		t.Errorf("direct egress: %v", err)
	}
	if e, err := cfg.Egress(""); err != nil || e == nil {
		t.Errorf("default egress: %v", err)
	}
	if e, err := cfg.Egress("warp"); err == nil || e != nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("egress warp on a node without WARP = %v, %v", e, err)
	}
	if _, err := cfg.Egress("tor"); err == nil {
		t.Error("an unknown egress was accepted")
	}
	// The awg factory builds the lazy wrapper, which probes nothing yet.
	e, err := engines["awg"](engine.Env{Log: slog.New(slog.DiscardHandler)})
	if err != nil || e.Protocol() != "awg" {
		t.Fatalf("awg factory: %v", err)
	}
	if e.(*awgEngine).peek() != nil {
		t.Error("the awg factory probed for a backend")
	}
}
