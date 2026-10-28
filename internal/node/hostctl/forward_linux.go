//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	forwardExceptionPrefix = "mistgate:doctor-forward:v1:"
	forwardStateFileName   = "doctor-forwarding"
	// forwardChain is the one user chain of the fix in iptables: iptables takes a single -o per rule, so "to anywhere but a
	// container bridge" needs a chain whose RETURNs hand the packet back to the host's own rules after our jump.
	forwardChain = "MISTGATE-FWD"
)

// forwardChainRules is the content of forwardChain, in the form iptables -S prints it.
var forwardChainRules = [][]string{
	{"-o", "docker+", "-j", "RETURN"},
	{"-o", "br-+", "-j", "RETURN"},
	{"-j", "ACCEPT"},
}

var (
	nftTableLineRe  = regexp.MustCompile(`^table\s+(\S+)\s+(\S+)\s*\{(?:\s+#.*)?$`)
	nftChainLineRe  = regexp.MustCompile(`^chain\s+(\S+)\s*\{(?:\s+#.*)?$`)
	nftCommentRe    = regexp.MustCompile(`comment\s+"([^"\\]*(?:\\.[^"\\]*)*)"`)
	nftHandleRe     = regexp.MustCompile(`#\s*handle\s+(\d+)\s*$`)
	nftNameRe       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	forwardHookRe   = regexp.MustCompile(`\bhook\s+forward\b.*\bpolicy\s+drop\b`)
	nftRelayRuleRe  = regexp.MustCompile(`ct status dnat ct original proto-dst (\d+) ip daddr ([0-9.]+) (tcp|udp) dport (\d+) counter name "?[A-Za-z0-9_.-]+"? accept(?:\s+#.*)?$`)
	iptablesComment = []string{"-m", "comment", "--comment"}
)

type nftMarkedRule struct {
	marker string
	handle uint64
}

type nftScopedLine struct {
	family, table, chain string
	line                 string
	chainStart           bool
}

type nftForwardChain struct {
	family, table, chain string
	policyDrop           bool
	rules                map[string][]nftMarkedRule
}

type iptablesMarkedRule struct {
	marker string
	spec   []string
}

type iptablesForward struct {
	binary     string
	family     string
	available  bool
	policyDrop bool
	version    string
	rules      map[string][]iptablesMarkedRule
	chain      [][]string // the rules of forwardChain; nil with chainFound false = no such chain
	chainFound bool
}

type forwardSnapshot struct {
	nftAvailable bool
	nftOutput    string
	nftChains    []nftForwardChain
	iptables     []iptablesForward
}

type relayTarget struct {
	network string
	port    uint16
	toIP    netip.Addr
	toPort  uint16
}

type forwardRule struct {
	marker string
	nft    string
	ipt    []string
}

type firewallCommand struct {
	binary string
	args   []string
}

type firewallActions struct {
	nftLines []string
	iptables []firewallCommand
	adds     int
	removes  int
	chains   int
	backends []string
}

func (h *linuxHost) forwardStateEnabled() bool {
	if h.forwardStateFile == "" {
		return false
	}
	b, err := os.ReadFile(h.forwardStateFile)
	return err == nil && string(b) == "enabled\n"
}

func (h *linuxHost) PlanForwardExceptions(ctx context.Context) (ForwardExceptionPlan, error) {
	h.rly.mu.Lock()
	targets, hasDesired := relayTargetsFromForwards(h.rly.desired), h.rly.hasDesired
	h.rly.mu.Unlock()
	snap, err := h.readForwardSnapshot(ctx)
	if err != nil {
		return ForwardExceptionPlan{}, err
	}
	if !hasExternalForwardDrop(snap) {
		return ForwardExceptionPlan{}, nil
	}
	if !hasDesired {
		targets = relayTargetsFromRuleset(snap.nftOutput)
	}
	local, err := h.forwardLocalAddresses(targets)
	if err != nil {
		return ForwardExceptionPlan{}, err
	}
	actions, err := planForwardActions(snap, targets, local, false)
	if err != nil {
		return ForwardExceptionPlan{}, err
	}
	return ForwardExceptionPlan{
		Needed:        !h.forwardStateEnabled() || actions.adds > 0 || actions.removes > 0,
		AddRules:      actions.adds,
		RemoveRules:   actions.removes,
		DropChains:    actions.chains,
		RelayForwards: len(targets),
		Backend:       strings.Join(actions.backends, " + "),
	}, nil
}

