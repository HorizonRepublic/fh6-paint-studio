package main

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFrameRecordsTheFittedRectangle(t *testing.T) {
	out := filepath.Join(t.TempDir(), "geometry.json")
	writeFrame(out, image.Rect(90, 0, 785, 563), 695, 563)

	b, err := os.ReadFile(out + ".frame.json")
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	var got frameInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("sidecar is not valid json: %v", err)
	}
	want := frameInfo{CropX: 90, CropY: 0, CropW: 695, CropH: 563, RenderW: 695, RenderH: 563}
	if got != want {
		t.Errorf("frame = %+v, want %+v", got, want)
	}
}

// An empty rect means no crop was applied, so there is nothing to disambiguate and no file should
// appear — a sidecar full of zeroes would be worse than none, since a reader cannot tell it apart
// from a genuine zero-origin crop.
func TestWriteFrameSkipsWhenThereIsNoCrop(t *testing.T) {
	out := filepath.Join(t.TempDir(), "geometry.json")
	writeFrame(out, image.Rectangle{}, 100, 100)
	if _, err := os.Stat(out + ".frame.json"); !os.IsNotExist(err) {
		t.Errorf("sidecar written for an empty rect (stat err = %v)", err)
	}
}
