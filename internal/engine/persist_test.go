package engine

import "testing"

func TestPersistCtx(t *testing.T) {
	p := newPersistCtx(0.5, 3)

	// Cell 0 stagnates at high error, cell 1 keeps improving, cell 2 is solved.
	grids := [][]float32{
		{10, 10, 0},
		{10, 8, 0},
		{10, 6, 0},
		{10, 4, 0},
	}
	for _, g := range grids {
		p.update(g)
	}
	if p.stag[0] != 4 {
		t.Errorf("stagnant cell counter = %v, want 4", p.stag[0])
	}
	// Cell 1: +1 on the first refresh (no prior), then halved on each improving refresh.
	if p.stag[1] >= 1 {
		t.Errorf("improving cell counter = %v, want < 1 (decayed)", p.stag[1])
	}
	if p.stag[2] != 0 {
		t.Errorf("solved cell counter = %v, want 0", p.stag[2])
	}

	w := p.apply([]float32{10, 10, 10})
	if w[0] <= w[1] {
		t.Errorf("stagnant cell weight %v must exceed improving cell weight %v at equal error", w[0], w[1])
	}
	if w[0] != 10*(1+0.5*4) {
		t.Errorf("stagnant weight = %v, want %v", w[0], 10*(1+0.5*4))
	}

	// Saturation: the upweight is capped so a forever-stuck cell can't starve the rest.
	for i := 0; i < 100; i++ {
		p.update([]float32{10, 10, 0})
	}
	if p.stag[0] != persistCap {
		t.Errorf("counter = %v, want cap %v", p.stag[0], float32(persistCap))
	}
}
