//go:build !js

package decoyphotos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/decoy/sitegen"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// pixabayFake is the only Pixabay these tests know, and a strict one: no key is a 400, a wrong key is a 400, past the limit is a 429
// with the rate-limit headers. It serves search pages from /api/ on pixabay.com and picture files from pixabay.com/get and
// cdn.pixabay.com, and counts them.
type pixabayFake struct {
	*fakeNet
	searches, files int
	limitAfter      int // a 429 for every search after this many (0 = never)
	status          int // the answer to every search when not 0
	empty           bool
	tags            string
	// the sizes of the pictures the answer describes
	webW, webH, origW, origH int
	filePaths                []string
}

func newPixabay(t *testing.T, key string) *pixabayFake {
	p := &pixabayFake{tags: "bread, loaf, table", webW: 640, webH: 427, origW: 4000, origH: 2667}
	jpg := stub(t, 1600, 1100, false)
	p.fakeNet = &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case req.URL.Host == "pixabay.com" && req.URL.Path == "/api/":
			p.searches++
			got := req.URL.Query().Get("key")
			switch {
			case got == "":
				http.Error(w, `[ERROR 400] "key" is missing`, http.StatusBadRequest)
				return
			case got != key:
				http.Error(w, `[ERROR 400] Invalid API key`, http.StatusBadRequest)
				return
			case p.limitAfter > 0 && p.searches > p.limitAfter:
				w.Header().Set("X-RateLimit-Limit", "100")
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", "42")
				http.Error(w, "API rate limit exceeded", http.StatusTooManyRequests)
				return
			case p.status != 0:
				w.WriteHeader(p.status)
				return
			case p.empty:
				fmt.Fprint(w, `{"total":0,"totalHits":0,"hits":[]}`)
				return
			}
			w.Header().Set("X-RateLimit-Limit", "100")
			w.Header().Set("X-RateLimit-Remaining", "99")
			w.Header().Set("X-RateLimit-Reset", "60")
			pg, _ := strconv.Atoi(req.URL.Query().Get("page"))
			var hits []map[string]any
			for i := 1; i <= 30; i++ {
				id := pg*1000 + i
				hits = append(hits, map[string]any{
					"id": id, "type": "photo", "tags": p.tags,
					"webformatURL": fmt.Sprintf("https://pixabay.com/get/w%d_640.jpg", id), "webformatWidth": p.webW, "webformatHeight": p.webH,
					"largeImageURL": fmt.Sprintf("https://cdn.pixabay.com/photo/l%d_1280.jpg", id), "imageWidth": p.origW, "imageHeight": p.origH,
				})
			}
			json.NewEncoder(w).Encode(map[string]any{"total": 500, "totalHits": 500, "hits": hits})
		case req.URL.Host == "pixabay.com" && strings.HasPrefix(req.URL.Path, "/get/"), req.URL.Host == "cdn.pixabay.com":
			p.files++
			p.filePaths = append(p.filePaths, req.URL.Path)
			w.Write(jpg)
		default:
			w.WriteHeader(500)
		}
	}}
	return p
}

const pixabayKey = "12345678-0123456789abcdef01234567"

