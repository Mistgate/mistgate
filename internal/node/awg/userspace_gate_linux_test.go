//go:build linux

package awg

import (
	"crypto/rand"
	"encoding/binary"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn/bindtest"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
)

// memTUN is a TUN device in memory: tests push packets into in and read what the device wrote from out.
type memTUN struct {
	in, out chan []byte
	events  chan tun.Event
	done    chan struct{}
	once    sync.Once
}

func newMemTUN() *memTUN {
	return &memTUN{in: make(chan []byte, 16), out: make(chan []byte, 16), events: make(chan tun.Event), done: make(chan struct{})}
}

func (m *memTUN) File() *os.File           { return nil }
func (m *memTUN) MTU() (int, error)        { return 1420, nil }
func (m *memTUN) Name() (string, error)    { return "mem", nil }
func (m *memTUN) Events() <-chan tun.Event { return m.events }
func (m *memTUN) BatchSize() int           { return 1 }
func (m *memTUN) Close() error {
	m.once.Do(func() { close(m.done); close(m.events) })
	return nil
}
func (m *memTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case p := <-m.in:
		sizes[0] = copy(bufs[0][offset:], p)
		return 1, nil
	case <-m.done:
		return 0, os.ErrClosed
	}
}
func (m *memTUN) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		m.out <- append([]byte(nil), b[offset:]...)
	}
	return len(bufs), nil
}

func ipv4Packet(src, dst netip.Addr, payload byte) []byte {
	p := make([]byte, 28)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8], p[9] = 64, 17
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	p[27] = payload
	return p
}

// TestFirstPacketAfterStartCarriesS4 starts two userspace devices with S4 set, the way Create does, and sends the
// very first packet through the first one right after its start. A peer that checks the padding (amneziawg-go does)
// must receive it. Without the gate the TUN reader has loaded S4 = 0 before the configuration and the first batch
// goes out unpadded: the peer drops it, the handshake that it triggered does succeed, so only this packet is lost.
func TestFirstPacketAfterStartCarriesS4(t *testing.T) {
	var privA, privB [32]byte
	rand.Read(privA[:])
	rand.Read(privB[:])
	pubA, _ := curve25519.X25519(privA[:], curve25519.Basepoint)
	pubB, _ := curve25519.X25519(privB[:], curve25519.Basepoint)
	obf := awgcfg.Obfuscation{S4: 24, H1: awgcfg.Range{Lo: 1, Hi: 1}, H2: awgcfg.Range{Lo: 2, Hi: 2}, H3: awgcfg.Range{Lo: 3, Hi: 3}, H4: awgcfg.Range{Lo: 4, Hi: 4}}
	addrA, addrB := netip.MustParseAddr("10.9.0.1"), netip.MustParseAddr("10.9.0.2")
	var pa, pb awgcfg.Peer
	copy(pb.PublicKey[:], pubB)
	pb.Endpoint, pb.AllowedIPs = "127.0.0.1:1", []netip.Prefix{netip.PrefixFrom(addrB, 32)} // A's bind sends to its target4
	copy(pa.PublicKey[:], pubA)
	pa.AllowedIPs = []netip.Prefix{netip.PrefixFrom(addrA, 32)}

	binds := bindtest.NewChannelBinds()
	quiet := device.NewLogger(device.LogLevelSilent, "")
	tunA, tunB := newMemTUN(), newMemTUN()

	devA, err := newConfiguredDevice(tunA, binds[0], quiet, func(dev *device.Device) error {
		// the reader goroutine of NewDevice has run and loaded S4 by now: the order that the bug needs
		time.Sleep(100 * time.Millisecond)
		return dev.IpcSet(awguapi.DeviceSet(awgcfg.Version31, privA, 0, obf) + awguapi.PeersSet(false, []awgcfg.Peer{pb}))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer devA.Close()
	devB, err := newConfiguredDevice(tunB, binds[1], quiet, func(dev *device.Device) error {
		return dev.IpcSet(awguapi.DeviceSet(awgcfg.Version31, privB, 0, obf) + awguapi.PeersSet(false, []awgcfg.Peer{pa}))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer devB.Close()
	if err := devA.Up(); err != nil {
		t.Fatal(err)
	}
	if err := devB.Up(); err != nil {
		t.Fatal(err)
	}

	want := ipv4Packet(addrA, addrB, 0xA5)
	tunA.in <- want // the first packet the device ever reads from its TUN
	select {
	case got := <-tunB.out:
		if string(got) != string(want) {
			t.Fatalf("peer got %x, want %x", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first packet after start never reached the peer: it left without the S4 padding")
	}
}
