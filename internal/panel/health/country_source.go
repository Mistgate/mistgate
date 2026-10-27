package health

import (
	"context"
	"errors"
	"time"
)

// The country check asks a source of probes inside a country how a node looks from there. The check itself (which nodes,
// how many probes, the verdict, the alert) lives in country.go and knows nothing about any one service: a CountrySource is
// anything that can take the targets of a round and report, per probe, what it saw. Globalping (internal/panel/globalping)
// is the first; a block list or a probe inside the country can feed the same verdicts later.

// CountryConfig is what the owner chose.
type CountryConfig struct {
	Enabled bool
	// Country is ISO 3166-1 alpha-2, upper case.
	Country string
	// Interval between rounds, at least ten minutes.
	Interval time.Duration
}

// CountrySource measures from inside a country.
type CountrySource interface {
	// Name is a short machine name for logs ("globalping").
	Name() string
	// HourlyLimit is how many tests (one probe running one measurement) the source allows an hour; 0 = no limit known.
	HourlyLimit() int
	// Measure runs one round. A *CountryLimitError means the source wants the caller to wait.
	Measure(ctx context.Context, r CountryRound) (CountryObserved, error)
}

// CountryRound is one round to measure.
type CountryRound struct {
	Country string
	// Probes is the number of probes per measurement the budget allows (2 or 3).
	Probes int
	// Want asks a source for named networks first; City pins a probe when set. Empty means the generic country selection.
	Want []CountryWant
	// Control is a site that is never blocked in the country: a probe that cannot open TCP Port of Host is not counted.
	ControlHost string
	ControlPort uint16
	Targets     []CountryTarget
}

// CountryWant is a named network to ask during a round. A want with an empty City can match any of its ASNs.
type CountryWant struct {
	Name string
	ASNs []uint32
	City string
}

// CountryTarget is one node. IP is its IPv4 address (the only address that is measured).
type CountryTarget struct {
	Key           string
	IP            string
	Ports         []uint16 // TCP ports; none = ICMP only
	PreferredPort uint16   // the TLS endpoint a paid source tests, one of Ports; 0 = none, fall back to 443 then the lowest
}

// CountryObserved is what the source saw.
type CountryObserved struct {
	// Eyeball: the probes are home or mobile networks (false: any network, too few of them were online).
	Eyeball bool
	Probes  []ObservedProbe
	NoProbe []string // named operators without a matching online home or mobile probe
	Asked   []string // operators pinned in the control request, whether or not their probes answered
}

// ObservedProbe is one probe of the round.
type ObservedProbe struct {
	Network string
	ASN     uint32
	City    string
	// Eyeball is derived from this probe's tags, including in a fallback round with mixed probe types.
	Eyeball bool
	// Online: the probe took part in the round at all (not offline).
	Online bool
	// ControlOK: it opened TCP to the control site.
	ControlOK bool
	// Nodes by CountryTarget.Key. A target missing here was not answered by this probe.
	Nodes map[string]ObservedNode
}

// ObservedNode is what one probe saw of one target.
type ObservedNode struct {
	// Answered: the probe returned a result for this target (it was not offline or broken in the middle of the round).
	Answered bool
	// Complete: the probe answered the ICMP test and every TCP port test for this target.
	Complete bool
	ICMPOK   bool
	ICMPMs   uint32
	TCP      []ObservedPort
}

// ObservedPort is one TCP port of a target.
type ObservedPort struct {
	Port uint16
	OK   bool
	MS   uint32
}

// CountryLimitError is returned by a source that hit its rate limit: no round before Reset has passed.
type CountryLimitError struct{ Reset time.Duration }

func (e *CountryLimitError) Error() string { return "country check source: rate limit, retry later" }

// ErrCountryStopped is what a source returns when the owner switched it off in the middle of a round. The round ends and
// stores nothing.
var ErrCountryStopped = errors.New("country check source: switched off")
