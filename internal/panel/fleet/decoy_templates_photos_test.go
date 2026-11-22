//go:build !js

package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/decoy/sitegen"
)

const pexelsTestKey = "pexels-test-key-0123456789abcdef"

// fakePexels is the only Pexels there is in these tests: a RoundTripper, so no packet leaves the test.
type fakePexels struct {
	mu                    sync.Mutex
	searches, files       int
	hosts                 []string
	status                int // the answer to every search when not 0
	jpg                   []byte
	authOnFiles, keySeen  bool
	otherHost             bool
	keyInURLOrFileRequest bool
}

func newFakePexels(t *testing.T) *fakePexels {
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1100))
	for y := 0; y < 1100; y++ {
		for x := 0; x < 1600; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x / 7), uint8(y / 5), uint8((x + y) / 11), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return &fakePexels{jpg: b.Bytes()}
}

func (p *fakePexels) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts = append(p.hosts, req.URL.Host)
	if req.URL.Host != "api.pexels.com" && req.URL.Host != "images.pexels.com" {
		p.otherHost = true
	}
	if strings.Contains(req.URL.String(), pexelsTestKey) {
		p.keyInURLOrFileRequest = true
	}
	rec := httptest.NewRecorder()
	switch req.URL.Host {
	case "api.pexels.com":
		p.searches++
		if req.Header.Get("Authorization") == pexelsTestKey {
			p.keySeen = true
		}
		if p.status != 0 {
			rec.WriteHeader(p.status)
			break
		}
		pg, _ := strconv.Atoi(req.URL.Query().Get("page"))
		var ph []map[string]any
		for i := 1; i <= 30; i++ {
			id := pg*1000 + i
			ph = append(ph, map[string]any{"id": id, "alt": "a wooden table", "src": map[string]string{
				"large":   fmt.Sprintf("https://images.pexels.com/photos/%d/l.jpeg", id),
				"large2x": fmt.Sprintf("https://images.pexels.com/photos/%d/l2.jpeg", id),
			}})
		}
		json.NewEncoder(rec).Encode(map[string]any{"photos": ph})
	default:
		p.files++
		if req.Header.Get("Authorization") != "" {
			p.keyInURLOrFileRequest = true
		}
		rec.Write(p.jpg)
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (p *fakePexels) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.searches, p.files
}

func photoEnv(t *testing.T) (*env, decoySiteService, *fakePexels) {
	e := newEnv(t)
	p := newFakePexels(t)
	e.f.photoTransport = p
	return e, decoySiteService{e.f}, p
}

func setKey(t *testing.T, e *env, svc decoySiteService, key string) {
	t.Helper()
	if _, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: key})); err != nil {
		t.Fatal(err)
	}
}

func preview(t *testing.T, e *env, svc decoySiteService, kind, seed string) *decoysitev1.PreviewDecoyTemplateResponse {
	t.Helper()
	got, err := svc.PreviewDecoyTemplate(e.ctx, connect.NewRequest(&decoysitev1.PreviewDecoyTemplateRequest{Kind: kind, Seed: seed}))
	if err != nil {
		t.Fatal(err)
	}
	return got.Msg
}

// seedWithPhotos is a seed of the kind whose site asks for photos.
func seedWithPhotos(t *testing.T, kind string) string {
	t.Helper()
	for i := 0; i < 300; i++ {
		seed := fmt.Sprintf("%016x", 0x1234abcd+i)
		if plan, err := sitegen.Plan(kind, mustHex(t, seed)); err == nil && len(plan) > 0 {
			return seed
		}
	}
	t.Fatalf("no %s seed asks for photos", kind)
	return ""
}

func photoCount(t *testing.T, b *decoy.Bundle) (n int) {
	for _, f := range b.Files() {
		if path.Ext(f.Path) == ".jpg" {
			n++
		}
	}
	return
}

