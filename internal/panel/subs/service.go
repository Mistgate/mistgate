package subs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Service implements SubscriptionService: the settings document, the list of client apps the registered
// protocol plugins support, and the "which format does this User-Agent get" probe. The session and the role
// (readonly reads, owner and helper write) are enforced by the HTTP server for every procedure.
type Service struct {
	st       *store.Store
	settings *subsettings.Cache
	reg      *protocols.Registry
	brand    func(ctx context.Context) (instance.Settings, error)
	dns      DefaultPresets // nil = no DNS module: default_dns_preset_id stays empty
	log      *slog.Logger
}

// NewService builds the service; settings is the cache the public handler reads too, brand the loader of the
// instance brand (for effective_title).
func NewService(st *store.Store, settings *subsettings.Cache, reg *protocols.Registry, brand func(ctx context.Context) (instance.Settings, error), dns DefaultPresets, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{st: st, settings: settings, reg: reg, brand: brand, dns: dns, log: log}
}

// Handler returns the Connect path and handler of SubscriptionService.
func (s *Service) Handler() (string, http.Handler) {
	return adminv1connect.NewSubscriptionServiceHandler(s, connect.WithReadMaxBytes(1<<20))
}

func (s *Service) internal(what string, err error) error {
	s.log.Error("subscription settings: "+what, "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// fillDNS puts the instance default DNS preset id (kept by the DNS module) into the settings shown to the admin.
func (s *Service) fillDNS(ctx context.Context, set *adminv1.SubscriptionSettings) error {
	if s.dns == nil {
		return nil
	}
	id, err := s.dns.DefaultPresetID(ctx)
	set.DefaultDnsPresetId = id
	return err
}

func (s *Service) effectiveTitle(ctx context.Context, set *adminv1.SubscriptionSettings) string {
	if set.GetTitle() != "" {
		return set.Title
	}
	b, err := s.brand(ctx)
	if err != nil {
		s.log.Warn("read brand", "err", err)
	}
	return b.BrandName()
}

func (s *Service) GetSubscriptionSettings(ctx context.Context, _ *connect.Request[adminv1.GetSubscriptionSettingsRequest]) (*connect.Response[adminv1.GetSubscriptionSettingsResponse], error) {
	set, err := subsettings.Load(ctx, s.st) // not the cache: the editor must see what is stored
	if err != nil {
		return nil, s.internal("load", err)
	}
	if err := s.fillDNS(ctx, set); err != nil {
		return nil, s.internal("default dns preset", err)
	}
	resp := &adminv1.GetSubscriptionSettingsResponse{Settings: set, EffectiveTitle: s.effectiveTitle(ctx, set)}
	if resp.SampleGroup, resp.ServerSamples, err = s.nameSamples(ctx); err != nil {
		return nil, s.internal("server name samples", err)
	}
	if b, err := s.brand(ctx); err == nil {
		resp.NamesLanguage = b.Language
	}
	return connect.NewResponse(resp), nil
}

// nameSamples is what a person of the group most people are in gets in Happ, in subscription order (access/sub.go):
// the servers of the group's profiles that run (the inbound on, pending or active, on an active node) and that Happ
// takes. The subscription's rule without a user's own node choice and apps, enough for a preview of names.
func (s *Service) nameSamples(ctx context.Context) (string, []*adminv1.ServerSample, error) {
	a := s.st.Access()
	groups, err := a.Groups(ctx)
	if err != nil || len(groups) == 0 {
		return "", nil, err
	}
	g := groups[0]
	for _, x := range groups[1:] {
		if x.UserCount > g.UserCount {
			g = x
		}
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return "", nil, err
	}
	var out []*adminv1.ServerSample
	for _, f := range full {
		p, ok := s.reg.Get(f.Profile.Protocol)
		if !ok || !slices.Contains(g.ProfileIDs, f.Profile.ID) || !f.Inbound.Enabled || f.Node.State != "active" ||
			(f.Inbound.State != "pending" && f.Inbound.State != "active") || !protocols.AllowedForApps(p, plugin.ClientHapp) {
			continue
		}
		out = append(out, &adminv1.ServerSample{Node: f.Node.Name, CountryCode: f.Node.CountryCode, Profile: f.Profile.Name})
	}
	if len(out) == 0 {
		return "", nil, nil
	}
	return g.Name, out, nil
}

func (s *Service) UpdateSubscriptionSettings(ctx context.Context, req *connect.Request[adminv1.UpdateSubscriptionSettingsRequest]) (*connect.Response[adminv1.UpdateSubscriptionSettingsResponse], error) {
	admin, ok := auth.AdminFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))
	}
	set, err := s.settings.Update(ctx, req.Msg.Settings)
	if errors.Is(err, subsettings.ErrInvalid) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if err != nil {
		return nil, s.internal("save", err)
	}
	// The instance default DNS preset lives in the DNS module; the settings were valid, so set it now.
	if s.dns != nil {
		switch err := s.dns.SetDefaultPresetID(ctx, req.Msg.Settings.GetDefaultDnsPresetId()); {
		case errors.Is(err, dns.ErrUnknownPreset):
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("default_dns_preset_id: no such preset"))
		case err != nil:
			return nil, s.internal("set default dns preset", err)
		}
	}
	if err := s.fillDNS(ctx, set); err != nil {
		return nil, s.internal("default dns preset", err)
	}
	// The audit row says that the settings changed and how big they are, never their texts.
	params, _ := json.Marshal(map[string]int{"apps": len(set.Apps), "rules": len(set.Rules)})
	entry := store.AuditEntry{Actor: admin.ID, Action: "subscription_settings_update", Result: "ok", Params: string(params)}
	if ip := auth.ClientIPFrom(ctx); ip.IsValid() {
		entry.IP = ip.String()
	}
	if err := s.st.Audit(ctx, time.Now(), entry); err != nil {
		s.log.Warn("audit", "action", entry.Action, "err", err)
	}
	return connect.NewResponse(&adminv1.UpdateSubscriptionSettingsResponse{Settings: set}), nil
}

