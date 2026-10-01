package qr

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Data codewords to error correction codewords of version 1-M: "HELLO WORLD" is the worked example of the
// Thonky QR tutorial, "01234567" the one of ISO/IEC 18004 Annex I.
func TestReedSolomonVectors(t *testing.T) {
	for _, c := range []struct {
		name      string
		data, ecc []byte
	}{
		{"HELLO WORLD",
			[]byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17},
			[]byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}},
		{"01234567",
			[]byte{16, 32, 12, 86, 97, 128, 236, 17, 236, 17, 236, 17, 236, 17, 236, 17},
			[]byte{165, 36, 212, 193, 237, 54, 199, 135, 44, 85}},
	} {
		if got := rsRemainder(c.data, rsGenerator(10)); !bytes.Equal(got, c.ecc) {
			t.Errorf("%s: ecc %v, want %v", c.name, got, c.ecc)
		}
	}
	// The generator of degree 7 is a well-known row: x^7 + 127x^6 + 122x^5 + 154x^4 + 164x^3 + 11x^2 + 68x + 117.
	if got := rsGenerator(7); !bytes.Equal(got, []byte{127, 122, 154, 164, 11, 68, 117}) {
		t.Errorf("generator(7) = %v", got)
	}
}

func TestFormatInfo(t *testing.T) {
	for _, c := range []struct {
		level Level
		mask  int
		bits  string
	}{ // ISO/IEC 18004 Table C.1
		{L, 0, "111011111000100"}, {L, 1, "111001011110011"},
		{M, 0, "101010000010010"}, {M, 1, "101000100100101"},
		{Q, 0, "011010101011111"}, {H, 0, "001011010001001"},
	} {
		if got := fmt.Sprintf("%015b", formatInfo(c.level, c.mask)); got != c.bits {
			t.Errorf("format %d/%d = %s, want %s", c.level, c.mask, got, c.bits)
		}
	}
}

func TestVersionInfo(t *testing.T) {
	for ver, bits := range map[int]string{ // ISO/IEC 18004 Table D.1
		7:  "000111110010010100",
		8:  "001000010110111100",
		40: "101000110001101001",
	} {
		if got := fmt.Sprintf("%018b", versionInfo(ver)); got != bits {
			t.Errorf("version %d = %s, want %s", ver, got, bits)
		}
	}
}

// Byte-mode capacities of ISO/IEC 18004 Table 7 (the corners), and a sanity check of every table entry.
func TestCapacity(t *testing.T) {
	for _, c := range []struct {
		ver   int
		level Level
		bytes int
	}{
		{1, L, 17}, {1, M, 14}, {1, Q, 11}, {1, H, 7},
		{6, Q, 74}, {7, H, 64}, {10, M, 213}, {10, H, 119},
		{40, L, 2953}, {40, M, 2331}, {40, Q, 1663}, {40, H, 1273},
	} {
		if got := capacity(c.ver, c.level); got != c.bytes {
			t.Errorf("capacity(%d, %d) = %d, want %d", c.ver, c.level, got, c.bytes)
		}
	}
	for level := L; level <= H; level++ {
		for ver := 1; ver <= 40; ver++ {
			if eccPerBlock[level][ver] == 0 || numBlocks[level][ver] == 0 || dataCodewords(ver, level) <= 0 {
				t.Errorf("version %d level %d: empty table entry", ver, level)
			}
			if ver > 1 && capacity(ver, level) <= capacity(ver-1, level) {
				t.Errorf("level %d: version %d holds no more than %d", level, ver, ver-1)
			}
		}
	}
}

func TestGolden(t *testing.T) {
	for _, g := range golden {
		rows := strings.Fields(g.rows)
		ver := (len(rows) - 17) / 4
		for _, c := range []struct {
			how  string
			code *Code
		}{
			{"build", build([]byte(g.data), ver, g.level, g.mask)},
			{"Encode", mustEncode(t, g.data, g.level)}, // the mask choice and the smallest version too
		} {
			if c.code.Size != len(rows) {
				t.Errorf("%s %s: size %d, want %d", g.name, c.how, c.code.Size, len(rows))
				continue
			}
			for y, row := range rows {
				for x, ch := range row {
					if c.code.Black(x, y) != (ch == '#') {
						t.Errorf("%s %s: module (%d,%d) differs", g.name, c.how, x, y)
						return
					}
				}
			}
		}
	}
}

func mustEncode(t *testing.T, s string, level Level) *Code {
	t.Helper()
	c, err := Encode([]byte(s), level)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEncodeLimits(t *testing.T) {
	if c, err := Encode(make([]byte, 2953), L); err != nil || c.Size != 177 {
		t.Errorf("2953 bytes at L: %v", err)
	}
	if _, err := Encode(make([]byte, 2954), L); !errors.Is(err, ErrTooLong) {
		t.Errorf("2954 bytes at L: %v", err)
	}
	if _, err := Encode(nil, Level(7)); err == nil {
		t.Error("an unknown level was accepted")
	}
	if c := mustEncode(t, "", M); c.Size != 21 {
		t.Errorf("empty data: size %d", c.Size)
	}
	if c := mustEncode(t, strings.Repeat("a", 14), M); c.Size != 21 {
		t.Errorf("14 bytes at M should fit version 1, got size %d", c.Size)
	}
	if c := mustEncode(t, strings.Repeat("a", 15), M); c.Size != 25 {
		t.Errorf("15 bytes at M should need version 2, got size %d", c.Size)
	}
	if mustEncode(t, "x", M).Black(-1, 0) || mustEncode(t, "x", M).Black(21, 0) {
		t.Error("Black is true outside the symbol")
	}
}

func TestTerminal(t *testing.T) {
	c := mustEncode(t, "hello, qr1", M) // 21 modules
	for _, invert := range []bool{false, true} {
		lines := strings.Split(strings.TrimSuffix(c.Terminal(invert), "\n"), "\n")
		if want := (21 + 8 + 1) / 2; len(lines) != want {
			t.Fatalf("invert=%v: %d rows, want %d", invert, len(lines), want)
		}
		for i, l := range lines {
			if n := len([]rune(l)); n != 29 {
				t.Errorf("invert=%v: row %d is %d columns, want 29", invert, i, n)
			}
		}
		// Quiet zone: light modules, which are the blocks by default and blanks when inverted.
		quiet, corner := "█", " " // the symbol's corner module is dark
		if invert {
			quiet, corner = " ", "█"
		}
		if lines[0] != strings.Repeat(quiet, 29) || lines[1] != strings.Repeat(quiet, 29) {
			t.Errorf("invert=%v: the quiet zone rows are %q", invert, lines[0])
		}
		if got := string([]rune(lines[2])[4]); got != corner { // module (0,0) and (0,1)
			t.Errorf("invert=%v: corner %q, want %q", invert, got, corner)
		}
	}
}
