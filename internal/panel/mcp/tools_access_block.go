//go:build !js

package mcp

import (
	"slices"
	"strconv"
	"strings"
)

// The block classifier of block_diagnosis and access_report. It is a pure function of stored facts (accessInput): every class
// is derived by a fixed rule from country checks, TSPU ports, vantages, kl!ck reports, port checks, path loss and the link
// counters, so the same facts always give the same answer. A signal that is missing is never guessed: the class says what is
// unknown and which check would tell.

// Classes of block_diagnosis.
const (
	clsIPBlocked      = "ip_blocked"
	clsPortBlocked    = "port_blocked"
	clsTCPThrottled   = "tcp_throttled"
	clsUDPBlocked     = "udp_blocked"
	clsFingerprinted  = "protocol_fingerprinted"
	clsWhitelist      = "whitelist_mode"
	clsOperator       = "operator_specific"
	clsRegional       = "regional"
	clsBroken         = "not_blocked_but_broken"
	clsUnknown        = "unknown"
	confHigh          = "high"
	confMedium        = "medium"
	confLow           = "low"
	minFleetAttempts  = 3
	minHellosNoHS     = 10
	lossBlockedPct    = 90
	stateFailing      = "failing"
	stateWorking      = "working"
	statePartial      = "partial"
	stateUnknown      = "unknown"
	stateBroken       = "broken"
	klickMinReports   = 2
	klickBadShareNum  = 3 // 3/5 of the reports
	klickBadShareDen  = 5
	throttleMinStalls = 2
)

// ---------------------------------------------------------------------------------------------------------------------
// input

type accNode struct {
	ID, Name, Country, Status string
	IPv6Supported, IPv6On     bool
	NoCounters                bool
	UDPChecks                 []accUDPCheck
	TCPChecks                 []accTCPCheck
}

type accUDPCheck struct {
	Port    uint32
	Verdict string // ok, lossy, broken, ""
	Sender  string // "panel" or a node name
}

type accTCPCheck struct {
	Port    uint32
	Verdict string // ours, other, refused, timeout, busy
	Sender  string
}

type accInbound struct {
	ID, NodeID, ProfileID, Protocol, Egress string
	Port                                    uint32
	Enabled, Direct, CDN                    bool
	State                                   string // active, pending, failed, disabled
	LastErrorCode                           string
}

type accProfile struct{ ID, Name, Protocol, Summary string }

type accGroup struct {
	ID, Name      string
	ProfileIDs    []string
	DisabledNodes []string
}

type accUser struct {
	ID, GroupID string
	AllNodes    bool
	NodeIDs     []string
	LastSeen    int64
}

// accRow is one device on one inbound over the window (one row of GetUserConnection).
type accRow struct {
	UserID, DeviceID, NodeID, InboundID string
	Connects, Rejects, Drops, Stalls    uint32
	Freezes, Quiet, ClientCloses        uint32
	OnlineS                             uint64
	RttMs, RttSamples                   uint32
	LossPermille                        uint32
	PktsSent                            uint64
}

// accTotals is an inbound with all its keys (GetFleetConnections).
type accTotals struct {
	Protocol                                    string
	Connects, Rejects, Drops, Stalls            uint32
	Freezes, Quiet, ClientCloses                uint32
	Hellos, Handshakes, Accepted, HellosForeign uint32
	UnknownKeys, KeysStalled, KeysTried         uint32
	OnlineS                                     uint64
	RttMs, RttSamples                           uint32
	LossPermille                                uint32
	PktsSent                                    uint64
	Unsupported                                 []string
}

type accTSPUPort struct {
	Port    uint32
	Verdict string // blocked, open, refused, timeout
}

type accVantage struct {
	Network, Region, Kind, State string // kind home, mobile, dc, unknown; state reach, dark, whitelist, uncertain
	ICMPOK, TCPOK                bool
}

type accProbePort struct {
	Port   uint32
	OK     bool
	Probes int // valid eyeball probes that tried
	Fail   int
}

type accCountry struct {
	NodeID, Country, Verdict, Confidence string
	Sources, ListedBy                    []string
	TSPU                                 []accTSPUPort
	Vantages                             []accVantage
	ProbePorts                           []accProbePort
	WhitelistSource                      bool // a source verdict reason names a whitelist (bsbord, cheburcheck)
	IPv6Verdict                          string
	AtUnix                               int64
}

type accKlickTally struct{ Reports, OK, Freeze, TCPBlocked, HandshakeFailed, NoAnswer uint32 }

type accKlickGroup struct {
	Name string
	accKlickTally
}

type accKlickKind struct {
	Kind string
	accKlickTally
	MedianDelayMs, DLChecked, DLStalled uint32
}

type accKlick struct {
	accKlickTally
	Operators []accKlickGroup
	Regions   []accKlickGroup
	Kinds     []accKlickKind
}

type accRelay struct {
	ID, EntryID, ExitID, InboundID, ProfileID, State string
	Enabled, ExitLive, AgentSupported                bool
	Checked, CheckOK                                 bool
	MonitorFails                                     uint32
}

