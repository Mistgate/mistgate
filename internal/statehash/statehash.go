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
func StateWarp(in []Inbound, warp *plugin.WarpSpec) string {
	lines := make([]string, len(in))
	for i, x := range in {
		lines[i] = x.Spec.ID + us + Spec(x.Spec) + us + Creds(x.Creds)
	}
	sort.Strings(lines)
	if warp != nil {
		lines = append(lines, "warp"+us+Warp(*warp))
	}
	return hexSum(strings.Join(lines, "\n"))
}
