//go:build !js

package fleet

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/decoy/sitegen"
	"github.com/mistgate/mistgate/internal/panel/decoyphotos"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// Sites made from the panel's built-in templates (internal/decoy Generate). A preview hands the client the seed it was made from;
// GenerateDecoySite with that seed saves exactly that site, so "another variant" never leaves a draft in the library.

// seedPattern is what the panel itself hands out: 8 to 32 random bytes in hex.
var seedPattern = regexp.MustCompile(`^(?:[0-9a-f]{2}){8,32}$`)

func (s decoySiteService) ListDecoyTemplates(_ context.Context, _ *connect.Request[decoysitev1.ListDecoyTemplatesRequest]) (*connect.Response[decoysitev1.ListDecoyTemplatesResponse], error) {
	out := &decoysitev1.ListDecoyTemplatesResponse{}
	for _, c := range sitegen.Countries() {
		out.Countries = append(out.Countries, c.Code)
	}
	for _, k := range sitegen.Kinds() {
		out.Templates = append(out.Templates, &decoysitev1.DecoyTemplate{Id: k.ID, NameEn: k.NameEN, NameRu: k.NameRU, Countries: k.Countries})
	}
	return connect.NewResponse(out), nil
}

// generated is one rendered site: its bundle, the brand its pages print, the seed it came from and whether it has photos (the
// reasons of PreviewDecoyTemplateResponse.photos).
type generated struct {
	bundle                      *decoy.Bundle
	brand, seed, photo, country string
}

const (
	photoNoKey       = "no_key"
	photoNoneForKind = "none_for_kind"

	// A preview is kept for this long and this many, so that "Create" saves the very site that was shown (photos and all).
	previewKeep    = 15 * time.Minute
	previewEntries = 8
	// One site's photos may take this long to fetch.
	photoBudget = 40 * time.Second

	photoKeySetting = "decoy.photos.key" // base64 of the vault-sealed Pexels key
	photoKeyAAD     = "setting/decoy.photos.key"
	photoKeyMin     = 8
	photoKeyMax     = 256
)

type previewEntry struct {
	at time.Time
	g  generated
}

// templateState is what the template calls remember between calls: the sites just previewed and the Pexels client.
type templateState struct {
	mu       sync.Mutex
	previews map[string]previewEntry
	maker    *decoyphotos.Maker
}

func (f *Fleet) templateState() *templateState {
	f.panelDecoyMu.Lock()
	defer f.panelDecoyMu.Unlock()
	if st, ok := f.decoyTemplates.(*templateState); ok {
		return st
	}
	st := &templateState{previews: map[string]previewEntry{}, maker: decoyphotos.NewMaker(f.photoTransport, f.now)}
	f.decoyTemplates = st
	return st
}

// previewKey names a preview: the country, the kind and the seed make one site.
func previewKey(country, kind, seed string) string { return country + "/" + kind + "/" + seed }

func (st *templateState) get(key string, now time.Time) (generated, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.previews[key]
	if !ok || now.Sub(e.at) > previewKeep {
		delete(st.previews, key)
		return generated{}, false
	}
	return e.g, true
}

func (st *templateState) put(key string, g generated, now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, e := range st.previews { // the expired go first, then the oldest
		if now.Sub(e.at) > previewKeep {
			delete(st.previews, k)
		}
	}
	for len(st.previews) >= previewEntries {
		var oldest string
		for k, e := range st.previews {
			if oldest == "" || e.at.Before(st.previews[oldest].at) {
				oldest = k
			}
		}
		delete(st.previews, oldest)
	}
	st.previews[key] = previewEntry{at: now, g: g}
}

// photoKey is the owner's Pexels key; "" when there is none (or the stored one cannot be opened: another master key).
func (f *Fleet) photoKey(ctx context.Context) vault.Redacted {
	raw, err := f.st.Setting(ctx, photoKeySetting)
	if err != nil || raw == "" {
		return ""
	}
	sealed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	b, err := f.v.Open(sealed, photoKeyAAD)
	if err != nil {
		f.log.Warn("decoy photos: the stored key cannot be opened; it is ignored", "code", "decoy_photos_key_unreadable")
		return ""
	}
	return vault.Redacted(b)
}

