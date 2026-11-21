package sitegen

import (
	"fmt"
	"strings"
)

// Photos. The generator never touches the network: the caller asks Plan which photos a site would like, fetches them however it
// likes, and hands the JPEG files back in Options.Photos. A slot with no photo keeps the drawn picture, byte for byte as if
// photos did not exist, so a site never depends on them.

// MaxPhotos is how many slots one site asks for at most.
const MaxPhotos = 8

// Slot is a place in a site that may hold a photo.
type Slot struct {
	Name  string // the place: "hero", "t3"; the key of Options.Photos
	Query string // what to search for, from the vocabulary of the site's bundle
	W, H  int    // the shape of the box the photo fills (the aspect matters, not the units)
	MaxW  int    // the widest file worth having, in pixels
}

// Options are what Generate may be given besides a kind and a seed.
type Options struct {
	// Country is the code of the country the site is made for ("" is DefaultCountry): its language, cities, money and formats.
	Country string
	// Photos maps Slot.Name to a JPEG that already has the shape of its slot. Nil keeps every picture drawn; an empty map does
	// the same, a slot missing from it stays drawn.
	Photos map[string][]byte
}

// photos is the state of one render: the slots it was asked for, and the files it was given or is only counting.
type photos struct {
	have   map[string][]byte // nil while planning
	asked  []string          // slot names in the order they were first asked for
	slots  map[string]Slot
	path   map[string]string // "" when a slot has no photo
	placed map[string]bool   // the slots whose file is in the site
	dir    string
}

// GenerateWith is GenerateNamed with Options.
func GenerateWith(kind string, seed []byte, o Options) ([]File, string, error) {
	d, err := kindOf(kind)
	if err != nil {
		return nil, "", err
	}
	var ph *photos
	if len(o.Photos) > 0 {
		ph = &photos{have: o.Photos}
	}
	return d.render(seed, o.Country, ph)
}

// Plan lists the photos a site of this kind and seed would like for DefaultCountry; see PlanFor.
func Plan(kind string, seed []byte) ([]Slot, error) { return PlanFor(kind, seed, "") }

// PlanFor lists the photos a site of this kind, seed and country would like, in the order its pages ask for them, at most
// MaxPhotos. It is empty when the kind or the variant the seed chose shows none (a card, a docs site, an illustrator's portfolio).
func PlanFor(kind string, seed []byte, country string) ([]Slot, error) {
	d, err := kindOf(kind)
	if err != nil {
		return nil, err
	}
	ph := &photos{}
	if _, _, err := d.render(seed, country, ph); err != nil {
		return nil, err
	}
	out := make([]Slot, 0, len(ph.asked))
	for _, n := range ph.asked {
		if ph.path[n] != "" {
			out = append(out, ph.slots[n])
		}
	}
	return out, nil
}

func kindOf(kind string) (*kindDef, error) {
	kinds, err := loadKinds()
	if err != nil {
		return nil, err
	}
	d := kinds[kind]
	if d == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	return d, nil
}

// photoWords are the search words of a variant: its own, or the kind's.
func (d *kindDef) photoWords(b bundleDef) []string {
	var out []string
	for _, v := range b.PhotoQ {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		return out
	}
	return d.meta.PhotoQ
}

// query is the search words of a slot, "" when the slot's place takes no photo here: "t3" is the third of the group "t".
func (s *site) query(name string) string {
	group := strings.TrimRight(name, "0123456789")
	idx := s.photoSlots[group]
	if len(idx) == 0 || len(s.photoQ) == 0 {
		return ""
	}
	n := 0
	fmt.Sscan(strings.TrimPrefix(name, group), &n)
	return s.photoQ[idx[max(n-1, 0)%len(idx)]%len(s.photoQ)]
}

// photoFile is the path of the photo of a slot and whether there is one. The first ask fixes the slot's shape. With place the photo
// is also put into the site's files, once: a question (hasphoto) leaves the site as it is, so that a photo no page shows is not kept.
// dir is the folder to put it in, "" for the site's own.
func (c *ctx) photoFile(name string, w, h int, dir string, place bool) (string, bool) {
	ph := c.ph
	if ph == nil {
		return "", false
	}
	if ph.path == nil {
		ph.path, ph.slots, ph.placed = map[string]string{}, map[string]Slot{}, map[string]bool{}
	}
	if p, ok := ph.path[name]; ok {
		if place && p != "" && ph.have != nil && !ph.placed[name] {
			ph.placed[name] = true
			c.files[strings.TrimPrefix(p, "/")] = ph.have[name]
		}
		return p, p != ""
	}
	if len(ph.asked) >= MaxPhotos {
		return "", false
	}
	q := c.s.query(name)
	if q == "" {
		ph.path[name] = ""
		return "", false
	}
	ph.asked = append(ph.asked, name)
	maxW := 640
	if w >= 1000 {
		maxW = 1280
	}
	ph.slots[name] = Slot{Name: name, Query: q, W: w, H: h, MaxW: maxW}
	data := ph.have[name]
	if ph.have != nil && len(data) == 0 { // given photos, and none for this slot
		ph.path[name] = ""
		return "", false
	}
	if dir == "" {
		if ph.dir == "" {
			ph.dir = imgDirs[c.r.sub("photodir").n(len(imgDirs))]
		}
		dir = ph.dir
	}
	p := fmt.Sprintf("%s/%s-%s.jpg", dir, name, hex6(c.r.sub("photo:"+name)))
	ph.path[name] = "/" + p
	if place && ph.have != nil {
		ph.placed[name] = true
		c.files[p] = data
	}
	return "/" + p, true
}

// photoTag is the markup of a photo: it fills its box whatever its own shape.
func photoTag(name, path string, w, h int) string {
	load := "lazy"
	switch name {
	case "hero", "cover", "side":
		load = "eager"
	}
	return fmt.Sprintf(`<img class="__art__" src="%s" alt="" width="%d" height="%d" loading="%s" style="object-fit:cover">`, path, w, h, load)
}
