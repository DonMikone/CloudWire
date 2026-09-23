package offline

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// remoteWithin reports whether remote path a equals b or lies below it.
func remoteWithin(a, b string) bool {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	return b == "" || a == b || strings.HasPrefix(a, b+"/")
}

// remoteOverlap reports whether two items of one Connection cover a common
// remote path.
func remoteOverlap(a, b store.OfflineItem) bool {
	if a.ConnectionID != b.ConnectionID {
		return false
	}
	switch {
	case a.Kind == "folder" && b.Kind == "folder":
		return remoteWithin(a.RemotePath, b.RemotePath) || remoteWithin(b.RemotePath, a.RemotePath)
	case a.Kind == "folder" && b.Kind == "files":
		return filesWithin(b, a.RemotePath)
	case a.Kind == "files" && b.Kind == "folder":
		return filesWithin(a, b.RemotePath)
	default:
		if strings.Trim(a.RemotePath, "/") != strings.Trim(b.RemotePath, "/") {
			return false
		}
		for _, f := range a.Files {
			if slices.Contains(b.Files, f) {
				return true
			}
		}
		return false
	}
}

func filesWithin(it store.OfflineItem, folder string) bool {
	for _, f := range it.Files {
		if remoteWithin(joinRemote(it.RemotePath, f), folder) {
			return true
		}
	}
	return false
}

func joinRemote(dir, name string) string {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// Validation outcome of a new item.
type validation struct {
	// mergeInto is set when the new files must be appended to an existing files item.
	mergeInto *store.OfflineItem
}

// validateNew checks a new item against the existing ones and the system.
func validateNew(nu store.OfflineItem, existing []store.OfflineItem, mountPoints []string, p paths.Paths) (validation, error) {
	var v validation
	sp := nu.StoragePath
	if !filepath.IsAbs(sp) {
		return v, api.Invalid("storage location must be an absolute path")
	}
	if sp == "/" || sp == p.Home || paths.IsWithin(p.Home, sp) {
		return v, api.Errorf("offline.overlap", "The storage location %s is too broad", sp)
	}
	for _, protected := range []string{p.AppSupport, p.CacheDir, filepath.Join(p.Home, "Library")} {
		if paths.IsWithin(sp, protected) || paths.IsWithin(protected, sp) {
			return v, api.Errorf("offline.overlap", "The storage location %s is not allowed", sp)
		}
	}
	for _, mp := range mountPoints {
		if paths.IsWithin(sp, mp) || paths.IsWithin(mp, sp) {
			return v, api.Errorf("offline.overlap", "The storage location %s overlaps the Mount at %s", sp, mp)
		}
	}
	for i := range existing {
		ex := existing[i]
		if nu.Kind == "files" && ex.Kind == "files" && ex.ConnectionID == nu.ConnectionID &&
			strings.Trim(ex.RemotePath, "/") == strings.Trim(nu.RemotePath, "/") && ex.StoragePath == sp {
			v.mergeInto = &existing[i]
			continue
		}
		if remoteOverlap(nu, ex) {
			return v, api.Errorf("offline.overlap", "This overlaps the Offline Item %q (%s)", itemName(ex), ex.StoragePath).
				WithData("itemId", ex.ID)
		}
		if paths.IsWithin(sp, ex.StoragePath) || paths.IsWithin(ex.StoragePath, sp) {
			return v, api.Errorf("offline.overlap", "The storage location overlaps the Offline Item %q (%s)", itemName(ex), ex.StoragePath).
				WithData("itemId", ex.ID)
		}
	}
	return v, nil
}
