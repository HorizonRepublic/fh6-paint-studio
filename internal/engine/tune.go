package engine

// Tunable constants for the greedy generator — the search-radius anneal, the hill-climb step sizes,
// and the fall-back defaults used when the matching Options field is left unset (the preset layer
// sets most of them per content mode). Gathered here so the generator's knobs live in one
// discoverable place rather than as bare literals scattered through Run.
const (
	// annealMaxR per-shape radius schedule: maxR = diag*(annealRadiusStart - annealRadiusRange*
	// progress^annealRadiusExp), floored at annealRadiusFloor px — a coarse base first, fine detail
	// later. Shared verbatim by the host generator and the on-device search so they stay in lockstep.
	annealRadiusStart = 0.25
	annealRadiusRange = 0.20
	annealRadiusExp   = 1.5
	annealRadiusFloor = 4

	// Hill-climb mutation steps as a fraction of the image diagonal (floored at mutateStepFloor px):
	// how far a candidate's centre / radius may jump per mutation round.
	mutateMoveFrac   = 0.012
	mutateRadiusFrac = 0.010
	mutateStepFloor  = 2

	// Fall-back defaults applied when the matching Options field is left unset (0 / -1):
	defaultAlphaMin      = 0.3  // candidate alpha floor (photo); the preset overrides per mode
	defaultDetailStart   = 0.6  // progress at which detail-weighted sampling engages
	defaultBoundaryPad   = 16   // boundary-aware radius padding (px)
	defaultBoundaryStart = 0.42 // progress at which the boundary radius cap engages
	defaultMomentRefine  = 2048 // moment-seeding refine-pool size (quality-neutral knee vs the 50k search)
	defaultMomentSeeds   = 16   // moment-seeding error-sampled centres per shape

	// boundaryEdgeThreshold is the luma-edge magnitude that counts as a boundary in
	// metric.BoundaryDistance (the field that caps boundary-aware candidate radii).
	boundaryEdgeThreshold = 0.18
)
