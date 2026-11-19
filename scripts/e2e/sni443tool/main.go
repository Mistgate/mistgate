// Command sni443tool is the probe and traffic tool of the shared-443 stand (scripts/e2e/sni443.sh). It talks only to the addresses
// it is given, and the stand runs it in a namespace that has no way out.
//
//	sni443tool fp    -addr H:P [-sni NAME] [-what tls,h2,http,plain,junk,byte1,records3,silent]
//	                                          what a stranger or a client sees, as lines "probe: observation" with nothing volatile in them
//	sni443tool flood -addr H:P -n N [-hold D]   N connections that say nothing, replaced when the server closes one; the totals at the end
//	sni443tool live  -addr H:P -sni NAME -dur D [-conc C]   TLS handshakes of "live" clients one after another, p50/p95/max in ms
//	sni443tool pin   -addr H:P -sni NAME      sha256 of the DER certificate the server shows for that name (hex)
//	sni443tool idle  -addr H:P -sni NAME -n N -hold D   N clients complete a TLS handshake and stay idle (the keepalive probe of the capture)
//	sni443tool mt    -addr H:P -domain NAME -secret HEX -n BYTES [-dc 2]   a Telegram-proxy client: Fake TLS, obfuscated2, N bytes echoed by the DC
//	sni443tool dc    -listen H:P                the fake Telegram DC: plain obfuscated2, echoes every byte back
package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mtclient "github.com/mistgate/mistgate/internal/mtproto/client"
	mp "github.com/mistgate/mistgate/internal/mtproto"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sni443tool fp|flood|live|pin|idle|mt|dc ...")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	addr := fs.String("addr", "", "host:port")
	sni := fs.String("sni", "", "server name (empty: none)")
	what := fs.String("what", "tls,h2,http,plain,junk,byte1,records3,silent", "fp: probes")
	n := fs.Int64("n", 1, "count or bytes")
	hold := fs.Duration("hold", 30*time.Second, "how long to hold connections")
	dur := fs.Duration("dur", 20*time.Second, "live: how long")
	conc := fs.Int("conc", 2, "live: clients at once")
	domain := fs.String("domain", "", "mt: Fake TLS domain")
	secret := fs.String("secret", "", "mt: proxy secret in hex (32 hex characters, or ee + 32 + the domain in hex)")
	dc := fs.Int("dc", 2, "mt: Telegram DC")
	listen := fs.String("listen", "", "dc: address")
	_ = fs.Parse(os.Args[2:])
	var err error
	switch os.Args[1] {
	case "fp":
		err = cmdFP(*addr, *sni, strings.Split(*what, ","))
	case "flood":
		err = cmdFlood(*addr, int(*n), *hold)
	case "live":
		err = cmdLive(*addr, *sni, *dur, *conc)
	case "pin":
		err = cmdPin(*addr, *sni)
	case "idle":
		err = cmdIdle(*addr, *sni, int(*n), *hold)
	case "mt":
		err = cmdMT(*addr, *domain, *secret, *n, int16(*dc))
	case "dc":
		err = cmdDC(*listen)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Println("FAIL", os.Args[1], err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- fingerprints

var addrRE = regexp.MustCompile(`\d+\.\d+\.\d+\.\d+:\d+`)

// clean strips what differs between two runs of the same behaviour (addresses, ports) from an error text.
func clean(err error) string { return addrRE.ReplaceAllString(err.Error(), "<addr>") }

func dial(addr string) (*net.TCPConn, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	tc := c.(*net.TCPConn)
	_ = tc.SetNoDelay(true)
	return tc, nil
}

// splitConn rewrites what a TLS client writes first: one byte per segment, or the ClientHello record cut in three records.
type splitConn struct {
	net.Conn
	mode  string // "byte1", "records3"
	first bool
}

func (c *splitConn) Write(p []byte) (int, error) {
	if c.first {
		return c.Conn.Write(p)
	}
	c.first = true
	switch c.mode {
	case "byte1":
		for i := range p {
			if _, err := c.Conn.Write(p[i : i+1]); err != nil {
				return i, err
			}
			time.Sleep(2 * time.Millisecond)
		}
		return len(p), nil
	case "records3":
		if len(p) > 5 && p[0] == 0x16 {
			body := p[5:]
			cut := []int{len(body) / 3, 2 * len(body) / 3, len(body)}
			var out []byte
			prev := 0
			for _, e := range cut {
				part := body[prev:e]
				out = append(out, 0x16, p[1], p[2], byte(len(part)>>8), byte(len(part)))
				out = append(out, part...)
				prev = e
			}
			if _, err := c.Conn.Write(out); err != nil {
				return 0, err
			}
			return len(p), nil
		}
	}
	return c.Conn.Write(p)
}

func tlsConfig(sni string, alpn []string) *tls.Config {
	return &tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: alpn}
}

func keyDesc(pub any) string {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return "ecdsa " + k.Curve.Params().Name
	case *rsa.PublicKey:
		return fmt.Sprintf("rsa %d", k.N.BitLen())
	case ed25519.PublicKey:
		return "ed25519"
	}
	return "?"
}

