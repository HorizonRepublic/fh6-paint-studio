package engine

import "testing"

func TestDetailBiasSchedule(t *testing.T) {
	// disabled
	if got := detailBias(0.8, 0.6, 0); got != 0 {
		t.Fatalf("strength 0 must give 0, got %v", got)
	}
	// before start
	if got := detailBias(0.5, 0.6, 0.4); got != 0 {
		t.Fatalf("before start must give 0, got %v", got)
	}
	// at start → 0, ramps to full at progress 1
	if got := detailBias(0.6, 0.6, 0.4); got != 0 {
		t.Fatalf("at start must give 0, got %v", got)
	}
	if got := detailBias(1.0, 0.6, 0.4); got != 0.4 {
		t.Fatalf("at progress 1 must give full strength 0.4, got %v", got)
	}
	// midway (progress 0.8, start 0.6) → t=0.5 → 0.2
	if got := detailBias(0.8, 0.6, 0.4); got < 0.19 || got > 0.21 {
		t.Fatalf("midway must give ~0.2, got %v", got)
	}
	// start>=1 guard
	if got := detailBias(1.0, 1.0, 0.4); got != 0 {
		t.Fatalf("start>=1 must give 0, got %v", got)
	}
}

func TestBlendDetailGrid(t *testing.T) {
	err := []float32{10, 0, 5, 8}
	detail := []float32{1, 1, 0, 0.5}
	out := blendDetailGrid(err, detail, 0.5)
	// cell0: 10*(1+0.5*1)=15 ; cell1: 0 stays 0 ; cell2: 5*(1+0)=5 ; cell3: 8*(1+0.25)=10
	want := []float32{15, 0, 5, 10}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("cell %d: got %v want %v", i, out[i], want[i])
		}
	}
	// fully-solved detailed cell stays 0 (no shapes wasted on perfect detail)
	if out[1] != 0 {
		t.Fatalf("zero-error cell must stay 0, got %v", out[1])
	}
	// s<=0 returns the same slice (no copy)
	if got := blendDetailGrid(err, detail, 0); &got[0] != &err[0] {
		t.Fatal("s<=0 should return err unchanged (same backing array)")
	}
	// size mismatch returns err unchanged
	if got := blendDetailGrid(err, detail[:2], 0.5); &got[0] != &err[0] {
		t.Fatal("size mismatch should return err unchanged")
	}
	// negative error clamped to 0 before scaling
	out2 := blendDetailGrid([]float32{-3}, []float32{1}, 1)
	if out2[0] != 0 {
		t.Fatalf("negative error must clamp to 0, got %v", out2[0])
	}
}
