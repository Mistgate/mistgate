//go:build !js

package clientdebug

import (
	"math/rand/v2"
	"strings"
	"testing"
)

const (
	secretSNI    = "agent-sni-label-0123456789.example.net"
	secretPrefix = "k3xq8mistgatePrefix7788"
)

func testCleaner() *Cleaner {
	return NewCleaner([]string{secretSNI, "/" + secretPrefix + "/", "www", "short"})
}

// Lines as a Windows app writes them, one class of personal data each; the people, machines and addresses are invented.
func TestCleanerTable(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZS1saWRkZWxsIn0.c2lnbmF0dXJlLXNpZ25hdHVyZQ"
	key := "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE" // 43 characters
	if len(key) != 43 {
		t.Fatalf("test key has %d characters", len(key))
	}
	for _, tc := range []struct{ name, in, want string }{
		{"ipv4 with port", "connect to 203.0.113.45:443 failed: i/o timeout", "connect to <ip>:port failed: i/o timeout"},
		{"ipv4 bare", "gateway 192.0.2.1 did not answer", "gateway <ip> did not answer"},
		{"ipv6 bracketed with port", "dial [2001:db8::1a2b]:8443: connectex: no route", "dial <ip>:port: connectex: no route"},
		{"ipv6 bare", "address fe80::1c2d:3e4f:5a6b:7c8d%12 assigned", "address <ip> assigned"},
		{"loopback is not personal", "listening on 127.0.0.1:7890", "listening on 127.0.0.1:7890"},
		{"version is not an address", "Mistgate 0.3.1.4567 started", "Mistgate 0.3.1.4567 started"},
		{"mac", "Ethernet adapter Physical Address 0A-1B-2C-3D-4E-5F is up", "Ethernet adapter Physical Address <mac> is up"},
		{"bssid", "wifi bssid aa:bb:cc:dd:ee:ff channel 36", "wifi bssid <mac> channel 36"},
		{"email", "account alice-liddell@mail.example.org signed in", "account <email> signed in"},
		{"windows home", `cannot write C:\Users\SomeUser\AppData\Roaming\Mistgate\state.json`, `cannot write C:\Users\<user>\AppData\Roaming\Mistgate\state.json`},
		{"windows home, forward slashes", `loaded C:/Users/SomeUser/Mistgate/app.log`, `loaded C:/Users/<user>/Mistgate/app.log`},
		{"windows home with a blank", `C:\Users\Some User\Desktop\report.txt`, `C:\Users\<user>\Desktop\report.txt`},
		{"linux home", "open /home/some-user/.config/mistgate/state.json: permission denied", "open /home/<user>/.config/mistgate/state.json: permission denied"},
		{"mac home", "wrote /Users/some-user/Library/Logs/Mistgate/app.log", "wrote /Users/<user>/Library/Logs/Mistgate/app.log"},
		{"share link", "profile vless://3f2b8c1e-aaaa-bbbb-cccc-123456789abc@de1.example.com:443?security=reality&sni=x.example.com#de1 failed", "profile [link] failed"},
		{"bearer", "request Authorization: Bearer abc123DEF456ghi789JKL012mno345PQR678stu failed", "request Authorization: Bearer [key] failed"},
		{"jwt", "token " + jwt + " expired", "token [token] expired"},
		{"uuid", "device 3f2b8c1e-1111-4222-8333-123456789abc enrolled", "device [uuid] enrolled"},
		{"43 character key", "key " + key + " rejected", "key [key] rejected"},
		{"private key by name", "private_key = " + strings.Repeat("A1b2C3d4", 6), "private_key [key]"},
		{"subscription link", "fetching https://sub.example.com/" + secretPrefix + "/AbCdEfGhIjKlMnOpQrStUvWx?client=1", "fetching https://<host>/[redacted]"},
		{"secret prefix in another case", "path /" + strings.ToUpper(secretPrefix) + "/client/sync", "path /<secret>/client/sync"},
		{"secret name", "SNI " + strings.ToUpper(secretSNI) + " rejected", "SNI <secret> rejected"},
		{"secret too short to cut", "short www stays", "short www stays"},
		{"host names", "resolving www.example.org via dns.example.net failed", "resolving <host> via <host> failed"},
		{"file names stay", "wrote state.json, loaded wintun.dll, started Mistgate.exe, see app.log and notes.TXT, geo.db, a.conf, b.yaml, c.yml, d.toml, e.sys", "wrote state.json, loaded wintun.dll, started Mistgate.exe, see app.log and notes.TXT, geo.db, a.conf, b.yaml, c.yml, d.toml, e.sys"},
		{"host and file together", "GET https://api.example.org/state.json", "GET https://<host>/state.json"},
		{"control characters", "a\x1b[31mred\x00\x07 b\r\nc\u0085d\u2028e", "a [31mred   b  c d e"},
		{"bidi and zero width", "evil\u202E txt\u200Bgone\u2066x\u2069 \ue000", "evil txtgonex "},
		{"plain text is left alone", "handshake timed out after 5000 ms (attempt 2)", "handshake timed out after 5000 ms (attempt 2)"},

		// the second review: a hardware address behind a word, in brackets, without separators
		{"mac behind a word", "mac:aa:bb:cc:dd:ee:ff", "mac:<mac>"},
		{"mac behind a word, capitals", "MAC:0A:1B:2C:3D:4E:5F", "MAC:<mac>"},
		{"mac behind a word and a dash", "mac-aa-bb-cc-dd-ee-ff", "mac-<mac>"},
		{"mac behind hex digits", "router 1a2b3c:aa:bb:cc:dd:ee:ff", "router 1a2b3c:<mac>"},
		{"mac without separators", "mac 0a1b2c3d4e5f", "mac <mac>"},
		{"physical address without separators", "Physical Address. . . . : 0A1B2C3D4E5F", "Physical Address. . . . : <mac>"},
		{"ipv4 in brackets with a port", "from [203.0.113.45]:443", "from <ip>:port"},
		{"ipv4 in brackets", "from [203.0.113.45] ok", "from <ip> ok"},

		// home directories with blanks and apostrophes, shares, machine names
		{"windows home, name of two words in another script", `C:\Users\Имя Фамилия`, `C:\Users\<user>`},
		{"windows home, apostrophe in the name", `C:\Users\John Smith's PC\x`, `C:\Users\<user>\x`},
		{"windows home on a share", `\\DESKTOP-ABC123\Users\Name\x`, `\\<computer>\Users\<user>\x`},
		{"made-up machine name", "host DESKTOP-7F3K2QJ joined", "host <computer> joined"},
		{"made-up laptop name", "host LAPTOP-9Q8W7E6R joined", "host <computer> joined"},

		// other scripts
		{"email in another script", "алиса@пример.рф wrote", "<email> wrote"},
		{"host in another script", "resolving пример.рф failed", "resolving <host> failed"},
		{"host in punycode", "resolving xn--e1afmkfd.xn--p1ai failed", "resolving <host> failed"},

		// secrets by the name of their field, Basic and Bearer
		{"password field", "password=hunter2pass", "password=[key]"},
		{"password field with a colon", "pwd: Tr0ub4dor&3", "pwd=[key]"},
		{"obfs password", "obfs-password=o0bfspw", "obfs-password=[key]"},
		{"short id", "short_id=ab12cd34", "short_id=[key]"},
		{"psk and pin", "psk=abc pin: 1234", "psk=[key] pin=[key]"},
		{"ssid", "SSID: Keenetic-1234", "SSID=[key]"},
		{"cookie", "Cookie: session=abcDEF123456ghiJKL", "Cookie=[key]"},
		{"a word that is no field", "ping: 5 ms, authority granted", "ping: 5 ms, authority granted"},
		{"basic authorization", "Authorization: Basic YWxpY2U6aHVudGVyMg==", "Authorization: Basic [key]"},
		{"bearer with a colon", "Bearer: abc123DEF456ghi789", "Bearer [key]"},
		{"bearer in small letters", "authorization: bearer abc123DEF456ghi789JKL012mno345PQR678stu", "authorization: bearer [key]"},

		// share links that went through percent-encoding
		{"vless in percent-encoding", "url vless%3A%2F%2F3f2b8c1e-aaaa-bbbb-cccc-123456789abc%40de1.example.com%3A443%3Fsid%3Dab12cd34 end", "url [link] end"},
		{"hysteria2 in percent-encoding", "Hysteria2%3A%2F%2Fhunter2pass%40de1.example.com%3A443", "[link]"},
		{"https in percent-encoding", "u=https%3A%2F%2Fsub.example.com%2Fabc", "u=[link]"},

		// 32 hex digits are a UUID or a HWID, whatever they look like
		{"uuid without dashes", "hwid 3f2b8c1e111142228333123456789abc end", "hwid [token] end"},
		{"uuid without dashes, low entropy", "uuid 00000000000040008000000000000001", "uuid [token]"},

		// look-alike characters are normalized before the steps look for a name or a scheme
		{"full-width hyphen in a secret name", "agent－sni-label-0123456789.example.net", "<secret>"},
		{"full-width scheme", "ｈｙｓｔｅｒｉａ２://hunter2pass@de1.example.com:443", "[link]"},

		// names that are not destinations stay
		{"type name", "System.Net.Http.HttpRequestException: failed", "System.Net.Http.HttpRequestException: failed"},
		{"file names with other endings", "settings.ini report.pdf app.exe.config Mistgate.app x.zip tool.py run.sh", "settings.ini report.pdf app.exe.config Mistgate.app x.zip tool.py run.sh"},
		{"sites", "youtube.com t.me site.RU example.co.uk", "<host> <host> <host> <host>"},
		{"a site in a domain that is also an extension", "wrote a.io and b.dev and c.ai", "wrote <host> and <host> and <host>"},
	} {
		got := testCleaner().Text(tc.in)
		if got != tc.want {
			t.Errorf("%s:\n  in   %q\n  got  %q\n  want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestCleanerIsStable(t *testing.T) {
	c := testCleaner()
	for _, in := range []string{
		`cannot write C:\Users\SomeUser\AppData\state.json via 203.0.113.45:443 for alice-liddell@mail.example.org`,
		"dial [2001:db8::1a2b]:8443 aa:bb:cc:dd:ee:ff www.example.org",
	} {
		once := c.Text(in)
		if twice := c.Text(once); twice != once {
			t.Errorf("cleaning twice changes %q: %q", once, twice)
		}
	}
}

func TestCutDoesNotLeaveHalfAKey(t *testing.T) {
	key := strings.Repeat("Ab1-", 10) // a 40 character key at the end of the text
	long := strings.Repeat("word ", 400) + key
	got := cut(long, 2020)
	if len(got) > 2020 || strings.Contains(got, "Ab1-") {
		t.Errorf("cut text of %d bytes ends with a part of the key: %q", len(got), got[len(got)-30:])
	}
	if cut("short", 2048) != "short" {
		t.Error("a short text changed")
	}
}

// Text(Text(x)) == Text(x) on random texts built from the pieces that the cleaners look at (a fixed seed): a cleaner that changes
// its own output leaves something in the first pass that the second finds.
func TestCleanerIsStableOnRandomText(t *testing.T) {
	c := testCleaner()
	parts := []string{" ", ".", ":", "/", `\`, "@", "-", "_", "=", "+", "%", "[", "]", "<", ">", "\"", "'", "?", "#", ",", ";", "(", ")",
		"a", "Z", "0", "9", "ff", "1.2.3.4", "::", "fe80", "C:", "Users", "home", "x.json", "a.bc", "www", "example.com", "mac", "MAC", "pwd",
		"https://", "vless://", "Bearer", "Basic", "eyJ", "k3xq8mistgatePrefix7788", "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk",
		"3f2b8c1e-1111-4222-8333-123456789abc", "3f2b8c1e111142228333123456789abc", "aa:bb:cc:dd:ee:ff", "0a1b2c3d4e5f", "private_key",
		"<host>", "[key]", "<ip>:port", "<user>", "<mac>", "я", "​", "DESKTOP-7F3K2QJ", "vless%3A%2F%2F", "password", "auth", "sid",
		"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789", "port", "443", "пример.рф", "x@y.zz", "System.Net.Http", "Mistgate.app", "t.me"}
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 60000; i++ {
		var b strings.Builder
		for n := r.IntN(14) + 1; n > 0; n-- {
			b.WriteString(parts[r.IntN(len(parts))])
		}
		s := b.String()
		once := c.Text(s)
		if twice := c.Text(once); twice != once {
			t.Fatalf("not stable:\n  in    %q\n  once  %q\n  twice %q", s, once, twice)
		}
	}
}

// A secret of the panel is cut out of the source and the app's name as well, which are checked against a shape otherwise.
func TestCleanerSecretsInSourceAndApp(t *testing.T) {
	c := testCleaner()
	if got := c.Secrets("ua-" + strings.ToUpper(secretPrefix)); got != "ua-<secret>" {
		t.Errorf("Secrets = %q", got)
	}
	b, err := parse(t, rec(ms(0), "info", "x"+strings.ToLower(secretPrefix), "m", ""))
	if err != nil || strings.Contains(b.Lines[0].Source, secretPrefix) || b.Lines[0].Source != "x<secret>" {
		t.Errorf("source = %q, %v", b.Lines[0].Source, err)
	}
}

// The cut before the cleaners falls at a character that cannot be part of a token, not only at a blank.
func TestCutFallsAtTheLastNonTokenCharacter(t *testing.T) {
	key := "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	for _, sep := range []string{`\`, ":", `"`, "(", ",", "я", "<"} {
		long := strings.Repeat("x", 3000) + sep + key
		got := cut(long, 3000+len(sep)+20) // cuts the key in half
		if strings.Contains(got, "yAnz") || !strings.HasSuffix(got, strings.Repeat("x", 100)) || len(got) > 3000+len(sep)+20 {
			t.Errorf("sep %q: cut text ends %q (%d bytes)", sep, got[max(0, len(got)-30):], len(got))
		}
	}
	if got := cut(strings.Repeat("a", 100), 10); got != strings.Repeat("a", 10) {
		t.Errorf("a text with no place to cut: %q", got)
	}
	if got := cut(strings.Repeat("я", 100), 11); got != strings.Repeat("я", 4) && got != strings.Repeat("я", 5) {
		t.Errorf("multi-byte text cut into %d bytes", len(got))
	}
}
