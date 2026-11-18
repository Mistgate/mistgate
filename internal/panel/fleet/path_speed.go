//go:build !js

package fleet

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// path_speed: a bounded throughput test between two fleet nodes (agent.proto "PATH SPEED"). The target opens a listener for one
// connection with a one-time token and serves N MB; the sender moves them and measures. It moves bulk traffic, so the MCP tool
// is plan/apply with the owner's approval; the limits here hold whatever the caller.
//
// Targets are fleet nodes by id, as for the other probes: never a host a caller names and never a public server. The target's
// address stays inside the panel (it goes to the sender only); results, audit rows and errors carry node names only.

const (
	speedMinMB = 8
	speedMaxMB = 32
	// speedMaxS is the longest a test may take, the listener's life included.
	speedMaxS = 30
	// A node may be an end of speedNodeDayMax tests in a day, and a caller may start speedCallerDayMax. Both count a started
	// test whether it worked or not. One test runs per node at a time.
	speedNodeDayMax   = 6
	speedCallerDayMax = 12
	speedDayWindow    = 24 * time.Hour

	speedServeWait = 10 * time.Second
	speedRunWait   = (speedMaxS + 10) * time.Second
	speedStopWait  = 5 * time.Second
)

// speedJSON is what a test produced; it is also the stored JSON.
type speedJSON struct {
	Error       string  `json:"error,omitempty"`
	MB          uint32  `json:"mb"`
	MbitS       float64 `json:"mbit_s,omitempty"`
	ConnectMS   uint32  `json:"connect_ms,omitempty"`
	FirstByteMS uint32  `json:"first_byte_ms,omitempty"`
	DurationMS  uint32  `json:"duration_ms,omitempty"`
	Bytes       uint64  `json:"bytes,omitempty"`
	// a channel measure (channel_measure.go): how many streams ran, and whether its volume cap ended it early
	Streams uint32 `json:"streams,omitempty"`
	Capped  bool   `json:"capped,omitempty"`
}

// startSpeed admits a test between two nodes: it applies the limits, records the reservation and takes both nodes. The returned
// function gives them back.
func (f *Fleet) startSpeed(ctx context.Context, a, b string) (func(), error) {
	return f.startSpeedAs(ctx, f.cfg.Actor(ctx), a, b)
}

// startSpeedAs is startSpeed for a caller that is not a signed-in admin (the relay controller asks as "system", so its tests
// count against the panel's own share of the caller limit and not against whoever's request happened to run it).
func (f *Fleet) startSpeedAs(ctx context.Context, actor, a, b string) (func(), error) {
	now := f.now()
	p := &f.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.speedBusy == nil {
		p.speedBusy = map[string]struct{}{}
	}
	usage, err := f.st.PathSpeedStartUsage(ctx, actor, a, b, now.Add(-speedDayWindow))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f.log.Error("read path speed start reservations", "code", "store_failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	retry := func(times []time.Time) error {
		wait := speedDayWindow - now.Sub(times[0])
		return connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("speed test limit reached: try again in %d minutes", int(wait.Minutes())+1))
	}
	if len(usage.Caller) >= speedCallerDayMax {
		return nil, retry(usage.Caller)
	}
	for _, id := range []string{a, b} {
		if _, running := p.speedBusy[id]; running {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("a speed test already runs on one of these nodes"))
		}
		if len(usage.Nodes[id]) >= speedNodeDayMax {
			return nil, retry(usage.Nodes[id])
		}
	}
	if p.active >= maxActiveProbes {
		return nil, activeProbeLimitError()
	}
	if err := f.st.AddPathSpeedStart(ctx, actor, a, b, now); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f.log.Error("reserve path speed start", "code", "store_failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	for _, id := range []string{a, b} {
		p.speedBusy[id] = struct{}{}
	}
	p.active++
	return func() {
		p.mu.Lock()
		delete(p.speedBusy, a)
		delete(p.speedBusy, b)
		p.active--
		p.mu.Unlock()
	}, nil
}

// PathSpeed runs one throughput test between two nodes and keeps its result for 7 days.
func (s probeService) PathSpeed(ctx context.Context, req *connect.Request[probev1.PathSpeedRequest]) (*connect.Response[probev1.PathSpeedResponse], error) {
	f, m := s.f, req.Msg
	if m.Direction != "down" && m.Direction != "up" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("direction must be down or up"))
	}
	if m.Mb < speedMinMB || m.Mb > speedMaxMB {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("mb is %d to %d", speedMinMB, speedMaxMB))
	}
	from, to, err := f.probePair(ctx, m.FromNodeId, m.ToNodeId)
	if err != nil {
		return nil, err
	}
	release, err := f.startSpeed(ctx, from.ID, to.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	res := f.runPathSpeed(ctx, from, to, m.Direction, m.Mb)
	at := f.now().UTC()
	if res.Error == "" {
		if b, err := json.Marshal(res); err == nil {
			if err := f.st.AddPathSpeed(ctx, from.ID, to.ID, m.Direction, at, string(b)); err != nil {
				f.log.Warn("store path speed", "code", "store_failed", "err", err)
			}
		}
	}
	f.audit(context.WithoutCancel(ctx), "node.path_speed", map[string]string{
		"from": from.Name, "to": to.Name, "direction": m.Direction, "mb": strconv.Itoa(int(m.Mb)), "error": res.Error,
	})
	return connect.NewResponse(speedMessage(from.Name, to.Name, m.Direction, at, res)), nil
}

