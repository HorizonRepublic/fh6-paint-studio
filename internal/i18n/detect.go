package i18n

import "golang.org/x/text/language"

// Match picks the best supported locale tag for the given preference tags (e.g. the OS UI languages,
// best first). It returns ("en", false) when no preference is a confident match — so callers can keep
// English rather than guess.
func Match(prefs ...string) (string, bool) {
	supported := make([]language.Tag, len(locales))
	for i, l := range locales {
		supported[i] = language.Make(l.Tag)
	}
	matcher := language.NewMatcher(supported)
	want := make([]language.Tag, 0, len(prefs))
	for _, p := range prefs {
		if t, err := language.Parse(p); err == nil {
			want = append(want, t)
		}
	}
	if len(want) == 0 {
		return "en", false
	}
	_, idx, conf := matcher.Match(want...)
	if conf != language.Exact && conf != language.High {
		return "en", false
	}
	return locales[idx].Tag, true
}

// Detect returns the best supported locale for the host's OS UI language, or ("en", false).
func Detect() (string, bool) { return Match(osLanguages()...) }
