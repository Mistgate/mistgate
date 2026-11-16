package trusttunnel

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Every vector below was written out by hand from DEEP_LINK.md (and the tag
// table of the official decoder) and cross-checked with an independent
// base64url encoder, not produced by this package. A codec that is only tested
// against itself would pass with the tags in any layout.

// tlv builds one entry with a one-byte tag and a one-byte length (values under 64 bytes).
func tlv(tag byte, value string) []byte {
	if len(value) > 63 {
		panic("test helper: value too long")
	}
	return append([]byte{tag, byte(len(value))}, value...)
}

func tlvBytes(tag byte, value []byte) []byte { return tlv(tag, string(value)) }

func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func link(payload []byte) string {
	return DeepLinkPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// minimal is the shortest valid static link body.
func minimal() []byte {
	return concat(tlv(0x01, "vpn.example.com"), tlv(0x02, "192.0.2.1:443"), tlv(0x05, "alice"), tlv(0x06, "s3cret"))
}

// allTagsLink is the hand-written link of TestDeepLinkDecodesEveryTag.
const allTagsLink = "tt://?AAECAQ92cG4uZXhhbXBsZS5jb20FBWFsaWNlBgVwYTpzcwIOMTkyLjAuMi4xOjg0NDMCEVsyMDAxOmRiODo6MV06NDQzCwlhYWJiL2ZmMDAPCDAwMTEyMjMzAw9zbmkuZXhhbXBsZS5jb20EAQAHAQEKAQEIBTADAgEFCQECDAdFeGFtcGxlDR4HMS4xLjEuMRV0bHM6Ly9kbnMuZXhhbXBsZS5jb20OIGh0dHBzOi8vYWxpY2U6cHdAZXhhbXBsZS5jb20vc3Vi"

func TestDeepLinkDecodesEveryTag(t *testing.T) {
	// 00 01 02                          version = 2
	// 01 0f "vpn.example.com"           hostname
	// 05 05 "alice"                     username
	// 06 05 "pa:ss"                     password (a colon is data)
	// 02 0e "192.0.2.1:8443"            address, repeated
	// 02 11 "[2001:db8::1]:443"
	// 0b 09 "aabb/ff00"                 client_random_prefix
	// 0f 08 "00112233"                  client_random_auth_key
	// 03 0f "sni.example.com"           custom_sni
	// 04 01 00                          has_ipv6 = false
	// 07 01 01                          skip_verification = true
	// 0a 01 01                          anti_dpi = true
	// 08 05 30 03 02 01 05              certificate (opaque bytes)
	// 09 01 02                          upstream_protocol = http3
	// 0c 07 "Example"                   name
	// 0d 1e 07 "1.1.1.1" 15 "tls://dns.example.com"   dns_upstreams, String[]
	// 0e 20 "https://alice:pw@example.com/sub"        subscription_url
	const encoded = allTagsLink
	want := DeepLink{
		Hostname:            "vpn.example.com",
		Addresses:           []string{"192.0.2.1:8443", "[2001:db8::1]:443"},
		HasIPv6:             false,
		Username:            "alice",
		Password:            "pa:ss",
		Name:                "Example",
		DNSUpstreams:        []string{"1.1.1.1", "tls://dns.example.com"},
		CustomSNI:           "sni.example.com",
		SkipVerification:    true,
		Certificate:         []byte{0x30, 3, 2, 1, 5},
		HTTP3:               true,
		AntiDPI:             true,
		ClientRandomPrefix:  "aabb/ff00",
		ClientRandomAuthKey: "00112233",
		SubscriptionURL:     "https://alice:pw@example.com/sub",
	}
	got, err := DecodeDeepLink(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded\n got %#v\nwant %#v", got, want)
	}
}

func TestDeepLinkEncoderMatchesHandWrittenBytes(t *testing.T) {
	// 00 01 01 (version 1: no subscription_url, see EncodeDeepLink) | 01 0f hostname | 05 05 alice | 06 06 s3cret | 02 0d 192.0.2.1:443 |
	// 04 01 00 (has_ipv6 explicit) | 09 01 01 (http2 explicit) | 0c 07 Example |
	// 0d 0b 0a "192.0.2.53" (String[] of one entry)
	const wantHex = "000101010f76706e2e6578616d706c652e636f6d0505616c6963650606733363726574020d3139322e302e322e313a3434330401000901010c074578616d706c650d0b0a3139322e302e322e3533"
	const want = "tt://?AAEBAQ92cG4uZXhhbXBsZS5jb20FBWFsaWNlBgZzM2NyZXQCDTE5Mi4wLjIuMTo0NDMEAQAJAQEMB0V4YW1wbGUNCwoxOTIuMC4yLjUz"
	got, err := EncodeDeepLink(DeepLink{
		Hostname:     "vpn.example.com",
		Addresses:    []string{"192.0.2.1:443"},
		Username:     "alice",
		Password:     "s3cret",
		Name:         "Example",
		DNSUpstreams: []string{"192.0.2.53"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("link\n got %s\nwant %s", got, want)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(got, DeepLinkPrefix))
	if hex.EncodeToString(payload) != wantHex {
		t.Fatalf("payload %x", payload)
	}
}

func TestDeepLinkSubscriptionOnly(t *testing.T) {
	// 00 01 02 | 0e 1d "https://example.com/sub?t=abc": nothing else is required.
	const encoded = "tt://?AAECDh1odHRwczovL2V4YW1wbGUuY29tL3N1Yj90PWFiYw"
	got, err := DecodeDeepLink(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := DeepLink{HasIPv6: true, SubscriptionURL: "https://example.com/sub?t=abc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %#v", got)
	}
	again, err := EncodeDeepLink(want)
	if err != nil || again != encoded {
		t.Fatalf("encode = %q, %v", again, err)
	}

	for name, body := range map[string][]byte{
		"plain http":                        tlv(0x0e, "http://example.com/sub"),
		"empty value":                       tlv(0x0e, ""),
		"scheme in capitals":                tlv(0x0e, "HTTPS://example.com/sub"),
		"with static fields and plain http": concat(minimal(), tlv(0x0e, "http://example.com/sub")),
	} {
		if _, err := DecodeDeepLink(link(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// With a subscription_url the static fields may be absent or empty.
	if _, err := DecodeDeepLink(link(concat(tlv(0x01, ""), tlv(0x0e, "https://example.com/s")))); err != nil {
		t.Errorf("empty hostname beside subscription_url: %v", err)
	}
	if _, err := EncodeDeepLink(DeepLink{SubscriptionURL: "http://example.com/sub"}); err == nil {
		t.Error("encoder accepted a plain http subscription_url")
	}
}

// The link of the official client's own tests: TrustTunnelClient,
// platform/android/lib/src/androidTest/java/com/adguard/trusttunnel/DeepLinkInstrumentedTest.kt
// (and platform/windows/test/trusttunnel_deeplink_test.cpp). It is an older
// link: no version tag (version 0), the "tt://" prefix without "?", a two-byte
// varint length (certificate, 0x4153 = 339) and tags 0x0b and 0x03.
const officialClientLink = "tt://AQlsb2NhbGhvc3QFBHRlc3QGBHRlc3QCDjEyNy4wLjAuMTo0NDQzCwRhYWJiAwlsb2NhbGhvc3QIQVMwggFPMIH1oAMCAQICFGi8WMY2yFmtW2u_18hMQBa0T4VGMAoGCCqGSM49BAMCMBQxEjAQBgNVBAMMCWxvY2FsaG9zdDAeFw0yNjAxMzAwMDAwMDBaFw0yNzAxMzAwMDAwMDBaMBQxEjAQBgNVBAMMCWxvY2FsaG9zdDBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABB4ozK9KbqScCCTJ8CvTfW4W0r9OEsn4VcQswd-BbP9z-tdyfE5HT4uHLUSZRWUKfZjnRRkHWOwhp9KJhOE-LAWjJTAjMCEGA1UdEQQaMBiCCWxvY2FsaG9zdIILKi5sb2NhbGhvc3QwCgYIKoZIzj0EAwIDSQAwRgIhAO6gFBHDsgvWjPj39JNchcMF3X2ICgzycBwTyydxqpdiAiEAgybYwECuZopK1g6JX5tK0-5B3Of7n0NuPXRGSU5TtSc"

func TestDeepLinkOfficialClientVector(t *testing.T) {
	got, err := DecodeDeepLink(officialClientLink)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "localhost" || got.Username != "test" || got.Password != "test" ||
		!reflect.DeepEqual(got.Addresses, []string{"127.0.0.1:4443"}) ||
		got.ClientRandomPrefix != "aabb" || got.CustomSNI != "localhost" ||
		!got.HasIPv6 || got.HTTP3 || got.SkipVerification || got.AntiDPI {
		t.Fatalf("decoded %#v", got)
	}
	if len(got.Certificate) != 339 || got.Certificate[0] != 0x30 || got.Certificate[1] != 0x82 {
		t.Fatalf("certificate: %d bytes", len(got.Certificate))
	}
	// The same payload with the "?" prefix decodes to the same link.
	withQuestion := DeepLinkPrefix + strings.TrimPrefix(officialClientLink, "tt://")
	again, err := DecodeDeepLink(withQuestion)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("with question mark: %v", err)
	}
	// And it survives our encoder (default http2, explicit has_ipv6 = true).
	got.Addresses = got.Addresses[:1]
	reencoded, err := EncodeDeepLink(got)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeDeepLink(reencoded)
	if err != nil || !reflect.DeepEqual(back, got) {
		t.Fatalf("re-encoded: %v", err)
	}
}

func TestDeepLinkVersions(t *testing.T) {
	body := minimal()
	versioned := func(versions ...string) []byte {
		out := []byte(nil)
		for _, v := range versions {
			out = append(out, 0x00, byte(len(v)))
			out = append(out, v...)
		}
		return append(out, body...)
	}
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{name: "absent means 0", payload: body},
		{name: "explicit 0", payload: versioned("\x00")},
		{name: "1", payload: versioned("\x01")},
		{name: "2", payload: versioned("\x02")},
		{name: "3", payload: versioned("\x03"), want: ErrUnsupportedVersion},
		{name: "63", payload: versioned("\x3f"), want: ErrUnsupportedVersion},
		{name: "2-byte varint 1", payload: versioned("\x40\x01")},
		{name: "2-byte varint 99", payload: versioned("\x40\x63"), want: ErrUnsupportedVersion},
		// The official decoder tests each version tag as it reads it, so a
		// later lower value does not rescue a link that once said 3.
		{name: "3 then 1", payload: versioned("\x03", "\x01"), want: ErrUnsupportedVersion},
		{name: "1 then 3", payload: versioned("\x01", "\x03"), want: ErrUnsupportedVersion},
		// It also reads only the first varint of the value.
		{name: "trailing bytes in value", payload: versioned("\x02\xff")},
		{name: "empty value", payload: versioned(""), want: ErrInvalidDeepLink},
		{name: "truncated varint", payload: versioned("\x40"), want: ErrInvalidDeepLink},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeDeepLink(link(test.payload))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	// A version tag number written as a two-byte varint (0x41 0x00 = 256) is
	// unknown, not version: the official decoder truncates it to a byte.
	if _, err := DecodeDeepLink(link(concat([]byte{0x41, 0x00, 0x01, 0x63}, body))); err != nil {
		t.Fatalf("tag 256: %v", err)
	}
}

func TestDeepLinkTagRules(t *testing.T) {
	base := minimal()
	with := func(extra ...[]byte) string { return link(concat(append([][]byte{base}, extra...)...)) }
	// decode returns the link with the extra TLVs appended to the minimal body.
	decode := func(extra ...[]byte) (DeepLink, error) { return DecodeDeepLink(with(extra...)) }

	t.Run("unknown tags are skipped", func(t *testing.T) {
		got, err := decode(
			tlv(0x10, "\xff\xfe"),                      // not UTF-8, still skipped
			[]byte{0x40, 0x10, 0x02, 0xaa, 0xbb},       // tag 16 as a 2-byte varint
			[]byte{0x7f, 0xff, 0x00},                   // tag 16383, empty value
			[]byte{0x41, 0x01, 0x01, 0x07},             // tag 257 must not act as 0x01 (hostname)
			[]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x20, 0x00}, // 8-byte tag 32, empty value
		)
		if err != nil || got.Hostname != "vpn.example.com" {
			t.Fatalf("%#v, %v", got, err)
		}
	})
	t.Run("a repeated tag overrides, addresses accumulate, dns is replaced", func(t *testing.T) {
		got, err := decode(
			tlv(0x01, "other.example.com"),
			tlv(0x02, "192.0.2.2:444"),
			tlv(0x0d, "\x07"+"1.1.1.1"),
			tlv(0x0d, "\x07"+"9.9.9.9"),
			tlv(0x0c, "first"), tlv(0x0c, "second"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if got.Hostname != "other.example.com" || got.Name != "second" ||
			!reflect.DeepEqual(got.Addresses, []string{"192.0.2.1:443", "192.0.2.2:444"}) ||
			!reflect.DeepEqual(got.DNSUpstreams, []string{"9.9.9.9"}) {
			t.Fatalf("%#v", got)
		}
	})
	t.Run("defaults without tags", func(t *testing.T) {
		got, err := decode()
		if err != nil || !got.HasIPv6 || got.HTTP3 || got.SkipVerification || got.AntiDPI ||
			got.Certificate != nil || got.DNSUpstreams != nil || got.Name != "" || got.CustomSNI != "" {
			t.Fatalf("%#v, %v", got, err)
		}
	})
	t.Run("booleans are exactly one byte 0 or 1", func(t *testing.T) {
		for _, tag := range []byte{0x04, 0x07, 0x0a} {
			for _, value := range []string{"", "\x02", "\xff", "\x00\x01", "\x01\x01"} {
				if _, err := decode(tlv(tag, value)); err == nil {
					t.Errorf("tag 0x%02x value %q accepted", tag, value)
				}
			}
			for _, value := range []string{"\x00", "\x01"} {
				if _, err := decode(tlv(tag, value)); err != nil {
					t.Errorf("tag 0x%02x value %q: %v", tag, value, err)
				}
			}
		}
	})
	t.Run("upstream_protocol is one byte, 1 or 2", func(t *testing.T) {
		for _, value := range []string{"", "\x00", "\x03", "\x01\x01", "\x40\x01"} {
			if _, err := decode(tlv(0x09, value)); err == nil {
				t.Errorf("value %q accepted", value)
			}
		}
		if got, err := decode(tlv(0x09, "\x02")); err != nil || !got.HTTP3 {
			t.Errorf("http3: %#v, %v", got, err)
		}
		if got, err := decode(tlv(0x09, "\x01")); err != nil || got.HTTP3 {
			t.Errorf("http2: %#v, %v", got, err)
		}
	})
	t.Run("strings must be UTF-8", func(t *testing.T) {
		for _, tag := range []byte{0x01, 0x02, 0x03, 0x05, 0x06, 0x0b, 0x0c, 0x0e, 0x0f} {
			if _, err := decode(tlv(tag, "\xff\xfe")); err == nil {
				t.Errorf("tag 0x%02x took invalid UTF-8", tag)
			}
		}
	})
	t.Run("required fields", func(t *testing.T) {
		for name, body := range map[string][]byte{
			"no hostname":    concat(tlv(0x02, "192.0.2.1:443"), tlv(0x05, "a"), tlv(0x06, "b")),
			"empty hostname": concat(tlv(0x01, ""), tlv(0x02, "192.0.2.1:443"), tlv(0x05, "a"), tlv(0x06, "b")),
			"no address":     concat(tlv(0x01, "h.example.com"), tlv(0x05, "a"), tlv(0x06, "b")),
			"no username":    concat(tlv(0x01, "h.example.com"), tlv(0x02, "192.0.2.1:443"), tlv(0x06, "b")),
			"empty password": concat(tlv(0x01, "h.example.com"), tlv(0x02, "192.0.2.1:443"), tlv(0x05, "a"), tlv(0x06, "")),
			"empty payload":  nil,
			"bad address":    concat(tlv(0x01, "h.example.com"), tlv(0x02, "192.0.2.1"), tlv(0x05, "a"), tlv(0x06, "b")),
		} {
			if _, err := DecodeDeepLink(link(body)); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	})
	t.Run("client_random_prefix and auth key are hex", func(t *testing.T) {
		good := []string{"aabb", "AABB", "aabb/ff00", "/ff", "aa/", "", "/"}
		bad := []string{"abc", "zz", "aabb/f", "aabb/ff/00", "aa bb"}
		for _, value := range good {
			if got, err := decode(tlv(0x0b, value)); err != nil || got.ClientRandomPrefix != value {
				t.Errorf("prefix %q: %v", value, err)
			}
		}
		for _, value := range bad {
			if _, err := decode(tlv(0x0b, value)); err == nil {
				t.Errorf("prefix %q accepted", value)
			}
		}
		if _, err := decode(tlv(0x0f, "")); err != nil {
			t.Errorf("empty auth key: %v", err)
		}
		for _, value := range []string{"abc", "xy"} {
			if _, err := decode(tlv(0x0f, value)); err == nil {
				t.Errorf("auth key %q accepted", value)
			}
		}
	})
	t.Run("String[] framing", func(t *testing.T) {
		got, err := decode(tlv(0x0d, "\x00\x03abc\x00"))
		if err != nil || !reflect.DeepEqual(got.DNSUpstreams, []string{"", "abc", ""}) {
			t.Fatalf("%#v, %v", got, err)
		}
		if got, err = decode(tlv(0x0d, "")); err != nil || len(got.DNSUpstreams) != 0 {
			t.Fatalf("empty array: %#v, %v", got, err)
		}
		for _, value := range []string{"\x05abc", "\x40", "\x02\xff\xfe"} {
			if _, err := decode(tlv(0x0d, value)); err == nil {
				t.Errorf("array %q accepted", value)
			}
		}
	})
	t.Run("framing errors", func(t *testing.T) {
		for name, tail := range map[string][]byte{
			"length beyond payload": {0x01, 0x10, 'a'},
			"tag without length":    {0x01},
			"truncated length":      {0x01, 0x40},
			"huge 8-byte length":    {0x01, 0xc0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		} {
			if _, err := DecodeDeepLink(link(concat(base, tail))); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	})
}

func TestDeepLinkEnvelope(t *testing.T) {
	good := link(minimal())
	payload := strings.TrimPrefix(good, DeepLinkPrefix)
	if _, err := DecodeDeepLink("tt://" + payload); err != nil {
		t.Errorf("tt:// without ?: %v", err)
	}
	// V0: "tt://?AQ92...": the first tag is hostname (01 0f), no version.
	if !strings.HasPrefix(payload, "AQ92cG4u") {
		t.Fatalf("hand-checked prefix of %s", payload)
	}
	bad := map[string]string{
		"empty":                "",
		"scheme only":          "tt://",
		"scheme and ?":         "tt://?",
		"upper-case scheme":    "TT://?" + payload,
		"https":                "https://?" + payload,
		"padded":               good + "=",
		"standard alphabet":    strings.Replace(good, "-", "+", 1) + "+",
		"trailing newline":     good + "\n",
		"fragment":             good + "#x",
		"surrounding space":    " " + good,
		"non-canonical bits":   good[:len(good)-1] + nextBase64Char(good[len(good)-1]),
		"plus instead of dash": "tt://?AQ92cG4u+",
	}
	for name, value := range bad {
		if _, err := DecodeDeepLink(value); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// nextBase64Char returns a neighbouring character of the base64url alphabet,
// which for the last character of an unpadded string sets trailing bits.
func nextBase64Char(c byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return string(alphabet[(strings.IndexByte(alphabet, c)+1)%64])
}

func TestDeepLinkEncoderPolicy(t *testing.T) {
	ok := DeepLink{Hostname: "h.example.com", Addresses: []string{"192.0.2.1:443"}, Username: "u", Password: "p"}
	encode := func(mutate func(*DeepLink)) error {
		l := ok
		mutate(&l)
		_, err := EncodeDeepLink(l)
		return err
	}
	for name, mutate := range map[string]func(*DeepLink){
		"no address":       func(l *DeepLink) { l.Addresses = nil },
		"two addresses":    func(l *DeepLink) { l.Addresses = []string{"192.0.2.1:443", "192.0.2.2:443"} },
		"address no port":  func(l *DeepLink) { l.Addresses = []string{"192.0.2.1"} },
		"no hostname":      func(l *DeepLink) { l.Hostname = "" },
		"no username":      func(l *DeepLink) { l.Username = "" },
		"no password":      func(l *DeepLink) { l.Password = "" },
		"three dns":        func(l *DeepLink) { l.DNSUpstreams = []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} },
		"ipv6 dns":         func(l *DeepLink) { l.DNSUpstreams = []string{"2001:db8::1"} },
		"url dns":          func(l *DeepLink) { l.DNSUpstreams = []string{"tls://dns.example.com"} },
		"bad prefix":       func(l *DeepLink) { l.ClientRandomPrefix = "abc" },
		"bad auth key":     func(l *DeepLink) { l.ClientRandomAuthKey = "xy" },
		"invalid utf-8":    func(l *DeepLink) { l.Name = "\xff" },
		"huge certificate": func(l *DeepLink) { l.Certificate = make([]byte, deepLinkMaxPayload) },
	} {
		if err := encode(mutate); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	full := ok
	full.HasIPv6 = true
	full.HTTP3 = true
	full.SkipVerification = true
	full.AntiDPI = true
	full.CustomSNI = "sni.example.com"
	full.Certificate = []byte{0x30, 0x03, 0x02, 0x01, 0x05}
	full.ClientRandomPrefix = "aabb/ff00"
	full.ClientRandomAuthKey = "00112233"
	full.Name = "Example"
	full.DNSUpstreams = []string{"192.0.2.53", "192.0.2.54"}
	encoded, err := EncodeDeepLink(full)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeDeepLink(encoded)
	if err != nil || !reflect.DeepEqual(back, full) {
		t.Fatalf("full round trip: %#v, %v", back, err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, DeepLinkPrefix))
	if protocol, found := findDeepLinkTag(payload, deepLinkTagUpstreamProtocol); !found || !bytes.Equal(protocol, []byte{2}) {
		t.Fatalf("http3 upstream_protocol = %x", protocol)
	}
	for _, tag := range []uint64{deepLinkTagSkipVerification, deepLinkTagAntiDPI} {
		if value, found := findDeepLinkTag(payload, tag); !found || !bytes.Equal(value, []byte{1}) {
			t.Fatalf("tag 0x%x = %x", tag, value)
		}
	}
}

// The stable official client (v1.1.12, found by the stand scripts/e2e/trusttunnel.sh: "Unsupported deep link version: 2 (max
// supported: 1)") takes versions up to 1. A link without subscription_url is version 1; one with it must be 2, the version that
// introduced the tag.
func TestEncodedVersionIsOneUnlessThereIsASubscription(t *testing.T) {
	version := func(link DeepLink) uint64 {
		got, err := EncodeDeepLink(link)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(got, DeepLinkPrefix))
		raw, found := findDeepLinkTag(payload, deepLinkTagVersion)
		if !found {
			t.Fatal("no version tag")
		}
		v, _, err := ReadQUICVarint(raw)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	plain := DeepLink{Hostname: "vpn.example.com", Addresses: []string{"192.0.2.1:443"}, Username: "alice", Password: "s3cret"}
	if got := version(plain); got != 1 {
		t.Errorf("a link without subscription_url is version %d, want 1", got)
	}
	plain.SubscriptionURL = "https://example.com/sub"
	if got := version(plain); got != 2 {
		t.Errorf("a link with subscription_url is version %d, want 2", got)
	}
}
