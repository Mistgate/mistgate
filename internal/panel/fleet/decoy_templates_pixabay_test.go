//go:build !js

package fleet

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
)

const pixabayTestKey = "pixabay-test-key-0123456789ab"

// photoWorld is the only Pexels and the only Pixabay there are in these tests: a RoundTripper, so no packet leaves the test. Pexels is
// the fakePexels of the older tests; Pixabay is strict: no key is a 400, a wrong key is a 400, a file request must carry no key.
type photoWorld struct {
	px *fakePexels

	mu                       sync.Mutex
	searches, files          int
	status                   int  // the answer to every Pixabay search when not 0
	netErr                   bool // a Pixabay search fails the way net/http fails: with its URL in the error
	other                    []string
	keyLeak                  bool // the Pixabay key was seen anywhere but in the query of a search
	sawKey, sawFileWithQuery bool
}

func newPhotoWorld(t *testing.T) *photoWorld { return &photoWorld{px: newFakePexels(t)} }

func (w *photoWorld) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Host {
	case "api.pexels.com", "images.pexels.com":
		return w.px.RoundTrip(req)
	case "pixabay.com", "cdn.pixabay.com":
	default:
		w.mu.Lock()
		w.other = append(w.other, req.URL.Host)
		w.mu.Unlock()
		return nil, fmt.Errorf("no such host %s", req.URL.Host)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	search := req.URL.Host == "pixabay.com" && req.URL.Path == "/api/"
	if !search && (strings.Contains(req.URL.String(), pixabayTestKey) || req.URL.RawQuery != "") {
		w.keyLeak, w.sawFileWithQuery = true, true
	}
	for _, v := range req.Header {
		if strings.Contains(strings.Join(v, ","), pixabayTestKey) {
			w.keyLeak = true
		}
	}
	rec := httptest.NewRecorder()
	if search {
		w.searches++
		switch got := req.URL.Query().Get("key"); {
		case w.netErr:
			return nil, fmt.Errorf("Get %q: dial tcp: i/o timeout", req.URL.String())
		case got == "":
			http.Error(rec, `[ERROR 400] "key" is missing`, http.StatusBadRequest)
		case got != pixabayTestKey:
			http.Error(rec, `[ERROR 400] Invalid API key`, http.StatusBadRequest)
		case w.status != 0:
			rec.Header().Set("X-RateLimit-Reset", "30")
			rec.WriteHeader(w.status)
		default:
			w.sawKey = true
			pg, _ := strconv.Atoi(req.URL.Query().Get("page"))
			var hits []map[string]any
			for i := 1; i <= 30; i++ {
				id := pg*1000 + i
				hits = append(hits, map[string]any{
					"id": id, "type": "photo", "tags": "wooden, table",
					"webformatURL": fmt.Sprintf("https://pixabay.com/get/w%d_640.jpg", id), "webformatWidth": 640, "webformatHeight": 427,
					"largeImageURL": fmt.Sprintf("https://cdn.pixabay.com/photo/l%d_1280.jpg", id), "imageWidth": 4000, "imageHeight": 2667,
				})
			}
			json.NewEncoder(rec).Encode(map[string]any{"total": 500, "totalHits": 500, "hits": hits})
		}
	} else {
		w.files++
		rec.Write(w.px.jpg)
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (w *photoWorld) pixabay() (searches, files int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.searches, w.files
}

func worldEnv(t *testing.T) (*env, decoySiteService, *photoWorld) {
	e := newEnv(t)
	w := newPhotoWorld(t)
	e.f.photoTransport = w
	return e, decoySiteService{e.f}, w
}

func setSourceKey(t *testing.T, e *env, svc decoySiteService, source, key string) *decoysitev1.SetDecoyPhotosKeyResponse {
	t.Helper()
	got, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: key, Source: source}))
	if err != nil {
		t.Fatal(err)
	}
	return got.Msg
}

