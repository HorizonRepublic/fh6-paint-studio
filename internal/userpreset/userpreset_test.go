package userpreset

import (
	"path/filepath"
	"testing"
	"time"

	"fh6-paint-studio/internal/preset"
)

func TestStoreRoundTrip(t *testing.T) {
	st := Open(t.TempDir())
	when := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	c := preset.DefaultChoices()
	c.Mode = "flat"
	c.Shapes = 2500

	p, err := st.Save(Preset{Name: "My Crisp", Created: when, KeepInside: true, Choices: c})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" {
		t.Fatal("Save did not assign an ID")
	}

	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	got := list[0]
	if got.Name != "My Crisp" || got.Choices.Mode != "flat" || got.Choices.Shapes != 2500 || !got.KeepInside {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	if err := st.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.List(); len(list) != 0 {
		t.Errorf("after delete, List len = %d, want 0", len(list))
	}
}

func TestSaveUniqueIDs(t *testing.T) {
	st := Open(t.TempDir())
	when := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	a, _ := st.Save(Preset{Name: "Same", Created: when})
	b, _ := st.Save(Preset{Name: "Same", Created: when})
	if a.ID == b.ID {
		t.Errorf("two presets with the same name+time share an ID: %q", a.ID)
	}
	if list, _ := st.List(); len(list) != 2 {
		t.Errorf("List len = %d, want 2", len(list))
	}
}

func TestListMissingRootEmpty(t *testing.T) {
	st := Open(filepath.Join(t.TempDir(), "does-not-exist"))
	list, err := st.List()
	if err != nil {
		t.Fatalf("List on missing root: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want empty, got %d", len(list))
	}
}
