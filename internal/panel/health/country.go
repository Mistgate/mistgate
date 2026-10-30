package health

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The country check: "does the node answer from inside a country". The panel's server is not in that country, so its own
// checks cannot see a block there. A CountrySource (country_source.go) runs probes in the country; this file picks the
// targets, keeps the test budget, turns the probes' answers into a verdict per node, stores the rounds and raises the
// COUNTRY_BLOCKED alert (eval.go calls addCountryConds). It runs the same way on the VPS and on the Cloudflare edition: no
// goroutines, one round at a time, the clock decides when the next one is due.

const (
	kCountryBlocked = "country_blocked"

	verdictOK         = "ok"
	verdictPartial    = "partial"
	verdictTCPBlocked = "tcp_blocked"
	verdictBlocked    = "blocked"
	verdictUnknown    = "unknown"
	verdictListed     = "listed" // only ever in the combined view (countryreach.Combine), never stored by a live round

	countryMaxProbes   = 3                  // probes per measurement
	countryMinProbes   = 2                  // fewer is no comparison; the budget never cuts below it
	countryMinValid    = 2                  // valid probes a verdict needs
	countryBudgetShare = 80                 // percent of the hourly limit a plan may use
	countryMaxPorts    = 4                  // TCP ports tried per node
	countryAlertRounds = 2                  // rounds in a row that open or close the alert
	countryRecent      = 10                 // rounds read back per node
	countryKeep        = 7 * 24 * time.Hour // retention
	countryMaxInterval = 24 * time.Hour
	countryRoundTime   = 60 * time.Second // the whole round, controls and polling included
	countryHoldSetting = "health.country_check.hold_until"
	familyV4           = 4 // the address families of country-check rows. IPv6 rows are measured by Globalping alone; they are read by the
	familyV6           = 6 // nested CountryCheck.ipv6, the IPv6 history and the IPv6 alert of a node with the IPv6 entry
)

// countryControls is the site every probe must reach, per country: big, domestic and never blocked there. Other countries
// use Cloudflare's.
var countryControls = map[string]string{
	"RU": "ya.ru", "BY": "onliner.by", "KZ": "kaspi.kz", "CN": "baidu.com", "IR": "digikala.com", "TR": "hurriyet.com.tr",
}

func countryControl(country string) (string, uint16) {
	if host, ok := countryControls[country]; ok {
		return host, 443
	}
	return "www.cloudflare.com", 443
}

// countryControl6 is the control site of an IPv6 round: a name that has an IPv6 address and is never blocked. Only Russia's is
// domestic; every other country uses Cloudflare's, and no other country's site is guessed to have an AAAA record.
func countryControl6(country string) (string, uint16) {
	if country == "RU" {
		return "ya.ru", 443
	}
	return "www.cloudflare.com", 443
}

// ---------------------------------------------------------------------------------------------------
// Budget

// countryNode is an online node that takes part in a round.
type countryNode struct {
	node      store.NodeRow
	ports     []uint16
	preferred uint16 // the TLS endpoint a paid source tests: see countryPreferredPort
	ipv6      ipv6Facts
}

// countryCost is the tests of the rounds of one interval with k probes: per family a control, then per node one ICMP and one
// per TCP port, each run by every probe. The IPv4 round has every node; the IPv6 round only the nodes whose IPv6 works, and
// only exists when there is one.
func countryCost(nodes []countryNode, k int) int {
	n, n6 := 1, 0
	for _, c := range nodes {
		n += 1 + len(c.ports)
		if c.ipv6.state == ipv6OK {
			n6 += 1 + len(c.ports)
		}
	}
	if n6 > 0 {
		n += 1 + n6
	}
	return n * k
}

// countryPlan chooses the probes per measurement and the interval so that the rounds of an hour (one IPv4 round and, while the
// IPv6 checks are on, one IPv6 round per interval) stay within 80 % of the source's limit: 3 probes if that fits, else 2, else
// 2 and a longer interval. limit 0 = unlimited.
func countryPlan(nodes []countryNode, interval time.Duration, limit int) (probes int, effective time.Duration, perHour int) {
	hourly := func(k int, every time.Duration) int {
		return int((int64(countryCost(nodes, k))*int64(time.Hour) + int64(every) - 1) / int64(every))
	}
	budget := limit * countryBudgetShare / 100
	for k := countryMaxProbes; k >= countryMinProbes; k-- {
		if limit == 0 || hourly(k, interval) <= budget {
			return k, interval, hourly(k, interval)
		}
	}
	every := time.Duration(int64(countryCost(nodes, countryMinProbes)) * int64(time.Hour) / int64(max(budget, 1)))
	every = max(min(every.Truncate(time.Minute)+time.Minute, countryMaxInterval), interval)
	return countryMinProbes, every, hourly(countryMinProbes, every)
}

// ---------------------------------------------------------------------------------------------------
// Verdict

// The stored detail of a round (country_check.detail_json). It holds no address of a probe.
type countryDetail struct {
	Probes  []countryProbeDetail `json:"probes,omitempty"`
	Reason  string               `json:"reason,omitempty"`
	Eyeball bool                 `json:"eyeball,omitempty"`
	Ports   []uint16             `json:"ports,omitempty"` // the TCP ports that were tried
	NoProbe []string             `json:"no_probe,omitempty"`
	Asked   []string             `json:"asked,omitempty"`
}

type countryProbeDetail struct {
	Network string              `json:"network"`
	ASN     uint32              `json:"asn,omitempty"`
	City    string              `json:"city,omitempty"`
	Eyeball bool                `json:"eyeball,omitempty"`
	Valid   bool                `json:"valid"`
	Dark    bool                `json:"dark,omitempty"`
	ICMPOK  bool                `json:"icmp_ok"`
	ICMPMs  uint32              `json:"icmp_ms,omitempty"`
	TCP     []countryPortDetail `json:"tcp,omitempty"`
}

type countryPortDetail struct {
	Port uint16 `json:"port"`
	OK   bool   `json:"ok"`
	MS   uint32 `json:"ms,omitempty"`
}

// countryJudge turns what the probes saw of one target into a verdict. Only valid probes count: online, through the control
// site, and answered for this target. At least two of them, or the verdict is unknown. With TCP ports, TCP carries the
// verdict (a probe's TCP counts when any port connects); an ICMP-only node is judged by ICMP. A split picture, for example
// TCP on a third of the probes, says nothing and is unknown rather than a false alarm. An otherwise-ok or mixed picture is
// partial when a complete eyeball probe misses this target but reaches another target in the round, and another probe reaches it.
func countryJudge(t CountryTarget, obs CountryObserved, reached []bool) (verdict string, d countryDetail) {
	d.Eyeball, d.Ports = obs.Eyeball, t.Ports
	var valid, icmpN, tcpN, reachN, darkN int
	for i, p := range obs.Probes {
		o, has := p.Nodes[t.Key]
		pd := countryProbeDetail{Network: p.Network, ASN: p.ASN, City: p.City, Valid: p.Online && p.ControlOK && has && o.Answered,
			ICMPOK: o.ICMPOK, ICMPMs: o.ICMPMs}
		pd.Eyeball = p.Eyeball
		anyTCP := false
		for _, tp := range o.TCP {
			pd.TCP = append(pd.TCP, countryPortDetail{Port: tp.Port, OK: tp.OK, MS: tp.MS})
			anyTCP = anyTCP || tp.OK
		}
		pd.Dark = pd.Valid && p.Eyeball && o.Complete && !o.ICMPOK && !anyTCP && i < len(reached) && reached[i]
		if pd.Valid {
			valid++
			if o.ICMPOK {
				icmpN++
			}
			if anyTCP {
				tcpN++
			}
			if (len(t.Ports) > 0 && anyTCP) || (len(t.Ports) == 0 && o.ICMPOK) {
				reachN++
			}
			if pd.Dark {
				darkN++
			}
		}
		d.Probes = append(d.Probes, pd)
	}
	if valid < countryMinValid {
		d.Reason = "too_few_probes"
		return verdictUnknown, d
	}
	half := func(n int) bool { return n*2 >= valid }
	switch hasTCP := len(t.Ports) > 0; {
	case hasTCP && half(tcpN), !hasTCP && half(icmpN):
		verdict = verdictOK
	case hasTCP && tcpN == 0 && half(icmpN):
		verdict = verdictTCPBlocked
	case icmpN == 0 && tcpN == 0:
		verdict = verdictBlocked
	default:
		d.Reason = "mixed"
		verdict = verdictUnknown
	}
	if (verdict == verdictOK || d.Reason == "mixed") && darkN > 0 && reachN > 0 {
		d.Reason = ""
		return verdictPartial, d
	}
	return verdict, d
}

