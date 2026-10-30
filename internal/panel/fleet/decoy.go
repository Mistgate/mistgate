package fleet

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// panelDecoyTTL is how long a snapshot of the panel's --decoy-dir is reused: every ListNodes would otherwise read and
// hash the directory again.
const panelDecoyTTL = 5 * time.Second

// panelDecoyBundle is the snapshot of the panel's --decoy-dir (at most 2 MiB, like an own bundle), or why there is none.
func (f *Fleet) panelDecoyBundle() (*decoy.Bundle, error) {
	if f.cfg.DecoyDir == "" {
		return nil, errNoPanelDecoy
	}
	f.panelDecoyMu.Lock()
	defer f.panelDecoyMu.Unlock()
	if f.panelDecoyDir == f.cfg.DecoyDir && time.Since(f.panelDecoyAt) < panelDecoyTTL {
		return f.panelDecoyB, f.panelDecoyErr
	}
	b, err := decoy.FromDir(f.cfg.DecoyDir)
	f.panelDecoyDir, f.panelDecoyAt, f.panelDecoyB, f.panelDecoyErr = f.cfg.DecoyDir, time.Now(), b, err
	return b, err
}

var errNoPanelDecoy = errors.New("the panel has no --decoy-dir")

// panelReason is the decoy_reason_code of a panel site that cannot be used.
func panelReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errNoPanelDecoy):
		return "panel_unavailable"
	case decoy.ReasonOf(err) == decoy.ReasonTooLarge:
		return "panel_too_large"
	case decoy.ReasonOf(err) == decoy.ReasonNoIndex:
		return "panel_no_index"
	default:
		return "panel_invalid"
	}
}

// panelUnavailable is the refusal when the owner picks the panel's site and there is none.
func panelUnavailable(err error) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(panelReason(err)))
}

func (f *Fleet) nodeMessages(ctx context.Context, nodes []store.NodeRow) ([]*adminv1.Node, error) {
	now := f.now().UTC()
	protos, err := f.st.FleetNodeProtocols(ctx)
	if err != nil {
		return nil, err
	}
	today, err := f.todayBytesByNode(ctx, now)
	if err != nil {
		return nil, err
	}
	enabled, err := f.st.FleetEnabledInbounds(ctx)
	if err != nil {
		return nil, err
	}
	liveRows, err := f.liveView(ctx)
	if err != nil {
		return nil, err
	}
	live := liveRowsByID(liveRows)
	panelBundle, _ := f.panelDecoyBundle()
	healthSummaries := f.healthSummaries(ctx)
	d6 := f.ipv6Doctors(ctx)
	out := make([]*adminv1.Node, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, f.nodeMsg(ctx, node, protos[node.ID], today[node.ID], inboundsOf(enabled, node.ID), now, live[node.ID], panelBundle, healthSummaries[node.ID], d6[node.ID]))
	}
	return out, nil
}

func (f *Fleet) nodeMessage(ctx context.Context, node store.NodeRow) (*adminv1.Node, error) {
	rows, err := f.nodeMessages(ctx, []store.NodeRow{node})
	if err != nil {
		return nil, err
	}
	return rows[0], nil
}

func (f *Fleet) scheduleDecoySync(nodeID string) {
	f.decoySyncMu.Lock()
	if f.decoySyncing[nodeID] {
		f.decoySyncDirty[nodeID] = true
		f.decoySyncMu.Unlock()
		return
	}
	f.decoySyncing[nodeID] = true
	f.decoySyncMu.Unlock()
	f.afterResponse(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for {
			err := f.syncNodeDecoy(ctx, nodeID)
			if err != nil && !errors.Is(err, errNodeOffline) {
				f.log.Warn("sync node decoy site", "node", nodeID, "err", err)
			}
			f.decoySyncMu.Lock()
			f.decoyAttempts[nodeID] = decoyAttempt{at: time.Now(), failed: err != nil && !errors.Is(err, errNodeOffline)}
			if f.decoySyncDirty[nodeID] && ctx.Err() == nil {
				delete(f.decoySyncDirty, nodeID)
				f.decoySyncMu.Unlock()
				continue
			}
			delete(f.decoySyncing, nodeID)
			delete(f.decoySyncDirty, nodeID)
			f.decoySyncMu.Unlock()
			return
		}
	})
}

// decoyAttempt is the last try to give a node its site: when, and whether it failed.
type decoyAttempt struct {
	at     time.Time
	failed bool
}

// decoyRetryAfter is how long the sweeper waits before it tries a node whose site failed to apply again.
const decoyRetryAfter = 5 * time.Minute

// decoySyncFailed says whether the last attempt to give this node its site ended in an error.
func (f *Fleet) decoySyncFailed(nodeID string) bool {
	f.decoySyncMu.Lock()
	defer f.decoySyncMu.Unlock()
	return f.decoyAttempts[nodeID].failed
}

