package subs

import (
	"context"
	"log/slog"
	"net/http"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
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

// routingAnswer is the routing an app gets in one answer: Header is the value of the `routing` header, Body the link as a line of the
// subscription body when it is too long for the header (subsettings.MaxRoutingHeaderValue); at most one of them is set.
type routingAnswer struct{ Header, Body string }

// routingFor is the routing profile of an app (app is "happ" or "incy") for a person of a main group:
//
//   - the owner's profile of that group (subsettings.RoutingProfileFor), with the DNS fields of the person's DNS preset in place of
//     its own (a preset with no server the app can take leaves the profile's own), in the mode the owner chose, under the scheme of
//     the app;
//   - no profile of the owner: Happ gets the DNS profile of the person's preset, exactly as it did before the owner could have a profile
//     (rt.HappRouting); INCY gets the same profile under its own scheme, which reads the same fields (docs.incy.cc/en/routing).
//
// presetOf is the person's DNS preset (nil when there is none); it is asked only when an answer needs it.
func routingFor(ctx context.Context, set *adminv1.SubscriptionSettings, groupID, userID, app string, rt Routing, presetOf func() *dns.Preset, log *slog.Logger) routingAnswer {
	if e := subsettings.RoutingProfileFor(set, groupID); e != nil {
		doc, ok := subsettings.RoutingDocOf(e)
		if !ok {
			return routingAnswer{}
		}
		if pre := presetOf(); pre != nil {
			if f, err := dns.ProfileDNSOf(*pre); err != nil {
				log.Warn("routing profile: the person's DNS preset is not put in", "code", "subscription_routing_dns_unavailable", "err", err)
			} else {
				doc = doc.WithDNS(subsettings.RoutingDNS{
					RemoteType: f.RemoteType, RemoteDomain: f.RemoteDomain, RemoteIP: f.RemoteIP,
					DomesticType: f.DomesticType, DomesticDomain: f.DomesticDomain, DomesticIP: f.DomesticIP, Hosts: f.Hosts,
				})
			}
		}
		link := doc.Link(app, e.GetOnlyAdd())
		if len(link) > subsettings.MaxRoutingHeaderValue {
			return routingAnswer{Body: link}
		}
		return routingAnswer{Header: link}
	}
	if rt == nil {
		return routingAnswer{}
	}
	switch app {
	case "happ":
		val, err := rt.HappRouting(ctx, userID)
		if err != nil {
			log.Warn("happ routing header skipped", "code", "subscription_happ_routing_unavailable", "err", err) // no user id, no token in the log
		}
		return routingAnswer{Header: val}
	case "incy":
		if pre := presetOf(); pre != nil {
			val, err := dns.AppRouting(*pre, app)
			if err != nil {
				log.Warn("incy routing header skipped", "code", "subscription_incy_routing_unavailable", "err", err)
			}
			return routingAnswer{Header: val}
		}
	}
	return routingAnswer{}
}

// routing is routingFor for the answer to a request: the link list of an active person to Happ or INCY; nothing for any other client.
func (h *handler) routing(r *http.Request, v access.SubView, set *adminv1.SubscriptionSettings) routingAnswer {
	app := routingApp(r.UserAgent())
	if app == "" || v.Status != access.StatusActive || v.UserID == "" {
		return routingAnswer{}
	}
	if v.Refusal != "" && subsettings.RoutingProfileFor(set, v.GroupID) != nil {
		return routingAnswer{} // a refused device is not handed the owner's rules (the DNS profile of its preset it always got)
	}
	ctx := r.Context()
	return routingFor(ctx, set, v.GroupID, v.UserID, app, h.cfg.Routing, func() *dns.Preset { return h.linkPreset(ctx, v) }, h.cfg.Log)
}
