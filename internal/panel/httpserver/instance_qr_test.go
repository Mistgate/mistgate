package httpserver

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/adminqr"
)

// The text EncodeQr is given is a credential (a subscription link, a device config with its private key, a proxy link): it is
// drawn for any signed-in admin, closed to anyone else, limited in length, and never reaches the logs or the audit trail.
func TestEncodeQr(t *testing.T) {
	var logs bytes.Buffer
	e := newTestEnv(t, func(c *Config) {
		c.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx := context.Background()
	base := e.admin.URL + "/"
	owner := newBrowser(t, base, "")
	if err := owner.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	encode := func(c interface {
		EncodeQr(context.Context, *connect.Request[adminv1.EncodeQrRequest]) (*connect.Response[adminv1.EncodeQrResponse], error)
	}, text string) (*adminv1.QrCode, error) {
		r, err := c.EncodeQr(ctx, connect.NewRequest(&adminv1.EncodeQrRequest{Text: text}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Qr, nil
	}
	secret := "https://example.com/s3cr3t-prefix/sub/token-xyz-0123456789"

	// the owner gets the modules, and so does the lowest role the policy has (levelRead covers every signed-in admin)
	q, err := encode(instanceClient(owner.jar, base), secret)
	if err != nil || q == nil || q.N < 21 || (q.N-17)%4 != 0 || len(q.Bits) != int((q.N*q.N+7)/8) {
		t.Fatalf("owner: %+v %v", q, err)
	}
	if q.Window <= 0 || q.Window > float64(q.N)/2 {
		t.Errorf("a code of %d modules for a link should leave a window for the logo, got %v", q.N, q.Window)
	}
	helper := instanceClient(&cookieJar{cookie: helperSession(t, e.st)}, base)
	if h, err := encode(helper, secret); err != nil || h == nil || h.N != q.N || !bytes.Equal(h.Bits, q.Bits) {
		t.Errorf("helper gets another code: %v", err)
	}
	// without a session, nothing
	if _, err := encode(instanceClient(&cookieJar{}, base), secret); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous EncodeQr: %v", err)
	}

	// the limit: empty and over 4096 bytes are refused; a text no QR code holds (over 1663 bytes at level Q) is no code, not an error
	for name, text := range map[string]string{"empty": "", "over the limit": strings.Repeat("a", adminqr.MaxText+1)} {
		if _, err := encode(helper, text); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r, err := encode(helper, strings.Repeat("a", 2000)); err != nil || r != nil {
		t.Errorf("a text too long for a code: %v %v", r, err)
	}
	if r, err := encode(helper, strings.Repeat("a", adminqr.MaxText)); err != nil || r != nil {
		t.Errorf("a text at the limit: %v %v", r, err)
	}

	// the text is neither logged nor audited, in a refusal either
	_, _ = encode(instanceClient(&cookieJar{}, base), secret)
	_, _ = encode(helper, strings.Repeat("a", adminqr.MaxText+1))
	for _, s := range []string{secret, "s3cr3t-prefix", "token-xyz"} {
		if strings.Contains(logs.String(), s) {
			t.Errorf("the log holds %q", s)
		}
	}
	var rows int
	if err := e.st.R.QueryRowContext(ctx, `SELECT count(*) FROM audit WHERE params LIKE '%s3cr3t%' OR action LIKE '%qr%'`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("audit rows that name the text: %d %v", rows, err)
	}
}