func photosState(t *testing.T, e *env, svc decoySiteService) *decoysitev1.GetDecoyPhotosResponse {
	t.Helper()
	got, err := svc.GetDecoyPhotos(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPhotosRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return got.Msg
}

func pickSource(e *env, svc decoySiteService, source string) error {
	_, err := svc.SetDecoyPhotosSource(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosSourceRequest{Source: source}))
	return err
}

func TestTwoPhotoKeysAreSealedAndTheSourceFollowsTheKeyStoredLast(t *testing.T) {
	e, svc, _ := worldEnv(t)
	if s := photosState(t, e, svc); s.HasKey || s.HasPixabayKey || s.Source != "pixabay" {
		t.Fatalf("nothing stored: %+v", s)
	}
	got := setSourceKey(t, e, svc, "pixabay", "  "+pixabayTestKey+" \n")
	if got.HasKey || !got.HasPixabayKey || got.Source != "pixabay" {
		t.Fatalf("a Pixabay key: %+v", got)
	}
	if k := e.f.photoKey(e.ctx, "pixabay"); k.Reveal() != pixabayTestKey || e.f.photoKey(e.ctx, "pexels") != "" {
		t.Fatal("the stored keys are not the ones that were set")
	}
	raw, _ := e.st.Setting(e.ctx, pixabayKeySetting)
	if raw == "" || strings.Contains(raw, pixabayTestKey) {
		t.Fatalf("the Pixabay key is stored as %q", raw)
	}
	// a Pexels key now: the one stored last is the one in use
	got = setSourceKey(t, e, svc, "pexels", pexelsTestKey)
	if !got.HasKey || !got.HasPixabayKey || got.Source != "pexels" {
		t.Fatalf("both keys: %+v", got)
	}
	// the owner picks one of the two
	if err := pickSource(e, svc, "pixabay"); err != nil || photosState(t, e, svc).Source != "pixabay" {
		t.Fatalf("pick pixabay: %v", err)
	}
	if err := pickSource(e, svc, "pexels"); err != nil || photosState(t, e, svc).Source != "pexels" {
		t.Fatalf("pick pexels: %v", err)
	}
	// refusals: a source that is not one, a source with no key
	if err := pickSource(e, svc, "flickr"); refusalOf(err) != "invalid_argument decoy_photos_source_invalid" {
		t.Fatalf("an unknown source: %v", err)
	}
	if _, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: pixabayTestKey, Source: "flickr"})); refusalOf(err) != "invalid_argument decoy_photos_source_invalid" {
		t.Fatalf("a key for an unknown source: %v", err)
	}
	if _, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: "short", Source: "pixabay"})); refusalOf(err) != "invalid_argument decoy_photos_key_invalid" {
		t.Fatalf("a short Pixabay key: %v", err)
	}
	if s := photosState(t, e, svc); !s.HasPixabayKey || !s.HasKey || s.Source != "pexels" {
		t.Fatalf("a refusal changed the state: %+v", s)
	}
	// removing the key of the source in use passes the use to the other one; removing the last leaves the pick where it was
	got = setSourceKey(t, e, svc, "pexels", "")
	if got.HasKey || !got.HasPixabayKey || got.Source != "pixabay" {
		t.Fatalf("Pexels key removed: %+v", got)
	}
	if err := pickSource(e, svc, "pexels"); refusalOf(err) != "failed_precondition decoy_photos_no_key" {
		t.Fatalf("picking a source with no key: %v", err)
	}
	got = setSourceKey(t, e, svc, "pixabay", "")
	if got.HasKey || got.HasPixabayKey || got.Source != "pixabay" {
		t.Fatalf("all keys removed: %+v", got)
	}
	// the audit log says which source, never a key
	rows, err := e.st.ListAudit(e.ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range rows {
		if strings.Contains(r.Params+r.Action+r.Result, pixabayTestKey) || strings.Contains(r.Params+r.Action+r.Result, pexelsTestKey) {
			t.Fatal("a key is in the audit log")
		}
		if strings.HasPrefix(r.Action, "decoy_photos.") {
			seen[r.Action+" "+r.Params]++
		}
	}
	for _, want := range []string{`decoy_photos.key_set {"source":"pixabay"}`, `decoy_photos.key_set {"source":"pexels"}`, `decoy_photos.source_set {"source":"pixabay"}`, `decoy_photos.key_remove {"source":"pexels"}`, `decoy_photos.key_remove {"source":"pixabay"}`} {
		if seen[want] == 0 {
			t.Errorf("no audit event %s: %v", want, seen)
		}
	}
}

