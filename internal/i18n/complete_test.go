package i18n

import "testing"

// TestCatalogsComplete asserts every non-English catalog defines exactly the English key set: no
// missing keys (would render English at runtime) and no stray keys (a typo or a removed string).
func TestCatalogsComplete(t *testing.T) {
	en := catalogs["en"]
	if len(en) == 0 {
		t.Fatal("en.json is empty")
	}
	for _, l := range locales {
		if l.Tag == "en" {
			continue
		}
		cat := catalogs[l.Tag]
		for key := range en {
			if v, ok := cat[key]; !ok || v == "" {
				t.Errorf("%s.json missing key %q", l.Tag, key)
			}
		}
		for key := range cat {
			if _, ok := en[key]; !ok {
				t.Errorf("%s.json has stray key %q (not in en.json)", l.Tag, key)
			}
		}
	}
}

// TestPlaceholdersMatchEnglish guards against a %-verb count mismatch that would corrupt or crash
// fmt.Sprintf at runtime (e.g. a translation that dropped the "%d").
func TestPlaceholdersMatchEnglish(t *testing.T) {
	count := func(s string) int { // count %verbs, ignoring escaped %%
		n, b := 0, []byte(s)
		for i := 0; i < len(b); i++ {
			if b[i] == '%' {
				if i+1 < len(b) && b[i+1] == '%' {
					i++
					continue
				}
				n++
			}
		}
		return n
	}
	en := catalogs["en"]
	for _, l := range locales {
		for key, src := range en {
			if tr, ok := catalogs[l.Tag][key]; ok && count(tr) != count(src) {
				t.Errorf("%s.json key %q has %d placeholders, en has %d", l.Tag, key, count(tr), count(src))
			}
		}
	}
}
