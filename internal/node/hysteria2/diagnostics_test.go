package hysteria2

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	quic "github.com/apernet/quic-go"
	"github.com/mistgate/mistgate/internal/plugin"
)

type testUDPProxy struct {
	clientConn *net.UDPConn
	serverAddr *net.UDPAddr

	mu         sync.RWMutex
	clientAddr *net.UDPAddr
	upstream   *net.UDPConn

	closed      chan struct{}
	closeOnce   sync.Once
	dropSignal  chan struct{}
	dropOnce    sync.Once
	dropAfter   atomic.Int64
	dropDown    atomic.Bool
	dropUp      atomic.Bool
	downBytes   atomic.Uint64
	downPackets atomic.Uint64
	lossEvery   atomic.Uint64
	upPackets   atomic.Uint64
	upLossEvery atomic.Uint64
	delayNanos  atomic.Int64
	delayed     chan delayedPacket // the delayed server-to-client datagrams, in order
}

type delayedPacket struct {
	due  time.Time
	data []byte
}

func newTestUDPProxy(t *testing.T, serverPort int) *testUDPProxy {
	t.Helper()
	serverAddr, err := net.ResolveUDPAddr("udp", bindAddr(serverPort))
	if err != nil {
		t.Fatal(err)
	}
	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: BindIP})
	if err != nil {
		t.Fatal(err)
	}
	p := &testUDPProxy{
		clientConn: clientConn, serverAddr: serverAddr, closed: make(chan struct{}), dropSignal: make(chan struct{}),
		delayed: make(chan delayedPacket, 4096),
	}
	if err := p.openUpstream(); err != nil {
		clientConn.Close()
		t.Fatal(err)
	}
	go p.readClient()
	go p.forwardDelayed()
	t.Cleanup(p.close)
	return p
}

func (p *testUDPProxy) openUpstream() error {
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: BindIP}, p.serverAddr)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.upstream = conn
	p.mu.Unlock()
	go p.readServer(conn)
	return nil
}

func (p *testUDPProxy) addr() *net.UDPAddr { return p.clientConn.LocalAddr().(*net.UDPAddr) }

func (p *testUDPProxy) upstreamAddr() *net.UDPAddr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.upstream.LocalAddr().(*net.UDPAddr)
}

func (p *testUDPProxy) readClient() {
	buf := make([]byte, 64<<10)
	for {
		n, addr, err := p.clientConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		clientAddr := *addr
		p.mu.Lock()
		p.clientAddr = &clientAddr
		upstream := p.upstream
		p.mu.Unlock()
		if p.dropUp.Load() || upstream == nil {
			continue
		}
		if every := p.upLossEvery.Load(); every > 0 && p.upPackets.Add(1)%every == 0 {
			continue
		}
		_, _ = upstream.Write(buf[:n])
	}
}

func (p *testUDPProxy) readServer(conn *net.UDPConn) {
	buf := make([]byte, 64<<10)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		packet := p.downPackets.Add(1)
		wireBytes := p.downBytes.Add(uint64(n))
		if limit := p.dropAfter.Load(); limit > 0 && wireBytes > uint64(limit) {
			p.dropDown.Store(true)
			p.dropOnce.Do(func() { close(p.dropSignal) })
		}
		lossEvery := p.lossEvery.Load()
		if p.dropDown.Load() || (lossEvery > 0 && packet%lossEvery == 0) {
			continue
		}
		data := append([]byte(nil), buf[:n]...)
		delay := time.Duration(p.delayNanos.Load())
		if delay == 0 {
			p.forward(data)
			continue
		}
		select {
		case p.delayed <- delayedPacket{due: time.Now().Add(delay), data: data}:
		default: // a full queue drops, like a full router buffer
		}
	}
}

// forwardDelayed sends the delayed datagrams one after another when they are due: one queue keeps their order (a timer
// per datagram reorders them under load, and quic-go counts reordered packets as lost).
func (p *testUDPProxy) forwardDelayed() {
	for {
		select {
		case <-p.closed:
			return
		case d := <-p.delayed:
			if wait := time.Until(d.due); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-p.closed:
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			p.forward(d.data)
		}
	}
}

func (p *testUDPProxy) forward(data []byte) {
	p.mu.RLock()
	addr := p.clientAddr
	p.mu.RUnlock()
	if addr != nil {
		_, _ = p.clientConn.WriteToUDP(data, addr)
	}
}

func (p *testUDPProxy) blackholeDownAfter(n uint64) {
	p.downBytes.Store(0)
	p.downPackets.Store(0)
	p.dropAfter.Store(int64(n))
}

func (p *testUDPProxy) waitDownBlackhole(t *testing.T) {
	t.Helper()
	select {
	case <-p.dropSignal:
	case <-time.After(8 * time.Second):
		t.Fatal("proxy did not start dropping server-to-client packets")
	}
}

