package subsettings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subhdr"
)

func hdr(name, value string) *adminv1.ResponseHeader {
	return &adminv1.ResponseHeader{Name: name, Value: value}
}

func withHeaders(list ...*adminv1.ResponseHeader) *adminv1.SubscriptionSettings {
	s := Defaults()
	s.ResponseHeaders = list
	return s
}

func TestResponseHeaderValidation(t *testing.T) {
	long := strings.Repeat("x", MaxHeaderValue+1)
	bad := map[string]*adminv1.ResponseHeader{
		"empty name":               hdr("", "v"),
		"space in the name":        hdr("flclashx servicename", "v"),
		"colon in the name":        hdr("flclashx:servicename", "v"),
		"slash in the name":        hdr("a/b", "v"),
		"non-ASCII name":           hdr("flclashx-имя", "v"),
		"name too long":            hdr(strings.Repeat("a", MaxHeaderName+1), "v"),
		"a panel header":           hdr("Profile-Title", "v"),
		"a panel header, any case": hdr("sUbScRiPtIoN-uSeRiNfO", "v"),
		"a family of the panel":    hdr("X-Hwid-Whatever", "v"),
		"content type":             hdr("Content-Type", "text/plain"),
		"content length":           hdr("content-length", "1"),
		"set cookie":               hdr("Set-Cookie", "a=b"),
		"location":                 hdr("Location", "https://example.com"),
		"hop by hop":               hdr("Transfer-Encoding", "chunked"),
		"empty value":              hdr("x-a", ""),
		"value with CR":            hdr("x-a", "a\rb"),
		"value with LF":            hdr("x-a", "a\nb"),
		"value with CRLF":          hdr("x-a", "a\r\nSet-Cookie: x=y"),
		"value with NUL":           hdr("x-a", "a\x00b"),
		"value with a tab":         hdr("x-a", "a\tb"),
		"value with DEL":           hdr("x-a", "a\x7fb"),
		"value with C1":            hdr("x-a", "a\u0085b"),
		"value too long":           hdr("x-a", long),
		"invalid UTF-8":            {Name: "x-a", Value: "a\xffb", Base64: true},
		"non-ASCII as is":          hdr("x-a", "Мой VPN"),
		"unknown format":           {Name: "x-a", Value: "v", Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_USER_PAGE}},
		"unspecified format":       {Name: "x-a", Value: "v", Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_UNSPECIFIED}},
		"repeated format":          {Name: "x-a", Value: "v", Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_XRAY_JSON, adminv1.SubFormat_SUB_FORMAT_XRAY_JSON}},
		"groups without the flag":  {Name: "x-a", Value: "v", GroupIds: []string{"grp_a"}},
		"malformed group":          {Name: "x-a", Value: "v", OnlyGroups: true, GroupIds: []string{"grp a"}},
		"repeated group":           {Name: "x-a", Value: "v", OnlyGroups: true, GroupIds: []string{"grp_a", "grp_a"}},
		"empty group":              {Name: "x-a", Value: "v", OnlyGroups: true, GroupIds: []string{""}},
	}
	for name, e := range bad {
		err := Validate(withHeaders(e))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted (err %v)", name, err)
		}
	}
	good := []*adminv1.ResponseHeader{
		hdr("flclashx-servicename", "My VPN"),
		hdr("X-Custom_1.2", "a b: c; d=e, f"),
		{Name: "flclashx-view", Value: "Мой VPN 🚀", Base64: true},
		{Name: "x-a", Value: "v", Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML, adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON}},
		{Name: "x-b", Value: strings.Repeat("x", MaxHeaderValue)},
		{Name: "x-c", Value: "v", OnlyGroups: true}, // none picked: nobody gets it, and that is a state a deleted group can leave
		{Name: "x-d", Value: "v", OnlyGroups: true, GroupIds: []string{"grp_abc", "grp-2"}},
	}
	for _, e := range good {
		if err := Validate(withHeaders(e)); err != nil {
			t.Errorf("%s: %v", e.Name, err)
		}
	}
}

func TestResponseHeaderLimits(t *testing.T) {
	var list []*adminv1.ResponseHeader
	for i := 0; i < MaxResponseHeaders; i++ {
		list = append(list, hdr("x-n"+string(rune('a'+i)), "v"))
	}
	if err := Validate(withHeaders(list...)); err != nil {
		t.Fatalf("%d entries: %v", len(list), err)
	}
	if err := Validate(withHeaders(append(list, hdr("x-one-more", "v"))...)); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d entries: %v", len(list)+1, err)
	}
	// the bytes of all the entries as sent
	var big []*adminv1.ResponseHeader
	for i := 0; i < 5; i++ {
		big = append(big, hdr("x-big"+string(rune('a'+i)), strings.Repeat("v", 900)))
	}
	if err := Validate(withHeaders(big...)); !errors.Is(err, ErrInvalid) {
		t.Errorf("5 x 900 bytes: %v", err)
	}
	// base64 counts as sent: 1000 bytes become 1340
	if err := Validate(withHeaders(
		&adminv1.ResponseHeader{Name: "x-a", Value: strings.Repeat("v", 1000), Base64: true},
		&adminv1.ResponseHeader{Name: "x-b", Value: strings.Repeat("v", 1000), Base64: true},
		&adminv1.ResponseHeader{Name: "x-c", Value: strings.Repeat("v", 1000), Base64: true},
		&adminv1.ResponseHeader{Name: "x-d", Value: strings.Repeat("v", 1000), Base64: true},
	)); !errors.Is(err, ErrInvalid) {
		t.Errorf("4 x 1000 bytes in base64: %v", err)
	}
}

