package inject

// GameProfile is the vinyl-group editor's in-memory layout. Group offsets locate the live
// layer table; layer offsets are byte positions of each field within a layer struct. The
// values are reverse-engineered from a live build and may need re-checking if the game updates.
type GameProfile struct {
	Key   string
	Label string
	// ProcessNames are the executable names to look for (first match wins).
	ProcessNames []string

	// group struct
	LiveryCountOffset int // uint16 layer count
	LayerTableOffset  int // pointer to the layer pointer-table (array of uint64 layer pointers)

	// layer struct
	PosOffset      int // 2x float32 (x, y)
	ScaleOffset    int // 2x float32 (sx, sy)
	RotationOffset int // float32 (degrees)
	SkewOffset     int // float32
	ColorOffset    int // 4 bytes RGBA
	MaskOffset     int // 1 byte (0/1)
	ShapeIDOffset  int // uint16 shape word (low 16 bits of the game shape code)
	ResourceOffset int // uint64 per-layer geometry resource pointer — READ-ONLY diagnostic (layer dump).
	// NEVER written by the injector: FH6 selects the mesh from the shape-word; aliasing this pointer
	// across layers corrupts FH6's per-layer ownership and crashes the game on free (FHE01).
}

// FH6Profile returns the Forza Horizon 6 memory profile.
//
// These offsets + the Word* shape codes below + the RTTI type-descriptor names in
// locate_rtti_windows.go (cliveryGroupRTTINames) are the THREE build-specific "code tables". If a
// future game build moves a field or renames the class, update them here / there; the count-scan
// locator keeps working as a fallback and re-learns the vtable. See locateTable for the chain.
func FH6Profile() GameProfile {
	return GameProfile{
		Key:          "fh6",
		Label:        "Forza Horizon 6",
		ProcessNames: []string{"ForzaHorizon6.exe", "ForzaHorizon6-Win64-Shipping.exe", "ForzaHorizon5.exe"},

		LiveryCountOffset: 0x5A,
		LayerTableOffset:  0x78,

		PosOffset:      0x18,
		ScaleOffset:    0x28,
		RotationOffset: 0x50,
		SkewOffset:     0x70,
		ColorOffset:    0x74,
		MaskOffset:     0x78,
		ShapeIDOffset:  0x7A,
		ResourceOffset: 0xA8,
	}
}

// Page-1 primitive shape words: the low 16 bits of the in-game shape code, written at
// ShapeIDOffset as a little-endian uint16. The full codes are 0x100000 | word; only the
// low word is stored in the layer.
const (
	WordSquare       uint16 = 1048677 & 0xFFFF // 0x0065 Square
	WordCircle       uint16 = 1048678 & 0xFFFF // 0x0066 Circle
	WordTriangle     uint16 = 0x0068           // 0x0068 Triangle (verified against the live build)
	WordCircleBorder uint16 = 1048688 & 0xFFFF // 0x0070 Circle Border
	WordEllipse      uint16 = 1048712 & 0xFFFF // 0x0088 Ellipse (NB: renders as a CRESCENT — use Circle+scale)
)