// countryReached records whether each probe got an ICMP or TCP response from any target in this round. A probe that did not
// reach the current target can then be called dark only when it reached a different target.
func countryReached(obs CountryObserved, targets []CountryTarget, icmpOnly bool) []bool {
	reached := make([]bool, len(obs.Probes))
	for i, p := range obs.Probes {
		for _, target := range targets {
			n, ok := p.Nodes[target.Key]
			if !ok {
				continue
			}
			if n.ICMPOK {
				reached[i] = true
				break
			}
			for _, tcp := range n.TCP {
				if tcp.OK && !icmpOnly {
					reached[i] = true
					break
				}
			}
			if reached[i] {
				break
			}
		}
	}
	return reached
}

// ---------------------------------------------------------------------------------------------------
// Targets

// hy2DecoyPort is the TCP port of a Hysteria2 inbound's HTTPS decoy (443 unless the profile says otherwise, 0 = none), the
// same rule as the agent's doctor.
func hy2DecoyPort(t *target) uint16 {
	if t.spec.TLS.Mode == 0 {
		return 0
	}
	var j struct {
		Masquerade struct {
			Type    string `json:"type"`
			TCPPort *int   `json:"tcp_port"`
		} `json:"masquerade"`
	}
	if len(t.settings) > 0 && json.Unmarshal(t.settings, &j) != nil {
		return 0
	}
	if strings.EqualFold(j.Masquerade.Type, "none") {
		return 0
	}
	if p := j.Masquerade.TCPPort; p != nil {
		return uint16(max(0, min(*p, 65535)))
	}
	return 443
}

// countryPorts are the distinct TCP ports a node answers on: the ports of its active TCP inbounds (VLESS) and the HTTPS
// decoy of its Hysteria2 ones. A node with only UDP inbounds and no decoy has none and is checked by ICMP alone.
func countryPorts(ts []*target) []uint16 {
	var ports []uint16
	add := func(p uint16) {
		if p != 0 && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	for _, t := range ts {
		if !t.in.Enabled || t.err != nil || t.in.State != "active" {
			continue
		}
		switch {
		case t.spec.Listen.Network == "tcp":
			add(t.spec.Listen.Port)
		case t.in.Protocol == "hysteria2":
			add(hy2DecoyPort(t))
		}
	}
	slices.Sort(ports)
	return ports[:min(len(ports), countryMaxPorts)]
}

// countryPreferredPort is the TCP port of a TLS endpoint a paid source should test: the VLESS REALITY port or the Hysteria2
// decoy site (the lowest of them). 0: the node has none, and the source falls back to 443, then the lowest port.
func countryPreferredPort(ts []*target) uint16 {
	ports := countryPorts(ts)
	var tls []uint16
	for _, t := range ts {
		if !t.in.Enabled || t.err != nil || t.in.State != "active" {
			continue
		}
		var p uint16
		switch {
		case t.in.Protocol == "hysteria2":
			p = hy2DecoyPort(t)
		case t.in.Protocol == "vless" && t.spec.Listen.Network == "tcp":
			var settings struct {
				Security string `json:"security"`
			}
			if json.Unmarshal(t.settings, &settings) == nil && strings.EqualFold(settings.Security, "reality") {
				p = t.spec.Listen.Port
			}
		}
		if slices.Contains(ports, p) {
			tls = append(tls, p)
		}
	}
	if len(tls) == 0 {
		return 0
	}
	return slices.Min(tls)
}

// CountryTargets are the online nodes as a paid external source tests them: the IPv4 (empty when the node has none, so the
// source can say so), the TCP ports and the preferred one.
func (s *Service) CountryTargets(ctx context.Context) ([]CountryTarget, error) {
	nodes, err := s.countryNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CountryTarget, 0, len(nodes))
	for _, n := range nodes {
		ip, _ := s.countryAddress(ctx, n.node)
		out = append(out, CountryTarget{Key: n.node.ID, IP: ip, Ports: n.ports, PreferredPort: n.preferred})
	}
	return out, nil
}

// countryNodes are the nodes the panel sees online: not retired, agent connected.
func (s *Service) countryNodes(ctx context.Context) ([]countryNode, error) {
	sn, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		return nil, err
	}
	live := indexLiveRows(liveRows)
	var out []countryNode
	for _, n := range sn.nodes {
		if n.State != "active" || !live[n.ID].Connected {
			continue
		}
		out = append(out, countryNode{node: n, ports: countryPorts(sn.byNode[n.ID]), preferred: countryPreferredPort(sn.byNode[n.ID])})
	}
	return out, nil
}

func parseAddr(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	return a.Unmap(), err == nil
}

// countryAddress is the node's IPv4: its address, or for a name the first IPv4 the panel resolves it to. ok is false when it
// has none.
func (s *Service) countryAddress(ctx context.Context, n store.NodeRow) (string, bool) {
	host := strings.TrimSpace(n.Address)
	if a, isIP := parseAddr(host); isIP {
		return a.String(), a.Is4()
	}
	if host == "" || s.cfg.Resolve == nil {
		return "", false
	}
	addrs, err := s.cfg.Resolve(ctx, host)
	for _, a := range addrs {
		if a = a.Unmap(); err == nil && a.Is4() {
			return a.String(), true
		}
	}
	return "", false
}

// The IPv6 state of a node, from its doctor's ipv6 check: whether it can be measured over IPv6 at all.
const (
	ipv6OK       = store.IPv6OK       // outbound IPv6 works, so the node has a working IPv6 uplink
	ipv6None     = store.IPv6None     // no global IPv6 address
	ipv6Broken   = store.IPv6Broken   // an address, but outbound IPv6 fails (so does the inbound one): nothing to test
	ipv6AgentOld = store.IPv6AgentOld // IPv6 works, but the agent does not say which address
	ipv6Unknown  = store.IPv6Unknown  // the doctor has not reported yet
)

// ipv6Reasons are the reasons of the unknown rows of the nodes that are not measured over IPv6 (CountryCheck.ipv6.reason).
var ipv6Reasons = map[string]string{ipv6None: "no_ipv6", ipv6Broken: "ipv6_broken", ipv6AgentOld: "agent_old", ipv6Unknown: "not_checked"}

// ipv6Facts is what the doctor said about a node's IPv6: the state and the source address the kernel picks for outbound IPv6
// (the uplink's own stable address when the agent could not name it). Zero state is "not looked at" and reads as unknown.
type ipv6Facts struct{ state, src string }

// usableIPv6 is s as a global unicast IPv6 address (not a unique-local, link-local or mapped IPv4 one).
func usableIPv6(s string) (string, bool) { return store.UsableIPv6(s) }

// ipv6FactsOf reads the params of the doctor's ipv6 check (store.DoctorIPv6). nil = no report.
func ipv6FactsOf(row *store.DoctorRow) ipv6Facts {
	d := store.DoctorIPv6(row)
	if d.State != ipv6OK {
		return ipv6Facts{state: d.State}
	}
	return ipv6Facts{state: ipv6OK, src: d.FirstAddr()}
}

// countryIPv6Facts reads the doctor's ipv6 check of every node into the nodes. A read that fails leaves them unknown.
func (s *Service) countryIPv6Facts(ctx context.Context, nodes []countryNode) {
	rows, err := s.st.DoctorResults(ctx, "")
	if err != nil {
		s.log.Warn("health: country check IPv6 facts", "err", err)
	}
	byNode := map[string]*store.DoctorRow{}
	for i := range rows {
		if rows[i].CheckID == "ipv6" {
			byNode[rows[i].NodeID] = &rows[i]
		}
	}
	for i := range nodes {
		nodes[i].ipv6 = ipv6FactsOf(byNode[nodes[i].node.ID])
	}
}

