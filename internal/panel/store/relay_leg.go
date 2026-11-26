package store

import (
	"context"
	"errors"
	"time"
)

// The address family of a relay's leg (relay.proto Relay.leg, migration 00160): people always reach the entry the way they did,
// and only the hop between the entry and the exit may change its family. The entry takes the exit's IPv6 entry when it is on
// (verified from outside, nodes.ipv6_entry), else the first stable address the doctor names for the node's uplink; the leg check
// then proves that this entry reaches it.

// SetRelayActiveLeg stores the family the leg uses now and when the panel last moved it by itself (the zero time = never).
func (s *Store) SetRelayActiveLeg(ctx context.Context, id, active string, triedAt, now time.Time) error {
	if active != RelayFamily4 && active != RelayFamily6 {
		return errors.New("store: relay leg must be 4 or 6")
	}
	res, err := s.W.ExecContext(ctx, `UPDATE relay SET active_leg = ?, leg_tried_at = ?, updated_at = ? WHERE id = ?`,
		active, unixOrZero(triedAt), unix(now), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RelayHostFor is the address a relay's leg should use now and the family of that address: the node's own address (a name or an
// IPv4 literal) for the IPv4 leg, and for the IPv6 leg its IPv6 entry when it is on, else the first stable address of the
// doctor's uplink. ok is false when the IPv6 leg is asked for and the node names no usable address: the caller must not invent
// one, and the relay cannot be used this way until the node has an address that passed a check.
func RelayHostFor(r Relay, address, ipv6Entry string, d IPv6Doctor) (host, family string, ok bool) {
	if r.ActiveFamily() != RelayFamily6 {
		return address, RelayFamily4, address != ""
	}
	if a, good := UsableIPv6(ipv6Entry); good {
		return a, RelayFamily6, true
	}
	if a := d.FirstAddr(); a != "" {
		return a, RelayFamily6, true
	}
	return "", RelayFamily6, false
}

// RelayLegWord is the family as the owner's setting names it (relay.proto Relay.leg): "ipv4" or "ipv6".
func RelayLegWord(family string) string {
	if family == RelayFamily6 {
		return RelayLegIPv6
	}
	return RelayLegIPv4
}

// ValidRelayLeg says whether s is one of the owner's words for the leg family; "" is allowed and means "auto".
func ValidRelayLeg(s string) bool {
	return s == "" || s == RelayLegAuto || s == RelayLegIPv4 || s == RelayLegIPv6
}
