// Package qr is a small QR Code encoder (ISO/IEC 18004, Model 2): byte mode, versions 1-40, error correction
// levels L/M/Q/H. It exists so the CLI can print a QR code to a terminal without a dependency.
package qr

import "errors"

// Level is the error correction level.
type Level int

// The order matches the rows of the tables.
const (
	L Level = iota // recovers about 7% of the codewords
	M              // about 15%
	Q              // about 25%
	H              // about 30%
)

// ErrTooLong means the data does not fit in a version 40 symbol at the chosen level.
var ErrTooLong = errors.New("qr: data too long for a QR code")

// Code is an encoded symbol, without the quiet zone.
type Code struct {
	Size int // modules per side, 4*version+17
	mods []bool
}

// Black reports whether the module at column x, row y is dark. Outside the symbol it is false.
func (c *Code) Black(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.mods[y*c.Size+x]
}

// Encode encodes data in byte mode in the smallest version that fits at level.
func Encode(data []byte, level Level) (*Code, error) {
	if level < L || level > H {
		return nil, errors.New("qr: unknown error correction level")
	}
	for ver := 1; ver <= 40; ver++ {
		if len(data) <= capacity(ver, level) {
			return build(data, ver, level, -1), nil
		}
	}
	return nil, ErrTooLong
}

// rawModules is the number of modules that carry codewords, remainder bits included (ISO/IEC 18004 Table 1).
func rawModules(ver int) int {
	n := (16*ver+128)*ver + 64
	if ver >= 2 {
		na := ver/7 + 2
		n -= (25*na-10)*na - 55
		if ver >= 7 {
			n -= 36 // the two version information blocks
		}
	}
	return n
}

func dataCodewords(ver int, level Level) int {
	return rawModules(ver)/8 - eccPerBlock[level][ver]*numBlocks[level][ver]
}

// capacity is the most bytes a byte-mode symbol of ver holds: 4 bits of mode, 8 or 16 of count, then the data.
func capacity(ver int, level Level) int {
	return (dataCodewords(ver, level)*8 - 4 - countBits(ver)) / 8
}

func countBits(ver int) int {
	if ver >= 10 {
		return 16
	}
	return 8
}

// build draws version ver. mask < 0 picks the mask with the lowest penalty.
func build(data []byte, ver int, level Level, mask int) *Code {
	m := newMatrix(ver, level)
	m.placeData(interleave(encodeData(data, ver, level), ver, level))
	if mask < 0 {
		best := -1
		for k := 0; k < 8; k++ {
			m.applyMask(k)
			m.drawFormat(level, k)
			if p := m.penalty(); best < 0 || p < best {
				best, mask = p, k
			}
			m.applyMask(k) // undo
		}
	}
	m.applyMask(mask)
	m.drawFormat(level, mask)
	return &Code{Size: m.size, mods: m.dark}
}

// encodeData returns the data codewords: mode, count, bytes, terminator, then the pad codewords 0xEC 0x11.
func encodeData(data []byte, ver int, level Level) []byte {
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>i&1 == 1)
		}
	}
	put(0b0100, 4) // byte mode
	put(len(data), countBits(ver))
	for _, b := range data {
		put(int(b), 8)
	}
	out := make([]byte, dataCodewords(ver, level))
	put(0, min(4, len(out)*8-len(bits))) // terminator
	for i, b := range bits {
		if b {
			out[i/8] |= 0x80 >> (i % 8)
		}
	}
	for i, pad := (len(bits)+7)/8, byte(0xEC); i < len(out); i++ {
		out[i], pad = pad, pad^0xEC^0x11
	}
	return out
}

