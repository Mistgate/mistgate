package qr

import "strconv"

// appendRect writes the dark modules as squares: a run of them in a row is one rectangle, drawn clockwise from the top left.
func appendRect(d []byte, n int, on []bool) []byte {
	for y := 0; y < n; y++ {
		for x := 0; x < n; {
			if !on[y*n+x] {
				x++
				continue
			}
			e := x
			for e+1 < n && on[y*n+e+1] {
				e++
			}
			d = append(d, 'M')
			d = strconv.AppendInt(d, int64(x), 10)
			d = append(d, ' ')
			d = strconv.AppendInt(d, int64(y), 10)
			d = append(d, 'H')
			d = strconv.AppendInt(d, int64(e+1), 10)
			d = append(d, 'V')
			d = strconv.AppendInt(d, int64(y+1), 10)
			d = append(d, 'H')
			d = strconv.AppendInt(d, int64(x), 10)
			d = append(d, 'V')
			d = strconv.AppendInt(d, int64(y), 10)
			d = append(d, 'z')
			x = e + 1
		}
	}
	return d
}

// the headings E, S, W, N: the dark module is on the right of an edge, so a figure runs clockwise on screen
var (
	edgeDX = [4]int{1, 0, -1, 0}
	edgeDY = [4]int{0, 1, 0, -1}
)

// corner is a point of a figure's outline where the heading changes: its place in tenths of a module, the headings in and out,
// and the radius of its arc in tenths (0: sharp).
type corner struct{ x, y, din, dout, r int }

// appendRounded is roundedPath of web/src/lib/qr-path.ts, number for number: one shape for every connected figure of dark
// modules, drawn along its outline (not module by module: that leaves hairline seams where two shapes meet). Where two edges
// leave a point (two modules that touch at a corner) the loop turns right, so the figures stay apart. A corner that sticks out
// (a right turn) has a radius of 0.5, one that goes in (a left turn) of 0.3 with fillet and none without; a point on a
// straight run adds nothing. Every number is a multiple of 0.1, so it is written from tenths.
func appendRounded(d []byte, n int, on []bool, fillet bool) []byte {
	dark := func(x, y int) bool { return x >= 0 && y >= 0 && x < n && y < n && on[y*n+x] }
	w := n + 1
	var out, seen [4][]bool // out[h][y*w+x]: an edge leaves the lattice point (x, y) in heading h; seen: it is in a loop already
	for h := range out {
		out[h], seen[h] = make([]bool, w*w), make([]bool, w*w)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if on[y*n+x] { // every edge belongs to one module, so these never overwrite one another
				out[0][y*w+x] = !dark(x, y-1)
				out[1][y*w+x+1] = !dark(x+1, y)
				out[2][(y+1)*w+x+1] = !dark(x, y+1)
				out[3][(y+1)*w+x] = !dark(x-1, y)
			}
		}
	}
	radius := func(right bool) int {
		switch {
		case right:
			return 5
		case fillet:
			return 3
		}
		return 0
	}
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			for h0 := 0; h0 < 4; h0++ {
				if !out[h0][y*w+x] || seen[h0][y*w+x] {
					continue
				}
				// one loop: follow the edges, turning right where two leave a point, until the first is met again
				var dirs, xs, ys []int
				cx, cy, ch := x, y, h0
				for {
					seen[ch][cy*w+cx] = true
					dirs, xs, ys = append(dirs, ch), append(xs, cx), append(ys, cy)
					cx, cy = cx+edgeDX[ch], cy+edgeDY[ch]
					for _, c := range [3]int{(ch + 1) % 4, ch, (ch + 3) % 4} {
						if out[c][cy*w+cx] {
							ch = c
							break
						}
					}
					if cx == x && cy == y && ch == h0 {
						break
					}
				}
				m := len(dirs)
				var corners []corner
				for i := 0; i < m; i++ {
					din, dout := dirs[(i+m-1)%m], dirs[i]
					if din != dout {
						corners = append(corners, corner{xs[i] * 10, ys[i] * 10, din, dout, radius(dout == (din+1)%4)})
					}
				}
				first := corners[0]
				px, py := first.x+first.r*edgeDX[first.dout], first.y+first.r*edgeDY[first.dout]
				d = append(d, 'M')
				d = appendTenths(d, px)
				d = append(d, ' ')
				d = appendTenths(d, py)
				for i := 1; i <= len(corners); i++ {
					c := corners[i%len(corners)]
					ex, ey := c.x-c.r*edgeDX[c.din], c.y-c.r*edgeDY[c.din]   // where the corner starts
					qx, qy := c.x+c.r*edgeDX[c.dout], c.y+c.r*edgeDY[c.dout] // and where it ends
					if ex != px || ey != py {
						if ex == px {
							d = append(d, 'V')
							d = appendTenths(d, ey)
						} else {
							d = append(d, 'H')
							d = appendTenths(d, ex)
						}
					}
					if c.r > 0 {
						sweep := byte('0')
						if c.dout == (c.din+1)%4 {
							sweep = '1'
						}
						d = append(d, 'a')
						d = appendTenths(d, c.r)
						d = append(d, ' ')
						d = appendTenths(d, c.r)
						d = append(d, " 0 0 "...)
						d = append(d, sweep, ' ')
						d = appendTenths(d, qx-ex)
						d = append(d, ' ')
						d = appendTenths(d, qy-ey)
					}
					px, py = qx, qy
				}
				d = append(d, 'z')
			}
		}
	}
	if !fillet {
		return d
	}
	// two modules that touch only at a corner: the light module on each side of the point gets a fillet of its own
	for y := 1; y < n; y++ {
		for x := 1; x < n; x++ {
			nw, ne, sw, se := dark(x-1, y-1), dark(x, y-1), dark(x-1, y), dark(x, y)
			if !((ne && sw && !nw && !se) || (nw && se && !ne && !sw)) {
				continue
			}
			lights := [2][2]int{{-1, -1}, {1, 1}} // the light modules lie lx, ly away from the point
			if nw {
				lights = [2][2]int{{1, -1}, {-1, 1}}
			}
			for _, l := range lights {
				sweep := "1"
				if l[0]*l[1] == 1 {
					sweep = "0"
				}
				d = append(d, 'M')
				d = appendTenths(d, x*10)
				d = append(d, ' ')
				d = appendTenths(d, y*10)
				d = append(d, 'H')
				d = appendTenths(d, x*10+3*l[0])
				d = append(d, "a0.3 0.3 0 0 "...)
				d = append(d, sweep...)
				d = append(d, ' ')
				d = appendTenths(d, -3*l[0])
				d = append(d, ' ')
				d = appendTenths(d, 3*l[1])
				d = append(d, 'z')
			}
		}
	}
	return d
}