func (s decoySiteService) GetDecoyPhotos(ctx context.Context, _ *connect.Request[decoysitev1.GetDecoyPhotosRequest]) (*connect.Response[decoysitev1.GetDecoyPhotosResponse], error) {
	return connect.NewResponse(&decoysitev1.GetDecoyPhotosResponse{HasKey: s.f.photoKey(ctx) != ""}), nil
}

func (s decoySiteService) SetDecoyPhotosKey(ctx context.Context, req *connect.Request[decoysitev1.SetDecoyPhotosKeyRequest]) (*connect.Response[decoysitev1.SetDecoyPhotosKeyResponse], error) {
	f := s.f
	key := vault.Redacted(strings.TrimSpace(req.Msg.Key))
	value := ""
	if key != "" {
		if len(key) < photoKeyMin || len(key) > photoKeyMax || strings.IndexFunc(key.Reveal(), func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r > unicode.MaxASCII }) >= 0 {
			return nil, invalid("decoy_photos_key_invalid")
		}
		value = base64.StdEncoding.EncodeToString(f.v.Seal([]byte(key.Reveal()), photoKeyAAD))
	}
	if err := f.st.SetSettings(ctx, map[string]string{photoKeySetting: value}); err != nil {
		return nil, s.fail("save the Pexels key", err)
	}
	st := f.templateState()
	st.mu.Lock() // sites made with the old key (or without one) are not the ones the next "Create" should save
	st.previews = map[string]previewEntry{}
	st.mu.Unlock()
	if key == "" {
		f.audit(ctx, "decoy_photos.key_remove", map[string]string{})
	} else {
		f.audit(ctx, "decoy_photos.key_set", map[string]string{})
	}
	return connect.NewResponse(&decoysitev1.SetDecoyPhotosKeyResponse{HasKey: key != ""}), nil
}

// render makes the site of a kind for a country from a seed; an empty seed takes a new random one, an empty country is
// sitegen.DefaultCountry. A site previewed in the last quarter of an hour comes back as it was.
func (s decoySiteService) render(ctx context.Context, kind, country, seed string) (generated, error) {
	if country == "" {
		country = sitegen.DefaultCountry
	}
	if seed == "" {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return generated{}, s.fail("draw a seed", err)
		}
		seed = hex.EncodeToString(raw)
	}
	if !seedPattern.MatchString(seed) {
		return generated{}, invalid("decoy_template_seed")
	}
	st := s.f.templateState()
	pk := previewKey(country, kind, seed)
	if g, ok := st.get(pk, s.f.now()); ok {
		return g, nil
	}
	raw, _ := hex.DecodeString(seed)
	plan, err := sitegen.PlanFor(kind, raw, country)
	switch {
	case errors.Is(err, sitegen.ErrUnknownKind):
		return generated{}, invalid("decoy_template_unknown")
	case errors.Is(err, sitegen.ErrUnknownCountry), errors.Is(err, sitegen.ErrNotInCountry):
		return generated{}, invalid("decoy_template_country")
	case err != nil:
		return generated{}, s.fail("plan decoy site", err)
	}
	// Photos are an extra: whatever goes wrong with them, the site is made with drawings.
	why, photos := photoNoneForKind, map[string][]byte(nil)
	if len(plan) > 0 {
		if key := s.f.photoKey(ctx); key == "" {
			why = photoNoKey
		} else {
			fetchCtx, cancel := context.WithTimeout(ctx, photoBudget)
			res := st.maker.Make(fetchCtx, key, raw, plan)
			cancel()
			why, photos = res.Reason, res.Photos
		}
	}
	files, brand, err := sitegen.GenerateWith(kind, raw, sitegen.Options{Country: country, Photos: photos})
	if err != nil {
		return generated{}, s.fail("generate decoy site", err)
	}
	b, err := decoy.FromFiles(files)
	if err != nil && len(photos) > 0 { // the photos made it too big or odd for the library: draw the site instead
		files, brand, err = sitegen.GenerateWith(kind, raw, sitegen.Options{Country: country})
		if err == nil {
			why = decoyphotos.ReasonUnavailable
			b, err = decoy.FromFiles(files)
		}
	}
	if err != nil {
		return generated{}, s.fail("bundle generated decoy site", err)
	}
	g := generated{bundle: b, brand: fitName(brand), seed: seed, photo: why, country: country}
	st.put(pk, g, s.f.now())
	return g, nil
}

