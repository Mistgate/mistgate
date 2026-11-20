package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	bridgeSiteCertID = "mistgate-bridge-site"
	// bridgeSiteCertName names the self-signed certificate of the agent's own bridge site. It is what any scanner reads, so it
	// is the most common name of a self-signed default server; nothing in it names this software or its purpose.
	bridgeSiteCertName = "localhost"
)

func (a *Agent) syncBridgeServe(ctx context.Context, next *model) {
	a.bridgeServeError = ""
	if next.bridgeServe == nil {
		a.clearBridgeServe()
		return
	}
	id := a.id.Load()
	if id == nil {
		a.clearBridgeServe()
		a.bridgeServeError = "identity_unavailable"
		return
	}
	serve := next.bridgeServe
	panelAddress := ""
	panelVia := serve.PanelVia
	if panelVia {
		panelAddress = a.bridgePanelAddress()
		panelVia = panelAddress != ""
	}
	if err := a.bridgeEntry.SetServe(nodebridge.ServeConfig{EntryNodeID: a.meta.NodeID, Path: serve.Path, ExitNodeIDs: serve.ExitNodeIDs,
		PanelVia: panelVia, PanelAddress: panelAddress, Roots: id.ca, Now: a.now}); err != nil {
		a.clearBridgeServe()
		a.bridgeServeError = "bad_config"
		return
	}
	if serve.Port == 0 {
		if a.bridgeSite != nil {
			a.bridgeSite.Close()
			a.bridgeSite = nil
		}
		return
	}
	if !bridgeSitePortAvailable(next, serve.Port, a.host.SSHPorts()) {
		if a.bridgeSite != nil {
			a.bridgeSite.Close()
			a.bridgeSite = nil
		}
		a.bridgeServeError = "port_conflict"
		return
	}
	getCertificate, release, pin, err := a.bridgeSiteCertificate(ctx, next)
	if err != nil {
		if a.bridgeSite != nil {
			a.bridgeSite.Close()
			a.bridgeSite = nil
		}
		a.bridgeServeError = "site_certificate_unavailable"
		return
	}
	if a.bridgeSite == nil {
		a.bridgeSite = &bridgeSiteServer{}
	}
	if err := a.bridgeSite.Sync(serve.Port, getCertificate, pin, a.bridgeEntry.Handler(a.decoySite.Handler())); err != nil {
		if release != nil {
			release()
		}
		a.bridgeServeError = "site_listen_failed"
		return
	}
	if code := a.bridgeSite.ErrorCode(); code != "" {
		a.bridgeServeError = code
	}
	a.bridgeSite.SetRelease(release)
}

// bridgePanelAddress is the entry agent's own panel endpoint. It is never read from a bridge request or the exit's desired targets.
func (a *Agent) bridgePanelAddress() string {
	if a.meta.Panel != "" {
		host, port, err := net.SplitHostPort(a.meta.Panel)
		if err == nil && host != "" && port != "" {
			return a.meta.Panel
		}
		return ""
	}
	link := a.linkURL() // a link node's cfg.LinkURL is its meta.Link: New and every new-address commit set both
	u, err := parseLinkBase(link)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func (a *Agent) clearBridgeServe() {
	if a.bridgeEntry != nil {
		_ = a.bridgeEntry.SetServe(nodebridge.ServeConfig{})
	}
	if a.bridgeSite != nil {
		a.bridgeSite.Close()
		a.bridgeSite = nil
	}
}

func bridgeSitePortAvailable(m *model, port uint16, ssh []uint16) bool {
	if port == 0 {
		return false
	}
	for _, p := range ssh {
		if p == port {
			return false
		}
	}
	for _, relay := range m.relays {
		if relay.Network == "tcp" && relay.Port == port {
			return false
		}
	}
	for _, id := range m.ids() {
		in := m.inbounds[id]
		if in.parseErr == "" && in.applyErr == "" && in.spec.Enabled && in.spec.Listen.Network == "tcp" && in.spec.Listen.Port == port {
			return false
		}
		if in.parseErr == "" && in.spec.Listen.Network == "tcp" && in.spec.Listen.HopFrom != 0 && port >= in.spec.Listen.HopFrom && port <= in.spec.Listen.HopTo {
			return false
		}
	}
	return port == 443 || port >= hostMinPort
}

const hostMinPort = 1024

func (a *Agent) bridgeSiteCertificate(ctx context.Context, m *model) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), func(), string, error) {
	if a.cfg.Certs != nil {
		for _, id := range m.ids() {
			in := m.inbounds[id]
			if in.parseErr != "" || in.applyErr != "" || !in.spec.Enabled || in.spec.TLS.Mode == 0 {
				continue
			}
			acquireID := fmt.Sprintf("%s-%d", bridgeSiteCertID, a.bridgeCertSequence.Add(1))
			cert, err := a.cfg.Certs.Acquire(ctx, acquireID, in.spec.TLS)
			if err == nil && cert.GetCertificate != nil {
				return cert.GetCertificate, func() { a.cfg.Certs.Release(acquireID) }, certificatePin(cert), nil
			}
			if err == nil {
				a.cfg.Certs.Release(acquireID)
			}
		}
		acquireID := fmt.Sprintf("%s-%d", bridgeSiteCertID, a.bridgeCertSequence.Add(1))
		cert, err := a.cfg.Certs.Acquire(ctx, acquireID, plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: bridgeSiteCertName})
		if err != nil {
			return nil, nil, "", err
		}
		if cert.GetCertificate == nil {
			a.cfg.Certs.Release(acquireID)
			return nil, nil, "", errors.New("certificate_unavailable")
		}
		return cert.GetCertificate, func() { a.cfg.Certs.Release(acquireID) }, certificatePin(cert), nil
	}
	return nil, nil, "", errors.New("certificate_source_unavailable")
}

