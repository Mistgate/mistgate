package logscrub

import (
	"net/netip"
	"strings"
	"testing"
)

func TestAddrs(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"handshake from 203.0.113.7:51234 failed", "handshake from <ip>:port failed"},
		{"peer 203.0.113.7 timed out.", "peer <ip> timed out."},
		{"dial [2001:db8::7]:443: i/o timeout", "dial <ip>:port: i/o timeout"},
		{"from 2001:db8::7, retrying", "from <ip>, retrying"},
		{"from fe80::1%eth0: no route", "from <ip>: no route"},
		{"addr=[fe80::1%25eth0]:51820", "addr=<ip>:port"},
		{"mapped ::ffff:198.51.100.2 sent junk", "mapped <ip> sent junk"},
		{"sendmsg 2001:db8::7:", "sendmsg <ip>:"},
		{"tcp:198.51.100.2:8080->10.8.0.2:443", "tcp:<ip>:port-><ip>:port"},
		{"tunnel 10.8.0.1/24 up", "tunnel <ip>/24 up"},
		// the node's own: unspecified and loopback stay
		{"listen udp 0.0.0.0:443: bind: address already in use", "listen udp 0.0.0.0:443: bind: address already in use"},
		{"listen udp [::]:443: bind failed", "listen udp [::]:443: bind failed"},
		{"dns 127.0.0.1:53 and ::1 refused", "dns 127.0.0.1:53 and ::1 refused"},
		// not addresses
		{"agent v0.1.54 on kernel 6.8.0-45-generic", "agent v0.1.54 on kernel 6.8.0-45-generic"},
		{"at 2026-10-24T17:35:00Z took 12:34:56", "at 2026-10-24T17:35:00Z took 12:34:56"},
		{"mac aa:bb:cc:dd:ee:ff, sha256:deadbeef", "mac aa:bb:cc:dd:ee:ff, sha256:deadbeef"},
		{"cert for vpn.example.com expires", "cert for vpn.example.com expires"},
		{"version 1.2.3.4567 ok", "version 1.2.3.4567 ok"},
		{"no dots or colons", "no dots or colons"},
		{"", ""},
	} {
		if got := Addrs(c.in); got != c.want {
			t.Errorf("Addrs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("привет", 5); got != "пр" {
		t.Errorf("Clip = %q", got)
	}
	if got := Clip("abc", 5); got != "abc" {
		t.Errorf("Clip short = %q", got)
	}
	if got := ClipRunes("привет", 3); got != "при" {
		t.Errorf("ClipRunes = %q", got)
	}
	if got := ClipRunes("abc", 3); got != "abc" {
		t.Errorf("ClipRunes exact = %q", got)
	}
}

// FuzzAddrs: an address put anywhere in text never survives (unless it is unspecified or loopback), and cleaning twice
// changes nothing.
func FuzzAddrs(f *testing.F) {
	f.Add("x ", " y", []byte{203, 0, 113, 7}, uint16(51234))
	f.Add("from [", "]: oops", []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7}, uint16(0))
	f.Add("a:", ":b", []byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, uint16(443))
	f.Fuzz(func(t *testing.T, pre, post string, raw []byte, port uint16) {
		a, ok := netip.AddrFromSlice(raw)
		if !ok || own(a) || a.Zone() != "" {
			return
		}
		text := a.String()
		if port != 0 {
			text = netip.AddrPortFrom(a, port).String()
		}
		s := pre + " " + text + " " + post
		out := Addrs(s)
		if strings.Contains(out, a.String()) && !strings.Contains(pre+post, a.String()) {
			t.Fatalf("Addrs(%q) = %q keeps %s", s, out, a)
		}
		if again := Addrs(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, out, again)
		}
	})
}
