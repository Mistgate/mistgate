package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/node/subhost"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The subscription host (agent.proto "SUBSCRIPTION HOST"). planShares adds the host's routes to the front of TCP 443
// (plugin.SubHostRoutes); after the fronts are synced, syncHost sets the host's bridge entry (the panel is its only exit),
// the intake of the subscription route and, when no inbound on 443 serves the node's site, the agent's own site. An unfit
// role, or a 443 that cannot be bound, runs none of it, and the inbounds run as they would without it.

// subHostPort is the host's port: 443, another one in tests on loopback.
var subHostPort uint16 = plugin.SubHostPort

// hostFrontDown is SubHostStatus.error when TCP 443 cannot be bound for the host's front.
const hostFrontDown = "front_down"

type hostState struct {
	unfit string // the role's unfit reason (planShares), "" = fits

	intake      *subhost.Intake
	intakeFront *front.Front
	site        *hostSite
	siteFront   *front.Front
	siteName    string
	acmeID      string
	selfID      string
	acmeCert    func() bool // the site's ACME certificate is issued
	err         string
}

const (
	hostSiteACMEID = "mistgate-sub-host-site"
	hostSiteSelfID = "mistgate-sub-host-site-self"
)

// hostExtra is planShares' part: the host's extra routes for the inbounds to run, or none (with the reason) when it does
// not fit.
func (a *Agent) hostExtra(specs []plugin.InboundSpec) []plugin.TCPExtraRoute {
	a.hostMu.Lock()
	defer a.hostMu.Unlock()
	role, ok := a.hostRole()
	if !ok {
		a.sh.unfit = ""
		return nil
	}
	extra, unfit := plugin.SubHostRoutesOn(subHostPort, role, specs)
	if unfit != a.sh.unfit && unfit != "" {
		a.log.Warn("subscription host role does not fit the node's inbounds", "code", "sub_host_unfit", "reason", unfit)
	}
	a.sh.unfit = unfit
	return extra
}

// syncHost brings the host's entry, intake and site to the role and the fronts just synced.
func (a *Agent) syncHost(ctx context.Context, fronts map[uint16]plugin.TCPPortShare) {
	a.hostMu.Lock()
	defer a.hostMu.Unlock()
	role, ok := a.hostRole()
	share, planned := fronts[subHostPort]
	a.frontMu.Lock()
	var f *front.Front
	if sp := a.shared[subHostPort]; sp != nil {
		f = sp.f
	}
	a.frontMu.Unlock()
	routed := planned && slicesHasRoute(share, plugin.SubHostRouteID)
	switch {
	case !ok:
		a.stopHost("")
		return
	case a.sh.unfit != "":
		a.stopHost(a.sh.unfit)
		return
	case !routed || f == nil:
		a.stopHost(hostFrontDown)
		return
	}
	id := a.id.Load()
	if id == nil {
		a.stopHost("identity_unavailable")
		return
	}
	if err := a.hostEntry.SetServe(nodebridge.ServeConfig{EntryNodeID: a.meta.NodeID, Path: role.Path, ExitNodeIDs: []string{nodebridge.PanelPeerID},
		Roots: id.ca, Now: a.now, Peer: nodebridge.PeerPanel, PassClient: true}); err != nil {
		a.stopHost("bad_config")
		return
	}
	if a.sh.intake == nil || a.sh.intakeFront != f {
		if a.sh.intake != nil {
			a.sh.intake.Close()
		}
		in := subhost.New(a.hostEntry, f, plugin.SubHostRouteID, a.log.With("source", "sub-host"))
		go in.Serve(f.Listener(plugin.SubHostRouteID))
		a.sh.intake, a.sh.intakeFront = in, f
	}
	a.sh.intake.SetSources(role.LinkSources)
	a.sh.err = ""
	if slicesHasRoute(share, plugin.SubHostSiteRouteID) {
		if err := a.syncHostSite(ctx, f, role.SiteName); err != nil {
			a.sh.err = "site_certificate_unavailable"
		}
	} else {
		a.stopHostSite()
	}
}

func slicesHasRoute(p plugin.TCPPortShare, id string) bool {
	for _, r := range p.Routes {
		if r.ID == id {
			return true
		}
	}
	return false
}