func (p *testUDPProxy) blackholeBoth() {
	p.dropUp.Store(true)
	p.dropDown.Store(true)
}

func (p *testUDPProxy) rotateSourcePort() error {
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: BindIP}, p.serverAddr)
	if err != nil {
		return err
	}
	p.mu.Lock()
	old := p.upstream
	p.upstream = conn
	p.mu.Unlock()
	go p.readServer(conn)
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (p *testUDPProxy) close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		_ = p.clientConn.Close()
		p.mu.Lock()
		if p.upstream != nil {
			_ = p.upstream.Close()
		}
		p.mu.Unlock()
	})
}

func dialProxyClient(t *testing.T, p *testUDPProxy, id, sni string, factory client.ConnFactory, idle time.Duration) client.Client {
	t.Helper()
	if factory == nil {
		factory = loopFactory{}
	}
	c, _, err := client.NewClient(&client.Config{
		ConnFactory: factory,
		ServerAddr:  p.addr(),
		Auth:        token(id),
		TLSConfig:   client.TLSConfig{ServerName: sni, InsecureSkipVerify: true},
		QUICConfig:  client.QUICConfig{MaxIdleTimeout: idle},
	})
	if err != nil {
		t.Fatalf("dial through UDP proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func tcpPayloadServer(t *testing.T, data []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, bytes.NewReader(data))
			}()
		}
	}()
	return ln.Addr().String()
}

func collectLinkTotal(t *testing.T, r *rig, id string, ready func(plugin.LinkStat) bool) plugin.LinkStat {
	t.Helper()
	var total plugin.LinkStat
	eventually(t, "link counters for "+id, func() bool {
		c := r.collect()
		for _, stat := range c.Links {
			if stat.CredID == id {
				addLinkTotal(&total, stat)
			}
		}
		return ready(total)
	})
	return total
}

func addLinkTotal(dst *plugin.LinkStat, src plugin.LinkStat) {
	dst.Connects += src.Connects
	dst.Rejects += src.Rejects
	dst.Drops += src.Drops
	dst.ClientCloses += src.ClientCloses
	dst.Freezes += src.Freezes
	dst.Quiet += src.Quiet
	dst.Accepted += src.Accepted
	dst.HellosForeign += src.HellosForeign
	dst.RTTMSSum += src.RTTMSSum
	dst.RTTSamples += src.RTTSamples
	dst.PktsSent += src.PktsSent
	dst.PktsLost += src.PktsLost
}

func TestIdleTimeoutDiagnosticsFreezeAndQuiet(t *testing.T) {
	t.Run("freeze", func(t *testing.T) {
		r := newRig(t, "")
		r.e.(*eng).quicIdleTimeout = 4 * time.Second
		r.apply(cred("freeze"))
		proxy := newTestUDPProxy(t, r.udpPort)
		factory := &silentLoopFactory{}
		c := dialProxyClient(t, proxy, "freeze", serverName, factory, 4*time.Second)
		stream, err := c.TCP(tcpPayloadServer(t, payload(1<<20)))
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(stream, make([]byte, 8<<10)); err != nil {
			t.Fatalf("initial download: %v", err)
		}
		proxy.blackholeDownAfter(32 << 10)
		_ = r.collect()
		proxy.waitDownBlackhole(t)
		factory.goSilent() // keep the client from acknowledging the packets the proxy now drops
		_ = r.collect()    // take the last receive-count baseline before the idle timeout

		got := collectLinkTotal(t, r, "crd_freeze", func(s plugin.LinkStat) bool { return s.Freezes+s.Quiet > 0 })
		if got.Freezes != 1 || got.Quiet != 0 {
			t.Fatalf("idle ending = freezes %d, quiet %d; want one freeze", got.Freezes, got.Quiet)
		}
	})

	t.Run("quiet", func(t *testing.T) {
		r := newRig(t, "")
		r.e.(*eng).quicIdleTimeout = 4 * time.Second
		r.apply(cred("quiet"))
		proxy := newTestUDPProxy(t, r.udpPort)
		factory := &silentLoopFactory{}
		c := dialProxyClient(t, proxy, "quiet", serverName, factory, 4*time.Second)
		if err := echoOnce(c, r.echo, payload(128)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		_ = r.collect() // no packets are in flight after the short request
		proxy.blackholeBoth()
		factory.goSilent()

		got := collectLinkTotal(t, r, "crd_quiet", func(s plugin.LinkStat) bool { return s.Freezes+s.Quiet > 0 })
		if got.Quiet != 1 || got.Freezes != 0 {
			t.Fatalf("idle ending = quiet %d, freezes %d; want one quiet connection", got.Quiet, got.Freezes)
		}
	})
}

func TestAcceptedForeignSNIClientCloseAndKick(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("good"), cred("kicked"))
	proxy := newTestUDPProxy(t, r.udpPort)

	foreign := dialProxyClient(t, proxy, "good", "foreign.example.net", nil, 4*time.Second)
	_ = foreign.Close()
	closeClient := r.mustDial("good")
	_ = closeClient.Close()
	var good plugin.LinkStat
	var inbound plugin.LinkStat
	eventually(t, "accepted and clean-close counters", func() bool {
		for _, stat := range r.collect().Links {
			switch stat.CredID {
			case "crd_good":
				addLinkTotal(&good, stat)
			case "":
				addLinkTotal(&inbound, stat)
			}
		}
		return good.ClientCloses >= 2 && inbound.Accepted >= 2 && inbound.HellosForeign >= 1
	})
	if good.ClientCloses != 2 || inbound.Accepted != 2 || inbound.HellosForeign != 1 {
		t.Fatalf("inbound diagnostics = %+v; want two accepts and one foreign hello", inbound)
	}

	kicked := r.mustDial("kicked")
	if err := echoOnce(kicked, r.echo, payload(128)); err != nil {
		t.Fatal(err)
	}
	if n, err := r.e.Kick(context.Background(), []string{"crd_kicked"}); err != nil || n != 1 {
		t.Fatalf("Kick = %d, %v; want one credential", n, err)
	}
	if err := echoOnce(kicked, r.echo, payload(128)); err == nil {
		t.Fatal("the next request after Kick must close the connection")
	}
	var kickedStat plugin.LinkStat
	eventually(t, "kicked session to close", func() bool {
		c := r.collect()
		for _, stat := range c.Links {
			if stat.CredID == "crd_kicked" {
				addLinkTotal(&kickedStat, stat)
			} else if stat.CredID == "" {
				addLinkTotal(&inbound, stat)
			}
		}
		return sessionsOf(c, "crd_kicked") == 0
	})
	if kickedStat.ClientCloses != 0 || inbound.Accepted != 3 || inbound.HellosForeign != 1 {
		t.Fatalf("server kick was counted as a client close: %+v", kickedStat)
	}
}