func (h *linuxHost) ApplyForwardExceptions(ctx context.Context) (uint32, error) {
	h.rly.mu.Lock()
	targets, hasDesired := relayTargetsFromForwards(h.rly.desired), h.rly.hasDesired
	h.forwardMu.Lock()
	h.rly.mu.Unlock()
	defer h.forwardMu.Unlock()
	if h.forwardStateFile == "" {
		return 0, ErrUnsupported
	}
	snap, err := h.readForwardSnapshot(ctx)
	if err != nil {
		return 0, err
	}
	if !hasExternalForwardDrop(snap) {
		return 0, nil
	}
	if !hasDesired {
		targets = relayTargetsFromRuleset(snap.nftOutput)
	}
	local, err := h.forwardLocalAddresses(targets)
	if err != nil {
		return 0, err
	}
	actions, err := planForwardActions(snap, targets, local, false)
	if err != nil {
		return 0, err
	}
	if _, err := writeIfChanged(h.forwardStateFile, "enabled\n", 0o600); err != nil {
		return 0, err
	}
	if err := h.applyForwardActions(ctx, actions); err != nil {
		_ = os.Remove(h.forwardStateFile)
		_ = h.removeForwardRules(ctx)
		return 0, err
	}
	return uint32(actions.adds + actions.removes), nil
}

// ReassertForwardExceptions is called from the existing agent sweep. The state file is written only by the confirmed
// doctor fix, so a reload can restore the owner's approved intent without silently enabling forwarding on other nodes.
func (h *linuxHost) ReassertForwardExceptions(ctx context.Context) error {
	h.rly.mu.Lock()
	targets, hasDesired := relayTargetsFromForwards(h.rly.desired), h.rly.hasDesired
	h.forwardMu.Lock()
	h.rly.mu.Unlock()
	defer h.forwardMu.Unlock()
	if !h.forwardStateEnabled() {
		return nil
	}
	snap, err := h.readForwardSnapshot(ctx)
	if err != nil {
		return err
	}
	if !hasDesired {
		targets = relayTargetsFromRuleset(snap.nftOutput)
	}
	local, err := h.forwardLocalAddresses(targets)
	if err != nil {
		return err
	}
	actions, err := planForwardActions(snap, targets, local, false)
	if err != nil {
		return err
	}
	return h.applyForwardActions(ctx, actions)
}

// ReconcileForwardExceptions refreshes the relay-only match set before SetRelays installs a new relay table.
func (h *linuxHost) ReconcileForwardExceptions(ctx context.Context, forwards []Forward, local []netip.Addr) error {
	h.forwardMu.Lock()
	defer h.forwardMu.Unlock()
	if !h.forwardStateEnabled() {
		return nil
	}
	snap, err := h.readForwardSnapshot(ctx)
	if err != nil {
		return err
	}
	targets := relayTargetsFromForwards(forwards)
	addresses := normalizeForwardLocal(local)
	actions, err := planForwardActions(snap, targets, addresses, false)
	if err != nil {
		return err
	}
	return h.applyForwardActions(ctx, actions)
}

