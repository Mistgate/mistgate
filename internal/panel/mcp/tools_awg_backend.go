//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// userspacePeerKiB is what a device costs the agent in the userspace AmneziaWG backend (internal/node/awg/choose.go).
const userspacePeerKiB = 26

type nodeAWGBackendArgs struct {
	ReasonField
	Node    string `json:"node" jsonschema:"node id or exact name"`
	Backend string `json:"backend" jsonschema:"auto, kernel or userspace"`
	// BeforeMode is the setting the plan saw; apply refuses when it moved.
	BeforeMode string `json:"before_mode,omitempty" jsonschema:"-"`
}

// awgBackendState reads what the plan needs: the setting, what the node reports as running, devices and their number,
// and whether the node serves userspace in place of a module that does not load.
func awgBackendState(c *call, id string) (mode, running string, fallback bool, devices, online uint32, hasAWG bool, err error) {
	r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
	if err != nil {
		return "", "", false, 0, 0, false, apiError(err)
	}
	mode = r.Msg.GetNode().GetAwgBackend()
	if mode == "" {
		mode = "auto"
	}
	for _, in := range r.Msg.GetInbounds() {
		if in.GetProtocol() != "awg" {
			continue
		}
		hasAWG = true
		if s := in.GetAwg(); s != nil {
			if running == "" {
				running = clean(s.GetBackend(), 16)
			}
			devices += s.GetPeers()
			online += s.GetPeersOnline()
		}
	}
	d, err := c.cl.Health.GetDoctor(c.ctx, connect.NewRequest(&adminv1.GetDoctorRequest{NodeId: id}))
	if err != nil {
		return "", "", false, 0, 0, false, apiError(err)
	}
	for _, n := range d.Msg.GetNodes() {
		for _, it := range n.GetItems() {
			if it.GetId() == "awg_backend" && it.GetDetailCode() == "awg_backend.fallback" {
				fallback = true
			}
		}
	}
	return mode, running, fallback, devices, online, hasAWG, nil
}

var nodeAWGBackend = changeSpec[nodeAWGBackendArgs]{
	// The minimum profile is the strictest role of the procedures it calls (TestRegistryMatchesPolicy): UpdateNode is a write of the
	// operator level, so the plan can be made from the operator profile up; UpdateNode is also TokenAccessApproved, so the apply
	// always waits for the owner (danger step_up).
	name: "node_awg_backend", min: ProfileOperator, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure,
		adminv1connect.HealthServiceGetDoctorProcedure, adminv1connect.NodeServiceUpdateNodeProcedure),
	desc: "Set how a node runs AmneziaWG: auto (the kernel module when it is loaded, else userspace), kernel (the module; an up-to-date agent serves userspace meanwhile when the module does not load, an older one stops AmneziaWG) or userspace (amneziawg-go inside the agent). " +
		"Always needs the owner's approval: the node's AmneziaWG interfaces are rebuilt and connected devices reconnect within about 20 seconds. The plan shows the current setting, what runs now, whether the node is on the userspace fallback, the devices online and, for userspace, the memory estimate. " +
		"It does not build the kernel module (that is the node's AmneziaWG settings or the node_fix awg_module_repair). A node whose agent is too old to follow the setting is refused at apply.",
	plan: func(c *call, a nodeAWGBackendArgs) (*planned, error) {
		if !slices.Contains([]string{"auto", "kernel", "userspace"}, a.Backend) {
			return nil, errors.New("backend must be auto, kernel or userspace")
		}
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		mode, running, fallback, devices, online, hasAWG, err := awgBackendState(c, id)
		if err != nil {
			return nil, err
		}
		if mode == a.Backend {
			return nil, errors.New("the node's AmneziaWG backend is already " + mode)
		}
		facts := []Fact{
			{Key: "node", Value: nm(name), Untrusted: true},
			{Key: "current_mode", Value: mode},
			{Key: "new_mode", Value: a.Backend},
			{Key: "running_backend", Value: orDash(running)},
			{Key: "userspace_fallback", Value: fmt.Sprint(fallback)},
			{Key: "awg_devices_online", Value: fmt.Sprint(online)},
		}
		if hasAWG {
			facts = append(facts, Fact{Key: "reconnect", Value: "the node's AmneziaWG interfaces are rebuilt; connected devices reconnect within about 20 seconds"})
		} else {
			facts = append(facts, Fact{Key: "reconnect", Value: "the node has no AmneziaWG profile: only the setting changes"})
		}
		if a.Backend != "kernel" && hasAWG {
			// The agent keeps every device in its own memory in userspace.
			facts = append(facts, Fact{Key: "userspace_memory", Value: fmt.Sprintf("about %d MiB for the %d devices on the node (%d KiB each), taken from the agent's memory budget",
				(devices*userspacePeerKiB+1023)/1024, devices, userspacePeerKiB)})
		}
		if a.Backend == "kernel" {
			facts = append(facts, Fact{Key: "module", Value: "the module has to be loaded on the node; an up-to-date agent serves userspace until it is, an older one stops AmneziaWG on that node"})
		}
		a.Node, a.BeforeMode = id, mode
		return &planned{Summary: "Change how one node runs AmneziaWG. The owner has to approve it first.", Facts: facts,
			Danger: []string{dangerStepUp}, Params: a}, nil
	},
	apply: func(c *call, a nodeAWGBackendArgs, _ Plan) (done, error) {
		mode, _, _, _, _, _, err := awgBackendState(c, a.Node)
		if err != nil {
			return done{}, err
		}
		if mode != a.BeforeMode {
			return done{}, failure("changed_since_plan", "the node's AmneziaWG backend changed after the plan; make a new plan")
		}
		backend := a.Backend
		if _, err := c.cl.Node.UpdateNode(c.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.Node, AwgBackend: &backend})); err != nil {
			if strings.Contains(err.Error(), "agent too old") {
				return done{}, failure("agent_too_old", "the node's agent is too old to follow this setting: update the agent first", "node", a.Node)
			}
			return done{}, apiError(err)
		}
		return doneWith("awg_backend_set", "The node's AmneziaWG backend is "+backend+" now; the interfaces are rebuilt and devices reconnect within about 20 seconds.", "node", a.Node, "backend", backend), nil
	},
}

func orDash(s string) string {
	if s == "" {
		return "not reported"
	}
	return s
}
