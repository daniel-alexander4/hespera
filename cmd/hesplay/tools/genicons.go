//go:build ignore

// genicons writes the phone remote's PWA icons. Stdlib only — no design tool,
// no binary blobs of unknown provenance in the tree, and the mark is
// reproducible from source. Run from cmd/hesplay: go run tools/genicons.go
//
// The mark is a play triangle in Catppuccin mauve on base, matching Hespera's
// own palette (web/static/app.css :root).
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

var (
	base  = color.RGBA{0x1e, 0x1e, 0x2e, 0xff} // Catppuccin base
	mauve = color.RGBA{0xcb, 0xa6, 0xf7, 0xff} // accent
)

func main() {
	write("web/icons/icon-192.png", render(192, 0.16))
	write("web/icons/icon-512.png", render(512, 0.16))
	// Maskable icons get cropped to a circle by the launcher, so the art has
	// to sit inside the ~80% safe zone.
	write("web/icons/icon-maskable-512.png", render(512, 0.28))
}

func render(size int, pad float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	fill(img, base)

	s := float64(size)
	inset := s * pad
	side := s - 2*inset

	// A play triangle inscribed in the safe square, nudged right so its optical
	// centre (a triangle's centroid sits left of its bounding box) lands middle.
	x0 := inset + side*0.16
	x1 := inset + side*0.94
	yTop := inset + side*0.06
	yBot := inset + side*0.94
	triangle(img, x0, yTop, x0, yBot, x1, (yTop+yBot)/2, mauve)
	return img
}

func fill(img *image.RGBA, c color.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// triangle fills the triangle (ax,ay)(bx,by)(cx,cy) with 3x3 supersampling, so
// the diagonals read as clean edges rather than staircases at 192px.
func triangle(img *image.RGBA, ax, ay, bx, by, cx, cy float64, col color.RGBA) {
	minX := int(math.Floor(math.Min(ax, math.Min(bx, cx))))
	maxX := int(math.Ceil(math.Max(ax, math.Max(bx, cx))))
	minY := int(math.Floor(math.Min(ay, math.Min(by, cy))))
	maxY := int(math.Ceil(math.Max(ay, math.Max(by, cy))))

	sign := func(px, py, qx, qy, rx, ry float64) float64 {
		return (px-rx)*(qy-ry) - (qx-rx)*(py-ry)
	}
	inside := func(px, py float64) bool {
		d1 := sign(px, py, ax, ay, bx, by)
		d2 := sign(px, py, bx, by, cx, cy)
		d3 := sign(px, py, cx, cy, ax, ay)
		hasNeg := d1 < 0 || d2 < 0 || d3 < 0
		hasPos := d1 > 0 || d2 > 0 || d3 > 0
		return !(hasNeg && hasPos)
	}

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			if !image.Pt(x, y).In(img.Bounds()) {
				continue
			}
			hits := 0
			for sy := 0; sy < 3; sy++ {
				for sx := 0; sx < 3; sx++ {
					px := float64(x) + (float64(sx)+0.5)/3
					py := float64(y) + (float64(sy)+0.5)/3
					if inside(px, py) {
						hits++
					}
				}
			}
			if hits == 0 {
				continue
			}
			a := float64(hits) / 9
			dst := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(float64(col.R)*a + float64(dst.R)*(1-a)),
				G: uint8(float64(col.G)*a + float64(dst.G)*(1-a)),
				B: uint8(float64(col.B)*a + float64(dst.B)*(1-a)),
				A: 0xff,
			})
		}
	}
}

func write(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("genicons: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("genicons: encode %s: %v", path, err)
	}
	log.Printf("wrote %s", path)
}
