package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// studioConfig persists lightweight UI preferences between sessions. It lives next to the generation
// library under ~/FH6PaintStudio/. Window POSITION is not persisted — Gio does not expose window
// placement; only the size is restored.
type studioConfig struct {
	WindowW     int    `json:"window_w"`                // last window width in dp
	WindowH     int    `json:"window_h"`                // last window height in dp
	SoundOnDone *bool  `json:"sound_on_done,omitempty"` // play a chime when a generation finishes; nil/absent = on (default)
	Preset      string `json:"preset,omitempty"`        // last content preset (anime|photo|flat)
	Budget      int    `json:"budget,omitempty"`        // last shape budget
	// Generator toggles (pointer = tri-state: absent → the NewAppState default). Persisted so a
	// preferred setup survives a restart.
	KeepInside *bool `json:"keep_inside,omitempty"` // default ON
	Polish     *bool `json:"polish,omitempty"`      // default ON
	Economy    *bool `json:"economy,omitempty"`     // default OFF
	Standout   *bool `json:"standout,omitempty"`    // default OFF
}

// SoundOn reports the saved sound preference, defaulting to ON when unset.
func (c studioConfig) SoundOn() bool { return c.SoundOnDone == nil || *c.SoundOnDone }

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "FH6PaintStudio", "studio.json"), nil
}

// loadConfig reads the saved preferences; a missing or unreadable file yields the zero value.
func loadConfig() studioConfig {
	var c studioConfig
	p, err := configPath()
	if err != nil {
		return c
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

// saveConfig writes the preferences best-effort (errors are ignored — it is a convenience, not state).
func saveConfig(c studioConfig) {
	p, err := configPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}