func (h *linuxHost) cleanupForwardExceptions(ctx context.Context) error {
	h.forwardMu.Lock()
	defer h.forwardMu.Unlock()
	err := h.removeForwardRules(ctx)
	if h.forwardStateFile != "" {
		if removeErr := os.Remove(h.forwardStateFile); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

func (h *linuxHost) removeForwardRules(ctx context.Context) error {
	snap, err := h.readForwardSnapshot(ctx)
	if err != nil {
		if missingFirewallTools(err) {
			return nil
		}
		return err
	}
	actions, err := planForwardActions(snap, nil, nil, true)
	if err != nil {
		return err
	}
	return h.applyForwardActions(ctx, actions)
}

func (h *linuxHost) forwardDrop(ctx context.Context) (string, bool) {
	snap, err := h.readForwardSnapshot(ctx)
	if err != nil {
		return "", false // neither firewall tool answering is not evidence of a drop
	}
	// The relay forwards IPv4 only: an IPv6 drop policy does not touch them.
	for _, ipt := range snap.iptables {
		if ipt.family == "ip" && ipt.policyDrop && (!h.forwardStateEnabled() || !iptablesHasStaticRules(ipt)) {
			return "iptables FORWARD policy is DROP", true
		}
	}
	for _, chain := range snap.nftChains {
		if chain.family == "ip6" || !chain.policyDrop || OwnNftTable(chain.family, chain.table) || iptablesOwnsDropChain(snap, chain) {
			continue
		}
		if !h.forwardStateEnabled() || !nftHasStaticRules(chain) {
			return "nft table " + chain.table + " drops forwarded packets by default", true
		}
	}
	return "", false
}

func (h *linuxHost) readForwardSnapshot(ctx context.Context) (forwardSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, firewallCmdTimeout)
	defer cancel()
	snap := forwardSnapshot{}
	var failures []error
	if out, err := h.run(ctx, "", "nft", "-a", "list", "ruleset"); err == nil {
		snap.nftAvailable, snap.nftOutput = true, string(out)
		snap.nftChains = parseNftForwardChains(snap.nftOutput)
	} else {
		failures = append(failures, fmt.Errorf("list nft ruleset: %w", err))
	}
	for _, item := range []struct{ binary, family string }{{"iptables", "ip"}, {"ip6tables", "ip6"}} {
		out, err := h.run(ctx, "", item.binary, "-S", "FORWARD")
		if err != nil {
			failures = append(failures, fmt.Errorf("list %s FORWARD: %w", item.binary, err))
			continue
		}
		ipt := parseIPTablesForward(item.binary, item.family, string(out))
		if chainOut, chainErr := h.run(ctx, "", item.binary, "-S", forwardChain); chainErr == nil {
			ipt.chainFound, ipt.chain = parseIPTablesChain(string(chainOut))
		}
		if ipt.policyDrop {
			if version, versionErr := h.run(ctx, "", item.binary, "-V"); versionErr == nil {
				ipt.version = strings.TrimSpace(string(version))
			}
		}
		snap.iptables = append(snap.iptables, ipt)
	}
	if !snap.nftAvailable && len(snap.iptables) == 0 {
		return snap, errors.Join(failures...)
	}
	return snap, nil
}

func parseNftForwardChains(ruleset string) []nftForwardChain {
	var chains []nftForwardChain
	current := -1
	walkNftRuleset(ruleset, func(scoped nftScopedLine) {
		if scoped.chainStart {
			if scoped.family != "ip" && scoped.family != "ip6" && scoped.family != "inet" {
				current = -1 // a bridge-family forward hook filters frames inside a bridge, not routed packets
				return
			}
			chains = append(chains, nftForwardChain{family: scoped.family, table: scoped.table, chain: scoped.chain, rules: map[string][]nftMarkedRule{}})
			current = len(chains) - 1
			return
		}
		if scoped.chain == "" || current < 0 {
			return
		}
		chain := &chains[current]
		line := strings.TrimSpace(scoped.line)
		if forwardHookRe.MatchString(line) {
			chain.policyDrop = true
			return
		}
		m := nftCommentRe.FindStringSubmatch(line)
		if m == nil || !strings.HasPrefix(m[1], forwardExceptionPrefix) {
			return
		}
		handle := uint64(0)
		if hm := nftHandleRe.FindStringSubmatch(line); hm != nil {
			handle, _ = strconv.ParseUint(hm[1], 10, 64)
		}
		chain.rules[m[1]] = append(chain.rules[m[1]], nftMarkedRule{marker: m[1], handle: handle})
	})
	return chains
}

// walkNftRuleset tracks nested set/map blocks so a table remains in scope for chains declared after them.
func walkNftRuleset(ruleset string, visit func(nftScopedLine)) {
	family, table, chain := "", "", ""
	tableDepth, chainDepth := 0, 0
	for _, raw := range strings.Split(ruleset, "\n") {
		line := strings.TrimSpace(raw)
		if m := nftTableLineRe.FindStringSubmatch(line); m != nil {
			family, table, chain = m[1], m[2], ""
			tableDepth, chainDepth = 1, 0
			continue
		}
		if tableDepth == 0 {
			continue
		}
		if m := nftChainLineRe.FindStringSubmatch(line); m != nil {
			chain = m[1]
			tableDepth++
			chainDepth = tableDepth
			visit(nftScopedLine{family: family, table: table, chain: chain, line: line, chainStart: true})
			continue
		}
		visit(nftScopedLine{family: family, table: table, chain: chain, line: line})
		tableDepth += strings.Count(line, "{") - strings.Count(line, "}")
		if chain != "" && tableDepth < chainDepth {
			chain, chainDepth = "", 0
		}
		if tableDepth <= 0 {
			family, table, chain = "", "", ""
			tableDepth, chainDepth = 0, 0
		}
	}
}

func parseIPTablesForward(binary, family, output string) iptablesForward {
	s := iptablesForward{binary: binary, family: family, available: true, rules: map[string][]iptablesMarkedRule{}}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == "-P" && fields[1] == "FORWARD" {
			s.policyDrop = strings.EqualFold(fields[2], "DROP")
			continue
		}
		if fields[0] != "-A" || fields[1] != "FORWARD" {
			continue
		}
		for i := 2; i+3 < len(fields); i++ {
			if fields[i] != iptablesComment[0] || fields[i+1] != iptablesComment[1] || fields[i+2] != iptablesComment[2] {
				continue
			}
			marker := strings.Trim(fields[i+3], `"'`)
			if strings.HasPrefix(marker, forwardExceptionPrefix) {
				spec := make([]string, 0, len(fields)-2)
				for _, f := range fields[2:] {
					spec = append(spec, strings.Trim(f, `"'`)) // -S quotes the comment; a quote would reach iptables -D as part of it
				}
				s.rules[marker] = append(s.rules[marker], iptablesMarkedRule{marker: marker, spec: spec})
			}
			break
		}
	}
	return s
}