// PathSpeedResults lists the speed tests kept in the last 7 days, newest first: what an approved path_speed plan measured.
// Node names and figures only, never an address.
func (s probeService) PathSpeedResults(ctx context.Context, req *connect.Request[probev1.PathSpeedResultsRequest]) (*connect.Response[probev1.PathSpeedResultsResponse], error) {
	f, m := s.f, req.Msg
	limit := int(m.Limit)
	if limit == 0 {
		limit = 20
	}
	limit = min(limit, 50)
	rows, err := f.st.PathSpeeds(ctx, f.now().Add(-store.PathSpeedKeepHours*time.Hour), m.NodeId, limit)
	if err != nil {
		return nil, internalErr(f.log.Error, "read path speeds", err)
	}
	names := map[string]string{}
	name := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		n, err := f.st.Node(ctx, id)
		if err != nil {
			return id
		}
		names[id] = n.Name
		return n.Name
	}
	out := &probev1.PathSpeedResultsResponse{}
	for _, r := range rows {
		var j speedJSON
		if json.Unmarshal([]byte(r.ResultJSON), &j) != nil {
			continue
		}
		out.Tests = append(out.Tests, speedMessage(name(r.FromID), name(r.ToID), r.Direction, r.At, j))
	}
	// the last channel measure of each node: it stays when the tests of its run are gone
	last, err := f.st.ChannelMeasures(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "read channel measures", err)
	}
	ids := make([]string, 0, len(last))
	for id := range last {
		if m.NodeId == "" || id == m.NodeId {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return last[ids[i]].At.After(last[ids[j]].At) })
	for _, id := range ids {
		var l channelLastJSON
		if json.Unmarshal([]byte(last[id].ResultJSON), &l) != nil {
			continue
		}
		out.Channels = append(out.Channels, &probev1.ChannelNodeLast{NodeName: name(id), Last: lastMessage(&l, last[id].At)})
	}
	return connect.NewResponse(out), nil
}

func (f *Fleet) runPathSpeed(ctx context.Context, from, to store.NodeRow, direction string, mb uint32) speedJSON {
	return f.runSpeedMode(ctx, from, to, direction, mb, false)
}

// runSpeedMode is one speed test between two nodes: the target serves, the sender runs, and the target's listener is closed again
// whatever happens. channel is the 8-stream timed mode (agent.proto "CHANNEL MEASURE"), the same steps with other codes and
// limits, and mb is then the volume cap of the step in MiB; the sender is stopped too when the call is cancelled.
func (f *Fleet) runSpeedMode(ctx context.Context, from, to store.NodeRow, direction string, mb uint32, channel bool) speedJSON {
	res := speedJSON{MB: mb}
	capability, serveStep, runStep, serveS, runS := capSpeed, uint32(1), uint32(2), uint32(speedMaxS), uint32(speedMaxS)
	runWait := speedRunWait
	if channel {
		capability, serveStep, runStep, serveS, runS = capSpeedChannel, speedStepServeChannel, speedStepRunChannel, channelServeS, channelRunS
		runWait = channelRunWait
	}
	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for path speed", "code", "store_failed", "err", err)
		res.Error = "failed"
		return res
	}
	live := liveRowsByID(liveRows)
	fromLive, code := f.probeLive(live, from.ID, capability)
	if code == "" {
		_, code = f.probeLive(live, to.ID, capability)
	}
	if code != "" {
		res.Error = code
		return res
	}
	toLive := live[to.ID]
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		res.Error = "failed"
		return res
	}
	// the commands travel in the UdpSend message (agent.proto "PATH SPEED" says what each field means there)
	test := func(role, host string, port uint32, reqID string) *agentv1.ConnectResponse {
		u := &agentv1.UdpSend{RequestId: reqID, Host: host, Tag: token[:], Count: mb, Df: direction == "up",
			Size: map[string]uint32{"serve": serveStep, "run": runStep, "stop": 3}[role]}
		u.Pps = serveS
		if role == "run" {
			u.Pps = runS
		}
		if port != 0 {
			u.Ports = []uint32{port}
		}
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SpeedTest{SpeedTest: u}}
	}
	armed, err := f.commandWithLive(ctx, to.ID, speedServeWait, func(reqID string) *agentv1.ConnectResponse {
		return test("serve", "", 0, reqID)
	}, &toLive)
	if err != nil {
		res.Error = "failed"
		return res
	}
	if armed == nil || !armed.Ok {
		res.Error = speedCommandError(armed)
		return res
	}
	// The test fills the link of both nodes from here on, whoever started it (the owner, the assistant or a relay's own
	// proof): the capacity report must not read that hour's traffic as people's.
	defer func() {
		f.noteSpeedTest(ctx, from.ID)
		f.noteSpeedTest(ctx, to.ID)
	}()
	// whatever happens next, the target's listener is closed again: it lives for one connection or 30 s otherwise
	defer func() {
		stop := func(id string, live *store.NodeLiveRow) {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), speedStopWait)
			defer cancel()
			_, _ = f.commandWithLive(stopCtx, id, speedStopWait, func(reqID string) *agentv1.ConnectResponse {
				return test("stop", "", 0, reqID)
			}, live)
		}
		if channel && ctx.Err() != nil {
			// cancelled: the sender may still be moving data, and it does not know the panel stopped waiting; each end is told on its own,
			// so one that does not answer does not hold the other up
			done := make(chan struct{})
			go func() { stop(from.ID, &fromLive); close(done) }()
			stop(to.ID, &toLive)
			<-done
			return
		}
		stop(to.ID, &toLive)
	}()
	port, err := strconv.ParseUint(armed.Params["port"], 10, 16)
	if err != nil || port == 0 {
		res.Error = "failed"
		return res
	}
	ran, err := f.commandWithLive(ctx, from.ID, runWait, func(reqID string) *agentv1.ConnectResponse {
		return test("run", to.Address, uint32(port), reqID)
	}, &fromLive)
	if err != nil {
		res.Error = "failed"
		return res
	}
	if ran == nil || !ran.Ok {
		res.Error = speedCommandError(ran)
		return res
	}
	if channel {
		return parseChannelResult(ran.Params, mb)
	}
	return parseSpeedResult(ran.Params, mb)
}

