//go:build !js

package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/gen/mistgate/probe/v1/probev1connect"
)

// channel_measure: how wide a node's channel really is. It is path_speed in its heavy mode (the same listener, token and caps on both
// ends): 8 parallel streams for 10 seconds in each direction against the nodes with the widest known channel, so a link that one
// stream cannot fill shows its width. It fills the links of the node and its partners, so it is plan/apply and waits for the owner
// in the admin panel, like path_speed. It never changes the node's channel number: that is the owner's to write.

const (
	dangerChannelTraffic = "channel_traffic"            // it moves up to 2 GiB per direction through the node and up to three partners
	channelWaitFor       = applyTimeout - 8*time.Second // the apply answers before its own limit; a longer run is read back from path_speed_results
)

// channelPollEvery is how often the apply asks how the run goes (a test shortens it).
var channelPollEvery = time.Second

type channelMeasureArgs struct {
	ReasonField
	Node string `json:"node" jsonschema:"the node whose channel to measure: id or exact name; the partners are chosen by the panel, never named by you"`
}

type channelMeasureStored struct {
	Node string `json:"node"`
}

// channelErrors are the reasons a measure did not run, in words.
var channelErrors = map[string]string{
	"node_offline":  "the node, or a partner, is not connected",
	"agent_too_old": "the agent of the node cannot measure the channel: update it",
	"busy":          "the node or its partners are running a test or a bandwidth measurement",
	"too_soon":      "this node was measured less than 10 minutes ago",
	"no_partner":    "no other node is connected with an agent that supports it",
	"limit":         "the panel is running its most probes already",
	"unreachable":   "the node could not connect to a partner's test port (a firewall on the partner may block it)",
	"timeout":       "a transfer did not finish in time",
	"no_route":      "a partner's address is not usable for a test",
	"canceled":      "the owner stopped the measure",
	"failed":        "the measure failed",
}

var channelMeasure = changeSpec[channelMeasureArgs]{
	name: "channel_measure", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, probev1connect.ProbeServiceChannelMeasurePlanProcedure,
		probev1connect.ProbeServiceChannelMeasureStartProcedure, probev1connect.ProbeServiceChannelMeasureStatusProcedure),
	desc: "Measure how wide a node's channel really is: the node moves data over 8 parallel TCP streams for 10 seconds in each direction (receiving, then sending) against up to three of your other nodes with the widest known channel, the one-time listener and token of path_speed on the partner, and the best figure per direction is kept with the partner that gave it. " +
		"One stream shows only a lower bound; this shows what the link carries when it is filled, so it answers 'is the number the owner wrote right'. " +
		"It fills the links of the node and its partners for about 25 seconds per partner and moves up to 2 GiB per direction per partner (up to 12 GB in all), so people on those nodes are slower meanwhile, a node with paid traffic pays for it, and it always needs the owner's approval in the admin panel. It is capped on both ends independently (2 GiB and 15 seconds a direction), one measure per node at a time and not more than one in 10 minutes per node. " +
		"The panel chooses the partners (connected nodes with a new enough agent, never the node itself, the widest known channel first; error no_partner when there is none); you cannot name a host. Read the figures with path_speed_results after approval. " +
		"The figure is an estimate at the moment of the measure (other traffic on the links counts against it, and a partner's own link may be the narrower one), not a guarantee of the tariff: say 'measured about N Mbit/s at this time', never 'the provider gives N'. It never changes the node's channel setting: tell the owner to write the number themselves if they want it. Node names only, never addresses.",
	plan: func(c *call, a channelMeasureArgs) (*planned, error) {
		node, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		r, err := c.cl.Probe.ChannelMeasurePlan(c.ctx, connect.NewRequest(&probev1.ChannelMeasurePlanRequest{NodeId: node}))
		if err != nil {
			return nil, probeError(err)
		}
		m := r.Msg
		if code := clean(m.GetError(), 40); code != "" {
			return nil, channelRefusal(nm(name), code, m.GetRetryAfterS())
		}
		partners := make([]string, 0, len(m.GetPartners()))
		for _, p := range m.GetPartners() {
			known := "channel not known"
			if p.GetKnownMbps() > 0 {
				known = strconv.FormatUint(uint64(p.GetKnownMbps()), 10) + " Mbit/s known"
			}
			partners = append(partners, fmt.Sprintf("%s (%s)", nm(p.GetName()), known))
		}
		maxBytes := uint64(m.GetMaxGb()) << 30
		summary := fmt.Sprintf("Measure the channel of %s against %d other node(s): 8 parallel streams for 10 seconds each way, up to %d GB through the links. The owner has to approve it first.",
			nm(name), len(m.GetPartners()), m.GetMaxGb())
		return &planned{
			Summary: summary,
			Facts: []Fact{
				{Key: "node", Value: nm(name), Untrusted: true},
				{Key: "partners", Value: strings.Join(partners, ", "), Untrusted: true},
				codedFact("size", "up to "+strconv.FormatUint(uint64(m.GetMaxGb()), 10)+" GB", "bytes", "bytes", strconv.FormatUint(maxBytes, 10)),
				secondsFact("duration", m.GetEstSeconds()),
				codedFact("effect", "The node and each partner take turns: a partner opens a listener on a free port for 8 streams (a one-time token, closed after the eighth stream or 20 seconds), the node receives for 10 seconds and then sends for 10 seconds. Both ends stop at 2 GiB and 15 seconds a direction whatever happens. People on these nodes are slower while it runs and a node with paid traffic pays for it. Nothing is changed on any node, no public server is used, and the node's channel setting is not touched; the figures are kept for 7 days.", "channel_measure_effect"),
			},
			Danger: []string{dangerChannelTraffic},
			Params: channelMeasureStored{Node: node},
		}, nil
	},
	applyStored: func(c *call, pl Plan) (done, error) {
		var a channelMeasureStored
		if err := strictJSON([]byte(pl.ParamsJSON), &a); err != nil || a.Node == "" {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		r, err := c.cl.Probe.ChannelMeasureStart(c.ctx, connect.NewRequest(&probev1.ChannelMeasureStartRequest{NodeId: a.Node}))
		if err != nil {
			return done{}, probeError(err)
		}
		st := r.Msg.GetStatus()
		if code := clean(st.GetError(), 40); code != "" {
			return done{}, notRun(nm(st.GetNodeName()), code)
		}
		// the run goes on in the panel: wait for it, within the apply's own time
		deadline := time.Now().Add(channelWaitFor)
		for st.GetState() == "running" && time.Now().Before(deadline) {
			select {
			case <-c.ctx.Done():
				return done{}, apiError(c.ctx.Err())
			case <-time.After(channelPollEvery):
			}
			s, err := c.cl.Probe.ChannelMeasureStatus(c.ctx, connect.NewRequest(&probev1.ChannelMeasureStatusRequest{NodeId: a.Node}))
			if err != nil {
				return done{}, probeError(err)
			}
			st = s.Msg
		}
		return channelDone(st)
	},
	apply: func(_ *call, _ channelMeasureArgs, _ Plan) (done, error) {
		return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
	},
}

