package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The bandwidth test (agent.proto "BANDWIDTH TEST"). The admin RPC only asks the node and returns what it measured: it
// never changes node.bandwidth_mbps, the admin chooses to use the number. The one write the panel makes by itself is the
// first measurement of a node that has just been enrolled, and only while its capacity is still 0.

// capBandwidth is the Hello.capabilities entry of an agent that can measure; the panel sends MeasureBandwidth to no other.
const capBandwidth = "bandwidth/1"

// node.bandwidth_mbps is validated to this by UpdateNode; an agent's number is held to it too.
const maxBandwidthMbps = 1_000_000

// measureBandwidth asks the node's live stream for one measurement. The errors are the admin API's (offline, too old, link
// lost, no answer); what the node itself answered (busy, nothing reachable) is in the response's error_code.
func (f *Fleet) measureBandwidth(ctx context.Context, n store.NodeRow) (*adminv1.MeasureBandwidthResponse, error) {
	sess := f.session(n.ID)
	if sess == nil {
		return nil, errNodeOffline
	}
	if !sess.can(capBandwidth) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
	}
	res, err := sess.roundtrip(ctx, f.measureWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &agentv1.MeasureBandwidth{RequestId: reqID}}}
	})
	if err != nil {
		return nil, err
	}
	if !res.Ok {
		return &adminv1.MeasureBandwidthResponse{ErrorCode: measureErrorCode(res.Error)}, nil
	}
	out := &adminv1.MeasureBandwidthResponse{
		DownMbps: mbpsParam(res.Params["down_mbps"]), UpMbps: mbpsParam(res.Params["up_mbps"]),
		Server: clip(res.Params["server"], 64), Seconds: mbpsParam(res.Params["seconds"]), Runs: mbpsParam(res.Params["runs"]),
	}
	// people's share is a part of the figure it is shown with: never more than it
	out.PeopleDownMbps = min(mbpsParam(res.Params["down_people_mbps"]), out.DownMbps)
	out.PeopleUpMbps = min(mbpsParam(res.Params["up_people_mbps"]), out.UpMbps)
	if out.DownMbps == 0 { // a link that moved nothing is not a measurement
		return &adminv1.MeasureBandwidthResponse{ErrorCode: "failed"}, nil
	}
	return out, nil
}

// measureErrorCode is the stable word the UI words; the agent's text is never passed on.
func measureErrorCode(agentError string) string {
	switch agentError {
	case "busy", "unreachable":
		return agentError
	case "unsupported", "unsupported_host":
		return "unsupported"
	}
	return "failed"
}

// mbpsParam reads a whole number from a result's params: anything else, or a negative or absurd one, is 0.
func mbpsParam(s string) uint32 {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(min(v, maxBandwidthMbps))
}

// MeasureBandwidth is owner only and closed to tokens (internal/panel/auth/policy.go): it makes the node push up to 1 GB.
func (s nodeService) MeasureBandwidth(ctx context.Context, req *connect.Request[adminv1.MeasureBandwidthRequest]) (*connect.Response[adminv1.MeasureBandwidthResponse], error) {
	f := s.f
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "measure bandwidth", err)
	}
	out, err := f.measureBandwidth(ctx, n)
	if err != nil {
		return nil, err
	}
	if out.ErrorCode == "" {
		f.audit(ctx, "node.bandwidth_measure", map[string]string{"node_id": n.ID, "node": n.Name, "down_mbps": strconv.Itoa(int(out.DownMbps)),
			"up_mbps": strconv.Itoa(int(out.UpMbps)), "server": out.Server})
	}
	return connect.NewResponse(out), nil
}

// autoMeasureBandwidth measures a node that has just come up for the first time after its enrollment, once, and stores the
// download figure if the capacity is still 0 by then. An agent without the capability is skipped without a word; a failure is
// only logged: the admin can still press the button. The wait ends with the node's stream, so a node that drops is not asked later.
func (f *Fleet) autoMeasureBandwidth(s *session) {
	if !s.can(capBandwidth) {
		return
	}
	go func() {
		t := time.NewTimer(f.measureDelay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-s.done:
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-s.done:
				cancel()
			case <-ctx.Done():
			}
		}()
		n, err := f.st.Node(ctx, s.nodeID)
		if err != nil || n.BandwidthMbps != 0 {
			return
		}
		out, err := f.measureBandwidth(ctx, n)
		if err != nil || out.ErrorCode != "" {
			f.log.Info("first bandwidth measurement did not work", "node", n.ID, "err", err, "code", out.GetErrorCode())
			return
		}
		stored, err := f.st.SetBandwidthIfUnset(ctx, n.ID, capacityOf(out.DownMbps, out.UpMbps))
		if err != nil || !stored {
			if err != nil {
				f.log.Warn("store the measured bandwidth", "node", n.ID, "err", err)
			}
			return
		}
		p := map[string]string{"down_mbps": strconv.Itoa(int(out.DownMbps)), "up_mbps": strconv.Itoa(int(out.UpMbps)), "server": out.Server, "auto": "1"}
		f.event(ctx, 1, "bandwidth_measured", n.ID, p)
		b, _ := json.Marshal(p)
		if err := f.st.Audit(ctx, f.now(), store.AuditEntry{Actor: "system", Action: "node.bandwidth_auto", Params: string(b), Result: "ok"}); err != nil {
			f.log.Warn("audit", "action", "node.bandwidth_auto", "err", err)
		}
	}()
}

// capacityOf is the capacity a measurement gives a VPN node: the slower direction. Every byte a person downloads comes
// in from the internet and goes out to them, and providers often leave the inbound free while they cap the outbound
// (a node measured 4984 Mbps down and 1080 up: its plan is 1 Gbit/s). Without an upload figure, the download.
func capacityOf(down, up uint32) int {
	if up > 0 && up < down {
		return int(up)
	}
	return int(down)
}