// A client from before Pixabay sends no source and knows only has_key: it works as it did, and a panel that has a Pexels key and no
// pick (an upgraded one) goes on using Pexels.
func TestRequestsWithoutASourceAreStillPexels(t *testing.T) {
	e, svc, w := worldEnv(t)
	got, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: pexelsTestKey}))
	if err != nil || !got.Msg.HasKey || got.Msg.HasPixabayKey || got.Msg.Source != "pexels" {
		t.Fatalf("set with no source: %v %+v", err, got)
	}
	if old, _ := svc.GetDecoyPhotos(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPhotosRequest{})); !old.Msg.HasKey {
		t.Fatal("has_key is the old client's whole picture of the key")
	}
	seed := seedWithPhotos(t, "business")
	if p := preview(t, e, svc, "business", seed); p.Photos != "used" || p.PhotosSource != "pexels" {
		t.Fatalf("preview: %q from %q", p.Photos, p.PhotosSource)
	}
	if s, _ := w.pixabay(); s != 0 {
		t.Fatalf("Pixabay was asked %d times by a Pexels-only panel", s)
	}
	if got, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: ""})); err != nil || got.Msg.HasKey {
		t.Fatalf("remove with no source: %v %+v", err, got)
	}

	// a panel upgraded from before Pixabay: its Pexels key is stored and nothing else is
	e2, svc2, w2 := worldEnv(t)
	sealed := e2.f.v.Seal([]byte(pexelsTestKey), photoKeyAAD)
	if err := e2.st.SetSettings(e2.ctx, map[string]string{photoKeySetting: base64.StdEncoding.EncodeToString(sealed)}); err != nil {
		t.Fatal(err)
	}
	if s := photosState(t, e2, svc2); !s.HasKey || s.HasPixabayKey || s.Source != "pexels" {
		t.Fatalf("an upgraded panel: %+v", s)
	}
	if p := preview(t, e2, svc2, "business", seed); p.Photos != "used" || p.PhotosSource != "pexels" {
		t.Fatalf("an upgraded panel's preview: %q from %q", p.Photos, p.PhotosSource)
	}
	if s, _ := w2.pixabay(); s != 0 {
		t.Fatal("an upgraded panel asked Pixabay")
	}
	// and the request an old preview made, with no photos_source, reads as before: the old fields are there
	if p := preview(t, e2, svc2, "docs", ""); p.Photos != "none_for_kind" || p.PhotosSource != "" {
		t.Fatalf("a kind with no photos: %q from %q", p.Photos, p.PhotosSource)
	}
}

