package qr

import (
	"errors"
	"math"
	"slices"
	"strconv"
)

// Color is an sRGB colour. The SVG only ever contains it as #rrggbb, never as text from the caller.
type Color struct{ R, G, B uint8 }

// SVGOptions is how SVG draws a code. The zero value is square black modules, no quiet zone, no background.
type SVGOptions struct {
	// Rounded draws every connected figure of dark modules as one shape along its outline, with a corner that sticks out
	// rounded by half a module: the shape of the board's page (roundedPath in web/src/lib/qr-path.ts). Without it the modules
	// are squares, a run in a row one rectangle.
	Rounded bool
	// Fillet, with Rounded, fills a corner that goes in (a light module with a dark one on each of two sides) with a quarter
	// of a circle of 0.3 of a module, which makes the figures look cast. Modules that touch only at a corner stay two figures,
	// and the light module on each side of that point gets a fillet of its own.
	Fillet bool
	// Eyes draws the three finder patterns as rounded squares (a 7x7 ring of radius 2, its 5x5 cut-out of radius 1.2 and the
	// 3x3 centre of radius 0.9) after the data, and leaves the modules in those squares out of the data, as the board's path
	// does (eyes in web/src/lib/qr-path.ts).
	Eyes bool
	// Window is the side, in modules, of a clear square in the middle for a logo the caller lays over it (the library
	// does not draw the logo). Every module that touches the square widened by half a module on each side is left out.
	// 0 means no window. A window needs level Q or H, must leave the finder, timing, format and version patterns and the
	// bottom-right alignment pattern alone, and must not use up more error correction than windowShare allows; see
	// FitWindow for the largest window that does.
	Window float64
	// Quiet is the quiet zone around the code, in modules, 0 to 32 (ISO/IEC 18004 asks for 4; a page that draws the code on
	// a white card of its own uses 0).
	Quiet int
	// Dark is the colour of the modules; nil leaves the fill out, so the page's CSS (fill: currentColor) or black decides.
	Dark *Color
	// Light draws a background of this colour over the symbol and the quiet zone; nil draws none.
	Light *Color
}

var (
	// ErrWindowLevel means a window was asked for at a level below Q: a logo takes error correction that L and M cannot spare.
	ErrWindowLevel = errors.New("qr: a window for a logo needs error correction level Q or H")
	// ErrWindowPatterns means the window would cover a pattern the readers find the code by.
	ErrWindowPatterns = errors.New("qr: the window would cover a finder, timing, format, version or the last alignment pattern")
	// ErrWindowBudget means the window would use up too much of the error correction of this symbol.
	ErrWindowBudget = errors.New("qr: the window leaves too little error correction for reading")
	// ErrOptions means an option is out of range (a negative or not-a-number window, a quiet zone over 32).
	ErrOptions = errors.New("qr: invalid SVG options")
)

// windowShare is the part of the error correction the window may use: in every Reed-Solomon block at most this share of
// the codewords the block can correct (half its error correction codewords) may be hit by a dark module the window
// takes away. The rest is left for what a camera does to a real code: blur, glare, a crease, a print that bled.
const windowShare = 0.5

// LogoWindow is the size, in modules, the board's page gives the logo window of a code `size` modules wide: 26.9 % of a
// 29-module code, 22.3 % of a 53-module one.
func LogoWindow(size int) float64 {
	return float64(0.18*float64(size)) + 2.6 // the conversion keeps the compiler from fusing the multiply and the add
}

// FitWindow is the largest window not over want (in steps of half a module) that SVG accepts for this code, or 0 when even
// the smallest does not fit (a level below Q, a version 1 symbol, ...). A caller lays its logo at the size this returns.
func (c *Code) FitWindow(want float64) float64 {
	if !(want > 0) || c.level < Q {
		return 0
	}
	w := c.windowCheck()
	for x := math.Min(want, float64(c.Size)); x > 0; x -= 0.5 {
		if w.fits(x) == nil {
			return x
		}
	}
	return 0
}