// nodeIPv6 is the IPv6 address a node is measured on and its state. Only a node whose doctor says that outbound IPv6 works is
// measured (state ok): an address that the host has but the hoster does not route would read as "blocked". The address is the
// IPv6 entry's when it is on (it is the address people use), else the node's own when its Address is an IPv6 literal, else the
// uplink's src from the doctor. An agent that predates the IPv6
// facts names none: agent_old.
// ponytail: the AAAA record of the node's name is not consulted - the panel's resolvers (system, DNS over HTTPS) ask for A
// only, and a second lookup per node would burn the edge's subrequests; add it when an owner points an AAAA elsewhere.
func nodeIPv6(n countryNode) (addr, state string) {
	if n.ipv6.state != ipv6OK {
		return "", cmp.Or(n.ipv6.state, ipv6Unknown)
	}
	if ip, ok := usableIPv6(n.node.IPv6Entry); ok { // the address people connect to
		return ip, ipv6OK
	}
	if ip, ok := usableIPv6(n.node.Address); ok {
		return ip, ipv6OK
	}
	if n.ipv6.src != "" {
		return n.ipv6.src, ipv6OK
	}
	return "", ipv6AgentOld
}

// ---------------------------------------------------------------------------------------------------
// A round

func (s *Service) countryConfig(ctx context.Context) (CountryConfig, CountrySource, bool) {
	if s.cfg.Country == nil {
		return CountryConfig{}, nil, false
	}
	cc, src := s.cfg.Country(ctx)
	if !cc.Enabled || src == nil || cc.Country == "" {
		return cc, nil, false
	}
	return cc, src, true
}

func (s *Service) countryHold(ctx context.Context) time.Time {
	v, err := s.st.Setting(ctx, countryHoldSetting)
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// CountryTick runs a round when one is due (the configured interval, stretched by the budget, since the last one, and not
// before a rate-limit hold ends) and reports whether it ran. The VPS scheduler and the edge cron both call it; it is safe
// to call often and never runs two rounds at once. With the IPv6 checks on, IPv4 and IPv6 are rounds of their own, each due
// once an interval (the budget counts both): the IPv4 round goes first when both are due, so an IPv6 round never delays it
// by more than its own time, and the IPv6 one follows on the next call.
func (s *Service) CountryTick(ctx context.Context) bool {
	cc, src, ok := s.countryConfig(ctx)
	if !ok || !s.countryMu.TryLock() {
		return false
	}
	defer s.countryMu.Unlock()
	nodes, err := s.countryNodes(ctx)
	if err != nil {
		s.log.Warn("health: country check targets", "err", err)
		return false
	}
	if len(nodes) == 0 {
		return false
	}
	if cc.IPv6 {
		s.countryIPv6Facts(ctx, nodes)
	}
	probes, every, _ := countryPlan(nodes, cc.Interval, src.HourlyLimit())
	s.countryNoteStretch(cc.Interval, every)
	now := s.now()
	families := []int{familyV4} // in this order: IPv4 goes first when both are due
	if cc.IPv6 {
		families = append(families, familyV6)
	}
	family := 0
	for _, f := range families {
		last, err := s.st.LastCountryCheckAt(ctx, f)
		if err != nil {
			s.log.Warn("health: country check last round", "err", err)
			return false
		}
		if !now.Before(last.Add(every)) {
			family = f
			break
		}
	}
	if family == 0 {
		return false
	}
	if now.Before(s.countryHold(ctx)) {
		return false
	}
	// Not released: it lapses after the round's own deadline, and the next due round is minutes away.
	if s.cfg.RoundLease != nil && !s.cfg.RoundLease(ctx, "country-round", max(s.cfg.CountryRoundTimeout, countryRoundTime)) {
		return false
	}
	var wants []CountryWant // an IPv6 round pins no operators: the probe list does not say who has IPv6
	if family == familyV4 {
		wants = s.countryRoundWants(ctx, cc.Country)
	}
	s.countryRound(ctx, cc, src, nodes, probes, now, wants, family)
	return true
}

// countryNoteStretch logs once per stretch that the interval is longer than the owner chose.
func (s *Service) countryNoteStretch(chosen, effective time.Duration) {
	stretched := effective > chosen
	if stretched && !s.countryStretched {
		s.log.Warn("health: country check interval stretched to stay within the hourly limit of the source",
			"chosen_min", int(chosen/time.Minute), "effective_min", int(effective/time.Minute))
	}
	s.countryStretched = stretched
}

func (s *Service) countryRound(ctx context.Context, cc CountryConfig, src CountrySource, nodes []countryNode, probes int, now time.Time, wants []CountryWant, family int) {
	timeout := s.cfg.CountryRoundTimeout
	if timeout <= 0 {
		timeout = countryRoundTime
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	round := CountryRound{Country: cc.Country, Probes: probes, Want: wants, Family: family}
	round.ControlHost, round.ControlPort = countryControl(cc.Country)
	if family == familyV6 {
		round.ControlHost, round.ControlPort = countryControl6(cc.Country)
	}
	var rows []store.CountryCheck
	unknown := func(n countryNode, reason string, asked []string) {
		b, _ := json.Marshal(countryDetail{Reason: reason, Ports: n.ports, Asked: asked})
		rows = append(rows, store.CountryCheck{NodeID: n.node.ID, At: now, Country: cc.Country, Family: family, Verdict: verdictUnknown, Detail: string(b)})
	}
	targets := map[string]countryNode{}
	for _, n := range nodes {
		var ip, reason string // the address of the round's family, or why the node has none
		if family == familyV6 {
			var state string
			if ip, state = nodeIPv6(n); state != ipv6OK {
				reason = ipv6Reasons[state]
			}
		} else if addr, ok := s.countryAddress(rctx, n.node); ok {
			ip = addr
		} else {
			reason = "no_ipv4"
		}
		if ip == "" {
			unknown(n, reason, nil)
			continue
		}
		round.Targets = append(round.Targets, CountryTarget{Key: n.node.ID, IP: ip, Ports: n.ports})
		targets[n.node.ID] = n
	}
	if len(round.Targets) > 0 {
		obs, err := src.Measure(rctx, round)
		if errors.Is(err, ErrCountryStopped) {
			return
		}
		if err != nil {
			reason := s.countryFailure(ctx, src, err, now)
			for _, t := range round.Targets {
				unknown(targets[t.Key], reason, obs.Asked)
			}
		} else {
			reachedAny, reachedICMP := countryReached(obs, round.Targets, false), countryReached(obs, round.Targets, true)
			for _, t := range round.Targets {
				reached := reachedAny
				if len(t.Ports) == 0 { // an ICMP-only node: the probe must have pinged another node
					reached = reachedICMP
				}
				verdict, d := countryJudge(t, obs, reached)
				d.NoProbe = obs.NoProbe
				d.Asked = obs.Asked
				b, _ := json.Marshal(d)
				rows = append(rows, store.CountryCheck{NodeID: t.Key, At: now, Country: cc.Country, Family: family, Verdict: verdict, Detail: string(b)})
			}
		}
	}
	if err := s.st.InsertCountryChecks(ctx, rows); err != nil {
		s.log.Warn("health: store country check", "err", err)
	}
}

// countryFailure maps a failed round to the reason stored on its unknown rows, and starts the hold of a rate limit.
func (s *Service) countryFailure(ctx context.Context, src CountrySource, err error, now time.Time) string {
	var limit *CountryLimitError
	switch {
	case errors.As(err, &limit):
		until := now.Add(max(limit.Reset, time.Minute))
		if serr := s.st.SetSettings(ctx, map[string]string{countryHoldSetting: strconv.FormatInt(until.Unix(), 10)}); serr != nil {
			s.log.Warn("health: country check hold", "err", serr)
		}
		s.log.Info("health: country check rate limited", "source", src.Name(), "wait_s", int(limit.Reset.Seconds()))
		return "rate_limited"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrCountryNoProbes):
		return "no_probes"
	}
	s.log.Warn("health: country check source failed", "source", src.Name(), "err", err)
	return "source_error"
}

// ---------------------------------------------------------------------------------------------------
// Views

var countryVerdictProto = map[string]adminv1.CountryVerdict{
	verdictOK: adminv1.CountryVerdict_COUNTRY_VERDICT_OK, verdictPartial: adminv1.CountryVerdict_COUNTRY_VERDICT_PARTIAL,
	verdictTCPBlocked: adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED,
	verdictBlocked:    adminv1.CountryVerdict_COUNTRY_VERDICT_BLOCKED, verdictUnknown: adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN,
	verdictListed: adminv1.CountryVerdict_COUNTRY_VERDICT_LISTED,
}

// countryCheckMsg is the API form of a stored round.
func countryCheckMsg(r store.CountryCheck) *adminv1.CountryCheck {
	var d countryDetail
	_ = json.Unmarshal([]byte(r.Detail), &d)
	m := &adminv1.CountryCheck{NodeId: r.NodeID, Country: r.Country, Verdict: countryVerdictProto[r.Verdict], AtUnix: r.At.Unix(), Reason: d.Reason, Eyeball: d.Eyeball,
		NoProbe: d.NoProbe}
	for _, p := range d.Probes {
		pm := &adminv1.CountryProbe{Network: p.Network, Asn: p.ASN, City: p.City, IcmpOk: p.ICMPOK, IcmpMs: p.ICMPMs,
			Valid: p.Valid, Eyeball: p.Eyeball, Dark: p.Dark}
		for _, t := range p.TCP {
			pm.Tcp = append(pm.Tcp, &adminv1.PortResult{Port: uint32(t.Port), Ok: t.OK, Ms: t.MS})
		}
		m.Probes = append(m.Probes, pm)
	}
	return m
}

// globalpingLive is a stored Globalping round as a live source of the combined verdict; its vantages go onto m.
func globalpingLive(m *adminv1.CountryCheck, r store.CountryCheck) countryreach.Live {
	var d countryDetail
	_ = json.Unmarshal([]byte(r.Detail), &d)
	for _, v := range globalpingVantages(d) {
		m.Vantages = append(m.Vantages, countryVantageProto(countryreach.SourceGlobalping, r.At, v))
	}
	return countryreach.Live{Source: countryreach.SourceGlobalping, Verdict: r.Verdict, Reason: d.Reason, Eyeball: d.Eyeball, At: r.At}
}

// countryIPv6Msg is the view of a node over IPv6 (CountryCheck.ipv6): Globalping is the only source of that family, so the
// verdict is its own. A node without a round yet is unknown, not checked.
func countryIPv6Msg(nodeID, country string, r *store.CountryCheck, fresh bool) *adminv1.CountryCheck {
	if r == nil {
		return &adminv1.CountryCheck{NodeId: nodeID, Country: country, Verdict: adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN, Reason: "not_checked"}
	}
	m := countryCheckMsg(*r)
	m.GlobalpingVerdict = countryVerdictProto[r.Verdict]
	var live []countryreach.Live
	if fresh {
		live = append(live, globalpingLive(m, *r))
	}
	combineCountry(m, live, nil)
	return m
}

// countryIPv6Word is what the newest IPv6 round says of a node, for the IPv4 alert's ipv6 parameter: open (the node answers
// there), partial (some networks only), blocked (TCP or everything is cut), unknown (no round, an old one, or a node without
// IPv6; the reason is on CountryCheck.ipv6). It is the verdict countryIPv6Msg shows, from the same row.
func countryIPv6Word(r *store.CountryCheck, fresh bool) string {
	if r == nil || !fresh {
		return "unknown"
	}
	switch r.Verdict {
	case verdictOK:
		return "open"
	case verdictPartial:
		return verdictPartial
	case verdictTCPBlocked, verdictBlocked:
		return verdictBlocked
	}
	return "unknown"
}

// combineCountry sets the verdict and source list on m from every fresh live row and the optional block-list check.
func combineCountry(m *adminv1.CountryCheck, live []countryreach.Live, b *store.BlocklistCheck) {
	var list *countryreach.Listing
	if b != nil {
		list = &countryreach.Listing{CDNProvider: b.CDNProvider, CDNRegion: b.CDNRegion}
		if b.InRKN && b.ListedBy != "" {
			list.ListedBy = strings.Split(b.ListedBy, ",")
		}
	}
	c := countryreach.Combine(live, list)
	m.Verdict, m.Confidence, m.Sources, m.ListedBy, m.CdnProvider, m.CdnRegion = countryVerdictProto[c.Verdict], c.Confidence, c.Sources, c.ListedBy, c.CDNProvider, c.CDNRegion
	for _, v := range c.SourceVerdicts {
		m.SourceVerdicts = append(m.SourceVerdicts, &adminv1.SourceVerdict{Source: v.Source, Verdict: countryVerdictProto[v.Verdict], Reason: v.Reason,
			Eyeball: v.Eyeball, AtUnix: v.At.Unix()})
	}
}

func countryMaxAge(interval time.Duration, newest, previous time.Time) time.Duration {
	if interval <= 0 {
		interval = 15 * time.Minute // Globalping's default interval.
	}
	if !newest.IsZero() && !previous.IsZero() {
		interval = max(interval, min(newest.Sub(previous), countryMaxInterval))
	}
	return 3 * interval
}

func countryRoundMaxAges(interval time.Duration, rounds []store.CountryCheck) map[string]time.Duration {
	newest, previous := make(map[string]time.Time), make(map[string]time.Time)
	for _, r := range rounds { // RecentCountryChecks returns newest first within each node.
		if _, ok := newest[r.NodeID]; !ok {
			newest[r.NodeID] = r.At
		} else if _, ok := previous[r.NodeID]; !ok {
			previous[r.NodeID] = r.At
		}
	}
	ages := make(map[string]time.Duration, len(newest))
	for id, at := range newest {
		ages[id] = countryMaxAge(interval, at, previous[id])
	}
	return ages
}

func (s *Service) activeExternalStates(ctx context.Context) []ExternalState {
	if s.cfg.External == nil {
		return nil
	}
	states := s.cfg.External(ctx)
	active := make([]ExternalState, 0, len(states))
	for _, state := range states {
		if state.On && state.Source != "" && state.MaxAge > 0 {
			active = append(active, state)
		}
	}
	slices.SortStableFunc(active, func(a, b ExternalState) int { return countryreach.CompareSources(a.Source, b.Source) })
	return active
}

func externalSourcePath(source string) bool {
	return source == countryreach.SourceCheburcheck || source == countryreach.SourceCheburcheckOwn
}

func externalSourceEyeball(source string) bool {
	switch source {
	case countryreach.SourceCheburcheck, countryreach.SourceCheburcheckOwn, countryreach.SourceBsbord,
		countryreach.SourceBsbordTunnel, countryreach.SourceBsbordGeo:
		return true
	default:
		return false
	}
}

func countryVantageProto(source string, at time.Time, v countryreach.Vantage) *adminv1.CountryVantage {
	return &adminv1.CountryVantage{Source: source, Key: v.Key, Network: v.Network, Region: v.Region, Asn: v.ASN, Kind: v.Kind, State: v.State,
		AtUnix: at.Unix(), DpiHop: uint32(max(v.DPIHop, 0)), IcmpOk: v.ICMPOK, TcpOk: v.TCPOK}
}

func globalpingVantages(detail countryDetail) []countryreach.Vantage {
	entries := globalpingAlertVantages(detail)
	out := make([]countryreach.Vantage, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.vantage)
	}
	return out
}

