//go:build !js

package subhost

import (
	"context"
	"net/netip"

	"connectrpc.com/connect"
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
		// Only the A records here: the server's IPv6 is known once the install has looked at it (CheckInstallDNS).
		if c := s.installDNS(ctx, s.addressesOf(ctx, sshHost), domain, siteName, false); !c.matches {
			return "", dnsMismatch(c)
		}
	}
	return newPath(), nil
}

// CheckInstallDNS checks, once the install read the server's global IPv6 addresses, that both names point at the server: the
// A records at sshHost, the AAAA records (when there are any) at one of ipv6.
func (s *Service) CheckInstallDNS(ctx context.Context, sshHost, domain, siteName string, ipv6 []netip.Addr) error {
	domain, err := normDomain(domain)
	if err != nil {
		return err
	}
	if siteName, err = normSiteName(siteName); err != nil {
		return err
	}
	if c := s.installDNS(ctx, append(s.addressesOf(ctx, sshHost), ipv6...), domain, siteName, true); !c.matches {
		return dnsMismatch(c)
	}
	return nil
}

func (s *Service) installDNS(ctx context.Context, want []netip.Addr, domain, siteName string, check6 bool) dnsCheck {
	c := s.compareDNS(ctx, want, domain, check6)
	if !c.matches || siteName == "" {
		return c
	}
	return s.compareDNS(ctx, want, siteName, check6)
}

// ActivateInstalled turns the role on for the node the install enrolled: off -> on, not ready. Running it again for the same
// install (the panel died in it, or its undo was refused) finds the role on for that node, domain and path, and is no change.
func (s *Service) ActivateInstalled(ctx context.Context, actor, nodeID, domain, siteName, path, pin string) error {
	domain, err := normDomain(domain)
	if err != nil {
		return err
	}
	if siteName, err = normSiteName(siteName); err != nil {
		return err
	}
	if pin == "" {
		// the panel would have nothing to verify the host's site by until its agent reports, which it can do only through
		// the bridge that pin verifies
		return coded(connect.CodeFailedPrecondition, "sub_host_no_site_pin")
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
		s.auditAs(ctx, actor, "sub_host_set", map[string]string{"domain": domain, "node_id": nodeID, "site_name": siteName, "via": "install"})
	}
	return err
}

// DeactivateInstalled turns the role off again after an install that did not complete. A host that is ready is in use:
// it is left alone (sub_host_in_use).
func (s *Service) DeactivateInstalled(ctx context.Context, actor, nodeID string) error {
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
		s.auditAs(ctx, actor, "sub_host_unset", map[string]string{"node_id": nodeID, "via": "install"})
	}
	return err
}
