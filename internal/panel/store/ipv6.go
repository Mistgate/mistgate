package store

import (
	"context"
	"net/netip"
	"strings"
)

// What the panel keeps about the IPv6 of a node: the doctor's view of its uplink (DoctorIPv6) and the outside checks of an
// address (IPv6Check). The IPv6 entry itself (nodes.ipv6_entry, ipv6_json) is in ipv6_entry.go.

// States of IPv6Doctor.State.
const (
	IPv6OK       = "ok"        // outbound IPv6 works, so the node has a working IPv6 uplink
	IPv6None     = "none"      // no global IPv6 address
	IPv6Broken   = "broken"    // an address, but outbound IPv6 fails (so does the inbound one)
	IPv6Unknown  = "unknown"   // the doctor has not reported yet
	IPv6AgentOld = "agent_old" // set by the readers: IPv6 works but the agent names no stable address (Addrs is empty)
)

// IPv6Doctor is what the doctor's ipv6 check says of a node (internal/node/doctor checkIPv6).
type IPv6Doctor struct {
	State string
	Iface string // the interface of the ::/0 route
	// Src is the address the kernel picks for outbound IPv6 ("" if the agent could not say).
	Src string
	// Addrs are the stable global addresses of that interface, with the prefix length the host holds them with.
	Addrs []netip.Prefix
}

// UsableIPv6 is s as a global unicast IPv6 address, in its canonical spelling (not a unique-local, link-local or mapped IPv4 one).
func UsableIPv6(s string) (string, bool) {
	a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(s), "[]"))
	if err != nil || a.Is4In6() || !a.Is6() || !a.IsGlobalUnicast() || a.IsPrivate() {
		return "", false
	}
	return a.String(), true
}

// DoctorIPv6 reads the params of a doctor row of the ipv6 check: ipv6=no|yes, connect=ok|failed and, from agents that know the
// uplink, iface, src and addrs ("addr/plen,..."). nil = the doctor has not reported.
func DoctorIPv6(row *DoctorRow) IPv6Doctor {
	if row == nil {
		return IPv6Doctor{State: IPv6Unknown}
	}
	p := row.Params
	switch {
	case p["ipv6"] == "no":
		return IPv6Doctor{State: IPv6None}
	case p["ipv6"] == "yes" && p["connect"] == "failed":
		return IPv6Doctor{State: IPv6Broken}
	case p["ipv6"] != "yes" || p["connect"] != "ok":
		return IPv6Doctor{State: IPv6Unknown}
	}
	d := IPv6Doctor{State: IPv6OK, Iface: p["iface"]}
	d.Src, _ = UsableIPv6(p["src"])
	for _, x := range strings.Split(p["addrs"], ",") {
		pfx, err := netip.ParsePrefix(strings.TrimSpace(x))
		if err != nil || !pfx.Addr().Is6() {
			continue
		}
		if _, ok := UsableIPv6(pfx.Addr().String()); ok {
			d.Addrs = append(d.Addrs, pfx)
		}
	}
	return d
}

// FirstAddr is the address the node is measured on when nothing better is known: Src if it is stable, else the first stable address.
func (d IPv6Doctor) FirstAddr() string {
	for _, p := range d.Addrs {
		if p.Addr().String() == d.Src {
			return d.Src
		}
	}
	if len(d.Addrs) != 0 {
		return d.Addrs[0].Addr().String()
	}
	return ""
}

// Has says whether the address is one of the doctor's stable addresses.
func (d IPv6Doctor) Has(addr string) bool {
	a, ok := UsableIPv6(addr)
	if !ok {
		return false
	}
	for _, p := range d.Addrs {
		if p.Addr().String() == a {
			return true
		}
	}
	return false
}

// IPv6Doctors reads the doctor's ipv6 check of every node that has one, by node id: one small query for a list of nodes.
func (s *Store) IPv6Doctors(ctx context.Context) (map[string]IPv6Doctor, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, params_json FROM doctor_result WHERE check_id = 'ipv6'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]IPv6Doctor{}
	for rows.Next() {
		var id, params string
		if err := rows.Scan(&id, &params); err != nil {
			return nil, err
		}
		out[id] = DoctorIPv6(&DoctorRow{NodeID: id, CheckID: "ipv6", Params: parseMap(params)})
	}
	return out, rows.Err()
}

// IPv6Doctor reads the doctor's ipv6 check of one node.
func (s *Store) IPv6Doctor(ctx context.Context, nodeID string) (IPv6Doctor, error) {
	rows, err := s.DoctorResults(ctx, nodeID)
	if err != nil {
		return IPv6Doctor{}, err
	}
	for i := range rows {
		if rows[i].CheckID == "ipv6" {
			return DoctorIPv6(&rows[i]), nil
		}
	}
	return IPv6Doctor{State: IPv6Unknown}, nil
}

// IPv6Verdict is the answer for one port in an IPv6Check: tcp "ours", "other", "refused" or "timeout"
// (internal/tcpcheck), "reachable" when Globalping only connected; udp "ok", "lossy" or "broken".
type IPv6Verdict struct {
	Net     string `json:"net"` // tcp | udp
	Port    uint16 `json:"port"`
	Verdict string `json:"verdict"`
}

// Who verified an address (IPv6Check.By), besides a node's id.
const (
	IPv6ByPanel      = "panel"
	IPv6ByGlobalping = "globalping"
)

// IPv6Check is the outcome of one check of an IPv6 address of a node from outside (fleet.CheckIPv6): the node's own ports,
// probed with the same packets as the port check but sent to this address by another node that has working IPv6.
type IPv6Check struct {
	Addr string `json:"addr"`
	At   int64  `json:"at"` // unix seconds
	// By is the id of the node that sent the probes, IPv6ByPanel or IPv6ByGlobalping (which only proves that a connection
	// opens, not that it reached this node). Empty when nothing could send.
	By string `json:"by,omitempty"`
	// Sender is By as a person reads it: the name the node had then, "panel" or "globalping".
	Sender string `json:"sender,omitempty"`
	Pass   bool   `json:"pass"`
	// Code says why a check did not run or could not be trusted: node_offline, agent_too_old, nothing_to_check,
	// no_ipv6_verifier, failed. Empty when the ports were probed (a refusing port is in Verdicts, not here).
	Code     string        `json:"code,omitempty"`
	Verdicts []IPv6Verdict `json:"verdicts,omitempty"`
}
