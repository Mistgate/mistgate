//go:build !js

package mcp

import (
	"slices"
	"sort"
	"strconv"
	"strings"
)

// access_report rules: the matrix of protocol by node, the country and operator view, the findings and the recommendations.
// buildAccess is pure (accessInput in, view out) so the rules are tested with fixtures.

const (
	lossPermilleHigh = 50
	lossMinPkts      = 200
	pathLossPct      = 5.0
	awgMinStalls     = 3
	maxRecs          = 8
	maxAffectedNames = 5
)

type affectedV struct {
	Users  int      `json:"users"`
	Groups []string `json:"groups,omitempty"`
}

type rejectSplitV struct {
	KeyExpired          uint32 `json:"key_expired,omitempty"`
	UnknownKey          uint32 `json:"unknown_key,omitempty"`
	HandshakeIncomplete uint32 `json:"handshake_incomplete,omitempty"`
	NoAuth              uint32 `json:"no_auth,omitempty"`
	StalledHandshake    uint32 `json:"stalled_handshake,omitempty"`
	// links/3: why, where the engine sees it
	NoReply           uint32 `json:"no_reply,omitempty"`            // hs_unfinished: the node answered, nothing came back
	CertRefused       uint32 `json:"cert_refused,omitempty"`        // the app refused the node's certificate
	AlertsSent        uint32 `json:"alerts_sent,omitempty"`         // the node refused the hello (ALPN, version)
	AlertsRecv        uint32 `json:"alerts_recv,omitempty"`         // the app ended the handshake with an alert
	RealityFallback   uint32 `json:"reality_fallback,omitempty"`    // sent to the REALITY target as not a client
	RealityAuthFailed uint32 `json:"reality_auth_failed,omitempty"` // our name, wrong key or short id
	AnswersLost       uint32 `json:"answers_lost,omitempty"`        // AWG responses never confirmed
	Unanswered        uint32 `json:"unanswered,omitempty"`          // AWG initiations the node did not answer
}

type accessGroupCellV struct {
	Group        string `json:"group"`
	GivenUsers   int    `json:"given_users"`
	DevicesTried int    `json:"devices_tried"`
	DevicesOK    int    `json:"devices_ok"`
	SuccessPct   *int   `json:"success_pct,omitempty"`
	NeverConn    int    `json:"never_connected_users,omitempty"`
}

type accessCellV struct {
	Node         string `json:"node"`
	NodeID       string `json:"node_id"`
	InboundID    string `json:"inbound_id"`
	Profile      string `json:"profile"`
	Protocol     string `json:"protocol"`
	Transport    string `json:"transport,omitempty"`
	Port         uint32 `json:"port"`
	Network      string `json:"network"`
	Egress       string `json:"egress,omitempty"`
	CDN          bool   `json:"cdn,omitempty"`
	InSubs       bool   `json:"in_subscriptions"`
	State        string `json:"state"`
	Way          string `json:"way"` // working, partial, failing, broken, unknown: what the classes and counters say
	GivenUsers   int    `json:"given_users"`
	UsersTried   int    `json:"users_tried"`
	UsersOK      int    `json:"users_ok"`
	DevicesTried int    `json:"devices_tried"`
	DevicesOK    int    `json:"devices_ok"`
	SuccessPct   *int   `json:"success_pct,omitempty"`
	// AttemptsObservable false: the protocol keeps no attempt counter (VLESS), so only successes are seen.
	AttemptsObservable bool               `json:"attempts_observable"`
	NeverConnected     int                `json:"never_connected_users"`
	UntriedUsers       int                `json:"untried_users"`
	Connects           uint32             `json:"connects,omitempty"`
	Rejects            uint32             `json:"rejects,omitempty"`
	Stalls             uint32             `json:"stalls,omitempty"`
	Drops              uint32             `json:"drops,omitempty"`
	Freezes            uint32             `json:"freezes,omitempty"`
	Quiet              uint32             `json:"quiet,omitempty"`
	LossPermille       *uint32            `json:"loss_permille,omitempty"`
	RttMs              *uint32            `json:"rtt_ms,omitempty"`
	RejectSplit        *rejectSplitV      `json:"reject_split,omitempty"`
	Hellos             uint32             `json:"hellos,omitempty"`
	Handshakes         uint32             `json:"handshakes,omitempty"`
	TCPStalls          uint32             `json:"tcp_stalls,omitempty"`
	TCPStalls16K       uint32             `json:"tcp_stalls_16k,omitempty"`
	Verdict            string             `json:"verdict"`
	Signals            []string           `json:"signals,omitempty"`
	Unsupported        []string           `json:"unsupported,omitempty"`
	Groups             []accessGroupCellV `json:"groups,omitempty"`
}

type accKlickOpV struct {
	Name    string `json:"name"`
	Reports uint32 `json:"reports"`
	OK      uint32 `json:"ok"`
	Bad     uint32 `json:"bad"`
}

type accKlickKindV struct {
	Kind          string `json:"kind"`
	Reports       uint32 `json:"reports"`
	OK            uint32 `json:"ok"`
	Freeze        uint32 `json:"freeze,omitempty"`
	TCPBlocked    uint32 `json:"tcp_blocked,omitempty"`
	MedianDelayMs uint32 `json:"median_delay_ms,omitempty"`
	DLChecked     uint32 `json:"dl_checked,omitempty"`
	DLStalled     uint32 `json:"dl_stalled,omitempty"`
}

type accTspuPortV struct {
	Port    uint32 `json:"port"`
	Verdict string `json:"verdict"`
}

type accessCountryNodeV struct {
	NodeID         string          `json:"node_id"`
	Node           string          `json:"node"`
	Country        string          `json:"country"`
	Verdict        string          `json:"verdict"`
	Confidence     string          `json:"confidence,omitempty"`
	Sources        []string        `json:"sources,omitempty"`
	ListedBy       []string        `json:"listed_by,omitempty"`
	TSPUPorts      []accTspuPortV  `json:"tspu_ports,omitempty"`
	DarkOperators  []string        `json:"dark_operators,omitempty"`
	ReachOperators []string        `json:"reach_operators,omitempty"`
	MobileDark     int             `json:"mobile_dark,omitempty"`
	FixedDark      int             `json:"fixed_dark,omitempty"`
	KlickReports   uint32          `json:"klick_reports,omitempty"`
	KlickOK        uint32          `json:"klick_ok,omitempty"`
	KlickOperators []accKlickOpV   `json:"klick_operators,omitempty"`
	KlickKinds     []accKlickKindV `json:"klick_kinds,omitempty"`
}

type accessFindingV struct {
	Code     string            `json:"code"`
	Severity string            `json:"severity"`
	Node     string            `json:"node,omitempty"`
	Evidence []string          `json:"evidence"`
	Affected affectedV         `json:"affected"`
	Detail   map[string]string `json:"detail,omitempty"`
	Class    string            `json:"block_class,omitempty"`
	Conf     string            `json:"confidence,omitempty"`
}

type accessRecV struct {
	Kind     string            `json:"kind"`
	Rank     int               `json:"rank"`
	Target   map[string]string `json:"target"`
	Why      []string          `json:"why"`
	Effect   string            `json:"expected_effect"`
	PlanTool string            `json:"plan_tool,omitempty"`
	NextTool string            `json:"next_tool,omitempty"`
	Affected affectedV         `json:"affected"`
	Conf     string            `json:"confidence,omitempty"`
	sev      int
	key      string
}