// parseIPTablesChain reads "iptables -S MISTGATE-FWD": whether the chain is declared and its rules.
func parseIPTablesChain(output string) (bool, [][]string) {
	found := false
	var rules [][]string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[0] == "-N" && fields[1] == forwardChain:
			found = true
		case len(fields) > 2 && fields[0] == "-A" && fields[1] == forwardChain:
			rules = append(rules, fields[2:])
		}
	}
	return found, rules
}

func (ipt iptablesForward) chainCurrent() bool {
	if !ipt.chainFound || len(ipt.chain) != len(forwardChainRules) {
		return false
	}
	for i, want := range forwardChainRules {
		if strings.Join(ipt.chain[i], " ") != strings.Join(want, " ") {
			return false
		}
	}
	return true
}

func planForwardActions(snap forwardSnapshot, targets []relayTarget, local []netip.Addr, remove bool) (firewallActions, error) {
	var actions firewallActions
	for _, ipt := range snap.iptables {
		desired := map[string][]string{}
		if ipt.policyDrop && !remove {
			for _, rule := range forwardingRules(ipt.family, targets, local) {
				desired[rule.marker] = rule.ipt
			}
		}
		commands, adds, removes := planIPTablesRules(ipt, desired)
		actions.iptables = append(actions.iptables, commands...)
		actions.adds += adds
		actions.removes += removes
		if ipt.policyDrop && !remove {
			actions.chains++
			backend := iptablesBackend(ipt)
			actions.backends = append(actions.backends, backend)
		}
	}
	for _, chain := range snap.nftChains {
		if iptablesOwnsDropChain(snap, chain) {
			continue // iptables owns this compatibility chain; keep insertion and removal through its rule interface
		}
		desired := map[string]string{}
		if chain.policyDrop && !remove && !OwnNftTable(chain.family, chain.table) {
			for _, rule := range forwardingRules(chain.family, targets, local) {
				desired[rule.marker] = rule.nft
			}
		}
		lines, adds, removes, err := planNftRules(chain, desired)
		if err != nil {
			return firewallActions{}, err
		}
		actions.nftLines = append(actions.nftLines, lines...)
		actions.adds += adds
		actions.removes += removes
		if chain.policyDrop && !remove && !OwnNftTable(chain.family, chain.table) {
			actions.chains++
			actions.backends = append(actions.backends, "nft")
		}
	}
	actions.backends = uniqueSorted(actions.backends)
	return actions, nil
}

