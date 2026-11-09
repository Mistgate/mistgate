package asndb

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxTSVBytes     = 128 << 20
	maxTSVLineBytes = 64 << 10
)

// Family selects the address family in an ip2asn TSV file.
type Family uint8

const (
	FamilyIPv4 Family = iota + 1
	FamilyIPv6
)

var (
	ipv4Like = regexp.MustCompile(`(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?:/[0-9]{1,2})?`)
	ipv6Like = regexp.MustCompile(`(?i)(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}(?:/[0-9]{1,3})?`)
)

// AppendTSV reads the five-column ip2asn TSV format (start, end, ASN, country, description),
// transparently accepting either plain text or gzip. Each source must be sorted and non-overlapping.
func AppendTSV(r io.Reader, family Family, table *Table) error {
	if r == nil || table == nil {
		return errors.New("asndb: TSV reader and table are required")
	}
	if family != FamilyIPv4 && family != FamilyIPv6 {
		return fmt.Errorf("asndb: unknown TSV address family %d", family)
	}
	buffered := bufio.NewReader(r)
	peek, err := buffered.Peek(2)
	if err != nil && err != io.EOF {
		return fmt.Errorf("asndb: read TSV header: %w", err)
	}
	if len(peek) == 2 && peek[0] == 0x1f && peek[1] == 0x8b {
		gz, err := gzip.NewReader(buffered)
		if err != nil {
			return fmt.Errorf("asndb: open compressed TSV: %w", err)
		}
		defer gz.Close()
		buffered = bufio.NewReader(gz)
	}
	limited := &io.LimitedReader{R: buffered, N: maxTSVBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxTSVLineBytes)
	lineNo := 0
	rows := 0
	var prev4 *IPv4Range
	var prev6 *IPv6Range
	if family == FamilyIPv4 && len(table.IPv4) != 0 {
		prev := table.IPv4[len(table.IPv4)-1]
		prev4 = &prev
	}
	if family == FamilyIPv6 && len(table.IPv6) != 0 {
		prev := table.IPv6[len(table.IPv6)-1]
		prev6 = &prev
		if !table.sourceV6Start.IsValid() {
			table.sourceV6Start = netip.AddrFrom16(high64Start(prev.Start))
		}
		if !table.sourceV6End.IsValid() {
			table.sourceV6End = netip.AddrFrom16(high64End(prev.End))
		}
	}
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows++
		if rows > maxTableRows {
			return fmt.Errorf("asndb: TSV exceeds %d rows", maxTableRows)
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return fmt.Errorf("asndb: TSV line %d has %d columns (want 5)", lineNo, len(fields))
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		asn64, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return fmt.Errorf("asndb: TSV line %d has invalid ASN %q: %w", lineNo, fields[2], err)
		}
		asn := uint32(asn64)
		country, err := parseCountry(fields[3])
		if err != nil {
			return fmt.Errorf("asndb: TSV line %d: %w", lineNo, err)
		}
		name := cleanOperatorName(fields[4], asn)
		switch family {
		case FamilyIPv4:
			start, err := parseIPv4(fields[0])
			if err != nil {
				return fmt.Errorf("asndb: TSV line %d has invalid IPv4 start %q: %w", lineNo, fields[0], err)
			}
			end, err := parseIPv4(fields[1])
			if err != nil {
				return fmt.Errorf("asndb: TSV line %d has invalid IPv4 end %q: %w", lineNo, fields[1], err)
			}
			if start > end {
				return fmt.Errorf("asndb: TSV line %d has IPv4 start after end", lineNo)
			}
			if prev4 != nil {
				if start < prev4.Start {
					return fmt.Errorf("asndb: TSV line %d is unsorted", lineNo)
				}
				if start <= prev4.End {
					return fmt.Errorf("asndb: TSV line %d overlaps the previous IPv4 range", lineNo)
				}
			}
			range4 := IPv4Range{Start: start, End: end, ASN: asn}
			table.IPv4 = append(table.IPv4, range4)
			prev4 = &range4
		case FamilyIPv6:
			startAddr, err := netip.ParseAddr(fields[0])
			if err != nil || !startAddr.Is6() || startAddr.Is4In6() {
				return fmt.Errorf("asndb: TSV line %d has invalid IPv6 start %q", lineNo, fields[0])
			}
			endAddr, err := netip.ParseAddr(fields[1])
			if err != nil || !endAddr.Is6() || endAddr.Is4In6() {
				return fmt.Errorf("asndb: TSV line %d has invalid IPv6 end %q", lineNo, fields[1])
			}
			if startAddr.Compare(endAddr) > 0 {
				return fmt.Errorf("asndb: TSV line %d has IPv6 start after end", lineNo)
			}
			startBytes, endBytes := startAddr.As16(), endAddr.As16()
			start := binary.BigEndian.Uint64(startBytes[:8])
			end := binary.BigEndian.Uint64(endBytes[:8])
			if prev6 != nil {
				if startAddr.Compare(table.sourceV6Start) < 0 {
					return fmt.Errorf("asndb: TSV line %d is unsorted", lineNo)
				}
				if startAddr.Compare(table.sourceV6End) <= 0 {
					return fmt.Errorf("asndb: TSV line %d overlaps the previous IPv6 range", lineNo)
				}
				if start < prev6.Start {
					return fmt.Errorf("asndb: TSV line %d is unsorted", lineNo)
				}
				if start <= prev6.End {
					if asn != prev6.ASN {
						return fmt.Errorf("asndb: TSV line %d assigns a different ASN within the same IPv6 /64", lineNo)
					}
					if end > prev6.End {
						table.IPv6[len(table.IPv6)-1].End = end
						prev6.End = end
					}
				} else {
					range6 := IPv6Range{Start: start, End: end, ASN: asn}
					table.IPv6 = append(table.IPv6, range6)
					prev6 = &table.IPv6[len(table.IPv6)-1]
				}
			} else {
				range6 := IPv6Range{Start: start, End: end, ASN: asn}
				table.IPv6 = append(table.IPv6, range6)
				prev6 = &table.IPv6[len(table.IPv6)-1]
				table.sourceV6Start = startAddr
			}
			table.sourceV6Start = startAddr
			table.sourceV6End = endAddr
		}
		if asn != 0 {
			if err := table.addOperator(Operator{ASN: asn, Country: country, Name: name}); err != nil {
				return fmt.Errorf("asndb: TSV line %d: %w", lineNo, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return fmt.Errorf("asndb: TSV line exceeds %d bytes", maxTSVLineBytes)
		}
		return fmt.Errorf("asndb: read TSV: %w", err)
	}
	if limited.N == 0 {
		return fmt.Errorf("asndb: TSV exceeds %d bytes", maxTSVBytes)
	}
	if family == FamilyIPv4 && rows == 0 {
		return errors.New("asndb: IPv4 TSV is empty")
	}
	if family == FamilyIPv6 && rows == 0 {
		return errors.New("asndb: IPv6 TSV is empty")
	}
	return nil
}

func (table *Table) addOperator(operator Operator) error {
	if table.operators == nil {
		table.operators = make(map[uint32]Operator, len(table.ASNs))
		for _, old := range table.ASNs {
			if _, exists := table.operators[old.ASN]; exists {
				return fmt.Errorf("duplicate operator metadata for ASN %d", old.ASN)
			}
			table.operators[old.ASN] = old
		}
	}
	if old, ok := table.operators[operator.ASN]; ok {
		if old.Country != operator.Country || old.Name != operator.Name {
			return fmt.Errorf("conflicting country or operator name for ASN %d", operator.ASN)
		}
		return nil
	}
	table.operators[operator.ASN] = operator
	table.ASNs = append(table.ASNs, operator)
	return nil
}

func parseIPv4(value string) (uint32, error) {
	if addr, err := netip.ParseAddr(value); err == nil {
		if !addr.Is4() {
			return 0, errors.New("address is not IPv4")
		}
		b := addr.As4()
		return binary.BigEndian.Uint32(b[:]), nil
	}
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

func parseCountry(value string) (string, error) {
	value = strings.ToUpper(value)
	if value == "" || value == "-" || value == "--" || value == "??" {
		return "", nil
	}
	if len(value) != 2 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
		return "", fmt.Errorf("invalid country code %q", value)
	}
	return value, nil
}

func cleanOperatorName(value string, asn uint32) string {
	value = strings.ToValidUTF8(value, "")
	value = ipv4Like.ReplaceAllString(value, " ")
	value = ipv6Like.ReplaceAllString(value, " ")
	var clean strings.Builder
	clean.Grow(len(value))
	for _, r := range value {
		if unicode.IsControl(r) {
			clean.WriteByte(' ')
		} else {
			clean.WriteRune(r)
		}
	}
	value = strings.Join(strings.Fields(clean.String()), " ")
	if value == "" {
		value = fmt.Sprintf("AS%d", asn)
	}
	var out strings.Builder
	for _, r := range value {
		if out.Len()+utf8.RuneLen(r) > maxOperatorBytes {
			break
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

func containsAddressLike(value string) bool {
	return ipv4Like.MatchString(value) || ipv6Like.MatchString(value)
}

func high64End(high uint64) [16]byte {
	var addr [16]byte
	binary.BigEndian.PutUint64(addr[:8], high)
	for i := 8; i < len(addr); i++ {
		addr[i] = 0xff
	}
	return addr
}

func high64Start(high uint64) [16]byte {
	var addr [16]byte
	binary.BigEndian.PutUint64(addr[:8], high)
	return addr
}
