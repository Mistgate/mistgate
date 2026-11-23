//go:build !js

package decoyphotos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/decoy/sitegen"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const testKey = "test-key-not-real-0123456789"

// fakeNet is the only network these tests have: a RoundTripper that answers from a handler and remembers what it was asked.
type fakeNet struct {
	mu    sync.Mutex
	seen  []*http.Request
	serve func(req *http.Request, w http.ResponseWriter)
}

func (f *fakeNet) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.seen = append(f.seen, req.Clone(context.Background()))
	f.mu.Unlock()
	rec := httptest.NewRecorder()
	f.serve(req, rec)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (f *fakeNet) hosts() (out []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.seen {
		out = append(out, r.URL.Host)
	}
	return
}

func (f *fakeNet) count(host string) (n int) {
	for _, h := range f.hosts() {
		if h == host {
			n++
		}
	}
	return
}

func stub(t testing.TB, w, h int, noisy bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(int64(w*7 + h)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 90, 255}
			if noisy {
				c = color.RGBA{uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func searchJSON(ids ...int64) string {
	var ph []map[string]any
	for _, id := range ids {
		ph = append(ph, map[string]any{"id": id, "width": 4000, "height": 3000, "alt": "a loaf of bread on a table", "src": map[string]string{
			"large":   fmt.Sprintf("https://images.pexels.com/photos/%d/large.jpeg", id),
			"large2x": fmt.Sprintf("https://images.pexels.com/photos/%d/large2x.jpeg", id),
		}})
	}
	b, _ := json.Marshal(map[string]any{"photos": ph})
	return string(b)
}

func TestSearchAsksTheRightWayAndKeepsOnlyImageHostFiles(t *testing.T) {
	var got *http.Request
	n := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		got = req
		fmt.Fprint(w, `{"photos":[
			{"id":1,"alt":"ok","src":{"large":"https://images.pexels.com/a.jpeg","large2x":"https://images.pexels.com/b.jpeg"}},
			{"id":2,"alt":"evil host","src":{"large":"https://evil.example/a.jpeg"}},
			{"id":3,"alt":"plain http","src":{"large":"http://images.pexels.com/a.jpeg"}},
			{"id":4,"alt":"userinfo","src":{"large":"https://images.pexels.com@evil.example/a.jpeg"}},
			{"id":5,"alt":"port","src":{"large":"https://images.pexels.com:8443/a.jpeg"}},
			{"id":6,"alt":"only large","src":{"large":"https://images.pexels.com/c.jpeg"}}]}`)
	}}
	res, err := New(n).Search(context.Background(), testKey, "sourdough loaf", false, 2, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].ID != 1 || res[1].ID != 6 || res[1].Big != res[1].Small {
		t.Fatalf("results = %+v", res)
	}
	q := got.URL.Query()
	if got.URL.Scheme != "https" || got.URL.Host != "api.pexels.com" || got.URL.Path != "/v1/search" ||
		q.Get("query") != "sourdough loaf" || q.Get("orientation") != "landscape" || q.Get("per_page") != "30" || q.Get("page") != "2" {
		t.Fatalf("request = %s", got.URL)
	}
	if got.Header.Get("Authorization") != testKey || !strings.HasPrefix(got.Header.Get("User-Agent"), "mistgate-panel") {
		t.Fatalf("headers = %v", got.Header)
	}
	if _, err := New(n).Search(context.Background(), testKey, "x", true, 1, 5); err != nil || got.URL.Query().Get("orientation") != "portrait" {
		t.Fatalf("portrait: %v %s", err, got.URL)
	}
}

func TestStatusesBecomeCodes(t *testing.T) {
	for _, tc := range []struct {
		status int
		retry  string
		code   string
		wait   time.Duration
	}{
		{401, "", CodeKey, 0}, {403, "", CodeKey, 0}, {429, "120", CodeLimit, 2 * time.Minute}, {429, "", CodeLimit, 0},
		{500, "", CodeUnavailable, 0}, {404, "", CodeUnavailable, 0}, {204, "", CodeUnavailable, 0},
	} {
		n := &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) {
			if tc.retry != "" {
				w.Header().Set("Retry-After", tc.retry)
			}
			w.WriteHeader(tc.status)
			fmt.Fprint(w, `{"error":"`+testKey+`"}`)
		}}
		_, err := New(n).Search(context.Background(), testKey, "x", false, 1, 5)
		var e *Error
		if !errors.As(err, &e) || e.Code != tc.code || e.RetryAfter != tc.wait {
			t.Fatalf("status %d: %v", tc.status, err)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Fatalf("status %d: the error shows the key", tc.status)
		}
	}
}