// interleave splits the data into blocks, adds Reed-Solomon codewords to each and interleaves them (ISO/IEC 18004
// "Constructing the final message codeword sequence"). The first blocks are short, the last ones one data byte longer.
func interleave(data []byte, ver int, level Level) []byte {
	nb, ecc := numBlocks[level][ver], eccPerBlock[level][ver]
	total := rawModules(ver) / 8
	shortBlocks, shortLen := nb-total%nb, total/nb // codewords per short block, data and ecc
	gen := rsGenerator(ecc)
	var blocks, checks [][]byte
	for i, k := 0, 0; i < nb; i++ {
		n := shortLen - ecc
		if i >= shortBlocks {
			n++
		}
		blocks = append(blocks, data[k:k+n])
		checks = append(checks, rsRemainder(data[k:k+n], gen))
		k += n
	}
	var out []byte
	for i := 0; i <= shortLen-ecc; i++ {
		for _, b := range blocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := 0; i < ecc; i++ {
		for _, c := range checks {
			out = append(out, c[i])
		}
	}
	return out
}

// Reed-Solomon over GF(256) with the polynomial x^8+x^4+x^3+x^2+1 (ISO/IEC 18004 Annex A).

func gfMul(x, y byte) byte {
	z := 0
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x11D
		z ^= int(y>>i&1) * int(x)
	}
	return byte(z)
}

