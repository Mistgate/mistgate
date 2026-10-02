package doctor

import (
	"context"
	"testing"
)

func awgIn(state string) Inbound {
	return Inbound{ID: "inb_awg", Protocol: "awg", Enabled: true, Network: "udp", Port: 51842, State: state}
}

func withInbounds(ibs ...Inbound) func(*Env) {
	return func(e *Env) { e.Inbounds = func() []Inbound { return ibs } }
}

func TestAwgBackendCheck(t *testing.T) {
	ok := AwgBackend{Mode: "auto", Name: "userspace", Version: "amneziawg-go v3.1.20260828", Available: true}
	t.Run("skip without awg inbound", func(t *testing.T) {
		f := newFake(t)
		want(t, run(t, f.doctor(withInbounds(Inbound{ID: "h", Protocol: "hysteria2", Enabled: true}), func(e *Env) { e.AwgBackend = func() AwgBackend { return ok } }), CheckAwgBackend), Skip, "")
	})
	t.Run("skip without an engine", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(awgIn("running"))), CheckAwgBackend)
		want(t, r, Skip, "")
		code(t, r, CodeAwgNoEngine)
	})
	t.Run("userspace is fine", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(awgIn("running")), func(e *Env) { e.AwgBackend = func() AwgBackend { return ok } }), CheckAwgBackend)
		want(t, r, OK, "")
		code(t, r, CodeAwgRunning, "backend", "userspace", "mode", "auto", "version", "amneziawg-go v3.1.20260828")
	})
	t.Run("no backend on a generation 2 unit: the hint says to refresh the unit", func(t *testing.T) {
		f := newFake(t)
		b := AwgBackend{Mode: "auto", Reason: "the amneziawg kernel module is not loaded; open /dev/net/tun: no such file or directory"}
		r := run(t, f.doctor(withInbounds(awgIn("failed")), func(e *Env) {
			e.AwgBackend = func() AwgBackend { return b }
			e.UnitGen = 2
		}), CheckAwgBackend)
		want(t, r, Fail, "")
		param(t, r, "hint", "unit_outdated")
		param(t, r, "unit_gen", "2")
		code(t, r, CodeAwgUnavailable, "mode", "auto")
	})
	t.Run("no tun on an up to date unit is a host problem, not a unit problem", func(t *testing.T) {
		f := newFake(t)
		b := AwgBackend{Mode: "userspace", Reason: "open /dev/net/tun: operation not permitted"}
		r := run(t, f.doctor(withInbounds(awgIn("failed")), func(e *Env) {
			e.AwgBackend = func() AwgBackend { return b }
			e.UnitGen = 3
		}), CheckAwgBackend)
		want(t, r, Fail, "")
		param(t, r, "hint", "no_tun")
	})
	t.Run("kernel mode without the module", func(t *testing.T) {
		f := newFake(t)
		b := AwgBackend{Mode: "kernel", Reason: "the amneziawg kernel module is not loaded"}
		r := run(t, f.doctor(withInbounds(awgIn("failed")), func(e *Env) { e.AwgBackend = func() AwgBackend { return b } }), CheckAwgBackend)
		want(t, r, Fail, "")
		param(t, r, "hint", "no_module")
	})
	t.Run("a FORWARD drop policy is a warning", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(awgIn("running")), func(e *Env) {
			e.AwgBackend = func() AwgBackend { return ok }
			e.HostPath = func(context.Context) []PathFinding {
				return []PathFinding{{ID: "forward_drop", Detail: "iptables FORWARD policy is DROP"}}
			}
		}), CheckAwgBackend)
		want(t, r, Warn, "")
		param(t, r, "hint", "docker_forward_drop")
		code(t, r, CodeAwgForwardDrop, "backend", "userspace")
	})
}

