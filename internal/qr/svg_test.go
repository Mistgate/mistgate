package qr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"
)

// path_golden.json holds, for a few texts, the modules this encoder makes and the path data web/src/lib/qr-path.ts
// (roundedPath with eyes, and the page's window rule) makes of them; web/src/lib/qr-path.test.ts reads the same file, so the two
// implementations of the shape are held to one another. Short paths are stored whole, the long ones by length and hash.
type goldenPath struct {
	Name   string   `json:"name"`
	Text   string   `json:"text"`
	Level  Level    `json:"level"`
	Window float64  `json:"window"`
	Rows   []string `json:"rows"`
	D      string   `json:"d"`
	DSHA   string   `json:"d_sha256"`
	DLen   int      `json:"d_len"`
}

func loadGolden(t testing.TB) []goldenPath {
	t.Helper()
	raw, err := os.ReadFile("testdata/path_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g []goldenPath
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

var svgShape = regexp.MustCompile(`^<svg xmlns="http://www.w3.org/2000/svg" viewBox="-?\d+ -?\d+ \d+ \d+">` +
	`(<rect x="-?\d+" y="-?\d+" width="\d+" height="\d+" fill="#[0-9a-f]{6}"/>)?` +
	`<path( fill="#[0-9a-f]{6}")? fill-rule="evenodd" d="[MHVazMHVa0-9. \-]*"/></svg>$`)

// dOf is the path data of an SVG made by SVG.
func dOf(t testing.TB, svg []byte) string {
	t.Helper()
	s := string(svg)
	_, rest, ok := strings.Cut(s, ` d="`)
	if !ok {
		t.Fatalf("no path in %.80s", s)
	}
	return strings.TrimSuffix(rest, `"/></svg>`)
}

func TestPathMatchesTheBoard(t *testing.T) {
	for _, g := range loadGolden(t) {
		t.Run(g.Name, func(t *testing.T) {
			c := mustEncode(t, g.Text, g.Level)
			for y, row := range g.Rows {
				for x, ch := range row {
					if c.Black(x, y) != (ch == '1') {
						t.Fatalf("module %d,%d: the encoder differs from the golden symbol", x, y)
					}
				}
			}
			if got := c.FitWindow(LogoWindow(c.Size)); g.Window > 0 && got != g.Window {
				t.Errorf("FitWindow = %v, the golden window is %v", got, g.Window)
			}
			svg, err := c.SVG(SVGOptions{Rounded: true, Fillet: true, Eyes: true, Window: g.Window})
			if err != nil {
				t.Fatal(err)
			}
			d := dOf(t, svg)
			sum := sha256.Sum256([]byte(d))
			if len(d) != g.DLen || hex.EncodeToString(sum[:]) != g.DSHA {
				t.Errorf("the path has %d bytes, sha256 %x; the board's has %d, %s", len(d), sum, g.DLen, g.DSHA)
			}
			if g.D != "" && d != g.D {
				t.Errorf("the path differs from the board's:\n got %.120s\nwant %.120s", d, g.D)
			}
		})
	}
}

func TestSVGShapes(t *testing.T) {
	c := mustEncode(t, "hello, qr1", M)
	black, white := Color{0, 0, 0}, Color{0xff, 0xfe, 0x01}
	for _, o := range []SVGOptions{
		{},
		{Rounded: true},
		{Rounded: true, Quiet: 4, Dark: &black, Light: &white},
		{Quiet: 32, Light: &white},
	} {
		svg, err := c.SVG(o)
		if err != nil {
			t.Fatal(err)
		}
		if !svgShape.Match(svg) {
			t.Errorf("%+v: unexpected markup %.200s", o, svg)
		}
	}
	svg, _ := c.SVG(SVGOptions{Quiet: 4, Dark: &black, Light: &white})
	for _, want := range []string{`viewBox="-4 -4 29 29"`, `<rect x="-4" y="-4" width="29" height="29" fill="#fffe01"/>`, `<path fill="#000000" `} {
		if !strings.Contains(string(svg), want) {
			t.Errorf("missing %s in %.160s", want, svg)
		}
	}
	// the square shape is the dark modules and nothing else: the area of its runs is the number of dark modules
	d := dOf(t, mustSVG(t, c, SVGOptions{}))
	var runs, cells int
	for _, m := range regexp.MustCompile(`M(\d+) (\d+)H(\d+)V(\d+)H(\d+)V(\d+)z`).FindAllStringSubmatch(d, -1) {
		runs++
		cells += atoi(m[3]) - atoi(m[1])
	}
	want := 0
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				want++
			}
		}
	}
	if cells != want || strings.Count(d, "M") != runs {
		t.Errorf("square path: %d cells in %d runs, the code has %d dark modules", cells, runs, want)
	}
}

