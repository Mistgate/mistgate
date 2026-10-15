package subs

import (
	"context"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/dns"
)

// Effective resolves the DNS preset of a user's link apps (dns.LinkDNS: the person's pick for them, the user's, the group's,
// the offer's default, the instance's); *dns.Service implements it.
type Effective interface {
	LinkEffective(ctx context.Context, userID string) (dns.Preset, error)
}

// HappRouting makes the Routing of the handler from the DNS module: the `routing` header value is the preset of the user's
// link apps as a Happ routing profile. An error (Happ cannot express any main server of the preset) leaves the header out.
func HappRouting(e Effective) Routing { return happRouting{e} }

type happRouting struct{ e Effective }

// LinkEffective lets the handler take the DNS preset of a user for the Mihomo, Xray JSON and sing-box profiles from the same
// module (Config.DNS defaults to the Routing when it has this method), so wiring HappRouting wires both.
func (h happRouting) LinkEffective(ctx context.Context, userID string) (dns.Preset, error) {
	return h.e.LinkEffective(ctx, userID)
}

func (h happRouting) HappRouting(ctx context.Context, userID string) (string, error) {
	p, err := h.e.LinkEffective(ctx, userID)
	if err != nil {
		return "", err
	}
	return dns.HappRouting(p)
}

// DefaultPresets reads and writes the instance default DNS preset and the offer for the link apps (owned by the DNS module,
// shown in the subscription settings as default_dns_preset_id and link_dns); *dns.Service implements it.
type DefaultPresets interface {
	DefaultPresetID(ctx context.Context) (string, error)
	SetDefaultPresetID(ctx context.Context, id string) error
	LinkOptions(ctx context.Context) (*adminv1.LinkDnsOptions, error)
	SetLinkOptions(ctx context.Context, o *adminv1.LinkDnsOptions) error
}
