package identity

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/selection"
)

// ScanLocal lists every folder and file below root (item-relative, with the
// exact names on disk, so case-only renames show) except excluded paths and
// the Storage Locations of other items (otherRoots).
func ScanLocal(root string, excludes, otherRoots []string) (map[string]LocalEntry, error) {
	root = filepath.Clean(root)
	out := map[string]LocalEntry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p != root && errors.Is(err, fs.ErrNotExist) {
				return nil // vanished during the walk
			}
			return err
		}
		if p == root {
			return nil
		}
		rel := filepath.ToSlash(p[len(root)+1:])
		if selection.Excluded(rel, excludes) || slices.ContainsFunc(otherRoots, func(r string) bool { return paths.IsWithin(p, r) }) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		e := LocalEntry{Dir: d.IsDir(), MTime: fi.ModTime().UnixNano()}
		if !e.Dir {
			e.Size = fi.Size()
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			e.Ino = st.Ino
		}
		out[rel] = e
		return nil
	})
	return out, err
}
