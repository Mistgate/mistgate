//go:build !js

package fleet

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
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
}

// startSpeed admits a test between two nodes: it applies the limits and takes both nodes. The returned function gives them
// back.
func (f *Fleet) startSpeed(ctx context.Context, a, b string) (func(), error) {
	actor := f.cfg.Actor(ctx)
	now := f.now()
	p := &f.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.speedBusy == nil {
		p.speedBusy, p.speedCaller, p.speedNode = map[string]struct{}{}, map[string][]time.Time{}, map[string][]time.Time{}
	}
	recent := func(times []time.Time) []time.Time {
		return slices.DeleteFunc(slices.Clone(times), func(t time.Time) bool { return now.Sub(t) >= speedDayWindow })
	}
	retry := func(times []time.Time) error {
		wait := speedDayWindow - now.Sub(times[0])
		return connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("speed test limit reached: try again in %d minutes", int(wait.Minutes())+1))
	}
	callerTimes := recent(p.speedCaller[actor])
	if len(callerTimes) >= speedCallerDayMax {
		return nil, retry(callerTimes)
	}
	nodeTimes := map[string][]time.Time{}
	for _, id := range []string{a, b} {
		if _, running := p.speedBusy[id]; running {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("a speed test already runs on one of these nodes"))
		}
		nodeTimes[id] = recent(p.speedNode[id])
		if len(nodeTimes[id]) >= speedNodeDayMax {
			return nil, retry(nodeTimes[id])
		}
	}
	p.speedCaller[actor] = append(callerTimes, now)
	for _, id := range []string{a, b} {
		p.speedNode[id] = append(nodeTimes[id], now)
		p.speedBusy[id] = struct{}{}
	}
	return func() {
		p.mu.Lock()
		delete(p.speedBusy, a)
		delete(p.speedBusy, b)
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

func (f *Fleet) runPathSpeed(ctx context.Context, from, to store.NodeRow, direction string, mb uint32) speedJSON {
	res := speedJSON{MB: mb}
	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for path speed", "code", "store_failed", "err", err)
		res.Error = "failed"
		return res
	}
	live := liveRowsByID(liveRows)
	fromLive, code := f.probeLive(live, from.ID, capSpeed)
	if code == "" {
		_, code = f.probeLive(live, to.ID, capSpeed)
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
		u := &agentv1.UdpSend{RequestId: reqID, Host: host, Tag: token[:], Count: mb, Pps: speedMaxS, Df: direction == "up",
			Size: map[string]uint32{"serve": 1, "run": 2, "stop": 3}[role]}
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
	// whatever happens next, the target's listener is closed again: it lives for one connection or 30 s otherwise
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), speedStopWait)
		defer cancel()
		_, _ = f.commandWithLive(stopCtx, to.ID, speedStopWait, func(reqID string) *agentv1.ConnectResponse {
			return test("stop", "", 0, reqID)
		}, &toLive)
	}()
	port, err := strconv.ParseUint(armed.Params["port"], 10, 16)
	if err != nil || port == 0 {
		res.Error = "failed"
		return res
	}
	ran, err := f.commandWithLive(ctx, from.ID, speedRunWait, func(reqID string) *agentv1.ConnectResponse {
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
		MbitS: r.MbitS, ConnectMs: r.ConnectMS, FirstByteMs: r.FirstByteMS, DurationMs: r.DurationMS, Bytes: r.Bytes}
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
