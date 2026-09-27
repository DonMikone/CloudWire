package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/rclone/rclone/fs"

	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/selection"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// ErrStopped is returned by Apply when it was asked to stop between two ops.
var ErrStopped = errors.New("stopped")

// Detect finds the renames since the item's snapshot and decides how to
// apply them. Without a snapshot there is nothing to compare against, except
// for a requested root rename.
func Detect(ctx context.Context, job *sv.IdentityJob, cloud Cloud) (ops []Op, renames []sv.Rename, collision string, err error) {
	if job.RootRename != nil {
		return rootRename(ctx, job, cloud)
	}
	snap, ok := Load(job.Snapshot)
	if !ok {
		return nil, nil, "", nil
	}
	curCloud, _, err := cloud.Scan(ctx, job.Files, snap)
	if err != nil {
		if job.RemotePath != "" && snap.RootCloud.ID != "" && errors.Is(err, fs.ErrorDirNotFound) {
			return legacyRootMoved(ctx, job, cloud, snap)
		}
		return nil, nil, "", err
	}
	curLocal, err := ScanLocal(job.StoragePath, job.Excludes, job.OtherRoots)
	if err != nil {
		return nil, nil, "", err
	}
	in := Inputs{
		PrevLocal: snap.Local, CurLocal: curLocal, PrevCloud: snap.Cloud,
		Local: DetectLocal(snap.Local, curLocal, snap.Persistent && platform.PersistentInodes(job.StoragePath), job.ConflictMarker),
		LocalStat: func(rel string) (os.FileInfo, error) {
			return os.Lstat(filepath.Join(job.StoragePath, filepath.FromSlash(rel)))
		},
		CloudStat: func(rel string) (bool, bool, error) { return cloud.Stat(ctx, rel) },
	}
	if curCloud != nil && snap.Provider != ProviderNone {
		in.CurCloud = curCloud
		in.Cloud = DetectCloud(snap.Cloud, curCloud, job.ConflictMarker)
	}
	ops, renames, collision, err = Reconcile(in)
	for i := range ops {
		if ops[i].Side == OpLocal {
			ops[i].From = filepath.Join(job.StoragePath, filepath.FromSlash(ops[i].From))
			ops[i].To = filepath.Join(job.StoragePath, filepath.FromSlash(ops[i].To))
		}
	}
	return ops, renames, collision, err
}

// rootRename moves the cloud root of an item created before 0.3.0 whose
// Storage Location was renamed (it already is at NewStoragePath).
func rootRename(ctx context.Context, job *sv.IdentityJob, cloud Cloud) ([]Op, []sv.Rename, string, error) {
	rr := job.RootRename
	r := sv.Rename{From: rr.From, To: rr.To, Dir: true, Result: sv.RenameRoot, NewStoragePath: rr.NewStoragePath}
	from, _, err := cloud.ConnStat(ctx, rr.From)
	if err != nil {
		return nil, nil, "", err
	}
	to, _, err := cloud.ConnStat(ctx, rr.To)
	if err != nil {
		return nil, nil, "", err
	}
	switch {
	case from && to:
		return nil, nil, rr.To, nil
	case from:
		return []Op{{Side: OpConn, From: rr.From, To: rr.To, Dir: true}}, []sv.Rename{r}, "", nil
	case to:
		return nil, []sv.Rename{r}, "", nil // moved before
	}
	return nil, nil, "", fmt.Errorf("%s: %w", rr.From, fs.ErrorDirNotFound)
}

// legacyRootMoved finds the cloud root of an item created before 0.3.0 that
// was renamed in the cloud by its id among its former siblings. The Storage
// Location follows when it carries the cloud folder's name.
func legacyRootMoved(ctx context.Context, job *sv.IdentityJob, cloud Cloud, snap Snapshot) ([]Op, []sv.Rename, string, error) {
	parent := path.Dir(job.RemotePath)
	if parent == "." || parent == "/" {
		parent = ""
	}
	found, err := cloud.Find(ctx, parent, snap.RootCloud.ID)
	if err != nil || found == "" {
		return nil, nil, "", fmt.Errorf("%s: %w", job.RemotePath, fs.ErrorDirNotFound)
	}
	r := sv.Rename{From: job.RemotePath, To: found, Dir: true, Result: sv.RenameRoot, NewStoragePath: job.StoragePath}
	var ops []Op
	if filepath.Base(job.StoragePath) == path.Base(job.RemotePath) {
		np := filepath.Join(filepath.Dir(job.StoragePath), path.Base(found))
		if _, err := os.Lstat(np); err == nil {
			return nil, nil, path.Base(found), nil
		}
		ops = append(ops, Op{Side: OpLocal, From: job.StoragePath, To: np, Dir: true})
		r.NewStoragePath = np
	}
	return ops, []sv.Rename{r}, "", nil
}

// Apply performs ops in order. It stops between two ops once stop is closed.
func Apply(ctx context.Context, cloud Cloud, ops []Op, stop <-chan struct{}) error {
	for _, op := range ops {
		select {
		case <-stop:
			return ErrStopped
		default:
		}
		var err error
		switch op.Side {
		case OpLocal:
			if err = os.MkdirAll(filepath.Dir(op.To), 0o755); err == nil {
				err = os.Rename(op.From, op.To)
			}
		case OpCloud:
			err = cloud.Move(ctx, op.From, op.To, op.Dir)
		case OpConn:
			err = cloud.ConnMove(ctx, op.From, op.To)
		default:
			err = fmt.Errorf("unknown op side %q", op.Side)
		}
		if err != nil {
			return fmt.Errorf("rename %s to %s: %w", op.From, op.To, err)
		}
	}
	return nil
}

// Capture records the item's identities after a successful run. Paths
// bisync just listed but that are gone now (renamed or deleted while the run
// ended) keep their previous identity, so the next run still finds them.
// On failure the snapshot is removed: the next run detects nothing rather
// than something wrong.
func Capture(ctx context.Context, job *sv.IdentityJob, cloud Cloud, workdir string) (err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(job.Snapshot)
		}
	}()
	prev, _ := Load(job.Snapshot)
	curCloud, root, err := cloud.Scan(ctx, job.Files, prev)
	if err != nil {
		return err
	}
	curLocal, err := ScanLocal(job.StoragePath, job.Excludes, job.OtherRoots)
	if err != nil {
		return err
	}
	s := Snapshot{Persistent: platform.PersistentInodes(job.StoragePath), Provider: cloud.Name(),
		Files: job.Files, RootCloud: root, Local: map[string]LocalEntry{}}
	for p, e := range curLocal {
		if selection.Relevant(job.Files, p) {
			s.Local[p] = e
		}
	}
	if curCloud == nil {
		s.Provider = ProviderNone
	} else {
		s.Cloud = map[string]CloudEntry{}
		for p, e := range curCloud {
			if selection.Relevant(job.Files, p) {
				s.Cloud[p] = e
			}
		}
	}
	if err := keepListed(workdir, "path1", prev.Local, s.Local); err != nil {
		return err
	}
	if s.Cloud != nil && prev.Provider == s.Provider {
		if err := keepListed(workdir, "path2", prev.Cloud, s.Cloud); err != nil {
			return err
		}
	}
	return Save(job.Snapshot, s)
}

// keepListed copies entries of prev that are missing from cur but listed by
// bisync on side.
func keepListed[E any](workdir, side string, prev, cur map[string]E) error {
	listed, err := listedPaths(workdir, side)
	if err != nil {
		return err
	}
	for p, e := range prev {
		if _, ok := cur[p]; !ok && listed[p] {
			cur[p] = e
		}
	}
	return nil
}
