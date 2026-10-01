// Package dns is the panel's DNS preset module: built-in and custom presets, the DnsService admin
// API, and the rule that picks the preset that applies to a user (user, then group, then the instance
// default, then the built-in default). Turning a preset into what a client understands lives next to it:
// the provider catalog (providers.go), Resolve (transport.go: which endpoints a given client gets for the
// preset's preferred transport), and HappRouting for Happ (happ.go); the Mihomo and AmneziaWG renderers
// only add callers of Effective and Preset.EndpointsFor.
//
// The instance default is stored here (setting "dns.default_preset"); SubscriptionService reads and writes
// SubscriptionSettings.default_dns_preset_id through DefaultPresetID / SetDefaultPresetID so there is one
// source of truth.
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Preset is a DNS preset ready for use (validated, servers and split decoded).
type Preset struct {
	ID, Name, Description string
	Builtin               bool
	Servers               []Server
	Split                 []SplitRule
	IPv4Only              bool
	// SplitDirect: the split rules' domains bypass the VPN (Happ can only send a split resolver's domains
	// direct). Off = clients that cannot keep the split proxied ignore it.
	SplitDirect bool
	// Transport is the preferred transport (plain, dot, doh); see Resolve. Never empty for a stored preset.
	Transport Kind
}

// Source tells where an effective preset comes from.
type Source string

const (
	SourceUser    Source = store.DNSSourceUser
	SourceGroup   Source = store.DNSSourceGroup
	SourceDefault Source = store.DNSSourceDefault
)

// Proto converts a source to the API enum.
func (s Source) Proto() adminv1.DnsSource {
	switch s {
	case SourceUser:
		return adminv1.DnsSource_DNS_SOURCE_USER
	case SourceGroup:
		return adminv1.DnsSource_DNS_SOURCE_GROUP
	case SourceDefault:
		return adminv1.DnsSource_DNS_SOURCE_DEFAULT
	}
	return adminv1.DnsSource_DNS_SOURCE_UNSPECIFIED
}

// ErrUnknownPreset is returned when a preset id does not exist.
var ErrUnknownPreset = errors.New("dns: unknown preset")

// Service implements DnsService and answers "which preset applies to this user".
type Service struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time
}

// New builds the service.
func New(st *store.Store) *Service {
	return &Service{st: st, log: slog.Default(), now: time.Now}
}

// Handler returns the Connect path and handler of DnsService (session checks are added by the HTTP server).
func (s *Service) Handler() (string, http.Handler) {
	return adminv1connect.NewDnsServiceHandler(s, connect.WithReadMaxBytes(1<<20))
}

