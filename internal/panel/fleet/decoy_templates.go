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
	photoSource                 string // the source the photos are about: "" when the kind shows none
}

const (
	photoNoKey       = "no_key"
	photoNoneForKind = "none_for_kind"

	// A preview is kept for this long and this many, so that "Create" saves the very site that was shown (photos and all).
	previewKeep    = 15 * time.Minute
	previewEntries = 8
	// One site's photos may take this long to fetch.
	photoBudget = 40 * time.Second

	// The keys are sealed with the vault and kept as base64; the pick is plain ("pexels", "pixabay", or none yet).
	photoKeySetting    = "decoy.photos.key" // Pexels: the name is older than Pixabay
	photoKeyAAD        = "setting/decoy.photos.key"
	pixabayKeySetting  = "decoy.photos.pixabay.key"
	pixabayKeyAAD      = "setting/decoy.photos.pixabay.key"
	photoSourceSetting = "decoy.photos.source"
	photoKeyMin        = 8
	photoKeyMax        = 256
)

type previewEntry struct {
	at time.Time
	g  generated
}

// templateState is what the template calls remember between calls: the sites just previewed and the photo maker (its sources).
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

// photoSources is where each source keeps its key.
var photoSources = []struct{ name, setting, aad string }{
	{decoyphotos.SourcePexels, photoKeySetting, photoKeyAAD},
	{decoyphotos.SourcePixabay, pixabayKeySetting, pixabayKeyAAD},
}

func photoSourceOf(name string) (setting, aad string, ok bool) {
	for _, x := range photoSources {
		if x.name == name {
			return x.setting, x.aad, true
		}
	}
	return "", "", false
}

// photoKey is the owner's key of a source; "" when there is none (or the stored one cannot be opened: another master key).
func (f *Fleet) photoKey(ctx context.Context, source string) vault.Redacted {
	setting, aad, ok := photoSourceOf(source)
	if !ok {
		return ""
	}
	raw, err := f.st.Setting(ctx, setting)
	if err != nil || raw == "" {
		return ""
	}
	sealed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	b, err := f.v.Open(sealed, aad)
	if err != nil {
		f.log.Warn("decoy photos: the stored key cannot be opened; it is ignored", "code", "decoy_photos_key_unreadable", "source", source)
		return ""
	}
	return vault.Redacted(b)
}

// photoSource is the source the photos of new sites come from: the one the owner picked, or whose key was stored last. A panel
// from before Pixabay has no pick: its Pexels key is the one it has; with no key at all the answer is Pixabay, the source that
// still hands out keys. The key of that source may be missing: the sites are then drawn and say "no_key" (the other source is
// never taken in its place).
func (f *Fleet) photoSource(ctx context.Context) string {
	pick, _ := f.st.Setting(ctx, photoSourceSetting)
	if _, _, ok := photoSourceOf(pick); ok {
		return pick
	}
	if f.photoKey(ctx, decoyphotos.SourcePexels) != "" && f.photoKey(ctx, decoyphotos.SourcePixabay) == "" {
		return decoyphotos.SourcePexels
	}
	return decoyphotos.SourcePixabay
}

func (f *Fleet) photosState(ctx context.Context) (hasPexels, hasPixabay bool, source string) {
	return f.photoKey(ctx, decoyphotos.SourcePexels) != "", f.photoKey(ctx, decoyphotos.SourcePixabay) != "", f.photoSource(ctx)
}

func (s decoySiteService) GetDecoyPhotos(ctx context.Context, _ *connect.Request[decoysitev1.GetDecoyPhotosRequest]) (*connect.Response[decoysitev1.GetDecoyPhotosResponse], error) {
	px, pb, src := s.f.photosState(ctx)
	return connect.NewResponse(&decoysitev1.GetDecoyPhotosResponse{HasKey: px, HasPixabayKey: pb, Source: src}), nil
}