// AccessReportV is the access_report result.
type AccessReportV struct {
	NowUnix         int64                `json:"now_unix"`
	SinceUnix       int64                `json:"since_unix"`
	Hours           int                  `json:"hours"`
	Filters         map[string]string    `json:"filters,omitempty"`
	UsersRead       int                  `json:"users_read"`
	UsersTotal      int                  `json:"users_total"`
	UsersSampled    bool                 `json:"users_sampled,omitempty"`
	Limits          []string             `json:"limits"`
	Matrix          []accessCellV        `json:"matrix"`
	Country         []accessCountryNodeV `json:"country"`
	BlockDiagnosis  []blockNodeV         `json:"block_diagnosis"`
	Findings        []accessFindingV     `json:"findings"`
	Recommendations []accessRecV         `json:"recommendations"`
	Unavailable     []string             `json:"unavailable,omitempty"`
	Truncated       bool                 `json:"truncated,omitempty"`
}

func (v *AccessReportV) shrink() bool {
	v.Truncated = true
	for i := range v.Matrix {
		if len(v.Matrix[i].Groups) > 0 {
			for j := range v.Matrix {
				v.Matrix[j].Groups = nil
			}
			return true
		}
	}
	for i := range v.BlockDiagnosis {
		for j := range v.BlockDiagnosis[i].Classes {
			if len(v.BlockDiagnosis[i].Classes[j].Next) > 0 {
				for a := range v.BlockDiagnosis {
					for b := range v.BlockDiagnosis[a].Classes {
						v.BlockDiagnosis[a].Classes[b].Next = nil
					}
				}
				return true
			}
		}
	}
	if cut(&v.Country) || cut(&v.Matrix) || cut(&v.BlockDiagnosis) || cut(&v.Findings) || cut(&v.Recommendations) {
		return true
	}
	for i := range v.BlockDiagnosis {
		if cut(&v.BlockDiagnosis[i].Classes) {
			return true
		}
	}
	return false
}

var accessLimits = []string{
	"VLESS keeps no failure counter per device, so success_pct is absent; with links/3 agents its server counts hellos, connects over REALITY TCP, REALITY refusals, unfinished handshakes and TCP stalls (reject_split, tcp_stalls, signals).",
	"rejects are known keys refused for an ended term; unknown_keys are keys the node does not hold. Why a handshake failed (no reply, a TLS alert either way, a refused certificate, a failed REALITY authentication, AWG answers lost or not sent) is in reject_split for links/3 agents only where the engine sees it; unsupported names the rest, including every reason on older agents and the AWG handshake counts on the kernel backend.",
	"The country check judges TCP ports and addresses only: there is no UDP test, so UDP protocols are judged by in-country port checks, path loss and the counters.",
	"kl!ck reports have no operator by protocol split: operators and server kinds are tallied apart. Counters know no country or operator of a person.",
}

// ---------------------------------------------------------------------------------------------------------------------

type accessCtx struct {
	*blockCtx
	groupsByID   map[string]*accGroup
	usersByGroup map[string][]*accUser
	given        map[string][]*accUser // inbound id -> users given it directly
	diags        map[string]*blockNodeV
	ways         map[string]*wayV
	cells        map[string]*accessCellV
	inScope      map[string]bool // node ids in scope
}

func (a *accessCtx) userGiven(u *accUser, x *accInbound) bool {
	g := a.groupsByID[u.GroupID]
	if g == nil || !x.Enabled || x.State == "disabled" || !(x.Direct || x.CDN) {
		return false
	}
	if !slices.Contains(g.ProfileIDs, x.ProfileID) || slices.Contains(g.DisabledNodes, x.NodeID) {
		return false
	}
	return u.AllNodes || slices.Contains(u.NodeIDs, x.NodeID)
}

func pct(ok, tried int) *int {
	if tried == 0 {
		return nil
	}
	p := ok * 100 / tried
	return &p
}

func transportOf(summary string) string {
	s := strings.ToLower(summary)
	var out []string
	for _, k := range []string{"xhttp", "reality", "vision", "grpc", "ws", "hop", "salamander", "gecko", "cdn"} {
		if strings.Contains(s, k) {
			out = append(out, k)
		}
	}
	return strings.Join(out, "+")
}

type devAgg struct {
	user                            string
	connects, rejects, drops, stall uint32
	freezes, quiet                  uint32
	online                          uint64
}

func (d *devAgg) ok(proto string) bool {
	if normProtocol(proto) == "vless" {
		return d.online > 0 || d.connects > 0
	}
	return d.connects > 0
}

func (d *devAgg) tried(proto string) bool {
	return d.ok(proto) || d.rejects+d.drops+d.stall+d.freezes > 0
}

// buildAccess runs the rules.
func buildAccess(in *accessInput) *AccessReportV {
	b := newBlockCtx(in)
	a := &accessCtx{blockCtx: b, groupsByID: map[string]*accGroup{}, usersByGroup: map[string][]*accUser{}, given: map[string][]*accUser{},
		diags: map[string]*blockNodeV{}, cells: map[string]*accessCellV{}, inScope: map[string]bool{}}
	for i := range in.Groups {
		a.groupsByID[in.Groups[i].ID] = &in.Groups[i]
	}
	for i := range in.Users {
		u := &in.Users[i]
		a.usersByGroup[u.GroupID] = append(a.usersByGroup[u.GroupID], u)
	}
	for i := range in.Inbounds {
		x := &in.Inbounds[i]
		for j := range in.Users {
			if a.userGiven(&in.Users[j], x) {
				a.given[x.ID] = append(a.given[x.ID], &in.Users[j])
			}
		}
	}
	for i := range in.Nodes {
		n := &in.Nodes[i]
		d := b.diagnose(n)
		a.diags[n.ID] = &d
	}
	a.ways = b.wayStates(a.diags)

	v := &AccessReportV{NowUnix: in.NowUnix, SinceUnix: in.SinceUnix, Hours: in.Hours, Limits: accessLimits, Matrix: []accessCellV{}, Country: []accessCountryNodeV{},
		BlockDiagnosis: []blockNodeV{}, Findings: []accessFindingV{}, Recommendations: []accessRecV{}, UsersRead: len(in.Users), UsersTotal: in.UsersTotal,
		UsersSampled: in.UsersTotal > len(in.Users), Unavailable: in.Unavailable}
	v.Filters = map[string]string{}
	for k, val := range map[string]string{"country": in.Country, "group": in.Group, "node": in.Node, "protocol": in.Protocol} {
		if val != "" {
			v.Filters[k] = val
		}
	}
	if len(v.Filters) == 0 {
		v.Filters = nil
	}

	// scope
	for i := range in.Nodes {
		n := &in.Nodes[i]
		if in.Node != "" && n.ID != in.Node {
			continue
		}
		if in.Country != "" {
			cc := in.Countries[n.ID]
			if cc == nil && !strings.EqualFold(n.Country, in.Country) || cc != nil && !strings.EqualFold(cc.Country, in.Country) {
				continue
			}
		}
		a.inScope[n.ID] = true
	}
	devs := map[string]map[string]*devAgg{} // inbound -> device
	for _, r := range in.Rows {
		m := devs[r.InboundID]
		if m == nil {
			m = map[string]*devAgg{}
			devs[r.InboundID] = m
		}
		k := r.UserID + "/" + r.DeviceID
		d := m[k]
		if d == nil {
			d = &devAgg{user: r.UserID}
			m[k] = d
		}
		d.connects += r.Connects
		d.rejects += r.Rejects
		d.drops += r.Drops
		d.stall += r.Stalls
		d.freezes += r.Freezes
		d.quiet += r.Quiet
		d.online += r.OnlineS
	}
	rowsBy := map[string][]accRow{}
	for _, r := range in.Rows {
		rowsBy[r.InboundID] = append(rowsBy[r.InboundID], r)
	}

	for i := range in.Inbounds {
		x := &in.Inbounds[i]
		if !a.inScope[x.NodeID] {
			continue
		}
		if in.Protocol != "" && normProtocol(x.Protocol) != in.Protocol {
			continue
		}
		cell := a.cell(x, devs[x.ID], rowsBy[x.ID])
		a.cells[x.ID] = cell
		v.Matrix = append(v.Matrix, *cell)
	}
	slices.SortFunc(v.Matrix, func(p, q accessCellV) int {
		return strings.Compare(p.Node+"/"+p.Protocol+"/"+u32(p.Port)+"/"+p.InboundID, q.Node+"/"+q.Protocol+"/"+u32(q.Port)+"/"+q.InboundID)
	})
	for i := range in.Nodes {
		n := &in.Nodes[i]
		if !a.inScope[n.ID] {
			continue
		}
		d := *a.diags[n.ID]
		v.BlockDiagnosis = append(v.BlockDiagnosis, d)
		if cn, ok := a.countryNode(n); ok {
			v.Country = append(v.Country, cn)
		}
	}
	slices.SortFunc(v.BlockDiagnosis, func(p, q blockNodeV) int { return strings.Compare(p.Node, q.Node) })
	slices.SortFunc(v.Country, func(p, q accessCountryNodeV) int { return strings.Compare(p.Node, q.Node) })

	v.Findings = a.findings()
	v.Recommendations = a.recommendations(v.Findings)
	return v
}

