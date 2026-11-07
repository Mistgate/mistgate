//go:build bench && linux

package bench

// The processes of the suite. The test binary runs itself again, inside the namespaces, with MG_BENCH_ROLE set:
//
//	dest    the "internet": a plain TCP server (download and ping-pong modes) and a TLS 1.3 site that VLESS REALITY borrows
//	server  one engine of the node, the process whose CPU time and memory are measured
//	client  the load: a tunnel client of the engine and the workloads (throughput, latency under load, handshakes)
//
// They talk through files (MG_BENCH_OUT) and stdin (closing it ends a helper), so nothing but the suite's own output reaches
// the report.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	xtlsreality "github.com/xtls/reality"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	xraycore "github.com/xtls/xray-core/core"
	xraysocks "github.com/xtls/xray-core/proxy/socks"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	tcpcfg "github.com/xtls/xray-core/transport/internet/tcp"
	"golang.org/x/net/proxy"

	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hysteria2"
	"github.com/mistgate/mistgate/internal/node/vless"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	roleEnv = "MG_BENCH_ROLE"
	argsEnv = "MG_BENCH_ARGS"
	outEnv  = "MG_BENCH_OUT"

	destPort   = 9000 // plain TCP: 'D' download, 'P' ping-pong
	targetPort = 8443 // the TLS 1.3 site REALITY borrows
	pingSize   = 64

	// A connection to the plain server starts with a preamble of preambleSize bytes whose first byte is the mode. It is not a letter
	// and not 16 bytes of one kind: Hysteria2 looks at the first bytes of a stream (HTTP, TLS, BitTorrent) and a lone "D" is the start
	// of an HTTP method it waits to see the rest of.
	preambleSize = 16
	modeDownload = 1
	modePing     = 2

	benchSNI = "bench.example.com"
	// The REALITY key pair of the vless package's own tests (a throwaway pair, public in the repository).
	realityPrivate = "aGSYystUbf59_9_6LKRxD27rmSW_-2_nyd9YG_Gwbks"
	realityPublic  = "E59WjnvZcQMu7tR7_BgyhycuEdBS-CtKxfImRCdAvFM"
	benchUUID      = "5783a3e7-e373-51cd-8642-c83782b807c5"
)

// roleArgs is what the suite tells a helper process.
type roleArgs struct {
	Engine   string
	Dest     string // ip:port of the plain server, as the engine's egress and the tunnel clients dial it
	Target   string // ip:port of the TLS site (REALITY)
	Server   string // ip of the node, as the client dials it
	Port     uint16
	Pin      string // hysteria2: the certificate pin the engine reported
	Seconds  int
	Phase    string // client: "load" or "handshakes"
	Spec     *plugin.InboundSpec
	Creds    []plugin.UserCred
	ListenIP string // dest: the address to listen on
}

// readyInfo is what a server or dest helper writes when it listens.
type readyInfo struct {
	Pin   string `json:"pin,omitempty"`
	Error string `json:"error,omitempty"`
}

// clientResult is what the client helper writes when it is done.
type clientResult struct {
	Tput1Mbit      float64 `json:"tput_1"`
	Tput8Mbit      float64 `json:"tput_8"`
	Bytes          int64   `json:"bytes"`
	P50MS          float64 `json:"p50_ms"`
	P99MS          float64 `json:"p99_ms"`
	HandshakesPerS float64 `json:"hs_per_s"`
	Error          string  `json:"error,omitempty"`
}

// TestBenchRole is the entry point of the helper processes; a normal run skips it.
func TestBenchRole(t *testing.T) {
	role := os.Getenv(roleEnv)
	if role == "" {
		t.Skip("entry point of the suite's helper processes")
	}
	var a roleArgs
	if err := json.Unmarshal([]byte(os.Getenv(argsEnv)), &a); err != nil {
		t.Fatal(err)
	}
	out := os.Getenv(outEnv)
	writeJSON := func(v any) {
		b, _ := json.Marshal(v)
		tmp := out + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err == nil {
			_ = os.Rename(tmp, out)
		}
	}
	switch role {
	case "dest":
		runDest(a, writeJSON)
	case "server":
		runServer(a, writeJSON)
	case "client":
		var res clientResult
		if err := runClient(a, &res); err != nil {
			res.Error = err.Error()
		}
		writeJSON(res)
	default:
		t.Fatalf("unknown role %q", role)
	}
}

func preamble(mode byte) []byte {
	p := make([]byte, preambleSize)
	p[0], p[1], p[2] = mode, 0x02, 0x03
	return p
}

func holdUntilStdinCloses() { _, _ = io.Copy(io.Discard, os.Stdin) }

