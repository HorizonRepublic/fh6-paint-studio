//go:build !windows

package inject

// calibWrite is unsupported off Windows (needs the live game's process memory).
func (f *FH6) calibWrite(layers []CalibLayer) error { return ErrNotImplemented }
