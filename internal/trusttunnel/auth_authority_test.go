package trusttunnel

import (
	"errors"
	"testing"
)

// The official server never decodes Proxy-Authorization: it strips the exact
// prefix "Basic " (case-sensitive, one space) and looks the rest up as a whole
// string in a table of standard, padded base64 of "user:password"
// (TrustTunnel lib/src/http_codec.rs auth_info, authentication/registry_based.rs).
// The official client sends exactly "Basic " + standard padded base64
// (core/src/utils.cpp make_credentials). So the only values that can ever
// authenticate are canonical ones, and this parser accepts nothing else.
func TestProxyAuthorizationAcceptsWhatTheOfficialClientSends(t *testing.T) {
	tests := []struct {
		value    string
		username string
		password string
	}{
		// "user:password": the vector of the official authentication/mod.rs test.
		{"Basic dXNlcjpwYXNzd29yZA==", "user", "password"},
		{"Basic YTpiOmM=", "a", "b:c"}, // "a:b:c": the password keeps its colons
		{"Basic dTpwOg==", "u", "p:"},  // "u:p:"
		{"Basic dTo6Og==", "u", "::"},
		{"Basic YTo/Pz8+fg==", "a", "???>~"},         // '/' and '+' of the standard alphabet                          // "u:::"
		{"Basic 0KLQtdGB0YI6xZHFkQ==", "Тест", "őő"}, // UTF-8 on both sides
	}
	for _, test := range tests {
		username, password, err := ParseProxyAuthorization(test.value)
		if err != nil || username != test.username || password != test.password {
			t.Errorf("%q: %q, %q, %v", test.value, username, password, err)
		}
	}
}

func TestProxyAuthorizationRejectsEverythingElse(t *testing.T) {
	for name, value := range map[string]string{
		"empty":                   "",
		"scheme only":             "Basic",
		"scheme and space":        "Basic ",
		"lower-case scheme":       "basic dXNlcjpwYXNzd29yZA==",
		"upper-case scheme":       "BASIC dXNlcjpwYXNzd29yZA==",
		"two spaces":              "Basic  dXNlcjpwYXNzd29yZA==",
		"tab":                     "Basic\tdXNlcjpwYXNzd29yZA==",
		"leading space":           " Basic dXNlcjpwYXNzd29yZA==",
		"trailing space":          "Basic dXNlcjpwYXNzd29yZA== ",
		"trailing newline":        "Basic dXNlcjpwYXNzd29yZA==\n",
		"embedded newline":        "Basic dXNlcjpw\nYXNzd29yZA==",
		"missing padding":         "Basic dXNlcjpwYXNzd29yZA",
		"url-safe alphabet":       "Basic YTo_Pz8-fg==", // the url-safe form of the accepted "Basic YTo/Pz8+fg=="
		"url-safe characters":     "Basic _-_-",
		"non-canonical trailing":  "Basic dXNlcjpwYXNzd29yZB==",
		"no colon":                "Basic dXNlcg==",
		"empty username":          "Basic OnBhc3M=",
		"empty password":          "Basic dXNlcjo=",
		"only a colon":            "Basic Og==",
		"bearer":                  "Bearer dXNlcjpwYXNzd29yZA==",
		"digest":                  "Digest username=\"user\"",
		"credentials not base 64": "Basic user:password",
	} {
		if _, _, err := ParseProxyAuthorization(value); !errors.Is(err, ErrInvalidProxyAuthorization) {
			t.Errorf("%s: %q accepted or wrong error: %v", name, value, err)
		}
	}
	// 8 KiB bound.
	long := "Basic " + string(make([]byte, MaxProxyAuthorizationBytes))
	if _, _, err := ParseProxyAuthorization(long); !errors.Is(err, ErrProxyAuthorizationTooLong) {
		t.Errorf("long value: %v", err)
	}
}

func TestBuildProxyAuthorizationMatchesTheClient(t *testing.T) {
	// What the official client would send for these credentials, written out by hand.
	for _, test := range []struct{ username, password, want string }{
		{"user", "password", "Basic dXNlcjpwYXNzd29yZA=="},
		{"a", "b:c", "Basic YTpiOmM="},
		{"u", "p:", "Basic dTpwOg=="},
	} {
		got, err := BuildProxyAuthorization(test.username, test.password)
		if err != nil || got != test.want {
			t.Errorf("%q:%q = %q, %v; want %q", test.username, test.password, got, err, test.want)
		}
	}
}