// handshake runs a TLS client handshake over conn and returns the lines that describe the server's side of it.
func handshake(conn net.Conn, sni string, alpn []string, label string) (*tls.Conn, []string) {
	tc := tls.Client(conn, tlsConfig(sni, alpn))
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tc.Handshake(); err != nil {
		return nil, []string{label + ": handshake error: " + clean(err)}
	}
	st := tc.ConnectionState()
	out := []string{fmt.Sprintf("%s: version=%s cipher=%s alpn=%q", label, tls.VersionName(st.Version), tls.CipherSuiteName(st.CipherSuite), st.NegotiatedProtocol)}
	if len(st.PeerCertificates) > 0 {
		c := st.PeerCertificates[0]
		ips := make([]string, 0, len(c.IPAddresses))
		for _, ip := range c.IPAddresses {
			ips = append(ips, ip.String())
		}
		out = append(out, fmt.Sprintf("%s: cert cn=%q sans=%v ips=%v key=%s sig=%s ca=%t chain=%d issuer-is-subject=%t days=%d",
			label, c.Subject.CommonName, c.DNSNames, ips, keyDesc(c.PublicKey), c.SignatureAlgorithm, c.IsCA, len(st.PeerCertificates),
			c.Issuer.String() == c.Subject.String(), int(c.NotAfter.Sub(c.NotBefore).Hours()/24)))
	}
	return tc, out
}

func cmdFP(addr, sni string, what []string) error {
	if addr == "" {
		return errors.New("-addr is needed")
	}
	h := sni
	if h == "" {
		h = "node"
	}
	for _, w := range what {
		switch w {
		case "tls":
			for _, alpn := range [][]string{{"h2", "http/1.1"}, {"http/1.1"}, nil} {
				conn, err := dial(addr)
				if err != nil {
					fmt.Println("tls: dial error:", clean(err))
					continue
				}
				tc, lines := handshake(conn, sni, alpn, "tls["+strings.Join(alpn, ",")+"]")
				for _, l := range lines {
					fmt.Println(l)
				}
				if tc != nil {
					_ = tc.Close()
				}
				_ = conn.Close()
			}
		case "byte1", "records3":
			conn, err := dial(addr)
			if err != nil {
				fmt.Println(w+": dial error:", clean(err))
				continue
			}
			tc, lines := handshake(&splitConn{Conn: conn, mode: w}, sni, []string{"h2", "http/1.1"}, w)
			for _, l := range lines {
				fmt.Println(l)
			}
			if tc != nil {
				_ = tc.Close()
			}
			_ = conn.Close()
		case "h2":
			fpH2(addr, sni)
		case "http":
			fpHTTP(addr, sni, h)
		case "plain":
			fpRaw(addr, "plain", []byte("GET / HTTP/1.1\r\nHost: "+h+"\r\nConnection: close\r\n\r\n"))
		case "junk":
			fpRaw(addr, "junk", []byte("\x00\x01\x02garbage that is not TLS\r\n\xff\xfe\xfd\r\n\r\n"))
		case "silent":
			fpSilent(addr)
		default:
			return fmt.Errorf("unknown probe %q", w)
		}
	}
	return nil
}

