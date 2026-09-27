package offline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/identity"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/selection"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// renameCollisionPrefix marks the reason of an item stopped by a rename
// whose target exists on the other side (ADR 0011).
const renameCollisionPrefix = "rename collision: "

// renameSelection applies renames to a Selection, in order: the entries at
// or below a renamed path follow it, and a path that was covered before
// stays covered under its new name (ADR 0010: checked stays checked).
func renameSelection(files []string, rs []sv.Rename) []string {
	if slices.Contains(files, "") {
		return files // the whole root
	}
	out := slices.Clone(files)
	for _, r := range rs {
		if r.Result == sv.RenameRoot {
			continue
		}
		covered := selection.Covers(out, r.From)
		for i, f := range out {
			out[i], _ = selection.Rebase(f, r.From, r.To)
		}
		if covered && !selection.Covers(out, r.To) {
			out = append(out, r.To)
		}
	}
	norm, err := selection.Normalize(out)
	if err != nil {
		return files
	}
	return norm
}

// identityJob describes an item for the worker's rename detection.
func (e *Engine) identityJob(it store.OfflineItem, conn store.Connection, others []store.OfflineItem, rr *sv.RootRename) *sv.IdentityJob {
	return &sv.IdentityJob{
		StoragePath: it.StoragePath, Remote: conn.RcloneRemote, RemotePath: it.RemotePath,
		RemoteFs: conn.RcloneRemote + ":" + it.RemotePath, ConnFs: conn.RcloneRemote + ":",
		Files: selectionOf(it), Excludes: it.Excludes, OtherRoots: storageRoots(others),
		Snapshot: e.paths.IdentityFile(it.ID), Nextcloud: e.isNextcloud(conn),
		ConflictMarker: ConflictMarker(e.label()), RootRename: rr,
	}
}

// isNextcloud reports whether a Connection is a Nextcloud or ownCloud WebDAV remote.
func (e *Engine) isNextcloud(conn store.Connection) bool {
	if conn.Provider != "webdav" {
		return false
	}
	cfg, err := e.Conns.Config(conn)
	return err == nil && nextcloudVendor(cfg)
}

func nextcloudVendor(cfg map[string]string) bool {
	return cfg["vendor"] == "nextcloud" || cfg["vendor"] == "owncloud"
}

// applyRenamesLocked records renames a worker applied before its sync: the
// Selection and, for a root rename, the item's paths follow; nested items
// follow a renamed folder that holds them; bisync's listings, the filters
// and the identity snapshot are rewritten so the sync sees no change. Every
// step may repeat: a run that fails before the next detects the same renames
// again from the old snapshot.
func (e *Engine) applyRenamesLocked(it *store.OfflineItem, rs []sv.Rename) error {
	if len(rs) == 0 {
		return nil
	}
	oldRemote, oldStorage := it.RemotePath, it.StoragePath
	if it.Kind == "files" {
		it.Files = renameSelection(it.Files, rs)
	}
	for _, r := range rs {
		if r.Result == sv.RenameRoot {
			it.RemotePath, it.StoragePath = r.To, r.NewStoragePath
			// bisync's session is named after both paths.
			e.requestResyncLocked(it)
		}
	}
	if err := e.followNestedLocked(*it, oldRemote, oldStorage, rs); err != nil {
		return err
	}
	if err := e.st.UpdateOfflineItem(*it); err != nil {
		return err
	}
	if it.StoragePath != oldStorage {
		e.stopWatchingLocked(it.ID)
		if e.Watch {
			go e.startWatching(*it)
		}
	}
	workdir := e.paths.BisyncWorkdir(it.ID)
	if err := identity.RewriteListings(workdir, rs); err != nil {
		// Without matching listings bisync would see deletions: a resync
		// (a union that never deletes) is the safe fallback.
		e.requestResyncLocked(it)
		if err := e.st.UpdateOfflineItem(*it); err != nil {
			return err
		}
		e.log.Warn("sync", it.ID, msg.Detail(err.Error()), nil)
	}
	if err := WriteFilters(e.paths, *it); err != nil {
		return err
	}
	if !it.NeedsResync {
		if listings, _ := filepath.Glob(filepath.Join(workdir, "*.path1.lst")); len(listings) > 0 {
			if err := identity.WriteFiltersHash(e.paths.FiltersFile(it.ID)); err != nil {
				return err
			}
		}
	}
	if s, ok := identity.Load(e.paths.IdentityFile(it.ID)); ok {
		s.Apply(rs)
		if err := identity.Save(e.paths.IdentityFile(it.ID), s); err != nil {
			return err
		}
	}
	list := make([]string, 0, len(rs))
	for _, r := range rs {
		list = append(list, r.From+" → "+r.To)
	}
	e.log.Info("offline", it.ID, msg.New("offline.renamed", "count", len(rs), "name", ItemName(*it)), list)
	e.publishItemLocked(*it)
	return nil
}

