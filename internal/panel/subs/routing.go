package subs

import (
	"context"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// Effective resolves the DNS preset of a user; *dns.Service implements it.
type Effective interface {
	Effective(ctx context.Context, userID string) (dns.Preset, dns.Source, error)
}

// HappRouting makes the Routing of the handler from the DNS module: the `routing` header value is the
// user's effective preset (user, group, instance default, built-in) as a Happ routing profile. An error
// (Happ cannot express any main server of the preset) leaves the header out.
func HappRouting(e Effective) Routing { return happRouting{e} }

type happRouting struct{ e Effective }

// Effective lets the handler take the DNS preset of a user for the Mihomo profile from the same module (Config.DNS
// defaults to the Routing when it has this method), so wiring HappRouting wires both.
func (h happRouting) Effective(ctx context.Context, userID string) (dns.Preset, dns.Source, error) {
	return h.e.Effective(ctx, userID)
}

func (h happRouting) HappRouting(ctx context.Context, userID string) (string, error) {
	p, _, err := h.e.Effective(ctx, userID)
	if err != nil {
		return "", err
	}
	return dns.HappRouting(p)
}

// DefaultPresets reads and writes the instance default DNS preset (owned by the DNS module, shown in the
// subscription settings as default_dns_preset_id); *dns.Service implements it.
type DefaultPresets interface {
	DefaultPresetID(ctx context.Context) (string, error)
	SetDefaultPresetID(ctx context.Context, id string) error
}