// fpH2 sends the HTTP/2 preface and an empty SETTINGS and reads what the server sends first.
func fpH2(addr, sni string) {
	conn, err := dial(addr)
	if err != nil {
		fmt.Println("h2: dial error:", clean(err))
		return
	}
	defer conn.Close()
	tc, lines := handshake(conn, sni, []string{"h2"}, "h2")
	if tc == nil {
		fmt.Println(lines[0])
		return
	}
	if tc.ConnectionState().NegotiatedProtocol != "h2" {
		fmt.Printf("h2: not negotiated (%q)\n", tc.ConnectionState().NegotiatedProtocol)
		return
	}
	_, _ = tc.Write(append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), 0, 0, 0, 4, 0, 0, 0, 0, 0))
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < 4000 {
		k, err := tc.Read(tmp)
		buf = append(buf, tmp[:k]...)
		if err != nil {
			break
		}
	}
	n := 0
	for len(buf) >= 9 && n < 6 {
		ln := int(buf[0])<<16 | int(buf[1])<<8 | int(buf[2])
		typ, flags := buf[3], buf[4]
		if len(buf) < 9+ln {
			fmt.Printf("h2: frame %d type=%d flags=%d len=%d (cut)\n", n, typ, flags, ln)
			break
		}
		pl := buf[9 : 9+ln]
		desc := ""
		switch typ {
		case 4:
			for i := 0; i+6 <= len(pl); i += 6 {
				desc += fmt.Sprintf(" %d=%d", binary.BigEndian.Uint16(pl[i:]), binary.BigEndian.Uint32(pl[i+2:]))
			}
		case 8:
			if len(pl) == 4 {
				desc = fmt.Sprintf(" inc=%d", binary.BigEndian.Uint32(pl))
			}
		case 7:
			if len(pl) >= 8 {
				desc = fmt.Sprintf(" err=%d", binary.BigEndian.Uint32(pl[4:]))
			}
		}
		fmt.Printf("h2: frame %d type=%d flags=%d len=%d%s\n", n, typ, flags, ln, desc)
		buf = buf[9+ln:]
		n++
	}
	if n == 0 {
		fmt.Println("h2: no frame")
	}
}

// fpHTTP asks GET / over the best protocol and prints the answer without what changes between two requests.
func fpHTTP(addr, sni, host string) {
	tr := &http.Transport{
		ForceAttemptHTTP2: true, DisableKeepAlives: true,
		TLSClientConfig: tlsConfig(sni, nil),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
		},
		TLSHandshakeTimeout: 8 * time.Second,
	}
	cl := &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", "https://"+host+"/", nil)
	resp, err := cl.Do(req)
	if err != nil {
		fmt.Println("http: error:", clean(err))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var hs []string
	for k, v := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Date", "Etag", "Last-Modified", "Set-Cookie":
			continue
		}
		hs = append(hs, k+"="+strings.Join(v, "|"))
	}
	sort.Strings(hs)
	fmt.Printf("http: proto=%s status=%d len=%d body=%x headers=%s\n", resp.Proto, resp.StatusCode, len(body), sha256.Sum256(body), strings.Join(hs, ";"))
}

