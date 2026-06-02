package metric

// ContentStats are cheap single-pass image statistics used to auto-pick a content
// MODE. The key discriminator between flat/vector art and smooth/photographic content
// is NOT "are adjacent pixels similar" (true for gradients too) but the GRADIENT-BAND
// distribution: each adjacent-pixel step is classified flat / ramp / edge by its color
// delta. Flat vector/logo art = mostly flat + a few hard edges, almost no ramp. Smooth
// photos/illustrations = lots of RAMP (gentle shading/gradients). Cel/anime sits between.
type ContentStats struct {
	FlatFrac float64 // fraction of steps with ~no color change (uniform fills)
	RampFrac float64 // fraction with a gentle change (smooth shading/gradients) — the key signal
	EdgeFrac float64 // fraction with a hard jump (outlines/edges)
	Colors   int     // distinct colors quantized to 5 bits/channel
}

// ContentClass computes the stats in one O(pixels) pass (microseconds). The per-step
// delta is the summed |ΔRGB| to the right and down neighbors (max of the two), so it
// catches color edges, not just luma.
func ContentClass(px []float32, w, h int) ContentStats {
	if w <= 0 || h <= 0 || len(px) < w*h*4 {
		return ContentStats{}
	}
	const q = 31 // 5 bits/channel
	seen := make([]bool, 1<<15)
	nColors := 0
	var flat, ramp, edge, n int
	qc := func(v float32) int {
		if v <= 0 {
			return 0
		}
		if v >= 1 {
			return q
		}
		return int(v * q)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			key := (qc(px[i]) << 10) | (qc(px[i+1]) << 5) | qc(px[i+2])
			if !seen[key] {
				seen[key] = true
				nColors++
			}
			var g float32
			if x+1 < w {
				j := i + 4
				g = absf(px[i]-px[j]) + absf(px[i+1]-px[j+1]) + absf(px[i+2]-px[j+2])
			}
			if y+1 < h {
				k := i + w*4
				gd := absf(px[i]-px[k]) + absf(px[i+1]-px[k+1]) + absf(px[i+2]-px[k+2])
				if gd > g {
					g = gd
				}
			}
			if x+1 < w || y+1 < h {
				switch {
				case g < 0.02:
					flat++
				case g < 0.25:
					ramp++
				default:
					edge++
				}
				n++
			}
		}
	}
	if n == 0 {
		return ContentStats{Colors: nColors}
	}
	fn := float64(n)
	return ContentStats{
		FlatFrac: float64(flat) / fn,
		RampFrac: float64(ramp) / fn,
		EdgeFrac: float64(edge) / fn,
		Colors:   nColors,
	}
}

// Mode classifies the content into "flat" (vector/logo/line-art/cartoon — opaque, crisp
// edges, rectangle-heavier), "photo" (photographic/3D — lots of smooth gradient, most
// transparency) or "anime" (cel/shaded illustration — moderate shading). Thresholds are
// empirically tuned. The caller still forces opaque for transparent cutouts.
func (s ContentStats) Mode() string {
	switch {
	case s.RampFrac < 0.15 && s.Colors < 4000:
		return "flat"
	case s.RampFrac > 0.38:
		return "photo"
	default:
		return "anime"
	}
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