func mustSVG(t testing.TB, c *Code, o SVGOptions) []byte {
	t.Helper()
	b, err := c.SVG(o)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func atoi(s string) int {
	n := 0
	for _, ch := range s {
		n = n*10 + int(ch-'0')
	}
	return n
}

func TestSVGRefusals(t *testing.T) {
	m := mustEncode(t, "https://example.com/some/link/to/scan", M)
	q := mustEncode(t, "https://example.com/some/link/to/scan", Q)
	h := mustEncode(t, "x", H)
	nan, inf := zero()/zero(), 1/zero()
	for _, c := range []struct {
		name string
		code *Code
		o    SVGOptions
		want error
	}{
		{"level M", m, SVGOptions{Window: 5}, ErrWindowLevel},
		{"too big", q, SVGOptions{Window: float64(q.Size) - 1}, ErrWindowPatterns},
		{"the whole code", h, SVGOptions{Window: float64(h.Size)}, ErrWindowPatterns},
		{"negative", q, SVGOptions{Window: -1}, ErrOptions},
		{"not a number", q, SVGOptions{Window: nan}, ErrOptions},
		{"infinite", q, SVGOptions{Window: inf}, ErrOptions},
		{"over the symbol", q, SVGOptions{Window: float64(q.Size) + 1}, ErrOptions},
		{"quiet zone", q, SVGOptions{Quiet: 33}, ErrOptions},
		{"negative quiet zone", q, SVGOptions{Quiet: -1}, ErrOptions},
	} {
		if _, err := c.code.SVG(c.o); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if got := m.FitWindow(5); got != 0 {
		t.Errorf("FitWindow at level M = %v", got)
	}
	if got := q.FitWindow(zero() / zero()); got != 0 {
		t.Errorf("FitWindow(NaN) = %v", got)
	}
}

// zero is 0 the compiler cannot fold, for the NaN and Inf cases.
func zero() float64 { return float64(len(os.Args) / 1000) }

// protectedModules is the independent list of what a window must leave alone, written from the standard and not from
// the encoder: the three finder patterns with their separators, the timing lines, the format information and the version
// information, and the alignment pattern at the bottom right.
func protectedModules(ver int) map[[2]int]bool {
	n := 4*ver + 17
	p := map[[2]int]bool{}
	for i := 0; i < 8; i++ {
		for j := 0; j < 8; j++ {
			p[[2]int{i, j}], p[[2]int{n - 1 - i, j}], p[[2]int{i, n - 1 - j}] = true, true, true
		}
	}
	for i := 0; i < n; i++ {
		p[[2]int{6, i}], p[[2]int{i, 6}] = true, true
	}
	for i := 0; i < 9; i++ { // format information around the top left finder
		p[[2]int{8, i}], p[[2]int{i, 8}] = true, true
	}
	for i := 0; i < 8; i++ { // ... and beside the other two
		p[[2]int{n - 1 - i, 8}], p[[2]int{8, n - 1 - i}] = true, true
	}
	if ver >= 7 { // version information: two 6x3 blocks
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				p[[2]int{n - 11 + j, i}], p[[2]int{i, n - 11 + j}] = true, true
			}
		}
	}
	if ver >= 2 {
		c := n - 7
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				p[[2]int{c + dx, c + dy}] = true
			}
		}
	}
	return p
}

// A window is never cut out of a pattern the readers use, in any version, at the sizes the board asks for and at every
// size from there up to the whole code.
func TestWindowNeverCoversPatterns(t *testing.T) {
	for ver := 1; ver <= 40; ver++ {
		for _, level := range []Level{Q, H} {
			c := build(fill(capacity(ver, level), ver), ver, level, 0)
			prot := protectedModules(ver)
			w := c.windowCheck()
			for _, want := range []float64{LogoWindow(c.Size), float64(c.Size) / 3, float64(c.Size) / 2, float64(c.Size)} {
				got := c.FitWindow(want)
				if got > want {
					t.Fatalf("v%d: FitWindow(%v) = %v", ver, want, got)
				}
				if got == 0 {
					continue
				}
				x0, x1 := w.bounds(got)
				for y := x0; y < x1; y++ {
					for x := x0; x < x1; x++ {
						if prot[[2]int{x, y}] {
							t.Fatalf("v%d level %d: window %v covers the protected module %d,%d", ver, level, got, x, y)
						}
					}
				}
				if _, err := c.SVG(SVGOptions{Window: got}); err != nil {
					t.Fatalf("v%d: the window FitWindow returned is refused: %v", ver, err)
				}
			}
			// just above the fit it is refused (when the fit is below the wish)
			if got := c.FitWindow(float64(c.Size)); got+0.5 < float64(c.Size) {
				if _, err := c.SVG(SVGOptions{Window: got + 0.5}); err == nil {
					t.Fatalf("v%d: a window half a module over the fit was accepted", ver)
				}
			}
		}
	}
}

