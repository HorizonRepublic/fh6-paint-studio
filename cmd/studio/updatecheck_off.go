//go:build !updatecheck

// The default/release build excludes the update check: no internal/update import means no net/http
// client and no api.github.com callout in the binary. Build with -tags updatecheck to re-enable it.

package main

import (
	"time"

	"gioui.org/app"

	"fh6-paint-studio/internal/ui"
)

const updateCheckEnabled = false

type updater struct{}

func newUpdater() *updater { return &updater{} }

func (u *updater) kick(*app.Window)                             {}
func (u *updater) startup(string, bool, time.Time, *app.Window) {}
func (u *updater) drain(*ui.AppState, string) (time.Time, bool) { return time.Time{}, false }
