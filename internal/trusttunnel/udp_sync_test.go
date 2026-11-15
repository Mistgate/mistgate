package trusttunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

// Facts of the official reader (TrustTunnel lib/src/http_udp_codec.rs, the
// client's core/src/http_udp_multiplexer.cpp) that these tests pin with bytes
// written by hand:
//
//   - Length counts everything after the Length field itself.
//   - A client frame needs Length >= 37; shorter frames are dropped whole
//     (Length bytes are discarded).
//   - Once the 37 fixed bytes are read, a frame with Length > 65471 - L, or
//     Length < 37 + L, is dropped: Length - 37 further bytes are discarded.
//   - A name that is not UTF-8 drops the packet, payload included.
//   - A server frame is 36 + payload; the official client takes up to
//     65527 payload bytes.
//
// In every case the stream must stay in step: the good frame placed after the
// bad one has to come out, whatever the chunking.

// handClient builds a client frame without the codec: length, source, destination, name length, name, payload.
func handClient(length uint32, nameLength byte, rest ...[]byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, length)
	out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 192, 0, 2, 1) // source 192.0.2.1
	out = append(out, 0x30, 0x39)                                       // port 12345
	out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 192, 0, 2, 2) // destination 192.0.2.2
	out = append(out, 0, 53)                                            // port 53
	out = append(out, nameLength)
	return append(out, bytes.Join(rest, nil)...)
}

func handServer(length uint32, payload []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, length)
	out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 192, 0, 2, 2)
	out = append(out, 0, 53)
	out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 192, 0, 2, 1)
	out = append(out, 0x30, 0x39)
	return append(out, payload...)
}

var goodClientFrame = handClient(37+3+2, 3, []byte("app"), []byte{0xaa, 0xbb})

