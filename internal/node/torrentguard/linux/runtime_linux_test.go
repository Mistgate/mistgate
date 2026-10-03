//go:build linux

package linux

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/mdlayher/netlink"
)

func TestRuntimeRetriesUnreportedDetectionOnTimer(t *testing.T) {
	runtime := &Runtime{
		tracker:   newFlowTracker(),
		events:    make(chan Detection, 1),
		retryStop: make(chan struct{}),
		retryDone: make(chan struct{}),
	}
	runtime.events <- Detection{} // hold the bounded queue full for first classification
	request := makeUDP4("10.0.0.2", "198.51.100.7", 55000, 6969, trackerConnectRequest())
	if !runtime.tracker.Process(request, "mgawg51820", netip.MustParseAddr("10.0.0.2"), time.Now(), runtime.enqueueEvent) {
		t.Fatal("confirmed tracker flow was passed")
	}

	go runtime.retryReports()
	defer runtime.stopReportRetries()
	<-runtime.events // free capacity without sending another packet in this flow
	select {
	case detection := <-runtime.events:
		if detection.Signature == "" {
			t.Fatal("timer enqueued an empty detection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not retry the pending detection")
	}
}

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

func TestTunnelSideRequiresExactlyOneActiveAWGInterface(t *testing.T) {
	runtime := &Runtime{ifaceByIndex: map[int]string{10: "mgawg51820", 11: "mgawg51821"}}
	packet, ok := parsePacket(makeTCP4("10.0.0.2", "198.51.100.7", 51000, 443, 1, 0x02, nil))
	if !ok {
		t.Fatal("test packet did not parse")
	}

	runtime.ifaceMu.RLock()
	iface, tunnelIP, valid := runtime.tunnelSideLocked(10, 20, packet)
	runtime.ifaceMu.RUnlock()
	if !valid || iface != "mgawg51820" || tunnelIP.String() != "10.0.0.2" {
		t.Fatalf("inbound tunnel side = %q %s %v", iface, tunnelIP, valid)
	}

	outPacket, ok := parsePacket(makeTCP4("198.51.100.7", "10.0.0.2", 443, 51000, 2, 0x02, nil))
	if !ok {
		t.Fatal("reverse test packet did not parse")
	}
	runtime.ifaceMu.RLock()
	iface, tunnelIP, valid = runtime.tunnelSideLocked(20, 10, outPacket)
	runtime.ifaceMu.RUnlock()
	if !valid || iface != "mgawg51820" || tunnelIP.String() != "10.0.0.2" {
		t.Fatalf("outbound tunnel side = %q %s %v", iface, tunnelIP, valid)
	}

	runtime.ifaceMu.RLock()
	_, _, valid = runtime.tunnelSideLocked(10, 11, packet)
	runtime.ifaceMu.RUnlock()
	if valid {
		t.Fatal("tunnel-to-tunnel packet was selected for inspection")
	}
}
