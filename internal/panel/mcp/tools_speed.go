//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"strconv"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/gen/mistgate/probe/v1/probev1connect"
)

// path_speed: a bounded throughput test between two of your nodes. It moves bulk traffic (up to 32 MB through the nodes' own
// links), so like every action that spends something it is plan/apply and waits for the owner in the admin panel, the way
// the panel's own bandwidth test (a button for the owner only) is closed to tokens. Targets are nodes by id: there is no way
// to name a host, and the result carries node names, figures and timings, never an address.

const (
	dangerTraffic = "traffic" // it sends bulk test traffic between nodes

	speedDefaultMB = 16
	speedMinMB     = 8
	speedMaxMB     = 32
)

type pathSpeedArgs struct {
	ReasonField
	From      string `json:"from" jsonschema:"the node that measures: id or exact name"`
	To        string `json:"to" jsonschema:"the other node, which serves the data: id or exact name; it is never a host you name"`
	MB        int    `json:"mb,omitempty" jsonschema:"how much data to move, 8 to 32 megabytes; empty = 16"`
	Direction string `json:"direction,omitempty" jsonschema:"down = from pulls the data from to (default); up = from pushes it to to"`
}

type pathSpeedStored struct {
	From      string `json:"from"`
	To        string `json:"to"`
	MB        int    `json:"mb"`
	Direction string `json:"direction"`
}

// speedErrors are the reasons a test did not run, in the words of an agent.
var speedErrors = map[string]string{
	"node_offline":  "one of the nodes is not connected",
	"agent_too_old": "the agent of one node cannot run speed tests: update it",
	"busy":          "one of the nodes is already running a test or a bandwidth measurement",
	"unreachable":   "the sender could not connect to the target's test port (a firewall on the target may block it)",
	"timeout":       "the transfer did not finish in 30 seconds",
	"no_route":      "the target's address is not usable for a test",
	"failed":        "the test failed",
}

var pathSpeed = changeSpec[pathSpeedArgs]{
	name: "path_speed", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, probev1connect.ProbeServicePathSpeedProcedure),
	desc: "Measure the throughput of the path between two of your nodes: the to node serves 8 to 32 megabytes over a short-lived TCP listener (one connection, a one-time token, closed after 30 seconds at most) and the from node pulls them (direction down, the default) or pushes them (up). " +
		"The result is megabits per second over the whole transfer, the connect time, the time to the first byte (down) and the duration. It moves bulk traffic through both nodes' links, so it always needs the owner's approval in the admin panel, and it is capped: at most 32 MB and 30 seconds, one test per node at a time, 6 per node and 12 per token in a day. " +
		"Targets are your own nodes only, never a public server (use node_ports and path_probe to find a bad path first; this tells how fast a good path is). Both agents must be online and support speed tests (error agent_too_old otherwise). Results are kept 7 days. The target's test port must be reachable from the sender: a firewall on the target can make it unreachable. Node names only, never addresses.",
	plan: func(c *call, a pathSpeedArgs) (*planned, error) {
		mb, dir, err := speedArgs(a.MB, a.Direction)
		if err != nil {
			return nil, err
		}
		from, fromName, err := c.nodeRef(a.From)
		if err != nil {
			return nil, err
		}
		to, toName, err := c.nodeRef(a.To)
		if err != nil {
			return nil, err
		}
		if from == to {
			return nil, errors.New("from and to must be two different nodes")
		}
		flow := "the data flows from the target to the sender (a download by the sender)"
		if dir == "up" {
			flow = "the data flows from the sender to the target (an upload by the sender)"
		}
		summary := fmt.Sprintf("Run a speed test between two of your nodes: %d MB of test data, at most 30 seconds, through both nodes' own links. The owner has to approve it first.", mb)
		return &planned{
			Summary: summary,
			Facts: []Fact{
				{Key: "sender", Value: nm(fromName), Untrusted: true},
				{Key: "target", Value: nm(toName), Untrusted: true},
				codedFact("direction", flow, "path_speed_"+dir),
				codedFact("size", strconv.Itoa(mb)+" MB", "bytes", "bytes", strconv.Itoa(mb<<20)),
				{Key: "effect", Value: "The target opens a listener on a free port for one connection (a one-time token, closed after 30 seconds at most); the sender connects to it. Nothing else runs, nothing is changed on either node, and no public server is used. Both nodes carry the traffic once; the figure is kept for 7 days."},
			},
			Danger: []string{dangerTraffic},
			Params: pathSpeedStored{From: from, To: to, MB: mb, Direction: dir},
		}, nil
	},
	applyStored: func(c *call, pl Plan) (done, error) {
		var a pathSpeedStored
		if err := strictJSON([]byte(pl.ParamsJSON), &a); err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		mb, dir, err := speedArgs(a.MB, a.Direction)
		if err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		r, err := c.cl.Probe.PathSpeed(c.ctx, connect.NewRequest(&probev1.PathSpeedRequest{
			FromNodeId: a.From, ToNodeId: a.To, Mb: uint32(mb), Direction: dir}))
		if err != nil {
			return done{}, probeError(err)
		}
		return speedDone(r.Msg)
	},
	apply: func(_ *call, _ pathSpeedArgs, _ Plan) (done, error) {
		return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
	},
}

func speedArgs(mb int, dir string) (int, string, error) {
	if mb == 0 {
		mb = speedDefaultMB
	}
	if mb < speedMinMB || mb > speedMaxMB {
		return 0, "", fmt.Errorf("mb: %d to %d", speedMinMB, speedMaxMB)
	}
	switch dir {
	case "":
		dir = "down"
	case "down", "up":
	default:
		return 0, "", errors.New("direction: down or up")
	}
	return mb, dir, nil
}

// speedDone is the line an agent reads after a test: the figures, or the reason it did not run. Names, numbers and timings only.
func speedDone(m *probev1.PathSpeedResponse) (done, error) {
	if code := clean(m.GetError(), 40); code != "" {
		why, ok := speedErrors[code]
		if !ok {
			code, why = "failed", speedErrors["failed"]
		}
		return done{}, failure("path_speed_not_run", fmt.Sprintf("The speed test between %s and %s did not run: %s (%s).",
			nm(m.GetFromName()), nm(m.GetToName()), code, why), "code", code)
	}
	dir := "from the target to the sender"
	if m.GetDirection() == "up" {
		dir = "from the sender to the target"
	}
	text := fmt.Sprintf("Speed test %s to %s, %d MB %s: %.1f Mbit/s over %d ms, connect %d ms",
		nm(m.GetFromName()), nm(m.GetToName()), m.GetMb(), dir, m.GetMbitS(), m.GetDurationMs(), m.GetConnectMs())
	if m.GetDirection() != "up" {
		text += fmt.Sprintf(", first byte after %d ms", m.GetFirstByteMs())
	}
	return doneWith("path_speed_done", text+"."), nil
}
