package countryreach

const (
	VantageKindHome    = "home"
	VantageKindMobile  = "mobile"
	VantageKindDC      = "dc"
	VantageKindUnknown = "unknown"

	VantageStateReach     = "reach"
	VantageStateDark      = "dark"
	VantageStateWhitelist = "whitelist"
	VantageStateUncertain = "uncertain"

	ReasonTooFewProbes = "too_few_probes"
)

// Vantage is one network, unit or city in an external source's result. It contains no probe or node address.
type Vantage struct {
	Key       string `json:"key"`
	Network   string `json:"network,omitempty"`
	Region    string `json:"region,omitempty"`
	ASN       string `json:"asn,omitempty"`
	Kind      string `json:"kind,omitempty"`
	State     string `json:"state"`
	DPIHop    int    `json:"dpi_hop,omitempty"`
	TargetHop int    `json:"target_hop,omitempty"`
	ICMPOK    bool   `json:"icmp_ok,omitempty"`
	TCPOK     bool   `json:"tcp_ok,omitempty"`
	TLS       bool   `json:"tls,omitempty"`
}

// ExtDetail is the source-neutral JSON stored with an external check.
type ExtDetail struct {
	Reason   string    `json:"reason,omitempty"`
	Vantages []Vantage `json:"vantages"`
	Skipped  []string  `json:"skipped,omitempty"`
	Cost     int       `json:"cost,omitempty"`
}

// JudgeVantages judges one source row. Only reach and dark vantages count as valid; a whitelist vantage counts as reach if
// its ICMP or TCP result succeeded, and never counts as dark.
func JudgeVantages(vs []Vantage, minValid int, full string) (verdict, reason string) {
	var valid, reached, dark int
	for _, v := range vs {
		switch v.State {
		case VantageStateReach:
			valid++
			reached++
		case VantageStateDark:
			valid++
			dark++
		case VantageStateWhitelist:
			if v.ICMPOK || v.TCPOK {
				valid++
				reached++
			}
		}
	}
	if valid < minValid {
		return VerdictUnknown, ReasonTooFewProbes
	}
	if dark == 0 {
		return VerdictOK, ""
	}
	if reached == 0 {
		return full, ""
	}
	return VerdictPartial, ""
}