type accLoss struct {
	FromID, FromName, ToID, ToName string
	LossPct                        float64
}

type accessInput struct {
	NowUnix, SinceUnix int64
	Hours              int
	Country, Group     string // filters (upper-case country, group id)
	Node, Protocol     string // filters (node id, normalised protocol)
	Nodes              []accNode
	Inbounds           []accInbound
	Profiles           map[string]accProfile
	Groups             []accGroup
	Users              []accUser
	UsersTotal         int
	Rows               []accRow
	Totals             map[string]accTotals // by inbound id
	Countries          map[string]*accCountry
	Klick              map[string]*accKlick
	KlickEnabled       bool
	Relays             []accRelay
	Loss               []accLoss
	UDPLossNodes       map[string]bool
	TSPURunStatus      string // "" when the probe is off or never ran
	Unavailable        []string
}

// ---------------------------------------------------------------------------------------------------------------------
// output

type blockClassV struct {
	Class      string            `json:"class"`
	Scope      string            `json:"scope"` // node, port:8443/tcp, protocol:awg, inbound:<id>
	Confidence string            `json:"confidence"`
	Evidence   []string          `json:"evidence"`
	Operators  []string          `json:"operators,omitempty"`
	Regions    []string          `json:"regions,omitempty"`
	Detail     map[string]string `json:"detail,omitempty"`
	Unknown    []string          `json:"unknown,omitempty"`
	Explain    string            `json:"explain"`
	Next       []nextStepV       `json:"next,omitempty"`
	node       string
	port       uint32
	protocol   string
	inbound    string
	hard       bool // a block of the way itself (not only some operators)
}

type nextStepV struct {
	Kind     string `json:"kind"`
	PlanTool string `json:"plan_tool,omitempty"`
	Note     string `json:"note"`
}

type blockNodeV struct {
	NodeID     string        `json:"node_id"`
	Node       string        `json:"node"`
	Country    string        `json:"country,omitempty"`
	Status     string        `json:"status"`
	Verdict    string        `json:"country_verdict,omitempty"`
	Confidence string        `json:"country_confidence,omitempty"`
	OpenPorts  []uint32      `json:"tspu_open_ports,omitempty"`
	BadPorts   []uint32      `json:"tspu_blocked_ports,omitempty"`
	Classes    []blockClassV `json:"classes"`
}

// wayV is the state of one inbound, as the classes of its node and its own counters say it.
type wayV struct {
	State     string
	BlockedBy []string // class names and signals
	WorksBy   []string
	Soft      bool // only some operators or regions
}

// ---------------------------------------------------------------------------------------------------------------------
// helpers

func normProtocol(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "hy2", "hysteria", "hysteria2":
		return "hysteria2"
	case "wg", "amneziawg":
		return "awg"
	}
	return p
}

func isUDPProto(p string) bool { p = normProtocol(p); return p == "hysteria2" || p == "awg" }

func netOf(p string) string {
	if isUDPProto(p) {
		return "udp"
	}
	return "tcp"
}

