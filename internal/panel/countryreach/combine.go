package countryreach

import "encoding/json"

// BlocklistCountry is the one country the block lists cover (they are Russian). With another country selected the block-list
// source is "not available for this country" and does nothing. TSPUCountry is the same for the TSPU probe (its references are
// Russian).
const (
	BlocklistCountry = "RU"
	TSPUCountry      = "RU"
)

// The verdicts and confidences of a combined country check, as stored by the sources and sent to the screens in lower case.
const (
	VerdictOK = "ok"
	// VerdictPartial means some networks cannot reach the node. The TSPU's per-port "partial" is folded separately below.
	VerdictPartial    = "partial"
	VerdictTCPBlocked = "tcp_blocked"
	VerdictBlocked    = "blocked"
	VerdictListed     = "listed"
	VerdictUnknown    = "unknown"

	// ConfidenceEyeball: a live check from home or mobile networks inside the country. ConfidenceDCOnly: a live check, but only
	// from data centres, which see their own uplink's filtering. ConfidenceListed: no live check, a block list only.
	ConfidenceEyeball = "eyeball"
	ConfidenceDCOnly  = "dc_only"
	ConfidenceListed  = "listed"
	// ConfidenceConflict: two live sources disagree; the worse verdict is shown so that nothing is silently dropped.
	ConfidenceConflict = "conflict"

	// The sources, as CountryCheck.sources and the alert's "sources" param name them.
	SourceGlobalping = "globalping"
	SourceTSPU       = "tspu"
	SourceLists      = "lists"
)

// Live is the newest round of a live source (Globalping): its verdict and whether the probes were eyeball networks.
type Live struct {
	Verdict string
	Eyeball bool
}

// TSPU is what the probe node in the country said of a node: ok, tcp_blocked or unknown (TSPUVerdict maps the stored one). It is
// always a datacentre view.
type TSPU struct {
	Verdict string
}

// Listing is what the block lists say of one node. ListedBy names the lists that hold the address; CDNProvider and CDNRegion
// are context and may be set when the address is in none of them.
type Listing struct {
	ListedBy    []string
	CDNProvider string
	CDNRegion   string
}

// Combined is the verdict a node shows. Sources are the sources the verdict rests on, in the order globalping, tspu (or "lists"
// alone for a listed verdict; empty for unknown).
type Combined struct {
	Verdict, Confidence string
	Sources             []string
	ListedBy            []string
	CDNProvider         string
	CDNRegion           string
}

func isLive(v string) bool {
	return v == VerdictOK || v == VerdictPartial || v == VerdictTCPBlocked || v == VerdictBlocked
}

// worse is the more serious of two live verdicts: blocked > tcp_blocked > partial > ok.
func worse(a, b string) string {
	rank := map[string]int{VerdictOK: 0, VerdictPartial: 1, VerdictTCPBlocked: 2, VerdictBlocked: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// TSPUPort is one entry of the detail the probe node stores per node (country_tspu_check.detail_json): what it saw on one TCP
// port. Verdict is blocked (the SYN dies at the TSPU), open (it connected, or a router behind the TSPU answered), refused (a
// reset) or timeout (no verdict).
type TSPUPort struct {
	Port    uint16 `json:"port"`
	Verdict string `json:"verdict"`
}

// ParseTSPUDetail reads detail_json; anything unreadable is no ports.
func ParseTSPUDetail(detail string) []TSPUPort {
	var ports []TSPUPort
	if json.Unmarshal([]byte(detail), &ports) != nil {
		return nil
	}
	return ports
}

// FoldTSPU is the verdict stored for a node, from what the probe saw on its ports: blocked when every port with a verdict was
// blocked, open when none was, partial when some were, unknown when no port has a verdict (all timed out). The ports that timed
// out say nothing and do not count.
func FoldTSPU(ports []TSPUPort) string {
	var blocked, other int
	for _, p := range ports {
		switch p.Verdict {
		case "blocked":
			blocked++
		case "open", "refused":
			other++
		}
	}
	switch {
	case blocked == 0 && other == 0:
		return "unknown"
	case other == 0:
		return "blocked"
	case blocked == 0:
		return "open"
	}
	return "partial"
}

// TSPUVerdict maps the verdict stored for a node by the probe node (blocked | open | partial | unknown) to the shared scale.
// blocked is tcp_blocked: the SYNs die silently behind the box while the address itself may still answer. partial (some ports
// blocked, others not) is tcp_blocked only when no port is open (the rest were refused or silent), otherwise the node can be
// reached and it is ok; the ports stay in the detail.
func TSPUVerdict(stored string, ports []TSPUPort) string {
	switch stored {
	case "blocked":
		return VerdictTCPBlocked
	case "open":
		return VerdictOK
	case "partial":
		for _, p := range ports {
			if p.Verdict == "open" {
				return VerdictOK
			}
		}
		return VerdictTCPBlocked
	}
	return VerdictUnknown
}

// Combine merges the sources of the country check for one node. live is the newest Globalping round (nil: none, or the
// integration is off); tspu is the newest verdict of the probe node (nil: none, too old, or the source is off); list is the
// newest block-list check (nil: none, or off).
//
//   - Live verdicts (ok, partial, tcp_blocked, blocked) win over the lists, which only add detail (listed_by, the CDN range).
//   - One live source: its verdict, with "eyeball" (Globalping on home or mobile networks) or "dc_only" (Globalping from data
//     centres; the probe node, always) as the confidence.
//   - Both agree: that verdict, "eyeball" when Globalping saw it from eyeball networks (that outranks the data-centre view),
//     else "dc_only". Both live and different: the worse verdict, with the confidence "conflict".
//   - Without a live verdict, a list hit gives "listed" (confidence "listed"); anything else is "unknown": a list that does not
//     hold the address proves nothing about reachability.
//
// Only live verdicts ever open an alert (health, addCountryConds); a listed one never does.
func Combine(live *Live, tspu *TSPU, list *Listing) Combined {
	var c Combined
	if list != nil {
		c.ListedBy, c.CDNProvider, c.CDNRegion = list.ListedBy, list.CDNProvider, list.CDNRegion
	}
	g, t := live != nil && isLive(live.Verdict), tspu != nil && isLive(tspu.Verdict)
	switch {
	case g && t:
		c.Verdict, c.Sources = live.Verdict, []string{SourceGlobalping, SourceTSPU}
		switch {
		case live.Verdict != tspu.Verdict:
			if (live.Verdict == VerdictPartial && tspu.Verdict == VerdictOK) || (live.Verdict == VerdictOK && tspu.Verdict == VerdictPartial) {
				c.Verdict = VerdictPartial
				if live.Eyeball {
					c.Confidence = ConfidenceEyeball
				} else {
					c.Confidence = ConfidenceDCOnly
				}
			} else {
				c.Verdict, c.Confidence = worse(live.Verdict, tspu.Verdict), ConfidenceConflict
			}
		case live.Eyeball:
			c.Confidence = ConfidenceEyeball
		default:
			c.Confidence = ConfidenceDCOnly
		}
	case g:
		c.Verdict, c.Confidence, c.Sources = live.Verdict, ConfidenceDCOnly, []string{SourceGlobalping}
		if live.Eyeball {
			c.Confidence = ConfidenceEyeball
		}
	case t:
		c.Verdict, c.Confidence, c.Sources = tspu.Verdict, ConfidenceDCOnly, []string{SourceTSPU}
	case len(c.ListedBy) > 0:
		c.Verdict, c.Confidence, c.Sources = VerdictListed, ConfidenceListed, []string{SourceLists}
	default:
		c.Verdict = VerdictUnknown
	}
	return c
}
