package store

import (
	"net"
	"strconv"
)

// RelayExit is the TCP or UDP destination seen by the entry, before any reverse bridge.
type RelayExit struct {
	Host string
	Port uint16
}

func (e RelayExit) String() string { return net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port))) }

func (in FleetInboundRow) RelayExit(address string, listen uint16) RelayExit {
	return RelayExit{address, listen}
}

// RelayInbound projects the same routing fields from the access view as the fleet query.
func (f AccessInboundFull) RelayInbound() FleetInboundRow {
	return FleetInboundRow{ID: f.Inbound.ID, ProfileID: f.Profile.ID, Protocol: f.Profile.Protocol, ProfileVersion: f.Profile.Version, SpecVersion: f.Inbound.SpecVersion, TLSNameOverride: f.Inbound.TLSServerNameOverride, RealityTargetOverride: f.Inbound.RealityTargetOverride}
}
