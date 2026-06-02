//go:build !windows

package inject

import "fh6-paint-studio/internal/model"

// run/locate are unsupported off Windows (the injector needs the live game's process memory).
func (f *FH6) run(shapes []model.Shape, cm CanvasMap) error {
	return ErrNotImplemented
}

func (f *FH6) locate() (string, error) {
	return "", ErrNotImplemented
}

func (f *FH6) dump(indices []int) ([]LayerInfo, error) {
	return nil, ErrNotImplemented
}

func (f *FH6) dumpGroups() ([]GroupInfo, error) {
	return nil, ErrNotImplemented
}

func (f *FH6) probe(needle, where string, max int) ([]ProbeHit, error) {
	return nil, ErrNotImplemented
}