func TestPreviewTakesPhotosFromTheSourceInUseOnly(t *testing.T) {
	e, svc, w := worldEnv(t)
	setSourceKey(t, e, svc, "pexels", pexelsTestKey)
	setSourceKey(t, e, svc, "pixabay", pixabayTestKey)
	seed := seedWithPhotos(t, "blog")

	// Pixabay is in use: the key stored last
	a := preview(t, e, svc, "blog", seed)
	if a.Photos != "used" || a.PhotosSource != "pixabay" {
		t.Fatalf("pixabay: %q from %q", a.Photos, a.PhotosSource)
	}
	pbS, pbF := w.pixabay()
	if pbS < 1 || pbS > 3 || pbF < 1 || pbF > 8 {
		t.Fatalf("%d searches and %d files for one site", pbS, pbF)
	}
	if pxS, pxF := w.px.counts(); pxS != 0 || pxF != 0 {
		t.Fatalf("Pexels was asked (%d/%d) while Pixabay was in use", pxS, pxF)
	}
	if len(w.other) != 0 || w.keyLeak || !w.sawKey {
		t.Fatalf("other hosts %v, key leaked %v, key seen %v", w.other, w.keyLeak, w.sawKey)
	}
	if again := preview(t, e, svc, "blog", seed); again.Html != a.Html {
		t.Fatal("the same seed gave another preview")
	}
	if s2, f2 := w.pixabay(); s2 != pbS || f2 != pbF {
		t.Fatalf("the repeat cost Pixabay requests: %d/%d then %d/%d", pbS, pbF, s2, f2)
	}

	// the owner picks Pexels: the same seed is made again, from the other source, and Pixabay is left alone
	if err := pickSource(e, svc, "pexels"); err != nil {
		t.Fatal(err)
	}
	b := preview(t, e, svc, "blog", seed)
	if b.Photos != "used" || b.PhotosSource != "pexels" {
		t.Fatalf("pexels: %q from %q", b.Photos, b.PhotosSource)
	}
	if s2, f2 := w.pixabay(); s2 != pbS || f2 != pbF {
		t.Fatalf("Pixabay was asked while Pexels was in use: %d/%d then %d/%d", pbS, pbF, s2, f2)
	}
	if pxS, pxF := w.px.counts(); pxS < 1 || pxF < 1 {
		t.Fatalf("Pexels was not asked: %d/%d", pxS, pxF)
	}
	// create saves the site that was shown, photos and all, with no new request
	pxS, pxF := w.px.counts()
	made, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "blog", Seed: seed}))
	if err != nil || made.Msg.Photos != "used" {
		t.Fatalf("create: %v %v", made, err)
	}
	if s2, f2 := w.px.counts(); s2 != pxS || f2 != pxF {
		t.Fatal("create asked Pexels again")
	}
}

// When the source in use refuses, the site stays drawn and says why; the other source is not asked in its place.
func TestARefusedSourceIsNeverReplacedByTheOther(t *testing.T) {
	seed := seedWithPhotos(t, "business")
	for _, tc := range []struct {
		name  string
		setup func(e *env, svc decoySiteService, w *photoWorld)
		want  string
		src   string
	}{
		{"pixabay limit", func(_ *env, _ decoySiteService, w *photoWorld) { w.status = 429 }, "limit", "pixabay"},
		{"pixabay down", func(_ *env, _ decoySiteService, w *photoWorld) { w.status = 503 }, "unavailable", "pixabay"},
		{"pixabay wrong key", func(e *env, svc decoySiteService, _ *photoWorld) {
			setSourceKey(t, e, svc, "pixabay", "wrong-key-0123456789")
		}, "bad_key", "pixabay"},
		{"pixabay no answer", func(_ *env, _ decoySiteService, w *photoWorld) { w.netErr = true }, "unavailable", "pixabay"},
	} {
		e, svc, w := worldEnv(t)
		setSourceKey(t, e, svc, "pexels", pexelsTestKey)
		setSourceKey(t, e, svc, "pixabay", pixabayTestKey)
		tc.setup(e, svc, w)
		got := preview(t, e, svc, "business", seed)
		if got.Photos != tc.want || got.PhotosSource != tc.src || got.Html == "" || strings.Contains(got.Html, "data:image/jpeg") {
			t.Errorf("%s: photos %q from %q", tc.name, got.Photos, got.PhotosSource)
		}
		if pxS, pxF := w.px.counts(); pxS != 0 || pxF != 0 {
			t.Errorf("%s: Pexels was asked instead (%d/%d)", tc.name, pxS, pxF)
		}
		made, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "business", Seed: seed, Name: "drawn " + tc.name}))
		if err != nil || made.Msg.Photos != tc.want || made.Msg.Site.FileCount < 5 {
			t.Errorf("%s: create: %v %v", tc.name, made, err)
		}
	}

	// the same the other way round: Pexels refuses, Pixabay is not asked
	e, svc, w := worldEnv(t)
	setSourceKey(t, e, svc, "pixabay", pixabayTestKey)
	setSourceKey(t, e, svc, "pexels", pexelsTestKey)
	w.px.mu.Lock()
	w.px.status = 429
	w.px.mu.Unlock()
	if got := preview(t, e, svc, "business", seed); got.Photos != "limit" || got.PhotosSource != "pexels" {
		t.Errorf("pexels limit: %q from %q", got.Photos, got.PhotosSource)
	}
	if s, f := w.pixabay(); s != 0 || f != 0 {
		t.Errorf("Pixabay was asked instead (%d/%d)", s, f)
	}

	// the pick names a source with no key (a restored backup can leave it so) while the other has one: drawn, "no_key", no request
	e, svc, w = worldEnv(t)
	setSourceKey(t, e, svc, "pexels", pexelsTestKey)
	if err := e.st.SetSettings(e.ctx, map[string]string{photoSourceSetting: "pixabay"}); err != nil {
		t.Fatal(err)
	}
	if s := photosState(t, e, svc); s.Source != "pixabay" || !s.HasKey || s.HasPixabayKey {
		t.Fatalf("state: %+v", s)
	}
	if got := preview(t, e, svc, "business", seed); got.Photos != "no_key" || got.PhotosSource != "pixabay" || got.Html == "" {
		t.Errorf("no key for the picked source: %q from %q", got.Photos, got.PhotosSource)
	}
	if pxS, _ := w.px.counts(); pxS != 0 {
		t.Error("Pexels was asked in place of the missing Pixabay key")
	}
	if s, _ := w.pixabay(); s != 0 {
		t.Error("Pixabay was asked without a key")
	}
}