// followNestedLocked moves the paths of the items of the same Connection
// whose cloud root lies in a folder it renamed (or in its renamed root).
func (e *Engine) followNestedLocked(it store.OfflineItem, oldRemote, oldStorage string, rs []sv.Rename) error {
	items, err := e.st.OfflineItems()
	if err != nil {
		return err
	}
	for _, y := range items {
		if y.ID == it.ID || y.ConnectionID != it.ConnectionID {
			continue
		}
		changed := false
		for _, r := range rs {
			var from, to, localFrom, localTo string
			switch r.Result {
			case sv.RenameMoved:
				from, to = selection.Join(oldRemote, r.From), selection.Join(oldRemote, r.To)
				localFrom = filepath.Join(oldStorage, filepath.FromSlash(r.From))
				localTo = filepath.Join(oldStorage, filepath.FromSlash(r.To))
			case sv.RenameRoot:
				from, to = strings.Trim(r.From, "/"), strings.Trim(r.To, "/")
				localFrom, localTo = oldStorage, r.NewStoragePath
			default:
				continue
			}
			q, ok := selection.Rebase(strings.Trim(y.RemotePath, "/"), from, to)
			if !ok {
				continue
			}
			y.RemotePath, changed = q, true
			if p, ok := selection.Rebase(y.StoragePath, localFrom, localTo); ok {
				y.StoragePath = p
			}
		}
		if !changed {
			continue
		}
		e.requestResyncLocked(&y)
		if err := e.st.UpdateOfflineItem(y); err != nil {
			return fmt.Errorf("update nested item %s: %w", y.ID, err)
		}
		e.stopWatchingLocked(y.ID)
		if e.Watch {
			go e.startWatching(y)
		}
		e.publishItemLocked(y)
	}
	return nil
}

// onRenames records the renames a worker applied before its sync and lets
// it continue with the params they changed.
func (e *Engine) onRenames(r *running, rs []sv.Rename) {
	e.mu.Lock()
	job := r.job
	cmd, ok := e.renamesAppliedLocked(r, rs)
	e.mu.Unlock()
	if job == nil {
		return
	}
	if !ok {
		_ = job.Send(sv.Command{Cmd: "stop"})
		return
	}
	_ = job.Send(cmd)
}

