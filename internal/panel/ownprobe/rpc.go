package ownprobe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	ownprobev1 "github.com/mistgate/mistgate/gen/mistgate/ownprobe/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// rpc is the OwnProbeService: the owner's side of the probes. Every call is the owner's; the ones that create, revoke or
// switch probes also need a fresh step-up (auth.stepUpProcedures says the same, this is the second lock).
type rpc struct{ s *Service }

func (r rpc) Get(ctx context.Context, _ *connect.Request[ownprobev1.OwnProbeServiceGetRequest]) (*connect.Response[ownprobev1.OwnProbeServiceGetResponse], error) {
	s := r.s
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return nil, s.internal("read own probe settings", "own_probe_get_failed", err)
	}
	rows, err := s.cfg.Store.OwnProbes(ctx)
	if err != nil {
		return nil, s.internal("list own probes", "own_probe_get_failed", err)
	}
	out := &ownprobev1.OwnProbeServiceGetResponse{Settings: settingsMessage(settings), ScriptVersion: ScriptVersion()}
	for _, row := range rows {
		out.Probes = append(out.Probes, probeMessage(row))
	}
	return connect.NewResponse(out), nil
}

func (r rpc) Update(ctx context.Context, req *connect.Request[ownprobev1.OwnProbeServiceUpdateRequest]) (*connect.Response[ownprobev1.OwnProbeServiceUpdateResponse], error) {
	s := r.s
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	in := req.Msg.GetSettings()
	if in == nil || in.IntervalMin < 30 || in.IntervalMin > 240 {
		return nil, fail(connect.CodeInvalidArgument, "interval_min must be 30..240")
	}
	fallback, err := validateFallbackURL(in.FallbackUrl)
	if err != nil {
		return nil, fail(connect.CodeInvalidArgument, "fallback_url must be an absolute HTTPS URL")
	}
	values := map[string]string{settingsEnabled: strconv.FormatBool(in.Enabled), settingsInterval: strconv.FormatUint(uint64(in.IntervalMin), 10), settingsFallback: fallback}
	s.mu.Lock()
	if err := s.cfg.Store.SetSettings(ctx, values); err != nil {
		s.mu.Unlock()
		return nil, s.internal("update own probe settings", "own_probe_update_failed", err)
	}
	if !in.Enabled {
		if err := s.cfg.Store.ClearOwnProbeJobs(ctx); err != nil {
			s.mu.Unlock()
			return nil, s.internal("clear own probe jobs", "own_probe_update_failed", err)
		}
	}
	s.mu.Unlock()
	s.audit(ctx, admin, "own_probe.update", map[string]any{"enabled": in.Enabled, "interval_min": in.IntervalMin, "fallback_set": fallback != ""})
	s.changed(ctx)
	return connect.NewResponse(&ownprobev1.OwnProbeServiceUpdateResponse{Settings: settingsMessage(Settings{Enabled: in.Enabled, IntervalMin: in.IntervalMin, FallbackURL: fallback})}), nil
}

func (r rpc) Create(ctx context.Context, req *connect.Request[ownprobev1.OwnProbeServiceCreateRequest]) (*connect.Response[ownprobev1.OwnProbeServiceCreateResponse], error) {
	s := r.s
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	name, city, kind := strings.TrimSpace(req.Msg.Name), strings.TrimSpace(req.Msg.City), req.Msg.Kind
	if !safeLabel(name, 80) || !safeLabel(city, 80) || !oneOf(kind, "home", "mobile", "dc") {
		return nil, fail(connect.CodeInvalidArgument, "name, city or kind is invalid")
	}
	tokenBytes := make([]byte, 32)
	idBytes := make([]byte, 12)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, s.internal("generate own probe token", "own_probe_create_failed", err)
	}
	if _, err := rand.Read(idBytes); err != nil {
		return nil, s.internal("generate own probe id", "own_probe_create_failed", err)
	}
	token := "mgp_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(tokenBytes))
	tokenHash := sha256.Sum256([]byte(token))
	now := s.cfg.Now().UTC()
	row := store.OwnProbe{ID: "prb_" + hex.EncodeToString(idBytes), Name: name, City: city, Kind: kind, TokenHash: tokenHash[:],
		TokenExpires: now.Add(probeTokenTTL), State: "pending", CreatedAt: now, CreatedBy: admin.ID}
	command, err := BuildInstallCommand(s.cfg.PublicURL, s.cfg.ProbePrefix, token, ScriptHash())
	if err != nil {
		return nil, fail(connect.CodeFailedPrecondition, "public HTTPS URL is required to build an install command")
	}
	serverCommand, err := BuildServerInstallCommand(s.cfg.PublicURL, s.cfg.ProbePrefix, token, ScriptHash())
	if err != nil {
		return nil, fail(connect.CodeFailedPrecondition, "public HTTPS URL is required to build an install command")
	}
	if err := s.cfg.Store.CreateOwnProbe(ctx, row); err != nil {
		return nil, s.internal("create own probe", "own_probe_create_failed", err)
	}
	s.audit(ctx, admin, "own_probe.create", map[string]any{"probe": row.ID, "name": row.Name, "kind": row.Kind})
	s.changed(ctx)
	return connect.NewResponse(&ownprobev1.OwnProbeServiceCreateResponse{Probe: probeMessage(row), Token: token, Command: command, ScriptSha256: ScriptHash(), ServerCommand: serverCommand}), nil
}

