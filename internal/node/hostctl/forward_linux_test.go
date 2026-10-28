//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type forwardFirewallFake struct {
	ruleset  string
	ipv4     string
	ipv6     string
	versions map[string]string
	userNS   map[string][]string // the fix's user chain per binary: nil = absent, else its rules in -S form
	calls    []call
	next     int
}

func (f *forwardFirewallFake) run(_ context.Context, stdin, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, call{stdin, name, joined})
	if (name == "iptables" || name == "ip6tables") && len(args) > 2 && args[0] == "-w" {
		args = args[2:] // the lock wait the fix passes to every change
		joined = strings.Join(args, " ")
	}
	switch {
	case name == "nft" && joined == "-a list ruleset":
		return []byte(f.ruleset), nil
	case name == "nft" && joined == "-f -":
		f.applyNFT(stdin)
		return nil, nil
	case (name == "iptables" || name == "ip6tables") && joined == "-S FORWARD":
		if name == "ip6tables" {
			return []byte(f.ipv6), nil
		}
		return []byte(f.ipv4), nil
	case (name == "iptables" || name == "ip6tables") && joined == "-V":
		return []byte(f.versions[name]), nil
	case (name == "iptables" || name == "ip6tables") && joined == "-S "+forwardChain:
		rules, ok := f.userNS[name]
		if !ok {
			return nil, errors.New("iptables: No chain/target/match by that name")
		}
		out := "-N " + forwardChain + "\n"
		for _, r := range rules {
			out += "-A " + forwardChain + " " + r + "\n"
		}
		return []byte(out), nil
	case (name == "iptables" || name == "ip6tables") && len(args) > 1 && args[1] == forwardChain && (args[0] == "-N" || args[0] == "-F" || args[0] == "-X" || args[0] == "-A"):
		if f.userNS == nil {
			f.userNS = map[string][]string{}
		}
		switch args[0] {
		case "-N":
			f.userNS[name] = []string{}
		case "-F":
			f.userNS[name] = []string{}
		case "-X":
			delete(f.userNS, name)
		case "-A":
			f.userNS[name] = append(f.userNS[name], strings.Join(args[2:], " "))
		}
		return nil, nil
	case (name == "iptables" || name == "ip6tables") && len(args) > 0 && args[0] == "-I":
		f.insertIPTables(name, args)
		return nil, nil
	case (name == "iptables" || name == "ip6tables") && len(args) > 0 && args[0] == "-D":
		f.deleteIPTables(name, args)
		return nil, nil
	default:
		return nil, nil
	}
}

func (f *forwardFirewallFake) applyNFT(script string) {
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "rule" {
			continue
		}
		switch fields[0] {
		case "insert":
			prefix := strings.Join(fields[:5], " ")
			body := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			f.insertNFTRule(fields[2], fields[3], fields[4], body)
		case "delete":
			if len(fields) == 7 && fields[5] == "handle" {
				f.deleteNFTRule(fields[2], fields[3], fields[4], fields[6])
			}
		}
	}
}

func (f *forwardFirewallFake) insertNFTRule(family, table, chain, body string) {
	lines := strings.Split(f.ruleset, "\n")
	inTable, inChain, insertAt := false, false, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := nftTableLineRe.FindStringSubmatch(trimmed); m != nil {
			inTable = m[1] == family && m[2] == table
			inChain = false
			continue
		}
		if inTable {
			if m := nftChainLineRe.FindStringSubmatch(trimmed); m != nil {
				inChain = m[1] == chain
				continue
			}
			if inChain && strings.Contains(trimmed, "policy ") {
				insertAt = i + 1
				break
			}
		}
	}
	if insertAt < 0 {
		return
	}
	f.next++
	rule := fmt.Sprintf("\t\t%s # handle %d", body, f.next+100)
	lines = append(lines[:insertAt], append([]string{rule}, lines[insertAt:]...)...)
	f.ruleset = strings.Join(lines, "\n")
}

