package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// TargetKey identifies the configuration a client-eye relay check exercised.
func (l RelayLink) TargetKey(in FleetInboundRow, exitAddress string, exitPort uint16) string {
	key := strings.Join([]string{l.ID, l.ViaNodeID, l.ViaNodeAddress, exitAddress, in.ID, in.ProfileID, in.Protocol,
		strconv.FormatUint(uint64(in.ProfileVersion), 10), strconv.FormatUint(in.SpecVersion, 10), in.TLSNameOverride, in.RealityTargetOverride,
		l.Network, strconv.Itoa(int(l.Port)), strconv.Itoa(int(exitPort))}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// ForTarget is a read-only view without evidence from a previous endpoint. Bridge
// reports and traffic counters have independent writers and must survive filtering.
func (l RelayLink) ForTarget(to, key string) RelayLink {
	c, ok := l.Check()
	if !ok {
		return l
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(l.CheckJSON), &fields) != nil {
		return l
	}
	if to == "" || c.To != to || c.Network != l.Network || c.Port != l.Port {
		for _, name := range []string{"leg", "path", "direct_speed", "direct_streak", "direct_streak_at", "ok", "stage"} {
			delete(fields, name)
		}
	}
	if c.Monitor != nil && (key == "" || c.Monitor.TargetKey != key) {
		delete(fields, "monitor")
	}
	raw, _ := json.Marshal(fields)
	l.CheckJSON = string(raw)
	return l
}