// UpdateCommand gives the commands that update a probe that stands already, for a router and for a server. They are the install
// commands without the token (the script downloaded, its sha256 checked, then `upgrade`), the same for every probe, so nothing
// secret leaves with them beyond the panel's probe address that the install command carried too.
func (r rpc) UpdateCommand(ctx context.Context, _ *connect.Request[ownprobev1.OwnProbeServiceUpdateCommandRequest]) (*connect.Response[ownprobev1.OwnProbeServiceUpdateCommandResponse], error) {
	s := r.s
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	command, err := BuildUpdateCommand(s.cfg.PublicURL, s.cfg.ProbePrefix, ScriptHash())
	if err != nil {
		return nil, fail(connect.CodeFailedPrecondition, "public HTTPS URL is required to build an install command")
	}
	serverCommand, err := BuildServerUpdateCommand(s.cfg.PublicURL, s.cfg.ProbePrefix, ScriptHash())
	if err != nil {
		return nil, fail(connect.CodeFailedPrecondition, "public HTTPS URL is required to build an install command")
	}
	return connect.NewResponse(&ownprobev1.OwnProbeServiceUpdateCommandResponse{Command: command, ServerCommand: serverCommand, ScriptVersion: ScriptVersion()}), nil
}

func (r rpc) Revoke(ctx context.Context, req *connect.Request[ownprobev1.OwnProbeServiceRevokeRequest]) (*connect.Response[ownprobev1.OwnProbeServiceRevokeResponse], error) {
	s := r.s
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	row, err := s.cfg.Store.OwnProbe(ctx, req.Msg.Id)
	if err != nil {
		return nil, mapStoreError(err)
	}
	s.mu.Lock()
	if err := s.cfg.Store.RevokeOwnProbe(ctx, row.ID); err != nil {
		s.mu.Unlock()
		return nil, mapStoreError(err)
	}
	if err := s.refold(ctx); err != nil {
		s.mu.Unlock()
		return nil, s.internal("refold own probes after revoke", "own_probe_fold_failed", err)
	}
	s.mu.Unlock()
	row, err = s.cfg.Store.OwnProbe(ctx, row.ID)
	if err != nil {
		return nil, s.internal("read revoked own probe", "own_probe_revoke_failed", err)
	}
	s.audit(ctx, admin, "own_probe.revoke", map[string]any{"probe": row.ID})
	s.changed(ctx)
	return connect.NewResponse(&ownprobev1.OwnProbeServiceRevokeResponse{Probe: probeMessage(row)}), nil
}

