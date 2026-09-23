// Package updates checks GitHub Releases daily and reports newer versions
// (a hint with a download link; nothing is installed automatically).
package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/store"
)

// ReleasesURL lists the project's releases.
var ReleasesURL = "https://api.github.com/repos/DonMikone/CloudWire/releases?per_page=10"

// Publisher pushes events.
type Publisher interface {
	Publish(eventType string, data any)
}

// Status is updates.status's result.
type Status struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	URL       string `json:"url,omitempty"`
	Available bool   `json:"available"`
	CheckedAt *int64 `json:"checkedAt,omitempty"`
}

// Release is one GitHub release.
type Release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Checker performs the checks.
type Checker struct {
	st      *store.Store
	pub     Publisher
	current string
	HTTP    *http.Client

	mu     sync.Mutex
	status Status
}

// New creates a Checker for the running version.
func New(st *store.Store, pub Publisher, current string) *Checker {
	return &Checker{st: st, pub: pub, current: current, HTTP: &http.Client{Timeout: 30 * time.Second},
		status: Status{Current: current}}
}

// Status returns the last result.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Run checks 60 s after start and then every 24 h until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	t := time.NewTimer(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if st, err := c.st.Settings(); err == nil && st.Updates.Check {
			if err := c.Check(ctx); err != nil {
				slog.Info("update check", "err", err)
			}
		}
		t.Reset(24 * time.Hour)
	}
}

// Check queries GitHub now.
func (c *Checker) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ReleasesURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "CloudWire/"+c.current)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	var rels []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels); err != nil {
		return err
	}
	latest, ok := Newest(rels, c.current)
	now := time.Now().UnixMilli()
	st, _ := c.st.Settings()
	c.mu.Lock()
	c.status.CheckedAt = &now
	c.status.Available = false
	if ok {
		v := strings.TrimPrefix(latest.TagName, "v")
		c.status.Latest, c.status.URL = v, latest.HTMLURL
		c.status.Available = v != st.Updates.SkippedVersion
	}
	s := c.status
	c.mu.Unlock()
	if s.Available {
		c.pub.Publish("update.available", map[string]string{"version": s.Latest, "url": s.URL})
	}
	return nil
}

// Newest returns the newest release above current. Drafts are skipped;
// prereleases count only while the running major version is 0.
func Newest(rels []Release, current string) (Release, bool) {
	cur, ok := Parse(current)
	if !ok {
		return Release{}, false
	}
	var best Release
	var bestV Version
	found := false
	for _, r := range rels {
		if r.Draft {
			continue
		}
		v, ok := Parse(r.TagName)
		if !ok {
			continue
		}
		if (r.Prerelease || v.Pre != "") && cur.Major != 0 {
			continue
		}
		if Compare(v, cur) <= 0 {
			continue
		}
		if !found || Compare(v, bestV) > 0 {
			best, bestV, found = r, v, true
		}
	}
	return best, found
}

// Version is a semantic version.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// Parse reads "v1.2.3" or "1.2.3-rc.1" (build metadata ignored).
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, v.Pre = s[:i], s[i+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, true
}

// Compare orders versions per SemVer 2.0 precedence.
func Compare(a, b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	ap, bp := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := range min(len(ap), len(bp)) {
		an, aErr := strconv.Atoi(ap[i])
		bn, bErr := strconv.Atoi(bp[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(ap[i], bp[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(ap) - len(bp))
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