func (s decoySiteService) PreviewDecoyTemplate(ctx context.Context, req *connect.Request[decoysitev1.PreviewDecoyTemplateRequest]) (*connect.Response[decoysitev1.PreviewDecoyTemplateResponse], error) {
	g, err := s.render(ctx, req.Msg.Kind, req.Msg.Country, req.Msg.Seed)
	if err != nil {
		return nil, err
	}
	doc, err := decoy.PreviewDocument(g.bundle, "")
	if err != nil {
		return nil, s.fail("preview generated decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.PreviewDecoyTemplateResponse{Html: doc, Seed: g.seed, Name: g.brand, Photos: g.photo}), nil
}

// fitName is a name the library takes: trimmed, at most 60 characters.
func fitName(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > store.MaxDecoyNameRunes {
		s = strings.TrimSpace(string([]rune(s)[:store.MaxDecoyNameRunes]))
	}
	return s
}

// numbered is name with " 2", " 3" ... after it, cut so the whole stays within the limit.
func numbered(name string, n int) string {
	suffix := fmt.Sprintf(" %d", n)
	if r := []rune(name); len(r)+len(suffix) > store.MaxDecoyNameRunes {
		name = string(r[:store.MaxDecoyNameRunes-len(suffix)])
	}
	return name + suffix
}

func (s decoySiteService) GenerateDecoySite(ctx context.Context, req *connect.Request[decoysitev1.GenerateDecoySiteRequest]) (*connect.Response[decoysitev1.GenerateDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	g, err := s.render(ctx, m.Kind, m.Country, m.Seed)
	if err != nil {
		return nil, err
	}
	note := strings.TrimSpace(m.Note)
	if note == "" {
		for _, k := range sitegen.Kinds() {
			if k.ID == m.Kind {
				note = k.NameEN
			}
		}
	}
	name := strings.TrimSpace(m.Name)
	given := name != ""
	if !given {
		name = g.brand
	}
	var row store.DecoySiteRow
	for n := 1; ; n++ {
		try := name
		if n > 1 {
			try = numbered(name, n)
		}
		row, err = f.st.CreateDecoySite(ctx, f.now(), store.NewDecoySite{Name: try, Note: note, Source: store.DecoySourceUpload, State: store.DecoyStateReady, By: f.actor(ctx), Label: store.DecoyLabelTemplate + m.Kind, Bundle: g.bundle})
		// a name the owner chose is theirs to change; one the panel took from the brand gets a number when another site has it
		if errors.Is(err, store.ErrDecoyNameTaken) && !given && n < 50 {
			continue
		}
		break
	}
	if err != nil {
		return nil, s.fail("create generated decoy site", err)
	}
	f.audit(ctx, "decoy_site.create", map[string]string{"site_id": row.ID, "name": row.Name, "source": row.Source, "state": row.State, "template": m.Kind, "country": g.country, "bundle_sha256": row.BundleHash})
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.GenerateDecoySiteResponse{Site: site, Photos: g.photo}), nil
}

// templateLabelOf keeps the source_label of a generated site on its copy; any other label stays the copier's name.
func templateLabelOf(label string) string {
	if strings.HasPrefix(label, store.DecoyLabelTemplate) {
		return label
	}
	return ""
}