func (r rpc) ConfirmNetwork(ctx context.Context, req *connect.Request[ownprobev1.OwnProbeServiceConfirmNetworkRequest]) (*connect.Response[ownprobev1.OwnProbeServiceConfirmNetworkResponse], error) {
	s := r.s
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	row, err := s.cfg.Store.OwnProbe(ctx, req.Msg.Id)
	if err != nil {
		s.mu.Unlock()
		return nil, mapStoreError(err)
	}
	if row.State != "network_changed" {
		s.mu.Unlock()
		return nil, fail(connect.CodeFailedPrecondition, "probe network has not changed")
	}
	if err := s.cfg.Store.ConfirmOwnProbeNetwork(ctx, row.ID, row.Operator); err != nil {
		s.mu.Unlock()
		return nil, mapStoreError(err)
	}
	s.mu.Unlock()
	row, err = s.cfg.Store.OwnProbe(ctx, row.ID)
	if err != nil {
		return nil, s.internal("read confirmed own probe", "own_probe_fold_failed", err)
	}
	s.audit(ctx, admin, "own_probe.confirm_network", map[string]any{"probe": row.ID})
	s.changed(ctx)
	return connect.NewResponse(&ownprobev1.OwnProbeServiceConfirmNetworkResponse{Probe: probeMessage(row)}), nil
}

func (r rpc) CheckNow(ctx context.Context, req *connect.Request[ownprobev1.OwnProbeServiceCheckNowRequest]) (*connect.Response[ownprobev1.OwnProbeServiceCheckNowResponse], error) {
	s := r.s
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := s.cfg.Store.DueOwnProbe(ctx, req.Msg.Id, s.cfg.Now()); err != nil {
		s.mu.Unlock()
		return nil, mapStoreError(err)
	}
	row, err := s.cfg.Store.OwnProbe(ctx, req.Msg.Id)
	s.mu.Unlock()
	if err != nil {
		return nil, mapStoreError(err)
	}
	return connect.NewResponse(&ownprobev1.OwnProbeServiceCheckNowResponse{Probe: probeMessage(row)}), nil
}

func (r rpc) Disconnect(ctx context.Context, _ *connect.Request[ownprobev1.OwnProbeServiceDisconnectRequest]) (*connect.Response[ownprobev1.OwnProbeServiceDisconnectResponse], error) {
	s := r.s
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	settings, err := s.settings(ctx)
	if err != nil {
		s.mu.Unlock()
		return nil, s.internal("read own probe settings", "own_probe_update_failed", err)
	}
	settings.Enabled = false
	if err := s.cfg.Store.SetSettings(ctx, map[string]string{settingsEnabled: "false"}); err != nil {
		s.mu.Unlock()
		return nil, s.internal("disconnect own probes", "own_probe_update_failed", err)
	}
	if err := s.cfg.Store.ClearOwnProbeJobs(ctx); err != nil {
		s.mu.Unlock()
		return nil, s.internal("clear own probe jobs", "own_probe_update_failed", err)
	}
	s.mu.Unlock()
	s.audit(ctx, admin, "own_probe.disconnect", nil)
	s.changed(ctx)
	return connect.NewResponse(&ownprobev1.OwnProbeServiceDisconnectResponse{Settings: settingsMessage(settings)}), nil
}

func settingsMessage(s Settings) *ownprobev1.OwnProbeSettings {
	return &ownprobev1.OwnProbeSettings{Enabled: s.Enabled, IntervalMin: s.IntervalMin, FallbackUrl: s.FallbackURL}
}

// referenceWord is what the interface is told of a round's reference result: the probe's own words (blocked, open, fail) say
// what the probe tried, these say what it means for the owner.
func referenceWord(ref string) string {
	switch ref {
	case "blocked":
		return "filtered"
	case "open":
		return "open"
	case "fail":
		return "unavailable"
	}
	return ""
}

// probeMessage carries no hash, token or address: the row has none of the last, and the first two never leave the store.
func probeMessage(p store.OwnProbe) *ownprobev1.OwnProbe {
	out := &ownprobev1.OwnProbe{Id: p.ID, Name: p.Name, City: p.City, Kind: p.Kind, State: p.State, Asn: p.ASN, Operator: p.Operator,
		PinnedOperator: p.PinnedOperator, Version: p.Version, ChecksDay: uint32(max(p.ChecksDay, 0)), Reference: referenceWord(store.OwnProbeRef(p))}
	if !p.LastSeen.IsZero() {
		out.LastSeenUnix = p.LastSeen.Unix()
	}
	if !p.LastResult.IsZero() {
		out.LastResultUnix = p.LastResult.Unix()
	}
	if !p.TokenExpires.IsZero() {
		out.TokenExpiresUnix = p.TokenExpires.Unix()
	}
	return out
}
