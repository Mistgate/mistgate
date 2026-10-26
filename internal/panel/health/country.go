package health

import (
	"context"
	"encoding/json"
	"errors"
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

// ---------------------------------------------------------------------------------------------------
// Budget

// countryNode is an online node that takes part in a round.
type countryNode struct {
	node  store.NodeRow
	ports []uint16
}

// countryCost is the tests of one round with k probes: the control, then per node one ICMP and one per TCP port, each run by
// every probe.
func countryCost(nodes []countryNode, k int) int {
	n := 1
	for _, c := range nodes {
		n += 1 + len(c.ports)
	}
	return n * k
}

// countryPlan chooses the probes per measurement and the interval so that the rounds of an hour stay within 80 % of the
// source's limit: 3 probes if that fits, else 2, else 2 and a longer interval. limit 0 = unlimited.
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
		out = append(out, countryNode{node: n, ports: countryPorts(sn.byNode[n.ID])})
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
// to call often and never runs two rounds at once.
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
	probes, every, _ := countryPlan(nodes, cc.Interval, src.HourlyLimit())
	s.countryNoteStretch(cc.Interval, every)
	now := s.now()
	last, err := s.st.LastCountryCheckAt(ctx)
	if err != nil {
		s.log.Warn("health: country check last round", "err", err)
		return false
	}
	if now.Before(last.Add(every)) || now.Before(s.countryHold(ctx)) {
		return false
	}
	// Not released: it lapses after the round's own deadline, and the next due round is minutes away.
	if s.cfg.RoundLease != nil && !s.cfg.RoundLease(ctx, "country-round", max(s.cfg.CountryRoundTimeout, countryRoundTime)) {
		return false
	}
	wants := s.countryRoundWants(ctx, cc.Country)
	s.countryRound(ctx, cc, src, nodes, probes, now, wants)
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

func (s *Service) countryRound(ctx context.Context, cc CountryConfig, src CountrySource, nodes []countryNode, probes int, now time.Time, wants []CountryWant) {
	timeout := s.cfg.CountryRoundTimeout
	if timeout <= 0 {
		timeout = countryRoundTime
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	round := CountryRound{Country: cc.Country, Probes: probes, Want: wants}
	round.ControlHost, round.ControlPort = countryControl(cc.Country)
	var rows []store.CountryCheck
	unknown := func(n countryNode, reason string, asked []string) {
		b, _ := json.Marshal(countryDetail{Reason: reason, Ports: n.ports, Asked: asked})
		rows = append(rows, store.CountryCheck{NodeID: n.node.ID, At: now, Country: cc.Country, Verdict: verdictUnknown, Detail: string(b)})
	}
	targets := map[string]countryNode{}
	for _, n := range nodes {
		ip, ok := s.countryAddress(rctx, n.node)
		if !ok {
			unknown(n, "no_ipv4", nil)
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
				rows = append(rows, store.CountryCheck{NodeID: t.Key, At: now, Country: cc.Country, Verdict: verdict, Detail: string(b)})
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

// combineCountry sets the verdict a node shows on m: its live round (nil: none), the probe node's verdict (nil: none, too old or
// the source is off) and what the block lists say (nil: off or not checked), merged by countryreach.Combine. The probes and the
// reason stay those of the live round.
func combineCountry(m *adminv1.CountryCheck, live *countryreach.Live, tspu *countryreach.TSPU, b *store.BlocklistCheck) {
	var list *countryreach.Listing
	if b != nil {
		list = &countryreach.Listing{CDNProvider: b.CDNProvider, CDNRegion: b.CDNRegion}
		if b.InRKN && b.ListedBy != "" {
			list.ListedBy = strings.Split(b.ListedBy, ",")
		}
	}
	c := countryreach.Combine(live, tspu, list)
	m.Verdict, m.Confidence, m.Sources, m.ListedBy, m.CdnProvider, m.CdnRegion = countryVerdictProto[c.Verdict], c.Confidence, c.Sources, c.ListedBy, c.CDNProvider, c.CDNRegion
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
	_, _, liveOn := s.countryConfig(ctx)
	listsOn := s.cfg.Lists != nil && s.cfg.Lists(ctx)
	tspu := s.tspuState(ctx)
	if !liveOn && !listsOn && !tspu.On {
		return nil, nil
	}
	var ids []string
	if nodeID != "" {
		ids = []string{nodeID}
	}
	byRound := map[string]store.CountryCheck{}
	byList := map[string]store.BlocklistCheck{}
	byTSPU := map[string]tspuRound{}
	if liveOn {
		rounds, err := s.st.RecentCountryChecks(ctx, 1, ids...)
		if err != nil {
			return nil, err
		}
		for _, r := range rounds {
			byRound[r.NodeID] = r
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
	order := slices.Sorted(maps.Keys(known))
	out := make([]*adminv1.CountryCheck, 0, len(order))
	for _, id := range order {
		var m *adminv1.CountryCheck
		var live *countryreach.Live
		if r, ok := byRound[id]; ok {
			var d countryDetail
			_ = json.Unmarshal([]byte(r.Detail), &d)
			m, live = countryCheckMsg(r), &countryreach.Live{Verdict: r.Verdict, Eyeball: d.Eyeball}
			m.GlobalpingVerdict = countryVerdictProto[r.Verdict]
		} else {
			m = &adminv1.CountryCheck{NodeId: id, Country: countryreach.BlocklistCountry}
		}
		at := time.Unix(m.AtUnix, 0)
		var t *countryreach.TSPU
		if r, ok := byTSPU[id]; ok {
			t = &countryreach.TSPU{Verdict: r.verdict}
			m.TspuVerdict, m.DpiHop = countryVerdictProto[r.verdict], uint32(max(r.row.DPIHop, 0))
			m.TspuVantageId, m.TspuVantageName, m.TspuAtUnix = tspu.Vantage, vantageName, r.row.At.Unix()
			for _, p := range r.ports {
				m.TspuPorts = append(m.TspuPorts, &adminv1.TspuPort{Port: uint32(p.Port), Verdict: p.Verdict})
			}
			if r.row.At.After(at) {
				at = r.row.At
			}
		}
		var b *store.BlocklistCheck
		if l, ok := byList[id]; ok {
			b = &l
			if l.At.After(at) {
				at = l.At
			}
		}
		if m.AtUnix == 0 { // no live round: the newest of the others
			m.AtUnix = at.Unix()
		}
		combineCountry(m, live, t, b)
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

// addCountryConds derives COUNTRY_BLOCKED from the stored rounds of the live sources (Globalping, the probe node): it opens after
// two rounds in a row with a bad combined verdict and closes after two with OK. Unknown rounds are skipped, as if they had not
// happened; anything in between keeps the alert as it is (held), open or closed. A round is one measurement of either source,
// combined with the newest of the other (countryEvents). A block list never counts. While a node is not connected an open alert
// is held and nothing new is raised; with every live source switched off the alerts are let go.
func (s *Service) addCountryConds(ctx context.Context, sn *snapshot, live map[string]store.NodeLiveRow, activeKeys map[key]bool, d *derived, add func(cond)) {
	_, _, liveOn := s.countryConfig(ctx)
	tspu := s.tspuState(ctx)
	if !liveOn && !tspu.On {
		return
	}
	hold := func() {
		for k := range activeKeys {
			if k.kind == kCountryBlocked {
				d.holdKeys[k] = true
			}
		}
	}
	byNode := map[string][]store.CountryCheck{}
	if liveOn {
		rows, err := s.st.RecentCountryChecks(ctx, countryRecent)
		if err != nil {
			s.log.Warn("health: country check rounds", "err", err)
			hold()
			return
		}
		for _, r := range rows {
			byNode[r.NodeID] = append(byNode[r.NodeID], r)
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
	for _, n := range sn.nodes {
		k := key{kCountryBlocked, n.ID, ""}
		if n.State != "active" || !live[n.ID].Connected {
			d.holdKeys[k] = true
			continue
		}
		seen := countryEvents(byNode[n.ID], tspuByNode[n.ID], tspu.MaxAge)
		seen = seen[:min(len(seen), countryAlertRounds)]
		switch {
		case len(seen) == countryAlertRounds && countryBad(seen[0].combined.Verdict) && countryBad(seen[1].combined.Verdict):
			add(countryCond(n, seen[0]))
		case len(seen) == countryAlertRounds && countryClear(seen[0].combined.Verdict) && countryClear(seen[1].combined.Verdict):
		default:
			d.holdKeys[k] = true
		}
	}
}

// countryClear: a round that closes a full-block alert. partial counts: other networks reach the node.
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

// countryEvent is one measurement of a live source that counts for the alert, with the verdict the node shows at that moment.
type countryEvent struct {
	at       time.Time
	combined countryreach.Combined
	round    *store.CountryCheck // the Globalping round in force at that moment, nil when none
	tspu     *tspuRound          // likewise the probe node's run, nil when none or older than maxAge
}

// countryEvents are the rounds of one node that count for its alert, newest first: every Globalping round and every run of the
// probe node that has a verdict (an unknown one is skipped), each combined with the newest verdict of the other source at that
// moment (the probe node's only while it is no older than maxAge). So a node that only the probe node sees gets a round per run,
// and a source that has no verdict now does not repeat an old measurement of the other as a new round. rounds and tspu are
// newest first.
func countryEvents(rounds []store.CountryCheck, tspu []tspuRound, maxAge time.Duration) []countryEvent {
	for i := range rounds { // a change of country starts the history over
		if rounds[i].Country != rounds[0].Country {
			rounds = rounds[:i]
			break
		}
	}
	var times []time.Time
	for _, r := range rounds {
		if r.Verdict != verdictUnknown {
			times = append(times, r.At)
		}
	}
	for _, r := range tspu {
		if r.verdict != verdictUnknown {
			times = append(times, r.row.At)
		}
	}
	slices.SortStableFunc(times, func(a, b time.Time) int { return b.Compare(a) })
	times = slices.CompactFunc(times, func(a, b time.Time) bool { return a.Equal(b) }) // two measurements in one second are one round
	out := make([]countryEvent, 0, len(times))
	for _, at := range times {
		e := countryEvent{at: at}
		var live *countryreach.Live
		for i := range rounds {
			if !rounds[i].At.After(at) {
				var d countryDetail
				_ = json.Unmarshal([]byte(rounds[i].Detail), &d)
				e.round, live = &rounds[i], &countryreach.Live{Verdict: rounds[i].Verdict, Eyeball: d.Eyeball}
				break
			}
		}
		var t *countryreach.TSPU
		for i := range tspu {
			if !tspu[i].row.At.After(at) {
				if at.Sub(tspu[i].row.At) <= maxAge {
					e.tspu, t = &tspu[i], &countryreach.TSPU{Verdict: tspu[i].verdict}
				}
				break
			}
		}
		e.combined = countryreach.Combine(live, t, nil)
		out = append(out, e)
	}
	return out
}

// countryCond is the alert of a node from its newest bad round. The params say which sources saw it and how sure the verdict is;
// networks are those of the Globalping probes, ports the TCP ports that were tried (Globalping) or found blocked (the probe node).
func countryCond(n store.NodeRow, e countryEvent) cond {
	c := e.combined
	country := countryreach.TSPUCountry
	if e.round != nil {
		country = e.round.Country
	}
	var networks []string
	var ports []uint16
	if e.round != nil && slices.Contains(c.Sources, countryreach.SourceGlobalping) {
		var d countryDetail
		_ = json.Unmarshal([]byte(e.round.Detail), &d)
		for _, p := range d.Probes {
			if p.Valid && p.Network != "" && !slices.Contains(networks, p.Network) {
				networks = append(networks, p.Network)
			}
		}
		ports = append(ports, d.Ports...)
	}
	if e.tspu != nil && slices.Contains(c.Sources, countryreach.SourceTSPU) {
		for _, p := range e.tspu.ports {
			if p.Verdict == "blocked" && !slices.Contains(ports, p.Port) {
				ports = append(ports, p.Port)
			}
		}
	}
	sort.Strings(networks)
	slices.Sort(ports)
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
	activeKeys := map[key]bool{}
	for _, a := range all {
		if a.Kind == kCountryBlocked {
			active = append(active, a)
			activeKeys[key{a.Kind, a.NodeID, a.Subject}] = true
		}
	}
	d := &derived{conds: map[key]cond{}, nodes: map[string]bool{}, holdKeys: map[key]bool{}}
	for _, n := range sn.nodes {
		d.nodes[n.ID] = true
	}
	s.addCountryConds(ctx, sn, indexLiveRows(liveRows), activeKeys, d, func(c cond) { d.conds[c.key] = c })
	if err := s.reconcile(ctx, now, d, active); err != nil {
		s.log.Warn("health: reconcile country alerts", "err", err)
	}
}
