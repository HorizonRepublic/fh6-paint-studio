// Package spark is the stylizer's Spark engine: it puts back the small specular highlights — eye
// catchlights, hair sheen, rim-light glints — that the flat fill averages away. In anime the eye
// catchlight is what gives a face life, and it is a tiny, locally-bright spot the global quantizer
// can't preserve. Spark finds small, locally-bright blobs (bright AND much brighter than their
// neighbourhood, AND small) and lays a bright little ellipse on each. Registered as "spark".
package spark

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
	"fh6-paint-studio/internal/stylize/shape"
)

var dbg = os.Getenv("SPARK_DEBUG") != ""

// Config holds the Spark engine's eye-tunable knobs.
type Config struct {
	AbsMin     float64 `json:"absMin"`     // min absolute luma to count as a highlight (only bright spots)
	Contrast   float64 `json:"contrast"`   // min (luma − local mean): how much it must out-shine its surroundings
	Blur       float64 `json:"blur"`       // neighbourhood radius for the local mean (px)
	MinArea    int     `json:"minArea"`    // drop sub-this blobs (single-pixel noise)
	MaxArea    int     `json:"maxArea"`    // drop super-this blobs (those are lit regions, not specular spots)
	Alpha      float64 `json:"alpha"`      // highlight opacity (1=opaque catchlight)
	GrowRadius float64 `json:"growRadius"` // scale the emitted ellipse radius vs the blob's equivalent radius
	Budget     int     `json:"budget"`     // hard shape cap (0 = use the run's remaining budget)
}

// Defaults are the starting knobs; tuned by eye on faces in the image bank.
func Defaults() Config {
	return Config{AbsMin: 0.5, Contrast: 0.12, Blur: 8, MinArea: 2, MaxArea: 60,
		Alpha: 0.9, GrowRadius: 1.1, Budget: 0}
}

type engine struct{ cfg Config }

func (e *engine) Name() string { return "spark" }

func (e *engine) Generate(ctx *stylize.Context) ([]model.Shape, error) {
	src := ctx.Orig
	if src == nil {
		src = ctx.Src
	}
	w, h := src.W, src.H
	luma := make([]float64, w*h)
	for i, p := range src.Pix {
		luma[i] = 0.299*float64(p.R) + 0.587*float64(p.G) + 0.114*float64(p.B)
	}
	local := boxBlur(luma, w, h, int(e.cfg.Blur))
	mask := make([]bool, w*h)
	nmask := 0
	for i := range luma {
		if luma[i] > e.cfg.AbsMin && luma[i]-local[i] > e.cfg.Contrast {
			mask[i] = true
			nmask++
		}
	}
	if dbg {
		var lmax float64
		for _, v := range luma {
			if v > lmax {
				lmax = v
			}
		}
		fmt.Fprintf(os.Stderr, "[spark] lumaMax=%.3f maskpx=%d/%d (absMin=%.2f contrast=%.2f)\n", lmax, nmask, w*h, e.cfg.AbsMin, e.cfg.Contrast)
	}

	budget := e.cfg.Budget
	if budget <= 0 || budget > ctx.Budget {
		budget = ctx.Budget
	}
	if budget < 1 {
		return nil, nil
	}
	var shapes []model.Shape
	visited := make([]bool, w*h)
	var stack, pix []int
	for start := 0; start < w*h && len(shapes) < budget; start++ {
		if visited[start] || !mask[start] {
			visited[start] = true
			continue
		}
		stack = append(stack[:0], start)
		pix = pix[:0]
		visited[start] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pix = append(pix, p)
			px, py := p%w, p/w
			if px > 0 && !visited[p-1] && mask[p-1] {
				visited[p-1] = true
				stack = append(stack, p-1)
			}
			if px < w-1 && !visited[p+1] && mask[p+1] {
				visited[p+1] = true
				stack = append(stack, p+1)
			}
			if py > 0 && !visited[p-w] && mask[p-w] {
				visited[p-w] = true
				stack = append(stack, p-w)
			}
			if py < h-1 && !visited[p+w] && mask[p+w] {
				visited[p+w] = true
				stack = append(stack, p+w)
			}
		}
		if len(pix) < e.cfg.MinArea || len(pix) > e.cfg.MaxArea {
			continue // too small (noise) or too big (a lit region, not a specular spot)
		}
		var sx, sy, sr, sg, sb float64
		for _, p := range pix {
			sx += float64(p % w)
			sy += float64(p / w)
			c := src.Pix[p]
			sr, sg, sb = sr+float64(c.R), sg+float64(c.G), sb+float64(c.B)
		}
		n := float64(len(pix))
		cx, cy := sx/n, sy/n
		rad := math.Sqrt(n/math.Pi) * e.cfg.GrowRadius
		if rad < 1 {
			rad = 1
		}
		shapes = append(shapes, model.Shape{Type: model.TypeRotatedEllipse,
			Color: []int{shape.C255(float32(sr / n)), shape.C255(float32(sg / n)), shape.C255(float32(sb / n)), shape.C255(float32(e.cfg.Alpha))},
			Data:  []float64{cx, cy, rad, rad, 0}})
	}
	if dbg {
		fmt.Fprintf(os.Stderr, "[spark] emitted %d highlights\n", len(shapes))
	}
	return shapes, nil
}

// boxBlur is a separable box blur (radius r) giving a cheap local mean. Edges clamp.
func boxBlur(src []float64, w, h, r int) []float64 {
	if r < 1 {
		out := make([]float64, len(src))
		copy(out, src)
		return out
	}
	clamp := func(v, hi int) int {
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	tmp := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				acc += src[y*w+clamp(x+i, w-1)]
			}
			tmp[y*w+x] = acc / float64(2*r+1)
		}
	}
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				acc += tmp[clamp(y+i, h-1)*w+x]
			}
			out[y*w+x] = acc / float64(2*r+1)
		}
	}
	return out
}

func init() {
	stylize.RegisterEngine("spark", func(cfg json.RawMessage) (stylize.Engine, error) {
		c := Defaults()
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
		}
		return &engine{cfg: c}, nil
	})
}