func TestPixabaySearchAsksTheRightWayAndKeepsOnlyItsOwnHosts(t *testing.T) {
	var got *http.Request
	n := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		got = req
		fmt.Fprint(w, `{"total":6,"totalHits":6,"hits":[
			{"id":1,"type":"photo","tags":"ok","webformatURL":"https://pixabay.com/get/a_640.jpg","webformatWidth":640,"webformatHeight":427,"largeImageURL":"https://pixabay.com/get/a_1280.jpg","imageWidth":4000,"imageHeight":2667},
			{"id":2,"type":"photo","tags":"evil host","webformatURL":"https://evil.example/a_640.jpg","webformatWidth":640,"webformatHeight":427},
			{"id":3,"type":"photo","tags":"plain http","webformatURL":"http://pixabay.com/get/a_640.jpg","webformatWidth":640,"webformatHeight":427},
			{"id":4,"type":"photo","tags":"userinfo","webformatURL":"https://pixabay.com@evil.example/a_640.jpg","webformatWidth":640,"webformatHeight":427},
			{"id":5,"type":"illustration","tags":"not a photo","webformatURL":"https://cdn.pixabay.com/a_640.jpg","webformatWidth":640,"webformatHeight":427},
			{"id":6,"type":"photo","tags":"no size","webformatURL":"https://cdn.pixabay.com/b_640.jpg"},
			{"id":7,"type":"photo","tags":"small original, cdn","webformatURL":"https://cdn.pixabay.com/c_640.jpg","webformatWidth":640,"webformatHeight":360,"largeImageURL":"https://cdn.pixabay.com/c_1280.jpg","imageWidth":900,"imageHeight":506},
			{"id":8,"type":"photo","tags":"no sizes of the original","webformatURL":"https://cdn.pixabay.com/d_640.jpg","webformatWidth":427,"webformatHeight":640,"largeImageURL":"https://cdn.pixabay.com/d_1280.jpg"},
			{"id":9,"type":"photo","tags":"no large file","webformatURL":"https://cdn.pixabay.com/e_640.jpg","webformatWidth":640,"webformatHeight":427,"largeImageURL":"https://evil.example/e_1280.jpg"}]}`)
	}}
	res, err := NewPixabay(n).Search(context.Background(), pixabayKey, "sourdough loaf", false, 2, 30)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, p := range res {
		ids = append(ids, p.ID)
	}
	if fmt.Sprint(ids) != "[1 7 8 9]" {
		t.Fatalf("results = %v", ids)
	}
	if p := res[0]; p.Text != "ok" || p.Small != "https://pixabay.com/get/a_640.jpg" || p.SmallW != 640 || p.SmallH != 427 ||
		p.Big != "https://pixabay.com/get/a_1280.jpg" || p.BigW != 1280 || p.BigH != 853 { // 4000x2667 cut to 1280 on its long side
		t.Errorf("a big original: %+v", p)
	}
	if p := res[1]; p.BigW != 900 || p.BigH != 506 { // an original smaller than 1280 stays what it is
		t.Errorf("a small original: %+v", p)
	}
	if p := res[2]; p.BigW != 854 || p.BigH != 1280 { // no size of the original: twice the 640 file
		t.Errorf("no original size: %+v", p)
	}
	if p := res[3]; p.Big != p.Small || p.BigW != 640 { // a large file that is not Pixabay's is left out: the small one stands in
		t.Errorf("a refused large file: %+v", p)
	}
	q := got.URL.Query()
	if got.URL.Scheme != "https" || got.URL.Host != "pixabay.com" || got.URL.Path != "/api/" || q.Get("key") != pixabayKey ||
		q.Get("q") != "sourdough loaf" || q.Get("image_type") != "photo" || q.Get("orientation") != "horizontal" ||
		q.Get("safesearch") != "true" || q.Get("per_page") != "30" || q.Get("page") != "2" {
		t.Fatalf("request = %s", strings.ReplaceAll(got.URL.String(), pixabayKey, "KEY"))
	}
	if got.Header.Get("Authorization") != "" || !strings.HasPrefix(got.Header.Get("User-Agent"), "mistgate-panel") {
		t.Fatalf("headers = %v", got.Header)
	}
	if _, err := NewPixabay(n).Search(context.Background(), pixabayKey, "x", true, 1, 1); err != nil ||
		got.URL.Query().Get("orientation") != "vertical" || got.URL.Query().Get("per_page") != "3" {
		t.Fatalf("portrait or per_page: %v %s", err, strings.ReplaceAll(got.URL.String(), pixabayKey, "KEY"))
	}
	long := strings.Repeat("ж", 150)
	if _, err := NewPixabay(n).Search(context.Background(), pixabayKey, long, false, 1, 30); err != nil || len([]rune(got.URL.Query().Get("q"))) != 100 {
		t.Fatalf("a search text over 100 characters was sent whole: %v", err)
	}
}

