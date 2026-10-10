// loadgen: N concurrent clients fetch subscription URLs for a fixed time; prints one JSON summary.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

type hlist []string

func (h *hlist) String() string     { return strings.Join(*h, ",") }
func (h *hlist) Set(v string) error { *h = append(*h, v); return nil }

func main() {
	urlsFile := flag.String("urls", "", "file with one URL per line")
	conc := flag.Int("c", 50, "concurrent clients")
	dur := flag.Duration("d", 60*time.Second, "duration")
	ua := flag.String("ua", "v2rayN/7.5.0", "User-Agent")
	var hdrs hlist
	flag.Var(&hdrs, "H", "extra header \"Name: value\" (repeatable)")
	xff := flag.Bool("xff", false, "send X-Forwarded-For: a fixed address per URL (10.<n>.<m>.7, one /24 per link)")
	bindPrefix := flag.String("bind-prefix", "11.11", "first two octets of the bound source addresses")
	bind := flag.Bool("bind", false, "bind the local address per link: 11.11.<n/250+1>.<n%250+1> (one source address per user; disables keep-alive)")
	rate := flag.Int("rate", 0, "open-loop pacing: total requests per second (0 = as fast as possible)")
	once := flag.Bool("once", false, "fetch every URL exactly once, sequentially (warm-up), print nothing but a short summary")
	flag.Parse()
	var urls []string
	f, err := os.Open(*urlsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() != "" {
			urls = append(urls, sc.Text())
		}
	}
	var bindA, bindB byte
	{
		var a, b int
		fmt.Sscanf(*bindPrefix, "%d.%d", &a, &b)
		bindA, bindB = byte(a), byte(b)
	}
	type bindKey struct{}
	tr := &http.Transport{MaxIdleConnsPerHost: *conc, MaxIdleConns: *conc, DisableCompression: true}
	if *bind {
		tr.DisableKeepAlives = true
		d := &net.Dialer{}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if k, ok := ctx.Value(bindKey{}).(int); ok {
				dd := *d
				dd.LocalAddr = &net.TCPAddr{IP: net.IPv4(bindA, bindB, byte(k/250+1), byte(k%250+1))}
				return dd.DialContext(ctx, network, addr)
			}
			return d.DialContext(ctx, network, addr)
		}
	}
	cl := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	fetch := func(u string, k int) (int, int, string, error) {
		req, _ := http.NewRequestWithContext(context.WithValue(context.Background(), bindKey{}, k), "GET", u, nil)
		req.Header.Set("User-Agent", *ua)
		for _, h := range hdrs {
			if k, v, ok := strings.Cut(h, ":"); ok {
				req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
		if *xff {
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.%d.%d.7", (k>>8)&255, k&255))
		}
		resp, err := cl.Do(req)
		if err != nil {
			return 0, 0, "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, len(b), "", err
		}
		return resp.StatusCode, len(b), resp.Header.Get("Content-Type"), nil
	}
	if *once {
		bad := 0
		for k, u := range urls {
			if st, _, _, err := fetch(u, k); err != nil || st != 200 {
				bad++
			}
		}
		fmt.Printf("{\"warmup_urls\":%d,\"bad\":%d}\n", len(urls), bad)
		return
	}
	var (
		mu      sync.Mutex
		lats    []float64
		codes   = map[string]int{}
		bytesN  int64
		ctypes  = map[string]int{}
		stop    = make(chan struct{})
		wg      sync.WaitGroup
		errs    int64
		idx     int64
		sizeSet = map[int]int{}
	)
	var slowMu sync.Mutex
	slowSecs := map[int]int{}
	var tokens chan struct{}
	if *rate > 0 {
		tokens = make(chan struct{}, 256)
	}
	start := time.Now()
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var l []float64
			lc := map[string]int{}
			lt := map[string]int{}
			ls := map[int]int{}
			var lb int64
			for {
				select {
				case <-stop:
					mu.Lock()
					lats = append(lats, l...)
					for k, v := range lc {
						codes[k] += v
					}
					for k, v := range lt {
						ctypes[k] += v
					}
					for k, v := range ls {
						sizeSet[k] += v
					}
					bytesN += lb
					mu.Unlock()
					return
				default:
				}
				if tokens != nil { // open-loop pacing: one request per token
					select {
					case <-tokens:
					case <-stop:
						continue
					}
				}
				k := int(atomic.AddInt64(&idx, 1)) % len(urls)
				u := urls[k]
				t0 := time.Now()
				st, n, ct, err := fetch(u, k)
				el := float64(time.Since(t0).Microseconds()) / 1000
				if err != nil {
					atomic.AddInt64(&errs, 1)
					lc["transport_error"]++
					continue
				}
				lc[fmt.Sprint(st)]++
				if st != 200 {
					atomic.AddInt64(&errs, 1)
				} else {
					l = append(l, el)
					if el > 500 {
						slowMu.Lock()
						slowSecs[int(time.Since(start).Seconds())]++
						slowMu.Unlock()
					}
					lb += int64(n)
					lt[ct]++
					ls[n/50*50]++
				}
			}
		}()
	}
	if tokens != nil {
		go func() {
			tk := time.NewTicker(time.Second / time.Duration(*rate))
			defer tk.Stop()
			for {
				select {
				case <-tk.C:
					select {
					case tokens <- struct{}{}:
					default:
					}
				case <-stop:
					return
				}
			}
		}()
	}
	time.Sleep(*dur)
	close(stop)
	wg.Wait()
	el := time.Since(start).Seconds()
	sort.Float64s(lats)
	pct := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		return lats[int(float64(len(lats)-1)*p)]
	}
	total := 0
	for _, v := range codes {
		total += v
	}
	var mean float64
	for _, v := range lats {
		mean += v
	}
	if len(lats) > 0 {
		mean /= float64(len(lats))
	}
	h := sha256.Sum256([]byte(urls[0]))
	_ = hex.EncodeToString(h[:])
	out := map[string]any{
		"elapsed_s": el, "requests": total, "ok": len(lats), "errors": errs,
		"error_rate": float64(errs) / float64(max(total, 1)), "rps": float64(total) / el, "ok_rps": float64(len(lats)) / el,
		"lat_ms_mean": mean, "lat_ms_p50": pct(.50), "lat_ms_p95": pct(.95), "lat_ms_p99": pct(.99), "lat_ms_max": pct(1),
		"slow_requests_per_second_over_500ms": slowSecs, "urls": len(urls), "requests_per_url": float64(total) / float64(len(urls)), "paced_rate": *rate, "concurrency": *conc,
		"status_codes": codes, "content_types": ctypes, "body_size_buckets": sizeSet, "mean_body_bytes": float64(bytesN) / float64(max(len(lats), 1)),
	}
	json.NewEncoder(os.Stdout).Encode(out)
}
