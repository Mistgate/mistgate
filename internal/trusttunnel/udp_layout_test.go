package trusttunnel

import (
	"bytes"
	"net/netip"
	"testing"
)

// The byte layout of a client frame, written out by hand from the specification: length, source address and port,
// destination address and port, name length, name, payload. A codec that only round-trips with itself would pass with
// the fields in any order; this pins the order the other side reads.
func TestUDPClientFrameLayout(t *testing.T) {
	want := []byte{0, 0, 0, 37 + 3 + 2}
	want = append(want, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 192, 0, 2, 1) // source 192.0.2.1 as 12 zero bytes + 4
	want = append(want, 0x30, 0x39)                                       // port 12345
	want = append(want, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x53)
	want = append(want, 0, 53)
	want = append(want, 3, 'a', 'p', 'p')
	want = append(want, 0xaa, 0xbb)

	frame := UDPFrame{
		AppName: "app",
		Source:  netip.MustParseAddrPort("192.0.2.1:12345"),
		Target:  netip.MustParseAddrPort("[2001:db8::53]:53"),
		Payload: []byte{0xaa, 0xbb},
	}
	got, err := EncodeUDPClientFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("client frame\n got %x\nwant %x", got, want)
	}
	frames, err := NewUDPDecoder(UDPClientToServer).Feed(want)
	if err != nil || len(frames) != 1 {
		t.Fatalf("decode: %v, %d frames", err, len(frames))
	}
	f := frames[0]
	if f.AppName != "app" || f.Source != frame.Source || f.Target != frame.Target || !bytes.Equal(f.Payload, frame.Payload) {
		t.Fatalf("decoded %+v", f)
	}

	// The server frame is the same without the name.
	wantServer := append([]byte{0, 0, 0, 36 + 2}, want[4:4+36]...)
	wantServer = append(wantServer, 0xaa, 0xbb)
	frame.AppName = ""
	got, err = EncodeUDPServerFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantServer) {
		t.Fatalf("server frame\n got %x\nwant %x", got, wantServer)
	}
}
