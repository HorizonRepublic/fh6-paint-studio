package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
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
	// Generator toggles (pointer = tri-state: absent -> the NewAppState default). Persisted so a
	// preferred setup survives a restart.
	KeepInside *bool  `json:"keep_inside,omitempty"` // default ON
	SourceRes  *bool  `json:"source_res,omitempty"`  // default OFF: fit the engine at the image's original resolution (max quality, much slower)
	AIFast     *bool  `json:"ai_fast,omitempty"`     // default OFF: neural candidate proposer — same result on ~a quarter of the candidates, a couple of percent more error
	Locale     string `json:"locale,omitempty"`      // chosen UI language tag (e.g. "uk", "pt-BR"); empty = auto-detect from the OS

	CheckUpdates    *bool     `json:"check_updates,omitempty"` // tri-state, nil = on
	LastUpdateCheck time.Time `json:"last_update_check,omitempty"`
	LastSeenVersion string    `json:"last_seen_version,omitempty"`
}

// SoundOn reports the saved sound preference, defaulting to ON when unset.
func (c studioConfig) SoundOn() bool { return c.SoundOnDone == nil || *c.SoundOnDone }

// CheckUpdatesEnabled reports the saved auto-update-check preference, defaulting to ON when unset.
func (c studioConfig) CheckUpdatesEnabled() bool { return c.CheckUpdates == nil || *c.CheckUpdates }

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
