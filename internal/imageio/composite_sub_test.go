package imageio

import (
	"image"
	"image/draw"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// compositing a shape into a sub-rectangle copy of the base must be bit-identical to compositing it onto
// the whole canvas — this is what lets the editor's fast drag refresh just the dragged region.
func TestCompositeShapeSubMatchesFull(t *testing.T) {
	const w, h = 64, 48
	base := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range base.Pix {
		base.Pix[i] = uint8((i*7 + 11) % 256)
	}
	for _, sh := range []model.Shape{
		{Type: model.TypeRotatedEllipse, Data: []float64{30, 24, 8, 6, 20}, Color: []int{200, 50, 50, 180}},
		{Type: model.TypeRectangle, Data: []float64{20, 18, 10, 7}, Color: []int{30, 120, 200, 255}},
		{Type: model.TypeTriangle, Data: []float64{10, 10, 40, 14, 22, 38}, Color: []int{200, 200, 40, 140}},
	} {
		full := image.NewNRGBA(image.Rect(0, 0, w, h))
		copy(full.Pix, base.Pix)
		CompositeShapeOnto(full, sh, w, h)

		x0, y0, x1, y1 := raster.BBox(model.KindFromType(sh.Type), model.ParamsFromShape(sh), w, h)
		b := image.Rect(x0-1, y0-1, x1+2, y1+2).Intersect(image.Rect(0, 0, w, h))
		sub := image.NewNRGBA(b)
		draw.Draw(sub, b, base, b.Min, draw.Src)
		CompositeShapeOnto(sub, sh, w, h)

		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				fq, sq := full.PixOffset(x, y), sub.PixOffset(x, y)
				for c := 0; c < 4; c++ {
					if full.Pix[fq+c] != sub.Pix[sq+c] {
						t.Fatalf("type %d pixel (%d,%d) ch %d: full=%d sub=%d", sh.Type, x, y, c, full.Pix[fq+c], sub.Pix[sq+c])
					}
				}
			}
		}
	}
}

func benchShape() model.Shape {
	return model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{1000, 1000, 60, 40, 15}, Color: []int{200, 50, 50, 255}}
}

// BenchmarkDragFullCanvas is the old per-frame cost: allocate + copy the whole base, then composite.
func BenchmarkDragFullCanvas(b *testing.B) {
	const w, h = 2000, 2000
	base := image.NewNRGBA(image.Rect(0, 0, w, h))
	sh := benchShape()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		img := image.NewNRGBA(base.Bounds())
		copy(img.Pix, base.Pix)
		CompositeShapeOnto(img, sh, w, h)
		_ = img
	}
}

// BenchmarkDragSprite is the fast path: copy + composite only the dragged shape's bounding box.
func BenchmarkDragSprite(b *testing.B) {
	const w, h = 2000, 2000
	base := image.NewNRGBA(image.Rect(0, 0, w, h))
	sh := benchShape()
	x0, y0, x1, y1 := raster.BBox(model.KindFromType(sh.Type), model.ParamsFromShape(sh), w, h)
	bb := image.Rect(x0-1, y0-1, x1+2, y1+2).Intersect(image.Rect(0, 0, w, h))
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		sprite := image.NewNRGBA(bb)
		draw.Draw(sprite, bb, base, bb.Min, draw.Src)
		CompositeShapeOnto(sprite, sh, w, h)
		_ = sprite
	}
}
