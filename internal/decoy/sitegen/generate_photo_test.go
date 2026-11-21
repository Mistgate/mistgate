//go:build !js

package sitegen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"path"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/decoy"
)

// stubJPEG is a small coloured JPEG made here; the tests use no real photograph.
func stubJPEG(t *testing.T, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{shade, uint8(x * 255 / w), uint8(y * 255 / h), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func photosFor(t *testing.T, plan []Slot) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	for i, s := range plan {
		m[s.Name] = stubJPEG(t, 80, 60, uint8(i*30))
	}
	return m
}

// Kinds that must never show a photograph, whatever the seed.
var photoFree = map[string]bool{"card": true, "docs": true, "project": true, "status": true}

func TestPlanSlots(t *testing.T) {
	withPhotos := map[string]int{}
	for _, kind := range kindIDs(t) {
		for i := 0; i < 60; i++ {
			plan, err := Plan(kind, testSeed(i))
			if err != nil {
				t.Fatal(err)
			}
			if len(plan) > MaxPhotos {
				t.Fatalf("%s seed %d: %d slots", kind, i, len(plan))
			}
			if photoFree[kind] && len(plan) > 0 {
				t.Fatalf("%s must not ask for photos, asked for %v", kind, plan)
			}
			queries, names := map[string]bool{}, map[string]bool{}
			for _, s := range plan {
				if s.Query == "" || s.W <= 0 || s.H <= 0 || s.MaxW <= 0 || names[s.Name] {
					t.Fatalf("%s seed %d: bad slot %+v", kind, i, s)
				}
				names[s.Name], queries[s.Query] = true, true
			}
			if len(queries) > 3 {
				t.Errorf("%s seed %d: %d searches", kind, i, len(queries))
			}
			if len(plan) > 0 {
				withPhotos[kind]++
			}
			again, _ := Plan(kind, testSeed(i))
			if fmt.Sprint(again) != fmt.Sprint(plan) {
				t.Fatalf("%s seed %d: the plan changed between two runs", kind, i)
			}
		}
	}
	for _, kind := range []string{"business", "blog", "portfolio", "club", "files", "login"} {
		if withPhotos[kind] == 0 {
			t.Errorf("%s never asks for a photo", kind)
		}
	}
}

// No bundle may need more than three searches, and a bundle's words are plain things and places, never a person.
func TestPhotoWordsAreFewAndPlain(t *testing.T) {
	people := []string{"person", "people", "man", "woman", "girl", "boy", "child", "kid", "portrait", "face", "team", "family", "couple", "crowd", "group", "selfie", "model", "owner", "staff", "customer"}
	kinds, err := loadKinds()
	if err != nil {
		t.Fatal(err)
	}
	for id, d := range kinds {
		for i, b := range d.bundles {
			q := d.photoWords(b)
			if len(q) > 3 {
				t.Errorf("%s bundle %d: %d search words", id, i, len(q))
			}
			for _, w := range q {
				for _, p := range people {
					if strings.Contains(" "+w+" ", " "+p+" ") {
						t.Errorf("%s bundle %d: %q asks for a person (%s)", id, i, w, p)
					}
				}
			}
		}
	}
}

// With photos every slot of the plan is a .jpg file of the site that a page shows, and the pages stay valid; with none the site is
// the drawn one, byte for byte.
func TestGenerateWithPhotos(t *testing.T) {
	for _, kind := range kindIDs(t) {
		t.Run(kind, func(t *testing.T) {
			got := 0
			for i := 0; i < 40; i++ {
				seed := testSeed(i)
				plan, _ := Plan(kind, seed)
				plain := mustGenerate(t, kind, i)
				for _, o := range []Options{{}, {Photos: map[string][]byte{}}} {
					files, _, err := GenerateWith(kind, seed, o)
					if err != nil || !sameFiles(files, plain) {
						t.Fatalf("seed %d: empty options changed the site (%v)", i, err)
					}
				}
				if len(plan) == 0 {
					continue
				}
				got++
				for _, ph := range []map[string][]byte{photosFor(t, plan), photosFor(t, plan[:1])} { // all, then only the first
					files, _, err := GenerateWith(kind, seed, Options{Photos: ph})
					if err != nil {
						t.Fatal(err)
					}
					b, err := decoy.FromFiles(files)
					if err != nil {
						t.Fatalf("seed %d: %v", i, err)
					}
					has, html := map[string]bool{}, ""
					jpgs := 0
					for _, f := range files {
						has[f.Path] = true
						if path.Ext(f.Path) == ".jpg" {
							jpgs++
						}
					}
					for _, f := range files {
						if path.Ext(f.Path) == ".html" {
							html += string(f.Content)
							for _, l := range checkPage(t, fmt.Sprintf("seed %d %s", i, f.Path), f.Content) {
								if p := strings.TrimPrefix(l, "/"); strings.HasPrefix(l, "/") && !has[p] && !has[path.Join(p, "index.html")] && !strings.HasPrefix(l, "/#") {
									// a link to a page is checked by TestGenerateValid; here only the photos matter
									if path.Ext(l) == ".jpg" {
										t.Errorf("seed %d: %s shows %s, which is not in the site", i, f.Path, l)
									}
								}
							}
						}
					}
					if jpgs != len(ph) {
						t.Errorf("seed %d: %d photos in the site, gave %d", i, jpgs, len(ph))
					}
					for _, f := range files {
						if path.Ext(f.Path) == ".jpg" && !strings.Contains(html, f.Path) {
							t.Errorf("seed %d: no page shows %s", i, f.Path)
						}
					}
					if b.Size() > decoy.MaxBundleBytes {
						t.Errorf("seed %d: %d bytes", i, b.Size())
					}
				}
			}
			if got == 0 {
				t.Skipf("%s asks for no photo", kind)
			}
		})
	}
}

func sameFiles(a, b []File) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || !bytes.Equal(a[i].Content, b[i].Content) {
			return false
		}
	}
	return true
}
