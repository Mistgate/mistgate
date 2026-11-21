//go:build !js

package decoyphotos

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // a photo may come as PNG
)

// maxPixels is the largest picture Scale decodes: a bomb of a file must not fill the panel's memory.
const maxPixels = 40 << 20

// Scaled is a photo cut to its slot and brought down to its width, not yet encoded.
type Scaled struct{ img *image.RGBA }

// Scale decodes a JPEG or PNG, cuts the middle of it to the shape w:h, and shrinks it to at most maxW pixels wide (never
// enlarges). Area averaging in pure Go; no metadata survives the re-encoding.
func Scale(src []byte, w, h, maxW int) (*Scaled, error) {
	if w <= 0 || h <= 0 || maxW <= 0 {
		return nil, errors.New("decoyphotos: bad shape")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, errors.New("decoyphotos: unreadable picture")
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, errors.New("decoyphotos: unreadable picture")
	}
	b := img.Bounds()
	cw, ch := b.Dx(), b.Dy()
	// the biggest rectangle of the slot's shape that fits, in the middle
	if cw*h > ch*w {
		cw = ch * w / h
	} else {
		ch = cw * h / w
	}
	if cw < 1 || ch < 1 {
		return nil, errors.New("decoyphotos: picture too thin")
	}
	x0, y0 := b.Min.X+(b.Dx()-cw)/2, b.Min.Y+(b.Dy()-ch)/2
	dw := min(cw, maxW)
	dh := max(1, ch*dw/cw)
	return &Scaled{img: boxScale(img, x0, y0, cw, ch, dw, dh)}, nil
}

// Size is the width and height in pixels.
func (s *Scaled) Size() (int, int) { return s.img.Bounds().Dx(), s.img.Bounds().Dy() }

// JPEG encodes the picture at the quality (1 to 100).
func (s *Scaled) JPEG(quality int) ([]byte, error) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, s.img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// boxScale averages the source rectangle (x0, y0, sw, sh) of img into a dw x dh picture: every output pixel is the mean of the
// source area it covers, edges weighted by their fraction. Two passes, columns then rows, in float32 / float64.
func boxScale(img image.Image, x0, y0, sw, sh, dw, dh int) *image.RGBA {
	// pass 1: horizontal, into rows of dw pixels (3 channels) for each of the sh source rows
	mid := make([]float32, sh*dw*3)
	xs := float64(sw) / float64(dw)
	row := make([]float32, sw*3)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			r, g, b, _ := img.At(x0+x, y0+y).RGBA()
			row[x*3], row[x*3+1], row[x*3+2] = float32(r>>8), float32(g>>8), float32(b>>8)
		}
		for dx := 0; dx < dw; dx++ {
			a, z := float64(dx)*xs, float64(dx+1)*xs
			var r, g, b, wsum float64
			for x := int(a); x < sw && float64(x) < z; x++ {
				wt := min(z, float64(x+1)) - max(a, float64(x))
				r += float64(row[x*3]) * wt
				g += float64(row[x*3+1]) * wt
				b += float64(row[x*3+2]) * wt
				wsum += wt
			}
			o := (y*dw + dx) * 3
			mid[o], mid[o+1], mid[o+2] = float32(r/wsum), float32(g/wsum), float32(b/wsum)
		}
	}
	// pass 2: vertical
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	ys := float64(sh) / float64(dh)
	for dy := 0; dy < dh; dy++ {
		a, z := float64(dy)*ys, float64(dy+1)*ys
		for dx := 0; dx < dw; dx++ {
			var r, g, b, wsum float64
			for y := int(a); y < sh && float64(y) < z; y++ {
				wt := min(z, float64(y+1)) - max(a, float64(y))
				o := (y*dw + dx) * 3
				r += float64(mid[o]) * wt
				g += float64(mid[o+1]) * wt
				b += float64(mid[o+2]) * wt
				wsum += wt
			}
			out.SetRGBA(dx, dy, color.RGBA{uint8(r/wsum + .5), uint8(g/wsum + .5), uint8(b/wsum + .5), 255})
		}
	}
	return out
}