func certificatePin(cert engine.Cert) string {
	if cert.Info == nil {
		return ""
	}
	return cert.Info().PinSHA256
}

func (a *Agent) bridgeServesExit(serve *plugin.BridgeServe, exitID string) bool {
	if a.bridgeServeError != "" || serve == nil || serve.IsZero() || a.bridgeEntry == nil {
		return false
	}
	for _, id := range serve.ExitNodeIDs {
		if id == exitID {
			return true
		}
	}
	return false
}

func (a *Agent) syncBridgeDials(ctx context.Context, next *model, results []*pb.InboundResult) {
	if a.bridgeDialer == nil {
		return
	}
	byID := make(map[string]*pb.InboundResult, len(results))
	for _, result := range results {
		byID[result.InboundId] = result
	}
	specs := make([]nodebridge.DialSpec, 0, len(next.bridgeDials))
	for _, dial := range next.bridgeDials {
		spec := nodebridge.DialSpec{Dial: dial}
		spec.TargetErrors = make(map[string]string)
		if len(dial.Targets) == 0 {
			spec.Error = "bad_config"
		} else {
			for _, target := range dial.Targets {
				if !bridgeTargetIsLocal(next, byID, target) {
					spec.TargetErrors[target.RelayID] = "target_not_local"
				}
			}
		}
		if len(spec.TargetErrors) == len(dial.Targets) && len(dial.Targets) > 0 {
			spec.Error = "target_not_local"
		}
		specs = append(specs, spec)
	}
	a.bridgeDialer.Sync(ctx, specs)
}

func bridgeTargetIsLocal(m *model, results map[string]*pb.InboundResult, target plugin.BridgeTarget) bool {
	if target.Network != "tcp" || target.Port == 0 {
		return false
	}
	for _, id := range m.ids() {
		in := m.inbounds[id]
		if in.parseErr != "" || in.applyErr != "" || !in.spec.Enabled || in.spec.Listen.Network != target.Network || in.spec.Listen.Port != target.Port {
			continue
		}
		if result := results[id]; result != nil && result.Error == "" && result.State == pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
			return true // any inbound on the port that runs: others may share it, and the first need not be the one that works
		}
	}
	return false
}

// bridgeInfo is the part of the worker-owned bridge state that the stats goroutine reads; the worker publishes a fresh one after
// every reconcile, so nothing here is shared mutably.
type bridgeInfo struct {
	serveError  string
	site        *bridgeSiteServer
	relayErrors map[string]string
}

func (a *Agent) publishBridgeInfo() {
	info := &bridgeInfo{serveError: a.bridgeServeError, site: a.bridgeSite, relayErrors: make(map[string]string, len(a.relay.results))}
	for _, result := range a.relay.results {
		info.relayErrors[result.RelayId] = bridgeRelayStateError(result.Error)
	}
	a.bridgeInfo.Store(info)
}