// BBR (the default) replaces quic-go's controller, whose own PacketsLost would stay at zero; the patched core counts the losses
// under it (server.LostPackets), and quic-go does under Reno.
func TestConnectionStatsIncludeRTTAndLoss(t *testing.T) {
	for _, cc := range []string{"", "reno"} {
		t.Run("congestion="+cc, func(t *testing.T) { connectionStatsRTTAndLoss(t, cc) })
	}
}

func connectionStatsRTTAndLoss(t *testing.T, congestion string) {
	r := newRig(t, "")
	r.e.(*eng).testCongestion = congestion
	r.apply(cred("stats"))
	proxy := newTestUDPProxy(t, r.udpPort)
	proxy.delayNanos.Store(int64(50 * time.Millisecond))
	proxy.lossEvery.Store(20) // one in twenty server-to-client datagrams, about five percent
	c := dialProxyClient(t, proxy, "stats", serverName, nil, 10*time.Second)

	var total plugin.LinkStat
	var totalMu sync.Mutex
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				collected, err := r.e.Collect(context.Background())
				if err != nil {
					return
				}
				totalMu.Lock()
				for _, stat := range collected.Links {
					if stat.CredID == "crd_stats" {
						addLinkTotal(&total, stat)
					}
				}
				totalMu.Unlock()
			}
		}
	}()

	stream, err := c.TCP(tcpPayloadServer(t, payload(128<<10)))
	if err != nil {
		close(stop)
		<-done
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.ReadFull(stream, make([]byte, 128<<10)); err != nil {
		close(stop)
		<-done
		t.Fatalf("download through delayed lossy proxy: %v", err)
	}
	close(stop)
	<-done
	final := r.collect()
	totalMu.Lock()
	for _, stat := range final.Links {
		if stat.CredID == "crd_stats" {
			addLinkTotal(&total, stat)
		}
	}
	totalMu.Unlock()
	if total.RTTSamples == 0 {
		t.Fatal("no RTT samples were recorded")
	}
	mean := float64(total.RTTMSSum) / float64(total.RTTSamples)
	if mean < 30 || mean > 70 {
		t.Fatalf("mean smoothed RTT = %.1f ms, want about 50 ms", mean)
	}
	// 5 % are dropped; under CPU load quic-go also declares late packets lost, so the bound only catches gross miscounting
	if total.PktsSent == 0 || total.PktsLost == 0 || total.PktsLost*2 > total.PktsSent {
		t.Fatalf("server packet counters look wrong for 5%% proxy loss: sent=%d lost=%d", total.PktsSent, total.PktsLost)
	}
}

