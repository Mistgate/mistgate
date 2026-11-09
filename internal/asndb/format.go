// Package asndb reads, writes and looks up the release-pinned autonomous-system table.
package asndb

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"time"
	"unicode/utf8"
)

const (
	formatVersion    = 1
	headerSize       = 28
	v4EntrySize      = 8
	v6EntrySize      = 12
	asnEntrySize     = 11
	maxPayloadSize   = 256 << 20
	maxTableRows     = 1_000_000
	maxOperatorBytes = 48
)

var magic = [6]byte{'M', 'G', 'A', 'S', 'N', '1'}

// IPv4Range and IPv6Range use inclusive endpoints. IPv6 ranges are indexed at /64 granularity.
type IPv4Range struct {
	Start uint32
	End   uint32
	ASN   uint32
}

type IPv6Range struct {
	Start uint64
	End   uint64
	ASN   uint32
}

// Operator names one autonomous system. Country is an ISO 3166-1 alpha-2 code, or empty if unknown.
type Operator struct {
	ASN     uint32
	Country string
	Name    string
}

// Table is the uncompressed input to Write. Ranges must be sorted and non-overlapping; gaps are added by Write.
type Table struct {
	Built         time.Time
	IPv4          []IPv4Range
	IPv6          []IPv6Range
	ASNs          []Operator
	operators     map[uint32]Operator
	sourceV6Start netip.Addr
	sourceV6End   netip.Addr
}

type index4 struct {
	start uint32
	asn   uint32
}

type index6 struct {
	start uint64
	asn   uint32
}

type asnRecord struct {
	asn     uint32
	country [2]byte
	nameOff uint32
	nameLen uint8
}

// DB retains the uncompressed table so Lookup can binary-search its pointer-free sections without allocating.
type DB struct {
	raw       []byte
	built     int64
	v4Offset  int
	n4        uint32
	v6Offset  int
	n6        uint32
	asnOffset int
	na        uint32
	nameOff   int
}