// forwardingRules is what the fix puts at the start of a host chain that drops by default. A rule is told apart from the
// host's own only by its marker, and an installed rule is compared by marker alone (nft and iptables print a rule
// differently from how it was entered), so a marker must name exactly one meaning: change a rule, change its marker.
//
//   - awg-fwd: NEW and later packets that come IN from a client tunnel, to anywhere except a container bridge (docker0, br-*:
//     a client would otherwise reach containers the host's firewall keeps closed to forwarded traffic);
//   - awg-reply: what goes OUT to a client tunnel, only as part of a connection the client began (ESTABLISHED,RELATED), so
//     nothing new is accepted from the internet towards a client. This also carries the replies of clients that egress
//     through WARP, so WARP itself needs no rule: it is only ever an outgoing hop of an accepted client flow (Hysteria2 and
//     VLESS reach WARP from local processes, which the FORWARD hook never sees);
//   - relay rules: the DNATed flows of the current relay forwards, both directions, nothing else.
func forwardingRules(family string, targets []relayTarget, local []netip.Addr) []forwardRule {
	rules := []forwardRule{
		{
			marker: forwardExceptionPrefix + "awg-fwd",
			nft:    `iifname "mgawg*" oifname != "docker*" oifname != "br-*" accept comment "` + forwardExceptionPrefix + `awg-fwd"`,
			ipt:    []string{"-i", "mgawg+", "-m", "comment", "--comment", forwardExceptionPrefix + "awg-fwd", "-j", forwardChain},
		},
		{
			marker: forwardExceptionPrefix + "awg-reply",
			nft:    `oifname "mgawg*" ct state established,related accept comment "` + forwardExceptionPrefix + `awg-reply"`,
			ipt: []string{"-o", "mgawg+", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED",
				"-m", "comment", "--comment", forwardExceptionPrefix + "awg-reply", "-j", "ACCEPT"},
		},
	}
	if family == "ip" || family == "inet" {
		rules = append(rules, relayForwardRules(family, targets, local)...)
	}
	return rules
}

func relayForwardRules(family string, targets []relayTarget, local []netip.Addr) []forwardRule {
	var out []forwardRule
	locals := normalizeForwardLocal(local)
	for _, target := range targets {
		if (target.network != "tcp" && target.network != "udp") || target.port == 0 || target.toPort == 0 || !target.toIP.Is4() {
			continue
		}
		for _, original := range locals {
			base := fmt.Sprintf("relay-%s-%d-%s-%s-%d", target.network, target.port, ipToken(original), ipToken(target.toIP), target.toPort)
			l4 := target.network
			out = append(out,
				relayForwardRule(family, target, original, l4, base+"-original", true),
				relayForwardRule(family, target, original, l4, base+"-reply", false),
			)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].marker < out[j].marker })
	return out
}

func relayForwardRule(family string, target relayTarget, original netip.Addr, l4, suffix string, outbound bool) forwardRule {
	marker := forwardExceptionPrefix + suffix
	var nftMatch string
	var ipt []string
	nfproto := "meta nfproto ipv4 " // only the inet family takes it: ip is IPv4 by itself
	if family == "ip" {
		nfproto = ""
	}
	if outbound {
		nftMatch = fmt.Sprintf(nfproto+`ct status dnat ct direction original ct original ip daddr %s ct original proto-dst %d ip daddr %s %s dport %d`,
			original, target.port, target.toIP, l4, target.toPort)
		ipt = []string{"-p", l4, "-d", target.toIP.String(), "--dport", strconv.Itoa(int(target.toPort)),
			"-m", "conntrack", "--ctstate", "DNAT", "--ctdir", "ORIGINAL", "--ctorigdst", original.String(), "--ctorigdstport", strconv.Itoa(int(target.port)),
			"-m", "comment", "--comment", marker, "-j", "ACCEPT"}
	} else {
		nftMatch = fmt.Sprintf(nfproto+`ct status dnat ct direction reply ct original ip daddr %s ct original proto-dst %d ip saddr %s %s sport %d`,
			original, target.port, target.toIP, l4, target.toPort)
		ipt = []string{"-p", l4, "-s", target.toIP.String(), "--sport", strconv.Itoa(int(target.toPort)),
			"-m", "conntrack", "--ctstate", "DNAT", "--ctdir", "REPLY", "--ctorigdst", original.String(), "--ctorigdstport", strconv.Itoa(int(target.port)),
			"-m", "comment", "--comment", marker, "-j", "ACCEPT"}
	}
	return forwardRule{marker: marker, nft: nftMatch + ` accept comment "` + marker + `"`, ipt: ipt}
}