type countryAlertVantage struct {
	vantage   countryreach.Vantage
	eyeball   bool
	crossNode bool
}

func globalpingAlertVantages(detail countryDetail) []countryAlertVantage {
	out := make([]countryAlertVantage, 0, len(detail.Probes))
	for i, p := range detail.Probes {
		state := countryreach.VantageStateUncertain
		tcpOK := false
		for _, port := range p.TCP {
			tcpOK = tcpOK || port.OK
		}
		reached := p.ICMPOK
		if len(detail.Ports) > 0 {
			reached = tcpOK
		}
		if p.Valid {
			switch {
			case p.Dark:
				state = countryreach.VantageStateDark
			case reached:
				state = countryreach.VantageStateReach
			}
		}
		key, asn := p.Network, ""
		if p.ASN != 0 {
			asn = strconv.FormatUint(uint64(p.ASN), 10)
			key = "AS" + asn
		}
		if key == "" {
			key = fmt.Sprintf("probe:%d", i+1)
		}
		out = append(out, countryAlertVantage{vantage: countryreach.Vantage{Key: key, Network: p.Network, Region: p.City, ASN: asn,
			Kind: countryreach.VantageKindUnknown, State: state, ICMPOK: p.ICMPOK, TCPOK: tcpOK}, eyeball: p.Eyeball, crossNode: p.Dark})
	}
	return out
}

