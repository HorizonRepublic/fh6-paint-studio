package inject_test

import (
	"errors"
	"testing"

	"fh6-paint-studio/internal/inject"
)

func TestStub(t *testing.T) {
	var inj inject.Injector = inject.Stub{}
	if inj.Name() == "" {
		t.Error("Name should be non-empty")
	}
	if inj.Available() {
		t.Error("stub should report unavailable")
	}
	if err := inj.Inject(nil, 100, 100); !errors.Is(err, inject.ErrNotImplemented) {
		t.Errorf("Inject err = %v, want ErrNotImplemented", err)
	}
}
