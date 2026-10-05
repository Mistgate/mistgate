package subsettings

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The choice of DNS per server is a new feature: it is off in a fresh install and for settings stored before the key existed
// (the opposite of the device switch, which was on before it had a key), and an explicit true is what turns it on.
func TestDNSChoiceAbsentMeansOff(t *testing.T) {
	if DNSChoice(nil) || DNSChoice(&adminv1.SubscriptionSettings{}) || DNSChoice(&adminv1.SubscriptionSettings{UserPage: &adminv1.UserPageOptions{}}) {
		t.Error("absent must be off")
	}
	if DNSChoice(Defaults()) {
		t.Error("a fresh install must not switch it on")
	}
	if err := Validate(Defaults()); err != nil {
		t.Fatal(err)
	}
	st := openStore(t)
	ctx := context.Background()
	c := NewCache(st, nil)
	in := Defaults()
	in.UserPage.AllowDnsChoice = proto.Bool(true)
	if _, err := c.Update(ctx, in); err != nil { // validated like the rest of the document
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); !DNSChoice(s) {
		t.Error("true did not survive a save")
	}
	if got := c.Get(ctx); !DNSChoice(got) {
		t.Error("the cache lost it")
	}
	b, _ := st.Setting(ctx, Key)
	if !strings.Contains(b, `"allow_dns_choice":true`) && !strings.Contains(b, `"allow_dns_choice": true`) {
		t.Errorf("stored document: %s", b)
	}
	in.UserPage.AllowDnsChoice = proto.Bool(false)
	if _, err := c.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); DNSChoice(s) {
		t.Error("false did not survive a save")
	}
	if err := st.SetSettings(ctx, map[string]string{Key: `{"title":"t","user_page":{"show_qr":true}}`}); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); DNSChoice(s) {
		t.Error("a stored document without the key must read as off")
	}
	// Saving another field of a document without the key does not turn it on.
	s, _ := Load(ctx, st)
	s.Title = "other"
	if _, err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(ctx, st); DNSChoice(s) {
		t.Error("a save of another field turned it on")
	}
}