func TestPixabayStrictFakeAnswersAsPixabayDoes(t *testing.T) {
	p := newPixabay(t, pixabayKey)
	c := NewPixabay(p)
	// no key at all, a wrong key: the client says "key"
	for _, k := range []string{"", "wrong-key-0000"} {
		if _, err := c.Search(context.Background(), vaultKey(k), "x", false, 1, 5); CodeOf(err) != CodeKey {
			t.Errorf("key %q: %v", k, err)
		}
	}
	if res, err := c.Search(context.Background(), pixabayKey, "x", false, 1, 5); err != nil || len(res) != 30 {
		t.Fatalf("the right key: %d %v", len(res), err)
	}
	// 401 and 403 are a refused key too; another 400 is not
	for status, code := range map[int]string{401: CodeKey, 403: CodeKey, 500: CodeUnavailable, 404: CodeUnavailable} {
		p.status = status
		if _, err := c.Search(context.Background(), pixabayKey, "x", false, 1, 5); CodeOf(err) != code {
			t.Errorf("status %d: %v", status, err)
		}
	}
	p.status = 400
	if _, err := c.Search(context.Background(), pixabayKey, "x", false, 1, 5); CodeOf(err) != CodeUnavailable {
		t.Errorf("a 400 with no word about the key: %v", err)
	}
}

func TestPixabayLimitSaysHowLongToWait(t *testing.T) {
	p := newPixabay(t, pixabayKey)
	p.limitAfter = 1
	c := NewPixabay(p)
	if _, err := c.Search(context.Background(), pixabayKey, "x", false, 1, 5); err != nil {
		t.Fatal(err)
	}
	_, err := c.Search(context.Background(), pixabayKey, "x", false, 1, 5)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeLimit || e.RetryAfter != 42*time.Second {
		t.Fatalf("over the limit: %v", err)
	}
}

