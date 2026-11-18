//go:build !js

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The channel measure of a node: how wide its channel really is, measured against the widest other nodes of the fleet. It is the
// heavy mode of path_speed (the same agent command, listener and one-time token; agent.proto "CHANNEL MEASURE"): 8 parallel
// streams for 10 seconds in each direction, so a link that one stream cannot fill shows its width.
//
// It fills the links of the node and its partners, so it starts only on the owner's explicit action (the node page's button, or an
// MCP plan the owner approved) and never from a schedule: nothing in this package or the jobs calls ChannelMeasureStart. Limits that
// hold here, besides the caps each agent holds alone: one measure per node at a time, not more than one in channelPause per node
// (a failed one counts), partners are busy for its length, and a run that is cancelled or outlives channelJobMax is stopped on both
// ends. Partners are fleet nodes by id; the address of a partner stays inside the panel, and results, audit rows and errors carry
// node names only.

const (
	channelMaxMB    = 2048 // the volume cap of one direction against one partner, MiB; the agents hold it too
	channelServeS   = 20   // how long a partner's listener waits for the streams, seconds
	channelRunS     = 15   // the longest a run takes, seconds
	channelRunWait  = (channelRunS + 10) * time.Second
	channelPause    = 10 * time.Minute
	channelPartners = 3
	// about how long one direction takes: the 10 s window, connecting and closing
	channelStepSeconds = 12
	// the longest the whole job may run: 3 partners, two directions, each at its longest
	channelJobMax = channelPartners * 2 * (channelRunWait + speedStopWait + speedServeWait)
)

type channelPartnerInfo struct {
	node  store.NodeRow
	known uint32
}

// channelPlan is what a measure would do now: why it cannot, or the partners it would use.
type channelPlan struct {
	err        string
	retryAfter time.Duration
	partners   []channelPartnerInfo
	last       *channelLastJSON
	lastAt     time.Time
}

// channelPlanLocked works out the plan; the caller holds f.probes.mu, so that the busy sets it reads cannot change before the
// measure that follows reserves them.
func (f *Fleet) channelPlanLocked(ctx context.Context, n store.NodeRow) (channelPlan, error) {
	var plan channelPlan
	now := f.now()
	if row, ok, err := f.st.ChannelMeasure(ctx, n.ID); err != nil {
		return plan, err
	} else if ok {
		if row.ResultJSON != "" {
			var last channelLastJSON
			if json.Unmarshal([]byte(row.ResultJSON), &last) == nil {
				plan.last, plan.lastAt = &last, row.At
			}
		}
		if wait := channelPause - now.Sub(row.Started); wait > 0 {
			plan.err, plan.retryAfter = "too_soon", wait
		}
	}
	liveRows, err := f.Live(ctx)
	if err != nil {
		return plan, err
	}
	live := liveRowsByID(liveRows)
	if _, code := f.probeLive(live, n.ID, capSpeedChannel); code != "" {
		plan.err, plan.retryAfter = code, 0
		return plan, nil
	}
	if run := f.probes.channel.runs[n.ID]; run != nil && run.isRunning() {
		plan.err, plan.retryAfter = "busy", 0
		return plan, nil
	}
	if _, running := f.probes.speedBusy[n.ID]; running {
		plan.err, plan.retryAfter = "busy", 0
		return plan, nil
	}
	plan.partners, err = f.channelPartnersFor(ctx, n, live)
	if err != nil {
		return plan, err
	}
	if len(plan.partners) == 0 {
		plan.err, plan.retryAfter = "no_partner", 0
	}
	return plan, nil
}

// channelPartnersFor picks up to three nodes for n: not n itself, not retired, connected with an agent that has the capability,
// not running a test, the widest known channel first (the owner's number or the fastest kept test, whichever is larger; a node with
// none known comes last).
func (f *Fleet) channelPartnersFor(ctx context.Context, n store.NodeRow, live map[string]store.NodeLiveRow) ([]channelPartnerInfo, error) {
	nodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	rows, err := f.st.PathSpeeds(ctx, f.now().Add(-store.PathSpeedKeepHours*time.Hour), "", 500)
	if err != nil {
		return nil, err
	}
	seen := fastestByNode(rows)
	var out []channelPartnerInfo
	for _, c := range nodes {
		if c.ID == n.ID || c.State == "retired" {
			continue
		}
		if _, code := f.probeLive(live, c.ID, capSpeedChannel); code != "" {
			continue
		}
		if _, running := f.probes.speedBusy[c.ID]; running {
			continue
		}
		known := max(uint32(min(max(c.BandwidthMbps, 0), maxBandwidthMbps)), uint32(seen[c.ID]))
		out = append(out, channelPartnerInfo{node: c, known: known})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].known != out[j].known {
			return out[i].known > out[j].known
		}
		return out[i].node.Name < out[j].node.Name
	})
	if len(out) > channelPartners {
		out = out[:channelPartners]
	}
	return out, nil
}

