package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.2.0", "0.3.0", true},
		{"0.2.0", "v0.3.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.3.0", "0.2.0", false},
		{"0.2.0", "0.2.1", true},
		{"0.2.0", "1.0.0", true},
		{"dev", "9.9.9", false},
		{"", "1.0.0", false},
		{"0.3.0", "0.3.0-rc.1", false},
		{"0.3.0-rc.1", "0.3.0", true},
		{"0.2.0", "garbage", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestLatest(t *testing.T) {
	const body = `{"tag_name":"v0.3.0","name":"v0.3.0","body":"- faster generation\n- gaussian mode",` +
		`"html_url":"https://github.com/HorizonRepublic/fh6-paint-studio/releases/tag/v0.3.0",` +
		`"draft":false,"prerelease":false,"published_at":"2026-06-01T12:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request missing User-Agent header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := &Checker{Client: srv.Client(), APIURL: srv.URL}
	rel, ok, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if rel.Version != "0.3.0" || rel.Tag != "v0.3.0" {
		t.Errorf("Version/Tag = %q/%q, want 0.3.0/v0.3.0", rel.Version, rel.Tag)
	}
	if rel.URL == "" || rel.Notes == "" {
		t.Errorf("URL/Notes empty: %+v", rel)
	}
}

func TestLatestNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Checker{Client: srv.Client(), APIURL: srv.URL}
	_, ok, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest 404: unexpected error %v", err)
	}
	if ok {
		t.Fatal("ok = true on 404, want false")
	}
}