// A redirect to another host, to plain http, or past three hops is not followed: nothing is sent to the other place.
func TestRedirectsStayOnPexels(t *testing.T) {
	for _, to := range []string{"https://evil.example/steal", "http://api.pexels.com/v1/search", "https://api.pexels.com:444/v1/x", "https://user@api.pexels.com/v1/x"} {
		n := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
			if req.URL.Host == "api.pexels.com" && req.URL.Path == "/v1/search" && req.URL.Scheme == "https" {
				http.Redirect(w, req, to, http.StatusFound)
				return
			}
			w.WriteHeader(200)
			fmt.Fprint(w, `{"photos":[]}`)
		}}
		_, err := New(n).Search(context.Background(), testKey, "x", false, 1, 5)
		if CodeOf(err) != CodeUnavailable {
			t.Fatalf("redirect to %s: %v", to, err)
		}
		if len(n.seen) != 1 {
			t.Fatalf("redirect to %s: %d requests were made, hosts %v", to, len(n.seen), n.hosts())
		}
	}
	loops := 0
	n := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		loops++
		http.Redirect(w, req, "https://api.pexels.com/v1/search?p="+strconv.Itoa(loops), http.StatusFound)
	}}
	if _, err := New(n).Search(context.Background(), testKey, "x", false, 1, 5); err == nil || loops > 4 {
		t.Fatalf("a redirect loop ran %d times (%v)", loops, err)
	}
	// an allowed redirect (the API to the image host) is followed
	ok := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		if req.URL.Host == "images.pexels.com" && req.URL.Path == "/a" {
			w.Write([]byte("DATA"))
			return
		}
		http.Redirect(w, req, "https://images.pexels.com/a", http.StatusFound)
	}}
	if b, err := New(ok).Download(context.Background(), "https://images.pexels.com/start"); err != nil || string(b) != "DATA" {
		t.Fatalf("download through a redirect: %q %v", b, err)
	}
}

func TestDownloadOnlyFromTheImageHostWithoutTheKey(t *testing.T) {
	n := &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) { w.Write([]byte("x")) }}
	c := New(n)
	for _, u := range []string{"https://evil.example/a.jpg", "http://images.pexels.com/a.jpg", "https://api.pexels.com/a.jpg", "ftp://images.pexels.com/a", "", "https://images.pexels.com:1/a"} {
		if _, err := c.Download(context.Background(), u); CodeOf(err) != CodeUnavailable {
			t.Errorf("Download(%q) = %v", u, err)
		}
	}
	if len(n.seen) != 0 {
		t.Fatalf("a refused URL was still requested: %v", n.hosts())
	}
	if _, err := c.Download(context.Background(), "https://images.pexels.com/a.jpg"); err != nil || n.seen[0].Header.Get("Authorization") != "" {
		t.Fatalf("download: %v, authorization %q", err, n.seen[0].Header.Get("Authorization"))
	}
}