// fill is n bytes of text that give a symbol dense in both colours.
func fill(n, ver int) []byte {
	r := rand.New(rand.NewSource(int64(ver)))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return b
}

// readBack takes the codewords out of a symbol the way a reader does (undoing the mask, walking the strips) and returns
// them with the index of the block each belongs to, which it works out from the block structure alone.
func readBack(c *Code, mask int) (cw []byte, block []int) {
	n := c.Size
	bits := make([]bool, 0, n*n)
	zigzag(n, c.fn, func(pos int) {
		v := c.mods[pos]
		if maskBit(mask, pos%n, pos/n) {
			v = !v
		}
		bits = append(bits, v)
	})
	total := rawModules(c.ver) / 8
	for i := 0; i < total; i++ {
		var b byte
		for k := 0; k < 8; k++ {
			b <<= 1
			if bits[i*8+k] {
				b |= 1
			}
		}
		cw = append(cw, b)
	}
	nb, ecc := numBlocks[c.level][c.ver], eccPerBlock[c.level][c.ver]
	// block b holds shortData or shortData+1 data codewords (the last blocks are the longer ones); data goes out column by
	// column, then the error correction codewords column by column
	shortData := total/nb - ecc
	longFrom := nb - total%nb
	for col := 0; col <= shortData; col++ {
		for b := 0; b < nb; b++ {
			if col < shortData || b >= longFrom {
				block = append(block, b)
			}
		}
	}
	for col := 0; col < ecc; col++ {
		for b := 0; b < nb; b++ {
			block = append(block, b)
		}
	}
	return cw, block
}

// What the window costs is checked on the symbol itself: cut it out, read the codewords back and count, per block,
// the ones that changed. That has to stay within the share of the block's correction the budget allows, for every version,
// and SVG's budget must be the same count.
func TestWindowBudgetHolds(t *testing.T) {
	for ver := 1; ver <= 40; ver++ {
		for _, level := range []Level{Q, H} {
			text := fill(capacity(ver, level), ver)
			mask := ver % 8
			c := build(text, ver, level, mask)
			cw0, block := readBack(c, mask)
			if len(block) != len(cw0) {
				t.Fatalf("v%d: %d codewords, %d block entries", ver, len(cw0), len(block))
			}
			for _, want := range []float64{LogoWindow(c.Size), float64(c.Size) / 4} {
				got := c.FitWindow(want)
				if got == 0 {
					continue
				}
				cut := *c
				cut.mods = append([]bool(nil), c.mods...)
				c.windowCheck().clear(got, func(x, y int) {
					if !c.fn[y*c.Size+x] {
						cut.mods[y*c.Size+x] = false
					}
				})
				cw1, _ := readBack(&cut, mask)
				changed := map[int]int{}
				for i := range cw0 {
					if cw0[i] != cw1[i] {
						changed[block[i]]++
					}
				}
				correct := eccPerBlock[level][ver] / 2
				for b, n := range changed {
					if float64(n) > float64(correct)*windowShare {
						t.Fatalf("v%d level %d window %v: block %d lost %d codewords, it corrects %d", ver, level, got, b, n, correct)
					}
				}
			}
		}
	}
}

