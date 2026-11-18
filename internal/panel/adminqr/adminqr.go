// Package adminqr makes the QR codes the admin panel draws: the server encodes, the page only draws the modules it is told
// (web/src/components/qr-code.tsx), as the person's page does (internal/panel/subs/page_qr.go).
package adminqr

import (
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/qr"
)

// MaxText is the longest text EncodeQr takes, in bytes. A code holds 1663 at the level used here, so the rest of the limit is
// only there to refuse a body that is not a text for a QR code at all.
const MaxText = 4096

// Encode draws text at level Q, the one a logo in the middle needs, with the window the board gives the logo (cut down
// where the code cannot spare the error correction). nil for "" and for a text too long for any QR code.
func Encode(text string) *adminv1.QrCode {
	if text == "" {
		return nil
	}
	c, err := qr.Encode([]byte(text), qr.Q)
	if err != nil {
		return nil
	}
	return &adminv1.QrCode{N: uint32(c.Size), Bits: c.Packed(), Window: c.FitWindow(qr.LogoWindow(c.Size))}
}