// ---- dest ----

func runDest(a roleArgs, ready func(any)) {
	ln, err := net.Listen("tcp", net.JoinHostPort(a.ListenIP, strconv.Itoa(destPort)))
	if err != nil {
		ready(readyInfo{Error: err.Error()})
		return
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveDest(c)
		}
	}()
	// the site REALITY borrows: TLS 1.3 only, X25519, an ordinary certificate
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: benchSNI}, DNSNames: []string{benchSNI},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	site, err := tls.Listen("tcp", net.JoinHostPort(a.ListenIP, strconv.Itoa(targetPort)), &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519},
	})
	if err != nil {
		ready(readyInfo{Error: err.Error()})
		return
	}
	defer site.Close()
	go func() {
		for {
			c, err := site.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _ = c.(*tls.Conn).Handshake() }()
		}
	}()
	ready(readyInfo{})
	holdUntilStdinCloses()
}

func serveDest(c net.Conn) {
	defer c.Close()
	mode := make([]byte, preambleSize)
	if _, err := io.ReadFull(c, mode); err != nil {
		return
	}
	switch mode[0] {
	case modeDownload: // download: zeros until the client goes away
		buf := make([]byte, 64<<10)
		for {
			if _, err := c.Write(buf); err != nil {
				return
			}
		}
	case modePing: // ping-pong: pingSize bytes back for pingSize bytes
		buf := make([]byte, pingSize)
		for {
			if _, err := io.ReadFull(c, buf); err != nil {
				return
			}
			if _, err := c.Write(buf); err != nil {
				return
			}
		}
	}
}

// ---- server ----

func benchEnv() engine.Env {
	direct := egress.New(nil, egress.AllowPrivate())
	return engine.Env{
		Certs:  certs.New(os.TempDir() + "/mg-bench-certs-" + strconv.Itoa(os.Getpid())),
		Egress: func(string) (engine.Egress, error) { return direct, nil },
	}
}

func hy2Token() string { return "token-bench-0123456789abcdef" }

func runServer(a roleArgs, ready func(any)) {
	fail := func(err error) { ready(readyInfo{Error: err.Error()}) }
	ctx := context.Background()
	var e engine.Engine
	var err error
	var spec plugin.InboundSpec
	var creds []plugin.UserCred
	switch a.Engine {
	case "hysteria2":
		e, err = hysteria2.New(benchEnv())
		sum := sha256.Sum256([]byte(hy2Token()))
		creds = []plugin.UserCred{{CredID: "crd_bench", UserID: "usr_bench", DeviceID: "dev_bench",
			Data: json.RawMessage(fmt.Sprintf(`{"auth_sha256":%q}`, hex.EncodeToString(sum[:])))}}
		spec = plugin.InboundSpec{ID: "inb_bench", Protocol: hysteria2.Protocol, ProfileID: "prf_bench", Version: 1, Enabled: true,
			Listen: plugin.Listen{Network: "udp", Port: a.Port}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "node.example.com"},
			Egress: "direct", Settings: json.RawMessage(`{"masquerade":{"tcp_port":8444}}`)}
	case "vless-tcp", "vless-xhttp":
		transport := "tcp"
		if a.Engine == "vless-xhttp" {
			transport = "xhttp"
		}
		for alpn := range 3 { // REALITY measures its target once per process; this one would make the first client wait 5 s
			xtlsreality.GlobalPostHandshakeRecordsLens.Store(a.Target+" "+benchSNI+" "+strconv.Itoa(alpn), []int{})
		}
		e, err = vless.New(benchEnv())
		creds = []plugin.UserCred{{CredID: "crd_bench", UserID: "usr_bench", Data: json.RawMessage(fmt.Sprintf(`{"id":%q}`, benchUUID))}}
		spec = plugin.InboundSpec{ID: "inb_bench", Protocol: vless.Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: a.Port},
			Settings: json.RawMessage(fmt.Sprintf(`{"transport":%q,"security":"reality","flow":"","reality":{"target":%q,"server_names":[%q],"short_ids":["6ba85179"],"private_key":%q},"xhttp":{"path":"/q8x2kd7w","host":"","mode":"auto","x_padding_bytes":"100-200"}}`,
				transport, a.Target, benchSNI, realityPrivate))}
	case "awg":
		e, err = awg.NewWithOptions(benchEnv(), awg.Options{Backend: "userspace"})
		if a.Spec != nil {
			spec, creds = *a.Spec, a.Creds
		}
	default:
		err = fmt.Errorf("unknown engine %q", a.Engine)
	}
	if err != nil {
		fail(err)
		return
	}
	defer e.Close(ctx)
	rep, err := e.Apply(ctx, spec, creds)
	if err != nil {
		fail(err)
		return
	}
	ready(readyInfo{Pin: rep.Cert.PinSHA256})
	holdUntilStdinCloses()
}

