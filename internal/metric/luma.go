package metric

// Rec.601 luma coefficients — perceptual brightness weights for a straight-alpha, sRGB-ish RGB triple.
const (
	lumaR = 0.299
	lumaG = 0.587
	lumaB = 0.114
)

// Luma returns the Rec.601 perceptual luminance of an (r, g, b) triple in the working colour space.
// It is the single source of these weights, which the saliency/edge maps and the standout detector
// all share; keep it tiny so the compiler inlines it (the maps call it per pixel).
func Luma(r, g, b float32) float32 { return lumaR*r + lumaG*g + lumaB*b }
