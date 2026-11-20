//go:build !js

package subhost

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"

	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/gen/mistgate/subhost/v1/subhostv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Handler is the SubscriptionHostService mount.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return subhostv1connect.NewSubscriptionHostServiceHandler(rpc{s}, opts...)
}

type rpc struct{ s *Service }

func (r rpc) GetSubscriptionHost(ctx context.Context, _ *connect.Request[subhostv1.GetSubscriptionHostRequest]) (*connect.Response[subhostv1.GetSubscriptionHostResponse], error) {
	h, err := r.s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&subhostv1.GetSubscriptionHostResponse{Host: h}), nil
}

// dnsOf checks that domain and, when given, siteName point at the node: both must.
func (s *Service) dnsOf(ctx context.Context, n store.NodeRow, domain, siteName string) (dnsCheck, error) {
	c, err := s.checkDNS(ctx, n, domain)
	if err != nil || siteName == "" || !c.matches {
		return c, err
	}
	return s.checkDNS(ctx, n, siteName)
}

func dnsMismatch(c dnsCheck) error {
	return coded(connect.CodeFailedPrecondition, "sub_host_dns_mismatch", "found", strings.Join(c.found, ","), "expected", strings.Join(c.expected, ","))
}

func (r rpc) CheckSubscriptionHostDomain(ctx context.Context, req *connect.Request[subhostv1.CheckSubscriptionHostDomainRequest]) (*connect.Response[subhostv1.CheckSubscriptionHostDomainResponse], error) {
	s := r.s
	domain, err := normDomain(req.Msg.Domain)
	if err != nil {
		return nil, err
	}
	siteName, err := normSiteName(req.Msg.SiteName)
	if err != nil {
		return nil, err
	}
	n, err := s.cfg.Store.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, s.internal("read node", err)
	}
	c, err := s.dnsOf(ctx, n, domain, siteName)
	if err != nil {
		return nil, s.internal("read node facts", err)
	}
	return connect.NewResponse(&subhostv1.CheckSubscriptionHostDomainResponse{Matches: c.matches, Found: c.found, Expected: c.expected}), nil
}