func u32(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

func portScope(port uint32, network string) string { return "port:" + u32(port) + "/" + network }

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func has(list []string, s string) bool { return slices.Contains(list, s) }

func (t accTotals) unsupportedHas(f string) bool { return has(t.Unsupported, f) }

// health reads an inbound's own counters (all keys together): working, failing or unknown. VLESS keeps no connect counter,
// only online_s.
func (t accTotals) health(proto string) string {
	switch normProtocol(proto) {
	case "vless":
		if t.OnlineS > 0 {
			return stateWorking
		}
		return stateUnknown
	case "awg":
		if t.Connects >= minFleetAttempts && t.Stalls*2 <= t.Connects {
			return stateWorking
		}
		if t.Stalls >= minFleetAttempts && t.Connects == 0 {
			return stateFailing
		}
	case "hysteria2":
		att := t.Connects + t.Rejects + t.Freezes
		if t.Connects >= minFleetAttempts && t.Freezes*5 <= t.Connects {
			return stateWorking
		}
		if att >= minFleetAttempts && t.Connects*10 < att*3 {
			return stateFailing
		}
		if !t.unsupportedHas("accepted") && t.Hellos > t.HellosForeign && t.Hellos-t.HellosForeign >= minHellosNoHS && t.Accepted == 0 {
			return stateFailing
		}
	}
	return stateUnknown
}

type vantSum struct {
	darkOps, reachOps, mobileDarkOps []string
	darkRegions, reachRegions        []string
	mobileDark, mobileReach          int
	fixedDark, fixedReach            int
	whiteMobile, whiteFixed          int
	darkE, reachE, darkNoICMP        int
	mobileKnown                      bool
}

func summarizeVantages(vs []accVantage) vantSum {
	var s vantSum
	for _, v := range vs {
		if v.Kind == "dc" {
			continue
		}
		mobile := v.Kind == "mobile"
		if mobile {
			s.mobileKnown = true
		}
		switch v.State {
		case "reach":
			s.reachE++
			s.reachOps = append(s.reachOps, v.Network)
			if v.Region != "" {
				s.reachRegions = append(s.reachRegions, v.Region)
			}
			if mobile {
				s.mobileReach++
			} else {
				s.fixedReach++
			}
		case "dark", "whitelist":
			s.darkE++
			s.darkOps = append(s.darkOps, v.Network)
			if v.Region != "" {
				s.darkRegions = append(s.darkRegions, v.Region)
			}
			if !v.ICMPOK {
				s.darkNoICMP++
			}
			if mobile {
				s.mobileDark++
				s.mobileDarkOps = append(s.mobileDarkOps, v.Network)
				if v.State == "whitelist" {
					s.whiteMobile++
				}
			} else {
				s.fixedDark++
				if v.State == "whitelist" {
					s.whiteFixed++
				}
			}
		}
	}
	s.darkOps, s.reachOps, s.mobileDarkOps = sortedUnique(s.darkOps), sortedUnique(s.reachOps), sortedUnique(s.mobileDarkOps)
	s.darkRegions, s.reachRegions = sortedUnique(s.darkRegions), sortedUnique(s.reachRegions)
	return s
}

func (k *accKlick) kind(proto string) *accKlickKind {
	if k == nil {
		return nil
	}
	for i := range k.Kinds {
		if normProtocol(k.Kinds[i].Kind) == normProtocol(proto) {
			return &k.Kinds[i]
		}
	}
	return nil
}

func (t accKlickTally) bad() uint32 { return t.Reports - min(t.OK, t.Reports) }

func (t accKlickTally) mostlyBad() bool {
	return t.Reports >= klickMinReports && t.bad()*klickBadShareDen >= t.Reports*klickBadShareNum
}

func (t accKlickTally) mostlyOK() bool {
	return t.Reports >= klickMinReports && t.OK*klickBadShareDen >= t.Reports*klickBadShareNum
}

func tspuIndex(ports []accTSPUPort) (open, blocked, timeout []uint32, all bool) {
	for _, p := range ports {
		switch p.Verdict {
		case "open":
			open = append(open, p.Port)
		case "blocked":
			blocked = append(blocked, p.Port)
		case "timeout":
			timeout = append(timeout, p.Port)
		}
	}
	slices.Sort(open)
	slices.Sort(blocked)
	slices.Sort(timeout)
	all = len(open) == 0 && len(blocked)+len(timeout) > 0 && len(blocked) > 0
	return
}

func portList(p []uint32) string {
	s := make([]string, len(p))
	for i, x := range p {
		s[i] = u32(x)
	}
	return strings.Join(s, ",")
}

func nodeIsOurs(status string) bool {
	return status == "down" || status == "no_traffic" || status == "pending"
}

// ---------------------------------------------------------------------------------------------------------------------
// the classifier

type blockCtx struct {
	in       *accessInput
	byID     map[string]*accNode
	byName   map[string]*accNode
	inbounds map[string][]*accInbound // by node
}

func newBlockCtx(in *accessInput) *blockCtx {
	b := &blockCtx{in: in, byID: map[string]*accNode{}, byName: map[string]*accNode{}, inbounds: map[string][]*accInbound{}}
	for i := range in.Nodes {
		n := &in.Nodes[i]
		b.byID[n.ID], b.byName[n.Name] = n, n
	}
	for i := range in.Inbounds {
		x := &in.Inbounds[i]
		b.inbounds[x.NodeID] = append(b.inbounds[x.NodeID], x)
	}
	return b
}

func (b *blockCtx) inCountry(sender, country string) bool {
	n := b.byName[sender]
	return n != nil && country != "" && strings.EqualFold(n.Country, country)
}

// diagnose classifies one node. The result is sorted: hard classes first, then by confidence and class name.
func (b *blockCtx) diagnose(n *accNode) blockNodeV {
	in := b.in
	cc := in.Countries[n.ID]
	km := in.Klick[n.ID]
	out := blockNodeV{NodeID: n.ID, Node: n.Name, Country: strings.ToUpper(n.Country), Status: n.Status, Classes: []blockClassV{}}
	add := func(c blockClassV) {
		c.node = n.ID
		c.Evidence = sortedUnique(c.Evidence)
		c.Operators = sortedUnique(c.Operators)
		c.Regions = sortedUnique(c.Regions)
		c.Next = classNext(c)
		out.Classes = append(out.Classes, c)
	}
	var vs vantSum
	var open, blocked, timeout []uint32
	var tspuAll bool
	country := strings.ToUpper(n.Country)
	if cc != nil {
		out.Country, out.Verdict, out.Confidence = cc.Country, cc.Verdict, cc.Confidence
		country = cc.Country
		vs = summarizeVantages(cc.Vantages)
		open, blocked, timeout, tspuAll = tspuIndex(cc.TSPU)
		out.OpenPorts, out.BadPorts = open, append(slices.Clone(blocked), timeout...)
		slices.Sort(out.BadPorts)
	}

	// Ours first: a node that is down or an inbound that failed is not the censor's work, and every dark vantage of a down
	// node is a consequence, not evidence.
	if nodeIsOurs(n.Status) {
		add(blockClassV{Class: clsBroken, Scope: "node", Confidence: confHigh, hard: true,
			Evidence: []string{"node:" + n.ID}, Detail: map[string]string{"status": n.Status},
			Explain: "The node itself is " + n.Status + ": nothing here points at a censor. Fix the node first."})
		sortClasses(&out)
		return out
	}
	broken := map[uint32]bool{}
	for _, x := range b.inbounds[n.ID] {
		if !x.Enabled || x.State == "disabled" {
			continue
		}
		if x.State == "failed" {
			broken[x.Port] = true
			add(blockClassV{Class: clsBroken, Scope: "inbound:" + x.ID, Confidence: confHigh, hard: true, inbound: x.ID, port: x.Port, protocol: x.Protocol,
				Evidence: []string{"inbound:" + x.ID}, Detail: map[string]string{"state": "failed", "error": x.LastErrorCode},
				Explain: "The server " + x.Protocol + " on port " + u32(x.Port) + " failed to start or apply on the node (" + clean(x.LastErrorCode, 40) + "): an engine or host problem, not a block."})
		}
	}
	for _, t := range n.TCPChecks {
		if t.Verdict == "refused" && !b.inCountry(t.Sender, country) && !broken[t.Port] {
			broken[t.Port] = true
			add(blockClassV{Class: clsBroken, Scope: portScope(t.Port, "tcp"), Confidence: confMedium, hard: true, port: t.Port,
				Evidence: []string{"port:" + n.ID + ":" + u32(t.Port)}, Detail: map[string]string{"tcp_check": "refused"},
				Explain: "A node outside the country got a refusal on tcp/" + u32(t.Port) + ": the port is closed on the host, which is not a block."})
		}
	}

	if cc == nil {
		var unk []string
		unk = append(unk, "country check is not configured or has no round for this node: enable Globalping or the TSPU probe, or the kl!ck source")
		if km == nil {
			if in.KlickEnabled {
				unk = append(unk, "no kl!ck report names this node yet")
			}
		}
		if len(out.Classes) == 0 {
			add(blockClassV{Class: clsUnknown, Scope: "node", Confidence: confLow, Unknown: unk,
				Explain: "No view from inside the country exists for this node, so a block can be neither shown nor ruled out."})
		}
		b.addCounterClasses(n, nil, km, vs, false, &out, add, broken)
		sortClasses(&out)
		return out
	}

	ev := []string{"check:" + n.ID}
	verdictBlocked := cc.Verdict == "blocked" || cc.Verdict == "tcp_blocked"
	listed := len(cc.ListedBy) > 0 || cc.Verdict == "listed"
	var unk []string
	if len(cc.TSPU) == 0 {
		if in.TSPURunStatus == "" {
			unk = append(unk, "TSPU probe is not configured: ports are not judged one by one")
		} else {
			unk = append(unk, "TSPU probe has no verdict for this node (last run "+in.TSPURunStatus+")")
		}
	}
	if !vs.mobileKnown {
		unk = append(unk, "no mobile vantage: whitelist mode cannot be told from other blocks")
	}

	// whitelist mode: dark on mobile only, fixed lines reach the node.
	whitelist := false
	if vs.fixedReach > 0 && vs.fixedDark == 0 && vs.mobileReach == 0 && vs.mobileDark > 0 &&
		(len(vs.mobileDarkOps) >= 2 || vs.whiteMobile > 0 || cc.WhitelistSource) {
		whitelist = true
		conf := confMedium
		if vs.whiteMobile > 0 || cc.WhitelistSource {
			conf = confHigh
		}
		u := []string{"run the same check against an unrelated foreign address from a mobile network: if it is dark too, the whitelist is on, not this node"}
		add(blockClassV{Class: clsWhitelist, Scope: "node", Confidence: conf, Operators: vs.mobileDarkOps, Evidence: ev, Unknown: u,
			Detail: map[string]string{"mobile_dark": strconv.Itoa(vs.mobileDark), "fixed_reach": strconv.Itoa(vs.fixedReach)},
			Explain: "Mobile networks cannot reach the node while fixed-line networks can: the operators allow only listed addresses. " +
				"Changing the port or the protocol will not help; only an address that is on the whitelist does."})
	}

	// ip blocked: every TCP port dark (TSPU) or the verdict says nothing gets through, and no eyeball network reaches.
	ipBlocked := false
	if !whitelist && vs.reachE == 0 && ((verdictBlocked && (vs.darkE >= 1 || (len(cc.Vantages) == 0 && cc.Confidence != "listed"))) || tspuAll) {
		ipBlocked = true
		conf := confMedium
		switch {
		case verdictBlocked && vs.darkE >= 2 && vs.fixedDark >= 1 && (tspuAll || listed):
			conf = confHigh
		case verdictBlocked && cc.Confidence == "dc_only" && !tspuAll:
			conf = confLow
		}
		scope := "ip"
		if cc.Verdict == "tcp_blocked" || (vs.darkE > 0 && vs.darkNoICMP == 0) {
			scope = "tcp_only"
		}
		e := slices.Clone(ev)
		d := map[string]string{"verdict": cc.Verdict, "scope": scope}
		if tspuAll {
			e = append(e, "tspu:"+n.ID)
			d["tspu_blocked_ports"] = portList(append(slices.Clone(blocked), timeout...))
		}
		if listed {
			d["listed"] = strings.Join(cc.ListedBy, ",")
		}
		ex := "Every TCP port of the address is dark" + map[bool]string{true: " and ICMP too", false: ", while ICMP still passes"}[scope == "ip"] +
			": the address itself is blocked, not one port. Changing ports does not help."
		if scope == "tcp_only" {
			ex += " UDP is not judged here: a UDP protocol may still pass."
			unk = append(unk, "UDP is not tested by the country check: run path_probe udp from an in-country node")
		}
		add(blockClassV{Class: clsIPBlocked, Scope: "node", Confidence: conf, Evidence: e, Operators: vs.darkOps, Regions: vs.darkRegions, Detail: d, hard: true, Unknown: unk, Explain: ex})
	} else if cc.Verdict == "listed" && len(cc.Vantages) == 0 && len(cc.TSPU) == 0 {
		add(blockClassV{Class: clsIPBlocked, Scope: "node", Confidence: confLow, Evidence: ev, Detail: map[string]string{"verdict": "listed", "listed": strings.Join(cc.ListedBy, ",")},
			Unknown: []string{"no live check from inside the country: the address is in a block list, which does not prove it is dropped"},
			Explain: "The address is in a public block list and no live check says otherwise or confirms it."})
		ipBlocked = true
	}

	// port blocked: some ports dark while others of the same address open.
	if !ipBlocked && !whitelist && len(open) > 0 {
		for _, p := range append(slices.Clone(blocked), timeout...) {
			if broken[p] {
				continue
			}
			conf := confMedium
			verdict := "timeout"
			if has(u32s(blocked), u32(p)) {
				verdict = "blocked"
				conf = confHigh
			}
			if verdict == "timeout" {
				conf = confLow
			}
			add(blockClassV{Class: clsPortBlocked, Scope: portScope(p, "tcp"), Confidence: conf, hard: true, port: p,
				Evidence: []string{"tspu:" + n.ID + ":" + u32(p), "check:" + n.ID},
				Detail:   map[string]string{"port": u32(p), "verdict": verdict, "open_ports": portList(open)},
				Explain:  "tcp/" + u32(p) + " is " + verdict + " on the TSPU probe while tcp/" + portList(open) + " of the same address open: the port is cut, not the address. Move the server to an open port."})
		}
	}
	if !ipBlocked && !whitelist && len(open) == 0 {
		// Globalping per port: a port that fails on several eyeball probes while another passes.
		var okPorts []uint32
		for _, p := range cc.ProbePorts {
			if p.OK && p.Probes-p.Fail > 0 {
				okPorts = append(okPorts, p.Port)
			}
		}
		for _, p := range cc.ProbePorts {
			if !p.OK && p.Fail >= 2 && len(okPorts) > 0 && !has(u32s(okPorts), u32(p.Port)) && !broken[p.Port] {
				add(blockClassV{Class: clsPortBlocked, Scope: portScope(p.Port, "tcp"), Confidence: confMedium, hard: true, port: p.Port,
					Evidence: []string{"probe:" + n.ID + ":" + u32(p.Port), "check:" + n.ID},
					Detail:   map[string]string{"port": u32(p.Port), "failed_probes": strconv.Itoa(p.Fail), "open_ports": portList(okPorts)},
					Explain:  "tcp/" + u32(p.Port) + " failed from several home or mobile probes while tcp/" + portList(okPorts) + " answered: a port-level cut seen by Globalping."})
			}
		}
	}

	// operator or regional: some networks dark, others reach.
	if !ipBlocked && !whitelist && vs.darkE > 0 && vs.reachE > 0 {
		regional := len(vs.darkRegions) > 0 && len(vs.reachRegions) > 0
		for _, r := range vs.darkRegions {
			if has(vs.reachRegions, r) {
				regional = false
			}
		}
		if regional {
			regional = false
			// the same operators reach from other regions: it is the region
			for _, op := range vs.darkOps {
				if has(vs.reachOps, op) {
					regional = true
				}
			}
		}
		cls := clsOperator
		if regional {
			cls = clsRegional
		}
		conf := confMedium
		if vs.darkE >= 2 {
			conf = confHigh
		}
		if vs.darkE == 1 {
			conf = confLow
		}
		add(blockClassV{Class: cls, Scope: "node", Confidence: conf, Operators: vs.darkOps, Regions: vs.darkRegions, Evidence: ev,
			Detail:  map[string]string{"dark": strconv.Itoa(vs.darkE), "reach": strconv.Itoa(vs.reachE)},
			Unknown: nil,
			Explain: "The node is dark only for some " + map[bool]string{true: "regions", false: "operators"}[regional] + " while others reach it: the block is local to " +
				strings.Join(append(slices.Clone(vs.darkOps), vs.darkRegions...), ", ") + ". A relay through an entry those people reach helps; other servers of this node may still work for the rest."})
	}
	if km != nil && !whitelist {
		var ops []string
		for _, o := range km.Operators {
			if o.Name != "" && o.mostlyBad() {
				ops = append(ops, o.Name)
			}
		}
		if len(ops) > 0 && km.mostlyOK() == false && !ipBlocked && !has(classNames(out.Classes), clsOperator) && !has(classNames(out.Classes), clsRegional) && km.OK > 0 {
			add(blockClassV{Class: clsOperator, Scope: "node", Confidence: confMedium, Operators: ops, Evidence: []string{"klick:" + n.ID},
				Detail:  map[string]string{"reports": u32(km.Reports), "ok": u32(km.OK)},
				Explain: "kl!ck users on " + strings.Join(ops, ", ") + " mostly see this node fail while other users see it work."})
		}
	}

	b.addCounterClasses(n, cc, km, vs, ipBlocked, &out, add, broken)
	if len(out.Classes) == 0 && len(unk) > 0 && cc.Verdict == "unknown" {
		add(blockClassV{Class: clsUnknown, Scope: "node", Confidence: confLow, Unknown: unk, Evidence: ev,
			Explain: "The country check gave no usable verdict for this node (" + clean(cc.Verdict, 20) + ")."})
	}
	sortClasses(&out)
	return out
}

func u32s(p []uint32) []string {
	s := make([]string, len(p))
	for i, x := range p {
		s[i] = u32(x)
	}
	return s
}

func classNames(cs []blockClassV) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Class)
	}
	return out
}

