package ui

import (
	"testing"

	"fh6-paint-studio/internal/library"
)

func TestEntryCountLabel(t *testing.T) {
	if got := entryCountLabel(library.Entry{Kind: library.KindDecal, Shapes: 1530}); got != "1530 shapes" {
		t.Fatalf("decal label = %q", got)
	}
}