func (f *forwardFirewallFake) deleteNFTRule(family, table, chain, handle string) {
	want := "# handle " + handle
	lines := strings.Split(f.ruleset, "\n")
	inTable, inChain := false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := nftTableLineRe.FindStringSubmatch(trimmed); m != nil {
			inTable = m[1] == family && m[2] == table
			inChain = false
			continue
		}
		if inTable {
			if m := nftChainLineRe.FindStringSubmatch(trimmed); m != nil {
				inChain = m[1] == chain
				continue
			}
		}
		if inChain && strings.Contains(line, want) {
			lines = append(lines[:i], lines[i+1:]...)
			f.ruleset = strings.Join(lines, "\n")
			return
		}
	}
}

func (f *forwardFirewallFake) insertIPTables(binary string, args []string) {
	if len(args) < 4 || args[1] != "FORWARD" || args[2] != "1" {
		return
	}
	line := "-A FORWARD " + strings.Join(args[3:], " ")
	line = strings.Replace(line, "--comment "+forwardExceptionPrefix, `--comment "`+forwardExceptionPrefix, 1)
	line = strings.Replace(line, " -j ACCEPT", `" -j ACCEPT`, 1) // iptables -S quotes the comment; the comment is always the last match here
	if binary == "ip6tables" {
		f.ipv6 = insertAfterPolicy(f.ipv6, line)
	} else {
		f.ipv4 = insertAfterPolicy(f.ipv4, line)
	}
}