func tspuVantage(r tspuRound, id, name string) countryreach.Vantage {
	state := countryreach.VantageStateUncertain
	if r.verdict == verdictOK {
		state = countryreach.VantageStateReach
	} else if r.verdict == verdictTCPBlocked || r.verdict == verdictBlocked {
		state = countryreach.VantageStateDark
	}
	tcpOK := false
	for _, p := range r.ports {
		tcpOK = tcpOK || p.Verdict == "open"
	}
	return countryreach.Vantage{Key: id, Network: name, Kind: countryreach.VantageKindDC, State: state,
		DPIHop: max(r.row.DPIHop, 0), TCPOK: tcpOK}
}

// NodeIPv4 is a node's IPv4 address as the country sources use it: the address itself, or for a name the first IPv4 the panel
// resolves it to (DNS over HTTPS on the Cloudflare edition). ok is false when it has none.
func (s *Service) NodeIPv4(ctx context.Context, n store.NodeRow) (netip.Addr, bool) {
	ip, ok := s.countryAddress(ctx, n)
	if !ok {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(ip)
	return a, err == nil
}

// countryChecks are the latest verdict of every node (or of one), for GetChecks: the live round of the country check, the probe
// node's run and what the block lists say, combined. Empty while all three sources are off; a source that is off shows nothing
// of its own.
func (s *Service) countryChecks(ctx context.Context, nodeID string) ([]*adminv1.CountryCheck, error) {
	cc, _, liveOn := s.countryConfig(ctx)
	listsOn := s.cfg.Lists != nil && s.cfg.Lists(ctx)
	tspu := s.tspuState(ctx)
	activeExternal := s.activeExternalStates(ctx)
	if !liveOn && !listsOn && !tspu.On && len(activeExternal) == 0 {
		return nil, nil
	}
	now := s.now()
	var ids []string
	if nodeID != "" {
		ids = []string{nodeID}
	}
	byRound := map[string]store.CountryCheck{}
	roundFresh := map[string]bool{}
	byRound6 := map[string]store.CountryCheck{}
	roundFresh6 := map[string]bool{}
	byList := map[string]store.BlocklistCheck{}
	byTSPU := map[string]tspuRound{}
	byExt := map[string]map[string]store.ExtCheck{}
	if liveOn {
		rounds, err := s.st.RecentCountryChecks(ctx, familyV4, 2, ids...)
		if err != nil {
			return nil, err
		}
		maxAge := countryRoundMaxAges(cc.Interval, rounds)
		for _, r := range rounds {
			if _, ok := byRound[r.NodeID]; !ok {
				byRound[r.NodeID] = r
				roundFresh[r.NodeID] = now.Sub(r.At) <= maxAge[r.NodeID]
			}
		}
		if cc.IPv6 {
			rounds6, err := s.st.RecentCountryChecks(ctx, familyV6, 2, ids...)
			if err != nil {
				return nil, err
			}
			maxAge6 := countryRoundMaxAges(cc.Interval, rounds6)
			for _, r := range rounds6 {
				if _, ok := byRound6[r.NodeID]; !ok {
					byRound6[r.NodeID] = r
					roundFresh6[r.NodeID] = now.Sub(r.At) <= maxAge6[r.NodeID]
				}
			}
		}
	}
	if listsOn {
		lists, err := s.st.RecentBlocklistChecks(ctx, ids...)
		if err != nil {
			return nil, err
		}
		for _, b := range lists {
			byList[b.NodeID] = b
		}
	}
	vantageName := ""
	if tspu.On {
		rows, err := s.st.TSPUHistory(ctx, tspu.Vantage, 1, ids...)
		if err != nil {
			return nil, err
		}
		for _, r := range tspuRounds(rows) {
			if s.now().Sub(r.row.At) <= tspu.MaxAge { // an old run (the probe node was off) says nothing now
				byTSPU[r.row.NodeID] = r
			}
		}
		vantageName = s.nodeName(ctx, tspu.Vantage)
	}
	if len(activeExternal) > 0 {
		sources := make([]string, len(activeExternal))
		maxAge := make(map[string]time.Duration, len(activeExternal))
		for i, state := range activeExternal {
			sources[i], maxAge[state.Source] = state.Source, state.MaxAge
		}
		rows, err := s.st.RecentExtChecks(ctx, 1, sources...)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if (nodeID != "" && r.NodeID != nodeID) || now.Sub(r.At) > maxAge[r.Source] {
				continue
			}
			if byExt[r.NodeID] == nil {
				byExt[r.NodeID] = make(map[string]store.ExtCheck)
			}
			byExt[r.NodeID][r.Source] = r
		}
	}
	// every node that any source has something on
	known := map[string]bool{}
	for id := range byRound {
		known[id] = true
	}
	for id := range byTSPU {
		known[id] = true
	}
	for id := range byList {
		known[id] = true
	}
	for id := range byExt {
		known[id] = true
	}
	order := slices.Sorted(maps.Keys(known))
	out := make([]*adminv1.CountryCheck, 0, len(order))
	for _, id := range order {
		m := &adminv1.CountryCheck{NodeId: id, Country: countryreach.BlocklistCountry}
		var live []countryreach.Live
		var at time.Time
		if r, ok := byRound[id]; ok {
			m = countryCheckMsg(r)
			m.GlobalpingVerdict = countryVerdictProto[r.Verdict]
			at = r.At
			if roundFresh[id] {
				live = append(live, globalpingLive(m, r))
			}
		}
		if r, ok := byTSPU[id]; ok {
			live = append(live, countryreach.Live{Source: countryreach.SourceTSPU, Verdict: r.verdict, Reason: tspu.Run.GetReason(), Path: true, At: r.row.At})
			m.TspuVerdict, m.DpiHop = countryVerdictProto[r.verdict], uint32(max(r.row.DPIHop, 0))
			m.TspuVantageId, m.TspuVantageName, m.TspuAtUnix = tspu.Vantage, vantageName, r.row.At.Unix()
			for _, p := range r.ports {
				m.TspuPorts = append(m.TspuPorts, &adminv1.TspuPort{Port: uint32(p.Port), Verdict: p.Verdict})
			}
			m.Vantages = append(m.Vantages, countryVantageProto(countryreach.SourceTSPU, r.row.At, tspuVantage(r, tspu.Vantage, vantageName)))
			if r.row.At.After(at) {
				at = r.row.At
			}
		}
		for _, state := range activeExternal {
			r, ok := byExt[id][state.Source]
			if !ok {
				continue
			}
			var detail countryreach.ExtDetail
			_ = json.Unmarshal([]byte(r.DetailJSON), &detail)
			live = append(live, countryreach.Live{Source: r.Source, Verdict: r.Verdict, Reason: detail.Reason, Eyeball: externalSourceEyeball(r.Source),
				Path: externalSourcePath(r.Source), At: r.At})
			for _, v := range detail.Vantages {
				m.Vantages = append(m.Vantages, countryVantageProto(r.Source, r.At, v))
			}
			if r.At.After(at) {
				at = r.At
			}
		}
		var b *store.BlocklistCheck
		if l, ok := byList[id]; ok {
			b = &l
			if l.At.After(at) {
				at = l.At
			}
		}
		if m.AtUnix == 0 {
			m.AtUnix = at.Unix()
		}
		combineCountry(m, live, b)
		if liveOn && cc.IPv6 {
			var r6 *store.CountryCheck
			if r, ok := byRound6[id]; ok {
				r6 = &r
			}
			m.Ipv6 = countryIPv6Msg(id, cc.Country, r6, roundFresh6[id])
		}
		out = append(out, m)
	}
	return out, nil
}

// nodeName is a node's name, its id when the node is not known.
func (s *Service) nodeName(ctx context.Context, id string) string {
	if sn, err := s.snapshot(ctx); err == nil {
		for _, n := range sn.nodes {
			if n.ID == id {
				return n.Name
			}
		}
	}
	return id
}

// ---------------------------------------------------------------------------------------------------
// The alert

func countryBad(verdict string) bool {
	return verdict == verdictTCPBlocked || verdict == verdictBlocked
}

