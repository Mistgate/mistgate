// Package retention holds the owner's retention settings: how long each table the sweep prunes keeps its rows. Every value
// is a whole number stored as the setting "retention.<name>"; a missing or unusable value means the default, which is what
// the panel always did. The package is small and has no dependencies because the health sweep reads it on every edition.
package retention

import (
	"strconv"
	"time"
)

// Prefix starts every retention setting key.
const Prefix = "retention."

// Field is one retention setting.
type Field struct {
	Key      string // the part after Prefix, also the MCP and RPC name
	Default  int
	Min, Max int
	Unit     time.Duration // what one unit of the value is: an hour or a day
}

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

// Fields lists the settings in display order.
var Fields = [...]Field{
	{"journal_days", 7, 1, 7, day},                 // the node journal; its read window is 7 days, so more would never be seen
	{"events_info_days", 90, 1, 365, day},          // events of severity info
	{"events_warn_days", 400, 1, 1095, day},        // warning and error events
	{"audit_days", 400, 30, 1095, day},             // the audit log
	{"alerts_days", 90, 7, 730, day},               // resolved alerts
	{"check_samples_hours", 25, 25, 168, hour},     // raw check rounds, once their day is rolled up
	{"check_daily_days", 90, 7, 730, day},          // the daily check aggregates
	{"host_history_days", 90, 7, 365, day},         // hourly host load of a node
	{"traffic_bucket_days", 400, 30, 3650, day},    // hourly traffic per person, node and protocol
	{"node_traffic_hour_days", 400, 30, 3650, day}, // hourly traffic per node
	{"networks_days", 7, 1, 7, day},                // hourly ASN counters and the recent per-person ASN view
}

// Keys lists the setting keys, for one batched read.
func Keys() []string {
	keys := make([]string, len(Fields))
	for i, f := range Fields {
		keys[i] = Prefix + f.Key
	}
	return keys
}

// Policy is the effective retention: Values[i] belongs to Fields[i].
type Policy struct{ Values [len(Fields)]int }

// Default is the policy of a panel whose owner never changed anything.
func Default() Policy {
	var p Policy
	for i, f := range Fields {
		p.Values[i] = f.Default
	}
	return p
}

// Valid reports whether v is within the field's limits.
func (f Field) Valid(v int) bool { return v >= f.Min && v <= f.Max }

// Parse reads stored settings (as SettingValues returns them). A value that is not a number, or is out of its limits, is the
// default: a hand-edited row cannot make the sweep delete everything.
func Parse(stored map[string]string) Policy {
	p := Default()
	for i, f := range Fields {
		if v, err := strconv.Atoi(stored[Prefix+f.Key]); err == nil && f.Valid(v) {
			p.Values[i] = v
		}
	}
	return p
}

// Get returns the value of the named field and whether the name exists.
func (p Policy) Get(key string) (int, bool) {
	for i, f := range Fields {
		if f.Key == key {
			return p.Values[i], true
		}
	}
	return 0, false
}

// Duration is the named field as a duration.
func (p Policy) Duration(key string) time.Duration {
	for i, f := range Fields {
		if f.Key == key {
			return time.Duration(p.Values[i]) * f.Unit
		}
	}
	return 0
}