func fromRow(r store.DNSPreset) (Preset, error) {
	p := Preset{ID: r.ID, Name: r.Name, Description: r.Description, Builtin: r.Builtin, IPv4Only: r.IPv4Only, SplitDirect: r.SplitDirect, Transport: Kind(r.Transport)}
	if p.Transport == "" {
		p.Transport = KindPlain
	}
	if err := json.Unmarshal([]byte(r.ServersJSON), &p.Servers); err != nil {
		return p, fmt.Errorf("preset %s servers: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(r.SplitJSON), &p.Split); err != nil {
		return p, fmt.Errorf("preset %s split: %w", r.ID, err)
	}
	return p, nil
}

func toRow(p Preset) (store.DNSPreset, error) {
	if p.Servers == nil {
		p.Servers = []Server{}
	}
	if p.Split == nil {
		p.Split = []SplitRule{}
	}
	sv, err := json.Marshal(p.Servers)
	if err != nil {
		return store.DNSPreset{}, err
	}
	sp, err := json.Marshal(p.Split)
	if err != nil {
		return store.DNSPreset{}, err
	}
	if p.Transport == "" {
		p.Transport = KindPlain
	}
	return store.DNSPreset{ID: p.ID, Name: p.Name, Description: p.Description, Builtin: p.Builtin, IPv4Only: p.IPv4Only, SplitDirect: p.SplitDirect,
		Transport: string(p.Transport), ServersJSON: string(sv), SplitJSON: string(sp)}, nil
}

// Effective returns the preset that applies to a user and where it comes from: the user's own, else the
// user's group's, else the instance default, else the built-in default. store.ErrNotFound for an unknown user.
func (s *Service) Effective(ctx context.Context, userID string) (Preset, Source, error) {
	d := s.st.DNS()
	own, group, err := d.UserRefs(ctx, userID)
	if err != nil {
		return Preset{}, "", err
	}
	l, err := d.Lookup(ctx)
	if err != nil {
		return Preset{}, "", err
	}
	id, _, src := l.Resolve(own, group)
	row, err := d.Get(ctx, id)
	if err != nil {
		return Preset{}, "", err
	}
	p, err := fromRow(row)
	return p, Source(src), err
}

// DefaultPresetID returns the instance default as chosen by the admin ("" = the built-in default, also
// when the stored id no longer exists).
func (s *Service) DefaultPresetID(ctx context.Context) (string, error) {
	l, err := s.st.DNS().Lookup(ctx)
	if err != nil {
		return "", err
	}
	id, err := s.st.DNS().DefaultID(ctx)
	if err != nil {
		return "", err
	}
	if _, ok := l.Names[id]; !ok {
		return "", nil
	}
	return id, nil
}

// SetDefaultPresetID sets the instance default; "" selects the built-in default. ErrUnknownPreset when the
// id does not exist. A real change is written to the audit log (the settings page saves it with every edit).
func (s *Service) SetDefaultPresetID(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id != "" {
		ok, err := s.st.DNS().Exists(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrUnknownPreset
		}
	}
	old, _ := s.DefaultPresetID(ctx)
	if err := s.st.DNS().SetDefaultID(ctx, id); err != nil {
		return err
	}
	if old != id {
		name := ""
		if l, err := s.st.DNS().Lookup(ctx); err == nil {
			name = l.Names[id]
		}
		s.audit(ctx, "preset_default", map[string]any{"preset": id, "name": name})
	}
	return nil
}

// audit writes one row of who changed the presets (an admin, or a token: auth.AdminFrom names both). No server lists.
func (s *Service) audit(ctx context.Context, action string, params map[string]any) {
	b, _ := json.Marshal(params)
	e := store.AuditEntry{Actor: "anonymous", Action: action, Params: string(b), Result: "ok"}
	if a, ok := auth.AdminFrom(ctx); ok {
		e.Actor = a.ID
	}
	if ip := auth.ClientIPFrom(ctx); ip.IsValid() {
		e.IP = ip.String()
	}
	if err := s.st.Audit(ctx, s.now(), e); err != nil {
		s.log.Warn("dns: audit", "action", action, "err", err)
	}
}

// ---- DnsService ----

func (s *Service) internal(op string, err error) error {
	s.log.Error("dns: "+op, "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// fail maps a validation or storage error to a Connect error.
func (s *Service) fail(op string, err error) error {
	var v validationError
	switch {
	case errors.As(err, &v):
		return connect.NewError(connect.CodeInvalidArgument, v)
	case errors.Is(err, store.ErrDNSExists): // the codes are worded by the admin UI ("err.<code>", web/src/lib/errors.ts)
		return connect.NewError(connect.CodeAlreadyExists, errors.New("name_taken"))
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("preset not found"))
	}
	return s.internal(op, err)
}

func kindToProto(k Kind) adminv1.DnsServerKind {
	switch k {
	case KindDoH:
		return adminv1.DnsServerKind_DNS_SERVER_KIND_DOH
	case KindDoT:
		return adminv1.DnsServerKind_DNS_SERVER_KIND_DOT
	}
	return adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN
}

// serverToProto: a catalog server carries its variant id, plus a plain (or DoH) address so that a client that
// predates the catalog still shows a usable server.
func serverToProto(s Server) *adminv1.DnsServer {
	if s.Variant != "" {
		out := &adminv1.DnsServer{ProviderVariant: s.Variant}
		if v, _, ok := LookupVariant(s.Variant); ok {
			k, a := v.legacy()
			out.Kind, out.Address = kindToProto(k), a
		}
		return out
	}
	return &adminv1.DnsServer{Kind: kindToProto(s.Kind), Address: s.Address}
}

func serverFromProto(s *adminv1.DnsServer) Server {
	if s.GetProviderVariant() != "" {
		return Server{Variant: s.GetProviderVariant()}
	}
	var k Kind // "" is rejected by NormalizeServer
	switch s.GetKind() {
	case adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN:
		k = KindPlain
	case adminv1.DnsServerKind_DNS_SERVER_KIND_DOH:
		k = KindDoH
	case adminv1.DnsServerKind_DNS_SERVER_KIND_DOT:
		k = KindDoT
	}
	return Server{Kind: k, Address: s.GetAddress()}
}

func serversFromProto(in []*adminv1.DnsServer) []Server {
	out := make([]Server, len(in))
	for i, s := range in {
		out[i] = serverFromProto(s)
	}
	return out
}

func transportToProto(k Kind) adminv1.DnsTransport {
	switch k {
	case KindDoT:
		return adminv1.DnsTransport_DNS_TRANSPORT_DOT
	case KindDoH:
		return adminv1.DnsTransport_DNS_TRANSPORT_DOH
	}
	return adminv1.DnsTransport_DNS_TRANSPORT_PLAIN
}

// transportFromProto: UNSPECIFIED (a client that predates the field) comes back as "".
func transportFromProto(t adminv1.DnsTransport) Kind {
	switch t {
	case adminv1.DnsTransport_DNS_TRANSPORT_PLAIN:
		return KindPlain
	case adminv1.DnsTransport_DNS_TRANSPORT_DOT:
		return KindDoT
	case adminv1.DnsTransport_DNS_TRANSPORT_DOH:
		return KindDoH
	}
	return ""
}

func categoryToProto(c Category) adminv1.DnsCategory {
	switch c {
	case CategoryRussia:
		return adminv1.DnsCategory_DNS_CATEGORY_RUSSIA
	case CategoryRegular:
		return adminv1.DnsCategory_DNS_CATEGORY_REGULAR
	case CategoryNoAds:
		return adminv1.DnsCategory_DNS_CATEGORY_NO_ADS
	case CategoryFamily:
		return adminv1.DnsCategory_DNS_CATEGORY_FAMILY
	case CategorySecurity:
		return adminv1.DnsCategory_DNS_CATEGORY_SECURITY
	}
	return adminv1.DnsCategory_DNS_CATEGORY_UNSPECIFIED
}

// builtinCategory groups the built-in presets in the UI. Everything else takes the category of its first
// catalog server, or none.
var builtinCategory = map[string]Category{
	"dns_builtin_ru_split":   CategoryRussia,
	"dns_builtin_ru_proxied": CategoryRussia,
	"dns_builtin_yandex":     CategoryRussia,
	"dns_builtin_standard":   CategoryRegular,
	"dns_builtin_adblock":    CategoryNoAds,
	"dns_builtin_family":     CategoryFamily,
	"dns_builtin_quad9":      CategorySecurity,
}

// CategoryOf is the category a preset is listed under.
func CategoryOf(p Preset) Category {
	if p.Builtin {
		if c, ok := builtinCategory[p.ID]; ok {
			return c
		}
	}
	for _, s := range p.Servers {
		if v, _, ok := LookupVariant(s.Variant); ok {
			return v.Category
		}
	}
	return ""
}

func splitFromProto(in []*adminv1.DnsSplitRule) []SplitRule {
	out := make([]SplitRule, len(in))
	for i, r := range in {
		out[i] = SplitRule{Suffixes: r.GetSuffixes(), Servers: serversFromProto(r.GetServers())}
	}
	return out
}

func presetProto(p Preset, users int, isDefault bool) *adminv1.DnsPreset {
	out := &adminv1.DnsPreset{
		Id: p.ID, Name: p.Name, Description: p.Description, Builtin: p.Builtin, Ipv4Only: p.IPv4Only, SplitDirect: p.SplitDirect,
		UserCount: uint32(users), IsDefault: isDefault, PreferredTransport: transportToProto(p.Transport), Category: categoryToProto(CategoryOf(p)),
	}
	for _, s := range p.Servers {
		out.Servers = append(out.Servers, serverToProto(s))
	}
	for _, r := range p.Split {
		pr := &adminv1.DnsSplitRule{Suffixes: r.Suffixes}
		for _, s := range r.Servers {
			pr.Servers = append(pr.Servers, serverToProto(s))
		}
		out.Split = append(out.Split, pr)
	}
	return out
}

// checkName reports store.ErrDNSExists when another preset has this name, ignoring case in any script
// (the NOCASE collation behind the UNIQUE index only folds ASCII, so Cyrillic names need this).
// Check then write, not atomic; two admins racing on one non-ASCII name could both win.
func (s *Service) checkName(ctx context.Context, name, exceptID string) error {
	rows, err := s.st.DNS().List(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.ID != exceptID && strings.EqualFold(r.Name, name) {
			return store.ErrDNSExists
		}
	}
	return nil
}

// one loads a preset with its counters.
func (s *Service) one(ctx context.Context, id string) (*adminv1.DnsPreset, error) {
	d := s.st.DNS()
	row, err := d.Get(ctx, id)
	if err != nil {
		return nil, s.fail("get preset", err)
	}
	p, err := fromRow(row)
	if err != nil {
		return nil, s.internal("decode preset", err)
	}
	counts, err := d.UserCounts(ctx)
	if err != nil {
		return nil, s.internal("user counts", err)
	}
	l, err := d.Lookup(ctx)
	if err != nil {
		return nil, s.internal("lookup", err)
	}
	return presetProto(p, counts[id], l.DefaultID == id), nil
}

func catalogProto() []*adminv1.DnsProvider {
	out := make([]*adminv1.DnsProvider, 0, len(catalog))
	for _, p := range catalog {
		pp := &adminv1.DnsProvider{Id: p.ID, Name: p.Name}
		for _, v := range p.Variants {
			pp.Variants = append(pp.Variants, &adminv1.DnsProviderVariant{
				Id: v.ID, NameRu: v.NameRU, NameEn: v.NameEN, Category: categoryToProto(v.Category),
				Ipv4: v.IPv4, Ipv6: v.IPv6, Plain: !v.NoPlain, DotHost: v.DoTHost, DotPort: uint32(v.DoTPort), DohUrl: v.DoHURL,
				NoteRu: v.NoteRU, NoteEn: v.NoteEN,
			})
		}
		out = append(out, pp)
	}
	return out
}

func clientProto(c Client) adminv1.DnsClient {
	switch c {
	case ClientHapp:
		return adminv1.DnsClient_DNS_CLIENT_HAPP
	case ClientMihomo:
		return adminv1.DnsClient_DNS_CLIENT_MIHOMO
	case ClientAmneziaWG:
		return adminv1.DnsClient_DNS_CLIENT_AMNEZIAWG
	}
	return adminv1.DnsClient_DNS_CLIENT_UNSPECIFIED
}

func clientSupportProto() []*adminv1.ClientDnsSupport {
	out := make([]*adminv1.ClientDnsSupport, 0, len(Clients))
	for _, c := range Clients {
		cs := &adminv1.ClientDnsSupport{Client: clientProto(c), Ipv6: c.IPv6()}
		for _, k := range chain { // DoH, DoT, plain: the order the fallback walks
			if c.Supports(k) {
				cs.Transports = append(cs.Transports, transportToProto(k))
			}
		}
		out = append(out, cs)
	}
	return out
}

func (s *Service) ListDnsPresets(ctx context.Context, _ *connect.Request[adminv1.ListDnsPresetsRequest]) (*connect.Response[adminv1.ListDnsPresetsResponse], error) {
	d := s.st.DNS()
	rows, err := d.List(ctx)
	if err != nil {
		return nil, s.internal("list presets", err)
	}
	counts, err := d.UserCounts(ctx)
	if err != nil {
		return nil, s.internal("user counts", err)
	}
	l, err := d.Lookup(ctx)
	if err != nil {
		return nil, s.internal("lookup", err)
	}
	resp := &adminv1.ListDnsPresetsResponse{Providers: catalogProto(), ClientSupport: clientSupportProto()}
	for _, r := range rows {
		p, err := fromRow(r)
		if err != nil {
			return nil, s.internal("decode preset", err)
		}
		resp.Presets = append(resp.Presets, presetProto(p, counts[p.ID], l.DefaultID == p.ID))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) CreateDnsPreset(ctx context.Context, req *connect.Request[adminv1.CreateDnsPresetRequest]) (*connect.Response[adminv1.CreateDnsPresetResponse], error) {
	m := req.Msg
	name, desc, servers, split, err := Clean(m.Name, m.Description, serversFromProto(m.Servers), splitFromProto(m.Split))
	if err != nil {
		return nil, s.fail("create preset", err)
	}
	transport, err := NormalizeTransport(transportFromProto(m.PreferredTransport))
	if err != nil {
		return nil, s.fail("create preset", err)
	}
	row, err := toRow(Preset{ID: store.NewID("dns_"), Name: name, Description: desc, Servers: servers, Split: split, IPv4Only: m.Ipv4Only, SplitDirect: m.SplitDirect, Transport: transport})
	if err != nil {
		return nil, s.internal("encode preset", err)
	}
	row.CreatedAt = s.now()
	row.UpdatedAt = row.CreatedAt
	if err := s.checkName(ctx, name, ""); err != nil {
		return nil, s.fail("create preset", err)
	}
	if err := s.st.DNS().Create(ctx, row); err != nil {
		return nil, s.fail("create preset", err)
	}
	s.audit(ctx, "preset_create", map[string]any{"preset": row.ID, "name": name})
	p, err := s.one(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CreateDnsPresetResponse{Preset: p}), nil
}

func (s *Service) UpdateDnsPreset(ctx context.Context, req *connect.Request[adminv1.UpdateDnsPresetRequest]) (*connect.Response[adminv1.UpdateDnsPresetResponse], error) {
	m := req.Msg
	name, desc, servers, split, err := Clean(m.Name, m.Description, serversFromProto(m.Servers), splitFromProto(m.Split))
	if err != nil {
		return nil, s.fail("update preset", err)
	}
	transport := transportFromProto(m.PreferredTransport)
	if transport == "" { // a client that predates the field: keep what is stored
		old, err := s.st.DNS().Get(ctx, m.Id)
		if err != nil {
			return nil, s.fail("update preset", err)
		}
		transport = Kind(old.Transport)
	}
	if transport, err = NormalizeTransport(transport); err != nil {
		return nil, s.fail("update preset", err)
	}
	row, err := toRow(Preset{ID: m.Id, Name: name, Description: desc, Servers: servers, Split: split, IPv4Only: m.Ipv4Only, SplitDirect: m.SplitDirect, Transport: transport})
	if err != nil {
		return nil, s.internal("encode preset", err)
	}
	row.UpdatedAt = s.now()
	if err := s.checkName(ctx, name, m.Id); err != nil {
		return nil, s.fail("update preset", err)
	}
	if err := s.st.DNS().Update(ctx, row); err != nil {
		return nil, s.fail("update preset", err)
	}
	s.audit(ctx, "preset_update", map[string]any{"preset": m.Id, "name": name})
	p, err := s.one(ctx, m.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateDnsPresetResponse{Preset: p}), nil
}

func (s *Service) DeleteDnsPreset(ctx context.Context, req *connect.Request[adminv1.DeleteDnsPresetRequest]) (*connect.Response[adminv1.DeleteDnsPresetResponse], error) {
	d := s.st.DNS()
	row, err := d.Get(ctx, req.Msg.Id)
	if err != nil {
		return nil, s.fail("get preset", err)
	}
	if row.Builtin {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("preset_builtin"))
	}
	l, err := d.Lookup(ctx)
	if err != nil {
		return nil, s.internal("lookup", err)
	}
	if l.DefaultID == row.ID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("preset_is_default"))
	}
	if err := d.Delete(ctx, row.ID); err != nil {
		return nil, s.fail("delete preset", err)
	}
	s.audit(ctx, "preset_delete", map[string]any{"preset": row.ID, "name": row.Name})
	return connect.NewResponse(&adminv1.DeleteDnsPresetResponse{}), nil
}
