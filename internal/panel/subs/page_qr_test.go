package subs_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/qr"
)

// The page does not encode: the server hands it the modules of every code it draws, and the page only paints them.

// qrMatches reports whether the page's code q is the QR code of text at level Q, module for module, with the window the
// board's size allows.
func qrMatches(t *testing.T, q any, text string) {
	t.Helper()
	m, ok := q.(map[string]any)
	if !ok {
		t.Fatalf("no code for %q: %v", text, q)
	}
	want, err := qr.Encode([]byte(text), qr.Q)
	if err != nil {
		t.Fatal(err)
	}
	if n := int(m["n"].(float64)); n != want.Size {
		t.Fatalf("a code %d modules wide, want %d", n, want.Size)
	}
	bits, err := base64.StdEncoding.DecodeString(m["bits"].(string))
	if err != nil || len(bits) != (want.Size*want.Size+7)/8 {
		t.Fatalf("bits: %d bytes, %v", len(bits), err)
	}
	for y := 0; y < want.Size; y++ {
		for x := 0; x < want.Size; x++ {
			i := y*want.Size + x
			if got := bits[i/8]>>(7-i%8)&1 == 1; got != want.Black(x, y) {
				t.Fatalf("module %d,%d of the code of %q", x, y, text)
			}
		}
	}
	if w := m["window"].(float64); w != want.FitWindow(qr.LogoWindow(want.Size)) || w <= 0 {
		t.Errorf("window %v, want %v", w, want.FitWindow(qr.LogoWindow(want.Size)))
	}
}

func TestPageCarriesTheCodesItDraws(t *testing.T) {
	m := newTGRig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	_, tok := m.person("alice", true)
	fetch(h, "/"+tok, happUA)

	d, raw := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	link := d["subscription_url"].(string)
	qrMatches(t, d["qr"], link)
	links := d["telegram"].(map[string]any)["links"].([]any)
	if len(links) == 0 {
		t.Fatal("no Telegram link")
	}
	for _, l := range links {
		qrMatches(t, l.(map[string]any)["qr"], l.(map[string]any)["url"].(string))
	}
	if strings.Contains(raw, "<svg") || strings.Contains(raw, `<svg`) {
		t.Error("the page data carries markup for a code, not modules")
	}

	// the owner switched the QR code off: no code is drawn for the link (and none is sent)
	set := subsettings.Defaults()
	set.UserPage.ShowQr = false
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	d, _ = pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if d["qr"] != nil {
		t.Errorf("QR code off, the page still carries %v", d["qr"])
	}
}
