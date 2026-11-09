package asndb

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixtureTable() (Table, error) {
	table := Table{Built: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)}
	for _, source := range []struct {
		file   string
		family Family
	}{{"ip2asn-v4.tsv", FamilyIPv4}, {"ip2asn-v6.tsv", FamilyIPv6}} {
		f, err := os.Open(filepath.Join("testdata", source.file))
		if err != nil {
			return Table{}, err
		}
		parseErr := AppendTSV(f, source.family, &table)
		closeErr := f.Close()
		if parseErr != nil {
			return Table{}, fmt.Errorf("AppendTSV(%s): %w", source.file, parseErr)
		}
		if closeErr != nil {
			return Table{}, closeErr
		}
	}
	return table, nil
}

func fixtureTable(t testing.TB) Table {
	t.Helper()
	table, err := loadFixtureTable()
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func fixtureDB(t *testing.T) *DB {
	t.Helper()
	var encoded bytes.Buffer
	if err := Write(&encoded, fixtureTable(t)); err != nil {
		t.Fatal(err)
	}
	db, err := Parse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFormatRoundTripAndLookup(t *testing.T) {
	table := fixtureTable(t)
	var encoded bytes.Buffer
	if err := Write(&encoded, table); err != nil {
		t.Fatal(err)
	}
	db, err := Parse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := db.Built(), table.Built; !got.Equal(want) {
		t.Fatalf("Built() = %s, want %s", got, want)
	}
	if got := db.IPv4RangeCount(); got != 4 {
		t.Fatalf("IPv4RangeCount() = %d, want 4 after merging adjacent same-ASN ranges", got)
	}
	if got := db.IPv6RangeCount(); got != 1 {
		t.Fatalf("IPv6RangeCount() = %d, want 1", got)
	}
	if got := db.ASNCount(); got != 4 {
		t.Fatalf("ASNCount() = %d, want 4", got)
	}
	for _, test := range []struct {
		address string
		want    uint32
	}{
		{"192.0.2.0", 64500},
		{"192.0.2.255", 64500},
		{"192.0.3.0", 0},
		{"198.51.100.0", 64500},
		{"198.51.100.63", 64500},
		{"198.51.100.64", 64500},
		{"198.51.100.127", 64500},
		{"198.51.100.128", 64501},
		{"198.51.100.255", 64501},
		{"198.51.101.0", 0},
		{"203.0.113.0", 64502},
		{"203.0.113.255", 64502},
		{"::ffff:192.0.2.1", 64500},
		{"2001:db8::", 64503},
		{"2001:db8:abcd:1234::1", 64503},
		{"2001:db9::1", 0},
	} {
		t.Run(test.address, func(t *testing.T) {
			addr := netip.MustParseAddr(test.address)
			if got := db.Lookup(addr); got != test.want {
				t.Fatalf("Lookup(%s) = %d, want %d", addr, got, test.want)
			}
		})
	}
	if got := db.Lookup(netip.Addr{}); got != 0 {
		t.Fatalf("Lookup(invalid) = %d, want 0", got)
	}
	if country, name, ok := db.Name(64501); !ok || country != "NL" || name != "Example Transit" {
		t.Fatalf("Name(64501) = %q, %q, %v", country, name, ok)
	}
	if country, name, ok := db.Name(64502); !ok || country != "GB" || len(name) > maxOperatorBytes || containsAddressLike(name) {
		t.Fatalf("Name(64502) = %q, %q, %v", country, name, ok)
	}
	if _, _, ok := db.Name(0); ok {
		t.Fatal("Name(0) unexpectedly found an operator")
	}
	if _, _, ok := db.Name(64599); ok {
		t.Fatal("Name(unknown) unexpectedly found an operator")
	}
}

func TestContentHashAndDeterministicWriter(t *testing.T) {
	table := fixtureTable(t)
	var first, second bytes.Buffer
	if err := Write(&first, table); err != nil {
		t.Fatal(err)
	}
	if err := Write(&second, table); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("Write produced different gzip bytes for the same table")
	}
	left, err := Parse(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse(bytes.NewReader(second.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if left.ContentHash() != right.ContentHash() {
		t.Fatal("ContentHash differs after an identical round trip")
	}
	if got, want := left.ContentHash(), sha256.Sum256(left.raw); got != want {
		t.Fatalf("ContentHash() = %x, want SHA-256 of the canonical payload %x", got, want)
	}
	changed := fixtureTable(t)
	changed.Built = changed.Built.Add(time.Second)
	var changedBytes bytes.Buffer
	if err := Write(&changedBytes, changed); err != nil {
		t.Fatal(err)
	}
	changedDB, err := Parse(bytes.NewReader(changedBytes.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if left.ContentHash() == changedDB.ContentHash() {
		t.Fatal("ContentHash did not change when the table content changed")
	}
}

func TestLookupAllocatesNothing(t *testing.T) {
	db := fixtureDB(t)
	address := netip.MustParseAddr("198.51.100.200")
	allocs := testing.AllocsPerRun(100, func() {
		benchmarkASN = db.Lookup(address)
	})
	if allocs != 0 {
		t.Fatalf("Lookup allocated %.2f objects per call, want 0", allocs)
	}
}

func TestAppendTSVAcceptsGzip(t *testing.T) {
	plain, err := os.ReadFile(filepath.Join("testdata", "ip2asn-v4.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	table := Table{Built: time.Unix(1_800_000_000, 0)}
	if err := AppendTSV(bytes.NewReader(compressed.Bytes()), FamilyIPv4, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.IPv4) != 5 {
		t.Fatalf("got %d source ranges, want 5", len(table.IPv4))
	}
}

func TestAppendTSVRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "unsorted",
			data: "198.51.100.0\t198.51.100.1\t64500\tDE\tExample Telecom\n192.0.2.0\t192.0.2.1\t64501\tNL\tExample Transit\n",
			want: "unsorted",
		},
		{
			name: "overlapping",
			data: "192.0.2.0\t192.0.2.100\t64500\tDE\tExample Telecom\n192.0.2.100\t192.0.2.200\t64501\tNL\tExample Transit\n",
			want: "overlaps",
		},
		{
			name: "invalid ASN",
			data: "192.0.2.0\t192.0.2.1\t4294967296\tDE\tExample Telecom\n",
			want: "invalid ASN",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var table Table
			err := AppendTSV(strings.NewReader(test.data), FamilyIPv4, &table)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AppendTSV error = %v, want text %q", err, test.want)
			}
		})
	}
	t.Run("huge line", func(t *testing.T) {
		line := "192.0.2.0\t192.0.2.1\t64500\tDE\t" + strings.Repeat("x", maxTSVLineBytes+1) + "\n"
		var table Table
		err := AppendTSV(strings.NewReader(line), FamilyIPv4, &table)
		if err == nil || !strings.Contains(err.Error(), "line exceeds") {
			t.Fatalf("AppendTSV error = %v, want oversized-line error", err)
		}
	})
}

func TestWriteRejectsUnsortedAndOverlappingRanges(t *testing.T) {
	base := Table{Built: time.Unix(1_800_000_000, 0), ASNs: []Operator{{ASN: 64500, Country: "DE", Name: "Example Telecom"}}}
	for _, test := range []struct {
		name   string
		ranges []IPv4Range
		want   string
	}{
		{"unsorted", []IPv4Range{{Start: 200, End: 210, ASN: 64500}, {Start: 100, End: 110, ASN: 64500}}, "unsorted"},
		{"overlap", []IPv4Range{{Start: 100, End: 210, ASN: 64500}, {Start: 200, End: 220, ASN: 64500}}, "overlaps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			table := base
			table.IPv4 = test.ranges
			err := Write(io.Discard, table)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Write error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsCorruptTables(t *testing.T) {
	db := fixtureDB(t)
	tests := []struct {
		name string
		edit func([]byte)
		want string
	}{
		{"bad magic", func(raw []byte) { raw[0] = 'X' }, "bad magic"},
		{"bad version", func(raw []byte) { binary.BigEndian.PutUint16(raw[6:8], 2) }, "version"},
		{"huge count", func(raw []byte) { binary.BigEndian.PutUint32(raw[16:20], ^uint32(0)) }, "counts exceed format limits"},
		{"unsorted index", func(raw []byte) { binary.BigEndian.PutUint32(raw[headerSize+v4EntrySize:headerSize+v4EntrySize+4], 0) }, "not strictly sorted"},
		{"repeated ASN", func(raw []byte) {
			binary.BigEndian.PutUint32(raw[headerSize+2*v4EntrySize+4:headerSize+3*v4EntrySize], 64500)
		}, "adjacent IPv4 index entries repeat ASN"},
		{"unused ASN", func(raw []byte) {
			n4 := binary.BigEndian.Uint32(raw[16:20])
			v6Offset := headerSize + int(n4)*v4EntrySize
			binary.BigEndian.PutUint32(raw[v6Offset+v6EntrySize+8:v6Offset+2*v6EntrySize], 64500)
		}, "ASN 64503 has no routed range"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := append([]byte(nil), db.raw...)
			test.edit(raw)
			encoded := gzipPayload(t, raw)
			_, err := Parse(bytes.NewReader(encoded))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want text %q", err, test.want)
			}
		})
	}
	short := make([]byte, headerSize)
	copy(short[:6], magic[:])
	binary.BigEndian.PutUint16(short[6:8], formatVersion)
	binary.BigEndian.PutUint64(short[8:16], uint64(time.Unix(1_800_000_000, 0).Unix()))
	binary.BigEndian.PutUint32(short[16:20], 1)
	binary.BigEndian.PutUint32(short[20:24], 1)
	binary.BigEndian.PutUint32(short[24:28], 1)
	if _, err := Parse(bytes.NewReader(gzipPayload(t, short))); err == nil || !strings.Contains(err.Error(), "counts need") {
		t.Fatalf("short payload count error = %v", err)
	}
	var encoded bytes.Buffer
	if err := Write(&encoded, fixtureTable(t)); err != nil {
		t.Fatal(err)
	}
	truncated := encoded.Bytes()[:encoded.Len()-2]
	if _, err := Parse(bytes.NewReader(truncated)); err == nil {
		t.Fatal("Parse accepted a truncated gzip stream")
	}
}

func TestValidateReleaseChecksCountsAndAge(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	limits := ReleaseLimits{MinIPv4Ranges: 4, MinASNs: 4, MaxAge: 45 * 24 * time.Hour}
	table := fixtureTable(t)
	table.Built = now.Add(-45 * 24 * time.Hour)
	var encoded bytes.Buffer
	if err := Write(&encoded, table); err != nil {
		t.Fatal(err)
	}
	db, err := Parse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReleaseWith(db, now, limits); err != nil {
		t.Fatalf("table exactly at count and age limits was refused: %v", err)
	}
	if err := ValidateReleaseWith(db, now, ReleaseLimits{MinIPv4Ranges: 5, MinASNs: 4, MaxAge: limits.MaxAge}); err == nil || !strings.Contains(err.Error(), "IPv4 ranges") {
		t.Fatalf("range check error = %v", err)
	}
	if err := ValidateReleaseWith(db, now, ReleaseLimits{MinIPv4Ranges: 4, MinASNs: 5, MaxAge: limits.MaxAge}); err == nil || !strings.Contains(err.Error(), "ASNs") {
		t.Fatalf("ASN check error = %v", err)
	}
	table.Built = now.Add(-limits.MaxAge - time.Second)
	encoded.Reset()
	if err := Write(&encoded, table); err != nil {
		t.Fatal(err)
	}
	db, err = Parse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReleaseWith(db, now, limits); err == nil || !strings.Contains(err.Error(), "older than") {
		t.Fatalf("stale table check error = %v", err)
	}
}

func gzipPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	gz := gzip.NewWriter(&encoded)
	if _, err := gz.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func FuzzParse(f *testing.F) {
	table, err := loadFixtureTable()
	if err != nil {
		f.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := Write(&encoded, table); err != nil {
		f.Fatal(err)
	}
	f.Add(encoded.Bytes())
	f.Add([]byte("not gzip"))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = Parse(bytes.NewReader(input))
	})
}

var benchmarkASN uint32

func BenchmarkLookup(b *testing.B) {
	const benchmarkRanges = 150_000
	const firstIPv6Block = uint64(0x20010db800000000) // within 2001:db8::/32
	table := Table{
		Built: time.Unix(1_800_000_000, 0),
		IPv6:  make([]IPv6Range, benchmarkRanges),
		ASNs:  []Operator{{ASN: 64503, Country: "US", Name: "Example IPv6 Network"}},
	}
	for i := range table.IPv6 {
		start := firstIPv6Block + uint64(i*2)
		table.IPv6[i] = IPv6Range{Start: start, End: start, ASN: 64503}
	}
	var encoded bytes.Buffer
	if err := Write(&encoded, table); err != nil {
		b.Fatal(err)
	}
	db, err := Parse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		b.Fatal(err)
	}
	var addressBytes [16]byte
	binary.BigEndian.PutUint64(addressBytes[:8], firstIPv6Block+uint64((benchmarkRanges-1)*2))
	address := netip.AddrFrom16(addressBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkASN = db.Lookup(address)
	}
}
