package offline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"

	"github.com/DonMikone/CloudWire/core/internal/identity"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// inodeCloud is the local "cloud" folder with its inodes as file ids, like
// Nextcloud's oc:fileid (the local backend has no ids of its own).
type inodeCloud struct {
	identity.Remote
	dir string
}

func (c inodeCloud) Name() string { return identity.ProviderIDs }

func (c inodeCloud) Scan(_ context.Context, _ []string, _ identity.Snapshot) (map[string]identity.CloudEntry, identity.CloudEntry, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(c.dir, &st); err != nil {
		return nil, identity.CloudEntry{}, fs.ErrorDirNotFound
	}
	local, err := identity.ScanLocal(c.dir, nil, nil)
	if err != nil {
		return nil, identity.CloudEntry{}, err
	}
	out := map[string]identity.CloudEntry{}
	for p, e := range local {
		out[p] = identity.CloudEntry{Dir: e.Dir, ID: strconv.FormatUint(e.Ino, 10)}
	}
	return out, identity.CloudEntry{Dir: true, ID: strconv.FormatUint(st.Ino, 10)}, nil
}

func (c inodeCloud) Find(context.Context, string, string) (string, error) { return "", nil }

func ino(t *testing.T, p string) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Ino
}

// renamePair is a pair whose item lives in an engine, so the bookkeeping of
// renames runs for real between the worker's phases.
type renamePair struct {
	*pair
	h   *harness
	ids inodeCloud // the cloud side with ids
}

func newRenamePair(t *testing.T, files ...string) *renamePair {
	t.Helper()
	h := newHarness(t)
	pr := newPair(t, "files", files...)
	pr.p = h.e.paths
	pr.item.ConnectionID, pr.item.State = "c1", StateIdle
	if err := h.st.InsertOfflineItem(pr.item); err != nil {
		t.Fatal(err)
	}
	f, err := cache.Get(context.Background(), "loc:"+pr.cloud)
	if err != nil {
		t.Fatal(err)
	}
	return &renamePair{pair: pr, h: h, ids: inodeCloud{Remote: identity.Remote{F: f, Conn: f}, dir: pr.cloud}}
}

func (rp *renamePair) job() *sv.IdentityJob {
	return rp.h.e.identityJob(rp.item, store.Connection{RcloneRemote: "loc"}, nil, nil)
}

// syncAndCapture runs bisync and records the identities like the worker.
func (rp *renamePair) syncAndCapture() {
	rp.t.Helper()
	if err := rp.sync(false); err != nil {
		rp.t.Fatal(err)
	}
	if err := rp.h.st.UpdateOfflineItem(rp.item); err != nil {
		rp.t.Fatal(err)
	}
	if err := identity.Capture(context.Background(), rp.job(), rp.ids, rp.p.BisyncWorkdir(rp.item.ID)); err != nil {
		rp.t.Fatal(err)
	}
}

// renames runs the worker's phases before a sync: detect, apply, bookkeeping.
func (rp *renamePair) renames() (renames []sv.Rename, collision string) {
	rp.t.Helper()
	ctx := context.Background()
	ops, rs, collision, err := identity.Detect(ctx, rp.job(), rp.ids)
	if err != nil {
		rp.t.Fatal(err)
	}
	if collision != "" {
		return nil, collision
	}
	if err := identity.Apply(ctx, rp.ids, ops, nil); err != nil {
		rp.t.Fatal(err)
	}
	rp.h.e.mu.Lock()
	defer rp.h.e.mu.Unlock()
	if err := rp.h.e.applyRenamesLocked(&rp.item, rs); err != nil {
		rp.t.Fatal(err)
	}
	return rs, ""
}

// transfers returns what the last sync copied or deleted.
func transfers() map[string]string {
	out := map[string]string{}
	for p, a := range classified("Konflikt") {
		if a == "transferred" || a == "deleted" {
			out[p] = a
		}
	}
	return out
}

func (rp *renamePair) populate(rels ...string) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, r := range rels {
		write(rp.t, filepath.Join(rp.cloud, r), r, base)
	}
	rp.syncAndCapture()
}

func TestRenameLocalFolderMovesItInTheCloud(t *testing.T) {
	rp := newRenamePair(t, "Tree/B")
	rp.populate("Tree/B/x.wav", "Tree/B/Sub/y.wav", "Tree/A/a.txt")
	cloudIno := ino(t, filepath.Join(rp.cloud, "Tree/B"))
	if err := os.Rename(filepath.Join(rp.local, "Tree/B"), filepath.Join(rp.local, "Tree/B2")); err != nil {
		t.Fatal(err)
	}
	rs, _ := rp.renames()
	if len(rs) != 1 || rs[0].Result != sv.RenameMoved || !slices.Equal(rp.item.Files, []string{"Tree/B2"}) || rp.item.NeedsResync {
		t.Fatalf("renames %+v item %+v", rs, rp.item)
	}
	if err := rp.sync(false); err != nil {
		t.Fatalf("sync after the rename: %v", err)
	}
	if got := allFiles(t, rp.cloud); !slices.Equal(got, []string{"Tree/A/a.txt", "Tree/B2/Sub/y.wav", "Tree/B2/x.wav"}) {
		t.Fatalf("cloud %v", got)
	}
	if got := transfers(); len(got) != 0 {
		t.Fatalf("the rename was transferred: %v", got)
	}
	if ino(t, filepath.Join(rp.cloud, "Tree/B2")) != cloudIno {
		t.Fatal("the cloud folder was copied, not moved")
	}
}

