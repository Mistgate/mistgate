package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// probeRateLimitBps caps the system credential on the node: a probe moves a few KB, so a leak of the
// secret is worth at most this much bandwidth.
const probeRateLimitBps = 2_000_000

// ---------------------------------------------------------------------------------------------------
// The system credential

// WithProbeCreds appends the system credential of every inbound that has a probe client to the desired
// state of a node (cmd/mistgate calls it on the result of access.Desired). The credential has no user and no
// device and lives in health_probe_cred, which no list, subscription or quota reads (an AWG one is a peer with its own
// key and an address from the profile's allocator; the node takes it for a device, which is why no agent update is
// needed). A credential that cannot be made is logged and left out: the node must not lose its real users over a probe.
func (s *Service) WithProbeCreds(ctx context.Context, in []statehash.Inbound) []statehash.Inbound {
	for i := range in {
		sp := in[i].Spec
		if _, ok := s.cfg.Dialers[sp.Protocol]; !ok {
			continue
		}
		row, err := s.probeCred(ctx, sp.ID, sp.Protocol)
		if err != nil {
			s.log.Warn("health: no probe credential", "inbound", sp.ID, "err", err)
			continue
		}
		creds := append(make([]plugin.UserCred, 0, len(in[i].Creds)+1), in[i].Creds...)
		in[i].Creds = append(creds, plugin.UserCred{CredID: row.CredID, Data: json.RawMessage(row.DataJSON), RateLimitBps: probeRateLimitBps})
	}
	return in
}

// probeCred returns the credential of an inbound, making it on first use. The secret is sealed with the
// credential id as AAD, like every secret of the panel.
func (s *Service) probeCred(ctx context.Context, inboundID, protocol string) (store.ProbeCredRow, error) {
	s.credMu.Lock()
	row, ok := s.creds[inboundID]
	s.credMu.Unlock()
	if ok {
		return row, nil
	}
	row, err := s.st.ProbeCred(ctx, inboundID)
	if errors.Is(err, store.ErrNotFound) {
		p, ok := s.reg.Get(protocol)
		if !ok {
			return store.ProbeCredRow{}, fmt.Errorf("protocol %q is not available", protocol)
		}
		credID := store.NewID("crd_")
		var ierr error
		if protocols.IsPerDevice(p) {
			ierr = s.insertPeerCred(ctx, p, inboundID, credID)
		} else {
			var issued protocols.Issued
			if issued, ierr = p.IssueCredential(protocols.IssueInput{}); ierr == nil {
				ierr = s.st.InsertProbeCred(ctx, store.ProbeCredRow{InboundID: inboundID, CredID: credID,
					SecretEnc: s.v.Seal([]byte(issued.Secret), credID), DataJSON: string(issued.NodeData)}, s.now())
			}
		}
		if ierr != nil {
			return store.ProbeCredRow{}, ierr
		}
		row, err = s.st.ProbeCred(ctx, inboundID) // a concurrent caller may have won: everybody reads the winner
	}
	if err != nil {
		return store.ProbeCredRow{}, err
	}
	s.credMu.Lock()
	s.creds[inboundID] = row
	s.credMu.Unlock()
	return row, nil
}

// insertPeerCred makes the system credential of an inbound whose clients are peers with a tunnel address (AWG): a
// key pair, a PSK and an address that the profile's own allocator hands out, in one transaction with the row. The
// credential is a peer of the inbound like a device's, but it is stored here and not in device_credential/awg_peer, so
// no device list, quota, limit, subscription, MCP view or export reads it.
func (s *Service) insertPeerCred(ctx context.Context, p protocols.Protocol, inboundID, credID string) error {
	row, err := s.st.FleetInbound(ctx, inboundID) // fresh: the inbound may be a moment old, older than the snapshot
	if err != nil {
		return err
	}
	settings, err := s.mergedSettings(row)
	if err != nil {
		return err
	}
	maxIdx, err := awg.MaxPeerIndex(settings)
	if err != nil {
		return err
	}
	err = s.st.InsertProbeCredIdx(ctx, inboundID, row.ProfileID, maxIdx, s.now(), func(idx int) (store.ProbeCredRow, error) {
		issued, err := p.IssueCredential(protocols.IssueInput{ProfileID: row.ProfileID, Settings: settings, PeerIndex: idx})
		if err != nil {
			return store.ProbeCredRow{}, err
		}
		return store.ProbeCredRow{CredID: credID, SecretEnc: s.v.Seal([]byte(issued.Secret), credID), DataJSON: string(issued.NodeData)}, nil
	})
	if errors.Is(err, store.ErrAccessSubnetFull) {
		return errors.New("the profile's client network has no free address for the probe peer")
	}
	return err
}

// probeSecret is what the probe client presents to the inbound.
func (s *Service) probeSecret(ctx context.Context, t *target) (string, error) {
	_, secret, err := s.probeCredSecret(ctx, t)
	return secret, err
}

// probeCredSecret is the credential row of the target's inbound and its opened secret.
func (s *Service) probeCredSecret(ctx context.Context, t *target) (store.ProbeCredRow, string, error) {
	row, err := s.probeCred(ctx, t.in.ID, t.in.Protocol)
	if err != nil {
		return row, "", err
	}
	b, err := s.v.Open(row.SecretEnc, row.CredID)
	return row, string(b), err
}

// ---------------------------------------------------------------------------------------------------
// Probe targets