// The key is in the URL of a search, so nothing that talks about a request may print it: not an error of the transport (net/http
// puts the URL into those), not a refused redirect, not a timeout.
func TestPixabayKeyNeverLeavesInAnError(t *testing.T) {
	leaky := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: lookup failed for %s", req.URL)
	})
	_, err := NewPixabay(leaky).Search(context.Background(), pixabayKey, "x", false, 1, 5)
	if CodeOf(err) != CodeUnavailable || strings.Contains(err.Error(), pixabayKey) || strings.Contains(fmt.Sprintf("%+v %v %#v", err, err, err), pixabayKey) {
		t.Fatalf("a failing transport: %v", err)
	}
	// a key with odd bytes in it
	if _, err := NewPixabay(leaky).Search(context.Background(), "bad\x7fkey"+pixabayKey, "x", false, 1, 5); err == nil || strings.Contains(err.Error(), pixabayKey) {
		t.Fatalf("a request that cannot be built: %v", err)
	}
	// every status, with the key echoed in the answer's text
	for _, status := range []int{400, 401, 403, 404, 429, 500, 502} {
		n := &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) {
			w.WriteHeader(status)
			fmt.Fprint(w, `invalid key `+pixabayKey)
		}}
		_, err := NewPixabay(n).Search(context.Background(), pixabayKey, "x", false, 1, 5)
		if err == nil || strings.Contains(fmt.Sprintf("%+v %v", err, err), pixabayKey) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	// a redirect to a foreign host is not followed, and says nothing of where
	n := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		http.Redirect(w, req, "https://evil.example/?key="+pixabayKey, http.StatusFound)
	}}
	_, err = NewPixabay(n).Search(context.Background(), pixabayKey, "x", false, 1, 5)
	if CodeOf(err) != CodeUnavailable || len(n.seen) != 1 || strings.Contains(err.Error(), "evil") {
		t.Fatalf("redirect: %v (%d requests)", err, len(n.seen))
	}
	// a cancelled or a timed-out search
	old := searchTimeout
	searchTimeout = 30 * time.Millisecond
	defer func() { searchTimeout = old }()
	_, err = NewPixabay(blocking{}).Search(context.Background(), pixabayKey, "x", false, 1, 5)
	if err == nil || strings.Contains(err.Error(), pixabayKey) {
		t.Fatalf("a timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = NewPixabay(blocking{}).Search(ctx, pixabayKey, "x", false, 1, 5); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), pixabayKey) {
		t.Fatalf("a cancel: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPixabayRedirectsAndDownloads(t *testing.T) {
	for _, to := range []string{"https://evil.example/steal", "http://cdn.pixabay.com/a", "https://cdn.pixabay.com:444/a", "https://user@cdn.pixabay.com/a"} {
		n := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
			if req.URL.Path == "/get/start.jpg" {
				http.Redirect(w, req, to, http.StatusFound)
				return
			}
			w.Write([]byte("DATA"))
		}}
		if _, err := NewPixabay(n).Download(context.Background(), "https://pixabay.com/get/start.jpg"); CodeOf(err) != CodeUnavailable || len(n.seen) != 1 {
			t.Fatalf("redirect to %s: %v after %d requests", to, err, len(n.seen))
		}
	}
	ok := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		if req.URL.Host == "cdn.pixabay.com" {
			w.Write([]byte("DATA"))
			return
		}
		http.Redirect(w, req, "https://cdn.pixabay.com/a", http.StatusFound)
	}}
	if b, err := NewPixabay(ok).Download(context.Background(), "https://pixabay.com/get/start.jpg"); err != nil || string(b) != "DATA" {
		t.Fatalf("a redirect to the cdn: %q %v", b, err)
	}
	// only Pixabay's own files: not Pexels', not another host, not http; and no key goes along
	n := &fakeNet{serve: func(_ *http.Request, w http.ResponseWriter) { w.Write([]byte("x")) }}
	c := NewPixabay(n)
	for _, u := range []string{"https://evil.example/a.jpg", "http://pixabay.com/a.jpg", "https://images.pexels.com/a.jpg", "https://pixabay.com:1/a", "https://www.pixabay.com/a", ""} {
		if _, err := c.Download(context.Background(), u); CodeOf(err) != CodeUnavailable {
			t.Errorf("Download(%q) = %v", u, err)
		}
	}
	if len(n.seen) != 0 {
		t.Fatalf("a refused URL was requested: %v", n.hosts())
	}
	if _, err := c.Download(context.Background(), "https://cdn.pixabay.com/a.jpg"); err != nil || n.seen[0].Header.Get("Authorization") != "" || n.seen[0].URL.RawQuery != "" {
		t.Fatalf("download: %v %v", err, n.seen)
	}
	// Pexels' client does not take Pixabay's files either
	if _, err := New(n).Download(context.Background(), "https://cdn.pixabay.com/a.jpg"); CodeOf(err) != CodeUnavailable {
		t.Fatalf("Pexels' client fetched a Pixabay file: %v", err)
	}
}

func TestPixabayMakeFillsTheSiteFromItsOwnHostsOnly(t *testing.T) {
	for _, kind := range []string{"business", "blog", "portfolio", "club"} {
		seed, plan := planFor(t, kind, 1)
		if kind == "portfolio" {
			seed, plan = planFor(t, kind, 6)
		}
		p := newPixabay(t, pixabayKey)
		res := NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, seed, plan)
		if res.Reason != ReasonUsed || len(res.Photos) != len(plan) {
			t.Fatalf("%s: reason %q, %d photos for %d slots", kind, res.Reason, len(res.Photos), len(plan))
		}
		if p.searches > MaxSearches || p.files != len(plan) {
			t.Fatalf("%s: %d searches, %d files for %d slots", kind, p.searches, p.files, len(plan))
		}
		for _, s := range plan {
			im, err := jpeg.Decode(bytes.NewReader(res.Photos[s.Name]))
			if err != nil || im.Bounds().Dx() > s.MaxW {
				t.Errorf("%s %s: %v, %d px wide, at most %d", kind, s.Name, err, im.Bounds().Dx(), s.MaxW)
			}
		}
		for _, h := range p.hosts() {
			if h != "pixabay.com" && h != "cdn.pixabay.com" {
				t.Errorf("%s: a request went to %s", kind, h)
			}
		}
		for _, r := range p.seen {
			if r.URL.Path != "/api/" && (r.URL.RawQuery != "" || r.Header.Get("Authorization") != "") {
				t.Errorf("%s: the key may have gone with a file request: %s", kind, r.URL.Path)
			}
		}
		if _, _, err := sitegen.GenerateWith(kind, seed, sitegen.Options{Photos: res.Photos}); err != nil {
			t.Fatal(err)
		}
	}
}

