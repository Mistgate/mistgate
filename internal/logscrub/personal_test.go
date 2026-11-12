package logscrub

import "testing"

func TestPersonalCleaners(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(string) string
		in   string
		want string
	}{
		{"mac colon", MACs, "adapter aa:bb:cc:dd:ee:ff is up", "adapter <mac> is up"},
		{"mac dash upper", MACs, "Physical Address: 0A-1B-2C-3D-4E-5F", "Physical Address: <mac>"},
		{"mac dotted", MACs, "bssid=aabb.ccdd.eeff;", "bssid=<mac>;"},
		{"two macs", MACs, "aa:bb:cc:dd:ee:ff aa:bb:cc:dd:ee:00", "<mac> <mac>"},
		{"eui-64", MACs, "id 00:11:22:33:44:55:66:77.", "id <mac>."},
		{"not inside a word", MACs, "xaa:bb:cc:dd:ee:ffz", "xaa:bb:cc:dd:ee:ffz"},
		{"a clock is no mac", MACs, "at 12:34:56 done", "at 12:34:56 done"},
		{"mac with a word before it", MACs, "mac:aa:bb:cc:dd:ee:ff", "mac:<mac>"},
		{"mac with a word before it, capitals", MACs, "MAC:0A:1B:2C:3D:4E:5F", "MAC:<mac>"},
		{"mac with a word and a dash", MACs, "mac-aa-bb-cc-dd-ee-ff", "mac-<mac>"},
		{"mac behind hex digits", MACs, "router 1a2b3c:aa:bb:cc:dd:ee:ff", "router 1a2b3c:<mac>"},
		{"mac behind a longer prefix", MACs, "dc:aa:bb:cc:dd:ee:ff up", "<mac> up"},
		{"mac without separators next to its word", MACs, "mac 0a1b2c3d4e5f up", "mac <mac> up"},
		{"bssid without separators", MACs, "BSSID=0A1B2C3D4E5F;", "BSSID=<mac>;"},
		{"physical address without separators", MACs, "Physical Address. . . : 0A1B2C3D4E5F", "Physical Address. . . : <mac>"},
		{"twelve hex digits alone are no mac", MACs, "hash 0a1b2c3d4e5f", "hash 0a1b2c3d4e5f"},
		{"email", Emails, "sent to alice-liddell@mail.example.org, ok", "sent to <email>, ok"},
		{"email plus", Emails, "bob.the-builder+tag@example.com", "<email>"},
		{"at sign alone", Emails, "user @ home", "user @ home"},
		{"email in another script", Emails, "почта алиса@пример.рф ok", "почта <email> ok"},
		{"windows home, a name with an apostrophe", HomePaths, `C:\Users\John Smith's PC\x`, `C:\Users\<user>\x`},
		{"windows home, a name of two words, no separator", HomePaths, `profile "C:\Users\Имя Фамилия"`, `profile "C:\Users\<user>"`},
		{"windows home, a name of two words, at the end", HomePaths, `profile C:\Users\Имя Фамилия`, `profile C:\Users\<user>`},
		{"windows home, a name before a bracket", HomePaths, `(C:\Users\Some User)`, `(C:\Users\<user>)`},
		{"windows home, a name before a comma", HomePaths, `C:\Users\SomeUser, then`, `C:\Users\<user>, then`},
		{"windows home, an apostrophe inside a name at the end", HomePaths, `at C:\Users\John Smith's PC`, `at C:\Users\<user>`},
		{"windows home, quoted with apostrophes", HomePaths, `open 'C:\Users\SomeUser' failed`, `open 'C:\Users\<user>' failed`},
		{"windows home on a share", HomePaths, `\\DESKTOP-ABC123\Users\SomeUser\x`, `\\<computer>\Users\<user>\x`},
		{"windows home on an administrative share", HomePaths, `\\fileserver\C$\Users\SomeUser\x`, `\\fileserver\C$\Users\<user>\x`},
		{"computer name", HomePaths, `host DESKTOP-7F3K2QJ and laptop-ab12cd joined`, `host <computer> and <computer> joined`},
		{"computer name that is a word", HomePaths, `a desktop-class laptop-grade part`, `a desktop-class laptop-grade part`},
		{"windows home", HomePaths, `cannot write C:\Users\SomeUser\AppData\Roaming\Mistgate\state.json`, `cannot write C:\Users\<user>\AppData\Roaming\Mistgate\state.json`},
		{"windows slashes", HomePaths, `c:/users/SomeUser/x.log`, `c:/users/<user>/x.log`},
		{"windows verbatim", HomePaths, `\\?\C:\Users\SomeUser\f`, `\\?\C:\Users\<user>\f`},
		{"windows blank in name", HomePaths, `C:\Users\Some User\Desktop\f.txt`, `C:\Users\<user>\Desktop\f.txt`},
		{"windows name ends the path", HomePaths, `denied: C:\Users\SomeUser: access`, `denied: C:\Users\<user>: access`},
		{"linux home", HomePaths, "open /home/some-user/.config/x: no such file", "open /home/<user>/.config/x: no such file"},
		{"mac home", HomePaths, "/Users/some-user/Library/Logs/m.log", "/Users/<user>/Library/Logs/m.log"},
		{"home twice", HomePaths, `C:\Users\SomeUser\a and C:\Users\Other\b`, `C:\Users\<user>\a and C:\Users\<user>\b`},
		{"home is idempotent", HomePaths, `C:\Users\<user>\a /home/<user>/b`, `C:\Users\<user>\a /home/<user>/b`},
		{"no drive no windows rule", HomePaths, `the Users group`, `the Users group`},
		{"hosts: real top-level domains only", func(s string) string { return HostsKnownTLD(s, "zip", "py") }, "state.json www.example.org System.Net.Http.HttpRequestException settings.ini report.pdf app.exe.config x.zip tool.py a.github.io", "state.json <host> System.Net.Http.HttpRequestException settings.ini report.pdf app.exe.config x.zip tool.py <host>"},
		{"hosts in other scripts", func(s string) string { return HostsKnownTLD(s) }, "пример.рф and xn--e1afmkfd.xn--p1ai and _dmarc.example.org.", "<host> and <host> and <host>."},
		{"a name must end where a name ends", func(s string) string { return HostsKnownTLD(s) }, "example.com1 a.bc.de1 www.example.org,b.example.net", "example.com1 a.bc.de1 <host>,<host>"},
		{"fold", func(s string) string { return ReplaceFold(s, "SeCrEt-Name", "<secret>") }, "a secret-name b SECRET-NAME", "a <secret> b <secret>"},
	} {
		if got := tc.fn(tc.in); got != tc.want {
			t.Errorf("%s: %q -> %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
