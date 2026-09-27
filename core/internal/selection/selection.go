// Package selection holds the vocabulary of an Offline Item's Selection:
// sorted paths relative to the item's root (with "/" separators), none of
// which lies below another. [""] is the whole root.
package selection

import (
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/api"
)

// Within reports whether path a equals b or lies below it.
func Within(a, b string) bool {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	return b == "" || a == b || strings.HasPrefix(a, b+"/")
}

// Rebase maps p from below from to below to; false if p is neither from nor
// below it.
func Rebase(p, from, to string) (string, bool) {
	if p == from {
		return to, true
	}
	if strings.HasPrefix(p, from+"/") {
		return to + p[len(from):], true
	}
	return p, false
}

// Join joins a folder and a name; either may be empty.
func Join(dir, name string) string {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return name
	}
	if name == "" {
		return dir
	}
	return dir + "/" + name
}

// Normalize validates, dedupes and sorts selected paths and drops every entry
// that lies below another one.
func Normalize(entries []string) ([]string, error) {
	trimmed := make([]string, 0, len(entries))
	for _, e := range entries {
		t := strings.Trim(e, "/")
		if t == "" {
			return nil, api.Invalid("invalid path %q", e)
		}
		for _, seg := range strings.Split(t, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return nil, api.Invalid("invalid path %q", e)
			}
		}
		trimmed = append(trimmed, t)
	}
	slices.Sort(trimmed)
	trimmed = slices.Compact(trimmed)
	out := make([]string, 0, len(trimmed))
	for _, t := range trimmed {
		below := false
		for _, o := range trimmed {
			if o != t && Within(t, o) {
				below = true
				break
			}
		}
		if !below {
			out = append(out, t)
		}
	}
	return out, nil
}

// Merge returns the union of two Selections.
func Merge(a, b []string) []string {
	if slices.Contains(a, "") || slices.Contains(b, "") {
		return []string{""}
	}
	merged, _ := Normalize(append(slices.Clone(a), b...)) // inputs are valid
	return merged
}

// Covers reports whether rel is an entry or lies below one.
func Covers(entries []string, rel string) bool {
	for _, e := range entries {
		if Within(rel, e) {
			return true
		}
	}
	return false
}

// HasBelow reports whether some entry lies strictly below rel.
func HasBelow(entries []string, rel string) bool {
	for _, e := range entries {
		if (rel == "" && e != "") || strings.HasPrefix(e, rel+"/") {
			return true
		}
	}
	return false
}

// Relevant reports whether rel is synced: the root, a covered path, or a
// parent folder of an entry (partially selected).
func Relevant(entries []string, rel string) bool {
	return rel == "" || Covers(entries, rel) || HasBelow(entries, rel)
}

// Uncovered returns the entries of a that b does not cover.
func Uncovered(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !Covers(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// PartialFolders returns the partially selected folders of a Selection: the
// root and every parent folder of an entry, sorted.
func PartialFolders(entries []string) []string {
	out := []string{""}
	for _, e := range entries {
		for d := path.Dir(e); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// FirstUnselected returns the outermost part of rel that the Selection does
// not sync (a new child of a partially selected folder), or "" if rel is synced.
func FirstUnselected(entries []string, rel string) string {
	for i := 0; i <= len(rel); i++ {
		if i == len(rel) || rel[i] == '/' {
			if p := rel[:i]; !Relevant(entries, p) {
				return p
			}
		}
	}
	return ""
}

// Excluded reports whether a path relative to the storage root matches one
// of the exclude patterns (used to ignore FSEvents).
func Excluded(rel string, excludes []string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	if strings.HasSuffix(base, ".partial") {
		return true // rclone's in-progress downloads
	}
	for _, ex := range excludes {
		ex = strings.TrimPrefix(strings.TrimSpace(ex), "/")
		if strings.HasSuffix(ex, "/**") {
			dir := strings.TrimSuffix(ex, "/**")
			for _, p := range parts[:len(parts)-1] {
				if ok, _ := path.Match(dir, p); ok {
					return true
				}
			}
			if ok, _ := path.Match(dir, base); ok {
				return true
			}
			continue
		}
		if strings.Contains(ex, "/") {
			if ok, _ := path.Match(ex, rel); ok {
				return true
			}
			continue
		}
		for _, p := range parts {
			if ok, _ := path.Match(ex, p); ok {
				return true
			}
		}
	}
	return false
}