// decoySweep runs with the 30 s sweep: a connected node whose reported site is not the configured one gets it again
// (the panel's --decoy-dir changed, or an earlier push failed; a failed push waits decoyRetryAfter).
func (f *Fleet) decoySweep(n store.NodeRow, live store.NodeLiveRow) {
	if n.State != "active" || !live.Connected || n.DecoyMode == decoy.ModeBuiltin || !slices.Contains(live.AgentCaps, capDecoy) {
		return
	}
	want := n.DecoyBundleHash
	if n.DecoyMode == decoy.ModePanel {
		b, err := f.panelDecoyBundle()
		if err != nil {
			return
		}
		want = b.Hash()
	}
	if n.DecoyAgentMode == n.DecoyMode && n.DecoyAgentHash == want {
		return
	}
	f.decoySyncMu.Lock()
	last := f.decoyAttempts[n.ID]
	busy := f.decoySyncing[n.ID]
	f.decoySyncMu.Unlock()
	if busy || (last.failed && time.Since(last.at) < decoyRetryAfter) {
		return
	}
	f.scheduleDecoySync(n.ID)
}

func decoyFilesFor(bundle *decoy.Bundle, reportedHash string) []*agentv1.DecoyFile {
	if bundle == nil || bundle.Hash() == reportedHash {
		return nil
	}
	files := bundle.Files()
	out := make([]*agentv1.DecoyFile, 0, len(files))
	for _, file := range files {
		out = append(out, &agentv1.DecoyFile{Path: file.Path, Contents: file.Content})
	}
	return out
}

func (f *Fleet) syncNodeDecoy(ctx context.Context, nodeID string) error {
	n, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return err
	}
	live, err := f.liveRowsForNode(ctx, nodeID, false)
	if err != nil {
		return err
	}
	if !live.Connected || !slices.Contains(live.AgentCaps, capDecoy) {
		return nil
	}

	mode, hash := n.DecoyMode, ""
	var bundle *decoy.Bundle
	switch mode {
	case decoy.ModeBuiltin:
	case decoy.ModePanel:
		bundle, err = f.panelDecoyBundle()
		if err != nil {
			return nil // the configured panel site disappeared or became invalid; leave the node's current site alone
		}
		hash = bundle.Hash()
	case decoy.ModeOwn:
		if n.DecoyBundleHash == "" {
			return nil
		}
		archive, readErr := f.st.NodeDecoyBundle(ctx, n.DecoyBundleHash)
		if readErr != nil {
			return readErr
		}
		bundle, err = decoy.ParseStoredArchive(archive)
		if err != nil {
			return err
		}
		hash = bundle.Hash()
	default:
		return errors.New("node has an invalid decoy mode")
	}
	if n.DecoyAgentMode == mode && n.DecoyAgentHash == hash {
		return nil
	}
	files := decoyFilesFor(bundle, n.DecoyAgentHash)
	send := func(files []*agentv1.DecoyFile) (*agentv1.CommandResult, error) {
		return f.command(ctx, nodeID, 90*time.Second, func(requestID string) *agentv1.ConnectResponse {
			return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SetDecoy{SetDecoy: &agentv1.SetDecoy{
				RequestId: requestID, Mode: mode, BundleSha256: hash, Files: files,
			}}}
		})
	}
	result, err := send(files)
	if err == nil && !result.Ok && result.Error == "bad_params" && files == nil && bundle != nil {
		// The node reported a hash it no longer has (a state file lost): send the whole tree once.
		result, err = send(decoyFilesFor(bundle, ""))
	}
	if err != nil {
		return err
	}
	if !result.Ok || result.Params["mode"] != mode || result.Params["sha256"] != hash {
		if result.Error == "" {
			return errors.New("node returned an invalid decoy result")
		}
		return errors.New("node could not apply decoy site: " + result.Error)
	}
	return f.st.UpdateNodeDecoyAgentState(ctx, nodeID, mode, hash)
}

func (f *Fleet) applyDecoyMode(ctx context.Context, nodeID, mode string, bundle []byte) (store.NodeRow, error) {
	if mode == decoy.ModePanel {
		if _, err := f.panelDecoyBundle(); err != nil {
			return store.NodeRow{}, panelUnavailable(err)
		}
	}
	if mode == decoy.ModeOwn {
		if err := edition.Check(f.cfg.Remote != nil, edition.DecoyOwn); err != nil {
			return store.NodeRow{}, err
		}
		if len(bundle) > 0 {
			parsed, err := decoy.ParseArchive(bundle)
			if err != nil {
				return store.NodeRow{}, invalid(decoy.ClientMessage(err))
			}
			return f.st.SetNodeDecoyBundle(ctx, nodeID, parsed.Hash(), parsed.Archive())
		}
	}
	if len(bundle) != 0 {
		return store.NodeRow{}, invalid("a decoy bundle is allowed only with own mode")
	}
	return f.st.SetNodeDecoyMode(ctx, nodeID, mode)
}
