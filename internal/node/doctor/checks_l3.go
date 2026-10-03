package doctor

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// The two checks of the L3 protocols: awg_backend and warp_path. A down WARP tunnel can be reconnected with its
// current account; host collisions, missing configuration, and unavailable WireGuard backends still need owner action.
// Missing /dev/net/tun is the unit's (run `mistgate-node install`), and a missing kernel module is an explicit owner
// action (`mistgate-node awg prepare-kernel`). Installing packages or rewriting the firewall is not in the safe set.

// AwgBackend is the choice of the AmneziaWG backend as the engine made it (awg.BackendStatus).
type AwgBackend struct {
	Mode      string // what NodeSettings.awg_backend asked: auto | kernel | userspace
	Name      string // "kernel" | "userspace"; "" when unavailable
	Version   string
	Available bool
	Reason    string // when unavailable: a short English fact
}

// WarpInfo is the WARP manager's view at the time of the check.
type WarpInfo struct {
	Configured bool // the node has a WarpSpec (paused included)
	// State: "none" | "unavailable" | "starting" | "up" | "down" | "disabled" (warp.State.String()).
	State         string
	Backend       string
	Endpoint      string
	Colo          string
	LastError     string
	LastHandshake time.Time
}

// PathFinding is one clash of the host with the tunnel path (warp.Finding): "table_in_use" | "rule_pref_in_use" |
// "interface_in_use" | "forward_drop".
type PathFinding struct{ ID, Detail string }

func awgInboundCount(e *Env) int {
	n := 0
	if e.Inbounds == nil {
		return 0
	}
	for _, in := range e.Inbounds() {
		if in.Enabled && in.Protocol == "awg" {
			n++
		}
	}
	return n
}

// awg_backend: the backend the AmneziaWG engine runs, or why it has none.
func checkAwgBackend(ctx context.Context, e *Env) Result {
	if awgInboundCount(e) == 0 {
		return skip(CodeAwgNone, "no AmneziaWG inbound on this node")
	}
	if e.AwgBackend == nil {
		return skip(CodeAwgNoEngine, "this build has no AmneziaWG engine")
	}
	b := e.AwgBackend()
	if !b.Available {
		r := Result{Status: Fail, Code: CodeAwgUnavailable, Params: p("mode", orStr(b.Mode, "auto"), "reason", clip(b.Reason, 160)),
			Detail: clip("no AmneziaWG backend: "+b.Reason, 200)}
		if strings.Contains(b.Reason, "/dev/net/tun") || strings.Contains(b.Reason, "tun") {
			r.Params["hint"] = "no_tun"
			if e.UnitGen > 0 && e.UnitGen < 3 {
				r.Params["hint"] = "unit_outdated" // the unit of generation 2 hides /dev/net/tun (PrivateDevices=yes)
				r.Params["unit_gen"] = strconv.Itoa(e.UnitGen)
			}
		} else if b.Mode == "kernel" {
			r.Params["hint"] = "no_module"
		}
		return r
	}
	r := Result{Status: OK, Code: CodeAwgRunning, Params: p("backend", b.Name, "version", clip(b.Version, 80), "mode", orStr(b.Mode, "auto")),
		Detail: clip(b.Name+": "+b.Version, 200)}
	if e.HostPath != nil {
		for _, f := range e.HostPath(ctx) {
			if f.ID == "forward_drop" { // forwarded client traffic is dropped before it reaches the internet
				r.Status, r.Code = Warn, CodeAwgForwardDrop
				r.Params["hint"] = "docker_forward_drop"
				r.Detail = clip("forwarded traffic is dropped: "+f.Detail, 200)
			}
		}
	}
	return r
}

// warp_path: the WARP tunnel and its routes, for a node that has a WARP account or an inbound that needs one.
func checkWarpPath(ctx context.Context, e *Env) Result {
	if e.Warp == nil {
		return skip(CodeWarpNoManager, "this build has no WARP manager")
	}
	var needs []string
	if e.Inbounds != nil {
		for _, in := range e.Inbounds() {
			if in.Enabled && in.Egress == "warp" {
				needs = append(needs, in.ID)
			}
		}
	}
	w := e.Warp(ctx)
	if !w.Configured && len(needs) == 0 {
		return skip(CodeWarpUnused, "WARP is not used on this node")
	}
	if !w.Configured {
		return Result{Status: Fail, Code: CodeWarpNoAccount, Params: p("hint", "not_configured", "inbounds", strings.Join(firstN(needs, maxNames), ",")),
			Detail: clip("inbound(s) with egress warp are not started, the node has no WARP account: "+strings.Join(firstN(needs, maxNames), ", "), 200)}
	}
	if e.HostPath != nil {
		for _, f := range e.HostPath(ctx) {
			switch f.ID {
			case "table_in_use", "rule_pref_in_use", "interface_in_use":
				return Result{Status: Fail, Code: CodeWarpHostClash, Params: p("hint", f.ID), Detail: clip("WARP cannot use the host: "+f.Detail, 200)}
			}
		}
	}
	params := p("state", w.State, "backend", w.Backend)
	if w.Colo != "" {
		params["colo"] = w.Colo
	}
	if (w.State == "up" || w.State == "starting") && warpHasCurrentFailure(w.LastError) {
		params["error"] = clip(w.LastError, 120)
		return Result{Status: Warn, Code: CodeWarpCheckFailed, Params: params,
			FixID: FixReconnectWarp, Detail: clip(w.State+" but the latest WARP check failed: "+w.LastError, 200)}
	}
	switch w.State {
	case "up":
		d := "up"
		if w.Backend != "" {
			d += " via " + w.Backend
		}
		if w.Colo != "" {
			d += ", " + w.Colo
		}
		return Result{Status: OK, Code: CodeWarpUp, Params: params, Detail: clip(d, 200)}
	case "starting":
		return Result{Status: OK, Code: CodeWarpStarting, Params: params, Detail: "starting"}
	case "unavailable":
		params["hint"] = "no_backend"
		return Result{Status: Fail, Code: CodeWarpNoBackend, Params: params, Detail: "no WireGuard backend (kernel module or /dev/net/tun)"}
	case "disabled":
		if len(needs) > 0 { // the owner paused WARP while inbounds depend on it: they fail closed
			params["hint"] = "paused"
			params["inbounds"] = strings.Join(firstN(needs, maxNames), ",")
			return Result{Status: Warn, Code: CodeWarpPausedUsed, Params: params, Detail: clip("paused; inbounds that use it fail closed: "+strings.Join(firstN(needs, maxNames), ", "), 200)}
		}
		return Result{Status: OK, Code: CodeWarpPaused, Params: params, Detail: "paused"}
	case "down":
		if w.LastError != "" {
			params["error"] = clip(w.LastError, 120)
		}
		return Result{Status: Fail, Code: CodeWarpDown, FixID: FixReconnectWarp, Params: params, Detail: clip("down: "+orStr(w.LastError, "no handshake"), 200)}
	}
	return Result{Status: Warn, Code: CodeWarpUnknown, Params: params, Detail: "unknown state " + w.State}
}

// Health.LastError describes the latest check and is empty after a passing check. A ladder-only note records a
// recovery action, not a failed check, so it must not turn an otherwise healthy path into a warning.
func warpHasCurrentFailure(lastError string) bool {
	reason := strings.TrimSpace(lastError)
	if i := strings.Index(reason, "; ladder: "); i >= 0 {
		reason = strings.TrimSpace(reason[:i])
	}
	return reason != "" && !strings.HasPrefix(reason, "ladder: ")
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
