//go:build !js

package subhost

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The first subscription host is installed over SSH (internal/panel/provision): the panel behind NAT cannot be reached by
// the new server, so the node enrols inside the SSH session and the role turns on with the install, without the RPCs'
// check that the node is online (it can only come online through the tunnel the role starts).

// State is the stored state: "" (off), "on", "moving" or "leaving".
func (s *Service) State() string {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	return s.cur.State
}

// PrepareInstall checks that a first host may be installed for domain on the server sshHost (the role is off, the domain
// is a host name, every node can follow the panel to a new link address, and the domain points at the server unless
// skipDNS) and returns the tunnel SNI the install gives the node. Nothing is stored.
func (s *Service) PrepareInstall(ctx context.Context, sshHost, domain string, skipDNS bool) (string, error) {
	domain, err := normDomain(domain)
	if err != nil {
		return "", err
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
		ids := make([]string, len(lacking))
		for i, l := range lacking {
			ids[i] = l.NodeId
		}
		return "", coded(connect.CodeFailedPrecondition, "sub_host_nodes_lack_link", "nodes", strings.Join(ids, ","))
	}
	if !skipDNS {
		c, err := s.checkDNS(ctx, store.NodeRow{Address: sshHost}, domain)
		if err != nil {
			return "", s.internal("read node facts", err)
		}
		if !c.matches {
			return "", coded(connect.CodeFailedPrecondition, "sub_host_dns_mismatch", "found", strings.Join(c.found, ","), "expected", strings.Join(c.expected, ","))
		}
	}
	return tunnelSNI(domain), nil
}

// ActivateInstalled turns the role on for the node an install just enrolled, with the domain and tunnel SNI PrepareInstall
// gave (off → on). Already on for the same node, domain and SNI it changes nothing: an install the panel died in, or one
// whose undo was refused because another node exists, is retried without the owner turning the host off first.
func (s *Service) ActivateInstalled(ctx context.Context, nodeID, domain, sni string) error {
	domain, err := normDomain(domain)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(sni, "."+domain) {
		return coded(connect.CodeInvalidArgument, "sub_host_tunnel_sni_invalid")
	}
	again := false
	err = s.transition(ctx, func(st *state) error {
		if st.State == stateOn && st.Node == nodeID && st.Domain == domain && st.SNI == sni {
			again = true
			return nil
		}
		if st.State != stateOff {
			return wrongState()
		}
		n, err := s.cfg.Store.Node(ctx, nodeID)
		if err != nil || n.State == "retired" {
			return coded(connect.CodeFailedPrecondition, "sub_host_node_unfit", "reason", "not_enrolled")
		}
		prev := st.PublicURL
		if prev == "" && s.cfg.Addr != nil {
			prev = s.cfg.Addr.PublicURL()
		}
		*st = state{State: stateOn, Domain: domain, Node: nodeID, SNI: sni, PrevURL: prev, PublicURL: "https://" + domain}
		return nil
	})
	if err != nil || again {
		return err
	}
	s.audit(ctx, "sub_host_set", map[string]string{"domain": domain, "node_id": nodeID, "via": "install"})
	return nil
}

// DeactivateInstalled undoes ActivateInstalled for an install that did not complete (on → off): only while the node is the
// host and no other node exists that may already follow the new address.
func (s *Service) DeactivateInstalled(ctx context.Context, nodeID string) error {
	err := s.transition(ctx, func(st *state) error {
		if st.State != stateOn || st.Node != nodeID {
			return wrongState()
		}
		nodes, err := s.cfg.Store.Nodes(ctx, false)
		if err != nil {
			return s.internal("read nodes", err)
		}
		for _, n := range nodes {
			if n.ID != nodeID && n.State != "retired" {
				return coded(connect.CodeFailedPrecondition, "sub_host_in_use")
			}
		}
		*st = state{PublicURL: st.PrevURL}
		return nil
	})
	if err != nil {
		return err
	}
	s.audit(ctx, "sub_host_unset", map[string]string{"node_id": nodeID, "via": "install"})
	return nil
}