// The same seed and the same source choose the same photos; another seed another ones.
func TestPixabayMakeSameSeedSamePhotos(t *testing.T) {
	_, plan := planFor(t, "blog", 2)
	asked := func(seed string) string {
		p := newPixabay(t, pixabayKey)
		NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, []byte(seed), plan)
		var urls []string
		for _, r := range p.seen {
			urls = append(urls, strings.ReplaceAll(r.URL.String(), pixabayKey, "KEY"))
		}
		sort.Strings(urls) // the files come in parallel
		return strings.Join(urls, " ")
	}
	if asked("one") != asked("one") {
		t.Fatal("the same seed asked for other pages or files")
	}
	if asked("one") == asked("two") {
		t.Fatal("two seeds chose the same pages and files")
	}
}

// Pixabay asks that its answers are cached for 24 hours: a second site, and one a day later but not yet 24 hours, cost no search.
func TestPixabayMakeCachesSearchesForADay(t *testing.T) {
	seed, plan := planFor(t, "blog", 1)
	p := newPixabay(t, pixabayKey)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewMaker(p, func() time.Time { return clock })
	m.Make(context.Background(), SourcePixabay, pixabayKey, seed, plan)
	first := p.searches
	clock = clock.Add(23*time.Hour + 50*time.Minute)
	m.Make(context.Background(), SourcePixabay, pixabayKey, seed, plan)
	if p.searches != first {
		t.Fatalf("within 24 hours the second site searched again: %d then %d", first, p.searches)
	}
	clock = clock.Add(20 * time.Minute)
	m.Make(context.Background(), SourcePixabay, pixabayKey, seed, plan)
	if p.searches != 2*first {
		t.Fatalf("after 24 hours the pages were not asked again: %d then %d", first, p.searches)
	}
}

// Each source has its own cache and its own cooldown: Pexels' pages are not Pixabay's, and a limit of one does not stop the other.
func TestMakerSourcesDoNotShareCachesOrCooldowns(t *testing.T) {
	seed, plan := planFor(t, "business", 2)
	px, pb := newPexels(t), newPixabay(t, pixabayKey)
	both := &fakeNet{serve: func(req *http.Request, w http.ResponseWriter) {
		if strings.HasSuffix(req.URL.Host, "pixabay.com") {
			pb.serve(req, w)
		} else {
			px.serve(req, w)
		}
	}}
	clock := time.Now()
	m := NewMaker(both, func() time.Time { return clock })
	px.status = 429
	if res := m.Make(context.Background(), SourcePexels, testKey, seed, plan); res.Reason != ReasonLimit {
		t.Fatalf("pexels: %q", res.Reason)
	}
	if res := m.Make(context.Background(), SourcePixabay, pixabayKey, seed, plan); res.Reason != ReasonUsed {
		t.Fatalf("pixabay while pexels cools down: %q", res.Reason)
	}
	px.status = 0
	if res := m.Make(context.Background(), SourcePexels, testKey, seed, plan); res.Reason != ReasonLimit {
		t.Fatalf("pexels still cools down: %q", res.Reason)
	}
	if res := m.Make(context.Background(), "flickr", testKey, seed, plan); res.Reason != ReasonUnavailable || len(res.Photos) != 0 {
		t.Fatalf("an unknown source: %q", res.Reason)
	}
}

