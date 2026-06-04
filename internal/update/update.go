// Package update reports whether a newer release exists on GitHub. It only reads public release
// metadata — no token, nothing sent.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const repoAPI = "https://api.github.com/repos/HorizonRepublic/fh6-paint-studio/releases/latest"

type Release struct {
	Version     string // normalized core ("0.3.0"), for comparison
	Tag         string // raw tag ("v0.3.0"), for display
	Name        string
	Notes       string // release body = the changelog
	URL         string
	PublishedAt time.Time
}

// Checker has injectable URL/Client so tests run against an httptest.Server.
type Checker struct {
	Client *http.Client
	APIURL string
}

func Default() *Checker {
	return &Checker{Client: &http.Client{Timeout: 10 * time.Second}, APIURL: repoAPI}
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
}

// Latest fetches the newest published release. ok is false (nil error) when the repo has no release (404).
func (c *Checker) Latest(ctx context.Context) (rel Release, ok bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIURL, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fh6-paint-studio") // GitHub rejects a request with no UA

	resp, err := c.Client.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Release{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}

	var g ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return Release{}, false, err
	}
	core, _ := splitCore(g.TagName)
	return Release{
		Version:     fmt.Sprintf("%d.%d.%d", core[0], core[1], core[2]),
		Tag:         g.TagName,
		Name:        g.Name,
		Notes:       strings.TrimSpace(g.Body),
		URL:         g.HTMLURL,
		PublishedAt: g.PublishedAt,
	}, true, nil
}

// IsNewer reports whether latest outranks current. A "dev"/empty current never matches a release.
func IsNewer(current, latest string) bool {
	if current == "" || current == "dev" {
		return false
	}
	return compare(latest, current) > 0
}

func compare(a, b string) int {
	ca, pa := splitCore(a)
	cb, pb := splitCore(b)
	for i := 0; i < 3; i++ {
		if ca[i] != cb[i] {
			if ca[i] > cb[i] {
				return 1
			}
			return -1
		}
	}
	switch { // equal cores: a release outranks its pre-release
	case !pa && pb:
		return 1
	case pa && !pb:
		return -1
	default:
		return 0
	}
}

// splitCore parses "vX.Y.Z-rc+meta" into its [3]int core and whether a pre-release suffix is present.
func splitCore(v string) (core [3]int, pre bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = true
		v = v[:i]
	}
	for i, part := range strings.SplitN(v, ".", 3) {
		n, _ := strconv.Atoi(strings.TrimSpace(part))
		core[i] = n
	}
	return core, pre
}