// ---- client ----

// dialer opens a TCP connection to the plain server through the engine under test.
type dialer struct {
	dial     func() (net.Conn, error)
	session  func() (func(), error) // a new tunnel session, for the handshake count; nil = not measured by the client
	teardown func()
}

func newDialer(a roleArgs) (*dialer, error) {
	switch a.Engine {
	case "hysteria2":
		newClient := func() (client.Client, error) {
			c, _, err := client.NewClient(&client.Config{
				ServerAddr: &net.UDPAddr{IP: net.ParseIP(a.Server), Port: int(a.Port)},
				Auth:       hy2Token(),
				TLSConfig: client.TLSConfig{ServerName: "node.example.com", InsecureSkipVerify: true,
					VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
						sum := sha256.Sum256(raw[0])
						if got := hex.EncodeToString(sum[:]); got != a.Pin {
							return fmt.Errorf("certificate pin %s does not match %s", got, a.Pin)
						}
						return nil
					}},
			})
			return c, err
		}
		c, err := newClient()
		if err != nil {
			return nil, err
		}
		return &dialer{
			dial:     func() (net.Conn, error) { return c.TCP(a.Dest) },
			session:  func() (func(), error) { c2, err := newClient(); return func() { c2.Close() }, err },
			teardown: func() { c.Close() },
		}, nil
	case "vless-tcp", "vless-xhttp":
		socks, closeX, err := startXray(a)
		if err != nil {
			return nil, err
		}
		d, err := proxy.SOCKS5("tcp", socks, nil, proxy.Direct)
		if err != nil {
			closeX()
			return nil, err
		}
		dial := func() (net.Conn, error) { return d.Dial("tcp", a.Dest) }
		return &dialer{dial: dial, teardown: closeX, session: func() (func(), error) {
			// without mux every connection is a new REALITY handshake and a new VLESS session; a ping proves it works
			c, err := dial()
			if err != nil {
				return nil, err
			}
			if err := pingOnce(c); err != nil {
				c.Close()
				return nil, err
			}
			return func() { c.Close() }, nil
		}}, nil
	case "awg":
		return &dialer{dial: func() (net.Conn, error) { return net.DialTimeout("tcp", a.Dest, 5*time.Second) }, teardown: func() {}}, nil
	}
	return nil, fmt.Errorf("unknown engine %q", a.Engine)
}

func pingOnce(c net.Conn) error {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(append(preamble(modePing), make([]byte, pingSize)...)); err != nil {
		return err
	}
	_, err := io.ReadFull(c, make([]byte, pingSize))
	return err
}

func startXray(a roleArgs) (socks string, closeFn func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	socksPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	transportName := "tcp"
	transportSettings := []*internet.TransportConfig{{ProtocolName: "tcp", Settings: serial.ToTypedMessage(&tcpcfg.Config{})}}
	flow := "xtls-rprx-vision"
	if a.Engine == "vless-xhttp" {
		transportName, flow = "splithttp", ""
		transportSettings = []*internet.TransportConfig{{ProtocolName: "splithttp", Settings: serial.ToTypedMessage(&splithttp.Config{
			Path: "/q8x2kd7w", Mode: "auto", XPaddingBytes: &splithttp.RangeConfig{From: 100, To: 200}})}}
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(realityPublic)
	if err != nil {
		return "", nil, err
	}
	shortID, _ := hex.DecodeString("6ba85179")
	shortID = append(shortID, make([]byte, 8-len(shortID))...)
	stream := &internet.StreamConfig{
		ProtocolName: transportName, TransportSettings: transportSettings,
		SecurityType: serial.GetMessageType(&reality.Config{}),
		SecuritySettings: []*serial.TypedMessage{serial.ToTypedMessage(&reality.Config{
			Fingerprint: "chrome", ServerName: benchSNI, PublicKey: publicKey, ShortId: shortID, SpiderX: "/"})},
	}
	config := &xraycore.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}), serial.ToTypedMessage(&proxyman.InboundConfig{}), serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(&policy.Config{Level: map[uint32]*policy.Policy{0: {Buffer: &policy.Policy_Buffer{Connection: 64 << 10}}}}),
		},
		Inbound: []*xraycore.InboundHandlerConfig{{
			Tag: "socks-in",
			ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
				PortList:       &xnet.PortList{Range: []*xnet.PortRange{xnet.SinglePortRange(xnet.Port(socksPort))}},
				Listen:         xnet.NewIPOrDomain(xnet.IPAddress(net.ParseIP("127.0.0.1"))),
				StreamSettings: &internet.StreamConfig{ProtocolName: "tcp"},
			}),
			ProxySettings: serial.ToTypedMessage(&xraysocks.ServerConfig{AuthType: xraysocks.AuthType_NO_AUTH}),
		}},
		Outbound: []*xraycore.OutboundHandlerConfig{{
			Tag: "vless-out",
			ProxySettings: serial.ToTypedMessage(&vlessout.Config{Vnext: &protocol.ServerEndpoint{
				Address: xnet.NewIPOrDomain(xnet.IPAddress(net.ParseIP(a.Server))), Port: uint32(a.Port),
				User: &protocol.User{Email: "crd_bench", Account: serial.ToTypedMessage(&xrayvless.Account{Id: benchUUID, Flow: flow})},
			}}),
			SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{StreamSettings: stream}),
		}},
	}
	instance, err := xraycore.New(config)
	if err != nil {
		return "", nil, err
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		return "", nil, err
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort)), func() { _ = instance.Close() }, nil
}

