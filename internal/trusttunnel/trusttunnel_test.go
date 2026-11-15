package trusttunnel

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestUDPFrames(t *testing.T) {
	tests := []struct {
		name      string
		direction UDPDirection
		frame     UDPFrame
	}{
		{
			name:      "empty app name and IPv4",
			direction: UDPClientToServer,
			frame: UDPFrame{
				AppName: "",
				Source:  netip.MustParseAddrPort("192.0.2.10:53000"),
				Target:  netip.MustParseAddrPort("192.0.2.53:53"),
				Payload: []byte("query"),
			},
		},
		{
			name:      "IPv6",
			direction: UDPClientToServer,
			frame: UDPFrame{
				AppName: "client",
				Source:  netip.MustParseAddrPort("[2001:db8::10]:53000"),
				Target:  netip.MustParseAddrPort("[2001:db8::53]:53"),
				Payload: []byte("query"),
			},
		},
		{
			name:      "server response",
			direction: UDPServerToClient,
			frame: UDPFrame{
				Source:  netip.MustParseAddrPort("192.0.2.53:53"),
				Target:  netip.MustParseAddrPort("192.0.2.10:53000"),
				Payload: []byte("response"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				wire []byte
				err  error
			)
			if test.direction == UDPClientToServer {
				wire, err = EncodeUDPClientFrame(test.frame)
			} else {
				wire, err = EncodeUDPServerFrame(test.frame)
			}
			if err != nil {
				t.Fatal(err)
			}

			if test.direction == UDPClientToServer {
				if got := binary.BigEndian.Uint32(wire[:4]); got != uint32(37+len(test.frame.AppName)+len(test.frame.Payload)) {
					t.Fatalf("frame length = %d", got)
				}
				if len(test.frame.AppName) == 0 && wire[40] != 0 {
					t.Fatalf("empty app name length byte = %d", wire[40])
				}
				if test.frame.Source.Addr().Is4() {
					for _, b := range wire[4:16] {
						if b != 0 {
							t.Fatalf("IPv4 padding contains non-zero byte: %x", wire[4:20])
						}
					}
					if !reflect.DeepEqual(wire[16:20], []byte{192, 0, 2, 10}) {
						t.Fatalf("IPv4 bytes = %x", wire[16:20])
					}
				}
			}

			decoder := NewUDPDecoder(test.direction)
			frames, err := decoder.Feed(wire)
			if err != nil {
				t.Fatal(err)
			}
			if len(frames) != 1 {
				t.Fatalf("decoded %d frames", len(frames))
			}
			if !reflect.DeepEqual(frames[0], test.frame) {
				t.Fatalf("decoded frame = %#v, want %#v", frames[0], test.frame)
			}
		})
	}
}