// channelRefusal is why the plan cannot be made, as the error the model reads.
func channelRefusal(name, code string, retryAfter uint32) error {
	why, ok := channelErrors[code]
	if !ok {
		code, why = "failed", channelErrors["failed"]
	}
	if code == "too_soon" && retryAfter > 0 {
		why += ": try again in " + strconv.Itoa(int((retryAfter+59)/60)) + " minute(s)"
	}
	return fmt.Errorf("the channel of %s cannot be measured now: %s (%s)", name, code, why)
}

func notRun(name, code string) error {
	why, ok := channelErrors[code]
	if !ok {
		code, why = "failed", channelErrors["failed"]
	}
	return failure("channel_measure_not_run", fmt.Sprintf("The channel measure of %s did not run: %s (%s).", name, code, why), "code", code)
}

// channelDone is the line an agent reads after a measure: the figures, or the reason nothing was measured.
func channelDone(st *probev1.ChannelMeasureStatusResponse) (done, error) {
	name := nm(st.GetNodeName())
	switch st.GetState() {
	case "running":
		return doneWith("channel_measure_done", fmt.Sprintf("The channel measure of %s is still running: read path_speed_results in a minute for its figures.", name),
			"node", name, "direction", "running"), nil
	case "done":
		res := st.GetResult()
		dn, up := res.GetDown(), res.GetUp()
		line := func(label string, d *probev1.ChannelDirection) string {
			if d.GetMbitS() <= 0 {
				return label + ": did not work"
			}
			s := fmt.Sprintf("%s: %.1f Mbit/s with %s over %d streams", label, d.GetMbitS(), nm(d.GetPartner()), d.GetStreams())
			if d.GetCapped() {
				s += " (the volume cap ended it early: at least that)"
			}
			return s
		}
		text := fmt.Sprintf("Channel of %s measured against %d node(s): %s; %s. An estimate at this moment, not a guarantee of the tariff.",
			name, res.GetPartners(), line("receiving", dn), line("sending", up))
		kv := []string{"node", name, "down_mbps", strconv.FormatFloat(dn.GetMbitS(), 'f', 1, 64), "up_mbps", strconv.FormatFloat(up.GetMbitS(), 'f', 1, 64),
			"down_partner", nm(dn.GetPartner()), "up_partner", nm(up.GetPartner())}
		switch {
		case up.GetMbitS() <= 0:
			kv = append(kv, "direction", "down")
		case dn.GetMbitS() <= 0:
			kv = append(kv, "direction", "up")
		}
		return doneWith("channel_measure_done", text, kv...), nil
	case "canceled":
		return done{}, notRun(name, "canceled")
	}
	code := clean(st.GetError(), 40)
	if code == "" {
		code = "failed"
	}
	return done{}, notRun(name, code)
}

// the figures of the last channel measure of a node, for path_speed_results
type channelDirV struct {
	MbitS   float64 `json:"mbit_s"`
	Partner string  `json:"partner,omitempty"`
	Streams uint32  `json:"streams,omitempty"`
	Capped  bool    `json:"capped,omitempty"`
}

type channelLastV struct {
	Node     string      `json:"node"`
	At       string      `json:"at"`
	Receive  channelDirV `json:"receive"`
	Send     channelDirV `json:"send"`
	Partners uint32      `json:"partners"`
}

func channelViews(in []*probev1.ChannelNodeLast) []channelLastV {
	out := []channelLastV{}
	dir := func(d *probev1.ChannelDirection) channelDirV {
		return channelDirV{MbitS: d.GetMbitS(), Partner: nm(d.GetPartner()), Streams: d.GetStreams(), Capped: d.GetCapped()}
	}
	for _, c := range in {
		l := c.GetLast()
		out = append(out, channelLastV{Node: nm(c.GetNodeName()), At: fmtTime(l.GetAtUnix()), Receive: dir(l.GetDown()), Send: dir(l.GetUp()), Partners: l.GetPartners()})
	}
	return out
}