// fastestByNode is the highest rate a stored test went, per node on either end of it: it proves that node's link at least that wide.
func fastestByNode(rows []store.PathSpeedRow) map[string]float64 {
	out := map[string]float64{}
	for _, r := range rows {
		var res speedJSON
		if json.Unmarshal([]byte(r.ResultJSON), &res) != nil || res.Error != "" || res.MbitS <= 0 || res.MbitS > maxBandwidthMbps {
			continue
		}
		out[r.FromID], out[r.ToID] = max(out[r.FromID], res.MbitS), max(out[r.ToID], res.MbitS)
	}
	return out
}

func (r *channelRun) isRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == "running"
}

// ChannelMeasurePlan says what a measure of the node would do and whether it can start now. It moves nothing.
func (s probeService) ChannelMeasurePlan(ctx context.Context, req *connect.Request[probev1.ChannelMeasurePlanRequest]) (*connect.Response[probev1.ChannelMeasurePlanResponse], error) {
	f := s.f
	n, err := f.probeNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	p := &f.probes
	p.mu.Lock()
	plan, err := f.channelPlanLocked(ctx, n)
	p.mu.Unlock()
	if err != nil {
		return nil, internalErr(f.log.Error, "plan the channel measure", err)
	}
	out := &probev1.ChannelMeasurePlanResponse{NodeName: n.Name, Error: plan.err, ConfiguredMbps: uint32(min(max(n.BandwidthMbps, 0), maxBandwidthMbps)),
		RetryAfterS: uint32(math.Ceil(plan.retryAfter.Seconds())), Last: lastMessage(plan.last, plan.lastAt)}
	if plan.err == "" || plan.err == "too_soon" {
		for _, c := range plan.partners {
			out.Partners = append(out.Partners, &probev1.ChannelPartner{Name: c.node.Name, KnownMbps: c.known})
		}
		out.MaxMb = channelMaxMB
		out.MaxGb = uint32(len(plan.partners)) * 2 * channelMaxMB / 1024
		out.EstSeconds = uint32(len(plan.partners)) * 2 * channelStepSeconds
	}
	return connect.NewResponse(out), nil
}

// ChannelMeasureStart starts the measure of a node and returns at once; the run goes on in the panel.
func (s probeService) ChannelMeasureStart(ctx context.Context, req *connect.Request[probev1.ChannelMeasureStartRequest]) (*connect.Response[probev1.ChannelMeasureStartResponse], error) {
	f := s.f
	n, err := f.probeNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	p := &f.probes
	p.mu.Lock()
	plan, err := f.channelPlanLocked(ctx, n)
	if err != nil {
		p.mu.Unlock()
		return nil, internalErr(f.log.Error, "plan the channel measure", err)
	}
	if plan.err != "" || p.active >= maxActiveProbes {
		p.mu.Unlock()
		if plan.err == "" {
			plan.err = "limit"
		}
		st := f.channelStatus(ctx, n, nil)
		st.Error, st.RetryAfterS = plan.err, uint32(math.Ceil(plan.retryAfter.Seconds()))
		return connect.NewResponse(&probev1.ChannelMeasureStartResponse{Status: st}), nil
	}
	// the pause counts from here, whether the measure works or not, and survives a restart
	if err := f.st.StartChannelMeasure(ctx, n.ID, f.now()); err != nil {
		p.mu.Unlock()
		return nil, internalErr(f.log.Error, "reserve the channel measure", err)
	}
	if p.speedBusy == nil {
		p.speedBusy = map[string]struct{}{}
	}
	if p.channel.runs == nil {
		p.channel.runs = map[string]*channelRun{}
	}
	ids := []string{n.ID}
	for _, c := range plan.partners {
		ids = append(ids, c.node.ID)
	}
	for _, id := range ids {
		p.speedBusy[id] = struct{}{}
	}
	p.active++
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), channelJobMax)
	run := &channelRun{nodeID: n.ID, started: f.now(), state: "running", cancel: cancel, done: make(chan struct{})}
	for _, c := range plan.partners {
		for _, dir := range []string{"down", "up"} {
			run.steps = append(run.steps, &channelStepState{Partner: c.node.Name, PartnerID: c.node.ID, Direction: dir, State: "waiting"})
		}
	}
	p.channel.runs[n.ID] = run
	p.mu.Unlock()

	go func() {
		defer close(run.done)
		defer cancel()
		f.runChannelJob(jobCtx, run, n, plan.partners, func() {
			p.mu.Lock()
			for _, id := range ids {
				delete(p.speedBusy, id)
			}
			p.active--
			p.mu.Unlock()
		})
	}()
	return connect.NewResponse(&probev1.ChannelMeasureStartResponse{Status: f.channelStatus(ctx, n, run)}), nil
}