// planIPTablesRules compares by marker (see forwardingRules). Order: the user chain first (a jump to a missing chain fails),
// inserts before deletes so a replaced rule never leaves a gap, the chain removed last (nothing jumps to it any more). The
// other copies of a marker are deleted by the spec iptables printed for them.
func planIPTablesRules(ipt iptablesForward, desired map[string][]string) ([]firewallCommand, int, int) {
	cmd := func(args ...string) firewallCommand {
		return firewallCommand{binary: ipt.binary, args: append([]string{"-w", "5"}, args...)}
	}
	var prepare, inserts, deletes, tail []firewallCommand
	adds, removes := 0, 0
	if _, jump := desired[forwardExceptionPrefix+"awg-fwd"]; jump && !ipt.chainCurrent() {
		if ipt.chainFound {
			prepare = append(prepare, cmd("-F", forwardChain))
		} else {
			prepare = append(prepare, cmd("-N", forwardChain))
		}
		for _, rule := range forwardChainRules {
			prepare = append(prepare, cmd(append([]string{"-A", forwardChain}, rule...)...))
		}
		adds++
	}
	for _, marker := range sortedMarkers(ipt.rules, desired) {
		want, wanted := desired[marker]
		for i, rule := range ipt.rules[marker] {
			if wanted && i == 0 {
				continue
			}
			deletes = append(deletes, cmd(append([]string{"-D", "FORWARD"}, rule.spec...)...))
		}
		if wanted && len(ipt.rules[marker]) == 0 {
			inserts = append(inserts, cmd(append([]string{"-I", "FORWARD", "1"}, want...)...))
		}
	}
	if _, jump := desired[forwardExceptionPrefix+"awg-fwd"]; !jump && ipt.chainFound {
		tail = append(tail, cmd("-F", forwardChain), cmd("-X", forwardChain))
		removes++
	}
	adds += len(inserts)
	removes += len(deletes)
	return append(append(append(prepare, inserts...), deletes...), tail...), adds, removes
}

func sortedMarkers[R any, D any](have map[string][]R, want map[string]D) []string {
	markers := make([]string, 0, len(have)+len(want))
	for marker := range have {
		markers = append(markers, marker)
	}
	for marker := range want {
		if _, ok := have[marker]; !ok {
			markers = append(markers, marker)
		}
	}
	sort.Strings(markers)
	return markers
}

func planNftRules(chain nftForwardChain, desired map[string]string) ([]string, int, int, error) {
	if !nftNameRe.MatchString(chain.family) || !nftNameRe.MatchString(chain.table) || !nftNameRe.MatchString(chain.chain) {
		return nil, 0, 0, errors.New("nft forward chain has an unsupported name")
	}
	var deletes, inserts []string
	for _, marker := range sortedMarkers(chain.rules, desired) {
		want, wanted := desired[marker]
		for i, rule := range chain.rules[marker] {
			if wanted && i == 0 {
				continue
			}
			if rule.handle == 0 {
				return nil, 0, 0, errors.New("a Mistgate-marked nft rule has no handle")
			}
			deletes = append(deletes, fmt.Sprintf("delete rule %s %s %s handle %d", chain.family, chain.table, chain.chain, rule.handle))
		}
		if wanted && len(chain.rules[marker]) == 0 {
			inserts = append(inserts, fmt.Sprintf("insert rule %s %s %s %s", chain.family, chain.table, chain.chain, want))
		}
	}
	// One nft transaction: the swap of a rule is atomic.
	return append(inserts, deletes...), len(inserts), len(deletes), nil
}

