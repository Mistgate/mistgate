package auth

import (
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Nothing a token can call returns a secret (a subscription link, a page password, a device key or config, a server
// password, a WARP key). Every field, at any depth, of every response a token can reach (Direct and Approved) whose name
// looks like one is either listed here with the reason it is safe, or the test fails: a new one needs a deliberate line.
func TestTokenReachableResponsesCarryNoSecret(t *testing.T) {
	looksSecret := regexp.MustCompile(`(^|_)(url|uri|link|password|passwd|secret|private|psk|conf|config|vpn|token|key|settings)(_|$)`)
	i18nKey := regexp.MustCompile(`^(title|why|error|pause)_key$`) // a message key the UI translates
	safe := map[protoreflect.FullName]string{
		"mistgate.admin.v1.CreateUserResponse.subscription_url": "left empty for a token (UserService.CreateUser; TestMCPEndToEnd calls it with one)",
		"mistgate.admin.v1.CreateUserResponse.page_password":    "left empty for a token, like subscription_url",
		"mistgate.admin.v1.GetProfileResponse.settings_json":    "x-secret values are masked (protocols.MaskSecrets)",
		"mistgate.admin.v1.ListUsersResponse.next_page_token":   "a page cursor (a user name)",
		"mistgate.admin.v1.PanelBuild.has_release_key":          "a bool",
		"mistgate.admin.v1.PanelBuild.release_key_fingerprint":  "the fingerprint of the public release key",
		"mistgate.admin.v1.PanelUpdate.url":                     "a public link to the official GitHub release",
		"mistgate.admin.v1.NodeServerAccess.password_generated": "a bool: whether the panel generated the saved password",
		// The subscription page every user sees (SubscriptionService.Get/UpdateSubscriptionSettings): names, texts and public
		// app links, no user's link or password.
		"mistgate.admin.v1.GetSubscriptionSettingsResponse.settings":    "the shared page's settings, checked field by field below",
		"mistgate.admin.v1.UpdateSubscriptionSettingsResponse.settings": "the same settings after a save",
		"mistgate.admin.v1.SubscriptionSettings.support_url":            "the public support link every user's page shows",
		"mistgate.admin.v1.PlatformApp.download_url":                    "a public app download link every user's page shows",
		"mistgate.admin.v1.PlatformApp.add_link_template":               "a template with placeholders; the subscription URL is filled in only on the user's own page",
		"mistgate.admin.v1.UserPageOptions.require_page_password":       "a bool: whether the pages ask for a password",
		"mistgate.admin.v1.WarpAccount.peer_public_key":                 "a public key; private keys are never returned",
		"mistgate.admin.v1.WarpAccount.has_token":                       "a bool: whether refresh is available, not a token",
		"mistgate.admin.v1.WarpAccount.tos_url":                         "the public Cloudflare terms link",
		"mistgate.admin.v1.GetWarpResponse.tos_url":                     "the public Cloudflare terms link",
	}
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor, via string)
	walk = func(md protoreflect.MessageDescriptor, via string) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		for i := 0; i < md.Fields().Len(); i++ {
			f := md.Fields().Get(i)
			name := string(f.Name())
			if _, ok := safe[f.FullName()]; !ok && !md.IsMapEntry() && !i18nKey.MatchString(name) && looksSecret.MatchString(name) {
				t.Errorf("%s returns %s, which looks like a secret", via, f.FullName())
			}
			if f.Message() != nil {
				walk(f.Message(), via)
			}
		}
	}
	for path := range tokenProcedures {
		svc, method, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		m := d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(method))
		if m == nil {
			t.Fatalf("%s: no such method", path)
		}
		walk(m.Output(), path)
	}
}
