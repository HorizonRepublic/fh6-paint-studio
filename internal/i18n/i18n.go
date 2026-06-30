// Package i18n holds the studio's UI string catalogs and the active-locale lookup. It is a leaf
// package (imported by internal/ui and cmd/studio); it imports only x/text and x/sys. Gio re-renders
// every frame, so changing the active locale with SetLocale updates the whole UI on the next frame.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed locales/*.json
var catalogsFS embed.FS

// Locale is a supported UI language: its BCP-47 tag and the endonym shown in the picker.
type Locale struct {
	Tag     string
	Endonym string
}

// locales is the ordered list shown in the picker. The first entry (English) is the source language
// and the fallback for any missing key.
var locales = []Locale{
	{"en", "English"},
	{"uk", "Українська"},
	{"de", "Deutsch"},
	{"es", "Español"},
	{"pt-BR", "Português (BR)"},
	{"fr", "Français"},
	{"pl", "Polski"},
	{"it", "Italiano"},
	{"tr", "Türkçe"},
	{"zh-CN", "中文(简体)"},
	{"ja", "日本語"},
	{"ko", "한국어"},
}

var (
	mu       sync.RWMutex
	current  = "en"
	catalogs = map[string]map[string]string{}
	fallback = map[string]string{}
)

func init() {
	for _, l := range locales {
		m := map[string]string{}
		if b, err := catalogsFS.ReadFile("locales/" + l.Tag + ".json"); err == nil {
			_ = json.Unmarshal(b, &m)
		}
		catalogs[l.Tag] = m
	}
	fallback = catalogs["en"]
}

// Available returns the supported locales in display order.
func Available() []Locale { return locales }

// Current returns the active locale tag.
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// SetLocale switches the active language to tag when supported; unknown tags are ignored.
func SetLocale(tag string) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := catalogs[tag]; ok {
		current = tag
	}
}

// EndonymOf returns the native name for a tag, or the tag itself if unknown.
func EndonymOf(tag string) string {
	for _, l := range locales {
		if l.Tag == tag {
			return l.Endonym
		}
	}
	return tag
}

// TagForEndonym maps a picker endonym back to its tag, or "" if unknown.
func TagForEndonym(endonym string) string {
	for _, l := range locales {
		if l.Endonym == endonym {
			return l.Tag
		}
	}
	return ""
}

// T returns the translated string for key in the active locale, falling back to English and finally to
// the key itself. When args are supplied they are applied with fmt.Sprintf (the catalog value is the
// format string, e.g. "%d shapes").
func T(key string, args ...any) string {
	mu.RLock()
	s, ok := catalogs[current][key]
	if !ok || s == "" {
		s, ok = fallback[key]
	}
	mu.RUnlock()
	if !ok || s == "" {
		s = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
