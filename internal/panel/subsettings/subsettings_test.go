package subsettings

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDefaultsAreValidAndMatchThePlan(t *testing.T) {
	d := Defaults()
	if err := Validate(d); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	got := map[string]string{}
	for _, a := range d.Apps {
		got[a.Platform.String()+"/"+a.Kind.String()] = a.Name + " " + a.AddLinkTemplate
	}
	for k, want := range map[string]string{
		"PLATFORM_IOS/APP_HAPP": "Happ happ://add/{url}", "PLATFORM_ANDROID/APP_HAPP": "Happ happ://add/{url}",
		"PLATFORM_WINDOWS/APP_HAPP": "Happ happ://add/{url}", "PLATFORM_MACOS/APP_HAPP": "Happ happ://add/{url}",
		"PLATFORM_IOS/APP_AMNEZIA": "AmneziaVPN ", "PLATFORM_LINUX/APP_AMNEZIA": "AmneziaVPN ",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if _, linuxHapp := got["PLATFORM_LINUX/APP_HAPP"]; linuxHapp {
		t.Error("the defaults have no Happ on Linux")
	}
}

// An install that never saved settings (every existing instance at upgrade time) gets the defaults; a saved
// document round-trips, and an unreadable one makes the cache fall back instead of failing a fetch.
func TestLoadUpdateAndFallback(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if s, err := Load(ctx, st); err != nil || !proto.Equal(s, Defaults()) {
		t.Fatalf("fresh database: %v %v", s, err)
	}
	c := NewCache(st, nil)
	in := Defaults()
	in.Title = "  Мой VPN  "
	in.DefaultDnsPresetId = "dns_x" // not ours: stripped
	in.Rules = []*adminv1.ServeRule{{UaContains: " Mihomo ", Format: adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML}}
	out, err := c.Update(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Мой VPN" || out.Rules[0].UaContains != "Mihomo" || out.DefaultDnsPresetId != "" || in.Title != "  Мой VPN  " {
		t.Errorf("update: %v (input must stay untouched)", out)
	}
	if s, _ := Load(ctx, st); !proto.Equal(s, out) {
		t.Errorf("stored differs: %v vs %v", s, out)
	}
	if !proto.Equal(c.Get(ctx), out) {
		t.Error("the cache does not serve the update")
	}

	// An invalid update changes nothing.
	bad := Defaults()
	bad.SupportUrl = "javascript:x"
	if _, err := c.Update(ctx, bad); err == nil {
		t.Fatal("accepted")
	}
	if s, _ := Load(ctx, st); !proto.Equal(s, out) {
		t.Error("a refused update changed the stored settings")
	}

	// Corrupt row: Load errors, the cache serves the defaults.
	if err := st.SetSettings(ctx, map[string]string{Key: "{not json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, st); err == nil {
		t.Error("corrupt document loaded")
	}
	c.Invalidate()
	if !proto.Equal(c.Get(ctx), Defaults()) {
		t.Error("the cache did not fall back to the defaults")
	}

	// Unknown fields in a stored document (written by a newer panel) are ignored.
	if err := st.SetSettings(ctx, map[string]string{Key: `{"title":"t","from_the_future":{"a":1}}`}); err != nil {
		t.Fatal(err)
	}
	if s, err := Load(ctx, st); err != nil || s.Title != "t" {
		t.Errorf("unknown field: %v %v", s, err)
	}
}
