//go:build vulkan

package vulkan

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The proposer's shader path is checked against a reference produced OUTSIDE this package: the
// trained network evaluated in torch, dumped by debug/tools/proposer/make_golden.py. The project's
// rule is that a GPU kernel is validated against an independent implementation and never against
// another GPU port -- two ports agreeing on the same wrong answer is how the gradient-scoring bug
// survived for months. debug/cmd/propcheck reproduces the same reference in pure Go and matches
// torch to 1e-6, so a failure here is the shader, not the reference.
func TestProposerMatchesGolden(t *testing.T) {
	dir := "testdata"
	inPath := filepath.Join(dir, "golden_in.bin")
	outPath := filepath.Join(dir, "golden_out.bin")
	blobPath := os.Getenv("FH6_PROPOSER_BIN")
	if blobPath == "" {
		// make_golden.py writes the export it dumped the pair from right here, so the two can never
		// drift apart. The env override stays for checking a different model against a fresh pair.
		blobPath = filepath.Join(dir, "golden_model.bin")
	}
	for _, p := range []string{inPath, outPath, blobPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("golden pair or weights missing (%s) — run make_golden.py + export_weights.py", p)
		}
	}

	inRaw, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatal(err)
	}
	w := int(binary.LittleEndian.Uint32(inRaw[0:]))
	h := int(binary.LittleEndian.Uint32(inRaw[4:]))
	ch := int(binary.LittleEndian.Uint32(inRaw[8:]))
	if ch != 6 {
		t.Fatalf("golden input has %d channels, want 6", ch)
	}
	plane := w * h
	get := func(c, i int) float32 {
		return math.Float32frombits(binary.LittleEndian.Uint32(inRaw[12+(c*plane+i)*4:]))
	}

	// The shader builds its input from the TARGET and the CANVAS, so the golden planes are fed in
	// through exactly those, sRGB-decoded here because the shader re-encodes them.
	target := make([]float32, plane*4)
	canvas := make([]float32, plane*4)
	for i := 0; i < plane; i++ {
		for c := 0; c < 3; c++ {
			target[i*4+c] = srgbToLinear(get(c, i))
			canvas[i*4+c] = srgbToLinear(get(3+c, i))
		}
		target[i*4+3], canvas[i*4+3] = 1, 1
	}

	g, err := New(target, nil, w, h, 16)
	if err != nil {
		t.Skipf("no Vulkan device: %v", err)
	}
	defer g.Close()
	if err := g.Reset(canvas); err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !g.SetProposer(blob) {
		t.Skip("DLL predates fp_set_proposer — rebuild the shim")
	}
	const progress = 0.42 // must match make_golden.py
	if !g.RunProposer(progress) {
		t.Fatal("fp_run_proposer failed")
	}
	got, dims, ok := g.ProposerMap()
	if !ok {
		t.Fatal("proposal map readback failed")
	}

	ref, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := int(binary.LittleEndian.Uint32(ref[0:]))
	gh := int(binary.LittleEndian.Uint32(ref[4:]))
	gheads := int(binary.LittleEndian.Uint32(ref[8:]))
	// Slot count per head: 6 geometry + alpha + confidence since the learned gate was added.
	gslots := int(binary.LittleEndian.Uint32(ref[12:]))
	if int(dims[0]) != gw || int(dims[1]) != gh || int(dims[2]) != gheads || gslots != 8 {
		t.Fatalf("map is %dx%dx%d, golden is %dx%dx%d x %d slots", dims[0], dims[1], dims[2], gw, gh, gheads, gslots)
	}
	var worst float64
	var worstAt int
	for i := range got {
		want := math.Float32frombits(binary.LittleEndian.Uint32(ref[16+i*4:]))
		if d := math.Abs(float64(got[i] - want)); d > worst {
			worst, worstAt = d, i
		}
	}
	// The tolerance covers float32 accumulation order differing between the GPU and torch; anything
	// larger means the shader computes something else, not that it rounds differently.
	if worst > 3e-3 {
		t.Fatalf("max |shader - torch| = %.6f at %d (tolerance 3e-3)", worst, worstAt)
	}
	t.Logf("proposal map %dx%d x %d heads, max deviation %.6f", gw, gh, gheads, worst)
}

func srgbToLinear(v float32) float32 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return float32(math.Pow(float64((v+0.055)/1.055), 2.4))
}
