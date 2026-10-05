package agent

import (
	"context"
	"errors"
	"strconv"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/speedtest"
)

// The bandwidth test (agent.proto "BANDWIDTH TEST"): the panel asks, the node downloads and uploads against a public speed
// server for about ten seconds and answers with one CommandResult. Nothing is stored and no setting changes here.

// capBandwidth is listed in Hello.capabilities: the panel sends MeasureBandwidth only to agents that have it.
const capBandwidth = "bandwidth/1"

// measureBandwidth answers a MeasureBandwidth. One at a time: a second request while one runs is "busy". A stream that ends
// meanwhile ends the test too (its context), so a closed admin page does not leave the node pushing traffic.
func (a *Agent) measureBandwidth(s *session, r *pb.MeasureBandwidth) {
	if !a.measuring.CompareAndSwap(false, true) {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	defer a.measuring.Store(false)
	run := a.cfg.SpeedTest
	if run == nil {
		run = func(ctx context.Context) (speedtest.Result, error) { return speedtest.Run(ctx, speedtest.Default()) }
	}
	res, err := run(s.ctx)
	switch {
	case s.ctx.Err() != nil:
		return // nobody to tell
	case errors.Is(err, speedtest.ErrUnreachable):
		a.log.Warn("bandwidth test: no test server answered", "err", err)
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unreachable"}))
		return
	case err != nil:
		a.log.Warn("bandwidth test failed", "err", err)
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "failed: " + clipText(err.Error(), 100)}))
		return
	}
	whole := func(v float64) string { return strconv.FormatInt(int64(v+0.5), 10) }
	a.log.Info("bandwidth test", "server", res.Server, "down_mbps", whole(res.DownMbps), "up_mbps", whole(res.UpMbps), "seconds", whole(res.Seconds))
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Detail: res.Server, Params: map[string]string{
		"down_mbps":  whole(res.DownMbps),
		"up_mbps":    whole(res.UpMbps),
		"server":     res.Server,
		"streams":    strconv.Itoa(res.DownStreams),
		"down_bytes": strconv.FormatInt(res.DownBytes, 10),
		"up_bytes":   strconv.FormatInt(res.UpBytes, 10),
		"seconds":    whole(res.Seconds),
	}}))
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
