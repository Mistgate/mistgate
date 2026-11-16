package subhdr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// Every constant of the package is in Panel: a header that the code names and the owner may still take is the failure this
// package exists to prevent.
func TestPanelListsEveryConstant(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "subhdr.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var consts []string
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
			for _, sp := range g.Specs {
				for i := range sp.(*ast.ValueSpec).Names {
					lit := sp.(*ast.ValueSpec).Values[i].(*ast.BasicLit)
					consts = append(consts, strings.Trim(lit.Value, `"`))
				}
			}
		}
	}
	if len(consts) < 20 {
		t.Fatalf("read %d constants: the scan is broken", len(consts))
	}
	for _, c := range consts {
		if !slices.Contains(Panel, c) {
			t.Errorf("%s is a constant but not in Panel", c)
		}
	}
	for _, p := range Panel {
		if !slices.Contains(consts, p) {
			t.Errorf("%s is in Panel but not a constant", p)
		}
	}
}

func TestReserved(t *testing.T) {
	for _, name := range []string{
		// the issue's list
		"Subscription-Userinfo", "Profile-Title", "Profile-Update-Interval", "Profile-Web-Page-Url", "Announce", "Support-Url", "X-Hwid-Limit",
		"x-hwid-anything-new", "Routing", "Content-Type", "Cache-Control", "Set-Cookie",
		// any case
		"CONTENT-TYPE", "set-cookie", "content-length", "Content-Encoding", "LOCATION", "profile-title",
		// framing and hop by hop (RFC 9110 7.6.1)
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Proxy-Connection",
		// the platform and the proxies
		"Date", "Server", "Vary", "ETag", "Via", "Strict-Transport-Security", "X-Request-Id", "CF-Cache-Status", "Access-Control-Allow-Origin",
		"Sec-Fetch-Site", "X-Forwarded-For", "Content-Security-Policy", "Referrer-Policy", "Mistgate-Client-Api", "Mistgate-Anything",
		"Ping-Type", "Subscription-Ping-Onopen-Enabled", "Retry-After", "Alt-Svc", "Cross-Origin-Opener-Policy", "WWW-Authenticate",
	} {
		if !Reserved(name) {
			t.Errorf("%s is free to take", name)
		}
	}
	for _, name := range []string{"flclashx-servicename", "flclashx-view", "FlClashX-Settings", "X-Custom", "My-App-Name", "Hiddify-Note", "profile", "announcement"} {
		if Reserved(name) {
			t.Errorf("%s is reserved", name)
		}
	}
	for _, p := range Panel {
		if !Reserved(p) || !Reserved(strings.ToUpper(p)) || !Reserved(strings.ToLower(p)) {
			t.Errorf("%s: not reserved in every case", p)
		}
	}
}

// The list for the admin follows the rule: whatever it names is reserved, and a name under a prefix it shows is too.
func TestReservedNamesFollowTheRule(t *testing.T) {
	names := ReservedNames()
	for _, want := range []string{"Profile-*", "X-Hwid-*", "Content-*", "Set-Cookie", "Mistgate-*", "Announce", "Cache-Control", "Routing"} {
		if !slices.Contains(names, want) {
			t.Errorf("ReservedNames lacks %s", want)
		}
	}
	for _, p := range Panel {
		if !Reserved(p) {
			t.Errorf("%s of Panel is free", p)
		}
	}
	seen := map[string]bool{}
	for _, n := range names {
		l := strings.ToLower(n)
		if seen[l] {
			t.Errorf("%s is listed twice", n)
		}
		seen[l] = true
		probe := n
		if p, ok := strings.CutSuffix(n, "*"); ok {
			probe = p + "Anything"
		}
		if !Reserved(probe) {
			t.Errorf("%s is listed but %s is free", n, probe)
		}
		if !strings.HasSuffix(n, "*") && hasPrefix(n, append(slices.Clone(panelPrefixes), transportPrefixes...)) {
			t.Errorf("%s is listed beside the prefix that covers it", n)
		}
	}
}