// parseSpeedResult reads the sender's numbers; a number that makes no sense is a failed test, never a figure.
func parseSpeedResult(p map[string]string, mb uint32) speedJSON {
	res := speedJSON{MB: mb}
	mbit, err := strconv.ParseFloat(p["mbit_s"], 64)
	bytes, errB := strconv.ParseUint(p["bytes"], 10, 64)
	dur, errD := strconv.ParseUint(p["duration_ms"], 10, 32)
	if err != nil || errB != nil || errD != nil || math.IsNaN(mbit) || mbit <= 0 || mbit > 1_000_000 || bytes == 0 || bytes > uint64(mb)<<20 {
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
	res.Bytes, res.DurationMS = bytes, uint32(min(dur, 3_600_000))
	res.ConnectMS, res.FirstByteMS = ms("connect_ms"), ms("first_byte_ms")
	return res
}

// speedCommandError is the short code of an agent's answer: its text is never passed on.
func speedCommandError(result *agentv1.CommandResult) string {
	if result == nil {
		return "failed"
	}
	switch strings.SplitN(result.Error, ":", 2)[0] {
	case "busy":
		return "busy"
	case "refused":
		return "unreachable"
	case "too_soon":
		return "too_soon"
	case "timeout":
		return "timeout"
	case "no_route", "unsupported_host":
		return "no_route"
	default:
		return "failed"
	}
}

func speedMessage(fromName, toName, direction string, at time.Time, r speedJSON) *probev1.PathSpeedResponse {
	return &probev1.PathSpeedResponse{FromName: fromName, ToName: toName, Direction: direction, Mb: r.MB, AtUnix: at.Unix(), Error: r.Error,
		MbitS: r.MbitS, ConnectMs: r.ConnectMS, FirstByteMs: r.FirstByteMS, DurationMs: r.DurationMS, Bytes: r.Bytes, Streams: r.Streams, Capped: r.Capped}
}

// SpeedSummary is the newest stored speed test of a pair and direction, for the digest views.
type SpeedSummary struct {
	FromID, ToID, Direction string
	At                      time.Time
	MbitS                   float64
	FirstByteMS             uint32
}

// LastSpeeds is the newest stored test of every pair and direction, newest first.
func (f *Fleet) LastSpeeds(ctx context.Context, since time.Time, nodeID string) ([]SpeedSummary, error) {
	rows, err := f.st.PathSpeeds(ctx, since, nodeID, 200)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []SpeedSummary
	for _, r := range rows {
		key := r.FromID + "|" + r.ToID + "|" + r.Direction
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		var j speedJSON
		if json.Unmarshal([]byte(r.ResultJSON), &j) != nil || j.MbitS <= 0 {
			continue
		}
		out = append(out, SpeedSummary{FromID: r.FromID, ToID: r.ToID, Direction: r.Direction, At: r.At, MbitS: j.MbitS, FirstByteMS: j.FirstByteMS})
	}
	return out, nil
}