func (a *Agent) bridgeResults() []*pb.BridgeState {
	states := make(map[string]*pb.BridgeState)
	entry := func(id string) *pb.BridgeState {
		if states[id] == nil {
			states[id] = &pb.BridgeState{PeerNodeId: id}
		}
		return states[id]
	}
	if a.bridgeEntry != nil {
		for _, state := range a.bridgeEntry.States() {
			out := entry(state.PeerNodeID)
			out.Conns += state.Conns
			out.Streams += state.Streams
			out.ClientsNow += state.Clients
			out.BytesIn += state.BytesIn
			out.BytesOut += state.BytesOut
			if !state.Since.IsZero() && (out.SinceUnix == 0 || state.Since.Unix() < out.SinceUnix) {
				out.SinceUnix = state.Since.Unix()
			}
			if state.Error != "" {
				out.Error = state.Error
			}
		}
	}
	if a.bridgeDialer != nil {
		for _, state := range a.bridgeDialer.States() {
			out := entry(state.PeerNodeID)
			out.Conns += state.Conns
			out.Streams += state.Streams
			out.BytesIn += state.BytesIn
			out.BytesOut += state.BytesOut
			if !state.Since.IsZero() && (out.SinceUnix == 0 || state.Since.Unix() < out.SinceUnix) {
				out.SinceUnix = state.Since.Unix()
			}
			if state.RTT > 0 {
				out.RttMs = uint32(state.RTT.Milliseconds())
			}
			if state.Error != "" {
				out.Error = state.Error
			}
		}
	}
	model := a.snapshotModel()
	if model == nil {
		model = newModel()
	}
	info := a.bridgeInfo.Load()
	if info == nil {
		info = &bridgeInfo{}
	}
	if desired := model.bridgeServe; desired != nil && !desired.IsZero() {
		serveError := info.serveError
		if info.site != nil {
			if code := info.site.ErrorCode(); code != "" {
				serveError = code
			}
		}
		sitePin := ""
		if info.site != nil && info.site.Port() != 0 {
			sitePin = info.site.CertificatePin()
		}
		for _, id := range desired.ExitNodeIDs {
			out := entry(id)
			out.SiteCertPinSha256 = sitePin
			if serveError != "" && out.Error == "" {
				out.Error = serveError
			}
		}
	}
	for _, relay := range model.relays {
		if relay.Mode != "reverse" {
			continue
		}
		out := entry(relay.ExitNodeID)
		if out.Error == "" {
			out.Error = info.relayErrors[relay.ID]
		}
	}
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*pb.BridgeState, 0, len(ids))
	for _, id := range ids {
		out = append(out, states[id])
	}
	return out
}

func bridgeRelayStateError(err string) string {
	switch err {
	case "bridge_unavailable", "listener_failed", "relay_table", "unsupported_host":
		return err
	case "bad_reverse_config":
		return "bad_config"
	case "":
		return ""
	default:
		return "relay_unavailable"
	}
}

type bridgeSiteServer struct {
	mu       sync.Mutex
	certMu   sync.RWMutex
	port     uint16
	ln       net.Listener
	server   *http.Server
	release  func()
	getCert  func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	pin      string
	lastCode string
}

func (s *bridgeSiteServer) Sync(port uint16, getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), pin string, handler http.Handler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == port && s.server != nil && s.lastCode == "" {
		s.certMu.Lock()
		s.getCert = getCertificate
		if pin != "" {
			s.pin = pin
		}
		s.certMu.Unlock()
		return nil
	}
	s.stopLocked()
	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		ErrorLog: log.New(io.Discard, "", 0)}
	if err := sitehttp.ConfigureSiteHTTP2(server); err != nil {
		_ = ln.Close()
		return err
	}
	s.certMu.Lock()
	s.getCert = getCertificate
	s.pin = pin
	s.certMu.Unlock()
	s.port, s.ln, s.server, s.lastCode = port, ln, server, ""
	tlsListener := tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}, GetCertificate: s.certificate})
	go func() {
		if err := server.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.lastCode = "site_listen_failed"
			s.mu.Unlock()
		}
	}()
	return nil
}

func (s *bridgeSiteServer) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.certMu.RLock()
	getCertificate := s.getCert
	s.certMu.RUnlock()
	if getCertificate == nil {
		return nil, errors.New("identity_unavailable")
	}
	cert, err := getCertificate(hello)
	if err == nil && cert != nil && len(cert.Certificate) != 0 {
		sum := sha256.Sum256(cert.Certificate[0])
		s.certMu.Lock()
		s.pin = hex.EncodeToString(sum[:])
		s.certMu.Unlock()
	}
	return cert, err
}

func (s *bridgeSiteServer) CertificatePin() string {
	s.certMu.RLock()
	defer s.certMu.RUnlock()
	return s.pin
}

func (s *bridgeSiteServer) SetRelease(release func()) {
	s.mu.Lock()
	if s.release != nil {
		s.release()
	}
	s.release = release
	s.mu.Unlock()
}

func (s *bridgeSiteServer) Port() uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

func (s *bridgeSiteServer) ErrorCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCode
}

func (s *bridgeSiteServer) Close() {
	s.mu.Lock()
	s.stopLocked()
	s.mu.Unlock()
}

func (s *bridgeSiteServer) stopLocked() {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = s.server.Shutdown(ctx)
		cancel()
		_ = s.server.Close()
		_ = s.ln.Close()
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
	s.certMu.Lock()
	s.getCert = nil
	s.pin = ""
	s.certMu.Unlock()
	s.port, s.server, s.ln = 0, nil, nil
}