func TestUDPFrameLimits(t *testing.T) {
	base := UDPFrame{
		Source: netip.MustParseAddrPort("192.0.2.10:53000"),
		Target: netip.MustParseAddrPort("192.0.2.53:53"),
	}
	tests := []struct {
		name        string
		appName     string
		payloadSize int
		wantLength  uint32
		wantError   bool
	}{
		{name: "maximum payload at zero app length", payloadSize: 65434, wantLength: 65471},
		{name: "65434 minus twice app length", appName: "abc", payloadSize: 65428, wantLength: 65471 - 3},
		{name: "over maximum at zero app length", payloadSize: 65435, wantError: true},
		{name: "over maximum at nonzero app length", appName: "abc", payloadSize: 65429, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := base
			frame.AppName = test.appName
			frame.Payload = make([]byte, test.payloadSize)
			wire, err := EncodeUDPClientFrame(frame)
			if test.wantError {
				if !errors.Is(err, ErrUDPFrameTooLarge) {
					t.Fatalf("error = %v, want ErrUDPFrameTooLarge", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := binary.BigEndian.Uint32(wire[:4]); got != test.wantLength {
				t.Fatalf("Length = %d, want %d", got, test.wantLength)
			}
			frames, err := NewUDPDecoder(UDPClientToServer).Feed(wire)
			if err != nil || len(frames) != 1 {
				t.Fatalf("decode = %d frames, %v", len(frames), err)
			}
		})
	}

	serverFrame := base
	serverFrame.Payload = make([]byte, MaxUDPServerPayload)
	wire, err := EncodeUDPServerFrame(serverFrame)
	if err != nil {
		t.Fatalf("maximum server payload: %v", err)
	}
	if got := binary.BigEndian.Uint32(wire[:4]); got != minUDPServerFrameSize+MaxUDPServerPayload {
		t.Fatalf("server Length = %d", got)
	}
	if _, err := NewUDPDecoder(UDPServerToClient).Feed(wire); err != nil {
		t.Fatalf("decode maximum server payload: %v", err)
	}
	serverFrame.Payload = make([]byte, MaxUDPServerPayload+1)
	if _, err := EncodeUDPServerFrame(serverFrame); !errors.Is(err, ErrUDPFrameTooLarge) {
		t.Fatalf("oversized server payload error = %v", err)
	}
}

func TestUDPDecoderDropsShortFrameAndRecovers(t *testing.T) {
	validFrame := UDPFrame{
		AppName: "sample",
		Source:  netip.MustParseAddrPort("192.0.2.10:53000"),
		Target:  netip.MustParseAddrPort("192.0.2.53:53"),
		Payload: []byte("after-drop"),
	}
	valid, err := EncodeUDPClientFrame(validFrame)
	if err != nil {
		t.Fatal(err)
	}
	short := make([]byte, 7)
	binary.BigEndian.PutUint32(short[:4], 3)
	copy(short[4:], []byte{1, 2, 3})

	decoder := NewUDPDecoder(UDPClientToServer)
	frames, err := decoder.Feed(append(short, valid...))
	if !errors.Is(err, ErrUDPFrameTooShort) {
		t.Fatalf("error = %v, want ErrUDPFrameTooShort", err)
	}
	if len(frames) != 1 || !reflect.DeepEqual(frames[0], validFrame) {
		t.Fatalf("frames after recovery = %#v", frames)
	}
}

func TestUDPDecoderAcceptsOneByteChunks(t *testing.T) {
	want := UDPFrame{
		AppName: "sample",
		Source:  netip.MustParseAddrPort("[2001:db8::10]:53000"),
		Target:  netip.MustParseAddrPort("[2001:db8::53]:53"),
		Payload: []byte("chunked"),
	}
	wire, err := EncodeUDPClientFrame(want)
	if err != nil {
		t.Fatal(err)
	}
	decoder := NewUDPDecoder(UDPClientToServer)
	var frames []UDPFrame
	for _, b := range wire {
		decoded, feedErr := decoder.Feed([]byte{b})
		if feedErr != nil {
			t.Fatal(feedErr)
		}
		frames = append(frames, decoded...)
	}
	if len(frames) != 1 || !reflect.DeepEqual(frames[0], want) {
		t.Fatalf("decoded frames = %#v", frames)
	}
}

func TestUDPDecoderBoundsHostileLength(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], ^uint32(0))
	decoder := NewUDPDecoder(UDPClientToServer)
	if _, err := decoder.Feed(header[:]); !errors.Is(err, ErrUDPFrameTooLarge) {
		t.Fatalf("error = %v, want ErrUDPFrameTooLarge", err)
	}
	if len(decoder.buffer) != 0 || decoder.discardRemaining != uint64(^uint32(0)) {
		t.Fatalf("decoder retained %d bytes and discard count %d", len(decoder.buffer), decoder.discardRemaining)
	}
}

