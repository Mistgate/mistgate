package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

func (s *Service) protocol(id string) (protocols.Protocol, error) {
	p, ok := s.reg.Get(id)
	if !ok {
		return nil, invalid("unknown protocol %q", id)
	}
	return p, nil
}

func clientApps(p protocols.Protocol) (happ, amnezia bool, apps []adminv1.App) {
	for _, c := range p.Clients() {
		switch c.Client {
		case plugin.ClientHapp:
			happ = true
		case plugin.ClientAmnezia:
			amnezia = true
		}
	}
	if happ {
		apps = append(apps, adminv1.App_APP_HAPP)
	}
	if amnezia {
		apps = append(apps, adminv1.App_APP_AMNEZIA)
	}
	return
}

func (s *Service) ListProtocols(ctx context.Context, _ *connect.Request[adminv1.ListProtocolsRequest]) (*connect.Response[adminv1.ListProtocolsResponse], error) {
	resp := &adminv1.ListProtocolsResponse{}
	for _, p := range s.reg.List() {
		def, err := p.DefaultSettings()
		if err != nil {
			return nil, s.internal("default settings", err)
		}
		// Defaults are shown masked: the real secrets are generated again when a profile is created.
		masked, err := protocols.MaskSecrets(def, s.secretPtrs[p.ID()])
		if err != nil {
			return nil, s.internal("mask defaults", err)
		}
		_, _, apps := clientApps(p)
		resp.Protocols = append(resp.Protocols, &adminv1.ProtocolInfo{
			Id: p.ID(), DisplayName: p.DisplayName(), SettingsSchemaJson: string(p.SettingsSchema()),
			DefaultSettingsJson: string(masked), Apps: apps,
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) profileSummary(ctx context.Context, p store.AccessProfile, inbounds []store.AccessInbound) (*adminv1.ProfileSummary, error) {
	proto, err := s.protocol(p.Protocol)
	if err != nil {
		return nil, s.internal("profile "+p.ID, err)
	}
	merged, err := s.mergedSettings(p)
	if err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	return s.summaryOf(ctx, p, proto, proto.Summary(merged), inbounds)
}

func (s *Service) summaryOf(ctx context.Context, p store.AccessProfile, proto protocols.Protocol, summary string, inbounds []store.AccessInbound) (*adminv1.ProfileSummary, error) {
	happ, amnezia, _ := clientApps(proto)
	users, err := s.st.Access().ProfileUsers(ctx, p.ID, happ, amnezia)
	if err != nil {
		return nil, s.internal("profile users", err)
	}
	out := &adminv1.ProfileSummary{
		Id: p.ID, Name: p.Name, Protocol: p.Protocol, Summary: summary,
		NodeCount: uint32(len(inbounds)), UserCount: uint32(len(users)), Version: p.Version,
	}
	for _, i := range inbounds {
		if i.State == "failed" {
			params := map[string]string{"inbound": i.ID, "error": i.LastError}
			if n, err := s.st.Access().Node(ctx, i.NodeID); err == nil {
				params["node"] = n.Name // "did not start on de1"
			}
			out.Warnings = append(out.Warnings, &adminv1.StatusReason{Code: "inbound_failed", Params: params})
		}
	}
	return out, nil
}

func (s *Service) ListProfiles(ctx context.Context, _ *connect.Request[adminv1.ListProfilesRequest]) (*connect.Response[adminv1.ListProfilesResponse], error) {
	a := s.st.Access()
	ps, err := a.Profiles(ctx)
	if err != nil {
		return nil, s.internal("list profiles", err)
	}
	resp := &adminv1.ListProfilesResponse{}
	for _, p := range ps {
		ins, err := a.InboundsOfProfile(ctx, p.ID)
		if err != nil {
			return nil, s.internal("profile inbounds", err)
		}
		sum, err := s.profileSummary(ctx, p, ins)
		if err != nil {
			return nil, err
		}
		resp.Profiles = append(resp.Profiles, sum)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetProfile(ctx context.Context, req *connect.Request[adminv1.GetProfileRequest]) (*connect.Response[adminv1.GetProfileResponse], error) {
	a := s.st.Access()
	p, err := a.Profile(ctx, req.Msg.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("profile")
	} else if err != nil {
		return nil, s.internal("get profile", err)
	}
	ins, err := a.InboundsOfProfile(ctx, p.ID)
	if err != nil {
		return nil, s.internal("profile inbounds", err)
	}
	sum, err := s.profileSummary(ctx, p, ins)
	if err != nil {
		return nil, err
	}
	merged, err := s.mergedSettings(p)
	if err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	masked, err := protocols.MaskSecrets(merged, s.secretPtrs[p.Protocol])
	if err != nil {
		return nil, s.internal("mask settings", err)
	}
	resp := &adminv1.GetProfileResponse{Profile: sum, SettingsJson: string(masked)}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	for _, f := range full {
		if f.Profile.ID == p.ID {
			resp.Inbounds = append(resp.Inbounds, s.inboundProto(f, merged))
		}
	}
	return connect.NewResponse(resp), nil
}

// resolveSettings lays a client's settings document over base (nil = a new profile, over the defaults), lets the
// plugin normalise the result (save: it will be stored, not only previewed) and validates it. It returns the
// merged document (secrets in place) or a FieldError-carrying RPC error.
func (s *Service) resolveSettings(proto protocols.Protocol, input string, base json.RawMessage, save bool) (json.RawMessage, []protocols.FieldError, error) {
	fresh, err := proto.DefaultSettings()
	if err != nil {
		return nil, nil, s.internal("default settings", err)
	}
	old := base
	if base == nil {
		base = fresh
	}
	if input == "" {
		input = "{}"
	}
	merged, err := protocols.ResolveInput(json.RawMessage(input), base, fresh, s.secretPtrs[proto.ID()])
	if err != nil {
		return nil, nil, invalid("settings_json: %v", err)
	}
	if n, ok := proto.(protocols.SettingsNormalizer); ok {
		if merged, err = n.NormalizeSettings(protocols.NormalizeInput{Input: json.RawMessage(input), Old: old, Merged: merged, Save: save}); err != nil {
			return nil, nil, s.internal("normalize settings", err)
		}
	}
	return merged, proto.Validate(merged), nil
}

// sealSettings splits secrets out of merged settings and encrypts them for the profile row.
func (s *Service) sealSettings(proto protocols.Protocol, id string, merged json.RawMessage) (public string, sealed []byte, err error) {
	pub, secrets, err := protocols.SplitSecrets(merged, s.secretPtrs[proto.ID()])
	if err != nil {
		return "", nil, err
	}
	if len(secrets) > 0 {
		pt, err := json.Marshal(secrets)
		if err != nil {
			return "", nil, err
		}
		sealed = s.vault.Seal(pt, id)
	}
	return string(pub), sealed, nil
}

func (s *Service) CreateProfile(ctx context.Context, req *connect.Request[adminv1.CreateProfileRequest]) (*connect.Response[adminv1.CreateProfileResponse], error) {
	proto, err := s.protocol(req.Msg.Protocol)
	if err != nil {
		return nil, err
	}
	name, err := cleanName("profile", req.Msg.Name)
	if err != nil {
		return nil, err
	}
	merged, errs, err := s.resolveSettings(proto, req.Msg.SettingsJson, nil, true)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, fieldErrors(errs)
	}
	if proto.ID() == awg.ID {
		s.awgNets.Lock() // held through the INSERT below
		defer s.awgNets.Unlock()
		if merged, err = s.checkAWGNetworks(ctx, "", merged, req.Msg.SettingsJson, nil); err != nil {
			return nil, err
		}
	}
	id := store.NewID("prf_")
	pub, sealed, err := s.sealSettings(proto, id, merged)
	if err != nil {
		return nil, s.internal("seal settings", err)
	}
	p := store.AccessProfile{ID: id, Protocol: proto.ID(), Name: name, SettingsJSON: pub, SecretsEnc: sealed, CreatedAt: s.now()}
	switch err := s.st.Access().CreateProfile(ctx, p); {
	case errors.Is(err, store.ErrAccessExists):
		return nil, coded(connect.CodeAlreadyExists, "name_taken")
	case err != nil:
		return nil, s.internal("create profile", err)
	}
	p.Version = 1
	s.audit(ctx, actor(ctx), "profile_create", map[string]any{"profile": id, "name": name, "protocol": proto.ID()})
	sum, err := s.summaryOf(ctx, p, proto, proto.Summary(merged), nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CreateProfileResponse{Profile: sum}), nil
}

// profileImpact computes what changing a profile from oldMerged to newMerged does to its inbounds and users.
func (s *Service) profileImpact(ctx context.Context, p store.AccessProfile, proto protocols.Protocol, oldMerged, newMerged json.RawMessage) (*adminv1.ProfileImpact, error) {
	a := s.st.Access()
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	impact := &adminv1.ProfileImpact{CriticalFields: protocols.ChangedPointers(oldMerged, newMerged, s.criticalPtrs[p.Protocol])}
	restartedNodes := map[string]bool{}
	for _, f := range full {
		if f.Profile.ID != p.ID || !f.Inbound.Enabled {
			continue
		}
		oldSpec, oldErr := s.buildSpec(f, oldMerged)
		newSpec, newErr := s.buildSpec(f, newMerged)
		if newErr != nil {
			return nil, invalid("node %s: %v", f.Node.Name, newErr)
		}
		if oldErr != nil || specChanged(oldSpec, newSpec) {
			impact.InboundsRestarted++
			restartedNodes[f.Node.ID] = true
		}
	}
	if len(restartedNodes) == 0 {
		return impact, nil
	}
	happ, amnezia, _ := clientApps(proto)
	users, err := a.ProfileUsers(ctx, p.ID, happ, amnezia)
	if err != nil {
		return nil, s.internal("profile users", err)
	}
	// A critical change of a protocol the subscription carries breaks the server for everyone who has it, until their
	// app refreshes the subscription: all of them are named. Otherwise the people online there reconnect (a per-device
	// protocol adds its key owners in addReissue).
	everyone := len(impact.CriticalFields) > 0 && !protocols.IsPerDevice(proto)
	online := s.online.OnlineUsers()
	for _, u := range users {
		node, ok := online[u.ID]
		here := ok && restartedNodes[node]
		if here {
			impact.UsersOnline++
		}
		if (here || everyone) && len(impact.AffectedUserNames) < 50 {
			impact.AffectedUserNames = append(impact.AffectedUserNames, u.Name)
		}
	}
	return impact, nil
}

// addReissue counts the devices whose issued config an x-critical change breaks and adds their owners to the
// names the confirmation window shows (at most 50 in all).
func (s *Service) addReissue(ctx context.Context, impact *adminv1.ProfileImpact, profileID string) error {
	n, users, err := s.st.Access().ProfileLiveAWG(ctx, profileID, 50)
	if err != nil {
		return s.internal("profile devices", err)
	}
	impact.DevicesNeedReissue = uint32(n)
	for _, name := range users {
		if len(impact.AffectedUserNames) >= 50 {
			break
		}
		if !slices.Contains(impact.AffectedUserNames, name) {
			impact.AffectedUserNames = append(impact.AffectedUserNames, name)
		}
	}
	return nil
}

// specChanged compares everything that reaches the node except the version counter.
func specChanged(a, b plugin.InboundSpec) bool {
	return a.Enabled != b.Enabled || a.Listen != b.Listen || a.TLS != b.TLS || a.Egress != b.Egress ||
		!bytes.Equal(a.Settings, b.Settings)
}

func (s *Service) UpdateProfile(ctx context.Context, req *connect.Request[adminv1.UpdateProfileRequest]) (*connect.Response[adminv1.UpdateProfileResponse], error) {
	m := req.Msg
	a := s.st.Access()
	p, err := a.Profile(ctx, m.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("profile")
	} else if err != nil {
		return nil, s.internal("get profile", err)
	}
	if p.Version != m.ExpectedVersion {
		return nil, coded(connect.CodeAborted, "stale_version")
	}
	proto, err := s.protocol(p.Protocol)
	if err != nil {
		return nil, s.internal("profile protocol", err)
	}
	oldMerged, err := s.mergedSettings(p)
	if err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	newMerged := oldMerged
	if m.SettingsJson != nil {
		if proto.ID() == awg.ID {
			s.awgNets.Lock() // held through the UPDATE below
			defer s.awgNets.Unlock()
		}
		var errs []protocols.FieldError
		if newMerged, errs, err = s.resolveSettings(proto, *m.SettingsJson, oldMerged, true); err != nil {
			return nil, err
		} else if len(errs) > 0 {
			return nil, fieldErrors(errs)
		}
		if proto.ID() == awg.ID {
			if _, err = s.checkAWGNetworks(ctx, p.ID, newMerged, "", oldMerged); err != nil {
				return nil, err
			}
		}
	}
	next := p
	if m.Name != nil {
		if next.Name, err = cleanName("profile", *m.Name); err != nil {
			return nil, err
		}
	}
	settingsChanged := !bytes.Equal(oldMerged, newMerged)
	impact, err := s.profileImpact(ctx, p, proto, oldMerged, newMerged)
	if err != nil {
		return nil, err
	}
	// An x-critical change of a per-device profile breaks every config already issued: its devices become stale.
	reissue := protocols.IsPerDevice(proto) && settingsChanged && len(impact.CriticalFields) > 0
	if reissue {
		if err := s.addReissue(ctx, impact, p.ID); err != nil {
			return nil, err
		}
	}
	inbounds, err := a.InboundsOfProfile(ctx, p.ID)
	if err != nil {
		return nil, s.internal("profile inbounds", err)
	}
	type changedInboundPort struct {
		node        store.AccessNode
		listen      plugin.Listen
		others      []nodeListen
		portChanged bool
	}
	var changedPorts []changedInboundPort
	var portWarnings []*adminv1.StatusReason
	var lossyOverrides []changedInboundPort
	if settingsChanged {
		full, err := a.InboundsFull(ctx, "")
		if err != nil {
			return nil, s.internal("profile inbounds", err)
		}
		for _, f := range full {
			if f.Profile.ID != p.ID || !f.Inbound.Enabled {
				continue
			}
			oldSpec, oldErr := s.buildSpec(f, oldMerged)
			newSpec, newErr := s.buildSpec(f, newMerged)
			if newErr != nil {
				return nil, invalid("node %s: %v", f.Node.Name, newErr)
			}
			if oldErr == nil && !specChanged(oldSpec, newSpec) {
				continue
			}
			others, _, err := s.nodeInbounds(ctx, f.Node.ID, f.Inbound.ID)
			if err != nil {
				return nil, err
			}
			changedPorts = append(changedPorts, changedInboundPort{
				node: f.Node, listen: newSpec.Listen, others: others,
				portChanged: oldErr != nil || oldSpec.Listen.Port != newSpec.Listen.Port,
			})
		}
	}
	if len(changedPorts) > 0 {
		nodeIDs := make([]string, 0, len(changedPorts))
		seen := make(map[string]bool, len(changedPorts))
		for _, changed := range changedPorts {
			if !seen[changed.node.ID] {
				seen[changed.node.ID] = true
				nodeIDs = append(nodeIDs, changed.node.ID)
			}
		}
		portChecks, err := s.readPortChecks(ctx, nodeIDs...)
		if err != nil {
			return nil, s.internal("read UDP port checks", err)
		}
		for _, changed := range changedPorts {
			checks := portChecks[changed.node.ID]
			if m.DryRun {
				if bad, ok := cachedPortLossy(checks, changed.listen.Port, s.now()); ok {
					if changed.portChanged && !m.AllowLossyPort {
						free := freePortFor(p.Protocol, changed.listen, changed.others, checks, s.now())
						return nil, portLossyRefusal(ctx, s, changed.node.Name, bad, free)
					}
					portWarnings = append(portWarnings, s.portLossyWarning(ctx, changed.node.Name, bad))
				}
				continue
			}
			if changed.portChanged {
				warning, override, err := s.checkPort(ctx, changed.node, p.Protocol, changed.listen, changed.others, checks, m.AllowLossyPort)
				if err != nil {
					return nil, err
				}
				if warning != nil {
					portWarnings = append(portWarnings, warning)
				}
				if override {
					lossyOverrides = append(lossyOverrides, changed)
				}
			} else if bad, ok := cachedPortLossy(checks, changed.listen.Port, s.now()); ok {
				portWarnings = append(portWarnings, s.portLossyWarning(ctx, changed.node.Name, bad))
			}
		}
	}
	if !m.DryRun {
		pub, sealed, err := s.sealSettings(proto, p.ID, newMerged)
		if err != nil {
			return nil, s.internal("seal settings", err)
		}
		next.SettingsJSON, next.SecretsEnc = pub, sealed
		switch next, err = a.UpdateProfile(ctx, next, m.ExpectedVersion, settingsChanged, reissue, s.now()); {
		case errors.Is(err, store.ErrAccessVersion):
			return nil, coded(connect.CodeAborted, "stale_version")
		case errors.Is(err, store.ErrAccessExists):
			return nil, coded(connect.CodeAlreadyExists, "name_taken")
		case errors.Is(err, store.ErrNotFound):
			return nil, notFound("profile")
		case err != nil:
			return nil, s.internal("update profile", err)
		}
		if settingsChanged && len(inbounds) > 0 {
			s.notify.StateChanged()
		}
		s.audit(ctx, actor(ctx), "profile_update", map[string]any{"profile": p.ID, "name": next.Name, "settings_changed": settingsChanged, "reissue": reissue})
		for _, changed := range lossyOverrides {
			s.auditLossyPort(ctx, changed.node.Name, changed.listen.Port)
		}
	}
	sum, err := s.summaryOf(ctx, next, proto, proto.Summary(newMerged), inbounds)
	if err != nil {
		return nil, err
	}
	sum.Warnings = append(sum.Warnings, portWarnings...)
	return connect.NewResponse(&adminv1.UpdateProfileResponse{Profile: sum, Impact: impact}), nil
}

func (s *Service) PreviewProfile(ctx context.Context, req *connect.Request[adminv1.PreviewProfileRequest]) (*connect.Response[adminv1.PreviewProfileResponse], error) {
	proto, err := s.protocol(req.Msg.Protocol)
	if err != nil {
		return nil, err
	}
	// Masked or empty secrets resolve to generated ones: the form is previewed, nothing is stored.
	merged, errs, err := s.resolveSettings(proto, req.Msg.SettingsJson, nil, false)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.PreviewProfileResponse{ClientLabel: "Subscription link · URI list"}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, &adminv1.FieldError{Pointer: e.Pointer, Code: e.Code, Message: e.Message})
	}
	if len(errs) > 0 {
		return connect.NewResponse(resp), nil
	}
	if proto.ID() == awg.ID && awgNetworkAllocationNeeded(req.Msg.SettingsJson) {
		// New AWG profiles leave their client networks unset so that the same free pair is used in the preview and on save.
		merged, err = s.checkAWGNetworks(ctx, "", merged, req.Msg.SettingsJson, nil)
		if err != nil {
			return nil, err
		}
	}
	resp.Summary = proto.Summary(merged)
	if proto.ID() == awg.ID {
		resp.Warnings, resp.ObfuscationScore = awgAdvice(merged)
	}
	node := store.AccessNode{Name: "node", Address: "example.com"}
	if id := req.Msg.NodeId; id != "" {
		if node, err = s.st.Access().Node(ctx, id); errors.Is(err, store.ErrNotFound) {
			return nil, notFound("node")
		} else if err != nil {
			return nil, s.internal("get node", err)
		}
	}
	in := protocols.InboundInput{Profile: protocols.ProfileView{Settings: merged}, Node: nodeView(node), Enabled: true, SpecVersion: 1}
	if ini, ok := proto.(protocols.InboundInitializer); ok {
		// A throwaway key, only so that the inbound can be built; the preview never shows it.
		made, err := ini.InitInbound(protocols.InboundInitInput{Profile: in.Profile, Node: in.Node})
		if err != nil {
			return nil, s.internal("preview key", err)
		}
		in.PluginState, in.PluginPublic = made.State, made.Public
	}
	spec, err := proto.BuildInbound(in)
	if err != nil {
		resp.Errors = append(resp.Errors, &adminv1.FieldError{Pointer: "/sni", Code: "invalid", Message: err.Error()})
		return connect.NewResponse(resp), nil
	}
	format := plugin.FormatURIList
	if protocols.IsPerDevice(proto) { // an L3 protocol: Happ cannot carry it, the .conf is what Amnezia users import
		format, resp.ClientLabel = plugin.FormatAWGConf, "AmneziaVPN · .conf"
	}
	frag, ok := proto.Render(protocols.RenderInput{
		Format: format, Settings: merged, UserName: "user", MaskSecrets: true,
		Inbound: inboundView(nodeView(node), spec, ""), NodeAddr: node.Address,
	})
	if ok {
		resp.ClientPreview = string(frag.Data)
	}
	return connect.NewResponse(resp), nil
}

func inboundView(node protocols.NodeView, spec plugin.InboundSpec, pin string) protocols.InboundView {
	return protocols.InboundView{
		ID: spec.ID, Node: node, Port: spec.Listen.Port, HopFrom: spec.Listen.HopFrom, HopTo: spec.Listen.HopTo,
		TLSServerName: spec.TLS.ServerName, TLSMode: spec.TLS.Mode, CertPinSHA256: pin,
	}
}

func (s *Service) DeleteProfile(ctx context.Context, req *connect.Request[adminv1.DeleteProfileRequest]) (*connect.Response[adminv1.DeleteProfileResponse], error) {
	a := s.st.Access()
	gone, _ := a.Profile(ctx, req.Msg.ProfileId) // its name, for the audit row; a miss is answered by the delete itself
	switch err := a.DeleteProfile(ctx, req.Msg.ProfileId, s.now()); {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("profile")
	case errors.Is(err, store.ErrAccessInUse):
		ins, err := a.InboundsOfProfile(ctx, req.Msg.ProfileId)
		if err != nil {
			return nil, s.internal("profile inbounds", err)
		}
		var nodes []string // a retired node keeps its inbounds, so it is named too
		for _, in := range ins {
			if n, err := a.Node(ctx, in.NodeID); err == nil {
				nodes = append(nodes, n.Name)
			}
		}
		slices.Sort(nodes)
		return nil, coded(connect.CodeFailedPrecondition, "profile_deployed", "nodes", strings.Join(nodes, ", "))
	case err != nil:
		return nil, s.internal("delete profile", err)
	}
	s.audit(ctx, actor(ctx), "profile_delete", map[string]any{"profile": req.Msg.ProfileId, "name": gone.Name, "protocol": gone.Protocol})
	return connect.NewResponse(&adminv1.DeleteProfileResponse{}), nil
}

// ---- inbounds ----

var inboundStates = map[string]adminv1.InboundState{
	"pending":  adminv1.InboundState_INBOUND_STATE_PENDING,
	"active":   adminv1.InboundState_INBOUND_STATE_ACTIVE,
	"failed":   adminv1.InboundState_INBOUND_STATE_FAILED,
	"disabled": adminv1.InboundState_INBOUND_STATE_DISABLED,
}

// inboundProto describes an inbound with its effective port and server name (from the same BuildInbound
// that feeds the nodes).
func (s *Service) inboundProto(f store.AccessInboundFull, settings json.RawMessage) *adminv1.Inbound {
	out := &adminv1.Inbound{
		Id: f.Inbound.ID, ProfileId: f.Profile.ID, ProfileName: f.Profile.Name, Protocol: f.Profile.Protocol,
		NodeId: f.Node.ID, NodeName: f.Node.Name, State: inboundStates[f.Inbound.State], LastError: f.Inbound.LastError,
		CertPinSha256: f.Inbound.CertPinSHA256, Port: uint32(f.Inbound.PortOverride), TlsServerName: f.Inbound.TLSServerNameOverride,
	}
	if !f.Inbound.CertNotAfter.IsZero() {
		out.CertNotAfterUnix = f.Inbound.CertNotAfter.Unix()
	}
	if spec, err := s.buildSpec(f, settings); err == nil {
		out.Port, out.TlsServerName, out.Egress = uint32(spec.Listen.Port), spec.TLS.ServerName, spec.Egress
	}
	out.Awg = awgStatus(f.Inbound)
	return out
}

// awgStatus is the last AwgHealth the node reported for the inbound (stored as protojson by the fleet module), or
// nil when there is none: a hysteria2 inbound, a node that has not reported yet, an agent without "awg/1".
func awgStatus(in store.AccessInbound) *adminv1.AwgInboundStatus {
	if in.AwgHealthJSON == "" {
		return nil
	}
	var h agentv1.AwgHealth
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(in.AwgHealthJSON), &h); err != nil {
		return nil
	}
	return &adminv1.AwgInboundStatus{
		Backend: h.Backend, BackendVersion: h.BackendVersion, IfaceUp: h.IfaceUp, Peers: h.Peers,
		PeersHandshaken: h.PeersHandshaken, PeersOnline: h.PeersOnline, NewestHandshakeUnix: h.NewestHandshakeUnix,
		UdpRxPackets: h.UdpRxPackets, UnknownPeerEvents: h.UnknownPeerEvents, ReportedUnix: unixOrZero(in.AwgHealthAt),
	}
}

func (s *Service) loadFull(ctx context.Context, inboundID string) (store.AccessInboundFull, json.RawMessage, error) {
	a := s.st.Access()
	var f store.AccessInboundFull
	var err error
	if f.Inbound, err = a.Inbound(ctx, inboundID); err != nil {
		return f, nil, err
	}
	if f.Profile, err = a.Profile(ctx, f.Inbound.ProfileID); err != nil {
		return f, nil, err
	}
	if f.Node, err = a.Node(ctx, f.Inbound.NodeID); err != nil {
		return f, nil, err
	}
	merged, err := s.mergedSettings(f.Profile)
	return f, merged, err
}

// nodeListen is another inbound of a node as the port check sees it: its profile and the UDP ports it takes.
type nodeListen struct {
	profile string
	l       plugin.Listen
}

// nodeInbounds is what the checks need of a node's inbounds other than skip (the one being changed): the UDP ports each
// one takes, and which profiles are there. A neighbour whose spec does not build has no ports here: it is reported on
// its own.
func (s *Service) nodeInbounds(ctx context.Context, nodeID, skip string) (listens []nodeListen, profiles map[string]bool, err error) {
	others, err := s.st.Access().InboundsFull(ctx, nodeID)
	if err != nil {
		return nil, nil, s.internal("inbounds", err)
	}
	profiles = map[string]bool{}
	for _, o := range others {
		if o.Inbound.ID == skip {
			continue
		}
		profiles[o.Profile.ID] = true
		merged, err := s.mergedSettings(o.Profile)
		if err != nil {
			return nil, nil, s.internal("open profile secrets", err)
		}
		if os, err := s.buildSpec(o, merged); err == nil {
			listens = append(listens, nodeListen{profile: o.Profile.Name, l: os.Listen})
		}
	}
	return listens, profiles, nil
}

// clashOf is the first neighbour that fights l over a UDP port (listenOverlap), or nil.
func clashOf(l plugin.Listen, others []nodeListen) *nodeListen {
	for i := range others {
		if listenOverlap(l, others[i].l) {
			return &others[i]
		}
	}
	return nil
}

// freePortFor is a port an inbound listening as l could take instead of l.Port, with its own hop range and stored bad
// checks excluded; 0 when none is free.
func freePortFor(protocol string, l plugin.Listen, others []nodeListen, checks map[uint16]store.PortCheck, now time.Time) uint16 {
	ports := freePortCandidates(protocol, 1, l, others, checks, now)
	if len(ports) == 0 {
		return 0
	}
	return ports[0]
}

// portRefusal is the coded refusal of a listen that another profile of the node fights over, or nil: "hop_taken" when
// the inbound's own hop range is what overlaps (no port fixes that), else "port_taken" with a free port to take instead.
func portRefusal(protocol, node string, l plugin.Listen, others []nodeListen, checks map[uint16]store.PortCheck, now time.Time) error {
	if l.HopFrom != 0 {
		// the range alone: its start stands in for the port, so that only the range is compared
		if c := clashOf(plugin.Listen{Network: l.Network, Port: l.HopFrom, HopFrom: l.HopFrom, HopTo: l.HopTo}, others); c != nil {
			return coded(connect.CodeAlreadyExists, "hop_taken", "from", strconv.Itoa(int(l.HopFrom)), "to", strconv.Itoa(int(l.HopTo)),
				"profile", c.profile, "node", node)
		}
	}
	c := clashOf(l, others)
	if c == nil {
		return nil
	}
	kv := []string{"port", strconv.Itoa(int(l.Port)), "profile", c.profile, "node", node,
		"free", strconv.Itoa(int(freePortFor(protocol, l, others, checks, now)))}
	if c.l.Port != l.Port {
		kv = append(kv, "hop", fmt.Sprintf("%d-%d", c.l.HopFrom, c.l.HopTo)) // the port lies in that profile's hop range
	}
	return coded(connect.CodeAlreadyExists, "port_taken", kv...)
}

// buildRefusal is a BuildInbound refusal on a node as the code the admin UI words itself (protocols/errors.go); any
// other error keeps the plugin's sentence.
func buildRefusal(err error, node string) error {
	var domain *protocols.NeedsDomainError
	var hop *protocols.PortInHopError
	var name *protocols.ServerNameError
	switch {
	case errors.As(err, &domain):
		return coded(connect.CodeInvalidArgument, "acme_needs_domain", "address", domain.Address, "node", node)
	case errors.As(err, &hop):
		return coded(connect.CodeInvalidArgument, "port_in_hop", "from", strconv.Itoa(hop.From), "to", strconv.Itoa(hop.To))
	case errors.As(err, &name) && name.DomainOnly:
		return coded(connect.CodeInvalidArgument, "sni_needs_domain", "name", name.Name)
	case errors.As(err, &name):
		return coded(connect.CodeInvalidArgument, "sni_invalid", "name", name.Name)
	}
	return invalid("%v", err)
}

// inboundWarnings is what the admin should know before adding or switching on an inbound although it does not stop it:
// an exit through WARP on a node whose WARP account is missing, paused, or down by its last report ("warp_missing",
// state "none" | "paused" | "down").
func (s *Service) inboundWarnings(ctx context.Context, nodeID string, spec plugin.InboundSpec) ([]*adminv1.StatusReason, error) {
	if spec.Egress != "warp" {
		return nil, nil
	}
	state := ""
	switch a, err := s.st.WarpAccount(ctx, nodeID); {
	case errors.Is(err, store.ErrNotFound):
		state = "none"
	case err != nil:
		return nil, s.internal("warp account", err)
	case !a.Enabled:
		state = "paused"
	case warpDown(a.HealthJSON):
		state = "down"
	default:
		return nil, nil
	}
	return []*adminv1.StatusReason{{Code: "warp_missing", Params: map[string]string{"state": state}}}, nil
}

// warpDown: the node's last WARP report says the tunnel does not work (down, or no backend to run it on).
func warpDown(healthJSON string) bool {
	var h agentv1.WarpHealth
	if healthJSON == "" || (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(healthJSON), &h) != nil {
		return false
	}
	return h.State == agentv1.WarpState_WARP_STATE_DOWN || h.State == agentv1.WarpState_WARP_STATE_UNAVAILABLE
}

// listenOverlap reports whether two inbounds fight over a UDP port: the same listen port, one listen port inside
// the other's hop range (the node's redirect would steal its packets), or overlapping hop ranges. The gap between
// a listen port and its own hop range is free: port 443 with hop 20000-40000 leaves 8443 alone.
func listenOverlap(a, b plugin.Listen) bool {
	span := func(l plugin.Listen) [][2]uint16 {
		s := [][2]uint16{{l.Port, l.Port}}
		if l.HopFrom != 0 {
			s = append(s, [2]uint16{l.HopFrom, l.HopTo})
		}
		return s
	}
	for _, x := range span(a) {
		for _, y := range span(b) {
			if x[0] <= y[1] && y[0] <= x[1] {
				return true
			}
		}
	}
	return false
}

func checkOverrides(port uint32, sni string) (uint16, string, error) {
	if port > 65535 {
		return 0, "", invalid("port override must be 1-65535")
	}
	if len(sni) > 253 {
		return 0, "", invalid("tls server name is too long")
	}
	return uint16(port), sni, nil
}

func (s *Service) CreateInbound(ctx context.Context, req *connect.Request[adminv1.CreateInboundRequest]) (*connect.Response[adminv1.CreateInboundResponse], error) {
	m := req.Msg
	a := s.st.Access()
	port, sni, err := checkOverrides(m.PortOverride, m.TlsServerNameOverride)
	if err != nil {
		return nil, err
	}
	f := store.AccessInboundFull{Inbound: store.AccessInbound{
		ID: store.NewID("inb_"), ProfileID: m.ProfileId, NodeID: m.NodeId, PortOverride: port, TLSServerNameOverride: sni,
		Enabled: true, SpecVersion: 1, State: "pending", CreatedAt: s.now(),
	}}
	if f.Profile, err = a.Profile(ctx, m.ProfileId); errors.Is(err, store.ErrNotFound) {
		return nil, notFound("profile")
	} else if err != nil {
		return nil, s.internal("get profile", err)
	}
	if f.Node, err = a.Node(ctx, m.NodeId); errors.Is(err, store.ErrNotFound) {
		return nil, notFound("node")
	} else if err != nil {
		return nil, s.internal("get node", err)
	}
	if f.Node.State != "pending" && f.Node.State != "active" {
		return nil, coded(connect.CodeFailedPrecondition, "node_retired")
	}
	merged, err := s.mergedSettings(f.Profile)
	if err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	proto, err := s.protocol(f.Profile.Protocol)
	if err != nil {
		return nil, s.internal("profile protocol", err)
	}
	others, here, err := s.nodeInbounds(ctx, f.Node.ID, "")
	if err != nil {
		return nil, err
	}
	if here[f.Profile.ID] {
		return nil, coded(connect.CodeAlreadyExists, "already_on_node")
	}
	portChecks, err := s.readPortChecks(ctx, f.Node.ID)
	if err != nil {
		return nil, s.internal("read UDP port checks", err)
	}
	if ini, ok := proto.(protocols.InboundInitializer); ok {
		// Key material of this (profile, node) only (AWG: the server key pair): a hacked node must not give away the
		// others. A key parked by DeleteInbound is taken back, so that a remove + re-add does not break the configs
		// users already imported; otherwise a new one is made. A check (validate_only) builds with a new key that is
		// never stored, and leaves a parked key where it is.
		if !m.ValidateOnly {
			if k, err := a.RetainedKey(ctx, f.Profile.ID, f.Node.ID); err == nil {
				state, oerr := s.vault.Open(k.StateEnc, store.RetainedKeyAAD(f.Profile.ID, f.Node.ID))
				if oerr == nil {
					f.Inbound.PluginStateEnc = s.vault.Seal(state, f.Inbound.ID)
					f.Inbound.PluginPublicJSON = k.PublicJSON
				} else {
					s.log.Warn("access: retained server key unreadable, making a new one", "profile", f.Profile.ID, "node", f.Node.ID, "err", oerr)
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, s.internal("get retained key", err)
			}
		}
		if f.Inbound.PluginStateEnc == nil {
			made, err := ini.InitInbound(protocols.InboundInitInput{
				InboundID: f.Inbound.ID, Profile: protocols.ProfileView{ID: f.Profile.ID, Version: f.Profile.Version, Settings: merged}, Node: nodeView(f.Node),
			})
			if err != nil {
				return nil, s.internal("init inbound", err)
			}
			f.Inbound.PluginStateEnc = s.vault.Seal(made.State, f.Inbound.ID)
			f.Inbound.PluginPublicJSON = string(made.Public)
		}
	}
	spec, err := s.buildSpec(f, merged)
	if err != nil {
		return nil, buildRefusal(err, f.Node.Name)
	}
	if err := portRefusal(proto.ID(), f.Node.Name, spec.Listen, others, portChecks[f.Node.ID], s.now()); err != nil {
		return nil, err
	}
	warnings, err := s.inboundWarnings(ctx, f.Node.ID, spec)
	if err != nil {
		return nil, err
	}
	if m.ValidateOnly {
		if bad, ok := cachedPortLossy(portChecks[f.Node.ID], spec.Listen.Port, s.now()); ok {
			free := freePortFor(proto.ID(), spec.Listen, others, portChecks[f.Node.ID], s.now())
			if !m.AllowLossyPort {
				return nil, portLossyRefusal(ctx, s, f.Node.Name, bad, free)
			}
			warnings = append(warnings, s.portLossyWarning(ctx, f.Node.Name, bad))
		}
		return connect.NewResponse(&adminv1.CreateInboundResponse{Inbound: s.inboundProto(f, merged), Warnings: warnings,
			FreePort: uint32(freePortFor(proto.ID(), spec.Listen, others, portChecks[f.Node.ID], s.now()))}), nil
	}
	portWarning, lossyOverride, err := s.checkPort(ctx, f.Node, proto.ID(), spec.Listen, others, portChecks[f.Node.ID], m.AllowLossyPort)
	if err != nil {
		return nil, err
	}
	if portWarning != nil {
		warnings = append(warnings, portWarning)
	}
	switch err := a.CreateInbound(ctx, f.Inbound); {
	case errors.Is(err, store.ErrAccessExists):
		return nil, coded(connect.CodeAlreadyExists, "already_on_node")
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("profile or node")
	case err != nil:
		return nil, s.internal("create inbound", err)
	}
	s.nodeEvent(ctx, "profile_added", f.Inbound.NodeID, f.Inbound.ID, f.Profile)
	s.audit(ctx, actor(ctx), "inbound_add", map[string]any{"inbound": f.Inbound.ID, "profile": f.Profile.Name, "node": f.Node.Name, "port": spec.Listen.Port})
	if lossyOverride {
		s.auditLossyPort(ctx, f.Node.Name, spec.Listen.Port)
	}
	s.notify.StateChanged()
	return connect.NewResponse(&adminv1.CreateInboundResponse{Inbound: s.inboundProto(f, merged), Warnings: warnings}), nil
}

func (s *Service) UpdateInbound(ctx context.Context, req *connect.Request[adminv1.UpdateInboundRequest]) (*connect.Response[adminv1.UpdateInboundResponse], error) {
	m := req.Msg
	f, merged, err := s.loadFull(ctx, m.InboundId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("inbound")
	} else if err != nil {
		return nil, s.internal("load inbound", err)
	}
	in := f.Inbound
	oldEnabled := in.Enabled
	oldSpec, err := s.buildSpec(f, merged)
	if err != nil {
		return nil, buildRefusal(err, f.Node.Name)
	}
	specChangedByAdmin := false
	if m.PortOverride != nil {
		port, _, err := checkOverrides(*m.PortOverride, "")
		if err != nil {
			return nil, err
		}
		specChangedByAdmin = specChangedByAdmin || port != in.PortOverride
		in.PortOverride = port
	}
	if m.TlsServerNameOverride != nil {
		_, sni, err := checkOverrides(0, *m.TlsServerNameOverride)
		if err != nil {
			return nil, err
		}
		specChangedByAdmin = specChangedByAdmin || sni != in.TLSServerNameOverride
		in.TLSServerNameOverride = sni
	}
	enabledChanged := false
	if m.Enabled != nil {
		enabledChanged = *m.Enabled != in.Enabled
		in.Enabled = *m.Enabled
	}
	f.Inbound = in
	spec, err := s.buildSpec(f, merged)
	if err != nil {
		return nil, buildRefusal(err, f.Node.Name)
	}
	others, _, err := s.nodeInbounds(ctx, f.Node.ID, f.Inbound.ID)
	if err != nil {
		return nil, err
	}
	var warnings []*adminv1.StatusReason
	var portWarning *adminv1.StatusReason
	var lossyOverride bool
	var portChecks portCheckCache
	if m.ValidateOnly || in.Enabled {
		portChecks, err = s.readPortChecks(ctx, f.Node.ID)
		if err != nil {
			return nil, s.internal("read UDP port checks", err)
		}
	}
	if in.Enabled {
		if err := portRefusal(f.Profile.Protocol, f.Node.Name, spec.Listen, others, portChecks[f.Node.ID], s.now()); err != nil {
			return nil, err
		}
		if warnings, err = s.inboundWarnings(ctx, f.Node.ID, spec); err != nil {
			return nil, err
		}
		portChanged := oldSpec.Listen.Port != spec.Listen.Port
		switchedOn := !oldEnabled && in.Enabled
		if m.ValidateOnly {
			if bad, ok := cachedPortLossy(portChecks[f.Node.ID], spec.Listen.Port, s.now()); ok {
				free := freePortFor(f.Profile.Protocol, spec.Listen, others, portChecks[f.Node.ID], s.now())
				if (portChanged || switchedOn) && !m.AllowLossyPort {
					return nil, portLossyRefusal(ctx, s, f.Node.Name, bad, free)
				}
				warnings = append(warnings, s.portLossyWarning(ctx, f.Node.Name, bad))
			}
		} else if portChanged || switchedOn {
			portWarning, lossyOverride, err = s.checkPort(ctx, f.Node, f.Profile.Protocol, spec.Listen, others, portChecks[f.Node.ID], m.AllowLossyPort)
			if err != nil {
				return nil, err
			}
			if portWarning != nil {
				warnings = append(warnings, portWarning)
			}
		} else if bad, ok := cachedPortLossy(portChecks[f.Node.ID], spec.Listen.Port, s.now()); ok {
			warnings = append(warnings, s.portLossyWarning(ctx, f.Node.Name, bad))
		}
	}
	if m.ValidateOnly {
		return connect.NewResponse(&adminv1.UpdateInboundResponse{Inbound: s.inboundProto(f, merged), Warnings: warnings,
			FreePort: uint32(freePortFor(f.Profile.Protocol, spec.Listen, others, portChecks[f.Node.ID], s.now()))}), nil
	}
	if specChangedByAdmin {
		in.SpecVersion++
		f.Inbound = in
	}
	if specChangedByAdmin || enabledChanged {
		if err := s.st.Access().UpdateInbound(ctx, in, true, s.now()); err != nil {
			return nil, s.internal("update inbound", err)
		}
		if p, ok := s.reg.Get(f.Profile.Protocol); ok && specChangedByAdmin && protocols.IsPerDevice(p) {
			// The endpoint port of every issued config of this profile changed.
			if err := s.st.Access().BumpProfileEpoch(ctx, f.Profile.ID); err != nil {
				return nil, s.internal("mark devices stale", err)
			}
		}
		f.Inbound.State = "pending"
		if !in.Enabled {
			f.Inbound.State = "disabled"
		}
		s.audit(ctx, actor(ctx), "inbound_update", map[string]any{"inbound": in.ID, "profile": f.Profile.Name, "node": f.Node.Name, "port": spec.Listen.Port, "enabled": in.Enabled})
		if lossyOverride {
			s.auditLossyPort(ctx, f.Node.Name, spec.Listen.Port)
		}
		s.notify.StateChanged()
	}
	return connect.NewResponse(&adminv1.UpdateInboundResponse{Inbound: s.inboundProto(f, merged), Warnings: warnings}), nil
}

// nodeEvent puts "an admin changed the profiles of this node" on the node's event list. The profile name and protocol go
// into the params as well: once the inbound is gone the list cannot join them any more.
func (s *Service) nodeEvent(ctx context.Context, code, nodeID, inboundID string, p store.AccessProfile) {
	e := store.EventRow{Time: s.now(), Severity: 1, Code: code, Source: "admin", NodeID: nodeID, InboundID: inboundID,
		Params: map[string]string{"actor": auth.ActorLabel(ctx), "profile": p.Name, "protocol": p.Protocol}}
	if err := s.st.InsertEvent(ctx, e); err != nil {
		s.log.Warn("access: node event", "code", code, "err", err)
	}
}

func (s *Service) DeleteInbound(ctx context.Context, req *connect.Request[adminv1.DeleteInboundRequest]) (*connect.Response[adminv1.DeleteInboundResponse], error) {
	a := s.st.Access()
	in, ierr := a.Inbound(ctx, req.Msg.InboundId) // for the event; a miss is answered by the delete itself
	var prof store.AccessProfile
	if ierr == nil {
		prof, ierr = a.Profile(ctx, in.ProfileID)
	}
	// The server key of a per-device (AWG) inbound is parked for the pair, so that adding the profile back to the
	// node keeps it (see CreateInbound); re-sealed here under the pair's AAD, in the delete's own transaction.
	retain := func(enc []byte, profileID, nodeID string) ([]byte, error) {
		inID := req.Msg.InboundId
		state, err := s.vault.Open(enc, inID)
		if err != nil {
			s.log.Warn("access: server key of the removed inbound is unreadable, not kept", "inbound", inID, "err", err)
			return nil, nil
		}
		return s.vault.Seal(state, store.RetainedKeyAAD(profileID, nodeID)), nil
	}
	switch err := a.DeleteInbound(ctx, req.Msg.InboundId, s.now(), retain); {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("inbound")
	case err != nil:
		return nil, s.internal("delete inbound", err)
	}
	if ierr == nil {
		s.nodeEvent(ctx, "profile_removed", in.NodeID, in.ID, prof)
		node, _ := a.Node(ctx, in.NodeID) // a retired node keeps its row and its name
		s.audit(ctx, actor(ctx), "inbound_remove", map[string]any{"inbound": in.ID, "profile": prof.Name, "node": node.Name})
	}
	s.notify.StateChanged()
	return connect.NewResponse(&adminv1.DeleteInboundResponse{}), nil
}
