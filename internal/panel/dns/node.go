package dns

import (
	"context"
	"errors"
	"slices"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// DNS per node (migration 00044). The owner offers presets on a node and marks one as the default; a person picks one of
// them on the user page. The effective preset of a person on a node is, in this order:
//
//  1. the person's pick, while the node still offers it;
//  2. the node's default;
//  3. what applies to the person anyway (Effective: their own preset, the group's, the instance default).
//
// A node that offers nothing has no choice, and only rule 3 applies: the same as before this existed.

// MaxNodeOptions is how many presets one node may offer.
const MaxNodeOptions = 20

// NodeChoices is what decides the DNS of one person on each node, read once: the offers of every node and the
// person's picks.
type NodeChoices struct {
	offers map[string][]store.NodeDNSOption
	picks  map[string]store.UserNodeDNS
}

// NodeChoices reads the offers of every node and the picks of one person.
func (s *Service) NodeChoices(ctx context.Context, userID string) (NodeChoices, error) {
	d := s.st.DNS()
	offers, err := d.NodeOptions(ctx)
	if err != nil {
		return NodeChoices{}, err
	}
	picks, err := d.UserNodeChoices(ctx, userID)
	if err != nil {
		return NodeChoices{}, err
	}
	return NewNodeChoices(offers, picks), nil
}

// NewNodeChoices is NodeChoices over rows already read.
func NewNodeChoices(offers map[string][]store.NodeDNSOption, picks map[string]store.UserNodeDNS) NodeChoices {
	return NodeChoices{offers: offers, picks: picks}
}

// Options lists the presets a node offers, in the owner's order; nil when it offers nothing.
func (c NodeChoices) Options(nodeID string) []string {
	var out []string
	for _, o := range c.offers[nodeID] {
		out = append(out, o.PresetID)
	}
	return out
}

// Default is the node's default preset ("" = none).
func (c NodeChoices) Default(nodeID string) string {
	for _, o := range c.offers[nodeID] {
		if o.Default {
			return o.PresetID
		}
	}
	return ""
}

// Pick is the person's pick on the node while the node still offers it, else "".
func (c NodeChoices) Pick(nodeID string) string {
	p, ok := c.picks[nodeID]
	if !ok || !slices.Contains(c.Options(nodeID), p.PresetID) {
		return ""
	}
	return p.PresetID
}

// Choice is the person's raw pick on the node, offered or not ("" = none).
func (c NodeChoices) Choice(nodeID string) string { return c.picks[nodeID].PresetID }

// Effective is the preset id that applies on the node by rules 1 and 2; "" when the node has no choice or no default and
// no pick (rule 3, the caller's fallback, applies).
func (c NodeChoices) Effective(nodeID string) string {
	if p := c.Pick(nodeID); p != "" {
		return p
	}
	return c.Default(nodeID)
}

// UpdatedMs is when the person picked on the node (Unix milliseconds, 0 = never).
func (c NodeChoices) UpdatedMs(nodeID string) int64 { return c.picks[nodeID].UpdatedMs }

// PresetByID loads one preset; store.ErrNotFound when it is gone.
func (s *Service) PresetByID(ctx context.Context, id string) (Preset, error) {
	row, err := s.st.DNS().Get(ctx, id)
	if err != nil {
		return Preset{}, err
	}
	return fromRow(row)
}

// Presets loads every preset, by id, in one query (a page names every preset its nodes offer). A row that cannot be
// decoded is left out, as a preset that is gone.
func (s *Service) Presets(ctx context.Context) (map[string]Preset, error) {
	rows, err := s.st.DNS().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Preset, len(rows))
	for _, row := range rows {
		if p, err := fromRow(row); err == nil {
			out[row.ID] = p
		}
	}
	return out, nil
}

// EffectiveOnNode is the preset that applies to a person on a node (rules 1 to 3 above).
func (s *Service) EffectiveOnNode(ctx context.Context, userID, nodeID string) (Preset, error) {
	c, err := s.NodeChoices(ctx, userID)
	if err != nil {
		return Preset{}, err
	}
	if id := c.Effective(nodeID); id != "" {
		if p, err := s.PresetByID(ctx, id); err == nil {
			return p, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return Preset{}, err
		}
	}
	p, _, err := s.Effective(ctx, userID)
	return p, err
}