// rsGenerator is (x-1)(x-2)(x-4)... of the given degree, highest power first, without the leading 1.
func rsGenerator(degree int) []byte {
	g := make([]byte, degree)
	g[degree-1] = 1
	root := byte(1)
	for range degree {
		for j := range g {
			g[j] = gfMul(g[j], root)
			if j+1 < len(g) {
				g[j] ^= g[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return g
}

// rsRemainder is data(x)*x^degree mod generator: the error correction codewords.
func rsRemainder(data, gen []byte) []byte {
	r := make([]byte, len(gen))
	for _, b := range data {
		f := b ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i := range r {
			r[i] ^= gfMul(gen[i], f)
		}
	}
	return r
}

// matrix is the module grid under construction. fn marks the function patterns, which data and masks leave alone.
type matrix struct {
	size, ver int
	dark, fn  []bool
}

func (m *matrix) setFn(x, y int, dark bool) {
	m.dark[y*m.size+x] = dark
	m.fn[y*m.size+x] = true
}

func newMatrix(ver int, level Level) *matrix {
	size := 4*ver + 17
	m := &matrix{size: size, ver: ver, dark: make([]bool, size*size), fn: make([]bool, size*size)}
	for i := 0; i < size; i++ { // timing patterns
		m.setFn(6, i, i%2 == 0)
		m.setFn(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} { // finder patterns with their separators
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y, d := c[0]+dx, c[1]+dy, max(dx, -dx, dy, -dy)
				if x >= 0 && y >= 0 && x < size && y < size {
					m.setFn(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	pos := alignmentPositions(ver)
	for i, cx := range pos {
		for j, cy := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0) {
				continue // under a finder pattern
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					m.setFn(cx+dx, cy+dy, max(dx, -dx, dy, -dy) != 1)
				}
			}
		}
	}
	m.drawFormat(level, 0) // reserves the format modules; the real bits come once the mask is known
	m.drawVersion()
	return m
}

// alignmentPositions are the row/column coordinates of the alignment pattern centres (ISO/IEC 18004 Annex E).
func alignmentPositions(ver int) []int {
	if ver == 1 {
		return nil
	}
	n := ver/7 + 2
	step := 26
	if ver != 32 {
		step = (ver*4 + n*2 + 1) / (n*2 - 2) * 2
	}
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, ver*4+10; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

// formatInfo is the 15-bit format information: level and mask, BCH(15,5), XOR 0x5412 (ISO/IEC 18004 "Format information").
func formatInfo(level Level, mask int) int {
	data := formatBits[level]<<3 | mask
	r := data
	for range 10 {
		r = r<<1 ^ (r>>9)*0x537
	}
	return (data<<10 | r) ^ 0x5412
}

// versionInfo is the 18-bit version information of versions 7 and up: BCH(18,6) (ISO/IEC 18004 "Version information").
func versionInfo(ver int) int {
	r := ver
	for range 12 {
		r = r<<1 ^ (r>>11)*0x1F25
	}
	return ver<<12 | r
}

func (m *matrix) drawFormat(level Level, mask int) {
	bits, n := formatInfo(level, mask), m.size
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := 0; i <= 5; i++ {
		m.setFn(8, i, bit(i))
	}
	m.setFn(8, 7, bit(6))
	m.setFn(8, 8, bit(7))
	m.setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		m.setFn(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		m.setFn(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.setFn(8, n-15+i, bit(i))
	}
	m.setFn(8, n-8, true) // the always-dark module
}

func (m *matrix) drawVersion() {
	if m.ver < 7 {
		return
	}
	bits := versionInfo(m.ver)
	for i := 0; i < 18; i++ {
		a, b := m.size-11+i%3, i/3
		m.setFn(a, b, bits>>i&1 == 1)
		m.setFn(b, a, bits>>i&1 == 1)
	}
}

// placeData writes the codeword bits in the zigzag of two-column strips from the bottom right, skipping function
// modules. Remainder bits stay light.
func (m *matrix) placeData(cw []byte) {
	i := 0
	for right := m.size - 1; right >= 1; right -= 2 {
		if right == 6 { // the vertical timing pattern takes a column
			right = 5
		}
		for v := 0; v < m.size; v++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := v
				if (right+1)&2 == 0 { // this strip runs upward
					y = m.size - 1 - v
				}
				if !m.fn[y*m.size+x] && i < len(cw)*8 {
					m.dark[y*m.size+x] = cw[i/8]>>(7-i%8)&1 == 1
					i++
				}
			}
		}
	}
}

// maskBit is the mask pattern condition (ISO/IEC 18004 Table 10) for column x, row y.
func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	}
	return ((x+y)%2+x*y%3)%2 == 0
}

// applyMask flips the data modules where the mask holds; applying it twice undoes it.
func (m *matrix) applyMask(mask int) {
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			if !m.fn[y*m.size+x] && maskBit(mask, x, y) {
				m.dark[y*m.size+x] = !m.dark[y*m.size+x]
			}
		}
	}
}

// penalty scores the masked symbol with the four rules of ISO/IEC 18004 "Evaluation of data masking results".
func (m *matrix) penalty() int {
	n, p, darkCount := m.size, 0, 0
	at := func(x, y int) bool { return m.dark[y*n+x] }
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if at(x, y) {
				darkCount++
			}
			// Rule 2: 2x2 blocks of one colour.
			if x+1 < n && y+1 < n && at(x, y) == at(x+1, y) && at(x, y) == at(x, y+1) && at(x, y) == at(x+1, y+1) {
				p += 3
			}
		}
	}
	for vertical := 0; vertical < 2; vertical++ {
		for a := 0; a < n; a++ {
			get := func(b int) bool {
				if vertical == 1 {
					return at(a, b)
				}
				return at(b, a)
			}
			// Rule 1: runs of five or more.
			for b, run := 0, 1; b < n; b++ {
				if b+1 < n && get(b+1) == get(b) {
					run++
					continue
				}
				if run >= 5 {
					p += 3 + run - 5
				}
				run = 1
			}
			// Rule 3: dark-light-dark-dark-dark-light-dark with four light modules on either side. The line
			// ends count as light, as the quiet zone is.
			w := 0
			for b := -4; b < n+4; b++ {
				w = (w<<1 | b2i(b >= 0 && b < n && get(b))) & 0x7FF
				if b >= 6 && (w == 0b10111010000 || w == 0b00001011101) {
					p += 40
				}
			}
		}
	}
	// Rule 4: distance of the dark share from 50%, in steps of 5%.
	total := n * n
	k := (abs(darkCount*20-total*10)+total-1)/total - 1
	return p + k*10
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