func TestPixabayMakeReasonsAndTheRefusedSourceStaysRefused(t *testing.T) {
	seed, plan := planFor(t, "business", 2)
	for _, tc := range []struct {
		name   string
		key    string
		set    func(*pixabayFake)
		reason string
	}{
		{"limit", pixabayKey, func(p *pixabayFake) { p.limitAfter = 1; p.searches = 1 }, ReasonLimit},
		{"wrong key", "another-key-0000000000", func(*pixabayFake) {}, ReasonBadKey},
		{"no key", "", func(*pixabayFake) {}, ReasonBadKey},
		{"down", pixabayKey, func(p *pixabayFake) { p.status = 503 }, ReasonUnavailable},
		{"empty", pixabayKey, func(p *pixabayFake) { p.empty = true }, ReasonUnavailable},
	} {
		p := newPixabay(t, pixabayKey)
		tc.set(p)
		res := NewMaker(p, nil).Make(context.Background(), SourcePixabay, vaultKey(tc.key), seed, plan)
		if res.Reason != tc.reason || len(res.Photos) != 0 || p.files != 0 {
			t.Errorf("%s: reason %q, %d photos, %d files", tc.name, res.Reason, len(res.Photos), p.files)
		}
		for _, h := range p.hosts() {
			if !strings.HasSuffix(h, "pixabay.com") {
				t.Errorf("%s: after the refusal a request went to %s", tc.name, h)
			}
		}
	}
}

// The people filter reads Pixabay's tags.
func TestPixabayMakeDropsPhotosWhoseTagsShowPeople(t *testing.T) {
	seed, plan := planFor(t, "business", 2)
	for _, tags := range []string{"woman, smile, portrait", "Man, hat", "family, kids", "chef, kitchen"} {
		p := newPixabay(t, pixabayKey)
		p.tags = tags
		if res := NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, seed, plan); len(res.Photos) != 0 || res.Reason != ReasonUnavailable || p.files != 0 {
			t.Errorf("tags %q: reason %q, %d photos, %d files", tags, res.Reason, len(res.Photos), p.files)
		}
	}
	p := newPixabay(t, pixabayKey)
	p.tags = "manhattan, mandarin, bread" // words that only contain "man"
	if res := NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, seed, plan); res.Reason != ReasonUsed {
		t.Errorf("harmless tags: %q", res.Reason)
	}
}

