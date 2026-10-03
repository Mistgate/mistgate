package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/dnsdefaults"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// ---------------------------------------------------------------------------------------------------
// resolver: the resolver does not resolve the control domains; a RU node must ask Yandex DNS or
// gosuslugi.ru dies. Fix: set_resolver, only when the recommended resolvers answered in this very run.

var controlDomains = []string{"www.cloudflare.com", "www.gstatic.com", "www.google.com"}

const ruDomain = "gosuslugi.ru"

// ResolverTargets is what set_resolver configures: the usable (IP, optionally host:port) entries of
// NodeSettings.dns_resolvers, else the default for the country.
func ResolverTargets(s Settings) []string {
	var out []string
	for _, r := range s.Resolvers {
		if _, ok := dialAddr(r); ok {
			out = append(out, strings.TrimSpace(r))
		}
	}
	if len(out) > 0 {
		return out
	}
	return dnsdefaults.ForCountry(s.Country)
}

// dialAddr turns "ip" or "ip:port" into "ip:port" for dialing; names are not accepted.
func dialAddr(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.String(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return net.JoinHostPort(a.String(), "53"), true
	}
	return "", false
}

func checkResolver(ctx context.Context, e *Env) Result {
	st := e.Settings()
	ru := strings.EqualFold(st.Country, "RU")
	domains := append([]string(nil), controlDomains...)
	if ru {
		domains = append(domains, ruDomain)
	}
	type probe struct {
		err error
		d   time.Duration
	}
	res := make([]probe, len(domains))
	var recommended string
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i].d, res[i].err = timedLookup(ctx, e, d)
		}()
	}
	wg.Add(1)
	go func() { // the recommended resolvers, asked directly and in parallel (the fix needs them to work)
		defer wg.Done()
		for _, t := range ResolverTargets(st) {
			addr, ok := dialAddr(t)
			if !ok {
				continue
			}
			good := true
			for _, d := range append([]string{controlDomains[0]}, domains[len(controlDomains):]...) {
				cctx, cancel := context.WithTimeout(ctx, resolverTimeout)
				err := e.LookupVia(cctx, addr, d)
				cancel()
				if err != nil {
					good = false
					break
				}
			}
			if good {
				recommended = t
				return
			}
		}
	}()
	wg.Wait()

	var failed []string
	var lat []time.Duration
	ruFailed := false
	for i, r := range res {
		if r.err != nil {
			failed = append(failed, domains[i])
			if domains[i] == ruDomain {
				ruFailed = true
			}
		} else {
			lat = append(lat, r.d)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	var median time.Duration
	if len(lat) > 0 {
		median = lat[len(lat)/2]
	}
	r := Result{Status: OK, Code: CodeResolverOK, Params: p("failed", csv(failed), "median_ms", strconv.Itoa(int(median.Milliseconds())), "resolver", recommended,
		"domains", strconv.Itoa(len(domains)), "failed_count", strconv.Itoa(len(failed)))}
	switch {
	case ruFailed || len(failed) >= 2:
		r.Status = Fail
	case len(failed) == 1 || median > resolverSlow:
		r.Status = Warn
	}
	if len(failed) > 0 {
		r.Code = CodeResolverFailed
		r.Detail = fmt.Sprintf("%d of %d control domains do not resolve: %s", len(failed), len(domains), strings.Join(failed, ", "))
	} else {
		r.Detail = fmt.Sprintf("all %d control domains resolve, median %d ms", len(domains), median.Milliseconds())
	}
	if r.Status != OK && recommended != "" {
		r.FixID = FixSetResolver
	}
	return r
}

// timedLookup resolves one name through the host resolver: 3 s, one retry. The duration is that of the
// attempt that answered (or the last one).
func timedLookup(ctx context.Context, e *Env, host string) (time.Duration, error) {
	var d time.Duration
	var err error
	for try := 0; try < 2; try++ {
		cctx, cancel := context.WithTimeout(ctx, resolverTimeout)
		start := e.now()
		err = e.Lookup(cctx, host)
		d = e.now().Sub(start)
		cancel()
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	return d, err
}

// ---------------------------------------------------------------------------------------------------
// ipv6: whether the node has IPv6; without it WARP works only with ForceIPv4. ForceIPv4 is the panel's setting (it
// knows has_ipv6 from the facts); this is the safety net. No fix.

var ipv6Probes = []string{"[2606:4700:4700::1111]:443", "[2001:4860:4860::8888]:443"}

func checkIPv6(ctx context.Context, e *Env) Result {
	has := false
	if s, err := e.read("/proc/net/if_inet6"); err == nil {
		has = hasGlobalInet6(s)
	}
	var warp []string
	for _, in := range e.Inbounds() {
		if in.Enabled && in.Egress == "warp" {
			warp = append(warp, in.ID)
		}
	}
	if !has {
		r := Result{Status: OK, Code: CodeIPv6None, Params: p("ipv6", "no"), Detail: "no global IPv6 address"}
		if len(warp) > 0 {
			r.Status, r.Code = Warn, CodeIPv6NoneWarp
			r.Params["warp_inbounds"] = csv(warp)
			r.Detail = "no global IPv6 address and WARP egress is in use"
		}
		return r
	}
	var wg sync.WaitGroup
	ok := make([]bool, len(ipv6Probes))
	for i, a := range ipv6Probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, ipv6Timeout)
			defer cancel()
			ok[i] = e.Dial(cctx, "tcp6", a) == nil
		}()
	}
	wg.Wait()
	for _, v := range ok {
		if v {
			return Result{Status: OK, Code: CodeIPv6OK, Params: p("ipv6", "yes", "connect", "ok"), Detail: "global IPv6 address, outbound connect works"}
		}
	}
	return Result{Status: Warn, Code: CodeIPv6Stalls, Params: p("ipv6", "yes", "connect", "failed"),
		Detail: "global IPv6 address but an outbound IPv6 connect fails; clients that get AAAA answers will stall"}
}

