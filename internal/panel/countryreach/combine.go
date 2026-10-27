package countryreach

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

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
	// ConfidenceConflict: a path-only reach disagrees with an end-to-end block.
	ConfidenceConflict = "conflict"

	// The sources, as CountryCheck.sources and the alert's "sources" param name them.
	SourceGlobalping     = "globalping"
	SourceTSPU           = "tspu"
	SourceLists          = "lists"
	SourceCheburcheck    = "cheburcheck"
	SourceCheburcheckOwn = "cheburcheck_own"
	SourceBsbord         = "bsbord"
	SourceBsbordTunnel   = "bsbord_tunnel"
	SourceBsbordGeo      = "bsbord_geo"
)

// Live is the newest row from one source. Path means it can show the path past the TSPU, but cannot prove that the node
// answered; Eyeball means the observation came from a home or mobile network.
type Live struct {
	Source  string
	Verdict string
	Reason  string
	Eyeball bool
	Path    bool
	At      time.Time
}

// SourceVerdict is the source's newest fresh observation, including an unknown result.
type SourceVerdict struct {
	Source  string
	Verdict string
	Reason  string
	Eyeball bool
	At      time.Time
}

// Listing is what the block lists say of one node. ListedBy names the lists that hold the address; CDNProvider and CDNRegion
// are context and may be set when the address is in none of them.
type Listing struct {
	ListedBy    []string
	CDNProvider string
	CDNRegion   string
}

// Combined is the verdict a node shows. Sources contains live sources in the fixed source order (or "lists" alone for a listed
// verdict); SourceVerdicts keeps each input verdict, including unknown rows.
type Combined struct {
	Verdict, Confidence string
	Sources             []string
	SourceVerdicts      []SourceVerdict
	ListedBy            []string
	CDNProvider         string
	CDNRegion           string
}

func isLive(v string) bool {
	return v == VerdictOK || v == VerdictPartial || v == VerdictTCPBlocked || v == VerdictBlocked
}

func isReach(v string) bool { return v == VerdictOK || v == VerdictPartial }

func isDark(v string) bool {
	return v == VerdictPartial || v == VerdictTCPBlocked || v == VerdictBlocked
}

func isFull(v string) bool { return v == VerdictTCPBlocked || v == VerdictBlocked }

func sourceRank(source string) int {
	switch source {
	case SourceGlobalping:
		return 0
	case SourceTSPU:
		return 1
	case SourceCheburcheck:
		return 2
	case SourceCheburcheckOwn:
		return 3
	case SourceBsbord:
		return 4
	case SourceBsbordTunnel:
		return 5
	case SourceBsbordGeo:
		return 6
	case SourceLists:
		return 7
	default:
		return 8
	}
}

// CompareSources compares source names in the fixed country-check order.
func CompareSources(a, b string) int {
	if ar, br := sourceRank(a), sourceRank(b); ar != br {
		return ar - br
	}
	return strings.Compare(a, b)
}

func orderedLive(live []Live) []Live {
	out := slices.Clone(live)
	slices.SortStableFunc(out, func(a, b Live) int { return CompareSources(a.Source, b.Source) })
	return out
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

// Combine merges every fresh source row for a node. The caller leaves stale rows and sources that are off out of live; unknown
// rows are kept for source_verdicts but do not affect the combined result. A path carrier can show that the TSPU lets a SYN
// through, but cannot prove that the node answers.
//
// Only live verdicts ever open an alert (health, addCountryConds); a listed one never does.
func Combine(live []Live, list *Listing) Combined {
	var c Combined
	if list != nil {
		c.ListedBy, c.CDNProvider, c.CDNRegion = list.ListedBy, list.CDNProvider, list.CDNRegion
	}
	ordered := orderedLive(live)
	var reached, dark []Live
	var nonPathFull string
	for _, row := range ordered {
		if row.Source != "" {
			c.SourceVerdicts = append(c.SourceVerdicts, SourceVerdict{Source: row.Source, Verdict: row.Verdict, Reason: row.Reason, Eyeball: row.Eyeball, At: row.At})
		}
		if !isLive(row.Verdict) {
			continue
		}
		if row.Source != "" && (len(c.Sources) == 0 || c.Sources[len(c.Sources)-1] != row.Source) {
			c.Sources = append(c.Sources, row.Source)
		}
		if isReach(row.Verdict) {
			reached = append(reached, row)
		}
		if isDark(row.Verdict) {
			dark = append(dark, row)
		}
		if !row.Path && isFull(row.Verdict) {
			nonPathFull = worse(nonPathFull, row.Verdict)
		}
	}
	switch {
	case len(reached)+len(dark) == 0 && len(c.Sources) == 0 && len(c.ListedBy) > 0:
		c.Verdict, c.Confidence, c.Sources = VerdictListed, ConfidenceListed, []string{SourceLists}
	case len(reached)+len(dark) == 0:
		c.Verdict = VerdictUnknown
	case len(dark) == 0:
		c.Verdict, c.Confidence = VerdictOK, confidenceFor(ordered, false)
	case len(reached) == 0:
		c.Verdict = VerdictTCPBlocked
		for _, row := range ordered {
			if isLive(row.Verdict) && !row.Path && row.Verdict == VerdictBlocked {
				c.Verdict = VerdictBlocked
				break
			}
		}
		c.Confidence = confidenceFor(ordered, false)
	default:
		allReachPath := true
		for _, row := range reached {
			allReachPath = allReachPath && row.Path
		}
		if nonPathFull != "" && allReachPath {
			c.Verdict, c.Confidence = nonPathFull, ConfidenceConflict
		} else {
			c.Verdict, c.Confidence = VerdictPartial, confidenceFor(dark, true)
		}
	}
	return c
}

func confidenceFor(rows []Live, darkOnly bool) string {
	for _, row := range rows {
		if isLive(row.Verdict) && (!darkOnly || isDark(row.Verdict)) && row.Eyeball {
			return ConfidenceEyeball
		}
	}
	return ConfidenceDCOnly
}