func (e *Engine) renamesAppliedLocked(r *running, rs []sv.Rename) (sv.Command, bool) {
	it, err := e.st.OfflineItem(r.q.itemID)
	if err != nil {
		return sv.Command{}, false // removed meanwhile
	}
	rt := e.itemRT(it.ID)
	rt.rootRename = nil
	if err := e.applyRenamesLocked(&it, rs); err != nil {
		e.log.Error("sync", it.ID, msg.New("sync.failed", "name", ItemName(it), "detail", err.Error()), nil)
		return sv.Command{}, false
	}
	conn, err := e.Conns.Get(it.ConnectionID)
	if err != nil {
		return sv.Command{}, false
	}
	b, err := json.Marshal(BisyncParams(it, conn.RcloneRemote, e.paths, e.label(), r.q.force))
	if err != nil {
		return sv.Command{}, false
	}
	items, err := e.st.OfflineItems()
	if err != nil {
		return sv.Command{}, false
	}
	// The continued run performs every resync requested so far.
	r.resyncGen = rt.resyncGen
	others := slices.DeleteFunc(items, func(x store.OfflineItem) bool { return x.ID == it.ID })
	return sv.Command{Cmd: "continue", Bisync: b, Identity: e.identityJob(it, conn, others, nil)}, true
}

// followRootLocked follows a Storage Location that was renamed or moved in
// Finder (ADR 0011). Its cloud folder stays, except for an item created
// before 0.3.0 whose folder name changed: its run renames the cloud root
// first. It reports whether the item now knows where its files are.
func (e *Engine) followRootLocked(it *store.OfflineItem) bool {
	rt := e.itemRT(it.ID)
	if rt.rootRename != nil {
		return true
	}
	np, err := e.followTarget(*it, platform.PathOfInode)
	if err != nil {
		return false
	}
	if it.RemotePath != "" && filepath.Base(np) != filepath.Base(it.StoragePath) {
		parent := path.Dir(strings.Trim(it.RemotePath, "/"))
		if parent == "." {
			parent = ""
		}
		rt.rootRename = &sv.RootRename{From: it.RemotePath, To: selection.Join(parent, filepath.Base(np)), NewStoragePath: np}
		return true
	}
	old := it.StoragePath
	it.StoragePath = np
	// bisync's session is named after both paths (like Relocate).
	e.requestResyncLocked(it)
	if it.State == StatePaused && it.LastError == ReasonLocationMissing {
		it.State, it.LastError = StatePending, ""
	}
	if err := e.st.UpdateOfflineItem(*it); err != nil {
		it.StoragePath = old
		return false
	}
	e.stopWatchingLocked(it.ID)
	if e.Watch {
		go e.startWatching(*it)
	}
	e.log.Info("offline", it.ID, msg.New("offline.followed", "name", ItemName(*it), "path", np), nil)
	e.publishItemLocked(*it)
	return true
}

// followRoot handles a changed Storage Location reported by FSEvents; a run
// in progress stops and starts again at the new place.
func (e *Engine) followRoot(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, err := e.st.OfflineItem(id)
	if err != nil {
		return
	}
	if e.followRootLocked(&it) && e.cur != nil && e.cur.q.itemID == id {
		e.interruptCurrentLocked()
	}
}

// errNotFollowed is returned by followRootLocked's checks; never surfaced.
var errNotFollowed = errors.New("not followed")

// followTarget checks where a vanished Storage Location went: its folder's
// inode must now live at a valid Storage Location that is not in a Trash.
func (e *Engine) followTarget(it store.OfflineItem, find func(string, uint64) (string, error)) (string, error) {
	if it.RootIno == 0 {
		return "", errNotFollowed
	}
	if _, err := os.Lstat(it.StoragePath); err == nil {
		return "", errNotFollowed
	}
	np, err := find(it.StoragePath, it.RootIno)
	if err != nil || np == it.StoragePath {
		return "", errNotFollowed
	}
	if strings.Contains(np, "/.Trash/") || strings.HasSuffix(np, "/.Trash") || strings.Contains(np, "/.Trashes/") {
		return "", errNotFollowed
	}
	items, err := e.st.OfflineItems()
	if err != nil {
		return "", err
	}
	others := slices.DeleteFunc(items, func(x store.OfflineItem) bool { return x.ID == it.ID })
	probe := it
	probe.StoragePath = np
	if _, err := validateNew(probe, others, e.mountPoints(), e.paths); err != nil {
		return "", errNotFollowed
	}
	return np, nil
}