// target is one inbound of a node as the checker and the views see it.
type target struct {
	node     store.NodeRow
	in       store.FleetInboundRow
	spec     plugin.InboundSpec // valid when err == nil and the inbound is enabled
	settings json.RawMessage    // merged profile settings (secrets in place)
	err      error              // the spec could not be built
}

// snapshot is every inbound of every non-retired node at one moment.
type snapshot struct {
	at      int64 // unix nanoseconds
	nodes   []store.NodeRow
	byNode  map[string][]*target // in inbound id order
	targets map[string]*target   // by inbound id
}

// snapshot returns the target list, rebuilt when it is older than SnapshotTTL. The evaluator, the scheduler and
// the RPCs share it, so a pass costs one query per node, not one per consumer.
func (s *Service) snapshot(ctx context.Context) (*snapshot, error) {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	now := s.now()
	if s.snap != nil && now.UnixNano()-s.snap.at < int64(s.cfg.SnapshotTTL) && now.UnixNano() >= s.snap.at {
		return s.snap, nil
	}
	nodes, err := s.st.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	sn := &snapshot{at: now.UnixNano(), nodes: nodes, byNode: map[string][]*target{}, targets: map[string]*target{}}
	for _, n := range nodes {
		rows, err := s.st.FleetInbounds(ctx, n.ID, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			t := &target{node: n, in: r}
			if r.Enabled {
				t.spec, t.settings, t.err = s.build(n, r)
			}
			sn.byNode[n.ID] = append(sn.byNode[n.ID], t)
			sn.targets[r.ID] = t
		}
	}
	s.snap = sn
	return sn, nil
}

func (s *Service) invalidateSnapshot() {
	s.snapMu.Lock()
	s.snap = nil
	s.snapMu.Unlock()
}

// build turns an inbound row into the node-side spec through the protocol plugin, like fleet.buildSpec: the
// framework owns the identity fields.
func (s *Service) build(n store.NodeRow, row store.FleetInboundRow) (plugin.InboundSpec, json.RawMessage, error) {
	p, ok := s.reg.Get(row.Protocol)
	if !ok {
		return plugin.InboundSpec{}, nil, fmt.Errorf("protocol %q is not available on this panel", row.Protocol)
	}
	settings, err := s.mergedSettings(row)
	if err != nil {
		return plugin.InboundSpec{}, nil, err
	}
	var state []byte
	if len(row.PluginStateEnc) > 0 { // the key material a plugin made for this inbound (AWG: the server key pair)
		if state, err = s.v.Open(row.PluginStateEnc, row.ID); err != nil {
			return plugin.InboundSpec{}, nil, fmt.Errorf("inbound %s key material: %w", row.ID, err)
		}
	}
	spec, err := p.BuildInbound(protocols.InboundInput{
		Profile:               protocols.ProfileView{ID: row.ProfileID, Version: row.ProfileVersion, Settings: settings},
		Node:                  protocols.NodeView{ID: n.ID, Name: n.Name, Address: n.Address, CountryCode: n.CountryCode},
		PortOverride:          row.PortOverride,
		TLSServerNameOverride: row.TLSNameOverride,
		SpecVersion:           row.SpecVersion,
		Enabled:               true,
		InboundID:             row.ID,
		PluginState:           state,
		PluginPublic:          json.RawMessage(row.PluginPublic),
	})
	if err != nil {
		return plugin.InboundSpec{}, nil, err
	}
	spec.ID, spec.Protocol, spec.ProfileID, spec.Version, spec.Enabled = row.ID, row.Protocol, row.ProfileID, row.SpecVersion, true
	return spec, settings, nil
}

// mergedSettings puts the vault-held "x-secret" values of the profile back into its settings (like fleet.mergedSettings).
func (s *Service) mergedSettings(row store.FleetInboundRow) (json.RawMessage, error) {
	settings := json.RawMessage(row.Settings)
	if len(row.SecretsEnc) == 0 {
		return settings, nil
	}
	raw, err := s.v.Open(row.SecretsEnc, row.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("profile %s secrets: %w", row.ProfileID, err)
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, fmt.Errorf("profile %s secrets: %w", row.ProfileID, err)
	}
	return protocols.MergeSecrets(settings, secrets)
}

// Why an inbound is not probed right now; "" = it is. The codes are the skip codes of health.proto, and the matrix tells
// them apart: a profile that failed to start is red, one the owner switched off is grey ("✕ not started" / "off").
// inbound_not_active is the old catch-all of the last three, which an older SPA reads for all of them; the hy2 probe
// still uses it for a self-signed certificate whose pin the node has not reported yet.
const (
	skipNodeOffline    = "node_offline"
	skipNotActive      = "inbound_not_active"
	skipFailed         = "inbound_failed"
	skipDisabled       = "inbound_disabled"
	skipPending        = "inbound_pending"
	skipClientMissing  = "client_unsupported"
	errUnsupportedText = "no probe client for this configuration"
)

func (s *Service) skipReason(t *target) string {
	if t.node.State != "active" {
		return skipNodeOffline
	}
	if connected, _, _ := s.fl.Live(t.node.ID); !connected {
		return skipNodeOffline
	}
	switch {
	case !t.in.Enabled:
		return skipDisabled
	case t.in.State == "failed":
		return skipFailed
	case t.in.State != "active":
		return skipPending
	}
	if _, ok := s.cfg.Dialers[t.in.Protocol]; !ok || t.err != nil {
		return skipClientMissing
	}
	return ""
}
