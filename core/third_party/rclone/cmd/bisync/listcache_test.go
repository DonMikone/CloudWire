package bisync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	rfs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/list"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// etagFs is Path2 for the listing cache tests: a local directory whose
// folders report Nextcloud-like change markers. The ETag hashes the whole
// subtree, so a change anywhere below a folder changes the ETags of all its
// ancestors, as Nextcloud's propagation does; the file id is the inode.
type etagFs struct {
	rfs.Fs
	root     string
	features *rfs.Features

	mu      sync.Mutex
	lists   map[string]int
	mounted map[string]bool
	failing map[string]bool
}

func newEtagFs(t *testing.T, root string) *etagFs {
	t.Helper()
	f, err := local.NewFs(context.Background(), "local", root, configmap.Simple{})
	require.NoError(t, err)
	e := &etagFs{Fs: f, root: root, lists: map[string]int{}, mounted: map[string]bool{}, failing: map[string]bool{}}
	features := *f.Features()
	features.ListR = nil
	features.ListP = e.ListP
	e.features = &features
	return e
}

func (e *etagFs) Features() *rfs.Features { return e.features }

func (e *etagFs) List(ctx context.Context, dir string) (rfs.DirEntries, error) {
	return list.WithListP(ctx, dir, e)
}

func (e *etagFs) ListP(ctx context.Context, dir string, callback rfs.ListRCallback) error {
	e.mu.Lock()
	e.lists[dir]++
	fail := e.failing[dir]
	e.mu.Unlock()
	if fail {
		return errors.New("listing failed")
	}
	entries, err := e.Fs.List(ctx, dir)
	if err != nil {
		return err
	}
	for i, de := range entries {
		if d, ok := de.(rfs.Directory); ok {
			p := filepath.Join(e.root, d.Remote())
			e.mu.Lock()
			mounted := e.mounted[d.Remote()]
			e.mu.Unlock()
			entries[i] = &markedDir{Directory: d, etag: subtreeETag(p), fileID: inode(p), mounted: mounted}
		}
	}
	return callback(entries)
}

func (e *etagFs) take() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.lists
	e.lists = map[string]int{}
	return l
}

type markedDir struct {
	rfs.Directory
	etag, fileID string
	mounted      bool
}

func (d *markedDir) ETag() string   { return d.etag }
func (d *markedDir) FileID() string { return d.fileID }
func (d *markedDir) Mounted() bool  { return d.mounted }

func subtreeETag(dir string) string {
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func inode(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return fmt.Sprint(info.Sys().(*syscall.Stat_t).Ino)
}

type cachePair struct {
	t            *testing.T
	path1, path2 string
	workdir      string
	fs1          rfs.Fs
	fs2          *etagFs
	runs         int
}

var cacheBase = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

func newCachePair(t *testing.T) *cachePair {
	t.Helper()
	root := t.TempDir()
	p := &cachePair{t: t, path1: filepath.Join(root, "p1"), path2: filepath.Join(root, "p2"), workdir: filepath.Join(root, "wd")}
	for _, f := range []string{"a/a1/x.txt", "a/a2/y.txt", "b/z.txt", "root.txt"} {
		p.write(p.path2, f, "v1", cacheBase)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(p.path2, "c"), 0o755)) // empty folder
	require.NoError(t, os.MkdirAll(p.path1, 0o755))
	var err error
	p.fs1, err = local.NewFs(context.Background(), "local", p.path1, configmap.Simple{})
	require.NoError(t, err)
	p.fs2 = newEtagFs(t, p.path2)
	require.NoError(t, p.run(ListingCacheUse, true))
	return p
}

func (p *cachePair) write(root, rel, content string, mtime time.Time) {
	p.t.Helper()
	f := filepath.Join(root, rel)
	require.NoError(p.t, os.MkdirAll(filepath.Dir(f), 0o755))
	require.NoError(p.t, os.WriteFile(f, []byte(content), 0o644))
	require.NoError(p.t, os.Chtimes(f, mtime, mtime))
}

func (p *cachePair) run(mode ListingCacheMode, resync bool) error {
	p.t.Helper()
	p.fs2.take()
	opt := &Options{
		Workdir:            p.workdir,
		Resync:             resync,
		CreateEmptySrcDirs: true,
		Resilient:          true,
		Recover:            true,
		MaxDelete:          DefaultMaxDelete,
		CompareFlag:        "size,modtime",
		ListingCache:       mode,
	}
	// Each rc job has its own stats group; errors must not leak into the next run.
	p.runs++
	ctx := accounting.WithStatsGroup(context.Background(), fmt.Sprintf("%s-%d", p.t.Name(), p.runs))
	return Bisync(ctx, p.fs1, p.fs2, opt)
}