func TestPhotosKeyIsStoredSealedAndNeverShown(t *testing.T) {
	e, svc, _ := photoEnv(t)
	var logs bytes.Buffer
	e.f.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	has := func() bool {
		got, err := svc.GetDecoyPhotos(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPhotosRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return got.Msg.HasKey
	}
	if has() {
		t.Fatal("a key before any was set")
	}
	set, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: "  " + pexelsTestKey + " \n"}))
	if err != nil || !set.Msg.HasKey || !has() {
		t.Fatalf("set: %v %v", set, err)
	}
	if got := e.f.photoKey(e.ctx); got.Reveal() != pexelsTestKey {
		t.Fatal("the stored key is not the one that was set (trimmed)")
	}
	// the stored value is sealed, not the key
	raw, _ := e.st.Setting(e.ctx, photoKeySetting)
	if raw == "" || strings.Contains(raw, pexelsTestKey) {
		t.Fatalf("stored value %q", raw)
	}
	// every shape of key that cannot be one is refused
	for _, bad := range []string{"short", strings.Repeat("a", 257), "has space inside1", "tab\there1234", "ключ-на-русском-1234"} {
		if _, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: bad})); refusalOf(err) != "invalid_argument decoy_photos_key_invalid" {
			t.Errorf("key %q: %v", bad, err)
		}
	}
	if !has() {
		t.Fatal("a refused key removed the stored one")
	}
	// the audit log names the action and nothing else
	rows, err := e.st.ListAudit(e.ctx, "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if strings.Contains(row.Params, pexelsTestKey) || strings.Contains(row.Action, pexelsTestKey) {
			t.Fatal("the key is in the audit log")
		}
		seen[row.Action] = true
	}
	if !seen["decoy_photos.key_set"] {
		t.Fatalf("no audit event for setting the key: %v", seen)
	}
	// removing it
	rm, err := svc.SetDecoyPhotosKey(e.ctx, connect.NewRequest(&decoysitev1.SetDecoyPhotosKeyRequest{Key: ""}))
	if err != nil || rm.Msg.HasKey || has() {
		t.Fatalf("remove: %v %v", rm, err)
	}
	if rows, _ := e.st.ListAudit(e.ctx, "", 0, 50); !func() bool {
		for _, r := range rows {
			if r.Action == "decoy_photos.key_remove" {
				return true
			}
		}
		return false
	}() {
		t.Fatal("no audit event for removing the key")
	}
	// a preview with the key in place, then everything the panel said and wrote: the key is nowhere
	setKey(t, e, svc, pexelsTestKey)
	p := preview(t, e, svc, "business", seedWithPhotos(t, "business"))
	msg, _ := json.Marshal(p)
	if strings.Contains(string(msg), pexelsTestKey) || strings.Contains(logs.String(), pexelsTestKey) {
		t.Fatal("the key is in an answer or in the log")
	}
	if rows, _ := e.st.ListAudit(e.ctx, "", 0, 200); len(rows) == 0 {
		t.Fatal("no audit rows")
	} else {
		for _, r := range rows {
			if strings.Contains(r.Params+r.Action+r.Result, pexelsTestKey) {
				t.Fatal("the key is in the audit log")
			}
		}
	}
}

func TestPreviewWithKeyHasPhotosAndCreateSavesTheSameSite(t *testing.T) {
	e, svc, p := photoEnv(t)
	setKey(t, e, svc, pexelsTestKey)
	kind := "blog"
	seed := seedWithPhotos(t, kind)
	a := preview(t, e, svc, kind, seed)
	if a.Photos != "used" || a.Seed != seed {
		t.Fatalf("photos = %q", a.Photos)
	}
	searches, files := p.counts()
	if searches < 1 || searches > 3 || files < 1 || files > 8 {
		t.Fatalf("%d searches and %d files for one preview", searches, files)
	}
	if p.otherHost || p.keyInURLOrFileRequest || !p.keySeen {
		t.Fatalf("hosts %v, key misused %v, key seen %v", p.hosts, p.keyInURLOrFileRequest, p.keySeen)
	}
	// asking again for the same seed is the same site, at no cost
	if again := preview(t, e, svc, kind, seed); again.Html != a.Html || again.Photos != "used" {
		t.Fatal("the same seed gave another preview")
	}
	if s2, f2 := p.counts(); s2 != searches || f2 != files {
		t.Fatalf("the repeat cost Pexels requests: %d/%d then %d/%d", searches, files, s2, f2)
	}
	got, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: kind, Seed: seed}))
	if err != nil {
		t.Fatal(err)
	}
	if s2, f2 := p.counts(); s2 != searches || f2 != files || got.Msg.Photos != "used" {
		t.Fatalf("create asked Pexels again (%d/%d) or lost the photos (%q)", s2, f2, got.Msg.Photos)
	}
	site := got.Msg.Site
	saved, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: site.Id}))
	if err != nil || saved.Msg.Html != a.Html {
		t.Fatalf("the saved site is not the one that was shown (%v)", err)
	}
	b, _ := e.f.templateState().get(previewKey(sitegen.DefaultCountry, kind, seed), e.f.now())
	if site.BundleSha256 != b.bundle.Hash() || photoCount(t, b.bundle) == 0 || b.bundle.Size() > decoy.MaxBundleBytes {
		t.Fatalf("saved hash %s, %d photos, %d bytes", site.BundleSha256, photoCount(t, b.bundle), b.bundle.Size())
	}
	t.Logf("%s with photos: %d files, %d bytes, %d photos", kind, len(b.bundle.Files()), b.bundle.Size(), photoCount(t, b.bundle))
}