func (h *linuxHost) applyForwardActions(ctx context.Context, actions firewallActions) error {
	if len(actions.nftLines) > 0 {
		if err := h.nft(ctx, strings.Join(actions.nftLines, "\n")+"\n", false); err != nil {
			return fmt.Errorf("update nft forwarding exceptions: %w", err)
		}
	}
	for _, command := range actions.iptables {
		ctx, cancel := context.WithTimeout(ctx, firewallCmdTimeout)
		out, err := h.run(ctx, "", command.binary, command.args...)
		cancel()
		if err != nil {
			return fmt.Errorf("update %s forwarding exceptions: %w: %s", command.binary, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (h *linuxHost) forwardLocalAddresses(targets []relayTarget) ([]netip.Addr, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	read := h.rly.addrs
	if read == nil {
		read = OwnIPv4s
	}
	addresses, err := read()
	if err != nil {
		return nil, fmt.Errorf("read node IPv4 addresses for relay exceptions: %w", err)
	}
	return normalizeForwardLocal(addresses), nil
}

func normalizeForwardLocal(addresses []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(addresses))
	for _, address := range normalizeLocal(addresses) {
		address = address.Unmap()
		if address.Is4() && !address.IsLoopback() {
			out = append(out, address)
		}
	}
	return out
}

func relayTargetsFromRuleset(ruleset string) []relayTarget {
	var out []relayTarget
	walkNftRuleset(ruleset, func(scoped nftScopedLine) {
		if scoped.family != "inet" || scoped.table != NftRelayTable || scoped.chain != "pass" || scoped.chainStart {
			return
		}
		m := nftRelayRuleRe.FindStringSubmatch(scoped.line)
		if m == nil {
			return
		}
		port, e1 := strconv.ParseUint(m[1], 10, 16)
		address, e2 := netip.ParseAddr(m[2])
		toPort, e3 := strconv.ParseUint(m[4], 10, 16)
		if e1 == nil && e2 == nil && e3 == nil && address.Is4() && port > 0 && toPort > 0 {
			out = append(out, relayTarget{network: m[3], port: uint16(port), toIP: address, toPort: uint16(toPort)})
		}
	})
	return normalizeRelayTargets(out)
}

func relayTargetsFromForwards(forwards []Forward) []relayTarget {
	out := make([]relayTarget, 0, len(forwards))
	for _, f := range forwards {
		if f.check() != nil {
			continue
		}
		out = append(out, relayTarget{network: f.Network, port: f.Port, toIP: f.ToIP.Unmap(), toPort: f.ToPort})
	}
	return normalizeRelayTargets(out)
}

func normalizeRelayTargets(targets []relayTarget) []relayTarget {
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		return a.network+strconv.Itoa(int(a.port))+a.toIP.String()+strconv.Itoa(int(a.toPort)) < b.network+strconv.Itoa(int(b.port))+b.toIP.String()+strconv.Itoa(int(b.toPort))
	})
	out := targets[:0]
	for _, target := range targets {
		if len(out) == 0 || out[len(out)-1] != target {
			out = append(out, target)
		}
	}
	return out
}

func ipToken(address netip.Addr) string { return strings.ReplaceAll(address.String(), ".", "-") }

func iptablesOwnsDropChain(snap forwardSnapshot, chain nftForwardChain) bool {
	for _, ipt := range snap.iptables {
		if ipt.policyDrop && strings.Contains(ipt.version, "nf_tables") && ipt.family == chain.family &&
			chain.family != "inet" && chain.table == "filter" && chain.chain == "FORWARD" {
			return true
		}
	}
	return false
}

func iptablesBackend(ipt iptablesForward) string {
	if strings.Contains(ipt.version, "nf_tables") {
		return "iptables-nft"
	}
	if strings.Contains(ipt.version, "legacy") {
		return "iptables-legacy"
	}
	return ipt.binary
}

func hasExternalForwardDrop(snap forwardSnapshot) bool {
	for _, ipt := range snap.iptables {
		if ipt.policyDrop {
			return true
		}
	}
	for _, chain := range snap.nftChains {
		if chain.policyDrop && !OwnNftTable(chain.family, chain.table) && !iptablesOwnsDropChain(snap, chain) {
			return true
		}
	}
	return false
}

func iptablesHasStaticRules(ipt iptablesForward) bool {
	if !ipt.chainCurrent() {
		return false
	}
	for _, want := range forwardingRules(ipt.family, nil, nil) {
		if len(ipt.rules[want.marker]) == 0 {
			return false
		}
	}
	return true
}

func nftHasStaticRules(chain nftForwardChain) bool {
	for _, want := range forwardingRules(chain.family, nil, nil) {
		if len(chain.rules[want.marker]) == 0 {
			return false
		}
	}
	return true
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func missingFirewallTools(err error) bool {
	var commandErr *exec.Error
	return errors.As(err, &commandErr)
}