// lists runs bisync and returns the Path2 folders it listed.
func (p *cachePair) lists(mode ListingCacheMode) []string {
	p.t.Helper()
	require.NoError(p.t, p.run(mode, false))
	return slices.Sorted(maps.Keys(p.fs2.take()))
}

func (p *cachePair) cacheFile() string {
	m, _ := filepath.Glob(filepath.Join(p.workdir, "*.path2.lsc"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func (p *cachePair) listing2() string {
	m, err := filepath.Glob(filepath.Join(p.workdir, "*.path2.lst"))
	require.NoError(p.t, err)
	require.Len(p.t, m, 1)
	b, err := os.ReadFile(m[0])
	require.NoError(p.t, err)
	_, body, _ := strings.Cut(string(b), "\n") // header carries a timestamp
	return body
}

var allDirs = []string{"", "a", "a/a1", "a/a2", "b", "c"}

func TestListingCacheReusesUnchangedFolders(t *testing.T) {
	p := newCachePair(t)
	assert.Empty(t, p.cacheFile(), "a resync keeps no listing cache")

	assert.Equal(t, allDirs, p.lists(ListingCacheUse), "without a cache everything is listed")
	require.NotEmpty(t, p.cacheFile())
	full := p.listing2()

	// Nothing changed: the root's listing vouches for every folder.
	assert.Equal(t, []string{""}, p.lists(ListingCacheUse))
	assert.Equal(t, full, p.listing2(), "the reused listing must equal the live one")

	// A cloud change deep below: only its ancestors are listed again, and
	// the change reaches Path1.
	p.write(p.path2, "a/a1/x.txt", "v2 cloud", cacheBase.Add(time.Hour))
	assert.Equal(t, []string{"", "a", "a/a1"}, p.lists(ListingCacheUse))
	got, err := os.ReadFile(filepath.Join(p.path1, "a/a1/x.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2 cloud", string(got))
	assert.Equal(t, []string{""}, p.lists(ListingCacheUse))

	// A local change is uploaded; the upload changes the cloud folder, so
	// the next run lists it again instead of trusting the old record.
	p.write(p.path1, "b/new.txt", "local", cacheBase)
	require.NoError(t, p.run(ListingCacheUse, false))
	_, err = os.Stat(filepath.Join(p.path2, "b/new.txt"))
	require.NoError(t, err)
	assert.Contains(t, p.lists(ListingCacheUse), "b")
	assert.Equal(t, []string{""}, p.lists(ListingCacheUse))

	// refresh lists everything and renews the cache.
	assert.Equal(t, allDirs, p.lists(ListingCacheRefresh))
	assert.Equal(t, []string{""}, p.lists(ListingCacheUse))

	// off neither uses nor keeps a cache.
	assert.Equal(t, allDirs, p.lists(ListingCacheOff))
	assert.Empty(t, p.cacheFile())
}

func TestListingCacheNeverTrustsMountsOrReplacedFolders(t *testing.T) {
	p := newCachePair(t)
	p.fs2.mounted["a/a2"] = true // e.g. external storage: no ETag propagation
	p.lists(ListingCacheUse)
	// a/a2 and its ancestor a are listed on every run.
	assert.Equal(t, []string{"", "a", "a/a2"}, p.lists(ListingCacheUse))
	assert.Equal(t, []string{"", "a", "a/a2"}, p.lists(ListingCacheUse))

	// A folder deleted and recreated with identical content and times keeps
	// its ETag here, but not its file id.
	b := filepath.Join(p.path2, "b")
	info, err := os.Stat(b)
	require.NoError(t, err)
	etag, id := subtreeETag(b), inode(b)
	require.NoError(t, os.RemoveAll(b))
	p.write(p.path2, "b/z.txt", "v1", cacheBase)
	require.NoError(t, os.Chtimes(b, info.ModTime(), info.ModTime()))
	require.Equal(t, etag, subtreeETag(b), "premise: same ETag")
	require.NotEqual(t, id, inode(b), "premise: new file id")
	assert.Contains(t, p.lists(ListingCacheUse), "b")
}

func TestListingCacheFailedRunListsEverythingNext(t *testing.T) {
	p := newCachePair(t)
	p.lists(ListingCacheUse)
	require.NotEmpty(t, p.cacheFile())

	p.write(p.path2, "b/later.txt", "cloud", cacheBase) // b must be listed live
	p.fs2.failing["b"] = true
	require.Error(t, p.run(ListingCacheUse, false))
	assert.Empty(t, p.cacheFile(), "a failed run must not leave a cache behind")

	// The next run lists everything.
	delete(p.fs2.failing, "b")
	assert.Equal(t, allDirs, p.lists(ListingCacheUse))
	assert.Equal(t, []string{""}, p.lists(ListingCacheUse))
}