func wantGoodClient(t *testing.T, frames []UDPFrame) {
	t.Helper()
	want := UDPFrame{
		AppName: "app",
		Source:  netip.MustParseAddrPort("192.0.2.1:12345"),
		Target:  netip.MustParseAddrPort("192.0.2.2:53"),
		Payload: []byte{0xaa, 0xbb},
	}
	if len(frames) != 1 || !reflect.DeepEqual(frames[0], want) {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestUDPClientDecoderStaysInStepAfterBadFrames(t *testing.T) {
	filler := func(n int) []byte { return bytes.Repeat([]byte{0x5a}, n) }
	tests := []struct {
		name string
		bad  []byte
		want error
	}{
		{name: "length 0", bad: []byte{0, 0, 0, 0}, want: ErrUDPFrameTooShort},
		{name: "length 36", bad: append([]byte{0, 0, 0, 36}, filler(36)...), want: ErrUDPFrameTooShort},
		{
			// Fixed header read, name length 5 does not fit into Length = 40.
			name: "name does not fit", want: ErrUDPFrameTooShort,
			bad: handClient(40, 5, filler(3)),
		},
		{
			// L = 255 allows Length <= 65471 - 255 = 65216; 65217 is dropped.
			name: "over the limit for a 255-byte name", want: ErrUDPFrameTooLarge,
			bad: handClient(65217, 255, filler(65217-37)),
		},
		{
			name: "one over the limit with no name", want: ErrUDPFrameTooLarge,
			bad: handClient(65472, 0, filler(65472-37)),
		},
		{
			name: "name is not UTF-8", want: ErrInvalidUDPFrame,
			bad: handClient(37+2+4, 2, []byte{0xff, 0xfe}, []byte("data")),
		},
		{
			name: "a 3-byte sequence cut short", want: ErrInvalidUDPFrame,
			bad: handClient(37+2+1, 2, []byte{0xe2, 0x82}, []byte("x")),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := append(append([]byte(nil), test.bad...), goodClientFrame...)
			stream = append(stream, test.bad...)
			stream = append(stream, goodClientFrame...)

			// All at once.
			frames, err := NewUDPDecoder(UDPClientToServer).Feed(stream)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(frames) != 2 {
				t.Fatalf("decoded %d frames, want the 2 good ones", len(frames))
			}
			wantGoodClient(t, frames[:1])
			wantGoodClient(t, frames[1:])

			// One byte at a time, and in 7-byte chunks.
			for _, size := range []int{1, 7} {
				decoder := NewUDPDecoder(UDPClientToServer)
				var got []UDPFrame
				for rest := stream; len(rest) > 0; {
					n := min(size, len(rest))
					decoded, _ := decoder.Feed(rest[:n])
					got = append(got, decoded...)
					rest = rest[n:]
				}
				if len(got) != 2 {
					t.Fatalf("chunk %d: decoded %d frames", size, len(got))
				}
				wantGoodClient(t, got[:1])
				wantGoodClient(t, got[1:])
			}
		})
	}
}

func TestUDPClientDecoderLimitsAreExact(t *testing.T) {
	// Largest accepted for L = 255: Length = 65471 - 255 = 65216, payload 65216 - 37 - 255 = 64924.
	name := bytes.Repeat([]byte("n"), 255)
	payload := bytes.Repeat([]byte{1}, 65216-37-255)
	frames, err := NewUDPDecoder(UDPClientToServer).Feed(handClient(65216, 255, name, payload))
	if err != nil || len(frames) != 1 || len(frames[0].Payload) != 64924 || len(frames[0].AppName) != 255 {
		t.Fatalf("limit frame: %d frames, %v", len(frames), err)
	}
	// Largest for L = 0: Length = 65471.
	frames, err = NewUDPDecoder(UDPClientToServer).Feed(handClient(65471, 0, bytes.Repeat([]byte{1}, 65471-37)))
	if err != nil || len(frames) != 1 || len(frames[0].Payload) != 65434 {
		t.Fatalf("limit frame, no name: %d frames, %v", len(frames), err)
	}
	// Empty payload, empty name: Length = 37 exactly.
	frames, err = NewUDPDecoder(UDPClientToServer).Feed(handClient(37, 0))
	if err != nil || len(frames) != 1 || len(frames[0].Payload) != 0 || frames[0].AppName != "" {
		t.Fatalf("minimum frame: %d frames, %v", len(frames), err)
	}
}

func TestUDPServerDecoderLimits(t *testing.T) {
	good := handServer(36+2, []byte{0xaa, 0xbb})
	want := UDPFrame{
		Source:  netip.MustParseAddrPort("192.0.2.2:53"),
		Target:  netip.MustParseAddrPort("192.0.2.1:12345"),
		Payload: []byte{0xaa, 0xbb},
	}
	frames, err := NewUDPDecoder(UDPServerToClient).Feed(good)
	if err != nil || len(frames) != 1 || !reflect.DeepEqual(frames[0], want) {
		t.Fatalf("good server frame: %#v, %v", frames, err)
	}
	// Length 36 is a frame with an empty payload; 35 is dropped whole.
	frames, err = NewUDPDecoder(UDPServerToClient).Feed(handServer(36, nil))
	if err != nil || len(frames) != 1 || len(frames[0].Payload) != 0 {
		t.Fatalf("empty server frame: %#v, %v", frames, err)
	}
	short := append([]byte{0, 0, 0, 35}, bytes.Repeat([]byte{9}, 35)...)
	frames, err = NewUDPDecoder(UDPServerToClient).Feed(append(short, good...))
	if !errors.Is(err, ErrUDPFrameTooShort) || len(frames) != 1 || !reflect.DeepEqual(frames[0], want) {
		t.Fatalf("after a short frame: %#v, %v", frames, err)
	}
	// The official client reads up to 65535 - 8 payload bytes (Length 65563).
	frames, err = NewUDPDecoder(UDPServerToClient).Feed(handServer(36+65527, bytes.Repeat([]byte{1}, 65527)))
	if err != nil || len(frames) != 1 || len(frames[0].Payload) != 65527 {
		t.Fatalf("largest server frame: %d frames, %v", len(frames), err)
	}
	over := handServer(36+65528, bytes.Repeat([]byte{1}, 65528))
	frames, err = NewUDPDecoder(UDPServerToClient).Feed(append(over, good...))
	if !errors.Is(err, ErrUDPFrameTooLarge) || len(frames) != 1 || !reflect.DeepEqual(frames[0], want) {
		t.Fatalf("after an oversized frame: %d frames, %v", len(frames), err)
	}
	// Our own encoder stays at the official server's 65508 bytes.
	frame := UDPFrame{Source: want.Source, Target: want.Target, Payload: make([]byte, MaxUDPServerPayload+1)}
	if _, err := EncodeUDPServerFrame(frame); !errors.Is(err, ErrUDPFrameTooLarge) {
		t.Fatalf("encoder error = %v", err)
	}
}

func TestUDPAddressFieldReading(t *testing.T) {
	zeros := func(n int) []byte { return make([]byte, n) }
	tests := []struct {
		name  string
		field []byte
		want  string
	}{
		{"IPv4 as 12 zeros and 4 bytes", append(zeros(12), 192, 0, 2, 7), "192.0.2.7"},
		{"IPv4 unspecified", zeros(16), "0.0.0.0"},
		// The client's reader and the encoder agree that ::1 is IPv6. The
		// official server's reader (net_utils.rs get_fixed_size_ip) has no
		// such exception and would read these bytes as 0.0.0.1.
		{"IPv6 loopback", append(zeros(15), 1), "::1"},
		{"IPv4-mapped IPv6 stays IPv6", append(append(zeros(10), 0xff, 0xff), 192, 0, 2, 7), "::ffff:192.0.2.7"},
		{"IPv6", []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x53}, "2001:db8::53"},
		{"one non-zero byte in the first 12", append(append(zeros(11), 1), 0, 0, 0, 5), "::1:0:5"},
	}
	for _, test := range tests {
		if got := decodedIP(test.field); got != netip.MustParseAddr(test.want) {
			t.Errorf("%s: %v, want %s", test.name, got, test.want)
		}
	}

	// What the encoder will not write because the wire cannot tell it apart
	// from another address.
	for _, address := range []string{"0.0.0.1", "::", "::5", "::192.0.2.7"} {
		frame := UDPFrame{Source: netip.AddrPortFrom(netip.MustParseAddr(address), 1), Target: netip.MustParseAddrPort("192.0.2.2:53")}
		if _, err := EncodeUDPServerFrame(frame); !errors.Is(err, ErrInvalidUDPFrame) {
			t.Errorf("%s encoded: %v", address, err)
		}
	}
	// And what it writes: IPv4-mapped goes out as plain IPv4, ::1 as ::1.
	frame := UDPFrame{Source: netip.MustParseAddrPort("[::ffff:192.0.2.1]:12345"), Target: netip.MustParseAddrPort("[::1]:53")}
	wire, err := EncodeUDPServerFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	wantSource := append(zeros(12), 192, 0, 2, 1)
	wantTarget := append(zeros(15), 1)
	if !bytes.Equal(wire[4:20], wantSource) || !bytes.Equal(wire[22:38], wantTarget) {
		t.Fatalf("addresses %x / %x", wire[4:20], wire[22:38])
	}
	for _, address := range []string{"0.0.0.0", "127.0.0.1", "255.255.255.255", "::1"} {
		frame := UDPFrame{Source: netip.AddrPortFrom(netip.MustParseAddr(address), 1), Target: netip.MustParseAddrPort("192.0.2.2:53")}
		wire, err := EncodeUDPServerFrame(frame)
		if err != nil {
			t.Fatalf("%s: %v", address, err)
		}
		decoded, err := NewUDPDecoder(UDPServerToClient).Feed(wire)
		if err != nil || len(decoded) != 1 || decoded[0].Source != frame.Source {
			t.Fatalf("%s: %#v, %v", address, decoded, err)
		}
	}
}

func TestUDPEncoderAppNameRules(t *testing.T) {
	base := UDPFrame{Source: netip.MustParseAddrPort("192.0.2.1:1"), Target: netip.MustParseAddrPort("192.0.2.2:2")}
	base.AppName = "\xff"
	if _, err := EncodeUDPClientFrame(base); !errors.Is(err, ErrInvalidUDPFrame) {
		t.Fatalf("non-UTF-8 name: %v", err)
	}
	base.AppName = string(bytes.Repeat([]byte("n"), 256))
	if _, err := EncodeUDPClientFrame(base); !errors.Is(err, ErrUDPFrameTooLarge) {
		t.Fatalf("256-byte name: %v", err)
	}
	base.AppName = string(bytes.Repeat([]byte("n"), 255))
	wire, err := EncodeUDPClientFrame(base)
	if err != nil || wire[40] != 255 {
		t.Fatalf("255-byte name: %v", err)
	}
}