func (r rpc) SetSubscriptionHost(ctx context.Context, req *connect.Request[subhostv1.SetSubscriptionHostRequest]) (*connect.Response[subhostv1.SetSubscriptionHostResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	domain, err := normDomain(req.Msg.Domain)
	if err != nil {
		return nil, err
	}
	siteName, err := normSiteName(req.Msg.SiteName)
	if err != nil {
		return nil, err
	}
	err = s.transition(ctx, func(st *state) error {
		if st.State != stateOff {
			return wrongState()
		}
		n, err := s.fitNode(ctx, req.Msg.NodeId, domain, siteName)
		if err != nil {
			return err
		}
		lacking, err := s.lackingLink(ctx)
		if err != nil {
			return s.internal("read nodes", err)
		}
		if len(lacking) > 0 {
			return coded(connect.CodeFailedPrecondition, "sub_host_nodes_lack_link", "nodes", refIDs(lacking))
		}
		if !req.Msg.SkipDnsCheck {
			c, err := s.dnsOf(ctx, n, domain, siteName)
			if err != nil {
				return s.internal("read node facts", err)
			}
			if !c.matches {
				return dnsMismatch(c)
			}
		}
		// The panel keeps its address until the host is ready (readiness moves it to https://<domain>).
		*st = state{State: stateOn, Domain: domain, Node: n.ID, Path: newPath(), SiteName: siteName, PrevURL: st.PublicURL, PublicURL: st.PublicURL}
		return nil
	})
	if err != nil {
		return nil, err
	}
	params := map[string]string{"domain": domain, "node_id": req.Msg.NodeId}
	if siteName != "" {
		params["site_name"] = siteName
	}
	if req.Msg.SkipDnsCheck {
		params["skip_dns_check"] = "true"
	}
	s.audit(ctx, "sub_host_set", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&subhostv1.SetSubscriptionHostResponse{Host: h}), nil
}

func (r rpc) StartSubscriptionHostMove(ctx context.Context, req *connect.Request[subhostv1.StartSubscriptionHostMoveRequest]) (*connect.Response[subhostv1.StartSubscriptionHostMoveResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	siteName, err := normSiteName(req.Msg.SiteName)
	if err != nil {
		return nil, err
	}
	var from string
	err = s.transition(ctx, func(st *state) error {
		if st.State != stateOn {
			return wrongState()
		}
		if req.Msg.NodeId == st.Node {
			return coded(connect.CodeInvalidArgument, "sub_host_same_node")
		}
		n, err := s.fitNode(ctx, req.Msg.NodeId, st.Domain, siteName)
		if err != nil {
			return err
		}
		from = st.Node
		// The next node gets a bridge path of its own: the old host never learns the path its successor serves.
		st.State, st.Next, st.NextSiteName, st.NextPath = stateMoving, n.ID, siteName, newPath()
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_move_start", map[string]string{"node_id": from, "next_node_id": req.Msg.NodeId})
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&subhostv1.StartSubscriptionHostMoveResponse{Host: h}), nil
}

func (r rpc) FinishSubscriptionHostChange(ctx context.Context, req *connect.Request[subhostv1.FinishSubscriptionHostChangeRequest]) (*connect.Response[subhostv1.FinishSubscriptionHostChangeResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	params := map[string]string{}
	err := s.transition(ctx, func(st *state) error {
		params["domain"], params["node_id"] = st.Domain, st.Node
		switch st.State {
		case stateMoving:
			params["from"] = "moving"
			if !req.Msg.Force {
				n, err := s.cfg.Store.Node(ctx, st.Next)
				if err != nil {
					return s.internal("read node", err)
				}
				c, err := s.checkDNS(ctx, n, st.Domain)
				if err != nil {
					return s.internal("read node facts", err)
				}
				if !c.matches {
					return dnsMismatch(c)
				}
			}
			st.State, st.Node, st.SiteName, st.Path = stateOn, st.Next, st.NextSiteName, st.NextPath
			st.Next, st.NextSiteName, st.NextPath, st.BootstrapPin = "", "", "", ""
			params["node_id"] = st.Node
		case stateLeaving:
			params["from"] = "leaving"
			if !req.Msg.Force {
				p, err := s.pending(ctx, *st)
				if err != nil {
					return s.internal("read nodes", err)
				}
				if len(p) > 0 {
					ids := make([]string, len(p))
					for i, n := range p {
						ids[i] = n.NodeId
					}
					return coded(connect.CodeFailedPrecondition, "sub_host_nodes_pending", "nodes", strings.Join(ids, ","))
				}
			}
			*st = state{PublicURL: st.PublicURL}
		default:
			return wrongState()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if req.Msg.Force {
		params["force"] = "true"
	}
	s.audit(ctx, "sub_host_finish", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&subhostv1.FinishSubscriptionHostChangeResponse{Host: h}), nil
}

func (r rpc) CancelSubscriptionHostChange(ctx context.Context, _ *connect.Request[subhostv1.CancelSubscriptionHostChangeRequest]) (*connect.Response[subhostv1.CancelSubscriptionHostChangeResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	params := map[string]string{}
	err := s.transition(ctx, func(st *state) error {
		params["domain"], params["node_id"] = st.Domain, st.Node
		switch st.State {
		case stateMoving:
			params["from"], params["next_node_id"] = "moving", st.Next
			st.State, st.Next, st.NextSiteName, st.NextPath = stateOn, "", "", ""
		case stateLeaving:
			params["from"] = "leaving"
			st.State = stateOn
			if st.ReadyAt != "" {
				st.PublicURL = "https://" + st.Domain
			} else {
				st.PublicURL = st.PrevURL
			}
		default:
			return wrongState()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_cancel", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&subhostv1.CancelSubscriptionHostChangeResponse{Host: h}), nil
}

func (r rpc) DisableSubscriptionHost(ctx context.Context, req *connect.Request[subhostv1.DisableSubscriptionHostRequest]) (*connect.Response[subhostv1.DisableSubscriptionHostResponse], error) {
	s := r.s
	if err := s.stepUp(ctx); err != nil {
		return nil, err
	}
	back, err := returnURL(req.Msg.PublicUrl)
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	err = s.transition(ctx, func(st *state) error {
		if st.State != stateOn {
			return wrongState()
		}
		if st.ReadyAt == "" {
			// Never ready: nothing moved to the subdomain, so the panel stays on the address it has and no agent has to come
			// back. The role goes at once.
			params["domain"], params["node_id"], params["from"] = st.Domain, st.Node, "not_ready"
			*st = state{PublicURL: st.PrevURL}
			return nil
		}
		if back == "" && !req.Msg.Force {
			nodes, err := s.cfg.Store.Nodes(ctx, false)
			if err != nil {
				return s.internal("read nodes", err)
			}
			if slices.ContainsFunc(nodes, func(n store.NodeRow) bool { return n.State != "retired" && n.ID != st.Node }) {
				return coded(connect.CodeInvalidArgument, "sub_host_return_url_required")
			}
		}
		params["domain"], params["node_id"] = st.Domain, st.Node
		if back != "" {
			params["public_url"] = back
		}
		st.State, st.PublicURL = stateLeaving, back
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "sub_host_disable", params)
	h, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&subhostv1.DisableSubscriptionHostResponse{Host: h}), nil
}
