package offline

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// A Selection is a sorted list of paths relative to an item's root (with
// "/" separators), none of which lies below another. [""] is the whole root.

// normalizeSelection validates, dedupes and sorts selected paths and drops
// every entry that lies below another one.
func normalizeSelection(entries []string) ([]string, error) {
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
			if o != t && remoteWithin(t, o) {
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

// selectionFromParams builds the Selection of kind/files request params.
func selectionFromParams(kind string, files []string) ([]string, error) {
	switch kind {
	case "folder":
		return []string{""}, nil
	case "files":
		if len(files) == 0 {
			return nil, api.Invalid("files must not be empty")
		}
		return normalizeSelection(files)
	default:
		return nil, api.Invalid("kind must be folder or files")
	}
}

// selectionOf returns the Selection of an item.
func selectionOf(it store.OfflineItem) []string {
	if it.Kind == "folder" {
		return []string{""}
	}
	return it.Files
}

// applySelection stores a Selection in an item's kind and files.
func applySelection(it *store.OfflineItem, entries []string) {
	if len(entries) == 1 && entries[0] == "" {
		it.Kind, it.Files = "folder", nil
		return
	}
	it.Kind, it.Files = "files", entries
}

// mergeSelections returns the union of two Selections.
func mergeSelections(a, b []string) []string {
	if slices.Contains(a, "") || slices.Contains(b, "") {
		return []string{""}
	}
	merged, _ := normalizeSelection(append(slices.Clone(a), b...)) // inputs are valid
	return merged
}

// covers reports whether rel is an entry or lies below one.
func covers(entries []string, rel string) bool {
	for _, e := range entries {
		if remoteWithin(rel, e) {
			return true
		}
	}
	return false
}

// hasBelow reports whether some entry lies strictly below rel.
func hasBelow(entries []string, rel string) bool {
	for _, e := range entries {
		if (rel == "" && e != "") || strings.HasPrefix(e, rel+"/") {
			return true
		}
	}
	return false
}

// relevant reports whether rel is synced: the root, a covered path, or a
// parent folder of an entry (kept as structure).
func relevant(entries []string, rel string) bool {
	return rel == "" || covers(entries, rel) || hasBelow(entries, rel)
}

// Includes reports whether rel, relative to an item's Storage Location, is
// synced by the item or is a parent folder of a selected path.
func Includes(it store.OfflineItem, rel string) bool {
	return it.Kind != "files" || relevant(it.Files, rel)
}

// changedDirs lists the cloud folders whose Mount listings a sync of it may
// have changed: every covered path and, for a Selection, their parent folders
// (a selected path may be a file).
func changedDirs(it store.OfflineItem) []string {
	var out []string
	for _, c := range CoveredRemote(it) {
		out = append(out, c)
		if it.Kind == "files" {
			parent := ""
			if i := strings.LastIndex(c, "/"); i >= 0 {
				parent = c[:i]
			}
			out = append(out, parent)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// uncovered returns the entries of a that b does not cover.
func uncovered(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !covers(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// deselectedLocal returns the existing local paths below storage that the
// deselected entries gone leave outside the new Selection next.
func deselectedLocal(storage string, gone, next []string) ([]string, error) {
	var out []string
	var walk func(g string) error
	walk = func(g string) error {
		abs := filepath.Join(storage, filepath.FromSlash(g))
		fi, err := os.Lstat(abs)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if covers(next, g) {
			return nil
		}
		if (g != "" && !hasBelow(next, g)) || !fi.IsDir() {
			out = append(out, abs)
			return nil
		}
		children, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := walk(joinRemote(g, c.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	for _, g := range gone {
		if err := walk(g); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// removeEmptyParents removes the now empty parent folders of the removed
// path p up to storage, stopping at a folder the Selection next still syncs
// or at the Storage Location of another item (roots). Best effort.
func removeEmptyParents(storage, p string, next, roots []string) {
	for d := filepath.Dir(p); d != storage && paths.IsWithin(d, storage); d = filepath.Dir(d) {
		rel, err := filepath.Rel(storage, d)
		if err != nil || relevant(next, filepath.ToSlash(rel)) || slices.Contains(roots, d) {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if n := e.Name(); n != ".DS_Store" && n != ".localized" {
				return
			}
		}
		for _, e := range entries {
			_ = os.Remove(filepath.Join(d, e.Name()))
		}
		if os.Remove(d) != nil {
			return
		}
	}
}
