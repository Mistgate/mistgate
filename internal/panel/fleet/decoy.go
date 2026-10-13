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

func (f *Fleet) panelDecoyBundle() (*decoy.Bundle, error) {
	if f.cfg.DecoyDir == "" {
		return nil, decoy.ErrInvalidBundle
	}
	return decoy.FromDir(f.cfg.DecoyDir)
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
	out := make([]*adminv1.Node, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, f.nodeMsg(ctx, node, protos[node.ID], today[node.ID], inboundsOf(enabled, node.ID), now, live[node.ID], panelBundle))
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
			if err := f.syncNodeDecoy(ctx, nodeID); err != nil && !errors.Is(err, errNodeOffline) {
				f.log.Warn("sync node decoy site", "node", nodeID, "err", err)
			}
			f.decoySyncMu.Lock()
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
	result, err := f.command(ctx, nodeID, 90*time.Second, func(requestID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SetDecoy{SetDecoy: &agentv1.SetDecoy{
			RequestId: requestID, Mode: mode, BundleSha256: hash, Files: files,
		}}}
	})
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
			return store.NodeRow{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("panel decoy site is unavailable"))
		}
	}
	if mode == decoy.ModeOwn {
		if err := edition.Check(f.cfg.Remote != nil, edition.DecoyOwn); err != nil {
			return store.NodeRow{}, err
		}
		if len(bundle) > 0 {
			parsed, err := decoy.ParseArchive(bundle)
			if err != nil {
				return store.NodeRow{}, invalid("invalid decoy bundle")
			}
			return f.st.SetNodeDecoyBundle(ctx, nodeID, parsed.Hash(), parsed.Archive())
		}
	}
	if len(bundle) != 0 {
		return store.NodeRow{}, invalid("a decoy bundle is allowed only with own mode")
	}
	return f.st.SetNodeDecoyMode(ctx, nodeID, mode)
}