func TestProxyAuthorization(t *testing.T) {
	value, err := BuildProxyAuthorization("sample-user", "sample:secret")
	if err != nil {
		t.Fatal(err)
	}
	username, password, err := ParseProxyAuthorization(value)
	if err != nil {
		t.Fatal(err)
	}
	if username != "sample-user" || password != "sample:secret" {
		t.Fatalf("credentials = %q:%q", username, password)
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "bad base64", value: "Basic !!!"},
		{name: "missing colon", value: "Basic " + base64.StdEncoding.EncodeToString([]byte("sample-user"))},
		{name: "empty username", value: "Basic " + base64.StdEncoding.EncodeToString([]byte(":secret"))},
		{name: "empty password", value: "Basic " + base64.StdEncoding.EncodeToString([]byte("sample-user:"))},
		{name: "case sensitive prefix", value: "basic " + base64.StdEncoding.EncodeToString([]byte("sample-user:secret"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ParseProxyAuthorization(test.value); !errors.Is(err, ErrInvalidProxyAuthorization) {
				t.Fatalf("error = %v, want ErrInvalidProxyAuthorization", err)
			}
		})
	}
	if _, err := BuildProxyAuthorization("sample:user", "secret"); !errors.Is(err, ErrInvalidProxyAuthorization) {
		t.Fatalf("builder error = %v, want ErrInvalidProxyAuthorization", err)
	}
}

func TestParseAuthority(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantKind  AuthorityKind
		wantHost  string
		wantPort  uint16
		wantError bool
	}{
		{name: "hostname and port", value: "example.com:443", wantKind: AuthorityHostPort, wantHost: "example.com", wantPort: 443},
		{name: "IPv4 and port", value: "192.0.2.10:1", wantKind: AuthorityHostPort, wantHost: "192.0.2.10", wantPort: 1},
		{name: "IPv6 and port", value: "[2001:db8::10]:65535", wantKind: AuthorityHostPort, wantHost: "2001:db8::10", wantPort: 65535},
		{name: "check pseudo-host", value: "_check", wantKind: AuthorityCheck, wantHost: "_check"},
		{name: "udp pseudo-host", value: "_udp2", wantKind: AuthorityUDP2, wantHost: "_udp2"},
		{name: "icmp pseudo-host", value: "_icmp", wantKind: AuthorityICMP, wantHost: "_icmp"},
		{name: "missing port", value: "example.com", wantError: true},
		{name: "port zero", value: "example.com:0", wantError: true},
		{name: "port too large", value: "example.com:65536", wantError: true},
		{name: "unbracketed IPv6", value: "2001:db8::10:443", wantError: true},
		{name: "bracketed IPv4", value: "[192.0.2.10]:443", wantError: true},
		{name: "pseudo-host with port", value: "_check:443", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseAuthority(test.value)
			if test.wantError {
				if err == nil {
					t.Fatalf("ParseAuthority(%q) succeeded: %#v", test.value, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != test.wantKind || got.Host != test.wantHost || got.Port != test.wantPort {
				t.Fatalf("authority = %#v", got)
			}
		})
	}
}

func TestQUICVarintBoundaries(t *testing.T) {
	tests := []struct {
		value uint64
		size  int
	}{
		{value: 63, size: 1},
		{value: 64, size: 2},
		{value: 16383, size: 2},
		{value: 16384, size: 4},
	}
	for _, test := range tests {
		encoded, err := AppendQUICVarint(nil, test.value)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) != test.size {
			t.Fatalf("varint %d has size %d, want %d", test.value, len(encoded), test.size)
		}
		value, n, err := ReadQUICVarint(encoded)
		if err != nil || value != test.value || n != len(encoded) {
			t.Fatalf("decoded varint = %d, %d, %v", value, n, err)
		}
	}
	if _, err := AppendQUICVarint(nil, 1<<62); !errors.Is(err, ErrInvalidQUICVarint) {
		t.Fatalf("out-of-range varint error = %v", err)
	}
}

func TestDeepLinkRoundTrip(t *testing.T) {
	tests := []DeepLink{
		{
			Hostname:     "example.com",
			Addresses:    []string{"192.0.2.10:443"},
			HasIPv6:      false,
			Username:     "sample-user",
			Password:     "sample-secret",
			Name:         "sample endpoint",
			DNSUpstreams: []netip.Addr{netip.MustParseAddr("192.0.2.53")},
		},
		{
			Hostname:  "example.com",
			Addresses: []string{"[2001:db8::10]:443"},
			HasIPv6:   true,
			Username:  "sample-user",
			Password:  "sample-secret",
		},
	}
	for _, want := range tests {
		encoded, err := EncodeDeepLink(want)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > MaxDeepLinkBytes {
			t.Fatalf("encoded link length = %d", len(encoded))
		}
		got, err := DecodeDeepLink(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded link = %#v, want %#v", got, want)
		}
	}
}

