//go:build !js

package subhost

import (
	"context"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The first subscription host is installed over SSH (internal/panel/provision): a panel behind NAT cannot be reached by the new
// server, so the node enrols inside the SSH session and the role turns on with the install, without the RPCs' check that the
// node is online (it can only come online through the bridge the role starts).

// PrepareInstall checks that a first host may be installed for domain (and siteName) on the server sshHost: the role is off,
// the names are host names, every node can follow the panel to a new link address, and both names point at the server unless
// skipDNS. It returns the bridge path the install gives the node; nothing is stored.
func (s *Service) PrepareInstall(ctx context.Context, sshHost, domain, siteName string, skipDNS bool) (string, error) {
	domain, err := normDomain(domain)
	if err != nil {
		return "", err
	}
	if siteName, err = normSiteName(siteName); err != nil {
		return "", err
	}
	if siteName == "" {
		return "", unfit("site_name_required") // a fresh server has no inbound whose site could stand on its 443
	}
	if siteName == domain {
		return "", unfit("site_name_conflict")
	}
	st, err := loadState(ctx, s.cfg.Store)
	if err != nil {
		return "", s.internal("read state", err)
	}
	if st.State != stateOff {
		return "", wrongState()
	}
	lacking, err := s.lackingLink(ctx)
	if err != nil {
		return "", s.internal("read nodes", err)
	}
	if len(lacking) > 0 {
		return "", coded(connect.CodeFailedPrecondition, "sub_host_nodes_lack_link", "nodes", refIDs(lacking))
	}
	if !skipDNS {
		c, err := s.dnsOf(ctx, store.NodeRow{Address: sshHost}, domain, siteName)
		if err != nil {
			return "", s.internal("read node facts", err)
		}
		if !c.matches {
			return "", dnsMismatch(c)
		}
	}
	return newPath(), nil
}

// ActivateInstalled turns the role on for the node the install enrolled: off -> on, not ready. Running it again for the same
// install (the panel died in it, or its undo was refused) finds the role on for that node, domain and path, and is no change.
func (s *Service) ActivateInstalled(ctx context.Context, nodeID, domain, siteName, path, pin string) error {
	domain, err := normDomain(domain)
	if err != nil {
		return err
	}
	if siteName, err = normSiteName(siteName); err != nil {
		return err
	}
	again := false
	err = s.transition(ctx, func(st *state) error {
		switch {
		case st.State == stateOn && st.Node == nodeID && st.Domain == domain && st.Path == path:
			again = true
			if pin != "" {
				st.BootstrapPin = pin
			}
			return nil
		case st.State != stateOff:
			return wrongState()
		}
		*st = state{State: stateOn, Domain: domain, Node: nodeID, Path: path, SiteName: siteName, BootstrapPin: pin,
			PrevURL: st.PublicURL, PublicURL: st.PublicURL}
		return nil
	})
	if err == nil && !again {
		s.audit(ctx, "sub_host_set", map[string]string{"domain": domain, "node_id": nodeID, "site_name": siteName, "via": "install"})
	}
	return err
}

// DeactivateInstalled turns the role off again after an install that did not complete. A host that is ready is in use:
// it is left alone (sub_host_in_use).
func (s *Service) DeactivateInstalled(ctx context.Context, nodeID string) error {
	done := false
	err := s.transition(ctx, func(st *state) error {
		if st.State != stateOn || st.Node != nodeID {
			return nil
		}
		if st.ReadyAt != "" {
			return coded(connect.CodeFailedPrecondition, "sub_host_in_use")
		}
		*st, done = state{PublicURL: st.PrevURL}, true
		return nil
	})
	if err == nil && done {
		s.audit(ctx, "sub_host_unset", map[string]string{"node_id": nodeID, "via": "install"})
	}
	return err
}