func TestWarpPathCheck(t *testing.T) {
	via := Inbound{ID: "inb_h", Protocol: "hysteria2", Enabled: true, Egress: "warp", State: "running"}
	warpEnv := func(w WarpInfo, fs ...PathFinding) func(*Env) {
		return func(e *Env) {
			e.Warp = func(context.Context) WarpInfo { return w }
			e.HostPath = func(context.Context) []PathFinding { return fs }
		}
	}
	t.Run("skip when WARP is not used", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(warpEnv(WarpInfo{})), CheckWarpPath), CodeWarpUnused)
	})
	t.Run("skip without a manager", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(withInbounds(via)), CheckWarpPath), CodeWarpNoManager)
	})
	t.Run("an inbound needs WARP and the node has none", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{})), CheckWarpPath)
		want(t, r, Fail, "")
		param(t, r, "hint", "not_configured")
		param(t, r, "inbounds", "inb_h")
		code(t, r, CodeWarpNoAccount)
	})
	t.Run("up", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{Configured: true, State: "up", Backend: "kernel", Colo: "FRA"})), CheckWarpPath)
		want(t, r, OK, "")
		code(t, r, CodeWarpUp, "colo", "FRA", "backend", "kernel", "state", "up")
	})
	t.Run("starting is not an alarm", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{Configured: true, State: "starting"})), CheckWarpPath), CodeWarpStarting)
	})
	t.Run("down carries the last error", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{Configured: true, State: "down", LastError: "probe_other_failed"})), CheckWarpPath)
		want(t, r, Fail, FixReconnectWarp)
		param(t, r, "error", "probe_other_failed")
		code(t, r, CodeWarpDown)
	})
	t.Run("unavailable", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{Configured: true, State: "unavailable"})), CheckWarpPath)
		want(t, r, Fail, "")
		param(t, r, "hint", "no_backend")
		code(t, r, CodeWarpNoBackend)
	})
	t.Run("paused with dependants warns, paused alone is fine", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{Configured: true, State: "disabled"})), CheckWarpPath)
		want(t, r, Warn, "")
		param(t, r, "hint", "paused")
		code(t, r, CodeWarpPausedUsed, "inbounds", "inb_h")
		code(t, run(t, f.doctor(warpEnv(WarpInfo{Configured: true, State: "disabled"})), CheckWarpPath), CodeWarpPaused)
	})
	t.Run("a clash of the routing table stops WARP", func(t *testing.T) {
		f := newFake(t)
		r := run(t, f.doctor(withInbounds(via), warpEnv(WarpInfo{Configured: true, State: "starting"},
			PathFinding{ID: "table_in_use", Detail: "table 51820 holds routes of another tool"})), CheckWarpPath)
		want(t, r, Fail, "")
		param(t, r, "hint", "table_in_use")
		code(t, r, CodeWarpHostClash)
	})
}

func TestKernelAwgSocketIsOurs(t *testing.T) {
	// The kernel backend holds the UDP port with a kernel socket: inode 0, no process. It is ours while the inbound runs,
	// and a stranger's when the inbound is not running.
	row := "  1: 00000000:CA82 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 0 2 0\n"
	f := newFake(t)
	f.put("/proc/net/udp", udpTable(row))
	f.put("/proc/net/tcp", tcpTable(""))
	want(t, run(t, f.doctor(withInbounds(awgIn("running"))), CheckPortConflicts), OK, "")

	f2 := newFake(t)
	f2.put("/proc/net/udp", udpTable(row))
	f2.put("/proc/net/tcp", tcpTable(""))
	h := awgIn("failed")
	h.Error = "address already in use"
	want(t, run(t, f2.doctor(withInbounds(h)), CheckPortConflicts), Fail, "")
}

func TestOurNftTablesAreNotForeign(t *testing.T) {
	f := newFake(t)
	f.bin("nft")
	f.cmd("nft -j list ruleset", `{"nftables":[`+nftOurs+`,
{"table":{"family":"inet","name":"mistgate_awg","handle":2}},
{"chain":{"family":"inet","table":"mistgate_awg","name":"post","handle":1,"type":"nat","hook":"postrouting","prio":100,"policy":"accept"}},
{"table":{"family":"inet","name":"mistgate_warp","handle":3}},
{"chain":{"family":"inet","table":"mistgate_warp","name":"post","handle":1,"type":"nat","hook":"postrouting","prio":100,"policy":"accept"}}]}`, nil)
	code(t, run(t, f.doctor(), CheckForeignNft), CodeNftClean)
}