// The Pixabay key is in the query of a search. It must come out nowhere: not in an answer of the panel, not in a log line, not in the
// audit log, not in the error a transport raises with the URL in it, not in a request for a file.
func TestPixabayKeyIsNowhereButTheSearchQuery(t *testing.T) {
	e, svc, w := worldEnv(t)
	var logs bytes.Buffer
	e.f.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	setSourceKey(t, e, svc, "pixabay", pixabayTestKey)
	var said []string
	say := func(v any) {
		b, _ := json.Marshal(v)
		said = append(said, string(b))
	}
	seed := seedWithPhotos(t, "business")
	say(photosState(t, e, svc))
	say(preview(t, e, svc, "business", seed)) // with photos
	for _, mode := range []func(){
		func() { w.mu.Lock(); w.status, w.netErr = 429, false; w.mu.Unlock() },
		func() { w.mu.Lock(); w.status, w.netErr = 500, false; w.mu.Unlock() },
		func() { w.mu.Lock(); w.status, w.netErr = 0, true; w.mu.Unlock() },
	} {
		mode()
		e.f.panelDecoyMu.Lock()
		e.f.decoyTemplates = nil // a new maker: the pause after a 429 does not carry over, and no search page is cached
		e.f.panelDecoyMu.Unlock()
		say(preview(t, e, svc, "business", seedWithPhotos(t, "business")))
		made, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "business", Seed: seedWithPhotos(t, "business")}))
		if err != nil {
			t.Fatal(err)
		}
		say(made.Msg)
	}
	for _, s := range said {
		if strings.Contains(s, pixabayTestKey) {
			t.Fatal("the key is in an answer of the panel")
		}
	}
	if strings.Contains(logs.String(), pixabayTestKey) || strings.Contains(logs.String(), "key=") {
		t.Fatalf("the key is in the log: %s", logs.String())
	}
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 200)
	if len(rows) == 0 {
		t.Fatal("no audit rows")
	}
	for _, r := range rows {
		if strings.Contains(r.Params+r.Action+r.Result, pixabayTestKey) {
			t.Fatal("the key is in the audit log")
		}
	}
	if w.keyLeak || w.sawFileWithQuery || !w.sawKey {
		t.Fatalf("key leaked to a file request or a header: %v, a file asked with a query: %v, key used on a search: %v", w.keyLeak, w.sawFileWithQuery, w.sawKey)
	}
	if len(w.other) != 0 {
		t.Fatalf("requests to other hosts: %v", w.other)
	}
}
