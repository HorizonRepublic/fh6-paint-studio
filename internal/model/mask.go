package model

// KindMask — the FH6 native-dictionary primitive. A mask shape is a captured silhouette ("word")
// placed by the full affine P = [cx, cy, Hx, Hy, rotDeg, skew] (Hx,Hy = full screen extents in px;
// slot 5 = skew). One ShapeKind per word: words register at KindMaskBase+i (a DI registry), so the
// raster/coverage code keeps its (kind, p) signature and resolves the silhouette by kind alone — no
// Candidate field, no per-call-site churn. native_w/h are kept only to build/export a candidate from
// the in-game sx,sy scale (Hx = sx·native_w); the raster math needs only Hx,Hy + the mask grid.
//
// The registry is populated at init by the maskbank package (which owns the embedded coverage grids),
// in a fixed order, so kind assignment is deterministic. Render-faithful path only — masks are not yet
// emitted by the generator, so existing behaviour is unchanged (KindMask kinds never arise until the
// bank registers them).
const KindMaskBase ShapeKind = 64

var (
	maskWords  []uint16     // index i -> kind KindMaskBase+i
	maskNative [][2]float32 // parallel: native_w, native_h (capture units)
	maskIndex  = map[uint16]int{}
)

// RegisterMaskWord registers a dictionary word and returns its ShapeKind (KindMaskBase+i). Idempotent:
// re-registering a known word returns its existing kind (native size is not overwritten).
func RegisterMaskWord(word uint16, nativeW, nativeH float32) ShapeKind {
	if i, ok := maskIndex[word]; ok {
		return KindMaskBase + ShapeKind(i)
	}
	i := len(maskWords)
	maskWords = append(maskWords, word)
	maskNative = append(maskNative, [2]float32{nativeW, nativeH})
	maskIndex[word] = i
	return KindMaskBase + ShapeKind(i)
}

// IsMask reports whether a kind is a registered mask word.
func IsMask(kind ShapeKind) bool {
	return kind >= KindMaskBase && int(kind-KindMaskBase) < len(maskWords)
}

// MaskKind returns the kind registered for a word.
func MaskKind(word uint16) (ShapeKind, bool) {
	if i, ok := maskIndex[word]; ok {
		return KindMaskBase + ShapeKind(i), true
	}
	return 0, false
}

// MaskWord returns the dictionary word for a mask kind.
func MaskWord(kind ShapeKind) (uint16, bool) {
	if !IsMask(kind) {
		return 0, false
	}
	return maskWords[kind-KindMaskBase], true
}

// MaskNative returns the captured native size (w,h) for a mask kind.
func MaskNative(kind ShapeKind) (w, h float32, ok bool) {
	if !IsMask(kind) {
		return 0, 0, false
	}
	n := maskNative[kind-KindMaskBase]
	return n[0], n[1], true
}
