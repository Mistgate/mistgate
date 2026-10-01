package subsettings

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func rulesOf(s *adminv1.SubscriptionSettings) map[string]adminv1.SubFormat {
	m := map[string]adminv1.SubFormat{}
	for _, r := range s.Rules {
		m[r.UaContains] = r.Format
	}
	return m
}

// A fresh install gets the Mihomo rules and the device self-service switch from the defaults.
func TestM3Defaults(t *testing.T) {
	d := Defaults()
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	r := rulesOf(d)
	if len(d.Rules) != 2 || r["mihomo"] != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML || r["clash"] != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML {
		t.Errorf("rules = %v", d.Rules)
	}
	if !SelfService(d) {
		t.Error("self-service must be on by default")
	}
}

// Settings stored before the key existed have no allow_device_self_service key: that is "on", only an explicit false turns it off.
func TestSelfServiceAbsentMeansOn(t *testing.T) {
	if !SelfService(nil) || !SelfService(&adminv1.SubscriptionSettings{}) || !SelfService(&adminv1.SubscriptionSettings{UserPage: &adminv1.UserPageOptions{}}) {
		t.Error("absent must be on")
	}
	off := &adminv1.SubscriptionSettings{UserPage: &adminv1.UserPageOptions{AllowDeviceSelfService: proto.Bool(false)}}
	if SelfService(off) {
		t.Error("an explicit false must be off")
	}
	// The stored form round-trips the three states.
	st := openStore(t)
	ctx := context.Background()
	c := NewCache(st, nil)
	in := Defaults()
	in.UserPage.AllowDeviceSelfService = proto.Bool(false)
	if _, err := c.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); SelfService(s) {
		t.Error("false did not survive a save")
	}
	if err := st.SetSettings(ctx, map[string]string{Key: `{"title":"t","user_page":{"show_qr":true}}`}); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); !SelfService(s) {
		t.Error("a stored document without the key must read as on")
	}
}

// An installation that saved settings before the Mihomo rules existed gets them once, at the start; the marker keeps them from
// coming back after the admin deletes them, and an existing rule of the same User-Agent text is left alone.
func TestMigrateM3(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	// Settings as an older panel stored them: a rule of their own, a Clash rule the admin pointed at the base64 list.
	if err := st.SetSettings(ctx, map[string]string{Key: `{"title":"Old","rules":[{"ua_contains":"curl","format":"SUB_FORMAT_BASE64_URIS"},{"ua_contains":"Clash","format":"SUB_FORMAT_BASE64_URIS"}]}`}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateM3(ctx, st); err != nil {
		t.Fatal(err)
	}
	s, err := Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Old" || len(s.Rules) != 3 || s.Rules[0].UaContains != "curl" || s.Rules[1].UaContains != "Clash" || s.Rules[1].Format != adminv1.SubFormat_SUB_FORMAT_BASE64_URIS ||
		s.Rules[2].UaContains != "mihomo" || s.Rules[2].Format != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML {
		t.Errorf("after the migration: %v", s.Rules)
	}
	if v, err := st.Setting(ctx, "sub_rules_m3"); err != nil || v != "1" {
		t.Errorf("marker = %q, %v", v, err)
	}

	// The admin removes the rules; another start does not add them back.
	c := NewCache(st, nil)
	s.Rules = nil
	if _, err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := MigrateM3(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(ctx, st); len(got.Rules) != 0 {
		t.Errorf("the rules came back: %v", got.Rules)
	}
}

// A fresh install (nothing stored) is not given a stored document, only the marker: Defaults() carries the rules.
func TestMigrateM3FreshInstall(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := MigrateM3(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := MigrateM3(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Setting(ctx, Key); err == nil {
		t.Error("a settings document was written for a fresh install")
	}
	if v, err := st.Setting(ctx, "sub_rules_m3"); err != nil || v != "1" {
		t.Errorf("marker = %q, %v", v, err)
	}
	if s, _ := Load(ctx, st); !proto.Equal(s, Defaults()) {
		t.Error("a fresh install no longer serves the defaults")
	}
}

// An unreadable stored document is not touched and not marked: the next start tries again.
func TestMigrateM3CorruptDocument(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := st.SetSettings(ctx, map[string]string{Key: "{not json"}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateM3(ctx, st); err == nil {
		t.Fatal("a corrupt document was accepted")
	}
	if v, _ := st.Setting(ctx, Key); v != "{not json" {
		t.Errorf("the document was rewritten: %q", v)
	}
	if _, err := st.Setting(ctx, "sub_rules_m3"); err == nil {
		t.Error("marked although nothing was migrated")
	}
}