// ---- words for the page ----

// The built-in presets have one stored name, Russian, and a description of the form "Russian\nEnglish". The page speaks
// the language of the visitor: English names are these while the stored name is still the stock one (an owner may have
// renamed it; that name is shown as it is). They are the names the admin shows an English owner
// (web/src/screens/subscriptions/model.ts builtinNames); the provider catalog's own English names ("Default", "Basic")
// do not say what a preset is for.
var builtinNames = map[string]struct{ ru, en string }{
	"dns_builtin_ru_split":   {"Россия: .ru напрямую", "Russia: .ru direct"},
	"dns_builtin_ru_proxied": {"Россия: всё через VPN", "Russia: all through VPN"},
	"dns_builtin_standard":   {"Cloudflare + Google", "Cloudflare + Google"},
	"dns_builtin_adblock":    {"AdGuard: без рекламы", "AdGuard: no ads"},
	"dns_builtin_family":     {"AdGuard Family: без рекламы и 18+", "AdGuard Family: no ads, no 18+"},
	"dns_builtin_quad9":      {"Quad9: защита от вредоносных", "Quad9: malware blocking"},
	"dns_builtin_yandex":     {"Яндекс DNS", "Yandex DNS"},
}

// NameIn is the name of the preset in the language of the page ("ru" or anything else = English).
func (p Preset) NameIn(lang string) string {
	if b, ok := builtinNames[p.ID]; ok && p.Builtin && lang != "ru" && p.Name == b.ru {
		return b.en
	}
	return p.Name
}

// DescriptionIn is the description in the language of the page. A built-in one is stored as "Russian\nEnglish" and is
// split; a description of an owner's own preset is one text, shown in any language.
func (p Preset) DescriptionIn(lang string) string {
	ru, en, ok := strings.Cut(p.Description, "\n")
	if !p.Builtin || !ok {
		return strings.TrimSpace(p.Description)
	}
	if lang == "ru" {
		return strings.TrimSpace(ru)
	}
	return strings.TrimSpace(en)
}

// ---- DnsService: what the owner offers per node, and what people picked ----

func (s *Service) nodeOptionsProto(id string, offers []store.NodeDNSOption) *adminv1.NodeDnsOptions {
	out := &adminv1.NodeDnsOptions{NodeId: id}
	for _, o := range offers {
		out.PresetIds = append(out.PresetIds, o.PresetID)
		if o.Default {
			out.DefaultPresetId = o.PresetID
		}
	}
	return out
}

func (s *Service) ListNodeDnsOptions(ctx context.Context, _ *connect.Request[adminv1.ListNodeDnsOptionsRequest]) (*connect.Response[adminv1.ListNodeDnsOptionsResponse], error) {
	offers, err := s.st.DNS().NodeOptions(ctx)
	if err != nil {
		return nil, s.internal("node options", err)
	}
	ids := make([]string, 0, len(offers))
	for id := range offers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	resp := &adminv1.ListNodeDnsOptionsResponse{}
	for _, id := range ids {
		resp.Nodes = append(resp.Nodes, s.nodeOptionsProto(id, offers[id]))
	}
	return connect.NewResponse(resp), nil
}

func invalidArg(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}