// addCountryConds derives COUNTRY_BLOCKED from stored rounds of every enabled source. A full alert opens after two counted events
// whose own and combined verdicts both indicate a block, and closes after two whose own and combined verdicts indicate reach.
// Partial observations are keyed by source and network. Unknown rows shadow older source state but do not create events; a
// disagreement is neutral and holds the full alert. Each source event combines with the newest fresh row from every other source
// (countryEvents). A block list never counts. While a node is disconnected both alerts are held; with all sources off they are
// let go. The IPv6 rounds (Globalping alone) do the same for a node that has the IPv6 entry (ipv6Entry), under the
// keys "ipv6" and "ipv6/partial"; for every other node they only put the ipv6 parameter on its IPv4 alert (countryIPv6Word),
// which refreshes the alert without announcing it again (reconcile: TouchAlert).
func (s *Service) addCountryConds(ctx context.Context, sn *snapshot, live map[string]store.NodeLiveRow, activeAlerts map[key]store.HealthAlert, d *derived, add func(cond)) {
	cc, _, liveOn := s.countryConfig(ctx)
	tspu := s.tspuState(ctx)
	external := s.activeExternalStates(ctx)
	if !liveOn && !tspu.On && len(external) == 0 {
		return
	}
	hold := func() {
		for k := range activeAlerts {
			if k.kind == kCountryBlocked {
				d.holdKeys[k] = true
			}
		}
	}

	byNode := map[string][]store.CountryCheck{}
	if liveOn {
		rows, err := s.st.RecentCountryChecks(ctx, familyV4, countryRecent)
		if err != nil {
			s.log.Warn("health: country check rounds", "err", err)
			hold()
			return
		}
		for _, r := range rows {
			byNode[r.NodeID] = append(byNode[r.NodeID], r)
		}
	}
	// The IPv6 rounds (Globalping alone). A read that fails costs only the IPv6 side: the IPv6 alerts are held and the IPv4
	// alerts carry no IPv6 verdict this time.
	ipv6On := liveOn && cc.IPv6
	byNode6 := map[string][]store.CountryCheck{}
	v6Failed := false
	var rows6 []store.CountryCheck
	if ipv6On {
		var err error
		if rows6, err = s.st.RecentCountryChecks(ctx, familyV6, countryRecent); err != nil {
			s.log.Warn("health: country check IPv6 rounds", "err", err)
			v6Failed = true
		}
		for _, r := range rows6 {
			byNode6[r.NodeID] = append(byNode6[r.NodeID], r)
		}
	}
	tspuByNode := map[string][]tspuRound{}
	if tspu.On {
		rows, err := s.st.TSPUHistory(ctx, tspu.Vantage, countryRecent)
		if err != nil {
			s.log.Warn("health: country check TSPU rounds", "err", err)
			hold()
			return
		}
		for _, r := range tspuRounds(rows) {
			tspuByNode[r.row.NodeID] = append(tspuByNode[r.row.NodeID], r)
		}
	}
	tspuName := tspu.Vantage
	for _, n := range sn.nodes {
		if n.ID == tspu.Vantage {
			tspuName = n.Name
		}
	}
	externalBySource := make(map[string]time.Duration, len(external))
	var externalRows []store.ExtCheck
	if len(external) > 0 {
		sources := make([]string, len(external))
		for i, state := range external {
			sources[i] = state.Source
			externalBySource[state.Source] = state.MaxAge
		}
		rows, err := s.st.RecentExtChecks(ctx, countryRecent, sources...)
		if err != nil {
			s.log.Warn("health: external country check rounds", "err", err)
			hold()
			return
		}
		externalRows = rows
	}

	var signals []countryAlertSignal
	for nodeID, rounds := range byNode {
		ages := countryRoundMaxAges(cc.Interval, rounds)
		country := rounds[0].Country
		for _, r := range rounds {
			if r.Country != country { // a country change starts the Globalping history over
				break
			}
			signals = append(signals, countryGlobalpingSignal(r, ages[nodeID]))
		}
	}
	for _, runs := range tspuByNode {
		for _, r := range runs {
			signals = append(signals, countryTSPUSignal(r, tspu.MaxAge, tspuName))
		}
	}
	for _, r := range externalRows {
		signals = append(signals, countryExternalSignal(r, externalBySource[r.Source]))
	}

	ages6 := countryRoundMaxAges(cc.Interval, rows6)
	now := s.now()
	for _, n := range sn.nodes {
		var ipv6Word string // what the IPv6 rounds say of a node that has no IPv6 alert of its own: a parameter of its IPv4 alert
		entry := ipv6On && ipv6Entry(n)
		if ipv6On {
			word := "unknown"
			if rounds := byNode6[n.ID]; len(rounds) > 0 && !v6Failed {
				r := rounds[0]
				word = countryIPv6Word(&r, r.Country == cc.Country && now.Sub(r.At) <= ages6[n.ID])
			}
			ipv6Word = word
			if entry {
				// the IPv6 alert speaks for IPv6 itself; the IPv4 one only says where people with IPv6 connect
				if ipv6Word = ""; word == "open" {
					ipv6Word = "entry"
				}
			}
		}
		s.countryNodeConds(n, live[n.ID].Connected, signals, activeAlerts, d, add, countryFamilyAlerts{family: familyV4, ipv6: ipv6Word})
		if ipv6On {
			// An IPv6 alert exists only for a node whose IPv6 entry is on: the people on it depend on it. Without
			// the entry the keys are let go, as those of a source that is off.
			var signals6 []countryAlertSignal
			if entry {
				rounds := byNode6[n.ID]
				for _, r := range rounds {
					if r.Country != rounds[0].Country { // a country change starts the history over
						break
					}
					signals6 = append(signals6, countryGlobalpingSignal(r, ages6[n.ID]))
				}
			}
			s.countryNodeConds(n, live[n.ID].Connected, signals6, activeAlerts, d, add, countryFamilyAlerts{family: familyV6, off: !entry, hold: v6Failed})
		}
	}
}

// ipv6Entry says whether a node's IPv6 entry is on, which is what gives its IPv6 country alert a reason to exist.
func ipv6Entry(n store.NodeRow) bool {
	_, ok := usableIPv6(n.IPv6Entry)
	return ok
}

// countryFamilyAlerts is one address family's side of the COUNTRY_BLOCKED alerts of a node.
type countryFamilyAlerts struct {
	family int
	ipv6   string // familyV4: the IPv6 verdict word for the alert's parameter, "" for none
	off    bool   // familyV6: the node has no IPv6 alert to keep (its keys are let go)
	hold   bool   // familyV6: the rounds could not be read (its keys are held)
}

// subjects are the alert subjects of the family: the IPv4 ones are the original "" and "partial", so alerts that are open
// when this ships stay the same alerts; the IPv6 ones are "ipv6" and "ipv6/partial" and the alert carries family=6.
func (f countryFamilyAlerts) subjects() (full, partial string) {
	if f.family == familyV6 {
		return "ipv6", "ipv6/partial"
	}
	return "", "partial"
}

func (f countryFamilyAlerts) decorate(c cond) cond {
	params := maps.Clone(c.params)
	switch {
	case f.family == familyV6:
		params["family"] = "6"
	case f.ipv6 != "":
		params["ipv6"] = f.ipv6
	}
	c.params = params
	c.key.subject = f.subject(c.key.subject)
	return c
}

// subject maps an IPv4 subject to the family's.
func (f countryFamilyAlerts) subject(v4 string) string {
	full, partial := f.subjects()
	if v4 == "partial" {
		return partial
	}
	return full
}

// countryNodeConds is the COUNTRY_BLOCKED logic of one node and one address family over the signals of that family (see
// addCountryConds). Nothing of it depends on the family but the keys, the signals and the parameters.
func (s *Service) countryNodeConds(n store.NodeRow, connected bool, signals []countryAlertSignal, activeAlerts map[key]store.HealthAlert, d *derived, add func(cond), f countryFamilyAlerts) {
	full, partial := f.subjects()
	k := key{kCountryBlocked, n.ID, full}
	partialKey := key{kCountryBlocked, n.ID, partial}
	if f.off {
		return
	}
	if n.State != "active" || !connected || f.hold {
		d.holdKeys[k] = true
		d.holdKeys[partialKey] = true
		return
	}
	events := countryEvents(signals, n.ID)
	counted := countryCountedEvents(events)
	seen := counted[:min(len(counted), countryAlertRounds)]
	if len(seen) == countryAlertRounds && seen[0].bad() && seen[1].bad() {
		add(f.decorate(countryCond(n, seen[0])))
		d.superseded[partialKey] = true
		return
	}
	if len(seen) != countryAlertRounds || !seen[0].clear() || !seen[1].clear() {
		d.holdKeys[k] = true
		if _, ok := activeAlerts[k]; ok { // the full alert is held open: it still explains the node
			d.superseded[partialKey] = true
			return
		}
	}
	if alert, ok := activeAlerts[partialKey]; ok {
		if countryPartialHeld(signals, n.ID, alert.Params["sources"]) {
			d.holdKeys[partialKey] = true
		}
	}
	if c, ok := countryPartialCond(n, events, signals); ok {
		add(f.decorate(c))
	}
}

