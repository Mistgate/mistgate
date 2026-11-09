// Package netstest provides network-table fixtures shared by node engine tests.
package netstest

import (
	"bytes"
	"net/netip"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/asndb"
)

// LoopbackBytes returns an ASN table that maps all loopback v4 and v6 addresses to asn.
func LoopbackBytes(asn uint32) ([]byte, error) {
	table := asndb.Table{
		Built: time.Unix(1_800_000_000, 0),
		IPv4:  []asndb.IPv4Range{{Start: 0x7f000000, End: 0x7fffffff, ASN: asn}},
		IPv6:  []asndb.IPv6Range{{Start: 0, End: 0, ASN: asn}},
		ASNs:  []asndb.Operator{{ASN: asn, Country: "US", Name: "Loopback Network"}},
	}
	var raw bytes.Buffer
	if err := asndb.Write(&raw, table); err != nil {
		return nil, err
	}
	return raw.Bytes(), nil
}

// LoopbackDB parses LoopbackBytes for use by an engine fixture.
func LoopbackDB(asn uint32) (*asndb.DB, error) {
	raw, err := LoopbackBytes(asn)
	if err != nil {
		return nil, err
	}
	return asndb.Parse(bytes.NewReader(raw))
}

// AddressForms returns the text, native address bytes, and 16-byte mapped form used by privacy tests.
func AddressForms(value string) ([][]byte, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return nil, err
	}
	forms := [][]byte{[]byte(addr.String())}
	if addr.Is4() {
		v4 := addr.As4()
		forms = append(forms, v4[:])
	}
	v6 := addr.As16()
	forms = append(forms, v6[:])
	return forms, nil
}

// LogBuffer is a synchronized writer for engine and agent logs emitted by fixture goroutines.
type LogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
