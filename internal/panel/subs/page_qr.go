package subs

import (
	"encoding/base64"

	"github.com/mistgate/mistgate/internal/qr"
)

// pageQR is a QR code the page draws, made here instead of in the browser: the page then carries no encoder (about 40 KB less
// of script) and draws the code from the modules it is told. It is the modules, not an image: the same code as an SVG path is
// about thirty times the size (28 KB for a link, 300 KB for a key), as bits it is a few hundred bytes.
type pageQR struct {
	N    int    `json:"n"`    // modules a side, 4*version+17
	Bits string `json:"bits"` // N*N modules row by row, eight to a byte, first in the high bit, 1 = dark; base64
	// Window is the side, in modules, of the clear square in the middle the page puts its logo in (0: no logo). It is the
	// board's size (qr.LogoWindow), cut down where the code cannot spare that much error correction (qr.FitWindow).
	Window float64 `json:"window"`
}

// pageQROf draws text at level Q (the one a logo in the middle needs). nil for "" and for a text too long for a QR code: the
// page then shows no code and says so where it matters (the key's dialog).
func pageQROf(text string) *pageQR {
	if text == "" {
		return nil
	}
	c, err := qr.Encode([]byte(text), qr.Q)
	if err != nil {
		return nil
	}
	return &pageQR{N: c.Size, Bits: base64.StdEncoding.EncodeToString(c.Packed()), Window: c.FitWindow(qr.LogoWindow(c.Size))}
}