// After 15 minutes the preview is gone and "Create" makes the site again.
func TestPreviewExpiresAfterFifteenMinutesAndTheCacheIsSmall(t *testing.T) {
	e, svc, p := photoEnv(t)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	e.f.now = func() time.Time { return now }
	setKey(t, e, svc, pexelsTestKey)
	seed := seedWithPhotos(t, "business")
	preview(t, e, svc, "business", seed)
	st := e.f.templateState()
	if _, ok := st.get(previewKey(sitegen.DefaultCountry, "business", seed), now.Add(14*time.Minute)); !ok {
		t.Fatal("a preview was forgotten within 15 minutes")
	}
	now = now.Add(16 * time.Minute)
	if _, ok := st.get(previewKey(sitegen.DefaultCountry, "business", seed), now); ok {
		t.Fatal("a preview was kept for 16 minutes")
	}
	_, files := p.counts()
	got, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "business", Seed: seed}))
	if err != nil || got.Msg.Site.Id == "" {
		t.Fatal(err)
	}
	if _, f2 := p.counts(); f2 <= files {
		t.Fatal("an expired preview was saved from memory")
	}
	// at most eight are kept, the oldest go
	for i := 0; i < 12; i++ {
		now = now.Add(time.Second)
		preview(t, e, svc, "card", fmt.Sprintf("%016x", 0xaaaa0000+i))
	}
	st.mu.Lock()
	n := len(st.previews)
	st.mu.Unlock()
	if n != previewEntries {
		t.Fatalf("%d previews kept, want %d", n, previewEntries)
	}
	if _, ok := st.get(previewKey(sitegen.DefaultCountry, "card", fmt.Sprintf("%016x", 0xaaaa0000+11)), now); !ok {
		t.Fatal("the newest preview was dropped")
	}
	if _, ok := st.get(previewKey(sitegen.DefaultCountry, "card", fmt.Sprintf("%016x", 0xaaaa0000)), now); ok {
		t.Fatal("the oldest preview was kept")
	}
}