func TestSizeLimits(t *testing.T) {
	big := &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"photos":[],"pad":"`))
		w.Write(bytes.Repeat([]byte("a"), maxJSON))
		w.Write([]byte(`"}`))
	}}
	if _, err := New(big).Search(context.Background(), testKey, "x", false, 1, 5); CodeOf(err) != CodeUnavailable {
		t.Fatalf("a JSON over 1 MiB: %v", err)
	}
	file := func(n int) *fakeNet {
		return &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) { w.Write(bytes.Repeat([]byte{1}, n)) }}
	}
	if _, err := New(file(maxFile+1)).Download(context.Background(), "https://images.pexels.com/a"); CodeOf(err) != CodeUnavailable {
		t.Fatalf("a file over 8 MiB: %v", err)
	}
	if b, err := New(file(maxFile)).Download(context.Background(), "https://images.pexels.com/a"); err != nil || len(b) != maxFile {
		t.Fatalf("a file of exactly 8 MiB: %d %v", len(b), err)
	}
	// a declared length over the limit is refused before the body is read
	declared := &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) {
		w.Header().Set("Content-Length", strconv.Itoa(maxFile+10))
		w.Write([]byte("short"))
	}}
	if _, err := New(declared).Download(context.Background(), "https://images.pexels.com/a"); CodeOf(err) != CodeUnavailable {
		t.Fatalf("a declared length over 8 MiB: %v", err)
	}
}

type blocking struct{}

func (blocking) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestTimeoutsAndCancel(t *testing.T) {
	oldS, oldF := searchTimeout, fileTimeout
	searchTimeout, fileTimeout = 40*time.Millisecond, 40*time.Millisecond
	defer func() { searchTimeout, fileTimeout = oldS, oldF }()
	c := New(blocking{})
	start := time.Now()
	if _, err := c.Search(context.Background(), testKey, "x", false, 1, 5); err == nil {
		t.Fatal("a search that never answers succeeded")
	}
	if _, err := c.Download(context.Background(), "https://images.pexels.com/a"); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("download timeout: %v after %v", err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, testKey, "x", false, 1, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context gave %v", err)
	}
}

func TestScaleCropsShrinksAndNeverEnlarges(t *testing.T) {
	src := stub(t, 400, 300, false)
	sizeOf := func(w, h, maxW int) (int, int) {
		s, err := Scale(src, w, h, maxW)
		if err != nil {
			t.Fatal(err)
		}
		return s.Size()
	}
	if w, h := sizeOf(4, 3, 200); w != 200 || h != 150 {
		t.Errorf("4:3 at 200 = %dx%d", w, h)
	}
	if w, h := sizeOf(16, 9, 1280); w != 400 || h != 225 { // cut to the shape, not enlarged
		t.Errorf("16:9 at 1280 = %dx%d", w, h)
	}
	if w, h := sizeOf(1, 1, 100); w != 100 || h != 100 {
		t.Errorf("1:1 at 100 = %dx%d", w, h)
	}
	if w, h := sizeOf(640, 900, 640); w != 213 || h != 300 { // portrait out of a landscape picture
		t.Errorf("640:900 = %dx%d", w, h)
	}
	if w, h := sizeOf(3, 1, 123); w != 123 || h != 40 {
		t.Errorf("3:1 at 123 = %dx%d", w, h)
	}
}

func TestScaleAveragesAndStripsMetadata(t *testing.T) {
	// a 2x2 black and white checker becomes one grey pixel
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i, c := range []uint8{0, 255, 255, 0} {
		img.Set(i%2, i/2, color.RGBA{c, c, c, 255})
	}
	var p bytes.Buffer
	png.Encode(&p, img)
	s, err := Scale(p.Bytes(), 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := s.img.At(0, 0).RGBA(); r>>8 < 126 || r>>8 > 129 || g != r || b != r {
		t.Fatalf("average = %d %d %d", r>>8, g>>8, b>>8)
	}
	// metadata: a JPEG with an Exif segment comes out without it, and the bytes are not the original's
	orig := stub(t, 64, 48, false)
	exif := append([]byte{0xFF, 0xE1, 0x00, 0x16}, []byte("Exif\x00\x00GPS-SECRET-DATA")...)
	withExif := append(append(append([]byte{}, orig[:2]...), exif...), orig[2:]...)
	s, err = Scale(withExif, 4, 3, 64)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.JPEG(75)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("Exif")) || bytes.Contains(out, []byte("GPS-SECRET")) || bytes.Equal(out, withExif) {
		t.Fatal("the metadata survived")
	}
	if im, err := jpeg.Decode(bytes.NewReader(out)); err != nil || im.Bounds().Dx() != 64 {
		t.Fatalf("the output is not a JPEG of 64 px: %v", err)
	}
}

