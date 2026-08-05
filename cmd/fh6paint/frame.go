package main

import (
	"encoding/json"
	"image"
	"os"

	"fh6-paint-studio/internal/applog"
)

// frameInfo records which part of the source image the shapes were fitted in, and at what size
// they were rendered. Written as a SIDECAR next to the geometry rather than inside it: the
// geometry JSON is what the in-game importer consumes, and an extra top-level key there would be
// a compatibility gamble taken purely for the convenience of offline tooling.
//
// The problem it solves is real and has cost measurements twice: the engine auto-crops uniform
// margins, so the render's aspect often is not the source file's. A harness that resizes one onto
// the other without checking silently compares misaligned images and reports confident nonsense.
type frameInfo struct {
	// Crop is the source rectangle the fit used, in the SOURCE file's pixel coordinates.
	CropX int `json:"crop_x"`
	CropY int `json:"crop_y"`
	CropW int `json:"crop_w"`
	CropH int `json:"crop_h"`
	// Render is the coordinate space the shapes themselves live in.
	RenderW int `json:"render_w"`
	RenderH int `json:"render_h"`
}

// writeFrame emits "<out>.frame.json". A zero rect means no crop was applied (plain -autocrop=false
// load), in which case there is nothing to disambiguate and the file is skipped. Failure is logged,
// never fatal — the geometry is the deliverable and a missing sidecar must not lose a run.
func writeFrame(out string, rect image.Rectangle, renderW, renderH int) {
	if rect.Empty() {
		return
	}
	b, err := json.Marshal(frameInfo{
		CropX: rect.Min.X, CropY: rect.Min.Y, CropW: rect.Dx(), CropH: rect.Dy(),
		RenderW: renderW, RenderH: renderH,
	})
	if err != nil {
		applog.Printf("frame: marshal failed: %v", err)
		return
	}
	if err := os.WriteFile(out+".frame.json", b, 0o644); err != nil {
		applog.Printf("frame: write failed: %v", err)
	}
}
