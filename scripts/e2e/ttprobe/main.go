// Command ttprobe is the helper of the TrustTunnel stand (scripts/e2e/trusttunnel.sh). It runs on loopback and veth addresses of a
// throwaway lab only.
//
//	ttprobe udp   <socks host:port> <target host:port> [count]   SOCKS5 UDP ASSOCIATE: count datagrams to an echo, "udp got/count"
//	ttprobe dns   <socks host:port> <resolver host:port> <name>  one A query through the SOCKS5 UDP relay, prints the first address
//	ttprobe settings <host:port> <server name>                   TLS (ALPN h2), then the frames the server sends first: SETTINGS, WINDOW_UPDATE
//	ttprobe site  <host:port> <server name>                      what an unauthenticated visitor gets over h2, one normalized block per request
//	ttprobe flood -addr host:port -sni name -creds file [-streams n] [-seconds s] [-h3] [-target host:port]
//	                                                       one connection per user:password line, n upload streams on each, for s seconds
//	ttprobe toml  [flags] < path.json                            a Mistgate sync path (protocol trusttunnel) as the client's TOML (SOCKS listener)
//
// "site" prints the status, the headers (sorted, Date left out) and a digest of the body for each probe, so two ports of one node can
// be compared with diff. "toml" needs no network at all.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"golang.org/x/net/http2"
	"sync"
	"sync/atomic"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "udp":
		err = cmdUDP(os.Args[2:])
	case "dns":
		err = cmdDNS(os.Args[2:])
	case "settings":
		err = cmdSettings(os.Args[2:])
	case "site":
		err = cmdSite(os.Args[2:])
	case "toml":
		err = cmdTOML(os.Args[2:])
	case "flood":
		err = cmdFlood(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ttprobe:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ttprobe udp|dns|settings|site|toml ... (see the package comment)")
	os.Exit(2)
}

// ---------------------------------------------------------------- SOCKS5 UDP

type udpRelay struct {
	ctl  net.Conn
	pc   *net.UDPConn
	peer *net.UDPAddr
}

func socksAssociate(socks string) (*udpRelay, error) {
	ctl, err := net.DialTimeout("tcp", socks, 5*time.Second)
	if err != nil {
		return nil, err
	}
	_ = ctl.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := ctl.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(ctl, rep); err != nil || rep[0] != 5 || rep[1] != 0 {
		return nil, fmt.Errorf("SOCKS5 greeting answer %v: %v", rep, err)
	}
	if _, err := ctl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(ctl, head); err != nil {
		return nil, err
	}
	if head[1] != 0 {
		return nil, fmt.Errorf("UDP ASSOCIATE refused: reply code %d", head[1])
	}
	var host net.IP
	switch head[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(ctl, b); err != nil {
			return nil, err
		}
		host = net.IP(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(ctl, b); err != nil {
			return nil, err
		}
		host = net.IP(b)
	default:
		return nil, fmt.Errorf("unsupported BND.ADDR type %d", head[3])
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(ctl, pb); err != nil {
		return nil, err
	}
	_ = ctl.SetDeadline(time.Time{})
	if host.IsUnspecified() {
		h, _, _ := net.SplitHostPort(socks)
		host = net.ParseIP(h)
	}
	peer := &net.UDPAddr{IP: host, Port: int(binary.BigEndian.Uint16(pb))}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, err
	}
	return &udpRelay{ctl: ctl, pc: pc, peer: peer}, nil
}

func (r *udpRelay) close() { _ = r.pc.Close(); _ = r.ctl.Close() }

func (r *udpRelay) send(target string, payload []byte) error {
	ap, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return err
	}
	ip4 := ap.IP.To4()
	if ip4 == nil {
		return errors.New("only IPv4 targets")
	}
	buf := append([]byte{0, 0, 0, 1}, ip4...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(ap.Port))
	_, err = r.pc.WriteToUDP(append(buf, payload...), r.peer)
	return err
}

