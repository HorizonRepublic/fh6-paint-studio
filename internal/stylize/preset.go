package stylize

import "encoding/json"

// Stage is one engine invocation within a preset: the engine name plus its opaque per-engine config
// (decoded by that engine's Factory). Stages run in order; later stages composite on top.
type Stage struct {
	Engine string          `json:"engine"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Preset is a recipe — which engines run, in what order, with what config. A style (anime, lineart,
// poster, bokeh) is just a registered Preset; nothing about one constrains another.
type Preset struct {
	Name   string        `json:"name"`
	Stages []Stage       `json:"stages"`
	Smooth *SmoothConfig `json:"smooth,omitempty"` // optional edge-preserving pre-smooth (nil = none)
}

var presetRegistry = map[string]Preset{}

// RegisterPreset adds a preset recipe under its name (Open/Closed: register, never edit existing).
func RegisterPreset(p Preset) { presetRegistry[p.Name] = p }

// Presets returns the registered preset names (for the studio picker / CLI listing).
func Presets() []string {
	names := make([]string, 0, len(presetRegistry))
	for n := range presetRegistry {
		names = append(names, n)
	}
	return names
}
