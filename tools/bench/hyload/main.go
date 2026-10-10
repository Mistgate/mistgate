// hyload: Hysteria2 traffic tools for the one-server benchmark. Built on the official Hysteria 2 client library
// (github.com/apernet/hysteria/core/v2), the same code the official client binary uses for the QUIC/HTTP3 tunnel.
//
//	hyload sink  -listen 127.0.0.1:19000                      HTTP sink: /zero (endless zeros), /small (1 KiB)
//	hyload tp    -server H:P -auth A -sink H:P -streams N -d 30s   one tunnel, N parallel downloads, a small-request probe; JSON out
//	hyload many  -server H:P -auths FILE -n 400 -d 60s -sink H:P   N tunnels at once, light traffic; handshake and request latency; JSON out
//
// Congestion control is BBR (no bandwidth declared, so no Brutal). TLS: certificate not verified (every server in the
// benchmark has a self-signed certificate). All traffic goes to a local sink.
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

type obfsFactory struct{ pw string }

func (f *obfsFactory) New(addr net.Addr) (net.PacketConn, error) {
	pc, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	if f.pw == "" {
		return pc, nil
	}
	return obfs.WrapPacketConnSalamander(pc, []byte(f.pw))
}

func dial(server, auth, sni, obfsPW string) (client.Client, time.Duration, error) {
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, 0, err
	}
	t0 := time.Now()
	c, _, err := client.NewClient(&client.Config{
		ConnFactory: &obfsFactory{obfsPW},
		ServerAddr:  addr,
		Auth:        auth,
		TLSConfig:   client.TLSConfig{ServerName: sni, InsecureSkipVerify: true},
		FastOpen:    false,
	})
	return c, time.Since(t0), err
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[int(float64(len(v)-1)*p)]
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hyload sink|tp|many [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:19000", "sink listen address")
	server := fs.String("server", "", "hysteria2 server host:port")
	auth := fs.String("auth", "", "auth string (user:pass or password, as in the hysteria2:// link)")
	authsFile := fs.String("auths", "", "file with one auth string per line (many)")
	sni := fs.String("sni", "", "TLS server name")
	obfsPW := fs.String("obfs", "", "salamander password (empty = none)")
	sink := fs.String("sink", "127.0.0.1:19000", "sink address as seen from the server")
	streams := fs.Int("streams", 1, "parallel downloads (tp)")
	n := fs.Int("n", 100, "clients (many)")
	dur := fs.Duration("d", 30*time.Second, "duration")
	ramp := fs.Duration("ramp", 10*time.Second, "spread the client start over this time (many)")
	every := fs.Duration("every", time.Second, "request interval per client (many)")
	probe := fs.Duration("probe", 100*time.Millisecond, "probe interval (tp); 0 = off")
	fs.Parse(os.Args[2:])
	switch cmd {
	case "sink":
		runSink(*listen)
	case "tp":
		runTP(*server, *auth, *sni, *obfsPW, *sink, *streams, *dur, *probe)
	case "many":
		runMany(*server, *authsFile, *sni, *obfsPW, *sink, *n, *dur, *ramp, *every)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}

// ---------------------------------------------------------------- sink
func runSink(addr string) {
	zero := make([]byte, 1<<16)
	mux := http.NewServeMux()
	mux.HandleFunc("/zero", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		for {
			if _, err := w.Write(zero); err != nil {
				return
			}
		}
	})
	small := make([]byte, 1024)
	mux.HandleFunc("/small", func(w http.ResponseWriter, r *http.Request) { w.Write(small) })
	srv := &http.Server{Addr: addr, Handler: mux}
	fmt.Fprintln(os.Stderr, "sink on", addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// httpGet does one GET through the tunnel on a fresh stream and returns the connection and reader positioned at the body.
func get(c client.Client, sink, path string) (net.Conn, *bufio.Reader, error) {
	type res struct {
		conn net.Conn
		br   *bufio.Reader
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		conn, br, err := get0(c, sink, path)
		ch <- res{conn, br, err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.br, r.err
	case <-time.After(6 * time.Second):
		go func() { // a late answer is closed, not leaked
			if r := <-ch; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, nil, fmt.Errorf("open stream: timeout")
	}
}

func get0(c client.Client, sink, path string) (net.Conn, *bufio.Reader, error) {
	conn, err := c.TCP(sink)
	if err != nil {
		return nil, nil, fmt.Errorf("tcp request: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, sink); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("request write: %w", err)
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for { // skip the response header
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("response header: %w", err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, br, nil
}

// probeRes holds the three moments of one small request through the tunnel: the response header, the whole body (Content-Length
// bytes) and the end of the stream (EOF). The request latency is the body time; EOF is kept apart because some servers close the
// stream late.
type probeRes struct {
	hdr, body, eof time.Duration
	err            error
}

func probeOnce(c client.Client, sink, path string) probeRes {
	ch := make(chan probeRes, 1)
	go func() {
		t0 := time.Now()
		conn, err := c.TCP(sink)
		if err != nil {
			ch <- probeRes{err: fmt.Errorf("tcp request: %w", err)}
			return
		}
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, sink); err != nil {
			ch <- probeRes{err: err}
			return
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReaderSize(conn, 8<<10)
		var cl int64 = -1
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- probeRes{err: fmt.Errorf("response header: %w", err)}
				return
			}
			if line == "\r\n" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				fmt.Sscanf(strings.TrimSpace(line[15:]), "%d", &cl)
			}
		}
		r := probeRes{hdr: time.Since(t0)}
		if cl < 0 {
			ch <- probeRes{err: fmt.Errorf("no content-length")}
			return
		}
		if _, err := io.CopyN(io.Discard, br, cl); err != nil {
			ch <- probeRes{err: fmt.Errorf("body: %w", err)}
			return
		}
		r.body = time.Since(t0)
		io.Copy(io.Discard, br) // wait for the end of the stream
		r.eof = time.Since(t0)
		ch <- r
	}()
	select {
	case r := <-ch:
		return r
	case <-time.After(8 * time.Second):
		return probeRes{err: fmt.Errorf("timeout")}
	}
}

// ---------------------------------------------------------------- tp
func runTP(server, auth, sni, obfsPW, sink string, streams int, dur, probeEvery time.Duration) {
	c, hs, err := dial(server, auth, sni, obfsPW)
	if err != nil {
		out(map[string]any{"error": "dial: " + err.Error()})
		return
	}
	var cmu sync.RWMutex
	cur := func() client.Client { cmu.RLock(); defer cmu.RUnlock(); return c }
	defer func() { cur().Close() }()
	var total, dropped, reconnects, failStreak int64
	var redialing int32
	var gaps []float64
	var lmu sync.Mutex
	var lat []float64
	var probeFail int64
	var firstErr, probeErr atomic.Value
	var latHdr, latEOF []float64
	perSec := make([]int64, int(dur/time.Second)+2)
	start := time.Now()
	deadline := start.Add(dur)
	var wg sync.WaitGroup
	redial := func() {
		if !atomic.CompareAndSwapInt32(&redialing, 0, 1) {
			return
		}
		defer atomic.StoreInt32(&redialing, 0)
		t0 := time.Now()
		for time.Now().Before(deadline) {
			nc, _, err := dial(server, auth, sni, obfsPW)
			if err == nil {
				cmu.Lock()
				old := c
				c = nc
				cmu.Unlock()
				old.Close()
				atomic.AddInt64(&reconnects, 1)
				atomic.StoreInt64(&failStreak, 0)
				lmu.Lock()
				gaps = append(gaps, float64(time.Since(t0).Milliseconds()))
				lmu.Unlock()
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	fail := func() {
		if atomic.AddInt64(&failStreak, 1) >= 3 {
			go redial()
		}
	}
	buf := func() []byte { return make([]byte, 128<<10) }
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := buf()
			for time.Now().Before(deadline) {
				conn, br, err := get(cur(), sink, "/zero")
				if err != nil {
					firstErr.CompareAndSwap(nil, err.Error())
					atomic.AddInt64(&dropped, 1)
					fail()
					time.Sleep(200 * time.Millisecond)
					continue
				}
				atomic.StoreInt64(&failStreak, 0)
				for {
					conn.SetReadDeadline(time.Now().Add(3 * time.Second)) // a stream that delivers nothing for 3 s counts as dropped
					k, err := br.Read(b)
					if k > 0 {
						atomic.AddInt64(&total, int64(k))
						if s := int(time.Since(start) / time.Second); s < len(perSec) {
							atomic.AddInt64(&perSec[s], int64(k))
						}
					}
					if err != nil {
						if time.Now().Before(deadline.Add(-time.Second)) {
							atomic.AddInt64(&dropped, 1) // the stream died before the end of the run
							fail()
						}
						break
					}
					if time.Now().After(deadline) {
						break
					}
				}
				conn.Close()
			}
		}()
	}
	if probeEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk := time.NewTicker(probeEvery)
			defer tk.Stop()
			for time.Now().Before(deadline) {
				<-tk.C
				t0 := time.Now()
				_ = t0
				r := probeOnce(cur(), sink, "/small")
				if r.err != nil {
					atomic.AddInt64(&probeFail, 1) // a failed or timed-out probe is counted as a failure, never as a latency
					probeErr.CompareAndSwap(nil, r.err.Error())
				} else {
					lmu.Lock()
					lat = append(lat, float64(r.body.Microseconds())/1000)
					latHdr = append(latHdr, float64(r.hdr.Microseconds())/1000)
					latEOF = append(latEOF, float64(r.eof.Microseconds())/1000)
					lmu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	series := make([]float64, 0, len(perSec))
	for i := 0; i < int(dur/time.Second); i++ {
		series = append(series, float64(perSec[i])*8/1e6)
	}
	out(map[string]any{
		"streams": streams, "seconds": el, "bytes": total, "gbit_s": float64(total) * 8 / el / 1e9,
		"mbit_s_per_second": series, "streams_dropped": dropped, "reconnects": reconnects, "reconnect_ms": gaps, "handshake_ms": float64(hs.Microseconds()) / 1000,
		"first_error": firstErr.Load(), "probe_first_error": probeErr.Load(), "probe_n": len(lat), "probe_fail": probeFail,
		"probe_hdr_ms_p50": pct(latHdr, .5), "probe_hdr_ms_p95": pct(latHdr, .95), "probe_eof_ms_p50": pct(latEOF, .5), "probe_eof_ms_p95": pct(latEOF, .95), "probe_ms_p50": pct(lat, .5), "probe_ms_p95": pct(lat, .95), "probe_ms_p99": pct(lat, .99), "probe_ms_max": pct(lat, 1),
	})
}

// ---------------------------------------------------------------- many
func runMany(server, authsFile, sni, obfsPW, sink string, n int, dur, ramp, every time.Duration) {
	f, err := os.Open(authsFile)
	if err != nil {
		out(map[string]any{"error": err.Error()})
		return
	}
	var auths []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			auths = append(auths, t)
		}
	}
	if n > len(auths) {
		n = len(auths)
	}
	var mu sync.Mutex
	var hsLat, reqLat []float64
	var hsFail, reqFail, reqOK int64
	var connected int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(dur)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(float64(ramp) * float64(i) / float64(n)))
			c, hs, err := dial(server, auths[i], sni, obfsPW)
			if err != nil {
				atomic.AddInt64(&hsFail, 1)
				return
			}
			defer c.Close()
			atomic.AddInt64(&connected, 1)
			mu.Lock()
			hsLat = append(hsLat, float64(hs.Microseconds())/1000)
			mu.Unlock()
			for time.Now().Before(deadline) {
				r := probeOnce(c, sink, "/small")
				if r.err != nil {
					atomic.AddInt64(&reqFail, 1)
				} else {
					atomic.AddInt64(&reqOK, 1)
					mu.Lock()
					reqLat = append(reqLat, float64(r.body.Microseconds())/1000)
					mu.Unlock()
				}
				time.Sleep(every)
			}
		}(i)
	}
	wg.Wait()
	out(map[string]any{
		"clients": n, "connected": connected, "handshake_fail": hsFail, "handshake_ms_p50": pct(hsLat, .5), "handshake_ms_p95": pct(hsLat, .95), "handshake_ms_max": pct(hsLat, 1),
		"requests_ok": reqOK, "requests_fail": reqFail, "request_ms_p50": pct(reqLat, .5), "request_ms_p95": pct(reqLat, .95), "request_ms_p99": pct(reqLat, .99),
	})
}

func out(v any) {
	json.NewEncoder(os.Stdout).Encode(v)
}

var _ = tls.VersionTLS13
