package blocklist

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestParseCIDRs(t *testing.T) {
	body := "# a comment\n\n203.0.113.0/24\r\n198.51.100.77\n192.0.2.130/25\n2001:db8::/32\n  203.0.113.0/24  \n"
	set, _, err := parseCIDRs([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	// the duplicate went, the bare address is a /32, host bits are cleared, IPv6 is not stored
	var got []string
	for _, e := range set.e {
		got = append(got, e.p.String())
	}
	if want := "192.0.2.128/25 198.51.100.77/32 203.0.113.0/24"; strings.Join(got, " ") != want {
		t.Fatalf("prefixes = %v, want %s", got, want)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	html := "<!doctype html>\n<html>\n<body>\n<h1>502 Bad Gateway</h1>\n</body>\n</html>\n"
	for name, body := range map[string]string{"an html page": html, "empty": "", "only comments": "# nothing\n", "only IPv6": "2001:db8::/32\n"} {
		if _, _, err := parseCIDRs([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, _, err := parseCDN([]byte(body)); err == nil {
			t.Errorf("%s: accepted as a CDN list", name)
		}
	}
	// a few bad lines among good ones are skipped; a flood of them refuses the list
	good := strings.Repeat("203.0.113.0/24\n", 3) + "198.51.100.0/24\n"
	if set, _, err := parseCIDRs([]byte(good + "not an address\n999.1.1.1/8\n")); err != nil || len(set.e) != 2 {
		t.Fatalf("two bad lines: %d prefixes, %v", len(set.e), err)
	}
	var flood strings.Builder
	flood.WriteString(good)
	for i := range 40 {
		fmt.Fprintf(&flood, "garbage %d\n", i)
	}
	if _, _, err := parseCIDRs([]byte(flood.String())); err == nil {
		t.Fatal("a list that is mostly garbage was accepted")
	}
}

func TestLongestPrefixMatch(t *testing.T) {
	set, _, err := parseCIDRs([]byte("203.0.113.0/24\n198.51.100.0/24\n198.51.100.8/29\n10.0.0.0/8\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ip   string
		want string // "" = no match
	}{
		{"203.0.113.10", "203.0.113.0/24"},    // a /24 hit
		{"203.0.113.255", "203.0.113.0/24"},   // the last address of it
		{"203.0.114.1", ""},                   // just outside
		{"198.51.100.9", "198.51.100.8/29"},   // the nested /29 wins over the /24
		{"198.51.100.100", "198.51.100.0/24"}, // beside it, the /24
		{"10.200.1.1", "10.0.0.0/8"},
		{"192.0.2.1", ""},           // a miss
		{"2001:db8::1", ""},         // IPv6 matches nothing
		{"::ffff:203.0.113.10", ""}, // a mapped address must be unmapped by the caller
	} {
		p, ok := set.longest(addr(c.ip))
		if got := p.p.String(); (c.want == "" && ok) || (c.want != "" && got != c.want) {
			t.Errorf("%s -> %v %v, want %q", c.ip, got, ok, c.want)
		}
	}
	if _, ok := (prefixSet{}).longest(addr("203.0.113.10")); ok {
		t.Error("an empty set matched")
	}
}

func TestParseCDNAndProviderLookup(t *testing.T) {
	body := "provider,cidr,region\n" +
		"ExampleHost,203.0.113.0/24,eu\n" +
		"ExampleHost,203.0.113.64/26,eu-north\n" + // nested: the narrower range wins
		"\"Example, Cloud\",198.51.100.0/24,\n" + // a quoted name, no region
		"ExampleHost,2001:db8::/32,eu\n" + // IPv6 ignored
		"\n"
	set, meta, err := parseCDN([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.e) != 3 || len(meta) != 3 {
		t.Fatalf("%d ranges, %d metas", len(set.e), len(meta))
	}
	l := &list{set: set, meta: meta, loaded: true}
	none := &list{}
	for _, c := range []struct{ ip, provider, region string }{
		{"203.0.113.10", "ExampleHost", "eu"},
		{"203.0.113.70", "ExampleHost", "eu-north"},
		{"198.51.100.5", "Example, Cloud", ""},
		{"192.0.2.1", "", ""},
	} {
		m := match(none, none, l, addr(c.ip))
		if m.cdnProvider != c.provider || m.cdnRegion != c.region || m.inRKN {
			t.Errorf("%s -> %+v, want %s/%s", c.ip, m, c.provider, c.region)
		}
	}
}

func TestMatchNamesTheListsAndTheNarrowestSubnet(t *testing.T) {
	mk := func(body string) *list {
		s, _, err := parseCIDRs([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return &list{set: s, loaded: true}
	}
	ipsum, community := mk("203.0.113.0/24\n"), mk("203.0.113.10/32\n198.51.100.1/32\n")
	m := match(ipsum, community, &list{}, addr("203.0.113.10"))
	if !m.inRKN || strings.Join(m.listedBy, ",") != ListIPSum+","+ListCommunity || m.subnet.String() != "203.0.113.10/32" {
		t.Fatalf("both lists: %+v", m)
	}
	if m := match(ipsum, community, &list{}, addr("198.51.100.1")); strings.Join(m.listedBy, ",") != ListCommunity {
		t.Fatalf("community only: %+v", m)
	}
	// a list that is not loaded says nothing
	if m := match(&list{}, &list{}, &list{}, addr("203.0.113.10")); m.inRKN {
		t.Fatalf("empty lists: %+v", m)
	}
}

// ---------------------------------------------------------------------------------------------------
// A fake server for the lists

// fakeLists serves the four lists. Nothing here is the real address of anything.
type fakeLists struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	bodies   map[string]string // path -> body
	etags    map[string]string
	modified map[string]string
	status   map[string]int           // path -> forced status
	delay    map[string]time.Duration // path -> time to answer
	hits     map[string]int
	conds    map[string]int // requests that carried a validator
	requests []string       // "METHOD URL"
}

func newFakeLists(t *testing.T) *fakeLists {
	f := &fakeLists{t: t, bodies: map[string]string{}, etags: map[string]string{}, modified: map[string]string{}, status: map[string]int{},
		delay: map[string]time.Duration{}, hits: map[string]int{}, conds: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLists) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.requests = append(f.requests, r.Method+" "+r.URL.String()+" "+r.Header.Get("Referer")+r.Header.Get("Cookie"))
	body, etag, mod, st, delay := f.bodies[r.URL.Path], f.etags[r.URL.Path], f.modified[r.URL.Path], f.status[r.URL.Path], f.delay[r.URL.Path]
	inm, ims := r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since")
	if inm != "" || ims != "" {
		f.conds[r.URL.Path]++
	}
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if st != 0 {
		http.Error(w, "forced", st)
		return
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
		if inm == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if mod != "" {
		w.Header().Set("Last-Modified", mod)
		if ims == mod && inm == "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = w.Write([]byte(body))
}

func (f *fakeLists) set(path, body, etag, modified string) {
	f.mu.Lock()
	f.bodies[path], f.etags[path], f.modified[path] = body, etag, modified
	f.mu.Unlock()
}

func (f *fakeLists) force(path string, status int) {
	f.mu.Lock()
	f.status[path] = status
	f.mu.Unlock()
}

func (f *fakeLists) count(path string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits[path] }

func (f *fakeLists) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeLists) urls() URLs {
	return URLs{IPSum: f.srv.URL + "/ipsum", IPSumMirror: f.srv.URL + "/mirror", Community: f.srv.URL + "/community", CDN: f.srv.URL + "/cdn"}
}

const (
	modified1 = "Tue, 14 Oct 2026 09:00:00 GMT"
	modified2 = "Tue, 14 Oct 2026 15:00:00 GMT"
)

func (f *fakeLists) standard() {
	f.set("/ipsum", "203.0.113.0/24\n198.51.100.0/24\n", `"ipsum-1"`, modified1)
	f.set("/mirror", "203.0.113.0/24\n", "", modified1)
	f.set("/community", "192.0.2.77/32\n", `"community-1"`, modified1)
	f.set("/cdn", "provider,cidr,region\nExampleHost,203.0.113.0/24,eu\nExampleCloud,192.0.2.0/24,\n", "", "")
}

func newList(f *fakeLists, path string, max int64, parse func([]byte) (prefixSet, []cdnMeta, error), more ...string) *list {
	urls := []string{f.srv.URL + path}
	for _, m := range more {
		urls = append(urls, f.srv.URL+m)
	}
	return &list{max: max, urls: urls, parse: parse}
}

var epoch = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func TestListFirstFetchThenNotModified(t *testing.T) {
	f := newFakeLists(t)
	f.standard()
	l := newList(f, "/ipsum", maxIPSum, parseCIDRs)
	hc := f.srv.Client()
	if _, err := l.refresh(context.Background(), hc, epoch); err != nil {
		t.Fatal(err)
	}
	if !l.loaded || len(l.set.e) != 2 || l.etag != `"ipsum-1"` || !l.date.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("first fetch: loaded=%v prefixes=%d etag=%q date=%v", l.loaded, len(l.set.e), l.etag, l.date)
	}
	if f.conds["/ipsum"] != 0 {
		t.Fatal("the first request was conditional")
	}
	// the second asks with the validators and the server answers 304: the copy stays, checked moves
	later := epoch.Add(7 * time.Hour)
	if _, err := l.refresh(context.Background(), hc, later); err != nil {
		t.Fatal(err)
	}
	if f.conds["/ipsum"] != 1 || f.count("/ipsum") != 2 || len(l.set.e) != 2 || !l.checked.Equal(later) || !l.date.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("after 304: conds=%d hits=%d prefixes=%d checked=%v", f.conds["/ipsum"], f.count("/ipsum"), len(l.set.e), l.checked)
	}
	// the list changes: a 200 replaces the copy
	f.set("/ipsum", "203.0.113.0/24\n198.51.100.0/24\n192.0.2.0/24\n", `"ipsum-2"`, modified2)
	if _, err := l.refresh(context.Background(), hc, later.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(l.set.e) != 3 || l.etag != `"ipsum-2"` || !l.date.Equal(time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("after the change: prefixes=%d etag=%q date=%v", len(l.set.e), l.etag, l.date)
	}
}

func TestListFallsBackToTheMirror(t *testing.T) {
	f := newFakeLists(t)
	f.standard()
	f.force("/ipsum", http.StatusBadGateway)
	l := newList(f, "/ipsum", maxIPSum, parseCIDRs, "/mirror")
	usedAlt, err := l.refresh(context.Background(), f.srv.Client(), epoch)
	if err != nil || !usedAlt || len(l.set.e) != 1 || l.url != f.srv.URL+"/mirror" {
		t.Fatalf("mirror: usedAlt=%v err=%v prefixes=%d url=%s", usedAlt, err, len(l.set.e), l.url)
	}
	// the main address is back: it is tried first again, without the mirror's validators
	f.force("/ipsum", 0)
	usedAlt, err = l.refresh(context.Background(), f.srv.Client(), epoch.Add(time.Hour))
	if err != nil || usedAlt || len(l.set.e) != 2 || f.conds["/ipsum"] != 0 {
		t.Fatalf("back to the main address: usedAlt=%v err=%v prefixes=%d conds=%d", usedAlt, err, len(l.set.e), f.conds["/ipsum"])
	}
}

func TestListOverTheSizeCapIsRefusedAndTheCopyStays(t *testing.T) {
	f := newFakeLists(t)
	f.standard()
	l := newList(f, "/ipsum", maxIPSum, parseCIDRs)
	if _, err := l.refresh(context.Background(), f.srv.Client(), epoch); err != nil {
		t.Fatal(err)
	}
	f.set("/ipsum", strings.Repeat("203.0.113.0/24\n", 100), `"ipsum-big"`, modified2)
	l.max = 200
	if _, err := l.refresh(context.Background(), f.srv.Client(), epoch.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "size cap") {
		t.Fatalf("an oversized list: %v", err)
	}
	if len(l.set.e) != 2 || l.etag != `"ipsum-1"` {
		t.Fatalf("the copy changed: prefixes=%d etag=%q", len(l.set.e), l.etag)
	}
}

func TestListGarbageIsRefusedAndTheCopyStays(t *testing.T) {
	f := newFakeLists(t)
	f.standard()
	l := newList(f, "/ipsum", maxIPSum, parseCIDRs)
	if _, err := l.refresh(context.Background(), f.srv.Client(), epoch); err != nil {
		t.Fatal(err)
	}
	f.set("/ipsum", "<html>\n<body>\nCaptive portal\n</body>\n</html>\n", `"ipsum-2"`, modified2)
	if _, err := l.refresh(context.Background(), f.srv.Client(), epoch.Add(time.Hour)); err == nil {
		t.Fatal("an html page was accepted as a list")
	}
	if len(l.set.e) != 2 || l.etag != `"ipsum-1"` {
		t.Fatalf("the copy changed: prefixes=%d etag=%q", len(l.set.e), l.etag)
	}
}

func TestListTimeout(t *testing.T) {
	f := newFakeLists(t)
	f.standard()
	f.mu.Lock()
	f.delay["/ipsum"] = 2 * time.Second
	f.mu.Unlock()
	l := newList(f, "/ipsum", maxIPSum, parseCIDRs)
	hc := &http.Client{Timeout: 50 * time.Millisecond}
	start := time.Now()
	if _, err := l.refresh(context.Background(), hc, epoch); err == nil || l.loaded {
		t.Fatalf("a timeout: err=%v loaded=%v", err, l.loaded)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestListErrorsNeverNameTheURL(t *testing.T) {
	l := &list{max: maxIPSum, urls: []string{"http://127.0.0.1:1/secret-path-xyz"}, parse: parseCIDRs}
	_, err := l.refresh(context.Background(), &http.Client{Timeout: time.Second}, epoch)
	if err == nil || strings.Contains(err.Error(), "secret-path-xyz") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error = %v", err)
	}
}