func (s *Service) SetNodeDnsOptions(ctx context.Context, req *connect.Request[adminv1.SetNodeDnsOptionsRequest]) (*connect.Response[adminv1.SetNodeDnsOptionsResponse], error) {
	m := req.Msg
	d := s.st.DNS()
	names := map[string]string{}
	if ok, err := d.NodeExists(ctx, m.NodeId); err != nil {
		return nil, s.internal("node", err)
	} else if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if len(m.PresetIds) > MaxNodeOptions {
		return nil, invalidArg("preset_ids: too many presets")
	}
	l, err := d.Lookup(ctx)
	if err != nil {
		return nil, s.internal("lookup", err)
	}
	seen := map[string]bool{}
	for _, id := range m.PresetIds {
		if _, ok := l.Names[id]; !ok {
			return nil, invalidArg("preset_ids: unknown preset")
		}
		if seen[id] {
			return nil, invalidArg("preset_ids: a preset is listed twice")
		}
		seen[id] = true
		names[id] = l.Names[id]
	}
	if m.DefaultPresetId != "" && !seen[m.DefaultPresetId] {
		return nil, invalidArg("default_preset_id: the default must be one of the offered presets")
	}
	before, err := d.NodeOptions(ctx)
	if err != nil {
		return nil, s.internal("node options", err)
	}
	if err := d.SetNodeOptions(ctx, m.NodeId, m.PresetIds, m.DefaultPresetId); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node or preset not found"))
	} else if err != nil {
		return nil, s.internal("set node options", err)
	}
	// One row: which node, how many presets and the default. The node's own name is for the owner, who named it.
	if !sameOffers(before[m.NodeId], m.PresetIds, m.DefaultPresetId) {
		params := map[string]any{"node": m.NodeId, "presets": len(m.PresetIds), "default": m.DefaultPresetId}
		if name, err := s.st.DNS().NodeName(ctx, m.NodeId); err == nil {
			params["node_name"] = name
		}
		s.audit(ctx, "node_dns_options", params)
	}
	offers, err := d.NodeOptions(ctx)
	if err != nil {
		return nil, s.internal("node options", err)
	}
	return connect.NewResponse(&adminv1.SetNodeDnsOptionsResponse{Options: s.nodeOptionsProto(m.NodeId, offers[m.NodeId])}), nil
}

func sameOffers(old []store.NodeDNSOption, ids []string, def string) bool {
	if len(old) != len(ids) {
		return false
	}
	for i, o := range old {
		if o.PresetID != ids[i] || o.Default != (o.PresetID == def) {
			return false
		}
	}
	return true
}

func (s *Service) GetUserDnsChoices(ctx context.Context, req *connect.Request[adminv1.GetUserDnsChoicesRequest]) (*connect.Response[adminv1.GetUserDnsChoicesResponse], error) {
	userID := req.Msg.UserId
	d := s.st.DNS()
	own, group, err := d.UserRefs(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	} else if err != nil {
		return nil, s.internal("user refs", err)
	}
	c, err := s.NodeChoices(ctx, userID)
	if err != nil {
		return nil, s.internal("node choices", err)
	}
	l, err := d.Lookup(ctx)
	if err != nil {
		return nil, s.internal("lookup", err)
	}
	fallback, _, _ := l.Resolve(own, group)
	nodes, err := d.NodeNames(ctx)
	if err != nil {
		return nil, s.internal("node names", err)
	}
	ids := make([]string, 0, len(c.picks))
	for id := range c.picks {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(nodes[a], nodes[b]) })
	resp := &adminv1.GetUserDnsChoicesResponse{}
	for _, nodeID := range ids {
		p := c.picks[nodeID]
		eff := cmpOr(c.Effective(nodeID), fallback)
		resp.Choices = append(resp.Choices, &adminv1.UserDnsChoice{
			NodeId: nodeID, NodeName: nodes[nodeID], PresetId: p.PresetID, PresetName: l.Names[p.PresetID],
			Offered: c.Pick(nodeID) != "", EffectivePresetId: eff, EffectivePresetName: l.Names[eff], UpdatedUnix: p.UpdatedMs / 1000,
		})
	}
	return connect.NewResponse(resp), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Service) ResetUserDnsChoices(ctx context.Context, req *connect.Request[adminv1.ResetUserDnsChoicesRequest]) (*connect.Response[adminv1.ResetUserDnsChoicesResponse], error) {
	d := s.st.DNS()
	if _, _, err := d.UserRefs(ctx, req.Msg.UserId); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	} else if err != nil {
		return nil, s.internal("user refs", err)
	}
	n, err := d.ResetUserNodeChoices(ctx, req.Msg.UserId)
	if err != nil {
		return nil, s.internal("reset user choices", err)
	}
	if n > 0 {
		params := map[string]any{"user": req.Msg.UserId, "removed": n}
		if u, err := s.st.Access().User(ctx, req.Msg.UserId); err == nil {
			params["name"] = u.Name
		}
		s.audit(ctx, "user_dns_choices_reset", params)
	}
	return connect.NewResponse(&adminv1.ResetUserDnsChoicesResponse{Removed: uint32(n)}), nil
}