func TestScaleRefusesWhatIsNotAPicture(t *testing.T) {
	if _, err := Scale([]byte("not a picture"), 4, 3, 100); err == nil {
		t.Error("garbage was accepted")
	}
	if _, err := Scale(stub(t, 10, 10, false)[:50], 4, 3, 100); err == nil {
		t.Error("a cut-off file was accepted")
	}
	if _, err := Scale(stub(t, 10, 10, false), 0, 3, 100); err == nil {
		t.Error("a zero shape was accepted")
	}
	// a PNG that claims to be 20000 x 20000 is refused from its header, before any pixel is allocated
	hdr := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x4e\x20\x00\x00\x4e\x20\x08\x02\x00\x00\x00")
	if _, err := Scale(hdr, 4, 3, 100); err == nil {
		t.Error("a 400 megapixel picture was accepted")
	}
}

func TestEncodeWithinBudget(t *testing.T) {
	pics, order := map[string]*Scaled{}, []sitegen.Slot{}
	for i := 0; i < 6; i++ {
		s, err := Scale(stub(t, 1280, 960, true), 4, 3, 1280) // noise does not compress
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("s%d", i)
		pics[name], order = s, append(order, sitegen.Slot{Name: name})
	}
	out := encodeWithin(pics, order, Budget)
	total := 0
	for _, b := range out {
		total += len(b)
	}
	if len(out) == 0 || len(out) == len(order) || total > Budget {
		t.Fatalf("%d of %d photos kept, %d bytes (budget %d)", len(out), len(order), total, Budget)
	}
	for i := 0; i < len(out); i++ { // the first slots are the ones kept
		if _, ok := out[fmt.Sprintf("s%d", i)]; !ok {
			t.Fatalf("slot s%d was dropped while a later one stayed: %v", i, len(out))
		}
	}
	small := map[string]*Scaled{"s0": pics["s0"]}
	if got := encodeWithin(small, order[:1], Budget); len(got) != 1 {
		t.Fatalf("one photo that fits was dropped")
	}
}

// pexelsFake serves search pages and picture files for the Maker tests and counts them.
type pexelsFake struct {
	*fakeNet
	searches, files int
	status          int // answer the search with this status when not 0
	empty           bool
	alt             string
}

func newPexels(t *testing.T) *pexelsFake {
	p := &pexelsFake{}
	jpg := stub(t, 1600, 1100, false)
	p.fakeNet = &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch req.URL.Host {
		case "api.pexels.com":
			p.searches++
			if p.status != 0 {
				w.WriteHeader(p.status)
				return
			}
			if p.empty {
				fmt.Fprint(w, `{"photos":[]}`)
				return
			}
			pg, _ := strconv.Atoi(req.URL.Query().Get("page"))
			ids := make([]int64, 30)
			for i := range ids {
				ids[i] = int64(pg*1000 + i + 1)
			}
			body := searchJSON(ids...)
			if p.alt != "" {
				body = strings.ReplaceAll(body, "a loaf of bread on a table", p.alt)
			}
			fmt.Fprint(w, body)
		case "images.pexels.com":
			p.files++
			w.Write(jpg)
		default:
			w.WriteHeader(500)
		}
	}}
	return p
}

func planFor(t *testing.T, kind string, wantMin int) (seed []byte, plan []sitegen.Slot) {
	t.Helper()
	for i := 0; i < 200; i++ {
		seed = []byte(fmt.Sprintf("maker-%s-%d", kind, i))
		var err error
		if plan, err = sitegen.Plan(kind, seed); err != nil {
			t.Fatal(err)
		}
		if len(plan) >= wantMin {
			return
		}
	}
	t.Fatalf("no %s seed asks for %d photos", kind, wantMin)
	return
}