func insertAfterPolicy(rules, line string) string {
	lines := strings.Split(strings.TrimSuffix(rules, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return line + "\n"
	}
	for i, current := range lines {
		if strings.HasPrefix(current, "-P FORWARD ") {
			lines = append(lines[:i+1], append([]string{line}, lines[i+1:]...)...)
			return strings.Join(lines, "\n") + "\n"
		}
	}
	return line + "\n" + strings.Join(lines, "\n") + "\n"
}

func (f *forwardFirewallFake) deleteIPTables(binary string, args []string) {
	if len(args) < 3 || args[1] != "FORWARD" {
		return
	}
	want := strings.Join(args[2:], " ")
	var source *string
	if binary == "ip6tables" {
		source = &f.ipv6
	} else {
		source = &f.ipv4
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(*source), "\n") {
		if strings.HasPrefix(line, "-A FORWARD ") && strings.ReplaceAll(strings.TrimPrefix(line, "-A FORWARD "), `"`, "") == want {
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	*source = strings.Join(kept, "\n") + "\n"
}

func forwardFixHost(t *testing.T, fake *forwardFirewallFake) *linuxHost {
	t.Helper()
	h, _ := testHost(t)
	h.run = fake.run
	h.SetStateDir(filepath.Join(t.TempDir(), "state"))
	h.rly.addrs = func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	}
	return h
}

func forwardStaticRules(fake *forwardFirewallFake, name string) int {
	count := 0
	for _, c := range fake.calls {
		if c.name == name && strings.HasPrefix(c.args, "-w 5 -I FORWARD 1 ") && strings.Contains(c.args, forwardExceptionPrefix) {
			count++
		}
		if name == "nft" && c.name == "nft" && c.args == "-f -" {
			for _, line := range strings.Split(c.stdin, "\n") {
				if strings.HasPrefix(line, "insert rule ") && strings.Contains(line, forwardExceptionPrefix) {
					count++
				}
			}
		}
	}
	return count
}

func clearMarkedNFTRules(fake *forwardFirewallFake) {
	var kept []string
	for _, line := range strings.Split(fake.ruleset, "\n") {
		if !strings.Contains(line, forwardExceptionPrefix) {
			kept = append(kept, line)
		}
	}
	fake.ruleset = strings.Join(kept, "\n")
}

func TestApplyForwardExceptionsUsesIPTablesNFTAndIsIdempotent(t *testing.T) {
	fake := &forwardFirewallFake{
		ruleset: `table ip filter {
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
		counter accept comment "operator-rule" # handle 1
	}
}
table inet host_firewall {
	chain forward {
		type filter hook forward priority filter + 1; policy drop;
		counter accept comment "operator-inet-rule" # handle 2
	}
}
`,
		ipv4:     "-P FORWARD DROP\n-A FORWARD -m comment --comment operator-rule -j ACCEPT\n",
		ipv6:     "-P FORWARD ACCEPT\n",
		versions: map[string]string{"iptables": "iptables v1.8.9 (nf_tables)"},
	}
	h := forwardFixHost(t, fake)
	ctx := context.Background()

	changed, err := h.ApplyForwardExceptions(ctx)
	if err != nil || changed != 5 { // iptables: the user chain and two rules; the independent nft chain: two rules
		t.Fatalf("ApplyForwardExceptions = %d, %v; want two iptables-nft and two independent nft-chain rules plus the user chain", changed, err)
	}
	if got := forwardStaticRules(fake, "iptables"); got != 2 {
		t.Fatalf("inserted %d iptables rules, want two", got)
	}
	if got := forwardStaticRules(fake, "nft"); got != 2 {
		t.Fatalf("inserted %d rules into the independent nft base chain, want two", got)
	}
	if strings.Contains(fake.ipv4, "-A FORWARD -j ACCEPT") || !strings.Contains(fake.ipv4, "operator-rule") {
		t.Fatalf("ruleset changed outside the Mistgate-marked rules:\n%s", fake.ipv4)
	}
	if plan, err := h.PlanForwardExceptions(ctx); err != nil || plan.Needed || plan.Backend != "iptables-nft + nft" || plan.DropChains != 2 {
		t.Fatalf("verified plan = %+v, %v", plan, err)
	}
	before := forwardStaticRules(fake, "iptables")
	if changed, err := h.ApplyForwardExceptions(ctx); err != nil || changed != 0 {
		t.Fatalf("second apply = %d, %v; want no change", changed, err)
	}
	if got := forwardStaticRules(fake, "iptables"); got != before {
		t.Fatalf("second apply inserted rules again: before=%d after=%d", before, got)
	}

	if err := h.cleanupForwardExceptions(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fake.ipv4, forwardExceptionPrefix) || !strings.Contains(fake.ipv4, "operator-rule") ||
		strings.Contains(fake.ruleset, forwardExceptionPrefix) || !strings.Contains(fake.ruleset, `counter accept comment "operator-inet-rule" # handle 2`) {
		t.Fatalf("cleanup did not remove only tagged rules:\n%s", fake.ipv4)
	}
	if _, err := os.Stat(h.forwardStateFile); !os.IsNotExist(err) {
		t.Fatalf("approved state remains after cleanup: %v", err)
	}
}

func TestApplyForwardExceptionsUsesNativeNFTAndScopesRelayRules(t *testing.T) {
	fake := &forwardFirewallFake{
		ruleset: `table inet ufw {
	set local_v4 {
		type ipv4_addr;
		elements = { 203.0.113.20 }
	}
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
		counter accept comment "operator-rule" # handle 1
	}
}
table inet mistgate_relay {
	set local4 {
		type ipv4_addr;
		flags interval;
		elements = { 203.0.113.10 }
	}
	chain pass {
		type filter hook forward priority filter + 1; policy accept;
		meta nfproto ipv4 ct status dnat ct original proto-dst 8443 ip daddr 203.0.113.20 tcp dport 443 counter name tcp_8443 accept
	}
}
table ip6 host_firewall_v6 {
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
		counter accept comment "operator-v6-rule" # handle 20
	}
}
`,
		ipv4:     "-P FORWARD ACCEPT\n",
		ipv6:     "-P FORWARD ACCEPT\n",
		versions: map[string]string{},
	}
	h := forwardFixHost(t, fake)
	ctx := context.Background()

	changed, err := h.ApplyForwardExceptions(ctx)
	if err != nil || changed != 6 {
		t.Fatalf("ApplyForwardExceptions = %d, %v; want interface rules in both drop chains plus two relay rules", changed, err)
	}
	if got := forwardStaticRules(fake, "nft"); got != 6 {
		t.Fatalf("inserted %d nft rules across independent base chains, want six", got)
	}
	if !strings.Contains(fake.ruleset, `ct direction original ct original ip daddr 203.0.113.10 ct original proto-dst 8443 ip daddr 203.0.113.20 tcp dport 443`) ||
		!strings.Contains(fake.ruleset, `ct direction reply ct original ip daddr 203.0.113.10 ct original proto-dst 8443 ip saddr 203.0.113.20 tcp sport 443`) {
		t.Fatalf("relay exceptions do not match only the current DNAT target:\n%s", fake.ruleset)
	}
	if !strings.Contains(fake.ruleset, `counter accept comment "operator-rule" # handle 1`) {
		t.Fatalf("unrelated nft rule was changed:\n%s", fake.ruleset)
	}
	plan, err := h.PlanForwardExceptions(ctx)
	if err != nil || plan.Needed || plan.Backend != "nft" || plan.DropChains != 2 || plan.RelayForwards != 1 {
		t.Fatalf("verified plan = %+v, %v", plan, err)
	}
	before := forwardStaticRules(fake, "nft")
	clearMarkedNFTRules(fake) // simulate a firewall reload removing the approved rules
	if err := h.ReassertForwardExceptions(ctx); err != nil {
		t.Fatalf("ReassertForwardExceptions after reload: %v", err)
	}
	if strings.Count(fake.ruleset, forwardExceptionPrefix) != 6 || forwardStaticRules(fake, "nft") != before+6 {
		t.Fatalf("sweep did not restore all marked exceptions after reload:\n%s", fake.ruleset)
	}
	if changed, err := h.ApplyForwardExceptions(ctx); err != nil || changed != 0 {
		t.Fatalf("second apply = %d, %v; want no change", changed, err)
	}
	if got := forwardStaticRules(fake, "nft"); got != before+6 {
		t.Fatalf("second apply inserted rules again: want %d, got %d", before+6, got)
	}

	if err := h.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fake.ruleset, forwardExceptionPrefix) || !strings.Contains(fake.ruleset, `counter accept comment "operator-rule" # handle 1`) ||
		!strings.Contains(fake.ruleset, `counter accept comment "operator-v6-rule" # handle 20`) {
		t.Fatalf("Cleanup did not remove only Mistgate-marked rules:\n%s", fake.ruleset)
	}
	if _, err := os.Stat(h.forwardStateFile); !os.IsNotExist(err) {
		t.Fatalf("approved state remains after Cleanup: %v", err)
	}
}

func TestApprovedForwardExceptionsLetRelayInstallThroughDropChain(t *testing.T) {
	fake := &forwardFirewallFake{
		ruleset: `table inet host_firewall {
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
	}
}
`,
		ipv4:     "-P FORWARD ACCEPT\n",
		ipv6:     "-P FORWARD ACCEPT\n",
		versions: map[string]string{},
	}
	h := forwardFixHost(t, fake)
	ctx := context.Background()
	forwards := []Forward{fwd("rly_test", "tcp", 8443, "203.0.113.20", 443)}
	if err := h.SetRelays(ctx, forwards); !errors.Is(err, ErrForwardDrop) {
		t.Fatalf("initial relay setup = %v, want fail-closed forward_drop", err)
	}
	plan, err := h.PlanForwardExceptions(ctx)
	if err != nil || !plan.Needed || plan.AddRules != 4 || plan.RelayForwards != 1 {
		t.Fatalf("plan omitted the closed relay target: %+v, %v", plan, err)
	}
	if changed, err := h.ApplyForwardExceptions(ctx); err != nil || changed != 4 {
		t.Fatalf("ApplyForwardExceptions = %d, %v; want the interface and current relay rules", changed, err)
	}
	path := filepath.Join(h.procSys, "net/ipv4/ip_forward")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.SetRelays(ctx, forwards); err != nil {
		t.Fatalf("SetRelays still failed closed after the approved fix: %v", err)
	}
	if !strings.Contains(fake.ruleset, "ct status dnat ct direction original") || !strings.Contains(fake.ruleset, "ct status dnat ct direction reply") {
		t.Fatalf("relay path did not get target-scoped exceptions:\n%s", fake.ruleset)
	}
	if why, drops := h.forwardDrop(context.Background()); drops {
		t.Fatalf("approved FORWARD path still looks blocked: %s", why)
	}
}

func TestForwardRuleRenderingIsRestrictedToMistgateInterfaces(t *testing.T) {
	rules := forwardingRules("inet", nil, nil)
	want := []string{
		`iifname "mgawg*" oifname != "docker*" oifname != "br-*" accept comment "mistgate:doctor-forward:v1:awg-fwd"`,
		`oifname "mgawg*" ct state established,related accept comment "mistgate:doctor-forward:v1:awg-reply"`,
	}
	if len(rules) != len(want) {
		t.Fatalf("forwarding rules = %d, want %d (no WARP rule: WARP is only an outgoing hop of an accepted client flow)", len(rules), len(want))
	}
	for i := range want {
		if rules[i].nft != want[i] || !strings.Contains(strings.Join(rules[i].ipt, " "), forwardExceptionPrefix) {
			t.Errorf("rule %d = %+v, want nft %q and a Mistgate marker", i, rules[i], want[i])
		}
	}
	if got := strings.Join(rules[1].ipt, " "); !strings.Contains(got, "-o mgawg+ -m conntrack --ctstate RELATED,ESTABLISHED") {
		t.Errorf("the rule towards clients must be reply-only, got %q", got)
	}
}

func TestForwardingExceptionsAreMarkerComparedAndStaleRulesGoFirstInsertThenDelete(t *testing.T) {
	// iptables -S prints a rule in its own canonical form and quotes the comment; a stale (older, broader) marker goes.
	fake := &forwardFirewallFake{
		ruleset: "",
		ipv4: "-P FORWARD DROP\n" +
			`-A FORWARD -i mgawg+ -m comment --comment "mistgate:doctor-forward:v1:awg-in" -j ACCEPT` + "\n" +
			`-A FORWARD -o mgawg+ -m comment --comment "mistgate:doctor-forward:v1:awg-out" -j ACCEPT` + "\n" +
			`-A FORWARD -i mgwarp -m comment --comment "mistgate:doctor-forward:v1:warp-in" -j ACCEPT` + "\n",
		ipv6:     "-P FORWARD ACCEPT\n",
		versions: map[string]string{"iptables": "iptables v1.8.10 (legacy)"},
	}
	h := forwardFixHost(t, fake)
	changed, err := h.ApplyForwardExceptions(context.Background())
	if err != nil || changed != 6 { // user chain, awg-fwd and awg-reply added; the stale awg-in, awg-out and warp-in removed
		t.Fatalf("ApplyForwardExceptions = %d, %v", changed, err)
	}
	if strings.Contains(fake.ipv4, "awg-out") || strings.Contains(fake.ipv4, "awg-in") || strings.Contains(fake.ipv4, "warp-in") || !strings.Contains(fake.ipv4, "awg-reply") || strings.Count(fake.ipv4, "awg-fwd") != 1 || !fake.userNSHas("iptables") {
		t.Fatalf("stale markers were not replaced:\n%s", fake.ipv4)
	}
	var insertAt, deleteAt int
	for i, c := range fake.calls {
		if strings.Contains(c.args, "-I FORWARD") {
			insertAt = i
		}
		if strings.Contains(c.args, "-D FORWARD") && deleteAt == 0 {
			deleteAt = i
		}
	}
	if deleteAt < insertAt {
		t.Fatalf("a rule was deleted before its replacement was inserted: insert at %d, delete at %d", insertAt, deleteAt)
	}
}

func TestForwardDropIgnoresIPv6AndBridgeChainsForRelays(t *testing.T) {
	fake := &forwardFirewallFake{
		ruleset: `table bridge filter {
	chain forward {
		type filter hook forward priority filter; policy drop;
	}
}
table ip6 host_firewall_v6 {
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
	}
}
`,
		ipv4: "-P FORWARD ACCEPT\n", ipv6: "-P FORWARD DROP\n", versions: map[string]string{},
	}
	h := forwardFixHost(t, fake)
	if why, drops := h.forwardDrop(context.Background()); drops {
		t.Fatalf("relay (IPv4 routed) blocked by %s", why)
	}
}

func (f *forwardFirewallFake) userNSHas(binary string) bool {
	return len(f.userNS[binary]) == len(forwardChainRules)
}