// The client counts what it lost too (the panel's health checks report it): under BBR quic-go's own counter stays at zero,
// the patched core counts each lost packet, and Close drops the counter.
func TestClientCountsItsLostPackets(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("up"))
	proxy := newTestUDPProxy(t, r.udpPort)
	proxy.delayNanos.Store(int64(20 * time.Millisecond))
	proxy.upLossEvery.Store(20) // one in twenty client-to-server datagrams
	c := dialProxyClient(t, proxy, "up", serverName, nil, 10*time.Second)

	sink, err := net.Listen("tcp", bindAddr(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	got := make(chan int64, 1)
	go func() {
		conn, err := sink.Accept()
		if err != nil {
			got <- -1
			return
		}
		defer conn.Close()
		n, _ := io.Copy(io.Discard, conn)
		got <- n
	}()
	stream, err := c.TCP(sink.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write(payload(256 << 10)); err != nil {
		t.Fatalf("upload through the lossy proxy: %v", err)
	}
	_ = stream.Close()
	select {
	case n := <-got:
		if n != 256<<10 {
			t.Fatalf("the sink got %d bytes", n)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the upload did not arrive")
	}
	st := c.(interface{ ConnectionStats() quic.ConnectionStats }).ConnectionStats()
	if st.PacketsSent == 0 || st.PacketsLost == 0 || st.PacketsLost*2 > st.PacketsSent { // see TestConnectionStatsIncludeRTTAndLoss
		t.Fatalf("client packet counters look wrong for 5%% proxy loss: sent=%d lost=%d", st.PacketsSent, st.PacketsLost)
	}
}

func TestConnectionIdentitySurvivesProxySourcePortChange(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("roam"))
	proxy := newTestUDPProxy(t, r.udpPort)
	c := dialProxyClient(t, proxy, "roam", serverName, nil, 10*time.Second)
	if err := echoOnce(c, r.echo, payload(128)); err != nil {
		t.Fatal(err)
	}
	conn := waitSessionConn(t, r, "crd_roam")
	oldPort := conn.RemoteAddr().(*net.UDPAddr).Port
	if err := proxy.rotateSourcePort(); err != nil {
		t.Fatal(err)
	}
	if err := echoOnce(c, r.echo, payload(128)); err != nil {
		t.Fatalf("request after proxy source port changed: %v", err)
	}
	newPort := proxy.upstreamAddr().Port
	eventually(t, "QUIC remote address to move", func() bool {
		return conn.RemoteAddr().(*net.UDPAddr).Port == newPort
	})
	if oldPort == newPort {
		t.Fatalf("proxy reused source port %d", oldPort)
	}
	_ = c.Close()
	eventually(t, "connection-keyed session to close", func() bool {
		return sessionsOf(r.collect(), "crd_roam") == 0
	})
	if got := r.e.(*eng).inbounds[r.spec.ID].idx.Load().byID["crd_roam"].conns.Load(); got != 0 {
		t.Fatalf("open connections after moved-address close = %d, want 0", got)
	}
}

func waitSessionConn(t *testing.T, r *rig, credID string) *quic.Conn {
	t.Helper()
	var found *quic.Conn
	eventually(t, "authenticated QUIC session", func() bool {
		in := r.e.(*eng).inbounds[r.spec.ID]
		in.smu.Lock()
		defer in.smu.Unlock()
		for conn, s := range in.sessions {
			if s.cs.id == credID {
				found = conn
				return true
			}
		}
		return false
	})
	return found
}

func TestIdleFreezeUsesLatestStableReceiveSnapshot(t *testing.T) {
	base := quic.ConnectionStats{PacketsReceived: 12, PacketsSent: 20}
	if !isFreezeAtClose(quic.ConnectionStats{PacketsReceived: 12, PacketsSent: 21}, base, 12, 20) {
		t.Fatal("new sends while the receive count stayed fixed must be a freeze")
	}
	if isFreezeAtClose(quic.ConnectionStats{PacketsReceived: 13, PacketsSent: 21}, base, 12, 20) {
		t.Fatal("a receive count not observed in a stable snapshot must not be classified as a freeze")
	}
	if isFreezeAtClose(quic.ConnectionStats{PacketsReceived: 12, PacketsSent: 20}, base, 12, 20) {
		t.Fatal("no sends while receive count stayed fixed must be quiet")
	}
}

func TestForeignSNIComparison(t *testing.T) {
	if isForeignServerName("NODE.EXAMPLE.COM.", "node.example.com") {
		t.Fatal("case and trailing-dot differences must still name the inbound")
	}
	if !isForeignServerName("foreign.example.net", "node.example.com") {
		t.Fatal("a different SNI must be foreign")
	}
	if isForeignServerName("foreign.example.net", "") {
		t.Fatal("an inbound without a server name must not count foreign hellos")
	}
}
