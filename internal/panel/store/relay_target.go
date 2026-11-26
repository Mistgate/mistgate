package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Target identifies the configuration a client-eye relay check exercised.
func (l RelayLink) Target(in FleetInboundRow, exitAddress string, listen uint16) (string, string) {
	exit := in.RelayExit(exitAddress, listen)
	key := strings.Join([]string{l.ID, l.ViaNodeID, l.ViaNodeAddress, exitAddress, in.ID, in.ProfileID, in.Protocol,
		strconv.FormatUint(in.SpecVersion, 10), in.TLSNameOverride, in.RealityTargetOverride,
		l.Network, strconv.Itoa(int(l.Port)), strconv.Itoa(int(exit.Port))}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return exit.String(), hex.EncodeToString(sum[:])
}

// legacyTargetKey is the key the monitors written before the profile revision left the target were stored under. It is
// accepted for one release so that an upgrade does not discard every relay's monitor at once; remove it in the next one.
func (l RelayLink) legacyTargetKey(in FleetInboundRow, exitAddress string, listen uint16) string {
	key := strings.Join([]string{l.ID, l.ViaNodeID, l.ViaNodeAddress, exitAddress, in.ID, in.ProfileID, in.Protocol,
		strconv.FormatUint(uint64(in.ProfileVersion), 10), strconv.FormatUint(in.SpecVersion, 10), in.TLSNameOverride, in.RealityTargetOverride,
		l.Network, strconv.Itoa(int(l.Port)), strconv.Itoa(int(listen))}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// ForInbound is Target followed by ForTarget: the view of the link for the exit inbound it points at, and the key a monitor
// of this target is written under. A monitor stored under the legacy key is re-keyed to the current one (together with the
// key of its automatic check), so it stays valid for the one release that accepts it and the first round after the upgrade
// carries its bookkeeping over instead of starting a full check.
func (l RelayLink) ForInbound(in FleetInboundRow, exitAddress string, listen uint16) (RelayLink, string) {
	to, key := l.Target(in, exitAddress, listen)
	if c, ok := l.Check(); ok && c.Monitor != nil {
		legacy := l.legacyTargetKey(in, exitAddress, listen)
		m := *c.Monitor
		if m.TargetKey == legacy {
			m.TargetKey = key
		}
		if m.AutoCheckTargetKey == legacy {
			m.AutoCheckTargetKey = key
		}
		if m != *c.Monitor {
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(l.CheckJSON), &fields) == nil {
				fields["monitor"], _ = json.Marshal(m)
				raw, _ := json.Marshal(fields)
				l.CheckJSON = string(raw)
			}
		}
	}
	return l.ForTarget(to, key), key
}

// ForTarget is a read-only view without evidence from a previous endpoint. Bridge
// reports and traffic counters have independent writers and must survive filtering.
// The key covers the entry's port too: moving only the entry's port drops the relay from subscriptions until the next health
// round, which proves the new forward and brings it back.
func (l RelayLink) ForTarget(to, key string) RelayLink {
	c, ok := l.Check()
	if !ok {
		return l
	}
	staleEndpoint := to == "" || c.To != to || c.Network != l.Network || c.Port != l.Port
	staleMonitor := c.Monitor != nil && (key == "" || c.Monitor.TargetKey != key)
	if !staleEndpoint && !staleMonitor {
		return l
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(l.CheckJSON), &fields) != nil {
		return l
	}
	if staleEndpoint {
		for _, name := range []string{"at", "to", "network", "port", "leg", "path", "direct_speed", "direct_streak", "direct_streak_at", "ok", "stage"} {
			delete(fields, name)
		}
	}
	if staleMonitor {
		delete(fields, "monitor")
	}
	raw, _ := json.Marshal(fields)
	l.CheckJSON = string(raw)
	return l
}
