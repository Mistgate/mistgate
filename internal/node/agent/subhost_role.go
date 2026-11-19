package agent

import (
	"net/netip"
	"slices"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The subscription host role (agent.proto "SUBSCRIPTION HOST"). The panel sends it in NodeSettings.sub_host; the agent keeps
// a copy in agent.json (Meta.SubHost), because the host's own session to the panel runs through the role: after a restart it
// must route the subscription names and accept the panel before it has heard from the panel.

// SubHostMeta is the role as agent.json keeps it. Path is a secret (the file is 0600) and is never logged.
type SubHostMeta struct {
	Names       []string `json:"names"`
	Path        string   `json:"path"`
	SiteName    string   `json:"site_name,omitempty"`
	LinkSources []string `json:"link_sources,omitempty"`
}

func subHostMetaOf(r *pb.SubHostRole) *SubHostMeta {
	if r == nil || len(r.Names) == 0 || r.Path == "" {
		return nil
	}
	return &SubHostMeta{Names: slices.Clone(r.Names), Path: r.Path, SiteName: r.SiteName, LinkSources: slices.Clone(r.LinkSources)}
}

func sameSubHostMeta(a, b *SubHostMeta) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Names, b.Names) && a.Path == b.Path && a.SiteName == b.SiteName && slices.Equal(a.LinkSources, b.LinkSources)
}

// role is the plugin's view of m; prefixes that do not parse are left out.
func (m *SubHostMeta) role() plugin.SubHostRole {
	r := plugin.SubHostRole{Names: slices.Clone(m.Names), Path: m.Path, SiteName: m.SiteName}
	for _, s := range m.LinkSources {
		if p, err := netip.ParsePrefix(s); err == nil {
			r.LinkSources = append(r.LinkSources, p.Masked())
		}
	}
	return r
}

// hostRole is the node's subscription host role, ok false when it is none.
func (a *Agent) hostRole() (plugin.SubHostRole, bool) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.SubHost == nil {
		return plugin.SubHostRole{}, false
	}
	return a.meta.SubHost.role(), true
}

// applySubHost takes in NodeSettings.sub_host: the role is saved to agent.json before anything runs on it, so a restart
// in between comes back with the new role. changed: the role is not what it was (the fronts must be planned again).
func (a *Agent) applySubHost(st *pb.NodeSettings) (changed bool) {
	if st == nil {
		return false
	}
	next := subHostMetaOf(st.GetSubHost())
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if sameSubHostMeta(a.meta.SubHost, next) {
		return false
	}
	a.meta.SubHost = next
	if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
		a.log.Error("subscription host role not saved", "code", "sub_host_role_not_saved", "err", err)
	}
	if next == nil {
		a.log.Info("subscription host role removed")
	} else {
		a.log.Info("subscription host role set", "names", next.Names) // never the path
	}
	return true
}

// subHostChanged asks the worker for a sweep, which plans the fronts again with the new role; a busy worker gets to it on
// its next tick.
func (a *Agent) subHostChanged() {
	if a.jobs == nil {
		return
	}
	select {
	case a.jobs <- job{sweep: true}:
	default:
	}
}