func TestDeepLinkRequiredTagsAndUnsupportedTags(t *testing.T) {
	config := DeepLink{
		Hostname:  "example.com",
		Addresses: []string{"192.0.2.10:443"},
		Username:  "sample-user",
		Password:  "sample-secret",
		HasIPv6:   false,
	}
	encoded, err := EncodeDeepLink(config)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, DeepLinkPrefix))
	if err != nil {
		t.Fatal(err)
	}
	hasIPv6, found := findDeepLinkTag(payload, deepLinkTagHasIPv6)
	if !found || !reflect.DeepEqual(hasIPv6, []byte{0}) {
		t.Fatalf("has_ipv6 field = %x, found %v", hasIPv6, found)
	}
	version, found := findDeepLinkTag(payload, deepLinkTagVersion)
	if !found {
		t.Fatal("version tag is absent")
	}
	if value, n, err := ReadQUICVarint(version); err != nil || n != len(version) || value != 2 {
		t.Fatalf("version = %d, %d, %v", value, n, err)
	}
	protocol, found := findDeepLinkTag(payload, deepLinkTagUpstreamProtocol)
	if !found {
		t.Fatal("upstream_protocol tag is absent")
	}
	if value, n, err := ReadQUICVarint(protocol); err != nil || n != len(protocol) || value != 1 {
		t.Fatalf("upstream_protocol = %d, %d, %v", value, n, err)
	}
	for _, forbidden := range []uint64{0x07, 0x08, 0x0a, 0x0b, 0x0e, 0x0f} {
		if _, found := findDeepLinkTag(payload, forbidden); found {
			t.Fatalf("unexpected tag 0x%x", forbidden)
		}
	}

	unknown, err := appendDeepLinkTLV(payload, 0x20, []byte("future"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDeepLink(DeepLinkPrefix + base64.RawURLEncoding.EncodeToString(unknown))
	if err != nil || !reflect.DeepEqual(decoded, config) {
		t.Fatalf("decode with unknown tag = %#v, %v", decoded, err)
	}

	versionThree, _ := AppendQUICVarint(nil, 3)
	newer, err := appendDeepLinkTLV(payload, deepLinkTagVersion, versionThree)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeDeepLink(DeepLinkPrefix + base64.RawURLEncoding.EncodeToString(newer))
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("version 3 error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestDeepLinkRejectsOversizedAndMalformedValues(t *testing.T) {
	if _, err := DecodeDeepLink(strings.Repeat("x", MaxDeepLinkBytes+1)); !errors.Is(err, ErrDeepLinkTooLong) {
		t.Fatalf("oversized link error = %v", err)
	}
	if _, err := DecodeDeepLink(DeepLinkPrefix + "AAAA"); err == nil {
		t.Fatal("malformed TLV was accepted")
	}
	config := DeepLink{
		Hostname:  "example.com",
		Addresses: []string{"192.0.2.10:443"},
		Username:  "sample-user",
		Password:  "sample-secret",
		Name:      strings.Repeat("n", MaxDeepLinkBytes),
	}
	if _, err := EncodeDeepLink(config); !errors.Is(err, ErrDeepLinkTooLong) {
		t.Fatalf("oversized field error = %v", err)
	}
}

func findDeepLinkTag(payload []byte, wanted uint64) ([]byte, bool) {
	for offset := 0; offset < len(payload); {
		tag, n, err := ReadQUICVarint(payload[offset:])
		if err != nil {
			return nil, false
		}
		offset += n
		length, n, err := ReadQUICVarint(payload[offset:])
		if err != nil {
			return nil, false
		}
		offset += n
		if length > uint64(len(payload)-offset) {
			return nil, false
		}
		value := payload[offset : offset+int(length)]
		offset += int(length)
		if tag == wanted {
			return value, true
		}
	}
	return nil, false
}

func FuzzUDPDecode(f *testing.F) {
	frame := UDPFrame{
		AppName: "sample",
		Source:  netip.MustParseAddrPort("192.0.2.10:53000"),
		Target:  netip.MustParseAddrPort("192.0.2.53:53"),
		Payload: []byte("seed"),
	}
	client, _ := EncodeUDPClientFrame(frame)
	server, _ := EncodeUDPServerFrame(frame)
	short := []byte{0, 0, 0, 3, 1, 2, 3}
	large := []byte{0xff, 0xff, 0xff, 0xff}
	f.Add([]byte{})
	f.Add(client)
	f.Add(server)
	f.Add(short)
	f.Add(large)
	f.Fuzz(func(t *testing.T, input []byte) {
		for _, direction := range []UDPDirection{UDPClientToServer, UDPServerToClient} {
			decoder := NewUDPDecoder(direction)
			_, _ = decoder.Feed(input)
			if len(decoder.buffer) > maxUDPFrameWireSize {
				t.Fatalf("retained %d bytes", len(decoder.buffer))
			}
		}
	})
}

func FuzzAuthority(f *testing.F) {
	for _, seed := range []string{
		"example.com:443",
		"192.0.2.10:443",
		"[2001:db8::10]:443",
		"_check",
		"_udp2",
		"_icmp",
		"example.com",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = ParseAuthority(input)
	})
}

func FuzzProxyAuth(f *testing.F) {
	for _, seed := range []string{
		"Basic c2FtcGxlLXVzZXI6c2FtcGxlLXNlY3JldA==",
		"Basic !!!",
		"Basic c2FtcGxlLXVzZXI=",
		"basic c2FtcGxlLXVzZXI6c2VjcmV0",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_, _, _ = ParseProxyAuthorization(input)
	})
}