// A slot wider than 1280 takes the 1280 file and comes out 1280 wide, never wider; one the file is too small for stays drawn.
func TestPixabayMakeNeverEnlargesAndLeavesAnUnsharpSlotDrawn(t *testing.T) {
	wide := sitegen.Slot{Name: "hero", Query: "bread", W: 1600, H: 600, MaxW: 1280}
	portrait := sitegen.Slot{Name: "g1", Query: "bread", W: 480, H: 640, MaxW: 640}
	p := newPixabay(t, pixabayKey)
	res := NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, []byte("s"), []sitegen.Slot{wide})
	if res.Reason != ReasonUsed {
		t.Fatalf("a wide slot: %q", res.Reason)
	}
	if im, err := jpeg.Decode(bytes.NewReader(res.Photos["hero"])); err != nil || im.Bounds().Dx() != 1280 || im.Bounds().Dy() != 480 {
		t.Fatalf("hero: %v %v", err, im.Bounds())
	}
	if len(p.filePaths) != 1 || !strings.HasSuffix(p.filePaths[0], "_1280.jpg") {
		t.Fatalf("files %v: a slot over 640 takes the 1280 file", p.filePaths)
	}

	// a portrait slot out of landscape 640 px files: the 640 file cut to the slot would be 320 px wide, so the 1280 file is taken
	p = newPixabay(t, pixabayKey)
	res = NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, []byte("s"), []sitegen.Slot{portrait})
	if res.Reason != ReasonUsed || len(p.filePaths) != 1 || !strings.HasSuffix(p.filePaths[0], "_1280.jpg") {
		t.Fatalf("portrait slot: %q %v", res.Reason, p.filePaths)
	}
	// a landscape slot out of the 640 files is the 640 one
	p = newPixabay(t, pixabayKey)
	NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, []byte("s"), []sitegen.Slot{{Name: "t1", Query: "bread", W: 640, H: 420, MaxW: 640}})
	if len(p.filePaths) != 1 || !strings.HasSuffix(p.filePaths[0], "_640.jpg") {
		t.Fatalf("a small slot: %v", p.filePaths)
	}

	// originals of 800 px: too small for a 1200 px hero, which stays drawn without a file being fetched; the 640 slot still gets one
	p = newPixabay(t, pixabayKey)
	p.origW, p.origH = 800, 533
	res = NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, []byte("s"), []sitegen.Slot{
		{Name: "hero", Query: "bread", W: 1200, H: 620, MaxW: 1280}, {Name: "t1", Query: "bread", W: 640, H: 420, MaxW: 640}})
	if res.Reason != ReasonUsed || len(res.Photos) != 1 || res.Photos["t1"] == nil || res.Photos["hero"] != nil || p.files != 1 {
		t.Fatalf("a small original: %q, %d photos, %d files", res.Reason, len(res.Photos), p.files)
	}
	// nothing sharp for any slot: no file at all, and the reason is the plain "unavailable"
	p = newPixabay(t, pixabayKey)
	p.origW, p.origH = 800, 533
	res = NewMaker(p, nil).Make(context.Background(), SourcePixabay, pixabayKey, []byte("s"), []sitegen.Slot{{Name: "hero", Query: "bread", W: 1200, H: 620, MaxW: 1280}})
	if res.Reason != ReasonUnavailable || len(res.Photos) != 0 || p.files != 0 {
		t.Fatalf("only an unsharp slot: %q, %d files", res.Reason, p.files)
	}
}

func TestFileFor(t *testing.T) {
	small := Photo{Small: "s", SmallW: 640, SmallH: 427, Big: "b", BigW: 1280, BigH: 853}
	for _, tc := range []struct {
		slot sitegen.Slot
		want string
	}{
		{sitegen.Slot{W: 640, H: 420, MaxW: 640}, "s"},
		{sitegen.Slot{W: 320, H: 320, MaxW: 640}, "s"},   // 427 px for 320
		{sitegen.Slot{W: 480, H: 640, MaxW: 640}, "b"},   // 320 px of the small one is under 4/5 of 480
		{sitegen.Slot{W: 1200, H: 620, MaxW: 1280}, "b"}, // wider than 640: the big one
		{sitegen.Slot{W: 1600, H: 600, MaxW: 1280}, "b"}, // wider than 1280: the same file, never enlarged
		{sitegen.Slot{W: 600, H: 800, MaxW: 640}, "b"},   // 320 px of the small one is under 4/5 of 600
		{sitegen.Slot{W: 480, H: 1200, MaxW: 640}, ""},   // 1280x853 cut to 2:5 is 341 px, under 4/5 of 480
		{sitegen.Slot{W: 2560, H: 800, MaxW: 1280}, "b"}, // 1280 px for 1280
	} {
		if got := fileFor(small, tc.slot); got != tc.want {
			t.Errorf("slot %+v: %q, want %q", tc.slot, got, tc.want)
		}
	}
	// Pexels says no sizes: the old rule (the smaller file up to 640, else the bigger) with no check
	px := Photo{Small: "s", Big: "b"}
	if fileFor(px, sitegen.Slot{W: 480, H: 1200, MaxW: 640}) != "s" || fileFor(px, sitegen.Slot{W: 1200, H: 620, MaxW: 1280}) != "b" {
		t.Error("a photo of unknown size is not trusted")
	}
}

func vaultKey(s string) vault.Redacted { return vault.Redacted(s) }