// countryClear: an event that says reach clears a full-block alert; partial is a clear result for that alert.
func countryClear(v string) bool { return v == verdictOK || v == verdictPartial }

// tspuRound is a stored row of the probe node with its verdict on the shared scale and its ports.
type tspuRound struct {
	row     store.TSPUCheck
	verdict string
	ports   []countryreach.TSPUPort
}

func tspuRounds(rows []store.TSPUCheck) []tspuRound {
	out := make([]tspuRound, len(rows))
	for i, r := range rows {
		ports := countryreach.ParseTSPUDetail(r.DetailJSON)
		out[i] = tspuRound{row: r, verdict: countryreach.TSPUVerdict(r.Verdict, ports), ports: ports}
	}
	return out
}

type countryAlertSignal struct {
	nodeID, source, verdict, reason, country string
	at                                       time.Time
	maxAge, interval                         time.Duration
	eyeball, path                            bool
	vantages                                 []countryAlertVantage
	ports                                    []uint16
}

type countryEvent struct {
	at       time.Time
	signal   countryAlertSignal
	active   []countryAlertSignal
	combined countryreach.Combined
}

func countryGlobalpingSignal(r store.CountryCheck, maxAge time.Duration) countryAlertSignal {
	var detail countryDetail
	_ = json.Unmarshal([]byte(r.Detail), &detail)
	vantages := globalpingAlertVantages(detail)
	if countryBad(r.Verdict) {
		for i, probe := range detail.Probes {
			if i < len(vantages) && probe.Valid {
				// A full source row says every valid probe missed its node carrier. Keep this for alert network names; crossNode
				// still comes only from countryJudge's stored dark flag below.
				vantages[i].vantage.State = countryreach.VantageStateDark
			}
		}
	}
	return countryAlertSignal{nodeID: r.NodeID, source: countryreach.SourceGlobalping, verdict: r.Verdict, reason: detail.Reason,
		country: r.Country, at: r.At, maxAge: maxAge, interval: maxAge / 3, eyeball: detail.Eyeball,
		vantages: vantages, ports: detail.Ports}
}

func countryTSPUSignal(r tspuRound, maxAge time.Duration, name string) countryAlertSignal {
	v := tspuVantage(r, r.row.Vantage, name)
	return countryAlertSignal{nodeID: r.row.NodeID, source: countryreach.SourceTSPU, verdict: r.verdict, reason: "",
		country: countryreach.TSPUCountry, at: r.row.At, maxAge: maxAge, interval: maxAge / 3, path: true,
		vantages: []countryAlertVantage{{vantage: v}}, ports: blockedTSPUPorts(r.ports)}
}

func countryExternalSignal(r store.ExtCheck, maxAge time.Duration) countryAlertSignal {
	var detail countryreach.ExtDetail
	_ = json.Unmarshal([]byte(r.DetailJSON), &detail)
	vantages := make([]countryAlertVantage, 0, len(detail.Vantages))
	for _, v := range detail.Vantages {
		vantages = append(vantages, countryAlertVantage{vantage: v, eyeball: externalSourceEyeball(r.Source)})
	}
	return countryAlertSignal{nodeID: r.NodeID, source: r.Source, verdict: r.Verdict, reason: detail.Reason,
		country: countryreach.TSPUCountry, at: r.At, maxAge: maxAge, interval: maxAge / 3,
		eyeball: externalSourceEyeball(r.Source), path: externalSourcePath(r.Source), vantages: vantages}
}

func blockedTSPUPorts(ports []countryreach.TSPUPort) []uint16 {
	var out []uint16
	for _, p := range ports {
		if p.Verdict == "blocked" {
			out = append(out, p.Port)
		}
	}
	return out
}

// countryEvents gives every non-unknown source row one event, newest first, combined with the newest fresh row from every
// other source at that time. Unknown rows still shadow older rows but do not create events. Equal-time measurements from
// different sources remain distinct events so one source cannot silently replace another.
func countryEvents(signals []countryAlertSignal, nodeID string) []countryEvent {
	bySource := map[string][]countryAlertSignal{}
	var own []countryAlertSignal
	for _, signal := range signals {
		if signal.nodeID != nodeID {
			continue
		}
		bySource[signal.source] = append(bySource[signal.source], signal)
	}
	for source := range bySource {
		slices.SortStableFunc(bySource[source], func(a, b countryAlertSignal) int { return b.at.Compare(a.at) })
	}
	if rounds := bySource[countryreach.SourceGlobalping]; len(rounds) > 0 {
		country := rounds[0].country
		// Rebuild the per-source rows as well: a country change starts only the Globalping history over.
		bySource[countryreach.SourceGlobalping] = slices.DeleteFunc(rounds, func(row countryAlertSignal) bool { return row.country != country })
	}
	for _, rows := range bySource {
		for _, row := range rows {
			if row.verdict != verdictUnknown {
				own = append(own, row)
			}
		}
	}
	slices.SortStableFunc(own, func(a, b countryAlertSignal) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return countryreach.CompareSources(a.source, b.source)
	})

	out := make([]countryEvent, 0, len(own))
	for _, signal := range own {
		active := make([]countryAlertSignal, 0, len(bySource))
		for _, rows := range bySource {
			for _, row := range rows {
				if row.at.After(signal.at) {
					continue
				}
				if signal.at.Sub(row.at) <= min(row.maxAge, signal.maxAge) {
					active = append(active, row)
				}
				break
			}
		}
		live := make([]countryreach.Live, 0, len(active))
		for _, row := range active {
			live = append(live, countryreach.Live{Source: row.source, Verdict: row.verdict, Reason: row.reason, Eyeball: row.eyeball,
				Path: row.path, At: row.at})
		}
		out = append(out, countryEvent{at: signal.at, signal: signal, active: active, combined: countryAlertCombine(live)})
	}
	return out
}

// The shared list Combine supplies the verdict. A full result is demoted to partial for alerts unless a country-wide source
// says it: Globalping, public cheburcheck or the TSPU probe. The owner's cheburcheck probe and bsbord select regions or single
// networks, so even several of them agreeing raise only the partial alert, naming their networks.
func countryAlertCombine(live []countryreach.Live) countryreach.Combined {
	c := countryreach.Combine(live, nil)
	if countryBad(c.Verdict) && !countryAlertHasBroadBlock(live) {
		c.Verdict = verdictPartial
	}
	return c
}

func countryAlertHasBroadBlock(live []countryreach.Live) bool {
	for _, row := range live {
		if !countryBad(row.Verdict) {
			continue
		}
		switch row.Source {
		case countryreach.SourceTSPU, countryreach.SourceCheburcheck, countryreach.SourceGlobalping:
			return true
		}
	}
	return false
}

func (e countryEvent) bad() bool {
	return countryBad(e.signal.verdict) && countryBad(e.combined.Verdict)
}

func (e countryEvent) clear() bool {
	return countryClear(e.signal.verdict) && countryClear(e.combined.Verdict)
}

func countryCountedEvents(events []countryEvent) []countryEvent {
	out := make([]countryEvent, 0, len(events))
	for _, event := range events {
		if event.bad() || event.clear() { // a source event that disagrees with the combined direction is neutral
			out = append(out, event)
		}
	}
	return out
}

type countryObservationKind uint8

const (
	countryObservationReach countryObservationKind = iota + 1
	countryObservationDark
)

type countryObservation struct {
	kind    countryObservationKind
	source  string
	vantage countryreach.Vantage
}

