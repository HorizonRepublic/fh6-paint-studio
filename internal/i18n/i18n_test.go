package i18n

import "testing"

func TestTFallsBackToEnglish(t *testing.T) {
	SetLocale("uk") // no uk.json yet -> must fall back to en
	defer SetLocale("en")
	if got, want := T("shapes.used"), "Used shapes"; got != want {
		t.Fatalf("T(shapes.used)=%q, want %q (en fallback)", got, want)
	}
}

func TestTUnknownKeyReturnsKey(t *testing.T) {
	if got := T("does.not.exist"); got != "does.not.exist" {
		t.Fatalf("unknown key = %q, want the key itself", got)
	}
}

func TestTInterpolates(t *testing.T) {
	if got := T("run.shapes_n", 7); got != "7 shapes" {
		t.Fatalf("T(run.shapes_n,7)=%q, want %q", got, "7 shapes")
	}
}

func TestMatch(t *testing.T) {
	cases := map[string]string{
		"uk-UA": "uk", "de-DE": "de", "pt-PT": "pt-BR", "zh-CN": "zh-CN",
		"zh-Hans": "zh-CN", "ja-JP": "ja", "en-GB": "en",
	}
	for in, want := range cases {
		if got, ok := Match(in); !ok || got != want {
			t.Errorf("Match(%q)=(%q,%v), want (%q,true)", in, got, ok, want)
		}
	}
	if got, ok := Match("ru-RU"); ok {
		t.Errorf("Match(ru-RU)=(%q,true), want no-match (Russian is not supported)", got)
	}
}
