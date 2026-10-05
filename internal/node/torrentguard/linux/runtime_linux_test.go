//go:build linux

package linux

import (
	"encoding/binary"
	"testing"

	"github.com/mdlayher/netlink"
)

func TestDecodeQueuedPacketUsesNetworkOrderForInterfaceIndexes(t *testing.T) {
	inIndex := make([]byte, 4)
	binary.BigEndian.PutUint32(inIndex, 42)
	outIndex := make([]byte, 4)
	binary.BigEndian.PutUint32(outIndex, 17)
	attributes, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: nfqAttributePacket, Data: []byte{0, 0, 0, 9}},
		{Type: nfqAttributePayload, Data: []byte{0x45}},
		{Type: nfqAttributeInDevice, Data: inIndex},
		{Type: nfqAttributeOutDevice, Data: outIndex},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := append(make([]byte, 4), attributes...)
	packet, ok := decodeQueuedPacket(data)
	if !ok || packet.inIndex != 42 || packet.outIndex != 17 {
		t.Fatalf("decodeQueuedPacket() = %#v, %v", packet, ok)
	}
}

// Only what a client sends out through its AWG interface is classified; what comes back from the outside, or crosses
// from one tunnel to another, is never evidence.
func TestOnlyPacketsFromATunnelClientToTheOutsideAreClassified(t *testing.T) {
	runtime := &Runtime{ifaceByIndex: map[int]string{10: "mgawg51820", 11: "mgawg51821"}}
	runtime.ifaceMu.RLock()
	defer runtime.ifaceMu.RUnlock()
	if iface, ok := runtime.clientIfaceLocked(10, 20); !ok || iface != "mgawg51820" {
		t.Fatalf("client to the outside = %q, %v", iface, ok)
	}
	if _, ok := runtime.clientIfaceLocked(20, 10); ok {
		t.Fatal("a packet from the outside to a client was selected")
	}
	if _, ok := runtime.clientIfaceLocked(10, 11); ok {
		t.Fatal("tunnel-to-tunnel packet was selected for inspection")
	}
}

// A block is a repeat with the block mark (the nft chain turns it into the connection's ct mark); an accept has no mark.
func TestVerdictMessages(t *testing.T) {
	decode := func(verdict, mark uint32) (gotVerdict, gotID, gotMark uint32, hasMark bool) {
		t.Helper()
		message, err := verdictMessage(4242, 9, verdict, mark)
		if err != nil {
			t.Fatal(err)
		}
		if binary.BigEndian.Uint16(message.Data[2:4]) != 4242 {
			t.Fatalf("queue = %d", binary.BigEndian.Uint16(message.Data[2:4]))
		}
		attributes, err := netlink.UnmarshalAttributes(message.Data[4:])
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range attributes {
			switch a.Type {
			case nfqAttributeVerdict:
				gotVerdict, gotID = binary.BigEndian.Uint32(a.Data[:4]), binary.BigEndian.Uint32(a.Data[4:8])
			case nfqAttributeMark:
				gotMark, hasMark = binary.BigEndian.Uint32(a.Data), true
			}
		}
		return
	}
	if v, id, m, ok := decode(nfqRepeat, BlockMark); v != 4 || id != 9 || !ok || m != BlockMark {
		t.Fatalf("block verdict = %d id %d mark %#x (%v)", v, id, m, ok)
	}
	if v, id, _, ok := decode(nfqAccept, 0); v != 1 || id != 9 || ok {
		t.Fatalf("accept verdict = %d id %d, mark present %v", v, id, ok)
	}
}