// forgetPreviews drops the sites just previewed: they were made with other keys or another source than the next "Create" should save.
func (f *Fleet) forgetPreviews() {
	st := f.templateState()
	st.mu.Lock()
	st.previews = map[string]previewEntry{}
	st.mu.Unlock()
}

func (s decoySiteService) SetDecoyPhotosKey(ctx context.Context, req *connect.Request[decoysitev1.SetDecoyPhotosKeyRequest]) (*connect.Response[decoysitev1.SetDecoyPhotosKeyResponse], error) {
	f := s.f
	source := req.Msg.Source
	if source == "" {
		source = decoyphotos.SourcePexels // a client from before Pixabay knows no other
	}
	setting, aad, ok := photoSourceOf(source)
	if !ok {
		return nil, invalid("decoy_photos_source_invalid")
	}
	key := vault.Redacted(strings.TrimSpace(req.Msg.Key))
	values := map[string]string{setting: ""}
	if key != "" {
		if len(key) < photoKeyMin || len(key) > photoKeyMax || strings.IndexFunc(key.Reveal(), func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r > unicode.MaxASCII }) >= 0 {
			return nil, invalid("decoy_photos_key_invalid")
		}
		values[setting] = base64.StdEncoding.EncodeToString(f.v.Seal([]byte(key.Reveal()), aad))
		values[photoSourceSetting] = source // the key just stored is the one the owner means to use
	} else if f.photoSource(ctx) == source {
		// the key of the source in use is gone: the other one takes over when it has a key, else the pick stays and sites are drawn
		for _, x := range photoSources {
			if x.name != source && f.photoKey(ctx, x.name) != "" {
				values[photoSourceSetting] = x.name
			}
		}
	}
	if err := f.st.SetSettings(ctx, values); err != nil {
		return nil, s.fail("save the "+source+" key", err)
	}
	f.forgetPreviews()
	if key == "" {
		f.audit(ctx, "decoy_photos.key_remove", map[string]string{"source": source})
	} else {
		f.audit(ctx, "decoy_photos.key_set", map[string]string{"source": source})
	}
	px, pb, src := f.photosState(ctx)
	return connect.NewResponse(&decoysitev1.SetDecoyPhotosKeyResponse{HasKey: px, HasPixabayKey: pb, Source: src}), nil
}

func (s decoySiteService) SetDecoyPhotosSource(ctx context.Context, req *connect.Request[decoysitev1.SetDecoyPhotosSourceRequest]) (*connect.Response[decoysitev1.SetDecoyPhotosSourceResponse], error) {
	f := s.f
	source := req.Msg.Source
	if _, _, ok := photoSourceOf(source); !ok {
		return nil, invalid("decoy_photos_source_invalid")
	}
	if f.photoKey(ctx, source) == "" {
		return nil, refuse("decoy_photos_no_key") // no key, nothing to pick: the sites would be drawn
	}
	if err := f.st.SetSettings(ctx, map[string]string{photoSourceSetting: source}); err != nil {
		return nil, s.fail("save the photos source", err)
	}
	f.forgetPreviews()
	f.audit(ctx, "decoy_photos.source_set", map[string]string{"source": source})
	px, pb, src := f.photosState(ctx)
	return connect.NewResponse(&decoysitev1.SetDecoyPhotosSourceResponse{HasKey: px, HasPixabayKey: pb, Source: src}), nil
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
	why, photos, source := photoNoneForKind, map[string][]byte(nil), ""
	if len(plan) > 0 {
		source = s.f.photoSource(ctx)
		if key := s.f.photoKey(ctx, source); key == "" {
			why = photoNoKey
		} else {
			fetchCtx, cancel := context.WithTimeout(ctx, photoBudget)
			res := st.maker.Make(fetchCtx, source, key, raw, plan)
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
	g := generated{bundle: b, brand: fitName(brand), seed: seed, photo: why, country: country, photoSource: source}
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
	return connect.NewResponse(&decoysitev1.PreviewDecoyTemplateResponse{Html: doc, Seed: g.seed, Name: g.brand, Photos: g.photo, PhotosSource: g.photoSource}), nil
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