// SVG draws the code as an SVG image: one path, in module units, so the viewBox is the size of the symbol and the
// quiet zone and the image scales to its box. Nothing of the encoded data is in it, only coordinates; the colours are
// written from three bytes each, so no text of the caller's reaches the markup.
func (c *Code) SVG(o SVGOptions) ([]byte, error) {
	n := c.Size
	if o.Quiet < 0 || o.Quiet > 32 || math.IsNaN(o.Window) || math.IsInf(o.Window, 0) || o.Window < 0 || o.Window > float64(n) {
		return nil, ErrOptions
	}
	on := slices.Clone(c.mods)
	if o.Window > 0 {
		if c.level < Q {
			return nil, ErrWindowLevel
		}
		w := c.windowCheck()
		if err := w.fits(o.Window); err != nil {
			return nil, err
		}
		w.clear(o.Window, func(x, y int) { on[y*n+x] = false })
	}
	if o.Eyes {
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				if inEye(n, x, y) {
					on[y*n+x] = false
				}
			}
		}
	}
	total := n + 2*o.Quiet
	perModule := 8 // a square run is about 8 bytes per module of the grid, a rounded one about 20
	if o.Rounded {
		perModule = 20
	}
	out := make([]byte, 0, 256+n*n*perModule)
	out = append(out, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="`...)
	out = strconv.AppendInt(out, int64(-o.Quiet), 10)
	out = append(out, ' ')
	out = strconv.AppendInt(out, int64(-o.Quiet), 10)
	out = append(out, ' ')
	out = strconv.AppendInt(out, int64(total), 10)
	out = append(out, ' ')
	out = strconv.AppendInt(out, int64(total), 10)
	out = append(out, `">`...)
	if o.Light != nil {
		out = append(out, `<rect x="`...)
		out = strconv.AppendInt(out, int64(-o.Quiet), 10)
		out = append(out, `" y="`...)
		out = strconv.AppendInt(out, int64(-o.Quiet), 10)
		out = append(out, `" width="`...)
		out = strconv.AppendInt(out, int64(total), 10)
		out = append(out, `" height="`...)
		out = strconv.AppendInt(out, int64(total), 10)
		out = append(out, `" fill="`...)
		out = o.Light.appendHex(out)
		out = append(out, `"/>`...)
	}
	out = append(out, `<path`...)
	if o.Dark != nil {
		out = append(out, ` fill="`...)
		out = o.Dark.appendHex(out)
		out = append(out, '"')
	}
	out = append(out, ` fill-rule="evenodd" d="`...)
	if o.Rounded {
		out = appendRounded(out, n, on, o.Fillet)
	} else {
		out = appendRect(out, n, on)
	}
	if o.Eyes {
		for _, p := range [3][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
			out = appendEye(out, p[0], p[1])
		}
	}
	out = append(out, `"/></svg>`...)
	return out, nil
}

func (c Color) appendHex(dst []byte) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '#')
	for _, v := range [3]uint8{c.R, c.G, c.B} {
		dst = append(dst, hex[v>>4], hex[v&15])
	}
	return dst
}

// inEye reports whether the module at (x, y) is in one of the three finder patterns of a code n modules wide.
func inEye(n, x, y int) bool {
	return (x < 7 && y < 7) || (x >= n-7 && y < 7) || (x < 7 && y >= n-7)
}

// appendEye is eye of web/src/lib/qr-path.ts: the finder pattern at (x, y) as three rounded squares for fill-rule evenodd.
func appendEye(d []byte, x, y int) []byte {
	sq := func(ox, oy, s, a int) { // a square of side s (tenths of a module) with corner radius a, at (ox, oy) modules
		x0, y0, w := ox*10, oy*10, s-2*a
		arc := func(dx, dy int) {
			d = append(d, 'a')
			d = appendTenths(d, a)
			d = append(d, ' ')
			d = appendTenths(d, a)
			d = append(d, " 0 0 1 "...)
			d = appendTenths(d, dx)
			d = append(d, ' ')
			d = appendTenths(d, dy)
		}
		d = append(d, 'M')
		d = appendTenths(d, x0+a)
		d = append(d, ' ')
		d = appendTenths(d, y0)
		d = append(d, 'h')
		d = appendTenths(d, w)
		arc(a, a)
		d = append(d, 'v')
		d = appendTenths(d, w)
		arc(-a, a)
		d = append(d, 'h')
		d = appendTenths(d, -w)
		arc(-a, -a)
		d = append(d, 'v')
		d = appendTenths(d, -w)
		arc(a, -a)
		d = append(d, 'z')
	}
	sq(x, y, 70, 20)
	sq(x+1, y+1, 50, 12)
	sq(x+2, y+2, 30, 9)
	return d
}

// appendTenths writes t/10 the way JavaScript writes the number: "3", "-2.5", "0.9".
func appendTenths(d []byte, t int) []byte {
	if t < 0 {
		d = append(d, '-')
		t = -t
	}
	d = strconv.AppendInt(d, int64(t/10), 10)
	if t%10 != 0 {
		d = append(d, '.', byte('0'+t%10))
	}
	return d
}

