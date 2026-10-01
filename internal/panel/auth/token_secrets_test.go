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