func TestResponseHeadersFor(t *testing.T) {
	s := withHeaders(
		hdr("flclashx-servicename", "My VPN"),
		&adminv1.ResponseHeader{Name: "flclashx-view", Value: "Мой", Base64: true, Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML}},
		&adminv1.ResponseHeader{Name: "x-g", Value: "g", OnlyGroups: true, GroupIds: []string{"grp_a"}},
		&adminv1.ResponseHeader{Name: "x-nobody", Value: "n", OnlyGroups: true},
		hdr("X-Twice", "first"),
		hdr("x-twice", "second"),
	)
	names := func(group string, f adminv1.SubFormat) []string {
		var out []string
		for _, h := range ResponseHeadersFor(s, group, f) {
			out = append(out, h.Name+"="+h.Value)
		}
		return out
	}
	if got := names("grp_a", adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML); !slices.Equal(got, []string{"flclashx-servicename=My VPN", "flclashx-view=base64:0JzQvtC5", "x-g=g", "X-Twice=first"}) {
		t.Errorf("mihomo, group a: %q", got)
	}
	if got := names("grp_b", adminv1.SubFormat_SUB_FORMAT_BASE64_URIS); !slices.Equal(got, []string{"flclashx-servicename=My VPN", "X-Twice=first"}) {
		t.Errorf("links, group b: %q", got)
	}
	if got := names("", adminv1.SubFormat_SUB_FORMAT_XRAY_JSON); !slices.Equal(got, []string{"flclashx-servicename=My VPN", "X-Twice=first"}) {
		t.Errorf("xray, no group: %q", got)
	}
	if ResponseHeadersFor(Defaults(), "grp_a", adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML) != nil {
		t.Error("headers out of nothing")
	}
}

func TestNormalizeTrimsHeadersAndFoldsAllFormats(t *testing.T) {
	s := withHeaders(&adminv1.ResponseHeader{Name: "  x-a ", Value: " v\n ", Formats: append([]adminv1.SubFormat(nil), headerFormats...)})
	Normalize(s)
	e := s.ResponseHeaders[0]
	if e.Name != "x-a" || e.Value != "v" || len(e.Formats) != 0 {
		t.Errorf("normalized: %+v", e)
	}
	if err := Validate(s); err != nil {
		t.Error(err)
	}
}

// Stored settings are read through the same rule: what a save would refuse now is not served, the rest is.
func TestParseDropsWhatASaveWouldRefuse(t *testing.T) {
	s, err := Parse(`{"response_headers":[` +
		`{"name":"flclashx-servicename","value":"My VPN"},` +
		`{"name":"Content-Type","value":"text/plain"},` +
		`{"name":"x-a","value":"a\r\nSet-Cookie: x=y"},` +
		`{"name":"x-b","value":"fine"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range s.ResponseHeaders {
		got = append(got, e.Name)
	}
	if !slices.Equal(got, []string{"flclashx-servicename", "x-b"}) {
		t.Errorf("kept %q", got)
	}
}

func TestResponseHeadersAreStoredAndRead(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := NewCache(st, nil)
	in := withHeaders(
		&adminv1.ResponseHeader{Name: " flclashx-view ", Value: "Мой VPN", Base64: true, Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML}},
		&adminv1.ResponseHeader{Name: "x-g", Value: "g", OnlyGroups: true, GroupIds: []string{"grp_a"}},
	)
	if _, err := c.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(ctx, st)
	if err != nil || len(out.ResponseHeaders) != 2 {
		t.Fatalf("%v, %v", out, err)
	}
	if e := out.ResponseHeaders[0]; e.Name != "flclashx-view" || !e.Base64 || len(e.Formats) != 1 {
		t.Errorf("first: %+v", e)
	}
	if e := out.ResponseHeaders[1]; !e.OnlyGroups || len(e.GroupIds) != 1 || e.GroupIds[0] != "grp_a" {
		t.Errorf("second: %+v", e)
	}
	if _, err := c.Update(ctx, withHeaders(hdr("Cache-Control", "max-age=1"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("a panel header was stored: %v", err)
	}
	if got := c.Get(ctx).ResponseHeaders; len(got) != 2 {
		t.Errorf("a refused save changed what is served: %v", got)
	}
}

// The form of the admin (web/src/screens/subscriptions/headers.test.tsx) reads the same table and says the same.
func TestHeaderCasesMatchTheAdmin(t *testing.T) {
	b, err := os.ReadFile("testdata/header_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Reserved []string `json:"reserved"`
		Cases    []struct {
			Name, Value string
			Base64, OK  bool
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	if want := subhdr.ReservedNames(); !slices.Equal(table.Reserved, want) {
		t.Fatalf("testdata/header_cases.json reserved is out of date; it must be:\n%q", want)
	}
	for _, c := range table.Cases {
		s := withHeaders(&adminv1.ResponseHeader{Name: c.Name, Value: c.Value, Base64: c.Base64})
		Normalize(s)
		if err := Validate(s); (err == nil) != c.OK {
			t.Errorf("%q = %q (base64 %v): ok = %v, the panel says %v", c.Name, c.Value, c.Base64, c.OK, err)
		}
	}
}