func FuzzDeepLinkRoundtrip(f *testing.F) {
	seed := DeepLink{
		Hostname:  "example.com",
		Addresses: []string{"192.0.2.10:443"},
		Username:  "sample-user",
		Password:  "sample-secret",
	}
	seedLink, _ := EncodeDeepLink(seed)
	f.Add([]byte(seedLink), "example.com", "192.0.2.10:443", "sample-user", "sample-secret", "", false)
	f.Add([]byte(DeepLinkPrefix+"AAAA"), "example.com", "[2001:db8::10]:443", "sample-user", "sample-secret", "sample endpoint", true)
	f.Add([]byte(DeepLinkPrefix+strings.Repeat("A", MaxDeepLinkBytes)), "example.com", "192.0.2.10:443", "sample-user", "sample-secret", "", false)
	f.Fuzz(func(t *testing.T, raw []byte, hostname, address, username, password, name string, hasIPv6 bool) {
		if decoded, err := DecodeDeepLink(string(raw)); err == nil && len(decoded.Addresses) == 1 {
			reencoded, encodeErr := EncodeDeepLink(decoded)
			if encodeErr == nil {
				again, decodeErr := DecodeDeepLink(reencoded)
				if decodeErr != nil {
					t.Fatalf("decode canonical link: %v", decodeErr)
				}
				if !reflect.DeepEqual(again, decoded) {
					t.Fatalf("decoded link changed after re-encoding: %#v != %#v", again, decoded)
				}
			}
		}

		config := DeepLink{
			Hostname:  hostname,
			Addresses: []string{address},
			HasIPv6:   hasIPv6,
			Username:  username,
			Password:  password,
			Name:      name,
		}
		encoded, err := EncodeDeepLink(config)
		if err != nil {
			return
		}
		decoded, err := DecodeDeepLink(encoded)
		if err != nil {
			t.Fatalf("decode encoded link: %v", err)
		}
		if !reflect.DeepEqual(decoded, config) {
			t.Fatalf("round trip = %#v, want %#v", decoded, config)
		}
	})
}
