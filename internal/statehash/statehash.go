package statehash

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/plugin"
)

const us = "\x1f"

func hexSum(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
func u(n uint64) string { return strconv.FormatUint(n, 10) }

// Spec hashes the fields listed in agent.proto. settings_json is hashed as the exact bytes received.
// Tunnel (L3 protocols) is appended only when it is set, so the hash of every spec without one is exactly
// what it was before Tunnel existed.
func Spec(s plugin.InboundSpec) string {
	parts := []string{
		s.ID, s.Protocol, s.ProfileID, u(s.Version), b2s(s.Enabled),
		s.Listen.Network, u(uint64(s.Listen.Port)), u(uint64(s.Listen.HopFrom)), u(uint64(s.Listen.HopTo)),
		strconv.Itoa(int(s.TLS.Mode)), s.TLS.ServerName, s.Egress, string(s.Settings),
	}
	if !s.Tunnel.IsZero() {
		parts = append(parts, prefix(s.Tunnel.AddrV4), prefix(s.Tunnel.AddrV6), u(uint64(s.Tunnel.MTU)))
	}
	return hexSum(strings.Join(parts, us))
}

// prefix renders an address prefix for hashing; an unset one is "".
func prefix(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}
	return p.String()
}

// Warp hashes the node-level WARP configuration (agent.proto, STATE HASH). Ports are joined by ",", reserved
// is lowercase hex; every other field is hashed as it stands.
func Warp(w plugin.WarpSpec) string {
	ports := make([]string, len(w.Ports))
	for i, p := range w.Ports {
		ports[i] = u(uint64(p))
	}
	return hexSum(strings.Join([]string{
		b2s(w.Enabled), w.PrivateKey, w.PeerPublicKey, w.EndpointV4, w.EndpointV6, strings.Join(ports, ","),
		w.AddressV4, w.AddressV6, u(uint64(w.MTU)), hex.EncodeToString(w.Reserved), w.Backend,
	}, us))
}

func Creds(cs []plugin.UserCred) string {
	lines := make([]string, len(cs))
	for i, c := range cs {
		var vu int64
		if !c.ValidUntil.IsZero() {
			vu = c.ValidUntil.Unix()
		}
		lines[i] = c.CredID + us + hexSum(string(c.Data)) + us + u(c.RateLimitBps) + us + strconv.FormatInt(vu, 10)
		if c.UserRateLimitBps != 0 { // appended only when set: every hash without a person limit is what it was before
			lines[i] += us + u(c.UserRateLimitBps)
		}
	}
	sort.Strings(lines)
	return hexSum(strings.Join(lines, "\n"))
}

// Inbound is one inbound as a node holds it (also what Engine.Observed returns).
type Inbound struct {
	Spec  plugin.InboundSpec
	Creds []plugin.UserCred
}

func State(in []Inbound) string { return StateWarp(in, nil) }

// StateWarp is State plus the node-level WARP configuration. With warp == nil the result is State(in) byte for
// byte; otherwise one extra line "warp" + "\x1f" + Warp(*warp) follows the sorted inbound lines (it is never
// sorted among them: an inbound line has three fields, this one has two).
func StateWarp(in []Inbound, warp *plugin.WarpSpec) string { return StateRelays(in, warp, nil) }

// Relay is the hash line of one forward: "relay" + "\x1f" + join("\x1f", id, network, port, to_host, to_port).
// It has six fields, so it can never be mistaken for an inbound line (three) or the warp line (two).
//
// A reverse-relay forward (a mode other than "" and "dnat", or an exit_node_id) adds two fields, mode and exit_node_id, so the
// line of a plain forward is what it was before the bridge existed; a mode of "dnat" counts as "".
func Relay(r plugin.RelayForward) string {
	line := []string{"relay", r.ID, r.Network, u(uint64(r.Port)), r.ToHost, u(uint64(r.ToPort))}
	mode := r.Mode
	if mode == "dnat" {
		mode = ""
	}
	if mode != "" || r.ExitNodeID != "" {
		line = append(line, mode, r.ExitNodeID)
	}
	return strings.Join(line, us)
}

// BridgeServe is the hash line of the entry side of the bridge: "bridge_serve" + "" + join("", port, path,
// join(",", sorted exit_node_ids), panel_via).
func BridgeServe(b plugin.BridgeServe) string {
	ids := append([]string(nil), b.ExitNodeIDs...)
	sort.Strings(ids)
	return strings.Join([]string{"bridge_serve", u(uint64(b.Port)), b.Path, strings.Join(ids, ","), b2s(b.PanelVia)}, us)
}

// BridgeDial is the hash line of one entry an exit calls: "bridge_dial" + "" + join("", entry_node_id, host, port, sni,
// pin_sha256, path, pool, panel_via, join(",", targets as relay_id:network:port, sorted)).
func BridgeDial(d plugin.BridgeDial) string {
	targets := make([]string, len(d.Targets))
	for i, t := range d.Targets {
		targets[i] = t.RelayID + ":" + t.Network + ":" + u(uint64(t.Port))
	}
	sort.Strings(targets)
	return strings.Join([]string{"bridge_dial", d.EntryNodeID, d.Host, u(uint64(d.Port)), d.SNI, d.PinSHA256, d.Path, u(uint64(d.Pool)),
		b2s(d.PanelVia), strings.Join(targets, ",")}, us)
}

// StateRelays is StateWarp plus the forwarded ports: one Relay line each, sorted among themselves, after the warp line.
// An empty list adds nothing, so the result is StateWarp(in, warp) byte for byte, and a delta that clears the list and a
// full state without one hash alike.
func StateRelays(in []Inbound, warp *plugin.WarpSpec, relays []plugin.RelayForward) string {
	return StateBridge(in, warp, relays, nil, nil)
}

// StateBridge is StateRelays plus the bridge of the reverse relays: the bridge_serve line (only when serve is set and not empty),
// then one bridge_dial line per dial, sorted among themselves, after the relay lines. A nil or empty serve and no dials add
// nothing, so the result is StateRelays(in, warp, relays) byte for byte.
func StateBridge(in []Inbound, warp *plugin.WarpSpec, relays []plugin.RelayForward, serve *plugin.BridgeServe, dials []plugin.BridgeDial) string {
	lines := make([]string, len(in))
	for i, x := range in {
		lines[i] = x.Spec.ID + us + Spec(x.Spec) + us + Creds(x.Creds)
	}
	sort.Strings(lines)
	if warp != nil {
		lines = append(lines, "warp"+us+Warp(*warp))
	}
	rl := make([]string, len(relays))
	for i, r := range relays {
		rl[i] = Relay(r)
	}
	sort.Strings(rl)
	lines = append(lines, rl...)
	if serve != nil && !serve.IsZero() {
		lines = append(lines, BridgeServe(*serve))
	}
	dl := make([]string, len(dials))
	for i, d := range dials {
		dl[i] = BridgeDial(d)
	}
	sort.Strings(dl)
	lines = append(lines, dl...)
	return hexSum(strings.Join(lines, "\n"))
}
