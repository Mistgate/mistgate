package countryreach

// BlocklistCountry is the one country the block lists cover (they are Russian). With another country selected the block-list
// source is "not available for this country" and does nothing.
const BlocklistCountry = "RU"

// The verdicts and confidences of a combined country check, as stored by the sources and sent to the screens in lower case.
const (
	VerdictOK         = "ok"
	VerdictTCPBlocked = "tcp_blocked"
	VerdictBlocked    = "blocked"
	VerdictListed     = "listed"
	VerdictUnknown    = "unknown"

	// ConfidenceEyeball: a live check from home or mobile networks inside the country. ConfidenceDCOnly: a live check, but only
	// from data centres, which see their own uplink's filtering. ConfidenceListed: no live check, a block list only.
	ConfidenceEyeball = "eyeball"
	ConfidenceDCOnly  = "dc_only"
	ConfidenceListed  = "listed"
)

// Live is the newest round of a live source (Globalping): its verdict and whether the probes were eyeball networks.
type Live struct {
	Verdict string
	Eyeball bool
}

// Listing is what the block lists say of one node. ListedBy names the lists that hold the address; CDNProvider and CDNRegion
// are context and may be set when the address is in none of them.
type Listing struct {
	ListedBy    []string
	CDNProvider string
	CDNRegion   string
}

// Combined is the verdict a node shows.
type Combined struct {
	Verdict, Confidence string
	ListedBy            []string
	CDNProvider         string
	CDNRegion           string
}

// Combine merges the sources of the country check for one node. live is the newest Globalping round (nil: none, or the
// integration is off); list is the newest block-list check (nil: none, or off).
//
//   - A live verdict (ok, tcp_blocked, blocked) wins; the block lists only add detail (listed_by, the CDN range).
//   - Without one (no round, or an unknown round), a list hit gives "listed", with the confidence "listed".
//   - Anything else is "unknown": a list that does not hold the address proves nothing about reachability.
//
// Only a live verdict ever opens an alert (health, addCountryConds reads the Globalping rounds, not this).
func Combine(live *Live, list *Listing) Combined {
	var c Combined
	if list != nil {
		c.ListedBy, c.CDNProvider, c.CDNRegion = list.ListedBy, list.CDNProvider, list.CDNRegion
	}
	switch {
	case live != nil && (live.Verdict == VerdictOK || live.Verdict == VerdictTCPBlocked || live.Verdict == VerdictBlocked):
		c.Verdict, c.Confidence = live.Verdict, ConfidenceDCOnly
		if live.Eyeball {
			c.Confidence = ConfidenceEyeball
		}
	case len(c.ListedBy) > 0:
		c.Verdict, c.Confidence = VerdictListed, ConfidenceListed
	default:
		c.Verdict = VerdictUnknown
	}
	return c
}