func (a *accessCtx) cell(x *accInbound, devs map[string]*devAgg, rows []accRow) *accessCellV {
	in := a.in
	n := a.byID[x.NodeID]
	p := in.Profiles[x.ProfileID]
	c := &accessCellV{NodeID: x.NodeID, InboundID: x.ID, Profile: nm(p.Name), Protocol: normProtocol(x.Protocol), Transport: transportOf(p.Summary), Port: x.Port,
		Network: netOf(x.Protocol), Egress: clean(x.Egress, 20), CDN: x.CDN, InSubs: x.Direct, State: x.State, AttemptsObservable: normProtocol(x.Protocol) != "vless"}
	if n != nil {
		c.Node = nm(n.Name)
	}
	if w := a.ways[x.ID]; w != nil {
		c.Way = w.State
	}
	t := in.Totals[x.ID]
	c.Hellos, c.Handshakes = t.Hellos, t.Handshakes
	c.Unsupported = t.Unsupported
	users := a.given[x.ID]
	givenSet := map[string]bool{}
	for _, u := range users {
		givenSet[u.ID] = true
	}
	c.GivenUsers = len(users)
	triedU, okU := map[string]bool{}, map[string]bool{}
	var lossNum, lossDen, rttNum, rttDen uint64
	for _, r := range rows {
		c.Connects += r.Connects
		c.Rejects += r.Rejects
		c.Stalls += r.Stalls
		c.Drops += r.Drops
		c.Freezes += r.Freezes
		c.Quiet += r.Quiet
		if r.PktsSent > 0 {
			lossNum += uint64(r.LossPermille) * r.PktsSent
			lossDen += r.PktsSent
		}
		if r.RttSamples > 0 {
			rttNum += uint64(r.RttMs) * uint64(r.RttSamples)
			rttDen += uint64(r.RttSamples)
		}
	}
	for _, d := range devs {
		if d.tried(x.Protocol) {
			c.DevicesTried++
			triedU[d.user] = true
		}
		if d.ok(x.Protocol) {
			c.DevicesOK++
			okU[d.user] = true
		}
	}
	c.UsersTried, c.UsersOK = len(triedU), len(okU)
	if c.AttemptsObservable {
		c.SuccessPct = pct(c.DevicesOK, c.DevicesTried)
	}
	if lossDen > 0 {
		l := uint32(lossNum / lossDen)
		c.LossPermille = &l
	}
	if rttDen > 0 {
		r := uint32(rttNum / rttDen)
		c.RttMs = &r
	}
	for _, u := range users {
		if u.LastSeen == 0 {
			c.NeverConnected++
		}
		if !triedU[u.ID] {
			c.UntriedUsers++
		}
	}
	split := rejectSplitV{KeyExpired: c.Rejects, UnknownKey: t.UnknownKeys}
	if !t.unsupportedHas("accepted") {
		split.HandshakeIncomplete = t.Hellos - min(t.Hellos, t.Accepted)
		split.NoAuth = t.Accepted - min(t.Accepted, t.Handshakes)
	}
	if c.Protocol == "awg" {
		split.StalledHandshake = max(c.Stalls, t.Stalls)
	}
	split.NoReply, split.CertRefused, split.AlertsSent = t.HsUnfinished, t.TLSCertRefused, t.TLSAlertsSent
	split.AlertsRecv, split.RealityFallback, split.RealityAuthFailed = t.TLSAlertsRecv, t.RealityFallback, t.AuthFailed
	split.AnswersLost, split.Unanswered = t.ResponsesUnmatched, t.Initiations-min(t.Initiations, t.Responses)
	if split != (rejectSplitV{}) {
		c.RejectSplit = &split
	}
	c.TCPStalls, c.TCPStalls16K = t.TCPStalls, t.TCPStalls16K
	if freeze, _, _ := t.tcpFreeze(); freeze {
		c.Signals = append(c.Signals, "tcp_freeze_16k")
	}
	if lost, _ := t.answerLost(c.Protocol); lost {
		c.Signals = append(c.Signals, "answers_lost")
	}
	if t.known("tls_cert_refused") && t.TLSCertRefused >= minReasonCount {
		c.Signals = append(c.Signals, "cert_refused")
	}
	// signals and the verdict
	if c.Rejects > 0 {
		c.Signals = append(c.Signals, "rejects")
	}
	if c.Stalls > 0 {
		c.Signals = append(c.Signals, "stalls")
	}
	if c.Freezes >= 2 {
		c.Signals = append(c.Signals, "freezes")
	}
	if c.LossPermille != nil && *c.LossPermille >= lossPermilleHigh && lossDen >= lossMinPkts {
		c.Signals = append(c.Signals, "high_loss")
	}
	switch {
	case !c.AttemptsObservable:
		c.Verdict = "no_signal"
		if c.DevicesOK > 0 {
			c.Verdict = "ok"
		}
		if slices.Contains(c.Signals, "tcp_freeze_16k") || slices.Contains(c.Signals, "answers_lost") || slices.Contains(c.Signals, "cert_refused") {
			c.Verdict = "degraded" // the server's own handshakes or streams say so, though no device counts its failures
		}
	case c.DevicesTried == 0:
		c.Verdict = "no_attempts"
	case c.DevicesTried < 2 && c.DevicesOK == 0:
		c.Verdict = "unclear"
	case c.DevicesOK*10 >= c.DevicesTried*8 && len(c.Signals) == 0:
		c.Verdict = "ok"
	case c.DevicesOK*10 >= c.DevicesTried*4:
		c.Verdict = "degraded"
	default:
		c.Verdict = "failing"
	}
	// by group
	byGroup := map[string][]*accUser{}
	for _, u := range users {
		byGroup[u.GroupID] = append(byGroup[u.GroupID], u)
	}
	var gids []string
	for g := range byGroup {
		gids = append(gids, g)
	}
	slices.Sort(gids)
	for _, gid := range gids {
		gc := accessGroupCellV{Group: nm(a.groupsByID[gid].Name), GivenUsers: len(byGroup[gid])}
		inG := map[string]bool{}
		for _, u := range byGroup[gid] {
			inG[u.ID] = true
			if u.LastSeen == 0 {
				gc.NeverConn++
			}
		}
		for _, d := range devs {
			if !inG[d.user] {
				continue
			}
			if d.tried(x.Protocol) {
				gc.DevicesTried++
			}
			if d.ok(x.Protocol) {
				gc.DevicesOK++
			}
		}
		if c.AttemptsObservable {
			gc.SuccessPct = pct(gc.DevicesOK, gc.DevicesTried)
		}
		c.Groups = append(c.Groups, gc)
	}
	return c
}