// addCounterClasses adds what the link counters and kl!ck add on top of the country view: throttling, UDP blocking and a
// fingerprinted protocol.
func (b *blockCtx) addCounterClasses(n *accNode, cc *accCountry, km *accKlick, vs vantSum, ipBlocked bool, out *blockNodeV, add func(blockClassV), broken map[uint32]bool) {
	in := b.in
	var open []uint32
	country := strings.ToUpper(n.Country)
	if cc != nil {
		open, _, _, _ = tspuIndex(cc.TSPU)
		country = cc.Country
	}
	tcpWorks := len(open) > 0
	if cc != nil && (cc.Verdict == "ok" || vs.reachE > 0) {
		tcpWorks = true
	}
	var udpIn, tcpIn []*accInbound
	for _, x := range b.inbounds[n.ID] {
		if !x.Enabled || x.State == "disabled" || x.State == "failed" {
			continue
		}
		if isUDPProto(x.Protocol) {
			udpIn = append(udpIn, x)
		} else {
			tcpIn = append(tcpIn, x)
		}
	}
	// TCP works also when a TCP kind of kl!ck passes.
	for _, k := range kindsOf(km) {
		if !isUDPProto(k.Kind) && k.mostlyOK() {
			tcpWorks = true
		}
	}
	udpWorks := false
	for _, x := range udpIn {
		if in.Totals[x.ID].health(x.Protocol) == stateWorking {
			udpWorks = true
		}
	}
	if k := km.kind("hysteria2"); k != nil && k.mostlyOK() {
		udpWorks = true
	}
	if k := km.kind("awg"); k != nil && k.mostlyOK() {
		udpWorks = true
	}

	// tcp throttled: connects fine, downloads stall (the 16 KB pattern).
	if !ipBlocked {
		for _, k := range kindsOf(km) {
			if isUDPProto(k.Kind) || k.DLChecked < throttleMinStalls || k.DLStalled < throttleMinStalls || k.DLStalled*2 < k.DLChecked {
				continue
			}
			conf := confMedium
			e := []string{"klick:" + n.ID + ":" + k.Kind}
			unk := []string{"VLESS keeps no stall or byte counter per connection: a counter of bytes before the stall would confirm it"}
			d := map[string]string{"dl_checked": u32(k.DLChecked), "dl_stalled": u32(k.DLStalled), "kind": k.Kind}
			if (len(open) > 0 || (cc != nil && cc.Verdict == "ok")) && k.DLStalled >= 3 && k.DLStalled*5 >= k.DLChecked*klickBadShareNum {
				conf = confHigh
				e = append(e, "check:"+n.ID)
				d["tcp_connects"] = "yes"
			}
			if udpWorks {
				d["udp_works"] = "yes"
			}
			add(blockClassV{Class: clsTCPThrottled, Scope: "protocol:" + normProtocol(k.Kind), Confidence: conf, hard: true, protocol: normProtocol(k.Kind), Evidence: e, Detail: d, Unknown: unk,
				Explain: "TCP connects, but a download stalls after a few KB for kl!ck users (" + u32(k.DLStalled) + " of " + u32(k.DLChecked) + "): the DPI freezes the stream. " +
					"A port change does not help; UDP protocols or XHTTP behind a CDN do."})
		}
	}

	// udp blocked and protocol fingerprinted.
	var udpBroken, udpOKIn, udpOKAny int
	var ev []string
	for _, u := range n.UDPChecks {
		in := b.inCountry(u.Sender, country)
		switch {
		case u.Verdict == "broken" && in:
			udpBroken++
			ev = append(ev, "port:"+n.ID+":"+u32(u.Port))
		case u.Verdict == "ok" && in:
			udpOKIn++
		case u.Verdict == "ok":
			udpOKAny++
		}
	}
	for _, l := range in.Loss {
		if l.ToID == n.ID && b.inCountry(l.FromName, country) {
			if l.LossPct >= lossBlockedPct {
				udpBroken++
				ev = append(ev, "loss:"+l.FromID+":"+l.ToID)
			}
		}
	}
	var failingUDP, workingUDP []*accInbound
	for _, x := range udpIn {
		switch in.Totals[x.ID].health(x.Protocol) {
		case stateFailing:
			failingUDP = append(failingUDP, x)
		case stateWorking:
			workingUDP = append(workingUDP, x)
		}
	}
	if !ipBlocked && len(udpIn) > 0 && tcpWorks && len(workingUDP) == 0 && (udpBroken > 0 && udpOKIn == 0 || len(failingUDP) == len(udpIn) && udpOKIn == 0 && udpBroken == 0) {
		conf := confLow
		var unk []string
		for _, x := range failingUDP {
			ev = append(ev, "conn:"+x.ID)
		}
		switch {
		case udpBroken > 0 && len(failingUDP) > 0:
			conf = confHigh
		case udpBroken > 0:
			conf = confHigh
		case len(failingUDP) > 0:
			conf = confMedium
		}
		if udpBroken == 0 {
			unk = append(unk, "no UDP port check from an in-country node: run path_probe udp from one of your nodes in the country")
		}
		add(blockClassV{Class: clsUDPBlocked, Scope: "protocol:udp", Confidence: conf, hard: true, protocol: "udp", Evidence: append(ev, "check:"+n.ID), Unknown: unk,
			Detail:  map[string]string{"udp_inbounds": strconv.Itoa(len(udpIn)), "tcp_works": "yes"},
			Explain: "UDP does not get through (packets from inside the country are lost, or QUIC and WireGuard handshakes never complete) while TCP works: UDP is cut. Use TCP protocols on 443."})
	}
	if len(failingUDP) > 0 && len(workingUDP) > 0 && !ipBlocked {
		for _, x := range failingUDP {
			if broken[x.Port] {
				continue
			}
			conf := confLow
			e := []string{"conn:" + x.ID, "conn:" + workingUDP[0].ID}
			d := map[string]string{"protocol": normProtocol(x.Protocol), "works": normProtocol(workingUDP[0].Protocol)}
			if k := km.kind(x.Protocol); k != nil && k.mostlyBad() {
				conf = confMedium
				e = append(e, "klick:"+n.ID+":"+k.Kind)
			}
			if udpOKIn > 0 || udpOKAny > 0 {
				d["udp_port_check"] = "ok"
			}
			add(blockClassV{Class: clsFingerprinted, Scope: "protocol:" + normProtocol(x.Protocol), Confidence: conf, hard: true, protocol: normProtocol(x.Protocol), inbound: x.ID, Evidence: e, Detail: d,
				Unknown: []string{"per-reason handshake counters (initiations seen, replies sent, replies lost; QUIC alerts) would confirm it: today the counters only say handshakes did not complete"},
				Explain: normProtocol(x.Protocol) + " fails while " + normProtocol(workingUDP[0].Protocol) + " works on the same node and UDP is not blocked as a whole: the DPI may recognise this protocol. The signal is weak."})
		}
	}
}

