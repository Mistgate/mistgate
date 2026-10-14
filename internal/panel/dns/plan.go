package dns

import (
	"fmt"
	"slices"
)

// SplitPlan is one split rule as a client carries it: the resolvers that answer its suffixes, and the suffixes.
type SplitPlan struct {
	Rule      int        // index in Preset.Split
	Endpoints []Endpoint // never empty
	// Suffixes are the bodies (lower-case punycode, no leading dot) not claimed by an earlier rule: the first rule
	// that names a suffix wins, like Happ's DirectSites. May be empty when every suffix was claimed earlier.
	Suffixes []string
}

// ClientPlan is what a client can carry of a preset, before it is written in the client's syntax: the main
// endpoints (for every domain that is not split off), the split rules (only when SplitDirect is on; off = the split
// is ignored, with a note), and notes (English) on what was dropped. An error means no main endpoint can be carried;
// the caller leaves the dns section out. Mihomo, Xray and sing-box renderers share it.
//
// IPv6 resolver addresses are left out when the client takes IPv6 but the profile switches IPv6 answers off (Mihomo):
// a resolver reached over IPv6 only adds noise. They are kept only when nothing else is left of a list and the preset
// allows IPv6.
func (p Preset) ClientPlan(c Client) (main []Endpoint, split []SplitPlan, notes []string, err error) {
	main, notes = p.planEndpoints(c, p.Servers)
	if len(main) == 0 {
		return nil, nil, nil, fmt.Errorf("dns: %s cannot carry any main server of preset %q", clientLabel(c), p.Name)
	}
	if len(p.Split) > 0 && !p.SplitDirect {
		notes = append(notes, "split ignored (SplitDirect off)")
	}
	if !p.SplitDirect {
		return main, nil, notes, nil
	}
	var claimed []string
	for i, r := range p.Split {
		eps, enotes := p.planEndpoints(c, r.Servers)
		notes = append(notes, enotes...)
		if len(eps) == 0 {
			notes = append(notes, fmt.Sprintf("split rule %d dropped: no server %s can carry", i+1, clientLabel(c)))
			continue
		}
		sp := SplitPlan{Rule: i, Endpoints: eps}
		for _, sfx := range r.Suffixes {
			body, err := hostname(suffixBody(sfx)) // punycode, lower case; a stored non-ASCII suffix is converted here
			if err != nil {
				notes = append(notes, fmt.Sprintf("suffix %q skipped: not a host name", sfx))
				continue
			}
			if slices.Contains(claimed, body) {
				continue
			}
			claimed = append(claimed, body)
			sp.Suffixes = append(sp.Suffixes, body)
		}
		split = append(split, sp)
	}
	return main, split, notes, nil
}

// planEndpoints is Resolve for a server list, with the IPv6 resolver addresses left out first (see ClientPlan).
func (p Preset) planEndpoints(c Client, servers []Server) ([]Endpoint, []string) {
	eps, notes := Resolve(c, p.Transport, servers, true)
	if len(eps) == 0 && c.IPv6() && !p.IPv4Only {
		return Resolve(c, p.Transport, servers, false)
	}
	return eps, notes
}

func clientLabel(c Client) string {
	switch c {
	case ClientHapp:
		return "Happ"
	case ClientMihomo:
		return "Mihomo"
	case ClientXray:
		return "Xray"
	case ClientSingbox:
		return "sing-box"
	case ClientAmneziaWG:
		return "AmneziaWG"
	}
	return string(c)
}