// ChannelMeasureStatus is the run of a node now or last, and its last stored measure. It moves nothing.
func (s probeService) ChannelMeasureStatus(ctx context.Context, req *connect.Request[probev1.ChannelMeasureStatusRequest]) (*connect.Response[probev1.ChannelMeasureStatusResponse], error) {
	f := s.f
	n, err := f.probeNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	f.probes.mu.Lock()
	run := f.probes.channel.runs[n.ID]
	f.probes.mu.Unlock()
	return connect.NewResponse(f.channelStatus(ctx, n, run)), nil
}

// ChannelMeasureCancel stops the run of a node: its streams are closed on both ends at once.
func (s probeService) ChannelMeasureCancel(ctx context.Context, req *connect.Request[probev1.ChannelMeasureCancelRequest]) (*connect.Response[probev1.ChannelMeasureCancelResponse], error) {
	f := s.f
	n, err := f.probeNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	f.probes.mu.Lock()
	run := f.probes.channel.runs[n.ID]
	f.probes.mu.Unlock()
	if run != nil && run.isRunning() {
		run.cancel()
		select {
		case <-run.done:
		case <-time.After(speedStopWait + time.Second):
		case <-ctx.Done():
		}
		f.audit(context.WithoutCancel(ctx), "node.channel_measure_cancel", map[string]string{"node": n.Name})
	}
	return connect.NewResponse(&probev1.ChannelMeasureCancelResponse{Status: f.channelStatus(ctx, n, run)}), nil
}

