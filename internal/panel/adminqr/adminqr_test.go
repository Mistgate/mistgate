package adminqr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/qr"
)

func TestEncode(t *testing.T) {
	if Encode("") != nil {
		t.Error("an empty text is no code")
	}
	if Encode(strings.Repeat("a", 1664)) != nil { // level Q holds 1663 bytes
		t.Error("a text over the capacity is no code")
	}
	text := "otpauth://totp/Mistgate:alice?secret=JBSWY3DPEHPK3PXP&issuer=Mistgate"
	got := Encode(text)
	c, err := qr.Encode([]byte(text), qr.Q)
	if err != nil || got == nil {
		t.Fatal(err, got)
	}
	if int(got.N) != c.Size || !bytes.Equal(got.Bits, c.Packed()) || got.Window != c.FitWindow(qr.LogoWindow(c.Size)) {
		t.Errorf("not the code of internal/qr at level Q: n=%d window=%v", got.N, got.Window)
	}
	if got.Window <= 0 {
		t.Errorf("a code of %d modules should leave room for the logo", got.N)
	}
	if small := Encode("hi"); small == nil || small.N != 21 || small.Window > 21.0/2 {
		t.Errorf("a tiny text: %+v", small)
	}
}
