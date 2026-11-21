//go:build !js

package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Sites made from the panel's built-in templates (internal/decoy Generate). A preview hands the client the seed it was made from;
// GenerateDecoySite with that seed saves exactly that site, so "another variant" never leaves a draft in the library.

// seedPattern is what the panel itself hands out: 8 to 32 random bytes in hex.
var seedPattern = regexp.MustCompile(`^(?:[0-9a-f]{2}){8,32}$`)

func (s decoySiteService) ListDecoyTemplates(_ context.Context, _ *connect.Request[decoysitev1.ListDecoyTemplatesRequest]) (*connect.Response[decoysitev1.ListDecoyTemplatesResponse], error) {
	out := &decoysitev1.ListDecoyTemplatesResponse{}
	for _, k := range decoy.Kinds() {
		out.Templates = append(out.Templates, &decoysitev1.DecoyTemplate{Id: k.ID, NameEn: k.NameEN, NameRu: k.NameRU})
	}
	return connect.NewResponse(out), nil
}

// generated is one rendered site: its bundle, the brand its pages print and the seed it came from.
type generated struct {
	bundle      *decoy.Bundle
	brand, seed string
}

// render makes the site of a kind from a seed; an empty seed takes a new random one.
func (s decoySiteService) render(kind, seed string) (generated, error) {
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
	raw, _ := hex.DecodeString(seed)
	files, brand, err := decoy.GenerateNamed(kind, raw)
	if errors.Is(err, decoy.ErrUnknownKind) {
		return generated{}, invalid("decoy_template_unknown")
	}
	if err != nil {
		return generated{}, s.fail("generate decoy site", err)
	}
	b, err := decoy.FromFiles(files)
	if err != nil {
		return generated{}, s.fail("bundle generated decoy site", err)
	}
	return generated{bundle: b, brand: fitName(brand), seed: seed}, nil
}

func (s decoySiteService) PreviewDecoyTemplate(_ context.Context, req *connect.Request[decoysitev1.PreviewDecoyTemplateRequest]) (*connect.Response[decoysitev1.PreviewDecoyTemplateResponse], error) {
	g, err := s.render(req.Msg.Kind, req.Msg.Seed)
	if err != nil {
		return nil, err
	}
	doc, err := decoy.PreviewDocument(g.bundle, "")
	if err != nil {
		return nil, s.fail("preview generated decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.PreviewDecoyTemplateResponse{Html: doc, Seed: g.seed, Name: g.brand}), nil
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
	g, err := s.render(m.Kind, m.Seed)
	if err != nil {
		return nil, err
	}
	note := strings.TrimSpace(m.Note)
	if note == "" {
		for _, k := range decoy.Kinds() {
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
	f.audit(ctx, "decoy_site.create", map[string]string{"site_id": row.ID, "name": row.Name, "source": row.Source, "state": row.State, "template": m.Kind, "bundle_sha256": row.BundleHash})
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.GenerateDecoySiteResponse{Site: site}), nil
}

// templateLabelOf keeps the source_label of a generated site on its copy; any other label stays the copier's name.
func templateLabelOf(label string) string {
	if strings.HasPrefix(label, store.DecoyLabelTemplate) {
		return label
	}
	return ""
}