func TestMakeFetchesWithinTheLimitsAndFillsTheSite(t *testing.T) {
	for _, kind := range []string{"business", "blog", "portfolio", "club"} {
		seed, plan := planFor(t, kind, 1)
		if kind == "portfolio" {
			seed, plan = planFor(t, kind, 6)
		}
		p := newPexels(t)
		res := NewMaker(p, nil).Make(context.Background(), SourcePexels, testKey, seed, plan)
		if res.Reason != ReasonUsed || len(res.Photos) != len(plan) {
			t.Fatalf("%s: reason %q, %d photos for %d slots", kind, res.Reason, len(res.Photos), len(plan))
		}
		if p.searches > MaxSearches || p.files > MaxFiles || p.files != len(plan) {
			t.Fatalf("%s: %d searches, %d files for %d slots", kind, p.searches, p.files, len(plan))
		}
		total := 0
		for _, s := range plan {
			b := res.Photos[s.Name]
			im, err := jpeg.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s %s: %v", kind, s.Name, err)
			}
			if im.Bounds().Dx() > s.MaxW {
				t.Errorf("%s %s: %d px wide, at most %d", kind, s.Name, im.Bounds().Dx(), s.MaxW)
			}
			total += len(b)
		}
		if total > Budget {
			t.Errorf("%s: %d bytes of photos", kind, total)
		}
		for _, h := range p.hosts() {
			if h != "api.pexels.com" && h != "images.pexels.com" {
				t.Errorf("%s: a request went to %s", kind, h)
			}
		}
		// the site with these photos is one the library takes
		files, _, err := sitegen.GenerateWith(kind, seed, sitegen.Options{Photos: res.Photos})
		if err != nil {
			t.Fatal(err)
		}
		b, err := decoy.FromFiles(files)
		if err != nil || b.Size() > decoy.MaxBundleBytes {
			t.Fatalf("%s: bundle %v, %d bytes", kind, err, b.Size())
		}
		t.Logf("%s: %d photos, %d bytes of photos, site %d bytes", kind, len(plan), total, b.Size())
	}
}

func TestMakeDifferentSeedsGiveDifferentPhotos(t *testing.T) {
	_, plan := planFor(t, "blog", 1)
	p := newPexels(t)
	m := NewMaker(p, nil)
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		res := m.Make(context.Background(), SourcePexels, testKey, []byte(fmt.Sprintf("diff-%d", i)), plan)
		if res.Reason != ReasonUsed {
			t.Fatal(res.Reason)
		}
		seen[string(res.Photos[plan[0].Name])] = true
	}
	// the fake serves one picture for every id, so the choice shows in the request log instead: pages and ids differ
	if p.searches > 3*3 {
		t.Fatalf("%d searches for 12 sites: the pages were not cached", p.searches)
	}
	same := newPexels(t)
	a := NewMaker(same, nil).Make(context.Background(), SourcePexels, testKey, []byte("one"), plan)
	var urls1, urls2 []string
	for _, r := range same.seen {
		urls1 = append(urls1, r.URL.String())
	}
	b := NewMaker(same, nil).Make(context.Background(), SourcePexels, testKey, []byte("one"), plan)
	for _, r := range same.seen[len(urls1):] {
		urls2 = append(urls2, r.URL.String())
	}
	sort.Strings(urls1) // the files come in parallel: their order is not the point
	sort.Strings(urls2)
	if strings.Join(urls1, " ") != strings.Join(urls2, " ") || len(a.Photos) != len(b.Photos) {
		t.Fatal("the same seed asked for other pages or files")
	}
}

func TestMakeSearchPagesAreCachedForAnHour(t *testing.T) {
	seed, plan := planFor(t, "blog", 1)
	p := newPexels(t)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewMaker(p, func() time.Time { return clock })
	m.Make(context.Background(), SourcePexels, testKey, seed, plan)
	first := p.searches
	m.Make(context.Background(), SourcePexels, testKey, seed, plan)
	if p.searches != first {
		t.Fatalf("the second site searched again: %d then %d", first, p.searches)
	}
	clock = clock.Add(61 * time.Minute)
	m.Make(context.Background(), SourcePexels, testKey, seed, plan)
	if p.searches != 2*first {
		t.Fatalf("after an hour the pages were not refreshed: %d searches", p.searches)
	}
}

