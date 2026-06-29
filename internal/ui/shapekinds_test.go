package ui

import "testing"

// The "Used shapes" chips (top of Advanced) are a second view on KindsSel. All on (the default) must
// emit an EMPTY kinds override so the mode keeps its own default kind mix — byte-identical to before the
// feature — and a restriction must survive Advanced being collapsed (a curated control, not expert-only).
func TestShapeKindsDefaultEmitsNoOverride(t *testing.T) {
	s := NewAppState(NewTheme())
	if got := s.Choices().Kinds; got != "" {
		t.Fatalf("default kinds override = %q, want \"\" (mode default mix)", got)
	}
}

func TestShapeKindsToggleKeepsAtLeastOne(t *testing.T) {
	s := NewAppState(NewTheme())

	// Restricting the set emits an explicit override even with Advanced closed (Expert off).
	s.toggleShapeKind("rectangle")
	s.toggleShapeKind("triangle")
	if got, want := s.Choices().Kinds, "ellipse"; got != want {
		t.Fatalf("after disabling two, kinds = %q, want %q", got, want)
	}

	// The last enabled kind cannot be turned off — the engine needs at least one primitive.
	s.toggleShapeKind("ellipse")
	if got, want := s.Choices().Kinds, "ellipse"; got != want {
		t.Fatalf("last kind must stay on, kinds = %q, want %q", got, want)
	}
	if n := s.KindsSel.OnCount(); n != 1 {
		t.Fatalf("OnCount = %d, want 1", n)
	}

	// Re-enabling restores it; ValueCSV reports in engine (preset.KindNames) order, not display order.
	s.toggleShapeKind("triangle")
	if got, want := s.Choices().Kinds, "ellipse,triangle"; got != want {
		t.Fatalf("after re-enabling triangle, kinds = %q, want %q", got, want)
	}
}