// hasGlobalInet6 reads /proc/net/if_inet6 ("<32 hex> ifindex prefixlen scope flags name"): scope 00 is global;
// ULA (fc00::/7), tentative (0x40) and DAD-failed (0x08) addresses do not count.
func hasGlobalInet6(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || len(f[0]) != 32 || f[3] != "00" {
			continue
		}
		if b := strings.ToLower(f[0][:2]); b == "fc" || b == "fd" {
			continue
		}
		if fl, err := strconv.ParseUint(f[4], 16, 32); err == nil && fl&0x48 != 0 {
			continue
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------------------------------
// foreign_vpn: old VPN services: x-ui, xray, remnanode, amnezia containers, old wg interfaces.
// No fix: removal is destructive, the owner decides.

var (
	vpnUnitRe      = regexp.MustCompile(`^(x-ui|xray|remnanode[\w.-]*|hysteria-server(@[\w.-]+)?|wg-quick@[\w.-]+)\.service$`)
	vpnContainerRe = regexp.MustCompile(`(?i)xray|x-ui|3x-ui|remnanode|amnezia|awg`)
	vpnProcs       = map[string]bool{"xray": true, "x-ui": true, "sing-box": true}
)

func checkForeignVPN(ctx context.Context, e *Env) Result {
	var names []string
	var notes []string

	if e.has("systemctl") {
		units := map[string]bool{}
		for _, args := range [][]string{
			{"list-units", "--all", "--type=service", "--no-legend", "--no-pager", "--plain"},
			{"list-unit-files", "--type=service", "--no-legend", "--no-pager", "--plain"},
		} {
			out, err := e.Run(ctx, "systemctl", args...)
			if err != nil {
				notes = append(notes, "systemctl "+args[0]+" failed")
				continue
			}
			for _, line := range strings.Split(string(out), "\n") {
				f := strings.Fields(strings.TrimLeft(line, " ●*"))
				if len(f) > 0 && vpnUnitRe.MatchString(f[0]) {
					units[f[0]] = true
				}
			}
		}
		for u := range units {
			names = append(names, "unit:"+strings.TrimSuffix(u, ".service"))
		}
	} else {
		notes = append(notes, "no systemctl")
	}

	containers, derr := e.Docker(ctx)
	_ = derr // no docker here is the normal case
	var contPorts []int
	for _, c := range containers {
		if vpnContainerRe.MatchString(c.Image) || vpnContainerRe.MatchString(strings.Join(c.Names, " ")) {
			n := strings.TrimPrefix(firstOr(c.Names, c.Image), "/")
			names = append(names, "docker:"+n)
			contPorts = append(contPorts, c.Ports...)
		}
	}

	if ents, err := e.readDir("/sys/class/net"); err == nil {
		for _, ent := range ents {
			n := ent.Name()
			if (strings.HasPrefix(n, "wg") || strings.HasPrefix(n, "awg")) && (e.OwnIface == nil || !e.OwnIface(n)) {
				names = append(names, "iface:"+n)
			}
		}
	}

	foreignPIDs := map[int]string{}
	for pid, comm := range procComms(e) {
		if vpnProcs[comm] && pid != e.SelfPID {
			foreignPIDs[pid] = comm
			names = append(names, "proc:"+comm)
		}
	}

	names = uniq(sorted(names))
	if len(names) == 0 {
		d := "no other VPN stack found"
		if len(notes) > 0 {
			d += " (" + strings.Join(notes, ", ") + ")"
		}
		return Result{Status: OK, Code: CodeVPNNone, Params: p(), Detail: d}
	}

	// FAIL when one of them sits on a port of ours: a process by its sockets, a container by published ports.
	var clash []string
	ours := inboundPorts(e.Inbounds())
	for _, l := range listeners(e) {
		if comm, ok := foreignPIDs[l.pid]; ok && ours[portKey{l.network, l.port}] {
			clash = append(clash, fmt.Sprintf("%s:%s/%d", comm, l.network, l.port))
		}
	}
	for _, port := range contPorts {
		for k := range ours {
			if k.port == port {
				clash = append(clash, fmt.Sprintf("docker:%s/%d", k.network, port))
			}
		}
	}
	r := Result{Status: Warn, Code: CodeVPNFound, Params: p("names", strings.Join(firstN(names, maxNames), ","), "count", strconv.Itoa(len(names)))}
	r.Detail = "found: " + strings.Join(firstN(names, maxNames), ", ")
	if clash = uniq(sorted(clash)); len(clash) > 0 {
		r.Status, r.Code = Fail, CodeVPNClash
		r.Params["on_our_ports"] = strings.Join(clash, ",")
		r.Detail += "; on our ports: " + strings.Join(clash, ", ")
	}
	return r
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// ---------------------------------------------------------------------------------------------------
// listeners and inbound ports, shared by port_conflicts and foreign_vpn

type listener struct {
	network string
	port    int
	pid     int // 0 = owner unknown (could not read /proc/<pid>/fd)
	comm    string
}

// listeners resolves the owner of every listening socket of the host once per run.
func listeners(e *Env) []listener {
	return e.memo.listen.get(func() []listener {
		socks := listenSockets(e)
		want := map[uint64]bool{}
		for _, s := range socks {
			want[s.inode] = true
		}
		owners := socketOwners(e, want)
		comms := procComms(e)
		out := make([]listener, 0, len(socks))
		for _, s := range socks {
			pid := owners[s.inode]
			out = append(out, listener{network: s.network, port: s.port, pid: pid, comm: comms[pid]})
		}
		return out
	})
}

type portKey struct {
	network string
	port    int
}

// inboundPorts is the set of (network, port) pairs of the enabled inbounds.
func inboundPorts(ibs []Inbound) map[portKey]bool {
	m := map[portKey]bool{}
	for _, in := range ibs {
		if in.Enabled {
			m[portKey{in.Network, in.Port}] = true
		}
	}
	return m
}

// tcpPortIsOurs: a TCP listener of this agent holds the port. When the socket table cannot be read the answer
// is yes (nothing to contradict it); when it can and the agent is not listening there, a certificate read from
// that port would be someone else's (Caddy on 443, say), so it is not used.
func tcpPortIsOurs(e *Env, port int) bool {
	if _, err := e.stat("/proc/net/tcp"); err != nil {
		return true
	}
	for _, l := range listeners(e) {
		if l.network == "tcp" && l.port == port && l.pid == e.SelfPID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------------
// foreign_nft: foreign nft tables (for example leftover port-hopping redirects of other tools). No fix:
// deleting a table is not safe. Legacy iptables (xtables) rules are not visible to `nft list ruleset`
// (not checked).

type nftDoc struct {
	Nftables []struct {
		Table *struct{ Family, Name string }                    `json:"table"`
		Chain *struct{ Family, Table, Name, Type, Hook string } `json:"chain"`
		Rule  *struct {
			Family, Table, Chain string
			Expr                 []map[string]json.RawMessage
		} `json:"rule"`
	} `json:"nftables"`
}

func checkForeignNft(ctx context.Context, e *Env) Result {
	if !e.has("nft") {
		return skip(CodeNftNone, "nft is not installed")
	}
	out, err := e.Run(ctx, "nft", "-j", "list", "ruleset")
	if err != nil {
		return skip(CodeNftError, "nft list ruleset failed: "+firstLine(err.Error()))
	}
	var doc nftDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return skip(CodeNftError, "nft output not parsable (too large?)")
	}
	own := func(family, name string) bool {
		return family == "inet" && (name == hostctl.NftTable || name == hostctl.NftTunnelTable || name == hostctl.NftWarpTable)
	}

	ours := portSpecs(e.Inbounds())
	natTables := map[string]bool{}
	tables := map[string]bool{}
	var rules int
	var hit []string
	for _, it := range doc.Nftables {
		switch {
		case it.Table != nil && !own(it.Table.Family, it.Table.Name):
			tables[it.Table.Family+" "+it.Table.Name] = true
		case it.Chain != nil && !own(it.Chain.Family, it.Chain.Table) && it.Chain.Type == "nat":
			natTables[it.Chain.Family+" "+it.Chain.Table] = true
		case it.Rule != nil && !own(it.Rule.Family, it.Rule.Table):
			rules++
			if ports := ruleHitsUs(it.Rule.Expr, ours); len(ports) > 0 {
				for _, pt := range ports {
					hit = append(hit, strconv.Itoa(pt))
				}
			}
		}
	}
	if len(tables) == 0 {
		return Result{Status: OK, Code: CodeNftClean, Params: p(), Detail: "only the mistgate tables are present"}
	}
	r := Result{Status: OK, Code: CodeNftFound, Params: p("tables", strings.Join(firstN(sorted(keys(tables)), maxNames), ","),
		"table_count", strconv.Itoa(len(tables)), "rules", strconv.Itoa(rules))}
	r.Detail = fmt.Sprintf("%d foreign table(s), %d rule(s)", len(tables), rules)
	if len(natTables) > 0 {
		r.Status, r.Code = Warn, CodeNftNat
		r.Params["nat_tables"] = strings.Join(firstN(sorted(keys(natTables)), maxNames), ",")
		r.Detail += "; nat hook in: " + r.Params["nat_tables"]
	}
	if hit = uniq(sorted(hit)); len(hit) > 0 {
		r.Status, r.Code = Fail, CodeNftHits
		r.Params["ports"] = strings.Join(hit, ",")
		r.Detail += "; foreign rule redirects or drops our port(s) " + strings.Join(hit, ",")
	}
	return r
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return clip(s, 100)
}

type portSpec struct {
	network  string
	from, to int
}

// portSpecs lists every port of ours that a firewall rule could break: each inbound's port and its hop range.
func portSpecs(ibs []Inbound) []portSpec {
	var out []portSpec
	for _, in := range ibs {
		if !in.Enabled {
			continue
		}
		out = append(out, portSpec{in.Network, in.Port, in.Port})
		if in.HopFrom != 0 {
			out = append(out, portSpec{in.Network, in.HopFrom, in.HopTo})
		}
	}
	return out
}

// ruleHitsUs returns our ports that a rule with a destination-port match and a redirect/dnat/drop/reject
// statement covers. A rule whose protocol is known and differs from the inbound's network does not count.
func ruleHitsUs(expr []map[string]json.RawMessage, ours []portSpec) []int {
	acts := false
	type m struct {
		proto    string
		from, to int
	}
	var matches []m
	for _, x := range expr {
		for k, raw := range x {
			switch k {
			case "redirect", "dnat", "drop", "reject":
				acts = true
			case "match":
				var mt struct {
					Left struct {
						Payload *struct{ Protocol, Field string } `json:"payload"`
					} `json:"left"`
					Right json.RawMessage `json:"right"`
				}
				if json.Unmarshal(raw, &mt) != nil || mt.Left.Payload == nil || mt.Left.Payload.Field != "dport" {
					continue
				}
				for _, rg := range portRanges(mt.Right) {
					matches = append(matches, m{mt.Left.Payload.Protocol, rg[0], rg[1]})
				}
			}
		}
	}
	if !acts {
		return nil
	}
	var hit []int
	for _, mt := range matches {
		for _, o := range ours {
			protoOK := mt.proto == "" || mt.proto == "th" || mt.proto == o.network
			if protoOK && mt.from <= o.to && o.from <= mt.to {
				hit = append(hit, max(mt.from, o.from))
			}
		}
	}
	return hit
}

// portRanges reads the right-hand side of an nft JSON match: a number, {"range":[a,b]} or {"set":[...]}.
func portRanges(raw json.RawMessage) [][2]int {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return [][2]int{{n, n}}
	}
	var obj struct {
		Range []int             `json:"range"`
		Set   []json.RawMessage `json:"set"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	if len(obj.Range) == 2 {
		return [][2]int{{obj.Range[0], obj.Range[1]}}
	}
	var out [][2]int
	for _, el := range obj.Set {
		out = append(out, portRanges(el)...)
	}
	return out
}

// ---------------------------------------------------------------------------------------------------
// port_conflicts: what already holds the ports (Caddy must not take UDP 443). Fix restart_inbound only
// for an inbound that failed to bind and whose port is free now.

var bindErrRe = regexp.MustCompile(`(?i)address already in use|bind|listen`)

func checkPortConflicts(_ context.Context, e *Env) Result {
	ibs := e.Inbounds()
	var enabled []Inbound
	for _, in := range ibs {
		if in.Enabled {
			enabled = append(enabled, in)
		}
	}
	if len(enabled) == 0 {
		return Result{Status: OK, Code: CodePortsNone, Params: p(), Detail: "no enabled inbound"}
	}
	_, e1 := e.stat("/proc/net/udp")
	_, e2 := e.stat("/proc/net/tcp")
	if e1 != nil && e2 != nil {
		return skip(CodePortsNoTable, "no socket table readable (/proc/net)")
	}
	ls := listeners(e)

	type hit struct {
		in    Inbound
		procs []string
	}
	var own, hop, tcp []hit
	var recoverable []Inbound
	for _, in := range enabled {
		var holders, inRange []string
		free := true
		for _, l := range ls {
			if l.network != in.Network {
				continue
			}
			// A running awg inbound on the kernel backend holds its UDP port with a kernel socket (inode 0, no owner process).
			foreign := l.pid != e.SelfPID && !(l.pid == 0 && in.Protocol == "awg" && in.State == "running")
			name := l.comm
			if name == "" {
				name = "unknown"
			}
			switch {
			case l.port == in.Port:
				free = false
				if foreign {
					holders = append(holders, fmt.Sprintf("%s(%d)", name, l.pid))
				}
			case in.HopFrom != 0 && l.port >= in.HopFrom && l.port <= in.HopTo && foreign:
				inRange = append(inRange, fmt.Sprintf("%s:%d", name, l.port))
			}
		}
		if len(holders) > 0 {
			own = append(own, hit{in, uniq(sorted(holders))})
		}
		if in.TLSPort > 0 { // the HTTPS listener (decoy and ACME) sits on a TCP port of its own
			var th []string
			for _, l := range ls {
				if l.network == "tcp" && l.port == in.TLSPort && l.pid != e.SelfPID {
					th = append(th, fmt.Sprintf("%s(%d)", orUnknown(l.comm), l.pid))
				}
			}
			if len(th) > 0 {
				tcp = append(tcp, hit{in, uniq(sorted(th))})
			}
		}
		if len(inRange) > 0 {
			hop = append(hop, hit{in, firstN(uniq(sorted(inRange)), maxNames)})
		}
		if in.State == "failed" && bindErrRe.MatchString(in.Error) && free {
			recoverable = append(recoverable, in)
		}
	}

	r := Result{Status: OK, Code: CodePortsOK, Params: p("checked", strconv.Itoa(len(enabled))), Detail: fmt.Sprintf("%d inbound port(s) checked, no foreign listener", len(enabled))}
	if len(hop) > 0 {
		h := hop[0]
		r.Status, r.Code = Warn, CodePortsHop
		r.Params = p("inbound_id", h.in.ID, "hop_holders", strings.Join(h.procs, ","), "hop_from", strconv.Itoa(h.in.HopFrom), "hop_to", strconv.Itoa(h.in.HopTo))
		r.Detail = fmt.Sprintf("foreign listener inside the hop range %d-%d of %s: %s", h.in.HopFrom, h.in.HopTo, h.in.ID, strings.Join(h.procs, ", "))
	}
	if len(tcp) > 0 {
		h := tcp[0]
		r.Status, r.Code = Warn, CodePortsTLS
		r.Params = p("inbound_id", h.in.ID, "port", strconv.Itoa(h.in.TLSPort), "network", "tcp", "process", strings.Join(h.procs, ","))
		r.Detail = fmt.Sprintf("tcp/%d (HTTPS decoy and ACME of %s) is held by %s", h.in.TLSPort, h.in.ID, strings.Join(h.procs, ", "))
	}
	if len(recoverable) > 0 {
		in := recoverable[0]
		r.Status, r.Code = max(r.Status, Warn), CodePortsFailedBnd
		r.Params = p("inbound_id", in.ID, "port", strconv.Itoa(in.Port), "network", in.Network)
		r.Detail = fmt.Sprintf("inbound %s failed to bind %s/%d and the port is free now", in.ID, in.Network, in.Port)
		r.FixID = FixRestartInbound
	}
	if len(own) > 0 {
		h := own[0]
		r.Status, r.Code = Fail, CodePortsHeld
		r.Params = p("inbound_id", h.in.ID, "port", strconv.Itoa(h.in.Port), "network", h.in.Network, "process", strings.Join(h.procs, ","))
		r.Detail = fmt.Sprintf("%s/%d of inbound %s is held by %s", h.in.Network, h.in.Port, h.in.ID, strings.Join(h.procs, ", "))
		r.FixID = "" // restarting cannot free a port a foreign process holds
	}
	return r
}

// ---------------------------------------------------------------------------------------------------
// net_baseline: fq + BBR and the journald cap; exactly what hostctl.ApplyBaseline installs (the
// SSH guard and conntrack tuning are not part of it). Fix: apply_baseline.

// baselineNotes are the tokens baselineDiff returns as notes, with their English wording (the UI has its own).
var baselineNotes = map[string]string{
	"no_bbr":     "bbr is not available in this kernel",
	"container":  "qdisc and congestion control cannot be set in a container",
	"no_systemd": "no systemd",
}

// noteText is the English sentence of note tokens.
func noteText(notes []string) string {
	t := make([]string, len(notes))
	for i, n := range notes {
		t[i] = baselineNotes[n]
	}
	return strings.Join(t, "; ")
}

// baselineDiff lists what differs from the baseline. fixable are the differences ApplyBaseline can remove; notes are
// tokens from baselineNotes.
func baselineDiff(e *Env) (diff, fixable []string, notes []string) {
	if !e.container() {
		for _, kv := range []struct{ name, file, want string }{
			{"default_qdisc", "/proc/sys/net/core/default_qdisc", "fq"},
			{"tcp_congestion_control", "/proc/sys/net/ipv4/tcp_congestion_control", "bbr"},
		} {
			cur, err := e.read(kv.file)
			if err != nil || strings.TrimSpace(cur) == kv.want {
				continue
			}
			diff = append(diff, kv.name)
			if kv.want == "bbr" {
				if avail, err := e.read("/proc/sys/net/ipv4/tcp_available_congestion_control"); err == nil && !containsField(avail, "bbr") {
					notes = append(notes, "no_bbr")
					continue // ApplyBaseline cannot load it
				}
			}
			fixable = append(fixable, kv.name)
		}
		if cur, err := e.read(hostctl.SysctlFilePath); err != nil || cur != hostctl.SysctlFileBody {
			diff = append(diff, "sysctl_file")
			fixable = append(fixable, "sysctl_file")
		}
	} else {
		notes = append(notes, "container")
	}
	if _, err := e.stat("/run/systemd/system"); err == nil {
		if cur, err := e.read(hostctl.JournaldFilePath); err != nil || cur != hostctl.JournaldFileBody {
			diff = append(diff, "journald_file")
			fixable = append(fixable, "journald_file")
		}
	} else {
		notes = append(notes, "no_systemd")
	}
	return diff, fixable, notes
}

func containsField(s, want string) bool {
	for _, f := range strings.Fields(s) {
		if f == want {
			return true
		}
	}
	return false
}

func checkNetBaseline(_ context.Context, e *Env) Result {
	diff, fixable, notes := baselineDiff(e)
	if len(diff) == 0 {
		if len(notes) > 0 {
			return Result{Status: OK, Code: CodeBaselineNotes, Params: p("notes", strings.Join(notes, ",")), Detail: noteText(notes)}
		}
		return Result{Status: OK, Code: CodeBaselineOK, Params: p(), Detail: "fq, bbr and the journald cap are in place"}
	}
	r := Result{Status: Warn, Code: CodeBaselineDiff, Params: p("differs", strings.Join(diff, ","), "notes", strings.Join(notes, ","))}
	r.Detail = "baseline differs: " + strings.Join(diff, ", ")
	if len(notes) > 0 {
		r.Detail += "; " + noteText(notes)
	}
	if len(fixable) > 0 {
		r.FixID = FixApplyBaseline
	}
	return r
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
