//go:build linux

package warp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/vishvananda/netlink"
)

func trimOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (p *linuxPlane) nftExists(ctx context.Context) bool {
	_, err := p.run(ctx, "", "nft", "list", "table", "inet", p.s.NftTable)
	return err == nil
}

// syncNft installs or removes the WARP nft table. The table is replaced only when its text changed or it is gone
// (a replace is atomic but there is no reason to do it every check).
func (p *linuxPlane) syncNft(ctx context.Context, rs routeSpec) (bool, error) {
	exists := p.nftExists(ctx)
	if !rs.Configured {
		p.nftText = ""
		if !exists {
			return false, nil
		}
		if out, err := p.run(ctx, "", "nft", "delete", "table", "inet", p.s.NftTable); err != nil {
			return true, fmt.Errorf("nft delete table: %s: %w", trimOut(out), err)
		}
		return true, nil
	}
	var rsv []byte
	if rs.Kernel {
		rsv = rs.Reserved
	}
	text := renderNft(p.s.NftTable, p.s.Iface, rsv, rs.Endpoint)
	if exists && text == p.nftText {
		return false, nil
	}
	if out, err := p.run(ctx, text, "nft", "-f", "-"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, errors.New("nft is not installed: masquerade for WARP cannot be set up")
		}
		return false, fmt.Errorf("nft: %s: %w", trimOut(out), err)
	}
	p.nftText = text
	return !exists, nil
}

// --- preflight -----------------------------------------------------------------------------------------------

func (p *linuxPlane) Preflight(ctx context.Context) []Finding {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Finding
	ifIdx := p.ifIndex()
	for _, fam := range families {
		rules, err := netlink.RuleList(fam)
		if err != nil {
			continue
		}
		for _, r := range rules {
			switch {
			case r.Table != p.s.Table && (r.Priority == p.s.OifPref || r.Priority == p.s.SubnetPref):
				out = append(out, Finding{ID: "rule_pref_in_use", Detail: fmt.Sprintf("ip rule preference %d is used by table %d", r.Priority, r.Table)})
			case r.Table == p.s.Table && r.Priority != p.s.OifPref && r.Priority != p.s.SubnetPref:
				out = append(out, Finding{ID: "table_in_use", Detail: fmt.Sprintf("routing table %d is referenced by a rule at preference %d that is not ours", p.s.Table, r.Priority)})
			}
		}
		routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: p.s.Table}, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue
		}
		for _, r := range routes {
			if !ownRoute(r, ifIdx) {
				out = append(out, Finding{ID: "table_in_use", Detail: fmt.Sprintf("routing table %d holds a route that is not ours", p.s.Table)})
				break
			}
		}
	}
	if f, ok := p.forwardDrop(ctx); ok {
		out = append(out, f)
	}
	return dedupeFindings(out)
}

func dedupeFindings(in []Finding) []Finding {
	seen := map[Finding]bool{}
	var out []Finding
	for _, f := range in {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

var (
	nftTableRe = regexp.MustCompile(`^table\s+(\S+)\s+(\S+)\s*\{`)
	nftHookRe  = regexp.MustCompile(`hook\s+forward\b.*\bpolicy\s+drop\b`)
)

// forwardDrop looks for a base chain on the forward hook with policy drop that is not in our table: Docker sets
// one, and it kills the forwarded traffic of AWG clients. A drop in another base chain cannot be
// overridden by an accept in ours, so this is reported, not fixed.
func (p *linuxPlane) forwardDrop(ctx context.Context) (Finding, bool) {
	if out, err := p.run(ctx, "", "nft", "list", "ruleset"); err == nil {
		table := ""
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if m := nftTableRe.FindStringSubmatch(line); m != nil {
				table = m[2]
				continue
			}
			if table != p.s.NftTable && nftHookRe.MatchString(line) {
				return Finding{ID: "forward_drop", Detail: "nft table " + table + " drops forwarded packets by default"}, true
			}
		}
	}
	if out, err := p.run(ctx, "", "iptables", "-S", "FORWARD"); err == nil && strings.Contains(string(out), "-P FORWARD DROP") {
		return Finding{ID: "forward_drop", Detail: "iptables FORWARD policy is DROP"}, true
	}
	return Finding{}, false
}