// fpRaw sends bytes without TLS and says what comes back and how the server ends the connection.
func fpRaw(addr, label string, payload []byte) {
	conn, err := dial(addr)
	if err != nil {
		fmt.Println(label+": dial error:", clean(err))
		return
	}
	defer conn.Close()
	_, _ = conn.Write(payload)
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	var got []byte
	tmp := make([]byte, 4096)
	end := "timeout"
	for len(got) < 8192 {
		k, err := conn.Read(tmp)
		got = append(got, tmp[:k]...)
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				end = "FIN"
			case strings.Contains(err.Error(), "reset"):
				end = "RST"
			case strings.Contains(err.Error(), "timeout"):
				end = "timeout"
			default:
				end = "error"
			}
			break
		}
	}
	fmt.Printf("%s: %d bytes %x end=%s first=%q\n", label, len(got), sha256.Sum256(got), end, firstLine(got))
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	if len(b) > 60 {
		b = b[:60]
	}
	return string(b)
}

// fpSilent connects, sends nothing, and says when and how the server ends it.
func fpSilent(addr string) {
	conn, err := dial(addr)
	if err != nil {
		fmt.Println("silent: dial error:", clean(err))
		return
	}
	defer conn.Close()
	t0 := time.Now()
	_ = conn.SetReadDeadline(t0.Add(20 * time.Second))
	var tmp [64]byte
	k, err := conn.Read(tmp[:])
	end := "data"
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			end = "FIN"
		case strings.Contains(err.Error(), "reset"):
			end = "RST"
		default:
			end = "none"
		}
	}
	fmt.Printf("silent: closed after %.1f s by %s (%d bytes)\n", time.Since(t0).Seconds(), end, k)
}

// ---------------------------------------------------------------- load

