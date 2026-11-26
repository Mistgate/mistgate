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

// ForTarget is a read-only view without evidence from a previous endpoint. Bridge
// reports and traffic counters have independent writers and must survive filtering.
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