func runClient(a roleArgs, res *clientResult) error {
	d, err := newDialer(a)
	if err != nil {
		return err
	}
	defer d.teardown()
	secs := time.Duration(max(a.Seconds, 1)) * time.Second
	if a.Phase == "handshakes" {
		if d.session == nil {
			return fmt.Errorf("%s does not count handshakes in the client", a.Engine)
		}
		var n atomic.Int64
		deadline := time.Now().Add(secs)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					closeSession, err := d.session()
					if err != nil {
						time.Sleep(10 * time.Millisecond)
						continue
					}
					n.Add(1)
					closeSession()
				}
			}()
		}
		start := time.Now()
		wg.Wait()
		res.HandshakesPerS = float64(n.Load()) / time.Since(start).Seconds()
		return nil
	}
	// a short warm-up: the first connection pays for the handshake of the tunnel
	if c, err := d.dial(); err == nil {
		_ = pingOnce(c)
		c.Close()
	} else {
		return fmt.Errorf("the tunnel does not carry traffic: %w", err)
	}
	var b1, b8 int64
	var lat []float64
	res.Tput1Mbit, b1, _ = download(d, 1, secs, nil)
	res.Tput8Mbit, b8, lat = download(d, 8, secs, d)
	res.Bytes = b1 + b8 // what the engine's CPU time is divided by
	if b1 == 0 || b8 == 0 {
		return fmt.Errorf("a download phase moved nothing (1 stream: %d bytes, 8 streams: %d bytes)", b1, b8)
	}
	if len(lat) > 0 {
		sort.Float64s(lat)
		res.P50MS = lat[len(lat)/2]
		res.P99MS = lat[min(len(lat)-1, len(lat)*99/100)]
	}
	return nil
}

// download runs n streams for d and returns the Mbit/s they reached together and the bytes. With latencyOn it also sends a
// small request every 20 ms over its own connection and returns the round trips in milliseconds.
func download(d *dialer, n int, dur time.Duration, latencyOn *dialer) (mbit float64, bytes int64, lat []float64) {
	var got atomic.Int64
	deadline := time.Now().Add(dur)
	var wg sync.WaitGroup
	start := time.Now()
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := d.dial()
			if err != nil {
				return
			}
			defer c.Close()
			c.SetDeadline(deadline.Add(2 * time.Second))
			if _, err := c.Write(preamble(modeDownload)); err != nil {
				return
			}
			buf := make([]byte, 64<<10)
			for time.Now().Before(deadline) {
				m, err := c.Read(buf)
				got.Add(int64(m))
				if err != nil {
					return
				}
			}
		}()
	}
	if latencyOn != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := latencyOn.dial()
			if err != nil {
				return
			}
			defer c.Close()
			c.SetDeadline(deadline.Add(2 * time.Second))
			if _, err := c.Write(preamble(modePing)); err != nil {
				return
			}
			msg, back := make([]byte, pingSize), make([]byte, pingSize)
			time.Sleep(300 * time.Millisecond) // let the streams fill the link first
			for time.Now().Before(deadline) {
				t0 := time.Now()
				if _, err := c.Write(msg); err != nil {
					return
				}
				if _, err := io.ReadFull(c, back); err != nil {
					return
				}
				lat = append(lat, float64(time.Since(t0).Microseconds())/1000)
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	return float64(got.Load()) * 8 / elapsed / 1e6, got.Load(), lat
}
