// Package offline keeps Offline Items as real local files, two-way synced
// with rclone bisync in background-priority worker processes.
package offline

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// MaxDeletePercent is the Mass-Delete Guard threshold.
const MaxDeletePercent = 50

// ConflictSuffix returns bisync's conflictSuffix. Path1 (local) always wins
// and keeps its name; the cloud version is saved as
// "<name>.<label> 2006-01-02 1504.<ext>". The first element only has to
// differ from the second so bisync does not add numbers.
func ConflictSuffix(label string) string {
	return "lokal," + label + " {2006-01-02 1504}"
}

// ConflictMarker is the text that identifies a Conflict Copy in a file name.
func ConflictMarker(label string) string {
	return "." + label + " "
}

// BisyncParams builds the sync/bisync rc parameters of an item. force is
// set only after the user confirmed a Mass-Delete Guard stop.
func BisyncParams(it store.OfflineItem, remote string, p paths.Paths, label string, force bool) map[string]any {
	cfg := map[string]any{}
	for k, v := range it.Advanced {
		cfg[k] = v
	}
	cfg["SuffixKeepExtension"] = true
	params := map[string]any{
		"path1":              it.StoragePath,
		"path2":              remote + ":" + it.RemotePath,
		"workdir":            p.BisyncWorkdir(it.ID),
		"filtersFile":        p.FiltersFile(it.ID),
		"resilient":          true,
		"recover":            true,
		"maxLock":            "2m",
		"maxDelete":          MaxDeletePercent,
		"createEmptySrcDirs": true,
		"compare":            "size,modtime",
		"conflictResolve":    "path1",
		"conflictLoser":      "pathname",
		"conflictSuffix":     ConflictSuffix(label),
		"_config":            cfg,
	}
	if it.NeedsResync {
		// resyncMode implies a resync in rclone, so it is only sent when one is due.
		params["resync"] = true
		params["resyncMode"] = "newer"
	}
	if force {
		params["force"] = true
	}
	return params
}

// globEscape escapes rclone filter glob metacharacters.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FilterLines returns the bisync filters file content of an item.
func FilterLines(it store.OfflineItem) []string {
	var lines []string
	for _, ex := range it.Excludes {
		ex = strings.TrimSpace(ex)
		if ex == "" || strings.ContainsAny(ex, "\n\r") {
			continue
		}
		lines = append(lines, "- "+ex)
	}
	if it.Kind == "files" {
		for _, f := range it.Files {
			lines = append(lines, "+ /"+globEscape(f))
		}
		lines = append(lines, "- **")
	}
	return lines
}

// WriteFilters writes the filters file of an item.
func WriteFilters(p paths.Paths, it store.OfflineItem) error {
	content := strings.Join(FilterLines(it), "\n") + "\n"
	f := p.FiltersFile(it.ID)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(content), 0o600)
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

// itemName is a short display name.
func itemName(it store.OfflineItem) string {
	if it.Kind == "files" && len(it.Files) == 1 {
		return it.Files[0]
	}
	if it.RemotePath != "" {
		return path.Base(it.RemotePath)
	}
	return filepath.Base(it.StoragePath)
}

// sanitizeComponent makes a Connection name usable as a folder name.
func sanitizeComponent(s string) string {
	s = strings.NewReplacer("/", "-", ":", "-").Replace(strings.TrimSpace(s))
	if s == "" || s == "." || s == ".." {
		return "Cloud"
	}
	return s
}

// DefaultStoragePath is <baseFolder>/<connection name>/<remote path>.
func DefaultStoragePath(baseFolder, connName, remotePath string) string {
	p := filepath.Join(baseFolder, sanitizeComponent(connName))
	for _, part := range strings.Split(strings.Trim(remotePath, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			continue
		}
		p = filepath.Join(p, strings.ReplaceAll(part, ":", "-"))
	}
	return p
}

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
