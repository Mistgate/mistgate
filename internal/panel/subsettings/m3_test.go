package subsettings

import (
	"context"
	"strings"
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
	if len(d.Rules) != 3 || r["mihomo"] != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML || r["clash"] != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML ||
		r["sing-box"] != adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON || d.Rules[2].UaContains != "sing-box" {
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
	want := Defaults() // and the mode of the device limit, which a panel that never got one counts only
	want.LinkDeviceLimit = adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_TRACK.Enum()
	if s, _ := Load(ctx, st); !proto.Equal(s, want) {
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

// ---- the sing-box rule ----

func hasRule(s *adminv1.SubscriptionSettings, ua string) (adminv1.SubFormat, int) {
	n := 0
	var f adminv1.SubFormat
	for _, r := range s.Rules {
		if strings.EqualFold(strings.TrimSpace(r.UaContains), ua) {
			n++
			f = r.Format
		}
	}
	return f, n
}

// An installation that saved settings before the sing-box profile gets the rule once, at the end; the marker keeps it from coming
// back after the admin deletes it, and a rule of the same text (any case, any format) is left alone.
func TestMigrateSingbox(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := st.SetSettings(ctx, map[string]string{Key: `{"title":"Old","rules":[{"ua_contains":"curl","format":"SUB_FORMAT_BASE64_URIS"},{"ua_contains":"mihomo","format":"SUB_FORMAT_MIHOMO_YAML"}]}`}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // idempotent
		if err := MigrateSingbox(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Old" || len(s.Rules) != 3 || s.Rules[0].UaContains != "curl" || s.Rules[1].UaContains != "mihomo" ||
		s.Rules[2].UaContains != "sing-box" || s.Rules[2].Format != adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON {
		t.Errorf("after the migration: %v", s.Rules)
	}
	if v, err := st.Setting(ctx, "sub_rules_singbox"); err != nil || v != "1" {
		t.Errorf("marker = %q, %v", v, err)
	}
	// The admin removes the rule; another start does not add it back.
	c := NewCache(st, nil)
	s.Rules = s.Rules[:2]
	if _, err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := MigrateSingbox(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(ctx, st); len(got.Rules) != 2 {
		t.Errorf("the rule came back: %v", got.Rules)
	}
}

// A rule of the owner with the same text is not duplicated or changed, whatever its format and case.
func TestMigrateSingboxKeepsTheOwnersRule(t *testing.T) {
	for _, stored := range []string{
		`{"rules":[{"ua_contains":"sing-box","format":"SUB_FORMAT_MIHOMO_YAML"}]}`,
		`{"rules":[{"ua_contains":" Sing-Box ","format":"SUB_FORMAT_BASE64_URIS"}]}`,
	} {
		ctx := context.Background()
		st := openStore(t)
		if err := st.SetSettings(ctx, map[string]string{Key: stored}); err != nil {
			t.Fatal(err)
		}
		if err := MigrateSingbox(ctx, st); err != nil {
			t.Fatal(err)
		}
		s, _ := Load(ctx, st)
		if _, n := hasRule(s, "sing-box"); n != 1 || len(s.Rules) != 1 || s.Rules[0].Format == adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON {
			t.Errorf("%s -> %v", stored, s.Rules)
		}
		if v, err := st.Setting(ctx, "sub_rules_singbox"); err != nil || v != "1" {
			t.Errorf("marker = %q, %v", v, err)
		}
	}
}

// A fresh install is given the marker only (Defaults() carries the rule); a corrupt document is not touched or marked; a full list
// stays within MaxRules; the cache runs both migrations in order (mihomo, clash, then sing-box).
func TestMigrateSingboxEdges(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := MigrateSingbox(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Setting(ctx, Key); err == nil {
		t.Error("a settings document was written for a fresh install")
	}
	if f, n := hasRule(Defaults(), "sing-box"); n != 1 || f != adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON {
		t.Errorf("Defaults() sing-box rule: %v x%d", f, n)
	}

	st = openStore(t)
	if err := st.SetSettings(ctx, map[string]string{Key: "{not json"}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateSingbox(ctx, st); err == nil {
		t.Fatal("a corrupt document was accepted")
	}
	if _, err := st.Setting(ctx, "sub_rules_singbox"); err == nil {
		t.Error("marked although nothing was migrated")
	}

	full := &adminv1.SubscriptionSettings{}
	for i := 0; i < MaxRules; i++ {
		full.Rules = append(full.Rules, &adminv1.ServeRule{UaContains: "x" + strings.Repeat("y", i), Format: adminv1.SubFormat_SUB_FORMAT_BASE64_URIS})
	}
	if addSingboxRule(full) || len(full.Rules) != MaxRules {
		t.Error("the rule list grew past MaxRules")
	}

	st = openStore(t)
	if err := st.SetSettings(ctx, map[string]string{Key: `{"title":"Old"}`}); err != nil {
		t.Fatal(err)
	}
	NewCache(st, nil)
	s, _ := Load(ctx, st)
	var uas []string
	for _, r := range s.Rules {
		uas = append(uas, r.UaContains)
	}
	if strings.Join(uas, ",") != "mihomo,clash,sing-box" {
		t.Errorf("rules after the cache started: %v", uas)
	}
}

// The boundary of Validate is the last format.
func TestValidateKnowsTheJSONFormats(t *testing.T) {
	for f, ok := range map[adminv1.SubFormat]bool{
		adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML: true, adminv1.SubFormat_SUB_FORMAT_XRAY_JSON: true, adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON: true,
		adminv1.SubFormat(7): false, adminv1.SubFormat_SUB_FORMAT_UNSPECIFIED: false,
	} {
		s := Defaults()
		s.Rules = []*adminv1.ServeRule{{UaContains: "x", Format: f}}
		if err := Validate(s); (err == nil) != ok {
			t.Errorf("%v: %v", f, err)
		}
	}
}