// recv returns the payload and the source address the relay reports.
func (r *udpRelay) recv(wait time.Duration) ([]byte, string, error) {
	_ = r.pc.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 70000)
	n, _, err := r.pc.ReadFromUDP(buf)
	if err != nil {
		return nil, "", err
	}
	if n < 10 || buf[2] != 0 {
		return nil, "", fmt.Errorf("short or fragmented SOCKS5 UDP datagram (%d bytes)", n)
	}
	switch buf[3] {
	case 1:
		return buf[10:n], fmt.Sprintf("%s:%d", net.IP(buf[4:8]), binary.BigEndian.Uint16(buf[8:10])), nil
	case 4:
		if n < 22 {
			return nil, "", errors.New("short IPv6 header")
		}
		return buf[22:n], fmt.Sprintf("[%s]:%d", net.IP(buf[4:20]), binary.BigEndian.Uint16(buf[20:22])), nil
	}
	return nil, "", fmt.Errorf("address type %d", buf[3])
}

func cmdUDP(args []string) error {
	if len(args) < 2 {
		usage()
	}
	count := 100
	if len(args) > 2 {
		count, _ = strconv.Atoi(args[2])
	}
	r, err := socksAssociate(args[0])
	if err != nil {
		return err
	}
	defer r.close()
	got := map[int]bool{}
	var firstFrom string
	collect := func(wait time.Duration) {
		for {
			p, from, err := r.recv(wait)
			if err != nil {
				return
			}
			if firstFrom == "" {
				firstFrom = from
			}
			if len(p) >= 4 {
				got[int(binary.BigEndian.Uint32(p))] = true
			}
			wait = 5 * time.Millisecond
		}
	}
	for i := 0; i < count; i++ {
		p := binary.BigEndian.AppendUint32(nil, uint32(i))
		p = append(p, bytes.Repeat([]byte{'x'}, 60)...)
		if err := r.send(args[1], p); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
		collect(time.Millisecond)
	}
	collect(2 * time.Second)
	fmt.Printf("udp %d/%d from=%s\n", len(got), count, firstFrom)
	return nil
}

func dnsQuery(id uint16, name string) []byte {
	b := binary.BigEndian.AppendUint16(nil, id)
	b = append(b, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0, 0, 1, 0, 1)
}

func cmdDNS(args []string) error {
	if len(args) != 3 {
		usage()
	}
	r, err := socksAssociate(args[0])
	if err != nil {
		return err
	}
	defer r.close()
	if err := r.send(args[1], dnsQuery(0x4d47, args[2])); err != nil {
		return err
	}
	p, from, err := r.recv(5 * time.Second)
	if err != nil {
		return fmt.Errorf("no DNS answer: %w", err)
	}
	if len(p) < 12 || binary.BigEndian.Uint16(p) != 0x4d47 || binary.BigEndian.Uint16(p[6:]) == 0 {
		return fmt.Errorf("DNS answer without a record: %x", p)
	}
	// the answer's last 4 bytes are the A record of a one-answer reply
	fmt.Printf("dns %s from=%s\n", net.IP(p[len(p)-4:]), from)
	return nil
}

// ---------------------------------------------------------------- h2 probes

