// Command bridgetool is the traffic generator and the "internet" server of the bridge lab (scripts/e2e/bridge.sh).
//
//	bridgetool serve -listen ADDR:PORT [-listen ...]       the far side: a tiny line protocol, see below
//	bridgetool get   -socks H:P -target H:P -n BYTES -seed S   download BYTES of the stream S through a SOCKS5 proxy, check sha256
//	bridgetool put   -socks H:P -target H:P -n BYTES -seed S   upload them, the far side answers with the sha256 it saw
//	bridgetool echo  -socks H:P -target H:P -secs N -every D   one TCP session of N seconds, a line every D, each must come back
//	bridgetool hold  -socks H:P -target H:P -n N -secs S -every D   N such sessions at once (the entry counts N people)
//	bridgetool idle  -socks H:P -target H:P -secs N            a session that says nothing for N seconds, then one line
//	bridgetool par   -socks H:P -target H:P -n WORKERS -bytes B -rounds R   WORKERS parallel get+put of B bytes, R times each
//	bridgetool who   -socks H:P -target H:P                    the address the far side saw
//	bridgetool impostor -listen ADDR:PORT -log FILE            a TLS site with a certificate of its own that logs what it sees
//	bridgetool probesite -listen ADDR:PORT -cert FILE          the sites the panel's own probes ask through a tunnel (generate_204,
//	                                                           trace, a 64 KiB download), under their real names; the certificate is written to FILE
//
// The far side speaks one request line per connection: "GET n seed" (n bytes of the stream, then it closes), "PUT n seed" (then
// n bytes from the client; it answers "OK n sha256\n"), "ECHO" (echoes what it reads), "WHO" (answers the peer's address). The
// stream of a seed is ChaCha8 output, so both ends hash the same bytes without a file. Every client command prints one line
// ("ok ..." or "FAIL ...") and exits 0 or 1. Nothing here talks to any address it was not given.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// nl is a line feed (kept out of string literals of this file)
const nl = string(rune(10))

func stream(seed uint64) io.Reader {
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:], seed)
	return mrand.NewChaCha8(key)
}

func streamSum(seed uint64, n int64) string {
	h := sha256.New()
	_, _ = io.CopyN(h, stream(seed), n)
	return hex.EncodeToString(h.Sum(nil))
}

type listens []string

func (l *listens) String() string     { return strings.Join(*l, ",") }
func (l *listens) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bridgetool serve|get|put|echo|idle|par|who ...")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var ls listens
	fs.Var(&ls, "listen", "serve: address to listen on (repeatable)")
	socks := fs.String("socks", "", "SOCKS5 proxy host:port")
	target := fs.String("target", "", "host:port of the far side")
	n := fs.Int64("n", 1<<20, "bytes (get, put) or workers (par)")
	bytesPer := fs.Int64("bytes", 1<<20, "par: bytes of every get and put")
	rounds := fs.Int("rounds", 1, "par: rounds per worker")
	logPath := fs.String("log", "", "impostor: file for what the site saw")
	certOut := fs.String("cert", "", "probesite: file to write the certificate to (PEM)")
	seed := fs.Uint64("seed", 1, "stream seed")
	secs := fs.Int("secs", 30, "echo, idle: seconds")
	every := fs.Duration("every", 5*time.Second, "echo: one line every")
	_ = fs.Parse(args)
	var err error
	switch cmd {
	case "serve":
		err = serve(ls)
	case "get":
		err = doGet(*socks, *target, *n, *seed)
	case "put":
		err = doPut(*socks, *target, *n, *seed)
	case "echo":
		err = doEcho(*socks, *target, *secs, *every)
	case "hold":
		err = doHold(*socks, *target, int(*n), *secs, *every)
	case "idle":
		err = doIdle(*socks, *target, *secs)
	case "par":
		err = doPar(*socks, *target, int(*n), *bytesPer, *rounds, *seed)
	case "who":
		err = doWho(*socks, *target)
	case "impostor":
		err = impostor(ls, *logPath)
	case "probesite":
		err = probeSite(ls, *certOut)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Println("FAIL", cmd, err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- the far side

func serve(addrs []string) error {
	if len(addrs) == 0 {
		return errors.New("serve needs -listen")
	}
	for _, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err != nil {
			return err
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go handle(c)
			}
		}()
	}
	fmt.Println("ready", strings.Join(addrs, ","))
	select {}
}

func handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 64<<10)
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	num := func(i int) int64 {
		if i >= len(f) {
			return 0
		}
		v, _ := strconv.ParseInt(f[i], 10, 64)
		return v
	}
	switch f[0] {
	case "GET":
		_, _ = io.CopyN(c, stream(uint64(num(2))), num(1))
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
			_, _ = io.Copy(io.Discard, br)
		}
	case "PUT":
		h := sha256.New()
		got, _ := io.CopyN(h, br, num(1))
		fmt.Fprintf(c, "OK %d %s\n", got, hex.EncodeToString(h.Sum(nil)))
	case "ECHO":
		_, _ = io.Copy(c, br)
	case "WHO":
		fmt.Fprintf(c, "%s\n", c.RemoteAddr().(*net.TCPAddr).IP)
	}
}

// ---------------------------------------------------------------- the client side

func dial(socks, target string) (net.Conn, error) {
	host, portS, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portS)
	c, err := net.DialTimeout("tcp", socks, 10*time.Second)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (net.Conn, error) { _ = c.Close(); return nil, e }
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	var rep [2]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil || rep[0] != 5 || rep[1] != 0 {
		return fail(fmt.Errorf("socks greeting: %v %v", rep, err))
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 1)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		return fail(err)
	}
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return fail(fmt.Errorf("socks connect: %w", err))
	}
	if h[1] != 0 {
		return fail(fmt.Errorf("socks connect refused (code %d)", h[1]))
	}
	skip := 4 + 2
	switch h[3] {
	case 1:
		skip = 4 + 2
	case 4:
		skip = 16 + 2
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return fail(err)
		}
		skip = int(l[0]) + 2
	}
	if _, err := io.CopyN(io.Discard, c, int64(skip)); err != nil {
		return fail(err)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func mbit(n int64, d time.Duration) float64 { return float64(n) * 8 / 1e6 / d.Seconds() }

func doGet(socks, target string, n int64, seed uint64) error {
	d, err := get(socks, target, n, seed)
	if err != nil {
		return err
	}
	fmt.Printf("ok get bytes=%d sec=%.1f mbit=%.0f sha256=match\n", n, d.Seconds(), mbit(n, d))
	return nil
}

func get(socks, target string, n int64, seed uint64) (time.Duration, error) {
	t0 := time.Now()
	c, err := dial(socks, target)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if _, err := fmt.Fprintf(c, "GET %d %d\n", n, seed); err != nil {
		return 0, err
	}
	// a stalled flow must fail, not hang the lab: no byte for 30 s
	h := sha256.New()
	buf := make([]byte, 256<<10)
	var got int64
	for got < n {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		m, err := c.Read(buf)
		h.Write(buf[:m])
		got += int64(m)
		if err != nil {
			if err == io.EOF && got == n {
				break
			}
			return 0, fmt.Errorf("download stopped at %d of %d bytes after %.1fs: %w", got, n, time.Since(t0).Seconds(), err)
		}
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != streamSum(seed, n) {
		return 0, fmt.Errorf("download of %d bytes arrived with another sha256", n)
	}
	return time.Since(t0), nil
}

type deadlineWriter struct {
	c net.Conn
	n atomic.Int64
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	_ = w.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	m, err := w.c.Write(p)
	w.n.Add(int64(m))
	return m, err
}

func doPut(socks, target string, n int64, seed uint64) error {
	d, err := put(socks, target, n, seed)
	if err != nil {
		return err
	}
	fmt.Printf("ok put bytes=%d sec=%.1f mbit=%.0f sha256=match\n", n, d.Seconds(), mbit(n, d))
	return nil
}

func put(socks, target string, n int64, seed uint64) (time.Duration, error) {
	t0 := time.Now()
	c, err := dial(socks, target)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if _, err := fmt.Fprintf(c, "PUT %d %d\n", n, seed); err != nil {
		return 0, err
	}
	w := &deadlineWriter{c: c}
	if _, err := io.CopyN(w, stream(seed), n); err != nil {
		return 0, fmt.Errorf("upload stopped at %d of %d bytes after %.1fs: %w", w.n.Load(), n, time.Since(t0).Seconds(), err)
	}
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("no answer after the upload: %w", err)
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != "OK" || f[1] != strconv.FormatInt(n, 10) {
		return 0, fmt.Errorf("the far side saw %q", strings.TrimSpace(line))
	}
	if f[2] != streamSum(seed, n) {
		return 0, fmt.Errorf("upload of %d bytes arrived with another sha256", n)
	}
	return time.Since(t0), nil
}

func doEcho(socks, target string, secs int, every time.Duration) error {
	lines, worst, d, err := echoSession(socks, target, secs, every)
	if err != nil {
		return err
	}
	fmt.Printf("ok echo sec=%.0f lines=%d slowest_ms=%d"+nl+"", d.Seconds(), lines, worst.Milliseconds())
	return nil
}

func echoSession(socks, target string, secs int, every time.Duration) (lines int, worst, took time.Duration, err error) {
	t0 := time.Now()
	c, err := dial(socks, target)
	if err != nil {
		return 0, 0, 0, err
	}
	defer c.Close()
	if _, err := io.WriteString(c, "ECHO"+nl); err != nil {
		return 0, 0, 0, err
	}
	br := bufio.NewReader(c)
	for time.Since(t0) < time.Duration(secs)*time.Second {
		want := fmt.Sprintf("ping %d %s", lines, strings.Repeat("x", 40))
		sent := time.Now()
		_ = c.SetWriteDeadline(sent.Add(20 * time.Second))
		if _, err := io.WriteString(c, want+nl); err != nil {
			return lines, worst, 0, fmt.Errorf("session broke after %.0fs (%d lines came back): write: %w", time.Since(t0).Seconds(), lines, err)
		}
		_ = c.SetReadDeadline(sent.Add(20 * time.Second))
		got, err := br.ReadString(10)
		if err != nil || strings.TrimSpace(got) != want {
			return lines, worst, 0, fmt.Errorf("session broke after %.0fs (%d lines came back): read: %v", time.Since(t0).Seconds(), lines, err)
		}
		if d := time.Since(sent); d > worst {
			worst = d
		}
		lines++
		time.Sleep(every)
	}
	return lines, worst, time.Since(t0), nil
}

// doHold keeps n sessions open at once, each a line every so often, so that the entry has n people to count at the same time.
func doHold(socks, target string, n, secs int, every time.Duration) error {
	var wg sync.WaitGroup
	var ok, bad, lines atomic.Int64
	var first atomic.Value
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, _, _, err := echoSession(socks, target, secs, every)
			lines.Add(int64(l))
			if err != nil {
				bad.Add(1)
				first.CompareAndSwap(nil, err.Error())
				return
			}
			ok.Add(1)
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		return fmt.Errorf("%d of %d sessions broke, first: %v", bad.Load(), n, first.Load())
	}
	fmt.Printf("ok hold sessions=%d sec=%d lines=%d"+nl+"", ok.Load(), secs, lines.Load())
	return nil
}

// doIdle holds a quiet session, then asks whether it is still there. It exits 0 either way and says which: the bridge is
// documented to close a session that is quiet for 2 minutes (the caller decides whether that is what it expects).
func doIdle(socks, target string, secs int) error {
	t0 := time.Now()
	c, err := dial(socks, target)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := io.WriteString(c, "ECHO\n"); err != nil {
		return err
	}
	closed := make(chan time.Duration, 1)
	go func() {
		var b [1]byte
		_, _ = c.Read(b[:])
		closed <- time.Since(t0)
	}()
	select {
	case d := <-closed:
		fmt.Printf("closed after=%.0fs\n", d.Seconds())
		return nil
	case <-time.After(time.Duration(secs) * time.Second):
	}
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, "late\n"); err != nil {
		fmt.Printf("closed after=%ds (write failed)\n", secs)
		return nil
	}
	select {
	case d := <-closed:
		fmt.Printf("closed after=%.0fs\n", d.Seconds())
	case <-time.After(5 * time.Second):
		fmt.Printf("alive after=%ds\n", secs)
	}
	return nil
}

