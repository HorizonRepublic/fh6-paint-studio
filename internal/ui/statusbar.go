package ui

import (
	"fh6-paint-studio/internal/i18n"
)

func (s *AppState) statusText() string {
	if s.Toast != "" {
		return s.Toast
	}
	switch s.Phase {
	case PhaseRunning:
		return i18n.T("status.generating")
	case PhaseDone:
		return i18n.T("status.done", group(s.Stats.Shapes), fmtDur(s.Stats.Elapsed))
	case PhaseError:
		return i18n.T("status.error")
	default:
		if s.Source == nil {
			return i18n.T("status.ready_open")
		}
		return i18n.T("status.ready")
	}
}