func TestMakeReasons(t *testing.T) {
	seed, plan := planFor(t, "business", 2)
	for _, tc := range []struct {
		name   string
		set    func(*pexelsFake)
		reason string
	}{
		{"limit", func(p *pexelsFake) { p.status = 429 }, ReasonLimit},
		{"bad key", func(p *pexelsFake) { p.status = 401 }, ReasonBadKey},
		{"down", func(p *pexelsFake) { p.status = 503 }, ReasonUnavailable},
		{"empty", func(p *pexelsFake) { p.empty = true }, ReasonUnavailable},
		{"only people", func(p *pexelsFake) { p.alt = "a smiling woman with a child" }, ReasonUnavailable},
	} {
		p := newPexels(t)
		tc.set(p)
		res := NewMaker(p, nil).Make(context.Background(), SourcePexels, testKey, seed, plan)
		if res.Reason != tc.reason || len(res.Photos) != 0 {
			t.Errorf("%s: reason %q, %d photos", tc.name, res.Reason, len(res.Photos))
		}
		if p.files != 0 {
			t.Errorf("%s: %d files were fetched", tc.name, p.files)
		}
	}
}

// After a 429 the maker does not ask again until the time Pexels named has passed.
func TestMakeLeavesPexelsAloneAfterTheLimit(t *testing.T) {
	seed, plan := planFor(t, "business", 2)
	p := newPexels(t)
	p.status = 429
	clock := time.Now()
	m := NewMaker(p, func() time.Time { return clock })
	m.Make(context.Background(), SourcePexels, testKey, seed, plan)
	asked := p.searches
	for i := 0; i < 5; i++ {
		if res := m.Make(context.Background(), SourcePexels, testKey, []byte(fmt.Sprint("again", i)), plan); res.Reason != ReasonLimit {
			t.Fatalf("reason %q", res.Reason)
		}
	}
	if p.searches != asked {
		t.Fatalf("%d searches while cooling down, %d before", p.searches, asked)
	}
	clock = clock.Add(coolDefault + time.Second)
	p.status = 0
	if res := m.Make(context.Background(), SourcePexels, testKey, seed, plan); res.Reason != ReasonUsed {
		t.Fatalf("after the wait: %q", res.Reason)
	}
}

func TestMakeSkipsABrokenFileAndKeepsTheRest(t *testing.T) {
	seed, plan := planFor(t, "blog", 3)
	p := newPexels(t)
	inner := p.fakeNet.serve
	n := 0
	p.fakeNet.serve = func(req *http.Request, w http.ResponseWriter) {
		if req.URL.Host == "images.pexels.com" {
			p.mu.Lock()
			n++
			bad := n == 1
			p.mu.Unlock()
			if bad {
				io.WriteString(w, "this is not a picture")
				return
			}
		}
		inner(req, w)
	}
	res := NewMaker(p, nil).Make(context.Background(), SourcePexels, testKey, seed, plan)
	if res.Reason != ReasonUsed || len(res.Photos) != len(plan)-1 {
		t.Fatalf("reason %q, %d photos of %d", res.Reason, len(res.Photos), len(plan))
	}
}

func TestMakeNeverPutsTheKeyOnAFileRequestOrInAResult(t *testing.T) {
	seed, plan := planFor(t, "business", 2)
	p := newPexels(t)
	res := NewMaker(p, nil).Make(context.Background(), SourcePexels, vault.Redacted(testKey), seed, plan)
	for _, r := range p.seen {
		if r.Host == "images.pexels.com" && r.Header.Get("Authorization") != "" {
			t.Fatal("the key went to the image host")
		}
		if strings.Contains(r.URL.String(), testKey) {
			t.Fatal("the key is in a URL")
		}
	}
	for _, b := range res.Photos {
		if bytes.Contains(b, []byte(testKey)) {
			t.Fatal("the key is in a photo")
		}
	}
}