func doPar(socks, target string, workers int, bytesPer int64, rounds int, seed uint64) error {
	t0 := time.Now()
	var wg sync.WaitGroup
	var ok, bad atomic.Int64
	var first atomic.Value
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				s := seed + uint64(w*1000+r)
				var e error
				if r%2 == 0 {
					_, e = get(socks, target, bytesPer, s)
				} else {
					_, e = put(socks, target, bytesPer, s)
				}
				if e != nil {
					bad.Add(1)
					first.CompareAndSwap(nil, e.Error())
				} else {
					ok.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		return fmt.Errorf("%d of %d transfers failed, first: %v", bad.Load(), bad.Load()+ok.Load(), first.Load())
	}
	fmt.Printf("ok par workers=%d transfers=%d bytes_each=%d sec=%.1f\n", workers, ok.Load(), bytesPer, time.Since(t0).Seconds())
	return nil
}

func doWho(socks, target string) error {
	c, err := dial(socks, target)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := io.WriteString(c, "WHO\n"); err != nil {
		return err
	}
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	fmt.Printf("ok who %s\n", strings.TrimSpace(line))
	return nil
}

// impostor is a TLS site with a certificate nobody pinned: the place a redirected bridge call lands in the lab. It logs one line
// per connection ("handshake ..." when the TLS handshake was finished, "tlsfail ..." when the caller stopped it) and one per HTTP
// request ("request METHOD PATH"): a caller that checks the certificate sends no request.
func impostor(addrs []string, logPath string) error {
	if len(addrs) != 1 || logPath == "" {
		return errors.New("impostor needs one -listen and a -log")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "impostor"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	l, err := tls.Listen("tcp", addrs[0], &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(f, format+"\n", a...)
	}
	fmt.Println("ready", addrs[0])
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			tc := c.(*tls.Conn)
			_ = tc.SetDeadline(time.Now().Add(15 * time.Second))
			if err := tc.Handshake(); err != nil {
				logf("tlsfail %s %v", tc.RemoteAddr(), err)
				return
			}
			logf("handshake %s", tc.RemoteAddr())
			req, err := http.ReadRequest(bufio.NewReader(tc))
			if err != nil {
				return
			}
			logf("request %s %s", req.Method, req.URL.Path)
			_, _ = io.WriteString(tc, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		}()
	}
}

// probeSite answers what the panel's own client-eye round asks through a relay (internal/panel/health: generate_204, the Cloudflare
// trace, a 64 KiB download) under the names it asks for. The panel trusts the certificate (it is in its trust file) and the exits
// resolve the names to this address (a hosts file of the exit's namespace); in the lab there is no other internet.
func probeSite(addrs []string, certPath string) error {
	if len(addrs) != 1 || certPath == "" {
		return errors.New("probesite needs one -listen and a -cert")
	}
	names := []string{"www.gstatic.com", "www.cloudflare.com", "speed.cloudflare.com"}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		nl := string(rune(10))
		_, _ = io.WriteString(w, strings.Join([]string{"fl=lab", "h=" + r.Host, "ip=" + host, "visit_scheme=https", "loc=ZZ", "tls=TLSv1.3", "warp=off", ""}, nl))
	})
	mux.HandleFunc("/__down", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if n <= 0 || n > 8<<20 {
			n = 65536
		}
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		_, _ = io.CopyN(w, stream(uint64(n)), n)
	})
	l, err := net.Listen("tcp", addrs[0])
	if err != nil {
		return err
	}
	fmt.Println("ready", addrs[0])
	srv := &http.Server{Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}}
	return srv.ServeTLS(l, "", "")
}