// Write validates table, inserts ASN 0 for gaps, and writes a deterministic gzip stream.
func Write(w io.Writer, table Table) error {
	if w == nil {
		return errors.New("asndb: nil writer")
	}
	if table.Built.Unix() <= 0 {
		return errors.New("asndb: built time must be after the Unix epoch")
	}
	v4, err := buildIPv4Index(table.IPv4)
	if err != nil {
		return fmt.Errorf("asndb: IPv4 ranges: %w", err)
	}
	v6, err := buildIPv6Index(table.IPv6)
	if err != nil {
		return fmt.Errorf("asndb: IPv6 ranges: %w", err)
	}
	operators, nameBlob, err := encodeOperators(table.ASNs, table.IPv4, table.IPv6)
	if err != nil {
		return err
	}
	if uint64(len(v4)) > uint64(^uint32(0)) || uint64(len(v6)) > uint64(^uint32(0)) || uint64(len(operators)) > uint64(^uint32(0)) {
		return errors.New("asndb: too many table entries")
	}
	payloadSize := uint64(headerSize) + uint64(len(v4))*v4EntrySize + uint64(len(v6))*v6EntrySize + uint64(len(operators))*asnEntrySize + uint64(len(nameBlob))
	if payloadSize > maxPayloadSize {
		return fmt.Errorf("asndb: uncompressed table is too large (%d bytes; limit %d)", payloadSize, maxPayloadSize)
	}
	raw := make([]byte, headerSize, int(payloadSize))
	copy(raw[:6], magic[:])
	binary.BigEndian.PutUint16(raw[6:8], formatVersion)
	binary.BigEndian.PutUint64(raw[8:16], uint64(table.Built.Unix()))
	binary.BigEndian.PutUint32(raw[16:20], uint32(len(v4)))
	binary.BigEndian.PutUint32(raw[20:24], uint32(len(v6)))
	binary.BigEndian.PutUint32(raw[24:28], uint32(len(operators)))
	for _, entry := range v4 {
		var encoded [v4EntrySize]byte
		binary.BigEndian.PutUint32(encoded[:4], entry.start)
		binary.BigEndian.PutUint32(encoded[4:], entry.asn)
		raw = append(raw, encoded[:]...)
	}
	for _, entry := range v6 {
		var encoded [v6EntrySize]byte
		binary.BigEndian.PutUint64(encoded[:8], entry.start)
		binary.BigEndian.PutUint32(encoded[8:], entry.asn)
		raw = append(raw, encoded[:]...)
	}
	for _, operator := range operators {
		var encoded [asnEntrySize]byte
		binary.BigEndian.PutUint32(encoded[:4], operator.asn)
		copy(encoded[4:6], operator.country[:])
		binary.BigEndian.PutUint32(encoded[6:10], operator.nameOff)
		encoded[10] = operator.nameLen
		raw = append(raw, encoded[:]...)
	}
	raw = append(raw, nameBlob...)
	if len(raw) != int(payloadSize) {
		return errors.New("asndb: internal size mismatch while writing table")
	}
	gz := gzip.NewWriter(w)
	gz.Header.ModTime = time.Unix(0, 0).UTC()
	if _, err := gz.Write(raw); err != nil {
		_ = gz.Close()
		return fmt.Errorf("asndb: write gzip data: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("asndb: finish gzip data: %w", err)
	}
	return nil
}

// Parse reads and strictly validates a gzip-compressed table.
func Parse(r io.Reader) (*DB, error) {
	if r == nil {
		return nil, errors.New("asndb: nil reader")
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("asndb: gzip header: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(gz, maxPayloadSize+1))
	closeErr := gz.Close()
	if readErr != nil {
		return nil, fmt.Errorf("asndb: read gzip data: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("asndb: finish gzip data: %w", closeErr)
	}
	if len(raw) > maxPayloadSize {
		return nil, fmt.Errorf("asndb: uncompressed table exceeds %d bytes", maxPayloadSize)
	}
	return parsePayload(raw)
}

func parsePayload(raw []byte) (*DB, error) {
	if len(raw) < headerSize {
		return nil, fmt.Errorf("asndb: header is truncated (%d bytes; want %d)", len(raw), headerSize)
	}
	if !bytes.Equal(raw[:6], magic[:]) {
		return nil, errors.New("asndb: bad magic")
	}
	version := binary.BigEndian.Uint16(raw[6:8])
	if version != formatVersion {
		return nil, fmt.Errorf("asndb: unsupported format version %d (want %d)", version, formatVersion)
	}
	built := int64(binary.BigEndian.Uint64(raw[8:16]))
	if built <= 0 {
		return nil, errors.New("asndb: built time must be after the Unix epoch")
	}
	n4 := binary.BigEndian.Uint32(raw[16:20])
	n6 := binary.BigEndian.Uint32(raw[20:24])
	na := binary.BigEndian.Uint32(raw[24:28])
	if n4 == 0 || n6 == 0 {
		return nil, errors.New("asndb: IPv4 and IPv6 indexes must each contain a gap entry")
	}
	maxIndexEntries := uint32(2*maxTableRows + 1)
	if n4 > maxIndexEntries || n6 > maxIndexEntries || na > maxTableRows {
		return nil, fmt.Errorf("asndb: counts exceed format limits (IPv4 %d, IPv6 %d, ASNs %d)", n4, n6, na)
	}
	fixed := uint64(headerSize) + uint64(n4)*v4EntrySize + uint64(n6)*v6EntrySize + uint64(na)*asnEntrySize
	if fixed > uint64(len(raw)) {
		return nil, fmt.Errorf("asndb: counts need at least %d bytes, file has %d", fixed, len(raw))
	}
	v4Offset := headerSize
	v6Offset := v4Offset + int(n4)*v4EntrySize
	asnOffset := v6Offset + int(n6)*v6EntrySize
	nameOffset := asnOffset + int(na)*asnEntrySize
	db := &DB{raw: raw, built: built, v4Offset: v4Offset, n4: n4, v6Offset: v6Offset, n6: n6, asnOffset: asnOffset, na: na, nameOff: nameOffset}
	if err := db.validateIndexes(); err != nil {
		return nil, err
	}
	if err := db.validateOperators(); err != nil {
		return nil, err
	}
	if err := db.validateReferences(); err != nil {
		return nil, err
	}
	return db, nil
}

func (db *DB) validateIndexes() error {
	if binary.BigEndian.Uint32(db.raw[db.v4Offset:db.v4Offset+4]) != 0 {
		return errors.New("asndb: first IPv4 index entry must start at 0.0.0.0")
	}
	prev4ASN := binary.BigEndian.Uint32(db.raw[db.v4Offset+4 : db.v4Offset+8])
	for i := uint32(0); i < db.n4; i++ {
		off := db.v4Offset + int(i)*v4EntrySize
		start := binary.BigEndian.Uint32(db.raw[off : off+4])
		asn := binary.BigEndian.Uint32(db.raw[off+4 : off+8])
		if i > 0 {
			prev := binary.BigEndian.Uint32(db.raw[off-v4EntrySize : off-v4EntrySize+4])
			if start <= prev {
				return fmt.Errorf("asndb: IPv4 index is not strictly sorted at entry %d", i)
			}
			if asn == prev4ASN {
				return fmt.Errorf("asndb: adjacent IPv4 index entries repeat ASN %d", asn)
			}
		}
		prev4ASN = asn
	}
	if binary.BigEndian.Uint64(db.raw[db.v6Offset:db.v6Offset+8]) != 0 {
		return errors.New("asndb: first IPv6 index entry must start at ::/64")
	}
	prev6ASN := binary.BigEndian.Uint32(db.raw[db.v6Offset+8 : db.v6Offset+12])
	for i := uint32(0); i < db.n6; i++ {
		off := db.v6Offset + int(i)*v6EntrySize
		start := binary.BigEndian.Uint64(db.raw[off : off+8])
		asn := binary.BigEndian.Uint32(db.raw[off+8 : off+12])
		if i > 0 {
			prev := binary.BigEndian.Uint64(db.raw[off-v6EntrySize : off-v6EntrySize+8])
			if start <= prev {
				return fmt.Errorf("asndb: IPv6 index is not strictly sorted at entry %d", i)
			}
			if asn == prev6ASN {
				return fmt.Errorf("asndb: adjacent IPv6 index entries repeat ASN %d", asn)
			}
		}
		prev6ASN = asn
	}
	return nil
}

func (db *DB) validateOperators() error {
	nameBlob := db.raw[db.nameOff:]
	var expectedNameOff uint64
	var prevASN uint32
	for i := uint32(0); i < db.na; i++ {
		off := db.asnOffset + int(i)*asnEntrySize
		asn := binary.BigEndian.Uint32(db.raw[off : off+4])
		country := db.raw[off+4 : off+6]
		nameOff := binary.BigEndian.Uint32(db.raw[off+6 : off+10])
		nameLen := db.raw[off+10]
		if asn == 0 || (i > 0 && asn <= prevASN) {
			return fmt.Errorf("asndb: ASN table is not strictly sorted at entry %d", i)
		}
		prevASN = asn
		if !(country[0] == 0 && country[1] == 0) && !(country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z') {
			return fmt.Errorf("asndb: ASN %d has an invalid country code", asn)
		}
		if nameLen == 0 || nameLen > maxOperatorBytes {
			return fmt.Errorf("asndb: ASN %d has an invalid name length %d", asn, nameLen)
		}
		if uint64(nameOff) != expectedNameOff || uint64(nameOff)+uint64(nameLen) > uint64(len(nameBlob)) {
			return fmt.Errorf("asndb: ASN %d name offset is out of order or out of bounds", asn)
		}
		name := nameBlob[nameOff : uint64(nameOff)+uint64(nameLen)]
		if !utf8.Valid(name) || hasControl(name) || containsAddressLike(string(name)) {
			return fmt.Errorf("asndb: ASN %d name is not clean UTF-8 operator text", asn)
		}
		expectedNameOff += uint64(nameLen)
	}
	if expectedNameOff != uint64(len(nameBlob)) {
		return errors.New("asndb: trailing or unreferenced bytes in operator-name table")
	}
	return nil
}

func (db *DB) validateReferences() error {
	used := make([]bool, db.na)
	for i := uint32(0); i < db.n4; i++ {
		off := db.v4Offset + int(i)*v4EntrySize + 4
		asn := binary.BigEndian.Uint32(db.raw[off : off+4])
		if asn != 0 {
			if index, ok := db.findASN(asn); ok {
				used[index] = true
			} else {
				return fmt.Errorf("asndb: IPv4 index refers to missing ASN %d", asn)
			}
		}
	}
	for i := uint32(0); i < db.n6; i++ {
		off := db.v6Offset + int(i)*v6EntrySize + 8
		asn := binary.BigEndian.Uint32(db.raw[off : off+4])
		if asn != 0 {
			if index, ok := db.findASN(asn); ok {
				used[index] = true
			} else {
				return fmt.Errorf("asndb: IPv6 index refers to missing ASN %d", asn)
			}
		}
	}
	for i, ok := range used {
		if !ok {
			off := db.asnOffset + i*asnEntrySize
			return fmt.Errorf("asndb: ASN %d has no routed range", binary.BigEndian.Uint32(db.raw[off:off+4]))
		}
	}
	return nil
}

func (db *DB) findASN(asn uint32) (int, bool) {
	lo, hi := uint32(0), db.na
	for lo < hi {
		mid := lo + (hi-lo)/2
		off := db.asnOffset + int(mid)*asnEntrySize
		got := binary.BigEndian.Uint32(db.raw[off : off+4])
		if got < asn {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == db.na {
		return 0, false
	}
	off := db.asnOffset + int(lo)*asnEntrySize
	return int(lo), binary.BigEndian.Uint32(db.raw[off:off+4]) == asn
}

// Lookup returns the ASN for addr, or 0 for a gap, invalid address, or unsupported family.
func (db *DB) Lookup(addr netip.Addr) uint32 {
	if db == nil || !addr.IsValid() {
		return 0
	}
	addr = addr.Unmap()
	if addr.Is4() {
		a := addr.As4()
		value := binary.BigEndian.Uint32(a[:])
		lo, hi := uint32(0), db.n4
		for lo < hi {
			mid := lo + (hi-lo)/2
			off := db.v4Offset + int(mid)*v4EntrySize
			if binary.BigEndian.Uint32(db.raw[off:off+4]) <= value {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo == 0 {
			return 0
		}
		off := db.v4Offset + int(lo-1)*v4EntrySize + 4
		return binary.BigEndian.Uint32(db.raw[off : off+4])
	}
	if addr.Is6() {
		a := addr.As16()
		value := binary.BigEndian.Uint64(a[:8])
		lo, hi := uint32(0), db.n6
		for lo < hi {
			mid := lo + (hi-lo)/2
			off := db.v6Offset + int(mid)*v6EntrySize
			if binary.BigEndian.Uint64(db.raw[off:off+8]) <= value {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo == 0 {
			return 0
		}
		off := db.v6Offset + int(lo-1)*v6EntrySize + 8
		return binary.BigEndian.Uint32(db.raw[off : off+4])
	}
	return 0
}

// Name returns the country and operator name for asn. The strings are allocated only on this metadata path.
func (db *DB) Name(asn uint32) (country, name string, ok bool) {
	if db == nil || asn == 0 {
		return "", "", false
	}
	lo, hi := uint32(0), db.na
	for lo < hi {
		mid := lo + (hi-lo)/2
		off := db.asnOffset + int(mid)*asnEntrySize
		got := binary.BigEndian.Uint32(db.raw[off : off+4])
		if got < asn {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == db.na {
		return "", "", false
	}
	off := db.asnOffset + int(lo)*asnEntrySize
	if binary.BigEndian.Uint32(db.raw[off:off+4]) != asn {
		return "", "", false
	}
	countryBytes := db.raw[off+4 : off+6]
	nameOff := binary.BigEndian.Uint32(db.raw[off+6 : off+10])
	nameLen := db.raw[off+10]
	if countryBytes[0] != 0 {
		country = string(countryBytes)
	}
	start := db.nameOff + int(nameOff)
	return country, string(db.raw[start : start+int(nameLen)]), true
}

// Built returns the source timestamp stored in the table header.
func (db *DB) Built() time.Time {
	if db == nil {
		return time.Time{}
	}
	return time.Unix(db.built, 0).UTC()
}

// ContentHash is SHA-256 of the canonical uncompressed table payload.
func (db *DB) ContentHash() [32]byte {
	if db == nil {
		return [32]byte{}
	}
	return sha256.Sum256(db.raw)
}

// IPv4RangeCount returns the number of mapped IPv4 intervals, excluding ASN 0 gap entries.
func (db *DB) IPv4RangeCount() int {
	if db == nil {
		return 0
	}
	count := 0
	for i := uint32(0); i < db.n4; i++ {
		off := db.v4Offset + int(i)*v4EntrySize + 4
		if binary.BigEndian.Uint32(db.raw[off:off+4]) != 0 {
			count++
		}
	}
	return count
}

// IPv6RangeCount returns the number of mapped IPv6 /64 intervals, excluding ASN 0 gap entries.
func (db *DB) IPv6RangeCount() int {
	if db == nil {
		return 0
	}
	count := 0
	for i := uint32(0); i < db.n6; i++ {
		off := db.v6Offset + int(i)*v6EntrySize + 8
		if binary.BigEndian.Uint32(db.raw[off:off+4]) != 0 {
			count++
		}
	}
	return count
}

// ASNCount returns the number of operator records.
func (db *DB) ASNCount() int {
	if db == nil {
		return 0
	}
	return int(db.na)
}

func buildIPv4Index(ranges []IPv4Range) ([]index4, error) {
	if len(ranges) > maxTableRows {
		return nil, fmt.Errorf("%d ranges exceed the limit %d", len(ranges), maxTableRows)
	}
	entries := make([]index4, 0, 2*len(ranges)+1)
	cursor := uint64(0)
	done := false
	for i, r := range ranges {
		if r.Start > r.End {
			return nil, fmt.Errorf("range %d has start after end", i+1)
		}
		if i > 0 {
			prev := ranges[i-1]
			if r.Start < prev.Start {
				return nil, fmt.Errorf("range %d is unsorted", i+1)
			}
			if r.Start <= prev.End {
				return nil, fmt.Errorf("range %d overlaps range %d", i+1, i)
			}
		}
		if uint64(r.Start) > cursor {
			append4(&entries, uint32(cursor), 0)
		}
		append4(&entries, r.Start, r.ASN)
		if r.End == ^uint32(0) {
			if i != len(ranges)-1 {
				return nil, fmt.Errorf("range %d reaches the end of IPv4 space before later ranges", i+1)
			}
			done = true
			break
		}
		cursor = uint64(r.End) + 1
	}
	if !done {
		append4(&entries, uint32(cursor), 0)
	}
	return entries, nil
}

func append4(entries *[]index4, start, asn uint32) {
	if len(*entries) > 0 && (*entries)[len(*entries)-1].asn == asn {
		return
	}
	*entries = append(*entries, index4{start: start, asn: asn})
}

func buildIPv6Index(ranges []IPv6Range) ([]index6, error) {
	if len(ranges) > maxTableRows {
		return nil, fmt.Errorf("%d ranges exceed the limit %d", len(ranges), maxTableRows)
	}
	entries := make([]index6, 0, 2*len(ranges)+1)
	cursor := uint64(0)
	done := false
	for i, r := range ranges {
		if r.Start > r.End {
			return nil, fmt.Errorf("range %d has start after end", i+1)
		}
		if i > 0 {
			prev := ranges[i-1]
			if r.Start < prev.Start {
				return nil, fmt.Errorf("range %d is unsorted", i+1)
			}
			if r.Start <= prev.End {
				return nil, fmt.Errorf("range %d overlaps range %d", i+1, i)
			}
		}
		if r.Start > cursor {
			append6(&entries, cursor, 0)
		}
		append6(&entries, r.Start, r.ASN)
		if r.End == ^uint64(0) {
			if i != len(ranges)-1 {
				return nil, fmt.Errorf("range %d reaches the end of IPv6 space before later ranges", i+1)
			}
			done = true
			break
		}
		cursor = r.End + 1
	}
	if !done {
		append6(&entries, cursor, 0)
	}
	return entries, nil
}

func append6(entries *[]index6, start uint64, asn uint32) {
	if len(*entries) > 0 && (*entries)[len(*entries)-1].asn == asn {
		return
	}
	*entries = append(*entries, index6{start: start, asn: asn})
}

func encodeOperators(input []Operator, ipv4 []IPv4Range, ipv6 []IPv6Range) ([]asnRecord, []byte, error) {
	if len(input) > maxTableRows {
		return nil, nil, fmt.Errorf("asndb: %d operator records exceed the limit %d", len(input), maxTableRows)
	}
	used := make(map[uint32]struct{}, len(input))
	for _, r := range ipv4 {
		if r.ASN != 0 {
			used[r.ASN] = struct{}{}
		}
	}
	for _, r := range ipv6 {
		if r.ASN != 0 {
			used[r.ASN] = struct{}{}
		}
	}
	operators := append([]Operator(nil), input...)
	sort.Slice(operators, func(i, j int) bool { return operators[i].ASN < operators[j].ASN })
	records := make([]asnRecord, 0, len(operators))
	nameBlob := make([]byte, 0, len(operators)*16)
	for i, operator := range operators {
		if operator.ASN == 0 || (i > 0 && operator.ASN == operators[i-1].ASN) {
			return nil, nil, fmt.Errorf("asndb: duplicate or reserved ASN %d", operator.ASN)
		}
		if _, ok := used[operator.ASN]; !ok {
			return nil, nil, fmt.Errorf("asndb: ASN %d has a name but no routed range", operator.ASN)
		}
		if !utf8.ValidString(operator.Name) || operator.Name == "" || len(operator.Name) > maxOperatorBytes || hasControl([]byte(operator.Name)) || containsAddressLike(operator.Name) {
			return nil, nil, fmt.Errorf("asndb: ASN %d has an invalid or unclean operator name", operator.ASN)
		}
		var country [2]byte
		if operator.Country != "" {
			if len(operator.Country) != 2 || operator.Country[0] < 'A' || operator.Country[0] > 'Z' || operator.Country[1] < 'A' || operator.Country[1] > 'Z' {
				return nil, nil, fmt.Errorf("asndb: ASN %d has invalid country code %q", operator.ASN, operator.Country)
			}
			copy(country[:], operator.Country)
		}
		if len(nameBlob) > int(^uint32(0)) {
			return nil, nil, errors.New("asndb: operator-name table exceeds 4 GiB")
		}
		records = append(records, asnRecord{asn: operator.ASN, country: country, nameOff: uint32(len(nameBlob)), nameLen: uint8(len(operator.Name))})
		nameBlob = append(nameBlob, operator.Name...)
	}
	for asn := range used {
		found := sort.Search(len(records), func(i int) bool { return records[i].asn >= asn })
		if found == len(records) || records[found].asn != asn {
			return nil, nil, fmt.Errorf("asndb: ASN %d has no operator name", asn)
		}
	}
	return records, nameBlob, nil
}

func hasControl(b []byte) bool {
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