func (a *accessCtx) countryNode(n *accNode) (accessCountryNodeV, bool) {
	cc := a.in.Countries[n.ID]
	km := a.in.Klick[n.ID]
	if cc == nil && km == nil {
		return accessCountryNodeV{}, false
	}
	o := accessCountryNodeV{NodeID: n.ID, Node: nm(n.Name)}
	if cc != nil {
		vs := summarizeVantages(cc.Vantages)
		o.Country, o.Verdict, o.Confidence = clean(cc.Country, 8), clean(cc.Verdict, 20), clean(cc.Confidence, 20)
		o.Sources, o.ListedBy = cc.Sources, cc.ListedBy
		for _, p := range cc.TSPU {
			o.TSPUPorts = append(o.TSPUPorts, accTspuPortV{Port: p.Port, Verdict: clean(p.Verdict, 12)})
		}
		o.DarkOperators, o.ReachOperators = cleanAll(vs.darkOps, 40, 8), cleanAll(vs.reachOps, 40, 8)
		o.MobileDark, o.FixedDark = vs.mobileDark, vs.fixedDark
	} else {
		o.Country = strings.ToUpper(n.Country)
		o.Verdict = "unknown"
	}
	if km != nil {
		o.KlickReports, o.KlickOK = km.Reports, km.OK
		for _, g := range km.Operators {
			o.KlickOperators = append(o.KlickOperators, accKlickOpV{Name: clean(g.Name, 40), Reports: g.Reports, OK: g.OK, Bad: g.bad()})
		}
		for _, k := range km.Kinds {
			o.KlickKinds = append(o.KlickKinds, accKlickKindV{Kind: clean(k.Kind, 40), Reports: k.Reports, OK: k.OK, Freeze: k.Freeze, TCPBlocked: k.TCPBlocked,
				MedianDelayMs: k.MedianDelayMs, DLChecked: k.DLChecked, DLStalled: k.DLStalled})
		}
	}
	return o, true
}