func kindsOf(k *accKlick) []accKlickKind {
	if k == nil {
		return nil
	}
	return k.Kinds
}

func sortClasses(n *blockNodeV) {
	rank := map[string]int{confHigh: 0, confMedium: 1, confLow: 2}
	slices.SortStableFunc(n.Classes, func(a, b blockClassV) int {
		if a.hard != b.hard {
			if a.hard {
				return -1
			}
			return 1
		}
		if rank[a.Confidence] != rank[b.Confidence] {
			return rank[a.Confidence] - rank[b.Confidence]
		}
		return strings.Compare(a.Class+a.Scope, b.Class+b.Scope)
	})
}

// classNext maps a class to the recommendations that fit it, with the tool that makes each.
func classNext(c blockClassV) []nextStepV {
	switch c.Class {
	case clsIPBlocked:
		return []nextStepV{
			{"add_relay", "relay_plan", "a relay through an entry node that people reach (prefer one inside the country whose address is not listed)"},
			{"add_cdn_profile", "profile_create_plan", "a CDN-fronted profile"},
			{"new_node_or_ip", "node_install_plan", "a new node or a new address; changing ports does not help"},
		}
	case clsPortBlocked:
		return []nextStepV{{"move_port", "profile_update_plan", "move the server to port 443 or another port the TSPU probe sees open"}}
	case clsTCPThrottled:
		return []nextStepV{
			{"prefer_protocol", "group_update_plan", "prefer UDP protocols (Hysteria2, AWG) for the affected groups"},
			{"add_cdn_profile", "profile_create_plan", "VLESS over XHTTP behind a CDN"},
			{"change_reality_sni", "profile_update_plan", "REALITY with a different server name; ports do not help"},
		}
	case clsUDPBlocked:
		return []nextStepV{
			{"prefer_tcp_protocol", "profile_create_plan", "VLESS over TCP or XHTTP on 443"},
			{"add_relay", "relay_plan", "a relay over TCP"},
			{"remove_from_subscriptions", "node_update_plan", "take the UDP servers out of subscriptions for this country"},
		}
	case clsFingerprinted:
		return []nextStepV{
			{"change_obfuscation", "profile_update_plan", "change the obfuscation (AmneziaWG parameters, Hysteria2 obfs) or move the group to another protocol"},
			{"prefer_protocol", "group_update_plan", "move the group to the protocol that works here"},
		}
	case clsWhitelist:
		return []nextStepV{
			{"add_relay", "relay_plan", "only whitelisted addresses help: an entry on hosting known to pass mobile networks, or CDN ranges"},
			{"check_entry_on_mobile", "", "check that the entry's address is reachable on mobile (checks_results, klick_reports) before relying on it"},
		}
	case clsOperator, clsRegional:
		return []nextStepV{
			{"add_relay", "relay_plan", "a relay through an entry node those operators reach"},
			{"enable_ipv6_entry", "node_ipv6_plan", "the IPv6 entry, when the node can take it and the dark networks reach it over IPv6"},
		}
	case clsBroken:
		return []nextStepV{
			{"fix_ours", "node_fix_plan", "repair the node or its server (node_doctor, profile_deploy_plan, relay_check)"},
		}
	case clsUnknown:
		return []nextStepV{{"investigate", "", "enable a country check, then run path_probe and node_trace"}}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------------------------
// ways

// works: the way has independent evidence that it works and nothing says it fails (it may still be dark for some operators).
func (w *wayV) works() bool {
	return len(w.WorksBy) > 0 && w.State != stateFailing && w.State != stateBroken
}

// wayStates says for every inbound of the diagnosed nodes whether it works for the people it is given to.
func (b *blockCtx) wayStates(diags map[string]*blockNodeV) map[string]*wayV {
	in := b.in
	out := map[string]*wayV{}
	for _, x := range in.Inbounds {
		w := &wayV{State: stateUnknown}
		out[x.ID] = w
		n := b.byID[x.NodeID]
		d := diags[x.NodeID]
		net := netOf(x.Protocol)
		if n != nil && nodeIsOurs(n.Status) {
			w.State = stateBroken
			w.BlockedBy = append(w.BlockedBy, "node_"+n.Status)
			continue
		}
		hard := false
		if d != nil {
			for _, c := range d.Classes {
				switch c.Class {
				case clsBroken:
					if c.inbound == x.ID || c.port == x.Port && c.inbound == "" && net == "tcp" {
						w.State = stateBroken
						w.BlockedBy = append(w.BlockedBy, clsBroken)
					}
				case clsIPBlocked:
					if c.Detail["scope"] == "tcp_only" && net == "udp" || c.Confidence == confLow {
						continue
					}
					hard = true
					w.BlockedBy = append(w.BlockedBy, clsIPBlocked)
				case clsPortBlocked:
					if net == "tcp" && c.port == x.Port && c.Confidence != confLow {
						hard = true
						w.BlockedBy = append(w.BlockedBy, clsPortBlocked)
					}
				case clsTCPThrottled:
					if net == "tcp" && (c.protocol == normProtocol(x.Protocol)) {
						hard = true
						w.BlockedBy = append(w.BlockedBy, clsTCPThrottled)
					}
				case clsUDPBlocked:
					if net == "udp" {
						hard = true
						w.BlockedBy = append(w.BlockedBy, clsUDPBlocked)
					}
				case clsFingerprinted:
					if c.protocol == normProtocol(x.Protocol) {
						hard = true
						w.BlockedBy = append(w.BlockedBy, clsFingerprinted)
					}
				case clsWhitelist, clsOperator, clsRegional:
					w.Soft = true
					w.BlockedBy = append(w.BlockedBy, c.Class)
				}
			}
			cc := in.Countries[x.NodeID]
			if cc != nil && net == "tcp" {
				for _, p := range cc.TSPU {
					if p.Port == x.Port && p.Verdict == "open" {
						w.WorksBy = append(w.WorksBy, "tspu_open")
					}
				}
			}
			if k := in.Klick[x.NodeID].kind(x.Protocol); k != nil && k.mostlyOK() {
				w.WorksBy = append(w.WorksBy, "klick_ok")
			}
		}
		switch in.Totals[x.ID].health(x.Protocol) {
		case stateWorking:
			w.WorksBy = append(w.WorksBy, "counters_ok")
		case stateFailing:
			hard = true
			w.BlockedBy = append(w.BlockedBy, "counters_failing")
		}
		w.BlockedBy, w.WorksBy = sortedUnique(w.BlockedBy), sortedUnique(w.WorksBy)
		switch {
		case w.State == stateBroken:
		case hard && len(w.WorksBy) == 0:
			w.State = stateFailing
		case hard:
			w.State = statePartial
		case len(w.WorksBy) > 0 && w.Soft:
			w.State = statePartial
		case len(w.WorksBy) > 0:
			w.State = stateWorking
		case w.Soft:
			w.State = statePartial
		}
	}
	return out
}