// clientNames are the display names of the client ids the plugins refer to.
var clientNames = map[plugin.ClientID]string{
	plugin.ClientHapp: "Happ", plugin.ClientAmnezia: "AmneziaVPN", plugin.ClientMihomo: "Mihomo / Clash Meta",
}

// formatOf maps a plugin wire format to the serving format of the settings; 0 = not a serving format (yet).
func formatOf(f plugin.ClientFormat) adminv1.SubFormat {
	switch f {
	case plugin.FormatURIList:
		return adminv1.SubFormat_SUB_FORMAT_BASE64_URIS
	case plugin.FormatMihomo:
		return adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML
	}
	return adminv1.SubFormat_SUB_FORMAT_UNSPECIFIED
}

func (s *Service) ListClients(context.Context, *connect.Request[adminv1.ListClientsRequest]) (*connect.Response[adminv1.ListClientsResponse], error) {
	byID := map[plugin.ClientID]*adminv1.ClientInfo{}
	var order []plugin.ClientID
	for _, p := range s.reg.List() {
		for _, c := range p.Clients() {
			ci := byID[c.Client]
			if ci == nil {
				name := clientNames[c.Client]
				if name == "" {
					name = string(c.Client)
				}
				ci = &adminv1.ClientInfo{Id: string(c.Client), Name: name}
				byID[c.Client] = ci
				order = append(order, c.Client)
			}
			if !slices.Contains(ci.Protocols, p.ID()) {
				ci.Protocols = append(ci.Protocols, p.ID())
			}
			for _, f := range c.Formats {
				if sf := formatOf(f); sf != 0 && !slices.Contains(ci.Formats, sf) {
					ci.Formats = append(ci.Formats, sf)
				}
			}
		}
	}
	resp := &adminv1.ListClientsResponse{}
	for _, id := range order {
		resp.Clients = append(resp.Clients, byID[id])
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) TestUserAgent(ctx context.Context, req *connect.Request[adminv1.TestUserAgentRequest]) (*connect.Response[adminv1.TestUserAgentResponse], error) {
	if len(req.Msg.UserAgent) > 1024 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user agent is too long"))
	}
	rule, format, browser := Choose(s.settings.Get(ctx), req.Msg.UserAgent)
	return connect.NewResponse(&adminv1.TestUserAgentResponse{RuleIndex: int32(rule), Format: format, Browser: browser}), nil
}
