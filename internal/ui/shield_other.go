//go:build !windows

package ui

import "image"

// loadShield is unavailable off Windows.
func loadShield() image.Image { return nil }