// appendHalf writes h/2 the way JavaScript writes the number: "3", "3.5". h is never negative.
func appendHalf(d []byte, h int) []byte {
	d = strconv.AppendInt(d, int64(h/2), 10)
	if h%2 == 1 {
		d = append(d, '.', '5')
	}
	return d
}

// ---- the window: where it may sit and how much error correction it may use ----

// windowing knows, for one code, which codeword and which Reed-Solomon block each data module belongs to.
type windowing struct {
	c   *Code
	pos []int   // the alignment pattern centres
	cw  []int32 // codeword of the module at y*Size+x, -1 for function modules and remainder bits
	// blockOf is the Reed-Solomon block of every codeword in the interleaved order, correct is how many codewords a block
	// can correct.
	blockOf []int32
	correct int
	blocks  int
}

func (c *Code) windowCheck() *windowing {
	w := &windowing{c: c, pos: alignmentPositions(c.ver), cw: make([]int32, c.Size*c.Size)}
	for i := range w.cw {
		w.cw[i] = -1
	}
	total := rawModules(c.ver) / 8
	i := 0
	zigzag(c.Size, c.fn, func(pos int) {
		if i < total*8 {
			w.cw[pos] = int32(i / 8)
		}
		i++
	})
	// the interleaving of interleave(): data codewords column by column over the blocks, then the error correction ones
	nb, ecc := numBlocks[c.level][c.ver], eccPerBlock[c.level][c.ver]
	shortBlocks, shortLen := nb-total%nb, total/nb
	w.blockOf = make([]int32, 0, total)
	for i := 0; i <= shortLen-ecc; i++ {
		for b := 0; b < nb; b++ {
			if i < shortLen-ecc || b >= shortBlocks {
				w.blockOf = append(w.blockOf, int32(b))
			}
		}
	}
	for i := 0; i < ecc; i++ {
		for b := 0; b < nb; b++ {
			w.blockOf = append(w.blockOf, int32(b))
		}
	}
	w.correct, w.blocks = ecc/2, nb
	return w
}

// bounds are the modules the window touches: [x0, x1) by [x0, x1), the square of side size in the middle widened by half a
// module (the board's rule: x + 1 > lo && x < hi).
func (w *windowing) bounds(size float64) (x0, x1 int) {
	n := float64(w.c.Size)
	lo, hi := (n-size)/2-0.5, (n+size)/2+0.5
	x0 = max(int(math.Floor(lo)), 0)
	x1 = min(int(math.Ceil(hi)), w.c.Size)
	for x0 < x1 && !(float64(x0+1) > lo) {
		x0++
	}
	for x1 > x0 && !(float64(x1-1) < hi) {
		x1--
	}
	return x0, x1
}

func (w *windowing) clear(size float64, f func(x, y int)) {
	x0, x1 := w.bounds(size)
	for y := x0; y < x1; y++ {
		for x := x0; x < x1; x++ {
			f(x, y)
		}
	}
}

// coverable reports whether the window may cover the function module at (x, y): an alignment pattern may be covered unless
// it is the bottom-right one, the one the readers use. The finder patterns with their separators, the timing patterns and the
// format and version information never may.
func (w *windowing) coverable(x, y int) bool {
	if x == 6 || y == 6 {
		return false // timing
	}
	pos := w.pos
	for i, cx := range pos {
		for j, cy := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0) || (i == len(pos)-1 && j == len(pos)-1) {
				continue // under a finder pattern, or the one at the bottom right
			}
			if abs(x-cx) <= 2 && abs(y-cy) <= 2 {
				return true
			}
		}
	}
	return false
}

// fits returns nil when a window of this size may be cut out, else the reason.
func (w *windowing) fits(size float64) error {
	c := w.c
	if c.level < Q {
		return ErrWindowLevel
	}
	x0, x1 := w.bounds(size)
	hit := make([]bool, len(w.blockOf))
	perBlock := make([]int, w.blocks)
	for y := x0; y < x1; y++ {
		for x := x0; x < x1; x++ {
			p := y*c.Size + x
			if c.fn[p] {
				if !w.coverable(x, y) {
					return ErrWindowPatterns
				}
				continue
			}
			if k := w.cw[p]; k >= 0 && c.mods[p] { // a light module the window takes away changes nothing
				if !hit[k] {
					hit[k] = true
					perBlock[w.blockOf[k]]++
				}
			}
		}
	}
	limit := int(float64(w.correct) * windowShare)
	for _, n := range perBlock {
		if n > limit {
			return ErrWindowBudget
		}
	}
	return nil
}