// The official server matches :authority as a string: "_check", "_udp2" and
// "_icmp" are the pseudo-hosts only in exactly that spelling and only with the
// CONNECT method (lib/src/http_downstream.rs, promote_to_next_state). The client
// writes them without a port because it formats a zero port as just the name
// (core/src/utils.cpp tunnel_addr_to_str). Any other spelling, "_udp2:0"
// included, would be an ordinary host name to the official server.
func TestAuthorityAsTheOfficialClientWritesIt(t *testing.T) {
	type want struct {
		kind AuthorityKind
		host string
		port uint16
		fail bool
	}
	for value, w := range map[string]want{
		// pseudo-hosts, no port
		"_check": {kind: AuthorityCheck, host: "_check"},
		"_udp2":  {kind: AuthorityUDP2, host: "_udp2"},
		"_icmp":  {kind: AuthorityICMP, host: "_icmp"},
		// not pseudo-hosts
		"_udp2:0":   {fail: true},
		"_udp2:443": {fail: true},
		"_CHECK":    {fail: true},
		"_Udp2":     {fail: true},
		"_icmp:1":   {fail: true},
		"_check ":   {fail: true},
		"_check.":   {fail: true},
		// the client formats a socket address as host:port or [v6]:port
		"192.0.2.1:443":       {kind: AuthorityHostPort, host: "192.0.2.1", port: 443},
		"[2001:db8::1]:443":   {kind: AuthorityHostPort, host: "2001:db8::1", port: 443},
		"[::1]:53":            {kind: AuthorityHostPort, host: "::1", port: 53},
		"example.com:443":     {kind: AuthorityHostPort, host: "example.com", port: 443},
		"example.com.:443":    {kind: AuthorityHostPort, host: "example.com.", port: 443},
		"Example.COM:443":     {kind: AuthorityHostPort, host: "Example.COM", port: 443},
		"a_b.example.com:443": {kind: AuthorityHostPort, host: "a_b.example.com", port: 443},
		"example.com:0443":    {kind: AuthorityHostPort, host: "example.com", port: 443},
		"example.com:65535":   {kind: AuthorityHostPort, host: "example.com", port: 65535},
		// internationalised names must arrive as punycode
		"xn--bcher-kva.example:443": {kind: AuthorityHostPort, host: "xn--bcher-kva.example", port: 443},
		"bücher.example:443":        {fail: true},
		// everything else
		"":                       {fail: true},
		":443":                   {fail: true},
		"example.com":            {fail: true},
		"example.com:":           {fail: true},
		"example.com:0":          {fail: true},
		"example.com:65536":      {fail: true},
		"example.com:+443":       {fail: true},
		"example.com:-1":         {fail: true},
		"example.com:44 3":       {fail: true},
		"example.com:443:1":      {fail: true},
		"2001:db8::1:443":        {fail: true},
		"[2001:db8::1]":          {fail: true},
		"[2001:db8::1]:":         {fail: true},
		"[2001:db8::1]:0":        {fail: true},
		"[2001:db8::1]:443:1":    {fail: true},
		"[2001:db8::1%eth0]:443": {fail: true},
		"[::ffff:192.0.2.1]:443": {fail: true},
		"[192.0.2.1]:443":        {fail: true},
		"[]:443":                 {fail: true},
		"[example.com]:443":      {fail: true},
		"user@example.com:443":   {fail: true},
		"example.com/x:443":      {fail: true},
		"exa mple.com:443":       {fail: true},
		"example.com\r\n:443":    {fail: true},
		"example.com\x00:443":    {fail: true},
	} {
		got, err := ParseAuthority(value)
		if w.fail {
			if err == nil {
				t.Errorf("%q accepted as %#v", value, got)
			}
			continue
		}
		if err != nil || got.Kind != w.kind || got.Host != w.host || got.Port != w.port {
			t.Errorf("%q = %#v, %v; want %+v", value, got, err, w)
		}
	}

	long := make([]byte, 0, 300)
	for i := 0; i < 256; i++ {
		long = append(long, 'a')
	}
	if _, err := ParseAuthority(string(long) + ":443"); err == nil {
		t.Error("256-byte host accepted")
	}
	if _, err := ParseAuthority(string(long[:255]) + ":443"); err != nil {
		t.Errorf("255-byte host: %v", err)
	}
	if _, err := ParseAuthority(string(make([]byte, MaxAuthorityBytes+1))); !errors.Is(err, ErrAuthorityTooLong) {
		t.Errorf("oversized: %v", err)
	}
}
