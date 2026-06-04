//go:build updatecheck

package main

import (
	"context"
	"sync"
	"time"

	"gioui.org/app"

	"fh6-paint-studio/internal/ui"
	"fh6-paint-studio/internal/update"
)

const updateCheckEnabled = true

type updateResult struct {
	rel update.Release
	ok  bool
	err error
}

type updateHolder struct {
	mu    sync.Mutex
	ready bool
	res   updateResult
}

func (h *updateHolder) put(r updateResult) {
	h.mu.Lock()
	h.res, h.ready = r, true
	h.mu.Unlock()
}

func (h *updateHolder) take() (updateResult, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready {
		return updateResult{}, false
	}
	h.ready = false
	return h.res, true
}

type updater struct {
	checker *update.Checker
	holder  updateHolder
}

func newUpdater() *updater { return &updater{checker: update.Default()} }

func (u *updater) kick(w *app.Window) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		rel, ok, err := u.checker.Latest(ctx)
		u.holder.put(updateResult{rel: rel, ok: ok, err: err})
		w.Invalidate()
	}()
}

// startup kicks a throttled auto-check; dev builds never match a release, so skip them.
func (u *updater) startup(version string, auto bool, last time.Time, w *app.Window) {
	if version != "dev" && auto && time.Since(last) >= 24*time.Hour {
		u.kick(w)
	}
}

// drain applies a finished check to st; on success returns the check time + true (caller persists it).
func (u *updater) drain(st *ui.AppState, version string) (time.Time, bool) {
	r, ok := u.holder.take()
	if !ok {
		return time.Time{}, false
	}
	if r.err != nil {
		st.AppendLog("update check: " + r.err.Error())
		st.UpdateStatus = "Couldn't check for updates"
		return time.Time{}, false
	}
	if r.ok && update.IsNewer(version, r.rel.Version) {
		st.Update = &ui.UpdateInfo{Version: r.rel.Tag, Notes: r.rel.Notes, URL: r.rel.URL}
		st.UpdateStatus = ""
	} else {
		st.Update = nil
		st.UpdateStatus = "You're up to date"
	}
	return time.Now(), true
}
