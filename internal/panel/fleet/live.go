package fleet

import (
	"context"
	"encoding/json"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// Live reads the durable live projection without the admin view's larger live_json column.
func (f *Fleet) Live(ctx context.Context) ([]store.NodeLiveRow, error) {
	return f.st.NodeLive(ctx, f.now().UTC(), "", false)
}

// liveView reads the live projection with live_json for the admin views that render it.
func (f *Fleet) liveView(ctx context.Context) ([]store.NodeLiveRow, error) {
	return f.st.NodeLive(ctx, f.now().UTC(), "", true)
}

func liveRowsByID(rows []store.NodeLiveRow) map[string]store.NodeLiveRow {
	out := make(map[string]store.NodeLiveRow, len(rows))
	for _, row := range rows {
		out[row.NodeID] = row
	}
	return out
}

func decodeLiveView(row store.NodeLiveRow) liveJSONView {
	var view liveJSONView
	if row.LiveJSON != "" {
		_ = json.Unmarshal([]byte(row.LiveJSON), &view)
	}
	return view
}

func (f *Fleet) liveRowsForNode(ctx context.Context, nodeID string, view bool) (store.NodeLiveRow, error) {
	rows, err := f.st.NodeLive(ctx, f.now().UTC(), nodeID, view)
	if err != nil {
		return store.NodeLiveRow{}, err
	}
	if len(rows) == 0 {
		return store.NodeLiveRow{NodeID: nodeID}, nil
	}
	return rows[0], nil
}

// NodeLive reads one node's durable liveness projection without live_json.
func (f *Fleet) NodeLive(ctx context.Context, nodeID string) (store.NodeLiveRow, error) {
	return f.liveRowsForNode(ctx, nodeID, false)
}