func cleanAll(in []string, max, n int) []string {
	var out []string
	for _, s := range in {
		if len(out) == n {
			break
		}
		out = append(out, clean(s, max))
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------------------
// findings

func (a *accessCtx) affectedBy(ids ...string) affectedV {
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, id := range ids {
		for _, u := range a.given[id] {
			seen[u.ID] = true
			if g := a.groupsByID[u.GroupID]; g != nil {
				groups[nm(g.Name)] = true
			}
		}
	}
	var gs []string
	for g := range groups {
		gs = append(gs, g)
	}
	slices.Sort(gs)
	if len(gs) > maxAffectedNames {
		gs = gs[:maxAffectedNames]
	}
	return affectedV{Users: len(seen), Groups: gs}
}

func (a *accessCtx) inboundsOf(nodeID string) []*accInbound { return a.inbounds[nodeID] }

func (a *accessCtx) idsOf(nodeID string, f func(*accInbound) bool) []string {
	var ids []string
	for _, x := range a.inboundsOf(nodeID) {
		if f == nil || f(x) {
			ids = append(ids, x.ID)
		}
	}
	return ids
}

func classScopeMatch(c blockClassV, x *accInbound) bool {
	switch {
	case c.inbound != "":
		return c.inbound == x.ID
	case strings.HasPrefix(c.Scope, "port:"):
		return c.port == x.Port && netOf(x.Protocol) == "tcp"
	case c.protocol == "udp":
		return isUDPProto(x.Protocol)
	case c.protocol != "":
		return normProtocol(x.Protocol) == c.protocol
	}
	return true
}

func sevFor(c blockClassV) string {
	switch {
	case c.Class == clsUnknown || c.Confidence == confLow:
		return "info"
	case c.hard && c.Confidence != confLow:
		return "error"
	}
	return "warning"
}

func (a *accessCtx) findings() []accessFindingV {
	in := a.in
	var out []accessFindingV
	add := func(f accessFindingV) {
		f.Evidence = sortedUnique(f.Evidence)
		f.Detail = cleanMap(f.Detail, 120)
		out = append(out, f)
	}
	nodeIDs := make([]string, 0, len(a.inScope))
	for id := range a.inScope {
		nodeIDs = append(nodeIDs, id)
	}
	slices.SortFunc(nodeIDs, func(p, q string) int { return strings.Compare(a.byID[p].Name, a.byID[q].Name) })

	for _, nid := range nodeIDs {
		n := a.byID[nid]
		d := a.diags[nid]
		all := a.idsOf(nid, nil)
		aff := a.affectedBy(all...)
		if nodeIsOurs(n.Status) {
			add(accessFindingV{Code: "node_down", Severity: "error", Node: nm(n.Name), Evidence: []string{"node:" + nid}, Affected: aff, Detail: map[string]string{"status": n.Status}})
		}
		if cc := in.Countries[nid]; cc != nil && !nodeIsOurs(n.Status) {
			switch cc.Verdict {
			case "blocked", "tcp_blocked", "partial":
				sev := "error"
				if cc.Verdict == "partial" {
					sev = "warning"
				}
				vs := summarizeVantages(cc.Vantages)
				add(accessFindingV{Code: "node_blocked_in_country", Severity: sev, Node: nm(n.Name), Evidence: []string{"check:" + nid}, Affected: aff,
					Detail: map[string]string{"verdict": cc.Verdict, "confidence": cc.Confidence, "country": cc.Country, "dark": strings.Join(cleanAll(vs.darkOps, 40, 8), ",")}})
			}
		}
		for _, c := range d.Classes {
			f := accessFindingV{Code: c.Class, Severity: sevFor(c), Node: nm(n.Name), Evidence: c.Evidence, Class: c.Class, Conf: c.Confidence, Detail: map[string]string{}}
			for k, v := range c.Detail {
				f.Detail[k] = v
			}
			if len(c.Operators) > 0 {
				f.Detail["operators"] = strings.Join(cleanAll(c.Operators, 40, 8), ",")
			}
			f.Detail["scope"] = c.Scope
			f.Affected = a.affectedBy(a.idsOf(nid, func(x *accInbound) bool { return classScopeMatch(c, x) })...)
			add(f)
		}
		// protocol blocked on node: one protocol fails while another works on the same node
		var failing, working []*accInbound
		for _, x := range a.inboundsOf(nid) {
			if !x.Enabled || x.State == "disabled" {
				continue
			}
			w := a.ways[x.ID]
			switch {
			case w.State == stateFailing:
				failing = append(failing, x)
			case w.works():
				working = append(working, x)
			}
		}
		for _, f := range failing {
			if a.ways[f.ID].State == stateBroken {
				continue
			}
			for _, w := range working {
				if normProtocol(w.Protocol) == normProtocol(f.Protocol) && netOf(w.Protocol) == netOf(f.Protocol) && w.Port == f.Port {
					continue
				}
				if normProtocol(w.Protocol) == normProtocol(f.Protocol) {
					continue
				}
				fw, ww := a.ways[f.ID], a.ways[w.ID]
				ev := []string{"conn:" + f.ID, "conn:" + w.ID, "check:" + nid}
				add(accessFindingV{Code: "protocol_blocked_on_node", Severity: "error", Node: nm(n.Name), Evidence: ev, Affected: a.affectedBy(f.ID),
					Detail: map[string]string{"blocked": normProtocol(f.Protocol) + "/" + netOf(f.Protocol) + "/" + u32(f.Port), "works": normProtocol(w.Protocol) + "/" + netOf(w.Protocol) + "/" + u32(w.Port),
						"blocked_by": strings.Join(fw.BlockedBy, ","), "works_by": strings.Join(ww.WorksBy, ","),
						"operators": strings.Join(cleanAll(summarizeVantages(a.vant(nid)).darkOps, 40, 8), ",")}})
				break
			}
		}
		// udp path loss
		var lossy bool
		ev := []string{}
		for _, x := range a.inboundsOf(nid) {
			if c := a.cells[x.ID]; c != nil && c.Protocol == "hysteria2" && c.LossPermille != nil && *c.LossPermille >= lossPermilleHigh {
				lossy = true
				ev = append(ev, "conn:"+x.ID)
			}
		}
		for _, l := range in.Loss {
			if l.ToID == nid && l.LossPct >= pathLossPct {
				lossy = true
				ev = append(ev, "loss:"+l.FromID+":"+l.ToID)
			}
		}
		if in.UDPLossNodes[nid] {
			lossy = true
			ev = append(ev, "check:"+nid)
		}
		if lossy {
			add(accessFindingV{Code: "udp_lossy_path", Severity: "warning", Node: nm(n.Name), Evidence: ev, Affected: a.affectedBy(a.idsOf(nid, func(x *accInbound) bool { return isUDPProto(x.Protocol) })...),
				Detail: map[string]string{"next": "path_probe udp, node_trace"}})
		}
		// awg initiations without a reply
		for _, x := range a.inboundsOf(nid) {
			if normProtocol(x.Protocol) != "awg" {
				continue
			}
			t := in.Totals[x.ID]
			if t.Stalls >= awgMinStalls && (t.Connects == 0 || t.Stalls >= t.Connects) {
				add(accessFindingV{Code: "awg_handshake_no_reply", Severity: "warning", Node: nm(n.Name), Evidence: []string{"conn:" + x.ID}, Affected: a.affectedBy(x.ID),
					Detail: map[string]string{"stalls": u32(t.Stalls), "connects": u32(t.Connects), "keys_stalled": u32(t.KeysStalled), "port": u32(x.Port),
						"why": "initiations reach the node but no handshake completes: the reply does not reach the client, the key is wrong or the protocol is recognised"}})
			}
		}
		// given but never connected
		never := map[string]bool{}
		for _, id := range all {
			for _, u := range a.given[id] {
				if u.LastSeen == 0 {
					never[u.ID] = true
				}
			}
		}
		if len(never) > 0 {
			add(accessFindingV{Code: "given_never_connected", Severity: "info", Node: nm(n.Name), Evidence: []string{"node:" + nid}, Affected: affectedV{Users: len(never)},
				Detail: map[string]string{"why": "people given this node who never connected at all (not only to this node)"}})
		}
		if n.NoCounters && aff.Users > 0 {
			add(accessFindingV{Code: "counters_unavailable", Severity: "info", Node: nm(n.Name), Evidence: []string{"node:" + nid}, Affected: aff})
		}
	}

	// relays
	for _, r := range in.Relays {
		if !a.inScope[r.ExitID] && !a.inScope[r.EntryID] || !r.Enabled {
			continue
		}
		if why := relayProblem(r); why != "" {
			x := a.inboundByID(r.InboundID)
			var aff affectedV
			if x != nil {
				aff = a.affectedBy(x.ID)
			}
			add(accessFindingV{Code: "relay_down", Severity: "error", Node: nm(a.nodeName(r.EntryID)), Evidence: []string{"relay:" + r.ID}, Affected: aff,
				Detail: map[string]string{"why": why, "entry": nm(a.nodeName(r.EntryID)), "exit": nm(a.nodeName(r.ExitID))}})
		}
	}

	// groups
	gids := make([]string, 0, len(a.groupsByID))
	for id := range a.groupsByID {
		gids = append(gids, id)
	}
	slices.Sort(gids)
	for _, gid := range gids {
		g := a.groupsByID[gid]
		if in.Group != "" && gid != in.Group {
			continue
		}
		users := a.usersByGroup[gid]
		if len(users) == 0 {
			continue
		}
		var ways []string // inbound ids given to the group
		for i := range in.Inbounds {
			x := &in.Inbounds[i]
			if a.groupGives(g, x) {
				ways = append(ways, x.ID)
			}
		}
		ways = sortedUnique(ways)
		var failN, unkN, okN int
		var okCDN, okOther int
		var ev []string
		for _, id := range ways {
			x := a.inboundByID(id)
			st := a.effectiveWay(x)
			switch st {
			case stateFailing, stateBroken:
				failN++
				ev = append(ev, "conn:"+id, "check:"+x.NodeID)
			case stateWorking, statePartial:
				okN++
				if x.CDN {
					okCDN++
				} else {
					okOther++
				}
			default:
				unkN++
			}
		}
		if len(ways) > 0 && failN > 0 && okN == 0 {
			sev := "error"
			if unkN > 0 {
				sev = "warning"
			}
			add(accessFindingV{Code: "group_without_working_way", Severity: sev, Evidence: ev, Affected: affectedV{Users: len(users), Groups: []string{nm(g.Name)}},
				Detail: map[string]string{"group": nm(g.Name), "ways": strconv.Itoa(len(ways)), "failing": strconv.Itoa(failN), "unknown": strconv.Itoa(unkN)}})
		}
		if len(ways) == 0 {
			add(accessFindingV{Code: "group_without_working_way", Severity: "error", Evidence: []string{"group:" + gid}, Affected: affectedV{Users: len(users), Groups: []string{nm(g.Name)}},
				Detail: map[string]string{"group": nm(g.Name), "ways": "0", "why": "no enabled server of the group's profiles is given to its people"}})
		}
		if okCDN > 0 && okOther == 0 && failN > 0 {
			add(accessFindingV{Code: "cdn_only_option", Severity: "info", Evidence: []string{"group:" + gid}, Affected: affectedV{Users: len(users), Groups: []string{nm(g.Name)}},
				Detail: map[string]string{"group": nm(g.Name), "why": "the only working way of the group goes through a CDN"}})
		}
	}
	slices.SortStableFunc(out, func(p, q accessFindingV) int {
		if sevRank(p.Severity) != sevRank(q.Severity) {
			return sevRank(q.Severity) - sevRank(p.Severity)
		}
		if p.Affected.Users != q.Affected.Users {
			return q.Affected.Users - p.Affected.Users
		}
		return strings.Compare(p.Code+p.Node, q.Code+q.Node)
	})
	return out
}

func (a *accessCtx) groupGives(g *accGroup, x *accInbound) bool {
	return x.Enabled && x.State != "disabled" && (x.Direct || x.CDN) && slices.Contains(g.ProfileIDs, x.ProfileID) && !slices.Contains(g.DisabledNodes, x.NodeID)
}

func (a *accessCtx) vant(nodeID string) []accVantage {
	if cc := a.in.Countries[nodeID]; cc != nil {
		return cc.Vantages
	}
	return nil
}

func (a *accessCtx) nodeName(id string) string {
	if n := a.byID[id]; n != nil {
		return n.Name
	}
	return ""
}

func (a *accessCtx) inboundByID(id string) *accInbound {
	for i := range a.in.Inbounds {
		if a.in.Inbounds[i].ID == id {
			return &a.in.Inbounds[i]
		}
	}
	return nil
}

func relayProblem(r accRelay) string {
	switch {
	case !r.AgentSupported:
		return "entry agent cannot take the forward"
	case r.State == "failed":
		return "entry reported the forward failed"
	case !r.ExitLive:
		return "the exit's server is not usable"
	case r.Checked && !r.CheckOK:
		return "the last check failed"
	case r.MonitorFails >= 2:
		return "the client-eye round failed repeatedly"
	}
	return ""
}

func relayHealthy(r accRelay) bool {
	return r.Enabled && r.State == "active" && relayProblem(r) == ""
}

// effectiveWay is the state of an inbound once a healthy relay in front of it is counted: a relay whose entry node is not itself
// failing makes a blocked server reachable.
func (a *accessCtx) effectiveWay(x *accInbound) string {
	st := a.ways[x.ID].State
	if st != stateFailing && st != stateBroken {
		return st
	}
	for _, r := range a.in.Relays {
		if r.InboundID == x.ID && relayHealthy(r) {
			if w := a.diags[r.EntryID]; w != nil && !hasHardClass(w) {
				return stateWorking
			}
		}
	}
	return st
}

func hasHardClass(d *blockNodeV) bool {
	for _, c := range d.Classes {
		if c.hard && c.Confidence != confLow {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------------------------------
// recommendations

func (a *accessCtx) hasHealthyRelayFor(nodeID string) bool {
	for _, r := range a.in.Relays {
		if r.ExitID == nodeID && relayHealthy(r) {
			if w := a.diags[r.EntryID]; w != nil && !hasHardClass(w) {
				return true
			}
		}
	}
	return false
}

// entryFor picks the nodes that could carry a relay for exit: online, not blocked themselves, their country view fine; for a
// whitelist the entry must be reached from mobile networks.
func (a *accessCtx) entryFor(exit *accNode, c blockClassV) []string {
	exitCC := a.in.Countries[exit.ID]
	var darkOps []string
	if exitCC != nil {
		darkOps = summarizeVantages(exitCC.Vantages).darkOps
	}
	country := ""
	if exitCC != nil {
		country = exitCC.Country
	}
	type cand struct {
		name    string
		inland  bool
		listed  bool
		blocked bool
	}
	var cs []cand
	for i := range a.in.Nodes {
		n := &a.in.Nodes[i]
		if n.ID == exit.ID || n.Status != "online" {
			continue
		}
		cc := a.in.Countries[n.ID]
		if cc == nil || cc.Verdict != "ok" {
			continue
		}
		if d := a.diags[n.ID]; d != nil && hasHardClass(d) {
			continue
		}
		vs := summarizeVantages(cc.Vantages)
		if c.Class == clsWhitelist && vs.mobileReach == 0 {
			continue
		}
		skip := false
		for _, op := range darkOps {
			if has(vs.darkOps, op) {
				skip = true
			}
		}
		if skip {
			continue
		}
		cs = append(cs, cand{name: n.Name, inland: country != "" && strings.EqualFold(n.Country, country), listed: len(cc.ListedBy) > 0})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].inland != cs[j].inland {
			return cs[i].inland
		}
		if cs[i].listed != cs[j].listed {
			return !cs[i].listed
		}
		return cs[i].name < cs[j].name
	})
	var out []string
	for _, c := range cs {
		if len(out) == 2 {
			break
		}
		out = append(out, nm(c.name))
	}
	return out
}

func (a *accessCtx) anyCDNWorks() bool {
	for _, x := range a.in.Inbounds {
		if x.CDN && x.Enabled && x.State != "failed" && x.State != "disabled" {
			return true
		}
	}
	return false
}

func (a *accessCtx) recommendations(findings []accessFindingV) []accessRecV {
	in := a.in
	var recs []accessRecV
	seen := map[string]bool{}
	add := func(r accessRecV) {
		r.key = r.Kind + "|" + r.Target["node"] + "|" + r.Target["inbound_id"] + "|" + r.Target["group_id"] + "|" + r.Target["group"] + "|" + r.Target["entry_node"] + "|" + r.Target["to_port"]
		if seen[r.key] {
			return
		}
		seen[r.key] = true
		r.Why = sortedUnique(r.Why)
		recs = append(recs, r)
	}
	nodeIDs := make([]string, 0, len(a.inScope))
	for id := range a.inScope {
		nodeIDs = append(nodeIDs, id)
	}
	slices.SortFunc(nodeIDs, func(p, q string) int { return strings.Compare(a.byID[p].Name, a.byID[q].Name) })
	for _, nid := range nodeIDs {
		n := a.byID[nid]
		d := a.diags[nid]
		cc := in.Countries[nid]
		for _, c := range d.Classes {
			scoped := a.idsOf(nid, func(x *accInbound) bool { return x.Enabled && x.State != "disabled" && classScopeMatch(c, x) })
			aff := a.affectedBy(scoped...)
			sev := sevRank(sevFor(c))
			base := func(kind string) accessRecV {
				return accessRecV{Kind: kind, Target: map[string]string{"node": nm(n.Name)}, Why: slices.Clone(c.Evidence), Affected: aff, Conf: c.Confidence, sev: sev}
			}
			firstIn := ""
			if len(scoped) > 0 {
				firstIn = scoped[0]
			}
			switch c.Class {
			case clsIPBlocked, clsWhitelist, clsOperator, clsRegional:
				if c.Confidence == confLow && c.Class == clsIPBlocked {
					r := base("investigate")
					r.NextTool = "checks_results"
					r.Effect = "Confirm the block with a live check before changing anything: the address is only in a block list."
					add(r)
					continue
				}
				if a.hasHealthyRelayFor(nid) {
					continue
				}
				entries := a.entryFor(n, c)
				if len(entries) > 0 {
					r := base("add_relay")
					r.Target["entry_node"] = strings.Join(entries, ",")
					if firstIn != "" {
						r.Target["inbound_id"] = firstIn
					}
					r.PlanTool = "relay_plan"
					r.Effect = "People of the dark networks reach the entry and the entry passes their traffic to this node's server; the servers they use today do not change."
					if c.Class == clsWhitelist {
						r.Effect = "Only an address on the whitelist helps: the entry must be one known to pass mobile networks. " + r.Effect
					}
					add(r)
				}
				if c.Class == clsIPBlocked {
					if !a.anyCDNWorks() {
						r := base("add_cdn_profile")
						r.PlanTool = "profile_create_plan"
						r.Effect = "A CDN-fronted server is reached through the CDN's addresses, not this node's blocked one."
						add(r)
					}
					r := base("new_node_or_ip")
					r.PlanTool = "node_install_plan"
					r.Effect = "A new address or node outside the block; changing ports on the blocked address does not help."
					add(r)
				}
				if c.Class == clsWhitelist {
					r := base("check_entry_on_mobile")
					r.NextTool = "checks_results"
					r.Effect = "Port or protocol changes will not help in whitelist mode; first check that the entry's address is reachable on mobile."
					add(r)
				}
				if cc != nil && cc.IPv6Verdict == "ok" && n.IPv6Supported && !n.IPv6On && c.Class != clsWhitelist {
					r := base("enable_ipv6_entry")
					r.PlanTool = "node_ipv6_plan"
					r.Effect = "The node's IPv6 address is reachable from the country: a second server per protocol over IPv6 for networks that block the IPv4 one."
					add(r)
				}
				if c.Class == clsIPBlocked && c.Confidence != confLow && c.Detail["scope"] == "ip" || c.Class == clsIPBlocked && c.Confidence == confHigh {
					for _, id := range scoped {
						x := a.inboundByID(id)
						if x == nil || !x.Direct || !a.otherWayExists(nid, id) {
							continue
						}
						r := base("remove_from_subscriptions")
						r.Target["inbound_id"] = id
						r.PlanTool = "node_update_plan"
						r.Effect = "People stop getting a server that fails for them; the other servers of their groups stay."
						add(r)
						break
					}
				}
			case clsPortBlocked:
				open := []uint32{}
				if cc != nil {
					open, _, _ = tspuIndex(cc.TSPU)
				}
				profileID := ""
				if firstIn != "" {
					if x := a.inboundByID(firstIn); x != nil {
						profileID = x.ProfileID
					}
				}
				to := a.freeProfilePort(profileID, netOf(c.protocol), c.port, open)
				if to != 0 {
					r := base("move_port")
					if firstIn != "" {
						r.Target["inbound_id"] = firstIn
					}
					r.Target["from_port"] = u32(c.port)
					r.Target["to_port"] = u32(to)
					r.Target["open_ports"] = u32(to)
					r.PlanTool = "profile_update_plan"
					r.Effect = "The server listens on a port the TSPU probe sees open on the same address; people reconnect after their next subscription refresh."
					add(r)
				}
			case clsTCPThrottled:
				if alt := a.workingProtocol(nid, "udp"); alt != "" {
					for _, r := range a.preferProtocolRecs(base("prefer_protocol"), nid, scoped, c.protocol, alt) {
						r.Effect = "The group keeps its other profiles and uses the working " + alt + " profile; TCP is throttled after a few KB, so a port change does not help."
						add(r)
					}
				}
				if !a.anyCDNWorks() {
					r := base("add_cdn_profile")
					r.PlanTool = "profile_create_plan"
					r.Effect = "VLESS over XHTTP behind a CDN avoids the freezing of a direct TCP stream."
					add(r)
				}
				if c.protocol == "vless" && a.profileHas(scoped, "reality") {
					r := base("change_reality_sni")
					if firstIn != "" {
						r.Target["inbound_id"] = firstIn
					}
					r.PlanTool = "profile_update_plan"
					r.Effect = "REALITY with a different, plausible server name may stop the DPI from matching the stream."
					add(r)
				}
			case clsUDPBlocked:
				r := base("prefer_tcp_protocol")
				r.PlanTool = "profile_create_plan"
				r.Effect = "A VLESS server over TCP or XHTTP on 443 works where UDP is cut."
				add(r)
				for _, id := range scoped {
					x := a.inboundByID(id)
					if x == nil || !x.Direct || !isUDPProto(x.Protocol) || !a.usersHaveWorkingAlternate([]string{id}, func(other *accInbound) bool {
						return other.NodeID == nid && netOf(other.Protocol) == "tcp"
					}) {
						continue
					}
					r := base("remove_from_subscriptions")
					r.Target["inbound_id"] = id
					r.PlanTool = "node_update_plan"
					r.Effect = "People stop getting UDP servers that cannot connect from this country; their working TCP servers stay."
					add(r)
					break
				}
			case clsFingerprinted:
				r := base("change_obfuscation")
				if firstIn != "" {
					r.Target["inbound_id"] = firstIn
				}
				r.PlanTool = "profile_update_plan"
				r.Effect = "A different obfuscation (AmneziaWG parameters, Hysteria2 obfs) may stop the DPI from recognising the protocol; the signal is weak, so try one group first."
				add(r)
				if alt := a.workingProtocol(nid, ""); alt != "" && alt != c.protocol {
					for _, r := range a.preferProtocolRecs(base("prefer_protocol"), nid, scoped, c.protocol, alt) {
						r.Effect = "The group keeps its other profiles and uses the working " + alt + " profile."
						add(r)
					}
				}
			case clsBroken:
				r := base("fix_ours")
				if strings.HasPrefix(c.Scope, "inbound:") {
					r.Target["inbound_id"] = c.inbound
				}
				r.PlanTool = "node_fix_plan"
				r.NextTool = "node_doctor"
				r.Effect = "The node or its server is broken; fixing it needs no change in what people are given."
				add(r)
			case clsUnknown:
				r := base("investigate")
				r.NextTool = "path_probe"
				r.Effect = "Enable a country check (Globalping, TSPU probe) or run path_probe udp from an in-country node to learn how the node looks from inside."
				add(r)
			}
		}
	}
	// findings that carry no class
	for _, f := range findings {
		switch f.Code {
		case "relay_down":
			add(accessRecV{Kind: "fix_ours", Target: map[string]string{"node": f.Detail["entry"], "relay": strings.TrimPrefix(firstWith(f.Evidence, "relay:"), "relay:")}, Why: f.Evidence, Affected: f.Affected,
				NextTool: "relay_check", PlanTool: "relay_plan", Effect: "The relay is not forwarding: check it, then switch it off or move it; people using it fall back to their other servers.", sev: 3})
		case "udp_lossy_path", "awg_handshake_no_reply":
			add(accessRecV{Kind: "investigate", Target: map[string]string{"node": f.Node, "finding": f.Code}, Why: f.Evidence, Affected: f.Affected,
				NextTool: "path_probe", Effect: "Tell a lossy or filtered UDP path from a bad node: path_probe udp from an in-country node, then node_trace.", sev: 2})
		case "group_without_working_way":
			g := f.Detail["group"]
			if g == "" {
				continue
			}
			if group, profileID := a.groupAltProfile(g); group != nil && profileID != "" {
				profileIDs := append(slices.Clone(group.ProfileIDs), profileID)
				profileIDs = sortedUnique(profileIDs)
				add(accessRecV{Kind: "prefer_protocol", Target: map[string]string{"group_id": group.ID, "profile_ids": strings.Join(profileIDs, ",")}, Why: f.Evidence, Affected: f.Affected, PlanTool: "group_update_plan",
					Effect: "The group adds a profile that has a working server, so its people have a way in again.", sev: 3, Conf: confMedium})
			}
		}
	}
	for i := range recs {
		recs[i].sev = max(recs[i].sev, 1)
	}
	sort.SliceStable(recs, func(i, j int) bool {
		p, q := recs[i], recs[j]
		if p.Affected.Users != q.Affected.Users {
			return p.Affected.Users > q.Affected.Users
		}
		if p.sev != q.sev {
			return p.sev > q.sev
		}
		return p.key < q.key
	})
	if len(recs) > maxRecs {
		recs = recs[:maxRecs]
	}
	for i := range recs {
		recs[i].Rank = i + 1
	}
	return recs
}

func firstWith(list []string, prefix string) string {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return s
		}
	}
	return ""
}

// otherWayExists: every user of this inbound has a working inbound on another node.
func (a *accessCtx) otherWayExists(nid, inboundID string) bool {
	return a.usersHaveWorkingAlternate([]string{inboundID}, func(x *accInbound) bool { return x.NodeID != nid })
}

func (a *accessCtx) usersHaveWorkingAlternate(ids []string, eligible func(*accInbound) bool) bool {
	affected := map[string]bool{}
	for _, id := range ids {
		for _, u := range a.given[id] {
			affected[u.ID] = true
		}
	}
	if len(affected) == 0 {
		return false
	}
	for userID := range affected {
		found := false
		for i := range a.in.Inbounds {
			x := &a.in.Inbounds[i]
			if !eligible(x) || !a.ways[x.ID].works() {
				continue
			}
			for _, u := range a.given[x.ID] {
				if u.ID == userID {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (a *accessCtx) freeProfilePort(profileID, network string, from uint32, candidates []uint32) uint32 {
	if profileID == "" {
		return 0
	}
	for _, missing := range a.in.Unavailable {
		if missing == "nodes_beyond_cap" || strings.HasPrefix(missing, "node:") {
			return 0
		}
	}
	placements := map[string]bool{}
	for _, x := range a.in.Inbounds {
		if x.ProfileID == profileID && x.Enabled {
			placements[x.NodeID] = true
		}
	}
	if len(placements) == 0 {
		return 0
	}
	var free []uint32
	for _, port := range candidates {
		if port == 0 || port == from {
			continue
		}
		occupied := false
		for _, x := range a.in.Inbounds {
			if placements[x.NodeID] && x.ProfileID != profileID && netOf(x.Protocol) == network && x.Port == port {
				occupied = true
				break
			}
		}
		if !occupied {
			free = append(free, port)
		}
	}
	if slices.Contains(free, 443) {
		return 443
	}
	if len(free) > 0 {
		return free[0]
	}
	return 0
}

func (a *accessCtx) preferProtocolRecs(base accessRecV, nodeID string, inboundIDs []string, failed, alternative string) []accessRecV {
	groups := map[string]bool{}
	for _, id := range inboundIDs {
		for _, u := range a.given[id] {
			groups[u.GroupID] = true
		}
	}
	groupIDs := make([]string, 0, len(groups))
	for id := range groups {
		groupIDs = append(groupIDs, id)
	}
	slices.Sort(groupIDs)
	var out []accessRecV
	for _, id := range groupIDs {
		g := a.groupsByID[id]
		if g == nil {
			continue
		}
		profiles := map[string]bool{}
		for _, profileID := range g.ProfileIDs {
			if normProtocol(a.in.Profiles[profileID].Protocol) != failed {
				profiles[profileID] = true
			}
		}
		for _, x := range a.in.Inbounds {
			if x.NodeID == nodeID && x.Enabled && x.State != "disabled" && x.State != "failed" && x.Direct &&
				normProtocol(x.Protocol) == alternative && a.ways[x.ID].works() {
				profiles[x.ProfileID] = true
			}
		}
		profileIDs := make([]string, 0, len(profiles))
		for profileID := range profiles {
			profileIDs = append(profileIDs, profileID)
		}
		profileIDs = sortedUnique(profileIDs)
		current := sortedUnique(slices.Clone(g.ProfileIDs))
		if len(profileIDs) == 0 || slices.Equal(profileIDs, current) {
			continue
		}
		r := base
		r.Target = make(map[string]string, len(base.Target)+2)
		for key, value := range base.Target {
			r.Target[key] = value
		}
		r.Target["group_id"] = g.ID
		r.Target["profile_ids"] = strings.Join(profileIDs, ",")
		r.Target["protocol"] = alternative
		r.PlanTool = "group_update_plan"
		users := map[string]bool{}
		for _, inboundID := range inboundIDs {
			for _, u := range a.given[inboundID] {
				if u.GroupID == id {
					users[u.ID] = true
				}
			}
		}
		r.Affected = affectedV{Users: len(users), Groups: []string{nm(g.Name)}}
		out = append(out, r)
	}
	return out
}

// workingProtocol names a protocol of the node whose way works ("udp" or "tcp" narrows by network).
func (a *accessCtx) workingProtocol(nid, network string) string {
	var names []string
	for _, x := range a.inboundsOf(nid) {
		if a.ways[x.ID].works() && (network == "" || netOf(x.Protocol) == network) {
			names = append(names, normProtocol(x.Protocol))
		}
	}
	slices.Sort(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func (a *accessCtx) profileHas(ids []string, word string) bool {
	for _, id := range ids {
		x := a.inboundByID(id)
		if x != nil && strings.Contains(strings.ToLower(a.in.Profiles[x.ProfileID].Summary), word) {
			return true
		}
	}
	return false
}

// groupAltProfile names a profile outside the group that has a working direct server.
func (a *accessCtx) groupAltProfile(groupName string) (*accGroup, string) {
	var g *accGroup
	for _, x := range a.groupsByID {
		if nm(x.Name) == groupName {
			g = x
		}
	}
	if g == nil {
		return nil, ""
	}
	var ids []string
	for _, x := range a.in.Inbounds {
		if !slices.Contains(g.ProfileIDs, x.ProfileID) && a.ways[x.ID].works() && x.Direct {
			ids = append(ids, x.ProfileID)
		}
	}
	ids = sortedUnique(ids)
	if len(ids) > 0 {
		return g, ids[0]
	}
	return g, ""
}