// syncHostSite serves the agent's own site on the front's site route: ACME for name, a self-signed certificate of the same
// name until it is issued. hostMu is held.
func (a *Agent) syncHostSite(ctx context.Context, f *front.Front, name string) error {
	if a.cfg.Certs == nil {
		return errors.New("certificate_source_unavailable")
	}
	if a.sh.site != nil && a.sh.siteFront == f && a.sh.siteName == name && a.sh.site.running() {
		return nil
	}
	a.stopHostSite()
	acmeCert, err := a.cfg.Certs.Acquire(ctx, hostSiteACMEID, plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: name})
	if err != nil {
		return err
	}
	selfCert, err := a.cfg.Certs.Acquire(ctx, hostSiteSelfID, plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: name})
	if err != nil {
		a.cfg.Certs.Release(hostSiteACMEID)
		return err
	}
	site := newHostSite(f.Listener(plugin.SubHostSiteRouteID), certs.SiteCertificate(acmeCert, selfCert),
		a.hostEntry.Handler(a.bridgeEntry.Handler(a.decoySite.Handler())))
	if selfCert.Info != nil {
		site.setPin(selfCert.Info().PinSHA256)
	}
	a.sh.site, a.sh.siteFront, a.sh.siteName = site, f, name
	a.sh.acmeID, a.sh.selfID = hostSiteACMEID, hostSiteSelfID
	a.sh.acmeCert = func() bool { return acmeCert.Info != nil && !acmeCert.Info().NotAfter.IsZero() }
	return nil
}

func (a *Agent) stopHostSite() {
	if a.sh.site != nil {
		a.sh.site.close()
		a.sh.site, a.sh.siteFront, a.sh.siteName, a.sh.acmeCert = nil, nil, "", nil
	}
	if a.cfg.Certs != nil {
		if a.sh.acmeID != "" {
			a.cfg.Certs.Release(a.sh.acmeID)
		}
		if a.sh.selfID != "" {
			a.cfg.Certs.Release(a.sh.selfID)
		}
	}
	a.sh.acmeID, a.sh.selfID = "", ""
}

// stopHost stops everything of the role; reason is what SubHostStatus.error says ("" = no role). hostMu is held.
func (a *Agent) stopHost(reason string) {
	if a.sh.intake != nil {
		a.sh.intake.Close()
		a.sh.intake, a.sh.intakeFront = nil, nil
	}
	a.stopHostSite()
	if a.hostEntry != nil {
		_ = a.hostEntry.SetServe(nodebridge.ServeConfig{})
	}
	a.sh.err = reason
}

// subHostStatus is StatsBatch.sub_host: nil when the node has no role.
func (a *Agent) subHostStatus() *pb.SubHostStatus {
	if _, ok := a.hostRole(); !ok {
		return nil
	}
	a.hostMu.Lock()
	defer a.hostMu.Unlock()
	st := &pb.SubHostStatus{Error: a.sh.err}
	if a.sh.intake != nil {
		s := a.sh.intake.Stats()
		st.ClientsSub, st.ClientsLink, st.Diverted, st.RefusedPerSource = s.ClientsSub, s.ClientsLink, s.Diverted, s.RefusedPerSource
	}
	for _, p := range a.hostEntry.States() {
		if p.PeerNodeID == nodebridge.PanelPeerID {
			st.PanelConns = p.Conns
		}
	}
	if a.sh.site != nil {
		st.SitePinSha256 = a.sh.site.pin()
		st.SiteCertPublic = a.sh.acmeCert != nil && a.sh.acmeCert()
	}
	return st
}

// dialHostLink is the host's own link to the panel through its bridge (DialContext of the link's HTTP client); ok false when
// the node is no running host or addr is no subscription name, and the link dials as any agent's does.
func (a *Agent) dialHostLink(addr string) (func(ctx context.Context, network, address string) (net.Conn, error), bool) {
	role, ok := a.hostRole()
	if !ok {
		return nil, false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	match := false
	for _, n := range role.Names {
		if n == host {
			match = true
		}
	}
	a.hostMu.Lock()
	in := a.sh.intake
	a.hostMu.Unlock()
	if !match || in == nil {
		return nil, false
	}
	return func(ctx context.Context, _, _ string) (net.Conn, error) { return in.DialLink(ctx) }, true
}

// hostSite is the agent's own HTTPS site on a route of the front (the bridge site's settings: sitehttp, short timeouts).
type hostSite struct {
	server *http.Server
	mu     sync.Mutex
	leaf   string
	done   chan struct{}
}

func newHostSite(ln net.Listener, getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), handler http.Handler) *hostSite {
	s := &hostSite{done: make(chan struct{})}
	s.server = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	_ = sitehttp.ConfigureSiteHTTP2(s.server)
	tlsLn := tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := getCertificate(hello)
			if err == nil && cert != nil && len(cert.Certificate) != 0 {
				sum := sha256.Sum256(cert.Certificate[0])
				s.setPin(hex.EncodeToString(sum[:]))
			}
			return cert, err
		}})
	go func() {
		defer close(s.done)
		_ = s.server.Serve(tlsLn)
	}()
	return s
}

func (s *hostSite) setPin(pin string) {
	s.mu.Lock()
	s.leaf = pin
	s.mu.Unlock()
}

func (s *hostSite) pin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaf
}

func (s *hostSite) running() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *hostSite) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = s.server.Shutdown(ctx)
	cancel()
	_ = s.server.Close()
	<-s.done
}
