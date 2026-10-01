package qr

import "strings"

// Terminal draws the code with Unicode half blocks, two modules per text row, inside the 4-module quiet zone
// (ISO/IEC 18004 "Quiet zone"). Every row ends with a newline.
//
// By default the light modules are the blocks, which scans on a dark terminal (light text on a dark background).
// With invert the dark modules are the blocks, for a light terminal.
func (c *Code) Terminal(invert bool) string {
	const quiet = 4
	n := c.Size + 2*quiet
	// Outside the symbol Black is false: the quiet zone is light.
	filled := func(x, y int) bool { return c.Black(x-quiet, y-quiet) == invert }
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		for x := 0; x < n; x++ {
			switch top, bottom := filled(x, y), filled(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