// cmdFlood keeps n connections that say nothing open for hold: a connection the server closes (its own 10 s, or an eviction to make
// room) is replaced at once, so the pressure on the front does not fade.
func cmdFlood(addr string, n int, hold time.Duration) error {
	end := time.Now().Add(hold)
	var dialed, closed, failed atomic.Int64
	sem := make(chan struct{}, 200)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(end) {
				sem <- struct{}{}
				c, err := net.DialTimeout("tcp", addr, 5*time.Second)
				<-sem
				if err != nil {
					failed.Add(1)
					time.Sleep(200 * time.Millisecond)
					continue
				}
				dialed.Add(1)
				_ = c.SetReadDeadline(end)
				var b [16]byte
				for {
					if _, err := c.Read(b[:]); err != nil {
						break
					}
				}
				_ = c.Close()
				if time.Now().Before(end) {
					closed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("flood slots=%d dialed=%d closed-by-server=%d dial-failed=%d\n", n, dialed.Load(), closed.Load(), failed.Load())
	return nil
}

func cmdLive(addr, sni string, dur time.Duration, conc int) error {
	var mu sync.Mutex
	var ms []float64
	errs := 0
	end := time.Now().Add(dur)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(end) {
				t0 := time.Now()
				c, err := net.DialTimeout("tcp", addr, 5*time.Second)
				if err == nil {
					_ = c.SetDeadline(time.Now().Add(8 * time.Second))
					tc := tls.Client(c, tlsConfig(sni, []string{"h2", "http/1.1"}))
					err = tc.Handshake()
					_ = c.Close()
				}
				mu.Lock()
				if err != nil {
					errs++
				} else {
					ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
				}
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if len(ms) == 0 {
		return fmt.Errorf("no handshake completed (%d errors)", errs)
	}
	sort.Float64s(ms)
	fmt.Printf("live ok=%d err=%d p50=%.1f p95=%.1f max=%.1f ms\n", len(ms), errs, ms[len(ms)/2], ms[len(ms)*95/100], ms[len(ms)-1])
	return nil
}

func cmdPin(addr, sni string) error {
	conn, err := dial(addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	tc, lines := handshake(conn, sni, nil, "pin")
	if tc == nil {
		return errors.New(lines[0])
	}
	defer tc.Close()
	fmt.Printf("%x\n", sha256.Sum256(tc.ConnectionState().PeerCertificates[0].Raw))
	return nil
}

// cmdIdle completes n handshakes and holds the connections idle for hold: the capture of the stand sees what the server sends
// meanwhile (the ports are printed), and the totals say how many connections the server closed in that time.
func cmdIdle(addr, sni string, n int, hold time.Duration) error {
	var conns []*tls.Conn
	var closed atomic.Int64
	end := time.Now().Add(hold)
	for i := 0; i < n; i++ {
		conn, err := dial(addr)
		if err != nil {
			return err
		}
		tc, lines := handshake(conn, sni, []string{"http/1.1"}, "idle")
		if tc == nil {
			return errors.New(lines[0])
		}
		_ = conn.SetDeadline(end)
		conns = append(conns, tc)
		fmt.Printf("port %d\n", conn.LocalAddr().(*net.TCPAddr).Port)
		go func() {
			var b [16]byte
			if _, err := tc.Read(b[:]); err != nil && time.Now().Before(end) {
				closed.Add(1)
			}
		}()
	}
	time.Sleep(time.Until(end))
	fmt.Printf("idle opened=%d closed-by-server=%d\n", len(conns), closed.Load())
	for _, c := range conns {
		_ = c.Close()
	}
	return nil
}

// ---------------------------------------------------------------- Telegram proxy

func cmdMT(addr, domain, secret string, n int64, dc int16) error {
	cred, err := mp.ParseCredential("stand", secret)
	if err != nil {
		return errors.New("the secret is not a proxy secret")
	}
	hello, err := mtclient.BuildClientHello(cred.Key, domain, uint32(time.Now().Unix()))
	if err != nil {
		return err
	}
	conn, err := dial(addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
	cl, err := mtclient.StartClient(conn, cred.Key, hello)
	if err != nil {
		return err
	}
	if err := cl.SendHeader(mtclient.TagAbridged, dc); err != nil {
		return err
	}
	// the stream to send and to expect back: a fixed pseudo-random sequence
	gen := func() io.Reader { return rand.NewChaCha8([32]byte{7, 4, 43}) }
	t0 := time.Now()
	werr := make(chan error, 1)
	go func() {
		_, err := io.CopyN(cl, gen(), n)
		werr <- err
	}()
	want, got := gen(), make([]byte, 64<<10)
	exp := make([]byte, len(got))
	var done int64
	for done < n {
		k, err := cl.Read(got[:min(int64(len(got)), n-done)])
		if k > 0 {
			if _, rerr := io.ReadFull(want, exp[:k]); rerr != nil || !bytes.Equal(exp[:k], got[:k]) {
				return fmt.Errorf("the echo differs from what was sent at byte %d", done)
			}
			done += int64(k)
		}
		if err != nil {
			if done < n {
				return fmt.Errorf("the echo ended at byte %d of %d: %w", done, n, err)
			}
			break
		}
	}
	if err := <-werr; err != nil {
		return err
	}
	fmt.Printf("ok mt %d bytes echoed through the proxy in %.1f s\n", n, time.Since(t0).Seconds())
	return nil
}

func ctr(key, iv []byte) cipher.Stream {
	b, _ := aes.NewCipher(key)
	return cipher.NewCTR(b, iv)
}

// cmdDC is the Telegram DC the proxy dials: plain obfuscated2, the bytes come back as they went.
func cmdDC(listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	fmt.Println("ready", ln.Addr())
	for {
		raw, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer raw.Close()
			var h [64]byte
			if _, err := io.ReadFull(raw, h[:]); err != nil {
				return
			}
			var rev [64]byte
			for i := range h {
				rev[i] = h[63-i]
			}
			dec, enc := ctr(h[8:40], h[40:56]), ctr(rev[8:40], rev[40:56])
			var plain [64]byte
			dec.XORKeyStream(plain[:], h[:])
			buf := make([]byte, 32<<10)
			for {
				k, err := raw.Read(buf)
				if k > 0 {
					dec.XORKeyStream(buf[:k], buf[:k])
					enc.XORKeyStream(buf[:k], buf[:k])
					if _, werr := raw.Write(buf[:k]); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}
}