func TestRenameInTheCloudRenamesLocally(t *testing.T) {
	rp := newRenamePair(t, "Tree/B")
	rp.populate("Tree/B/x.wav")
	localIno := ino(t, filepath.Join(rp.local, "Tree/B/x.wav"))
	if err := os.Rename(filepath.Join(rp.cloud, "Tree/B"), filepath.Join(rp.cloud, "Tree/B3")); err != nil {
		t.Fatal(err)
	}
	rs, _ := rp.renames()
	if len(rs) != 1 || !slices.Equal(rp.item.Files, []string{"Tree/B3"}) {
		t.Fatalf("renames %+v item %+v", rs, rp.item)
	}
	if err := rp.sync(false); err != nil {
		t.Fatal(err)
	}
	if got := allFiles(t, rp.local); !slices.Equal(got, []string{"Tree/B3/x.wav"}) {
		t.Fatalf("local %v", got)
	}
	if got := transfers(); len(got) != 0 {
		t.Fatalf("the rename was transferred: %v", got)
	}
	if ino(t, filepath.Join(rp.local, "Tree/B3/x.wav")) != localIno {
		t.Fatal("the local file was downloaded again")
	}
}

func TestRenamePartialFolderTakesCloudOnlyContent(t *testing.T) {
	rp := newRenamePair(t, "Tree/B")
	rp.populate("Tree/B/b.txt", "Tree/A/a.txt")
	if err := os.Rename(filepath.Join(rp.local, "Tree"), filepath.Join(rp.local, "Tree2")); err != nil {
		t.Fatal(err)
	}
	rp.renames()
	if !slices.Equal(rp.item.Files, []string{"Tree2/B"}) {
		t.Fatalf("Selection %q", rp.item.Files)
	}
	if err := rp.sync(false); err != nil {
		t.Fatal(err)
	}
	if got := allFiles(t, rp.cloud); !slices.Equal(got, []string{"Tree2/A/a.txt", "Tree2/B/b.txt"}) {
		t.Fatalf("cloud %v", got)
	}
	if got := allFiles(t, rp.local); !slices.Equal(got, []string{"Tree2/B/b.txt"}) {
		t.Fatalf("local %v", got)
	}
}

func TestRenameNeverTripsTheMassDeleteGuard(t *testing.T) {
	rp := newRenamePair(t, "Tree")
	var rels []string
	for i := range 10 {
		dir := "Tree/Rest"
		if i < 6 {
			dir = "Tree/B"
		}
		rels = append(rels, fmt.Sprintf("%s/f%02d.wav", dir, i))
	}
	rp.populate(rels...)
	if err := os.Rename(filepath.Join(rp.local, "Tree/B"), filepath.Join(rp.local, "Tree/B2")); err != nil {
		t.Fatal(err)
	}
	rp.renames()
	if err := rp.sync(false); err != nil {
		t.Fatalf("a rename of 6 of 10 files: %v", err)
	}
	if n := len(allFiles(t, rp.cloud)); n != 10 {
		t.Fatalf("cloud has %d files", n)
	}
}

func TestRenameBeatsCloudDeletion(t *testing.T) {
	rp := newRenamePair(t, "Tree/B", "Tree/C")
	rp.populate("Tree/B/x.wav", "Tree/C/z.wav")
	if err := os.Rename(filepath.Join(rp.local, "Tree/B"), filepath.Join(rp.local, "Tree/B2")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(rp.cloud, "Tree/B")); err != nil {
		t.Fatal(err)
	}
	rs, _ := rp.renames()
	if len(rs) != 1 || rs[0].Result != sv.RenameUpload {
		t.Fatalf("renames %+v", rs)
	}
	if err := rp.sync(false); err != nil {
		t.Fatal(err)
	}
	if got := allFiles(t, rp.cloud); !slices.Equal(got, []string{"Tree/B2/x.wav", "Tree/C/z.wav"}) {
		t.Fatalf("cloud %v", got)
	}
}

func TestRenameCollisionMovesNothing(t *testing.T) {
	rp := newRenamePair(t, "Tree/B")
	rp.populate("Tree/B/x.wav", "Tree/B2/other.wav")
	if err := os.Rename(filepath.Join(rp.local, "Tree/B"), filepath.Join(rp.local, "Tree/B2")); err != nil {
		t.Fatal(err)
	}
	if _, collision := rp.renames(); collision != "Tree/B2" {
		t.Fatalf("collision %q", collision)
	}
	if got := allFiles(t, rp.cloud); !slices.Equal(got, []string{"Tree/B/x.wav", "Tree/B2/other.wav"}) {
		t.Fatalf("cloud changed: %v", got)
	}
}

func TestRenameDetectionWithoutSnapshotDoesNothing(t *testing.T) {
	rp := newRenamePair(t, "Tree/B")
	rp.populate("Tree/B/x.wav")
	if err := os.Remove(rp.p.IdentityFile(rp.item.ID)); err != nil {
		t.Fatal(err)
	}
	ops, rs, collision, err := identity.Detect(context.Background(), rp.job(), rp.ids)
	if err != nil || ops != nil || rs != nil || collision != "" {
		t.Fatalf("ops %+v renames %+v collision %q err %v", ops, rs, collision, errors.Unwrap(err))
	}
}