func dialH2(addr, sni string) (*tls.Conn, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: sni, NextProtos: []string{"h2", "http/1.1"}}) //nolint:gosec // a lab node's own certificate
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := c.Handshake(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func cmdSettings(args []string) error {
	if len(args) != 2 {
		usage()
	}
	c, err := dialH2(args[0], args[1])
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Printf("alpn=%q version=%x\n", c.ConnectionState().NegotiatedProtocol, c.ConnectionState().Version)
	if _, err := io.WriteString(c, http2.ClientPreface); err != nil {
		return err
	}
	fr := http2.NewFramer(c, c)
	if err := fr.WriteSettings(); err != nil {
		return err
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(deadline)
		f, err := fr.ReadFrame()
		if err != nil {
			break
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if f.IsAck() {
				fmt.Println("SETTINGS ack")
				continue
			}
			var parts []string
			_ = f.ForeachSetting(func(s http2.Setting) error { parts = append(parts, fmt.Sprintf("%s=%d", s.ID, s.Val)); return nil })
			sort.Strings(parts)
			fmt.Println("SETTINGS", strings.Join(parts, " "))
			_ = fr.WriteSettingsAck()
		case *http2.WindowUpdateFrame:
			fmt.Printf("WINDOW_UPDATE stream=%d inc=%d\n", f.StreamID, f.Increment)
		default:
			fmt.Printf("frame %s stream=%d\n", f.Header().Type, f.Header().StreamID)
		}
	}
	return nil
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func cmdSite(args []string) error {
	if len(args) != 2 {
		usage()
	}
	addr, sni := args[0], args[1]
	tr := &http2.Transport{DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) { return dialH2(addr, sni) }}
	defer tr.CloseIdleConnections()
	type probe struct {
		name, method, target, auth string
	}
	probes := []probe{
		{"GET /", "GET", "/", ""},
		{"GET /index.html", "GET", "/index.html", ""},
		{"GET /nope", "GET", "/nope", ""},
		{"HEAD /", "HEAD", "/", ""},
		{"POST /", "POST", "/", ""},
		{"OPTIONS *", "OPTIONS", "*", ""},
		{"CONNECT no credentials", "CONNECT", "example.org:443", ""},
		{"CONNECT wrong password", "CONNECT", "example.org:443", basic("nobody", "wrong")},
		{"CONNECT broken base64", "CONNECT", "example.org:443", "Basic !!!"},
		{"CONNECT bearer", "CONNECT", "example.org:443", "Bearer abc"},
		{"CONNECT _check no credentials", "CONNECT", "_check:443", ""},
		{"CONNECT _udp2 no credentials", "CONNECT", "_udp2:0", ""},
	}
	for _, p := range probes {
		req := &http.Request{Method: p.method, Header: http.Header{}, Host: sni}
		if p.method == "CONNECT" {
			req.URL = &url.URL{Scheme: "https", Host: p.target}
			req.Host = p.target
			req.Body = http.NoBody
		} else {
			req.URL = &url.URL{Scheme: "https", Host: sni, Path: p.target}
		}
		if p.auth != "" {
			req.Header.Set("Proxy-Authorization", p.auth)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := tr.RoundTrip(req.WithContext(ctx))
		fmt.Printf("== %s\n", p.name)
		if err != nil {
			fmt.Printf("error: %v\n", errClass(err))
			cancel()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		cancel()
		sum := sha256.Sum256(body)
		fmt.Printf("%s %d\n", resp.Proto, resp.StatusCode)
		var keys []string
		for k := range resp.Header {
			if !strings.EqualFold(k, "Date") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("%s: %s\n", k, strings.Join(resp.Header[k], ", "))
		}
		fmt.Printf("body %d bytes sha256 %s\n", len(body), hex.EncodeToString(sum[:8]))
	}
	return nil
}

func errClass(err error) string {
	s := err.Error()
	for _, k := range []string{"stream error", "connection error", "EOF", "timeout", "deadline"} {
		if strings.Contains(s, k) {
			return k
		}
	}
	return s
}

// ---------------------------------------------------------------- sync path -> client TOML

// syncPath is the part of a Mistgate sync path (docs of the client API) that the TOML needs.
type syncPath struct {
	Protocol string `json:"protocol"`
	IPv6Exit bool   `json:"ipv6_egress"`
	Settings struct {
		Server    string `json:"server"`
		Port      int    `json:"port"`
		SNI       string `json:"sni"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		Transport string `json:"transport"`
		TLS       string `json:"tls"`
		PinSHA256 string `json:"pin_sha256"`
	} `json:"settings"`
}

func cmdTOML(args []string) error {
	fs := flag.NewFlagSet("toml", flag.ContinueOnError)
	host := fs.String("host", "", "the address to dial instead of settings.server (the lab reaches the node over a veth)")
	port := fs.Int("port", 0, "the port to dial instead of settings.port")
	socks := fs.String("socks", "127.0.0.1:1080", "the SOCKS5 listener")
	dns := fs.String("dns", "", "a DNS upstream of the lab (host:port)")
	proto := fs.String("protocol", "", "http2 or http3; default: http2 for transport h2, http3 for h3, http2 for auto")
	cert := fs.String("cert", "", "PEM file of the certificate the client trusts (a CA; a self-signed leaf without keyCertSign is no anchor for it)")
	skip := fs.Bool("skip-verify", false, "skip_verification = true: the lab node's certificate is self-signed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var p syncPath
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		return err
	}
	if p.Protocol != "trusttunnel" {
		return fmt.Errorf("the path is %q, not trusttunnel", p.Protocol)
	}
	s := p.Settings
	if *host == "" {
		*host = s.Server
	}
	if *port == 0 {
		*port = s.Port
	}
	up := *proto
	if up == "" {
		up = "http2"
		if s.Transport == "h3" {
			up = "http3"
		}
	}
	// a quoted TOML string: JSON string syntax is a subset of it for these values
	q := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	var b strings.Builder
	fmt.Fprintf(&b, "loglevel = \"info\"\nvpn_mode = \"general\"\nkillswitch_enabled = false\npost_quantum_group_enabled = true\nexclusions = []\n\n")
	fmt.Fprintf(&b, "[endpoint]\nhostname = %s\naddresses = [%s]\ncustom_sni = \"\"\nhas_ipv6 = %v\nusername = %s\npassword = %s\n",
		q(s.SNI), q(net.JoinHostPort(*host, strconv.Itoa(*port))), p.IPv6Exit, q(s.Username), q(s.Password))
	fmt.Fprintf(&b, "client_random = \"\"\nskip_verification = %v\n", *skip)
	if *cert != "" {
		pem, err := os.ReadFile(*cert)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "certificate = %s\n", q(string(pem)))
	} else {
		b.WriteString("certificate = \"\"\n")
	}
	fmt.Fprintf(&b, "upstream_protocol = %s\nanti_dpi = false\n", q(up))
	if *dns != "" {
		fmt.Fprintf(&b, "dns_upstreams = [%s]\n", q(*dns))
	} else {
		b.WriteString("dns_upstreams = []\n")
	}
	fmt.Fprintf(&b, "\n[listener.socks]\naddress = %s\nusername = \"\"\npassword = \"\"\n", q(*socks))
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	_, err := w.WriteString(b.String())
	return err
}

// ---------------------------------------------------------------- flood: memory of the node under many people and streams

// crlf is the line end of an HTTP/1.1 head.
const crlf = "\r\n"

func cmdFlood(args []string) error {
	fs := flag.NewFlagSet("flood", flag.ContinueOnError)
	addr := fs.String("addr", "", "the node, host:port")
	sni := fs.String("sni", "", "server name")
	credsFile := fs.String("creds", "", "file of user:password lines")
	streams := fs.Int("streams", 20, "upload streams per user")
	seconds := fs.Int("seconds", 30, "how long the uploads run")
	h3 := fs.Bool("h3", false, "HTTP/3 instead of HTTP/2")
	target := fs.String("target", "203.0.113.77:8080", "the tunnel's destination: an HTTP server that reads (POST) ")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*credsFile)
	if err != nil {
		return err
	}
	var creds []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			creds = append(creds, l)
		}
	}
	var opened, failed, written atomic.Int64
	var wg sync.WaitGroup
	stop := time.Now().Add(time.Duration(*seconds) * time.Second)
	head := []byte("POST /up HTTP/1.1" + crlf + "Host: x" + crlf + "Content-Length: 4000000000" + crlf + crlf)
	chunk := bytes.Repeat([]byte{'x'}, 32<<10)
	for _, c := range creds {
		user, pass, _ := strings.Cut(c, ":")
		var rt func(*http.Request) (*http.Response, error)
		if *h3 {
			tr := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: *sni}, //nolint:gosec // lab node
				Dial: func(ctx context.Context, _ string, tc *tls.Config, qc *quic.Config) (*quic.Conn, error) {
					return quic.DialAddr(ctx, *addr, tc, qc)
				}}
			defer tr.Close()
			rt = tr.RoundTrip
		} else {
			tr := &http2.Transport{DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) { return dialH2(*addr, *sni) }}
			defer tr.CloseIdleConnections()
			rt = tr.RoundTrip
		}
		for i := 0; i < *streams; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				pr, pw := io.Pipe()
				req := &http.Request{Method: "CONNECT", URL: &url.URL{Scheme: "https", Host: *target}, Host: *target, Header: http.Header{}, Body: pr, ContentLength: -1}
				req.Header.Set("Proxy-Authorization", basic(user, pass))
				go func() {
					_, _ = pw.Write(head)
					for time.Now().Before(stop) {
						n, err := pw.Write(chunk)
						written.Add(int64(n))
						if err != nil {
							return
						}
					}
					_ = pw.Close()
				}()
				resp, err := rt(req)
				if err != nil || resp.StatusCode != 200 {
					failed.Add(1)
					_ = pw.CloseWithError(errors.New("refused"))
					return
				}
				opened.Add(1)
				time.Sleep(time.Until(stop) + time.Second)
				_ = resp.Body.Close()
			}()
		}
	}
	wg.Wait()
	fmt.Printf("flood users=%d streams-open=%d refused=%d written=%d\n", len(creds), opened.Load(), failed.Load(), written.Load())
	return nil
}