// channelStatus is the status message of a run (nil = none since the panel started) and the node's stored last measure.
func (f *Fleet) channelStatus(ctx context.Context, n store.NodeRow, run *channelRun) *probev1.ChannelMeasureStatusResponse {
	out := &probev1.ChannelMeasureStatusResponse{NodeName: n.Name, State: "idle", ConfiguredMbps: uint32(min(max(n.BandwidthMbps, 0), maxBandwidthMbps))}
	if row, ok, err := f.st.ChannelMeasure(ctx, n.ID); err != nil {
		f.log.Warn("read the channel measure", "code", "store_failed", "err", err)
	} else if ok && row.ResultJSON != "" {
		var last channelLastJSON
		if json.Unmarshal([]byte(row.ResultJSON), &last) == nil {
			out.Last = lastMessage(&last, row.At)
		}
	}
	if run == nil {
		return out
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	out.State, out.Error, out.StartedUnix = run.state, run.err, run.started.Unix()
	for _, s := range run.steps {
		out.Steps = append(out.Steps, &probev1.ChannelStep{Partner: s.Partner, Direction: s.Direction, State: s.State, MbitS: s.MbitS, Error: s.Error, Streams: s.Streams, Capped: s.Capped})
	}
	if run.state == "done" {
		out.Result = lastMessage(&run.result, run.started)
	}
	return out
}

func lastMessage(l *channelLastJSON, at time.Time) *probev1.ChannelLast {
	if l == nil {
		return nil
	}
	dir := func(d channelDirJSON) *probev1.ChannelDirection {
		return &probev1.ChannelDirection{MbitS: d.MbitS, Partner: d.Partner, Streams: d.Streams, Capped: d.Capped}
	}
	return &probev1.ChannelLast{AtUnix: at.Unix(), Down: dir(l.Down), Up: dir(l.Up), Partners: l.Partners}
}

// runChannelJob measures the node against each partner in turn, down then up, and stores what worked: every step as a path speed
// test (kept 7 days, read by path_speed_results and by the capacity report), and the best figure per direction as the node's last
// channel measure; release gives the nodes back. Both ends of every step have their hours marked as measured (runSpeedMode), so the load page does not read the
// test as people's traffic.
func (f *Fleet) runChannelJob(ctx context.Context, run *channelRun, n store.NodeRow, partners []channelPartnerInfo, release func()) {
	byID := map[string]store.NodeRow{}
	for _, c := range partners {
		byID[c.node.ID] = c.node
	}
	var firstErr string
	skipped := map[string]string{} // partner id -> why its other direction is not tried
	for _, step := range run.steps {
		if ctx.Err() != nil {
			break
		}
		partner := byID[step.PartnerID]
		if why := skipped[partner.ID]; why != "" {
			run.update(func() { step.State, step.Error = "failed", why })
			continue
		}
		run.update(func() { step.State = "running" })
		res := f.runSpeedMode(ctx, n, partner, step.Direction, channelMaxMB, true)
		if res.Error != "" {
			run.update(func() { step.State, step.Error = "failed", res.Error })
			if firstErr == "" {
				firstErr = res.Error
			}
			switch res.Error {
			case "node_offline", "agent_too_old", "unreachable", "no_route", "busy", "too_soon":
				skipped[partner.ID] = res.Error
			}
			continue
		}
		at := f.now().UTC()
		if b, err := json.Marshal(res); err == nil {
			if err := f.st.AddPathSpeed(context.WithoutCancel(ctx), n.ID, partner.ID, step.Direction, at, string(b)); err != nil {
				f.log.Warn("store path speed", "code", "store_failed", "err", err)
			}
		}
		run.update(func() {
			step.State, step.MbitS, step.Streams, step.Capped = "done", res.MbitS, res.Streams, res.Capped
			best := &run.result.Down
			if step.Direction == "up" {
				best = &run.result.Up
			}
			if res.MbitS > best.MbitS {
				*best = channelDirJSON{MbitS: res.MbitS, Partner: partner.Name, PartnerID: partner.ID, Streams: res.Streams, Capped: res.Capped}
			}
		})
	}

	release() // the nodes are free before the run says it is over
	run.mu.Lock()
	run.result.Partners = uint32(len(partners))
	result := run.result
	canceled := ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)
	for _, step := range run.steps { // what a cancel or the time limit cut short
		if step.State == "waiting" || step.State == "running" {
			step.State, step.Error = "failed", "canceled"
		}
	}
	switch {
	case canceled:
		run.state = "canceled"
	case result.Down.MbitS <= 0 && result.Up.MbitS <= 0:
		run.state, run.err = "failed", firstErr
		if run.err == "" {
			run.err = "failed"
		}
	default:
		run.state = "done"
	}
	state, runErr := run.state, run.err
	run.mu.Unlock()

	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if state == "done" {
		if b, err := json.Marshal(result); err == nil {
			if err := f.st.SetChannelMeasure(saveCtx, n.ID, f.now(), string(b)); err != nil {
				f.log.Warn("store the channel measure", "code", "store_failed", "err", err)
			}
		}
	}
	names := make([]string, len(partners))
	for i, c := range partners {
		names[i] = c.node.Name
	}
	f.audit(saveCtx, "node.channel_measure", map[string]string{
		"node": n.Name, "partners": strings.Join(names, ","), "state": state, "error": runErr,
		"down_mbps": strconv.FormatFloat(result.Down.MbitS, 'f', 1, 64), "up_mbps": strconv.FormatFloat(result.Up.MbitS, 'f', 1, 64),
	})
}

func (r *channelRun) update(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn()
}

// parseChannelResult reads the sender's numbers of a channel step; a number that makes no sense is a failed test, never a figure.
// capMB is the volume cap that was asked for: the sender cannot have moved more than that.
func parseChannelResult(p map[string]string, capMB uint32) speedJSON {
	res := speedJSON{MB: capMB}
	mbit, err := strconv.ParseFloat(p["mbit_s"], 64)
	bytes, errB := strconv.ParseUint(p["bytes"], 10, 64)
	dur, errD := strconv.ParseUint(p["duration_ms"], 10, 32)
	streams, errS := strconv.ParseUint(p["streams"], 10, 8)
	if err != nil || errB != nil || errD != nil || errS != nil || math.IsNaN(mbit) || mbit <= 0 || mbit > maxBandwidthMbps ||
		bytes == 0 || bytes > uint64(capMB)<<20 || dur == 0 || dur > 60_000 || streams == 0 || streams > 8 {
		res.Error = "failed"
		return res
	}
	// the figure is what the bytes and the time say: an agent whose own figure is far off them is not believed
	if calc := float64(bytes) * 8 / (float64(dur) / 1000) / 1e6; math.Abs(calc-mbit) > calc*0.05+0.2 {
		res.Error = "failed"
		return res
	}
	ms := func(key string) uint32 {
		v, err := strconv.ParseUint(p[key], 10, 32)
		if err != nil {
			return 0
		}
		return uint32(min(v, 3_600_000))
	}
	res.MbitS = math.Round(mbit*10) / 10
	res.Bytes, res.DurationMS, res.Streams = bytes, uint32(dur), uint32(streams)
	res.ConnectMS, res.FirstByteMS, res.Capped = ms("connect_ms"), ms("first_byte_ms"), p["capped"] == "1"
	return res
}
