package httpserver

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func instanceClient(jar *cookieJar, adminBase string) adminv1connect.InstanceServiceClient {
	return adminv1connect.NewInstanceServiceClient(&http.Client{Transport: jar}, adminBase+"api", connect.WithProtoJSON())
}

func sp(s string) *string { return &s }

// helperSession signs in a non-owner admin by writing the session row directly, and
// returns the cookie to send.
func helperSession(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_helper', 'Helper', 'helper', x'0102', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("helper-token"))
	if err := st.CreateSession(ctx, store.Session{TokenHash: h[:], AdminID: "adm_helper", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return auth.CookieName + "=helper-token"
}

const evilLogo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" onload="alert(1)">
<script>alert(2)</script><foreignObject><iframe src="//evil.example"/></foreignObject>
<path d="M0 0L10 10" fill="#b8acf2" onclick="alert(3)"/><image href="https://evil.example/x.png"/></svg>`

func TestInstanceService(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.AdminURL, c.SubscriptionBase = "https://admin.example.com/k7q2/", "https://example.com/s3cr3t"
	})
	ctx := context.Background()
	base := e.admin.URL + "/"
	owner := newBrowser(t, base, "")
	if err := owner.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	inst := instanceClient(owner.jar, base)
	get := func(c adminv1connect.InstanceServiceClient) (*adminv1.Instance, error) {
		r, err := c.GetInstance(ctx, connect.NewRequest(&adminv1.GetInstanceRequest{}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Instance, nil
	}
	update := func(m *adminv1.UpdateInstanceRequest) (*adminv1.Instance, error) {
		r, err := inst.UpdateInstance(ctx, connect.NewRequest(m))
		if err != nil {
			return nil, err
		}
		return r.Msg.Instance, nil
	}

	// Anonymous callers get nothing from either RPC.
	anon := instanceClient(&cookieJar{}, base)
	if _, err := get(anon); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous GetInstance: %v", err)
	}
	if _, err := anon.UpdateInstance(ctx, connect.NewRequest(&adminv1.UpdateInstanceRequest{Accent: sp("#112233")})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous UpdateInstance: %v", err)
	}

	got, err := get(inst)
	if err != nil || got.BrandHead != "Mist" || got.BrandTail != "gate" || got.Accent != "#b8acf2" || got.Language != "en" || got.HasLogo || got.LogoVersion != "" {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	// The owner sees where the admin and the subscription links live (Settings -> Domains), read-only.
	if got.AdminUrl != "https://admin.example.com/k7q2/" || got.SubscriptionBase != "https://example.com/s3cr3t" {
		t.Errorf("owner's addresses: %q %q", got.AdminUrl, got.SubscriptionBase)
	}
	got, err = update(&adminv1.UpdateInstanceRequest{BrandHead: sp("Fog"), Accent: sp("#7DD3A0"), Language: sp("ru")})
	if err != nil || got.BrandHead != "Fog" || got.BrandTail != "gate" || got.Accent != "#7dd3a0" || got.Language != "ru" {
		t.Fatalf("update: %+v %v", got, err)
	}
	// The public sign-in info follows.
	info, _ := newBrowser(t, base, "").api.GetLoginInfo(ctx, connect.NewRequest(&adminv1.GetLoginInfoRequest{}))
	if info.Msg.BrandHead != "Fog" || info.Msg.BrandTail != "gate" || info.Msg.Accent != "#7dd3a0" || info.Msg.Language != "ru" || info.Msg.HasLogo {
		t.Errorf("login info: %+v", info.Msg)
	}

	// Validation: nothing is applied from a bad request.
	for name, m := range map[string]*adminv1.UpdateInstanceRequest{
		"accent":          {Accent: sp("green"), BrandHead: sp("Changed")},
		"language":        {Language: sp("fr")},
		"markup in brand": {BrandHead: sp("<script>")},
		"empty brand":     {BrandHead: sp("")},
		"bad svg":         {LogoSvg: sp("<svg><")},
		"huge svg":        {LogoSvg: sp(strings.Repeat("x", 70<<10))},
	} {
		if _, err := update(m); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := update(&adminv1.UpdateInstanceRequest{LogoSvg: sp(strings.Repeat("x", 300<<10))}); err == nil {
		t.Error("300 KB request accepted")
	}
	if got, _ := get(inst); got.BrandHead != "Fog" {
		t.Errorf("rejected request changed the settings: %+v", got)
	}

	// No logo yet: 404 everywhere it is served.
	for _, u := range []string{e.admin.URL + "/brand/logo.svg", e.public.URL + testPrefix + "brand/logo.svg"} {
		if r := do(t, http.MethodGet, u, "", nil); r.status != 404 {
			t.Errorf("%s without a logo: %d", u, r.status)
		}
	}

	// Upload a hostile SVG: it is sanitised, and served inert.
	got, err = update(&adminv1.UpdateInstanceRequest{LogoSvg: sp(evilLogo)})
	if err != nil || !got.HasLogo || got.LogoVersion == "" {
		t.Fatalf("logo upload: %+v %v", got, err)
	}
	version := got.LogoVersion
	for name, u := range map[string]struct{ url, host string }{
		"listener": {e.admin.URL + "/brand/logo.svg", ""},
		"prefix":   {e.public.URL + testPrefix + "brand/logo.svg", ""},
		"host":     {e.public.URL + "/brand/logo.svg", testAdminHst},
	} {
		hdr := map[string]string{}
		if u.host != "" {
			hdr["Host"] = u.host
		}
		r := do(t, http.MethodGet, u.url, "", hdr) // no cookie: it is public
		if r.status != 200 || r.header.Get("Content-Type") != "image/svg+xml" || r.header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(r.header.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(r.header.Get("Content-Security-Policy"), "sandbox") ||
			r.header.Get("Cache-Control") != "no-cache" || r.header.Get("ETag") != `"`+version+`"` {
			t.Errorf("%s: %d %v", name, r.status, r.header)
		}
		low := strings.ToLower(r.body)
		for _, bad := range []string{"script", "onload", "onclick", "foreignobject", "iframe", "evil", "<image", "alert"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s: %q survived in %s", name, bad, r.body)
			}
		}
		if !strings.Contains(r.body, `<path d="M0 0L10 10" fill="#b8acf2">`) || !strings.HasPrefix(r.body, `<svg xmlns="http://www.w3.org/2000/svg"`) {
			t.Errorf("%s: the logo itself is gone: %s", name, r.body)
		}
		// Conditional GET, HEAD, other methods.
		if r := do(t, http.MethodGet, u.url, "", mergeHdr(hdr, "If-None-Match", `"`+version+`"`)); r.status != 304 || r.body != "" {
			t.Errorf("%s revalidation: %d", name, r.status)
		}
		if r := do(t, http.MethodHead, u.url, "", hdr); r.status != 200 || r.body != "" {
			t.Errorf("%s HEAD: %d", name, r.status)
		}
		if r := do(t, http.MethodPost, u.url, "", hdr); r.status != 404 {
			t.Errorf("%s POST: %d", name, r.status)
		}
	}
	if info, _ := newBrowser(t, base, "").api.GetLoginInfo(ctx, connect.NewRequest(&adminv1.GetLoginInfoRequest{})); !info.Msg.HasLogo {
		t.Error("login info does not know about the logo")
	}
	// The same handler serves under the subscription prefix (mounted by the integrator).
	sub := httptest.NewServer(NewLogoHandler(e.st, quietLog))
	defer sub.Close()
	if r := do(t, http.MethodGet, sub.URL, "/", nil); r.status != 200 || r.header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("standalone logo handler: %d %v", r.status, r.header)
	}

	// A new logo gets a new version; "" removes it.
	got, _ = update(&adminv1.UpdateInstanceRequest{LogoSvg: sp(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`)})
	if got.LogoVersion == version || got.LogoVersion == "" {
		t.Errorf("logo version did not change: %q", got.LogoVersion)
	}
	got, _ = update(&adminv1.UpdateInstanceRequest{LogoSvg: sp("")})
	if got.HasLogo || got.LogoVersion != "" {
		t.Errorf("logo not removed: %+v", got)
	}
	if r := do(t, http.MethodGet, e.admin.URL+"/brand/logo.svg", "", nil); r.status != 404 {
		t.Errorf("removed logo: %d", r.status)
	}

	// Only the owner changes the instance; any admin can read it.
	helper := &cookieJar{cookie: helperSession(t, e.st)}
	hc := instanceClient(helper, base)
	if got, err := get(hc); err != nil || got.BrandHead != "Fog" || got.AdminUrl != "" || got.SubscriptionBase != "" {
		t.Errorf("helper GetInstance (no addresses for anyone but the owner): %+v %v", got, err)
	}
	if _, err := hc.UpdateInstance(ctx, connect.NewRequest(&adminv1.UpdateInstanceRequest{BrandHead: sp("Hacked")})); code(err) != connect.CodePermissionDenied {
		t.Errorf("helper UpdateInstance: %v", err)
	}
	if got, _ := get(inst); got.BrandHead != "Fog" {
		t.Error("helper changed the brand")
	}

	// Changes are audited with the field names, not the values.
	rows, _ := e.st.ListAudit(ctx, "", 0, 100)
	n := 0
	for _, r := range rows {
		if r.Action == "instance_update" {
			n++
			if r.Actor == "" || strings.Contains(r.Params, "svg") && strings.Contains(r.Params, "<") || r.Source != "panel" {
				t.Errorf("audit row %+v", r)
			}
		}
	}
	if n != 4 {
		t.Errorf("%d instance_update audit rows", n)
	}
}

func mergeHdr(h map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}
