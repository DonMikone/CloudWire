package offline

import (
	"path/filepath"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// remoteWithin reports whether remote path a equals b or lies below it.
func remoteWithin(a, b string) bool {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	return b == "" || a == b || strings.HasPrefix(a, b+"/")
}

// CoveredRemote returns the Connection-relative remote paths an item syncs.
func CoveredRemote(it store.OfflineItem) []string {
	sel := selectionOf(it)
	out := make([]string, 0, len(sel))
	for _, e := range sel {
		out = append(out, joinRemote(it.RemotePath, e))
	}
	return out
}

// remoteOverlap reports whether two items of one Connection cover a common
// remote path.
func remoteOverlap(a, b store.OfflineItem) bool {
	if a.ConnectionID != b.ConnectionID {
		return false
	}
	bs := CoveredRemote(b)
	for _, x := range CoveredRemote(a) {
		for _, y := range bs {
			if remoteWithin(x, y) || remoteWithin(y, x) {
				return true
			}
		}
	}
	return false
}

func joinRemote(dir, name string) string {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return name
	}
	if name == "" {
		return dir
	}
	return dir + "/" + name
}

// nestedAtCloudPath reports whether the Storage Location of one item lies in
// the other's exactly at its cloud path, where the outer item, which mirrors
// its root, would keep it. The remote paths must not overlap (checked before):
// each item then syncs only its own part of the shared folders.
func nestedAtCloudPath(a, b store.OfflineItem) bool {
	if a.ConnectionID != b.ConnectionID {
		return false
	}
	outer, inner := a, b
	if len(outer.StoragePath) > len(inner.StoragePath) {
		outer, inner = b, a
	}
	root, innerRoot := strings.Trim(outer.RemotePath, "/"), strings.Trim(inner.RemotePath, "/")
	if !remoteWithin(innerRoot, root) || innerRoot == root {
		return false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(innerRoot, root), "/")
	return inner.StoragePath == filepath.Join(outer.StoragePath, filepath.FromSlash(rel))
}

// Validation outcome of a new item.
type validation struct {
	// mergeInto is set when the new Selection must be merged into an existing item.
	mergeInto *store.OfflineItem
}

// vaultFolderSuffix marks the folder of a Vault; its content is encrypted and
// only usable through the unlocked Vault's own Connection.
const vaultFolderSuffix = ".cwvault"

// validateNew checks a new item against the existing ones and the system.
func validateNew(nu store.OfflineItem, existing []store.OfflineItem, mountPoints []string, p paths.Paths) (validation, error) {
	var v validation
	for _, covered := range CoveredRemote(nu) {
		for _, part := range strings.Split(covered, "/") {
			if strings.HasSuffix(part, vaultFolderSuffix) {
				return v, api.Fail("offline.vaultFolder", msg.New("offline.vaultFolder", "folder", part))
			}
		}
	}
	sp := nu.StoragePath
	if !filepath.IsAbs(sp) {
		return v, api.Invalid("storage location must be an absolute path")
	}
	if sp == "/" || sp == p.Home || paths.IsWithin(p.Home, sp) {
		return v, api.Fail("offline.overlap", msg.New("offline.storageTooBroad", "path", sp))
	}
	for _, protected := range []string{p.AppSupport, p.CacheDir, filepath.Join(p.Home, "Library")} {
		if paths.IsWithin(sp, protected) || paths.IsWithin(protected, sp) {
			return v, api.Fail("offline.overlap", msg.New("offline.storageNotAllowed", "path", sp))
		}
	}
	for _, mp := range mountPoints {
		if paths.IsWithin(sp, mp) || paths.IsWithin(mp, sp) {
			return v, api.Fail("offline.overlap", msg.New("offline.storageOverlapsMount", "path", sp, "mountPoint", mp))
		}
	}
	for i := range existing {
		ex := existing[i]
		if ex.ConnectionID == nu.ConnectionID &&
			strings.Trim(ex.RemotePath, "/") == strings.Trim(nu.RemotePath, "/") && ex.StoragePath == sp {
			v.mergeInto = &existing[i]
			continue
		}
		if remoteOverlap(nu, ex) {
			return v, api.Fail("offline.overlap", msg.New("offline.overlapsItem", "name", ItemName(ex), "path", ex.StoragePath)).
				WithData("itemId", ex.ID)
		}
		if (paths.IsWithin(sp, ex.StoragePath) || paths.IsWithin(ex.StoragePath, sp)) && !nestedAtCloudPath(nu, ex) {
			return v, api.Fail("offline.overlap", msg.New("offline.storageOverlapsItem", "name", ItemName(ex), "path", ex.StoragePath)).
				WithData("itemId", ex.ID)
		}
	}
	return v, nil
}