// countryObservations records one R or D for each source|vantage key and row. D is valid only when the event combines to
// partial and that same vantage reached another fleet node. A full block, whitelist unit, or source outage is not a D/R.
func countryObservations(events []countryEvent, signals []countryAlertSignal) map[string][]countryObservation {
	observations := map[string][]countryObservation{}
	for _, event := range events {
		byKey := map[string]countryObservation{}
		for _, item := range event.signal.vantages {
			v := item.vantage
			if v.State == countryreach.VantageStateWhitelist || v.State == countryreach.VantageStateUncertain ||
				(event.signal.source == countryreach.SourceGlobalping && !item.eyeball) {
				continue
			}
			key := countryObservationKey(event.signal.source, v.Key, v.Network)
			var observation countryObservation
			switch v.State {
			case countryreach.VantageStateReach:
				observation = countryObservation{kind: countryObservationReach, source: event.signal.source, vantage: v}
			case countryreach.VantageStateDark:
				if event.combined.Verdict != verdictPartial || !countryVantageReachedAnother(event.signal, item, signals) {
					continue
				}
				observation = countryObservation{kind: countryObservationDark, source: event.signal.source, vantage: v}
			default:
				continue
			}
			if previous, ok := byKey[key]; !ok || observation.kind == countryObservationReach {
				byKey[key] = observation
			} else if previous.kind == countryObservationReach {
				byKey[key] = previous
			}
		}
		for key, observation := range byKey {
			observations[key] = append(observations[key], observation)
		}
	}
	return observations
}

func countryObservationKey(source, key, network string) string {
	if key == "" {
		key = network
	}
	if key == "" {
		return ""
	}
	return source + "|" + key
}

func countryVantageReachedAnother(signal countryAlertSignal, item countryAlertVantage, signals []countryAlertSignal) bool {
	vantage := item.vantage
	if vantage.Key == "" && signal.source != countryreach.SourceGlobalping {
		return false // without a key no other node's row can be matched to this vantage
	}
	if signal.source == countryreach.SourceGlobalping {
		// countryJudge sets this state only for a complete eyeball probe that missed this node and reached another target.
		return item.crossNode
	}
	for _, other := range signals {
		if other.nodeID == signal.nodeID || other.source != signal.source || other.verdict == verdictUnknown {
			continue
		}
		delta := signal.at.Sub(other.at)
		switch signal.source {
		case countryreach.SourceTSPU, countryreach.SourceBsbord, countryreach.SourceBsbordTunnel, countryreach.SourceBsbordGeo:
			if delta != 0 {
				continue
			}
		case countryreach.SourceCheburcheck, countryreach.SourceCheburcheckOwn:
			if delta < 0 {
				delta = -delta
			}
			if signal.interval <= 0 || delta > signal.interval {
				continue
			}
		default:
			continue
		}
		for _, candidate := range other.vantages {
			if candidate.vantage.Key == vantage.Key && candidate.vantage.State == countryreach.VantageStateReach {
				return true
			}
		}
	}
	return false
}

func countryPartialHeld(signals []countryAlertSignal, nodeID, sourceList string) bool {
	newest := map[string]countryAlertSignal{}
	for _, signal := range signals {
		if signal.nodeID != nodeID {
			continue
		}
		if old, ok := newest[signal.source]; !ok || signal.at.After(old.at) {
			newest[signal.source] = signal
		}
	}
	if sourceList == "" { // legacy partial alerts predate the source list
		sourceList = countryreach.SourceGlobalping
	}
	for _, source := range strings.Split(sourceList, ",") {
		if signal, ok := newest[source]; ok && signal.verdict == verdictUnknown {
			return true // an outage in a source that raised the alert does not clear its retained D,D history
		}
	}
	return false
}

func countryVantageName(source string, v countryreach.Vantage) string {
	name := v.Network
	if name == "" {
		name = v.Key
	}
	var details []string
	if v.ASN != "" {
		details = append(details, "AS"+v.ASN)
	}
	if v.Region != "" {
		details = append(details, v.Region)
	}
	if source != countryreach.SourceGlobalping {
		details = append(details, source)
	}
	if name == "" {
		return strings.Join(details, " ")
	}
	if len(details) > 0 {
		return name + " (" + strings.Join(details, ", ") + ")"
	}
	return name
}

func countryEventVantageName(source string, v countryreach.Vantage) string {
	if source == countryreach.SourceGlobalping {
		return v.Network // keep Globalping-only full alerts' existing network labels
	}
	return countryVantageName(source, v)
}

// countryPartialCond opens when a source|vantage key has two newest D observations. Skipped and unknown rows are not
// observations, so a rotation or outage cannot erase its evidence. The newest D supplies the displayed network name.
func countryPartialCond(n store.NodeRow, events []countryEvent, signals []countryAlertSignal) (cond, bool) {
	observations := countryObservations(events, signals)
	var networks []string
	sources := map[string]bool{}
	country := countryreach.TSPUCountry
	confidence := countryreach.ConfidenceDCOnly
	for _, event := range events {
		if event.signal.country != "" {
			country = event.signal.country
			break
		}
	}
	for _, seen := range observations {
		if len(seen) < countryAlertRounds || seen[0].kind != countryObservationDark || seen[1].kind != countryObservationDark {
			continue
		}
		latest := seen[0]
		networks = append(networks, countryVantageName(latest.source, latest.vantage))
		sources[latest.source] = true
		if latest.source != countryreach.SourceTSPU {
			confidence = countryreach.ConfidenceEyeball
		}
	}
	if len(networks) == 0 {
		return cond{}, false
	}
	sort.Strings(networks)
	orderedSources := slices.SortedFunc(maps.Keys(sources), countryreach.CompareSources)
	return cond{key: key{kCountryBlocked, n.ID, "partial"}, severity: sevWarning,
		why: "health.alert.country_blocked.why.partial",
		params: map[string]string{"node": n.Name, "country": country, "verdict": verdictPartial,
			"confidence": confidence, "sources": strings.Join(orderedSources, ","), "networks": strings.Join(networks, ", ")}}, true
}

// countryCond builds a full alert from the newest bad event. Its network list includes the dark vantages from every live
// source in that event, with an external source suffix so equal operator names cannot be confused.
func countryCond(n store.NodeRow, e countryEvent) cond {
	c := e.combined
	country := countryreach.TSPUCountry
	var networks []string
	var ports []uint16
	for _, signal := range e.active {
		if signal.country != "" {
			country = signal.country
		}
		if !slices.Contains(c.Sources, signal.source) {
			continue
		}
		for _, v := range signal.vantages {
			if v.vantage.State == countryreach.VantageStateDark && signal.source != countryreach.SourceTSPU {
				name := countryEventVantageName(signal.source, v.vantage)
				if !slices.Contains(networks, name) {
					networks = append(networks, name)
				}
			}
		}
		ports = append(ports, signal.ports...)
	}
	sort.Strings(networks)
	slices.Sort(ports)
	ports = slices.Compact(ports)
	portStrs := make([]string, len(ports))
	for i, p := range ports {
		portStrs[i] = strconv.Itoa(int(p))
	}
	return cond{key: key{kCountryBlocked, n.ID, ""}, severity: sevWarning, why: "health.alert.country_blocked.why." + c.Verdict,
		params: map[string]string{"node": n.Name, "country": country, "verdict": c.Verdict, "confidence": c.Confidence,
			"sources": strings.Join(c.Sources, ","), "networks": strings.Join(networks, ", "), "ports": strings.Join(portStrs, ", ")}}
}

// ReconcileCountry brings the COUNTRY_BLOCKED alerts up to date without the full evaluator, which the Cloudflare edition
// does not run on the admin isolate. The edge calls it when the integration is switched off (globalping.Config.OnChange);
// the cron tick's evaluator (EvaluateOnce) derives these alerts itself. Alerts of other kinds are left alone.
func (s *Service) ReconcileCountry(ctx context.Context) {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()
	now := s.now()
	all, err := s.st.ActiveAlerts(ctx)
	if err != nil {
		s.log.Warn("health: country alerts", "err", err)
		return
	}
	sn, err := s.snapshot(ctx)
	if err != nil {
		s.log.Warn("health: country alerts", "err", err)
		return
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		s.log.Warn("health: country alerts", "err", err)
		return
	}
	var active []store.HealthAlert
	activeAlerts := map[key]store.HealthAlert{}
	for _, a := range all {
		if a.Kind == kCountryBlocked {
			active = append(active, a)
			k := key{a.Kind, a.NodeID, a.Subject}
			activeAlerts[k] = a
		}
	}
	d := &derived{conds: map[key]cond{}, nodes: map[string]bool{}, holdKeys: map[key]bool{}, superseded: map[key]bool{}}
	for _, n := range sn.nodes {
		d.nodes[n.ID] = true
	}
	s.addCountryConds(ctx, sn, indexLiveRows(liveRows), activeAlerts, d, func(c cond) { d.conds[c.key] = c })
	if err := s.reconcile(ctx, now, d, active); err != nil {
		s.log.Warn("health: reconcile country alerts", "err", err)
	}
}