func TestPhotoReasons(t *testing.T) {
	e, svc, p := photoEnv(t)
	seed := seedWithPhotos(t, "business")
	// no key: drawn, and no request leaves
	a := preview(t, e, svc, "business", seed)
	if a.Photos != "no_key" || a.Html == "" {
		t.Fatalf("no key: %q", a.Photos)
	}
	if s, f := p.counts(); s != 0 || f != 0 {
		t.Fatalf("no key, yet %d searches and %d files", s, f)
	}
	setKey(t, e, svc, pexelsTestKey)
	// kinds that never show a photo, with a key
	for _, kind := range []string{"docs", "project", "status"} {
		if got := preview(t, e, svc, kind, ""); got.Photos != "none_for_kind" {
			t.Errorf("%s: %q", kind, got.Photos)
		}
	}
	if s, _ := p.counts(); s != 0 {
		t.Fatalf("a kind without photos searched %d times", s)
	}
	for _, tc := range []struct {
		status int
		want   string
	}{{429, "limit"}, {401, "bad_key"}, {500, "unavailable"}} {
		p.mu.Lock()
		p.status = tc.status
		p.mu.Unlock()
		e.f.templateState().mu.Lock()
		e.f.templateState().previews = map[string]previewEntry{}
		e.f.templateState().mu.Unlock()
		// a new maker for each, so that the pause after a 429 does not carry over
		e.f.panelDecoyMu.Lock()
		e.f.decoyTemplates = nil
		e.f.panelDecoyMu.Unlock()
		got := preview(t, e, svc, "business", seedWithPhotos(t, "business"))
		if got.Photos != tc.want || got.Html == "" || strings.Contains(got.Html, "data:image/jpeg") {
			t.Errorf("status %d: photos %q (jpeg in page: %v)", tc.status, got.Photos, strings.Contains(got.Html, "data:image/jpeg"))
		}
		// and the site is still made and saved
		made, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "business", Seed: seedWithPhotos(t, "business"), Name: "drawn " + tc.want}))
		if err != nil || made.Msg.Photos != tc.want || made.Msg.Site.FileCount < 5 {
			t.Errorf("status %d: create: %v %v", tc.status, made, err)
		}
	}
}

func TestSettingOrRemovingTheKeyForgetsThePreviews(t *testing.T) {
	e, svc, _ := photoEnv(t)
	seed := seedWithPhotos(t, "business")
	if got := preview(t, e, svc, "business", seed); got.Photos != "no_key" {
		t.Fatal(got.Photos)
	}
	setKey(t, e, svc, pexelsTestKey)
	if got := preview(t, e, svc, "business", seed); got.Photos != "used" {
		t.Fatalf("after the key was set the seed still shows %q", got.Photos)
	}
	setKey(t, e, svc, "")
	if got := preview(t, e, svc, "business", seed); got.Photos != "no_key" {
		t.Fatalf("after the key was removed the seed shows %q", got.Photos)
	}
}

// "Another variant" must not eat the hourly limit: the search pages are reused, so the searches stay bounded however many variants
// are looked at, and looking at the same ones again costs none.
func TestAnotherVariantReusesSearchPages(t *testing.T) {
	e, svc, p := photoEnv(t)
	setKey(t, e, svc, pexelsTestKey)
	seeds := make([]string, 24)
	for i := range seeds {
		seeds[i] = fmt.Sprintf("%016x", 0xbeef0000+i)
		if got := preview(t, e, svc, "business", seeds[i]); got.Photos != "used" {
			t.Fatalf("seed %d: %q", i, got.Photos)
		}
	}
	s, _ := p.counts()
	if s > 6*3*3 { // six bundles, three queries each, the first three pages of every query: the most there can ever be
		t.Fatalf("%d searches for 24 variants", s)
	}
	e.f.templateState().mu.Lock()
	e.f.templateState().previews = map[string]previewEntry{} // the previews are gone, the search pages are not
	e.f.templateState().mu.Unlock()
	for _, seed := range seeds {
		preview(t, e, svc, "business", seed)
	}
	if s2, _ := p.counts(); s2 != s {
		t.Fatalf("the same 24 variants again cost %d more searches", s2-s)
	}
}

// A site with photos has every photo file a page shows, and still fits the library.
func TestEveryPhotoKindFitsTheLibrary(t *testing.T) {
	e, svc, _ := photoEnv(t)
	setKey(t, e, svc, pexelsTestKey)
	for _, kind := range []string{"business", "blog", "portfolio", "club", "files", "login"} {
		seed := seedWithPhotos(t, kind)
		got := preview(t, e, svc, kind, seed)
		if got.Photos != "used" {
			t.Errorf("%s: %q", kind, got.Photos)
			continue
		}
		b, _ := e.f.templateState().get(previewKey(sitegen.DefaultCountry, kind, seed), e.f.now())
		if b.bundle == nil || photoCount(t, b.bundle) == 0 || b.bundle.Size() > decoy.MaxBundleBytes {
			t.Errorf("%s: %d photos", kind, photoCount(t, b.bundle))
		}
	}
	_ = context.Background
}