func mustEncodeBytes(t testing.TB, b []byte, l Level) *Code {
	t.Helper()
	c, err := Encode(b, l)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Nothing of the text is in the markup: whatever the input, the SVG is made of the fixed grammar and numbers.
func TestSVGHoldsNoText(t *testing.T) {
	text := `"><script>alert(1)</script>&amp;'` + strings.Repeat("</svg><svg onload=x>", 4)
	c := mustEncode(t, text, Q)
	svg := mustSVG(t, c, SVGOptions{Rounded: true, Window: c.FitWindow(LogoWindow(c.Size)), Quiet: 2, Dark: &Color{1, 2, 3}, Light: &Color{255, 255, 255}})
	if !svgShape.Match(svg) {
		t.Fatalf("the markup is not the fixed shape: %.200s", svg)
	}
	for _, frag := range []string{"script", "alert", "onload", "&amp;", "'"} {
		if bytes.Contains(svg, []byte(frag)) {
			t.Errorf("%q is in the SVG", frag)
		}
	}
}

func TestSVGIsDeterministic(t *testing.T) {
	c := mustEncode(t, "https://example.com/a", Q)
	w := c.FitWindow(6)
	a := mustSVG(t, c, SVGOptions{Rounded: true, Window: w})
	b := mustSVG(t, c, SVGOptions{Rounded: true, Window: w})
	if !bytes.Equal(a, b) {
		t.Error("two renders differ")
	}
	if c.Black(0, 0) != mustEncode(t, "https://example.com/a", Q).Black(0, 0) {
		t.Error("two encodings differ")
	}
}

// FuzzSVG: no input and no option panics, and what comes out is the fixed shape.
func FuzzSVG(f *testing.F) {
	f.Add([]byte("https://example.com/s/alice"), uint8(2), 8.5, uint8(0), true)
	f.Add([]byte(""), uint8(3), 30.0, uint8(4), false)
	f.Add(bytes.Repeat([]byte{0xff, 0, '<', '"'}, 100), uint8(1), -2.0, uint8(40), true)
	f.Add(fill(700, 3), uint8(2), 1e300, uint8(32), true)
	f.Fuzz(func(t *testing.T, data []byte, level uint8, window float64, quiet uint8, rounded bool) {
		if len(data) > 1200 {
			data = data[:1200] // keeps one run short; the long inputs are in TestEncodeLimits
		}
		c, err := Encode(data, Level(level%5))
		if err != nil {
			return
		}
		svg, err := c.SVG(SVGOptions{Rounded: rounded, Window: window, Quiet: int(quiet % 40)})
		if err != nil {
			if svg != nil {
				t.Fatal("an answer with an error")
			}
			return
		}
		if !svgShape.Match(svg) {
			t.Fatalf("not the fixed shape: %.200s", svg)
		}
		if w := c.FitWindow(window); w < 0 {
			t.Fatalf("FitWindow = %v", w)
		}
	})
}

func BenchmarkLink(b *testing.B) {
	text := []byte("https://sub.example.com/s/alice-0123456789abcdef0123456789abcdef-and-a-little-more-to-make-it-120-bytes-long-ok-fine-yes")
	benchEncodeSVG(b, text[:120])
}

func BenchmarkConfig(b *testing.B) {
	text := bytes.Repeat([]byte("PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 16)[:900]
	benchEncodeSVG(b, text)
}

func benchEncodeSVG(b *testing.B, text []byte) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		c, err := Encode(text, Q)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := c.SVG(SVGOptions{Rounded: true, Window: c.FitWindow(LogoWindow(c.Size))}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeLink(b *testing.B) {
	b.ReportAllocs()
	text := bytes.Repeat([]byte("a"), 120)
	for b.Loop() {
		if _, err := Encode(text, Q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeConfig(b *testing.B) {
	b.ReportAllocs()
	text := bytes.Repeat([]byte("a"), 900)
	for b.Loop() {
		if _, err := Encode(text, Q); err != nil {
			b.Fatal(err)
		}
	}
}

func TestPacked(t *testing.T) {
	c := mustEncode(t, "hello, qr1", M)
	b := c.Packed()
	if len(b) != (c.Size*c.Size+7)/8 {
		t.Fatalf("%d bytes for %d modules", len(b), c.Size*c.Size)
	}
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			i := y*c.Size + x
			if got := b[i/8]>>(7-i%8)&1 == 1; got != c.Black(x, y) {
				t.Fatalf("module %d,%d", x, y)
			}
		}
	}
	if pad := b[len(b)-1] & (1<<(8-(c.Size*c.Size)%8%8) - 1); (c.Size*c.Size)%8 != 0 && pad != 0 {
		t.Errorf("the padding bits are %b", pad)
	}
}

// shapes_golden.json: small grids and the paths web/src/lib/qr-path.ts makes of them (read by qr-path.test.ts too).
func TestRoundedShapesMatchTheWeb(t *testing.T) {
	raw, err := os.ReadFile("testdata/shapes_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var shapes []struct {
		Name   string   `json:"name"`
		Rows   []string `json:"rows"`
		Fillet bool     `json:"fillet"`
		D      string   `json:"d"`
	}
	if err := json.Unmarshal(raw, &shapes); err != nil || len(shapes) < 6 {
		t.Fatal(len(shapes), err)
	}
	for _, s := range shapes {
		t.Run(s.Name, func(t *testing.T) {
			n := len(s.Rows)
			on := make([]bool, n*n)
			for y, row := range s.Rows {
				for x, ch := range row {
					on[y*n+x] = ch == '1'
				}
			}
			if got := string(appendRounded(nil, n, on, s.Fillet)); got != s.D {
				t.Errorf("got  %s\nwant %s", got, s.D)
			}
		})
	}
}

// A figure of any size is one shape, however many modules it has: no seams to show between two shapes.
func TestRoundedFigureIsOneShape(t *testing.T) {
	const n = 12
	on := make([]bool, n*n)
	for y := 1; y < 11; y++ {
		for x := 1; x < 11; x++ {
			on[y*n+x] = true
		}
	}
	if d := appendRounded(nil, n, on, true); strings.Count(string(d), "M") != 1 {
		t.Errorf("a 10x10 block is %d shapes", strings.Count(string(d), "M"))
	}
}
