package httpserver

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// fakeDNS stands in for the DNS module: one known preset id.
type fakeDNS struct{ id string }

func (f *fakeDNS) DefaultPresetID(context.Context) (string, error) { return f.id, nil }
func (f *fakeDNS) SetDefaultPresetID(_ context.Context, id string) error {
	if id != "" && id != "dns_ok" {
		return dns.ErrUnknownPreset
	}
	f.id = id
	return nil
}

func subscriptionEnv(t *testing.T) (*testEnv, adminv1connect.SubscriptionServiceClient, func(cookie string) adminv1connect.SubscriptionServiceClient) {
	t.Helper()
	var inner atomic.Pointer[http.Handler]
	e := newTestEnv(t, func(c *Config) {
		c.AdminHandlers = append(c.AdminHandlers, AdminHandler{
			Path: "/mistgate.admin.v1.SubscriptionService/",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				(*inner.Load()).ServeHTTP(w, r)
			}),
		})
	})
	cache := subsettings.NewCache(e.st, quietLog)
	brand := func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, e.st) }
	_, h := subs.NewService(e.st, cache, builtin.Registry(), brand, &fakeDNS{}, quietLog, nil).Handler()
	inner.Store(&h)
	client := func(cookie string) adminv1connect.SubscriptionServiceClient {
		jar := &cookieJar{cookie: cookie}
		return adminv1connect.NewSubscriptionServiceClient(&http.Client{Transport: jar}, e.admin.URL+"/api", connect.WithProtoJSON())
	}
	return e, client(roleSession(t, e.st, store.RoleHelper)), client
}

func TestSubscriptionServiceRolesAndValidation(t *testing.T) {
	ctx := context.Background()
	e, helper, client := subscriptionEnv(t)
	readonly := client(roleSession(t, e.st, store.RoleReadonly))
	anon := client("")

	// Anonymous: nothing. Readonly: reads, no writes. Helper (admin): both.
	if _, err := anon.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous read: %v", err)
	}
	got, err := readonly.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))
	if err != nil {
		t.Fatalf("readonly read: %v", err)
	}
	s := got.Msg.Settings
	if got.Msg.EffectiveTitle != "Mistgate" || s.ServerNameTemplate != subsettings.DefaultNameTemplate || s.UpdateIntervalHours != 12 || len(s.Apps) != 11 {
		t.Errorf("fresh install: title %q template %q interval %d apps %d", got.Msg.EffectiveTitle, s.ServerNameTemplate, s.UpdateIntervalHours, len(s.Apps))
	}
	if _, err := readonly.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: s})); code(err) != connect.CodePermissionDenied {
		t.Errorf("readonly write: %v", err)
	}
	if _, err := readonly.TestUserAgent(ctx, connect.NewRequest(&adminv1.TestUserAgentRequest{UserAgent: "curl/8"})); err != nil {
		t.Errorf("readonly TestUserAgent: %v", err)
	}
	if _, err := readonly.ListClients(ctx, connect.NewRequest(&adminv1.ListClientsRequest{})); err != nil {
		t.Errorf("readonly ListClients: %v", err)
	}

	// A valid edit is stored, audited without its texts, and read back (and feeds effective_title).
	s.Title, s.Announcement = "My VPN", "secret announcement text"
	s.Rules = []*adminv1.ServeRule{{UaContains: "mihomo", Format: adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML}}
	s.DefaultDnsPresetId = "dns_ok"
	up, err := helper.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: s}))
	if err != nil {
		t.Fatalf("helper write: %v", err)
	}
	if up.Msg.Settings.Title != "My VPN" || up.Msg.Settings.DefaultDnsPresetId != "dns_ok" {
		t.Errorf("update echo: %v", up.Msg.Settings)
	}
	again, _ := readonly.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))
	if again.Msg.EffectiveTitle != "My VPN" || again.Msg.Settings.Announcement != "secret announcement text" || again.Msg.Settings.DefaultDnsPresetId != "dns_ok" || len(again.Msg.Settings.Rules) != 1 {
		t.Errorf("read back: %v", again.Msg)
	}
	rows, err := e.st.ListAudit(ctx, "", 0, 10)
	if err != nil || len(rows) == 0 || rows[0].Action != "subscription_settings_update" || strings.Contains(rows[0].Params, "secret") {
		t.Errorf("audit: %v %+v", err, rows)
	}
	if ua, _ := readonly.TestUserAgent(ctx, connect.NewRequest(&adminv1.TestUserAgentRequest{UserAgent: "Mihomo/1.19"})); ua.Msg.RuleIndex != 0 || ua.Msg.Format != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML {
		t.Errorf("TestUserAgent rule: %v", ua.Msg)
	}
	if ua, _ := readonly.TestUserAgent(ctx, connect.NewRequest(&adminv1.TestUserAgentRequest{UserAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"})); ua.Msg.RuleIndex != -1 || !ua.Msg.Browser || ua.Msg.Format != adminv1.SubFormat_SUB_FORMAT_USER_PAGE {
		t.Errorf("TestUserAgent browser: %v", ua.Msg)
	}

	// Validation: each of these is refused and leaves the stored settings as they were.
	mod := func(f func(*adminv1.SubscriptionSettings)) *adminv1.SubscriptionSettings {
		c := proto.Clone(again.Msg.Settings).(*adminv1.SubscriptionSettings)
		f(c)
		return c
	}
	many := func(n int) []*adminv1.ServeRule {
		var r []*adminv1.ServeRule
		for range n {
			r = append(r, &adminv1.ServeRule{UaContains: "x", Format: adminv1.SubFormat_SUB_FORMAT_DECOY})
		}
		return r
	}
	for name, bad := range map[string]*adminv1.SubscriptionSettings{
		"javascript support url": mod(func(c *adminv1.SubscriptionSettings) { c.SupportUrl = "javascript:alert(1)" }),
		"ftp support url":        mod(func(c *adminv1.SubscriptionSettings) { c.SupportUrl = "ftp://example.com/" }),
		"support url with space": mod(func(c *adminv1.SubscriptionSettings) { c.SupportUrl = "https://example.com/a b" }),
		"download url not http":  mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].DownloadUrl = "happ://x" }),
		"unknown name placeholder": mod(func(c *adminv1.SubscriptionSettings) {
			c.ServerNameTemplate = "{flag} {token}"
		}),
		"unbalanced name template": mod(func(c *adminv1.SubscriptionSettings) { c.ServerNameTemplate = "{flag {node}" }),
		"unknown add placeholder":  mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].AddLinkTemplate = "happ://add/{sub}" }),
		"javascript add template":  mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].AddLinkTemplate = "javascript:alert({url})" }),
		"data add template":        mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].AddLinkTemplate = "data:text/html,{url}" }),
		"add template no scheme":   mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].AddLinkTemplate = "//evil.example/{url}" }),
		"app without a name":       mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].Name = " " }),
		"app unknown platform":     mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].Platform = 99 }),
		"app unknown kind":         mod(func(c *adminv1.SubscriptionSettings) { c.Apps[0].Kind = 0 }),
		"31 apps": mod(func(c *adminv1.SubscriptionSettings) {
			for len(c.Apps) < 31 {
				c.Apps = append(c.Apps, c.Apps[0])
			}
		}),
		"51 rules": mod(func(c *adminv1.SubscriptionSettings) { c.Rules = many(51) }),
		"rule without ua": mod(func(c *adminv1.SubscriptionSettings) {
			c.Rules = []*adminv1.ServeRule{{Format: adminv1.SubFormat_SUB_FORMAT_DECOY}}
		}),
		"rule no format":     mod(func(c *adminv1.SubscriptionSettings) { c.Rules = []*adminv1.ServeRule{{UaContains: "x"}} }),
		"huge announcement":  mod(func(c *adminv1.SubscriptionSettings) { c.Announcement = strings.Repeat("я", 1001) }),
		"huge interval":      mod(func(c *adminv1.SubscriptionSettings) { c.UpdateIntervalHours = 100000 }),
		"unknown dns preset": mod(func(c *adminv1.SubscriptionSettings) { c.DefaultDnsPresetId = "dns_nope" }),
		"newline in title":   mod(func(c *adminv1.SubscriptionSettings) { c.Title = "a\nb" }),
	} {
		if _, err := helper.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: bad})); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid_argument", name, err)
		}
	}
	// 50 rules and 30 apps are the limits themselves: allowed.
	ok := mod(func(c *adminv1.SubscriptionSettings) {
		c.Rules = many(50)
		c.DefaultDnsPresetId = ""
		for len(c.Apps) < 30 {
			c.Apps = append(c.Apps, c.Apps[0])
		}
	})
	if _, err := helper.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: ok})); err != nil {
		t.Errorf("50 rules and 30 apps: %v", err)
	}
}

func TestListClientsFromTheRegistry(t *testing.T) {
	_, helper, _ := subscriptionEnv(t)
	r, err := helper.ListClients(context.Background(), connect.NewRequest(&adminv1.ListClientsRequest{}))
	if err != nil || len(r.Msg.Clients) == 0 {
		t.Fatalf("%v %v", err, r)
	}
	c := r.Msg.Clients[0]
	for _, x := range r.Msg.Clients { // awg is registered too: its client (amnezia) sorts before happ
		if x.Id == "happ" {
			c = x
		}
	}
	if c.Id != "happ" || c.Name != "Happ" || len(c.Protocols) != 1 || c.Protocols[0] != "hysteria2" || len(c.Formats) != 1 || c.Formats[0] != adminv1.SubFormat_SUB_FORMAT_BASE64_URIS {
		t.Errorf("client: %v", c)
	}
}
