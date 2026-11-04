package bisync

// CloudWire patch (see docs/adr/0014): the transfer journal decides which
// Path2 changes are Cloud Changes. Path2 here is a plain local folder; the
// tests change it directly to play "someone else changed the cloud".

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

type journalPair struct {
	t                     *testing.T
	path1, path2, workdir string
	fs1, fs2              rfs.Fs
	settle                time.Duration
	runs                  int
	trashed               map[string]string // base name -> content
}

var jBase = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newJournalPair(t *testing.T) *journalPair {
	t.Helper()
	// bisync names its listings after both paths; a short root keeps them
	// below the file name limit
	root, err := os.MkdirTemp("", "jnl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := &journalPair{t: t, path1: filepath.Join(root, "p1"), path2: filepath.Join(root, "p2"),
		workdir: filepath.Join(root, "wd"), trashed: map[string]string{}}
	require.NoError(t, os.MkdirAll(p.path1, 0o755))
	require.NoError(t, os.MkdirAll(p.path2, 0o755))
	p.fs1, err = local.NewFs(context.Background(), "local", p.path1, configmap.Simple{})
	require.NoError(t, err)
	p.fs2, err = local.NewFs(context.Background(), "local", p.path2, configmap.Simple{})
	require.NoError(t, err)
	LocalTrash = func(path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		p.trashed[filepath.Base(path)] = string(b)
		return os.Remove(path)
	}
	t.Cleanup(func() { LocalTrash = nil })
	return p
}

func (p *journalPair) write(root, rel, content string, mtime time.Time) {
	p.t.Helper()
	f := filepath.Join(root, rel)
	require.NoError(p.t, os.MkdirAll(filepath.Dir(f), 0o755))
	require.NoError(p.t, os.WriteFile(f, []byte(content), 0o644))
	require.NoError(p.t, os.Chtimes(f, mtime, mtime))
}

func (p *journalPair) read(root, rel string) string {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(p.t, err)
	return string(b)
}

func (p *journalPair) run(resync bool) error {
	p.t.Helper()
	opt := &Options{
		Workdir:            p.workdir,
		Resync:             resync,
		CreateEmptySrcDirs: true,
		Resilient:          true,
		Recover:            true,
		MaxDelete:          DefaultMaxDelete,
		CompareFlag:        "size,modtime",
		ConflictResolve:    PreferPath1,
		ConflictLoser:      ConflictLoserPathname,
		ConflictSuffixFlag: "lokal,Konflikt",
		Journal:            true,
		SettleTime:         rfs.Duration(p.settle),
	}
	p.runs++
	ctx := accounting.WithStatsGroup(context.Background(), fmt.Sprintf("%s-%d", p.t.Name(), p.runs))
	return Bisync(ctx, p.fs1, p.fs2, opt)
}

// conflicts lists the Conflict Copies on both sides.
func (p *journalPair) conflicts() []string {
	var out []string
	for _, root := range []string{p.path1, p.path2} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && strings.Contains(d.Name(), ".Konflikt") {
				out = append(out, path)
			}
			return nil
		})
	}
	return out
}

// forgetLastRun replaces the listings with the ones before the last run, as
// an aborted run (or --recover from the backup listings) leaves them.
func (p *journalPair) forgetLastRun() {
	p.t.Helper()
	for _, side := range []string{"path1", "path2"} {
		old, err := filepath.Glob(filepath.Join(p.workdir, "*."+side+".lst-old"))
		require.NoError(p.t, err)
		require.Len(p.t, old, 1)
		b, err := os.ReadFile(old[0])
		require.NoError(p.t, err)
		require.NoError(p.t, os.WriteFile(strings.TrimSuffix(old[0], "-old"), b, 0o600))
	}
}

// synced starts a pair whose sides hold the given files, in sync.
func synced(t *testing.T, files ...string) *journalPair {
	p := newJournalPair(t)
	for _, f := range files {
		p.write(p.path2, f, "v1 "+f, jBase)
	}
	require.NoError(t, p.run(true))
	return p
}

// The incident of 2026-11-02: an upload whose record was lost (aborted run,
// recover from older listings) looked like a change in the cloud; the next
// local save made it "changed on both sides" and a Conflict Copy of the
// user's own older file.
func TestJournalOwnUploadWithLostListingsIsNoConflict(t *testing.T) {
	p := synced(t, "Song.logicx/ProjectData")
	p.write(p.path1, "Song.logicx/ProjectData", "v2 comp", jBase.Add(time.Hour))
	require.NoError(t, p.run(false))
	require.Equal(t, "v2 comp", p.read(p.path2, "Song.logicx/ProjectData"))

	p.forgetLastRun()
	p.write(p.path1, "Song.logicx/ProjectData", "v3 more takes", jBase.Add(2*time.Hour))
	require.NoError(t, p.run(false))

	assert.Empty(t, p.conflicts())
	assert.Equal(t, "v3 more takes", p.read(p.path2, "Song.logicx/ProjectData"))
	assert.Equal(t, "v3 more takes", p.read(p.path1, "Song.logicx/ProjectData"))
	assert.Empty(t, p.trashed)
}

// Logic Pro can save newer content with an older modtime; a resync must not
// let CloudWire's own earlier upload replace it (--resync-mode newer did).
func TestJournalResyncKeepsNewerLocalContentWithOlderModtime(t *testing.T) {
	p := synced(t, "Song.logicx/ProjectData")
	p.write(p.path1, "Song.logicx/ProjectData", "v2", jBase.Add(2*time.Hour))
	require.NoError(t, p.run(false))

	p.write(p.path1, "Song.logicx/ProjectData", "v3 regions", jBase.Add(time.Hour))
	require.NoError(t, p.run(true))

	assert.Equal(t, "v3 regions", p.read(p.path1, "Song.logicx/ProjectData"))
	assert.Equal(t, "v3 regions", p.read(p.path2, "Song.logicx/ProjectData"))
	assert.Empty(t, p.conflicts())
	assert.Empty(t, p.trashed)
}

func TestJournalCloudChangeReplacesUnchangedLocalFile(t *testing.T) {
	p := synced(t, "a.txt", "b.txt")
	p.write(p.path2, "a.txt", "v2 from another device", jBase.Add(time.Hour))
	require.NoError(t, p.run(false))

	assert.Equal(t, "v2 from another device", p.read(p.path1, "a.txt"))
	assert.Equal(t, map[string]string{"a.txt": "v1 a.txt"}, p.trashed, "the replaced local version goes to the Trash")
	assert.Empty(t, p.conflicts())
}

func TestJournalCloudChangeAndLocalChangeMakeAConflictCopy(t *testing.T) {
	p := synced(t, "a.txt", "b.txt")
	p.write(p.path2, "a.txt", "cloud edit", jBase.Add(time.Hour))
	p.write(p.path1, "a.txt", "local edit!", jBase.Add(2*time.Hour))
	require.NoError(t, p.run(false))

	assert.Equal(t, "local edit!", p.read(p.path1, "a.txt"))
	assert.Equal(t, "local edit!", p.read(p.path2, "a.txt"))
	assert.Len(t, p.conflicts(), 2, "the cloud version is kept beside the file on both sides")
}

func TestJournalCloudDeletionMovesLocalFileToTrash(t *testing.T) {
	p := synced(t, "a.txt", "b.txt", "c.txt")
	require.NoError(t, os.Remove(filepath.Join(p.path2, "b.txt")))
	require.NoError(t, p.run(false))

	assert.NoFileExists(t, filepath.Join(p.path1, "b.txt"))
	assert.Equal(t, map[string]string{"b.txt": "v1 b.txt"}, p.trashed)
}

// A cloud file CloudWire never confirmed (an upload that failed and was
// removed by rclone, "sha1 hashes differ") is not deleted by anyone else:
// the local file is uploaded again, never deleted.
func TestJournalUnprovenCloudDeletionIsUploadedAgain(t *testing.T) {
	p := synced(t, "a.txt", "b.txt", "Bass #41.aif")
	jnl, err := filepath.Glob(filepath.Join(p.workdir, "*.jnl"))
	require.NoError(t, err)
	require.Len(t, jnl, 1)
	f, err := os.OpenFile(jnl[0], os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"p":"Bass #41.aif","x":true}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(filepath.Join(p.path2, "Bass #41.aif")))

	require.NoError(t, p.run(false))

	assert.Equal(t, "v1 Bass #41.aif", p.read(p.path1, "Bass #41.aif"))
	assert.Equal(t, "v1 Bass #41.aif", p.read(p.path2, "Bass #41.aif"))
	assert.Empty(t, p.trashed)
}

// An item that synced before the journal existed: whatever the cloud holds
// counts as CloudWire's own until the first run recorded it.
func TestJournalMigrationKeepsLocalVersionSilently(t *testing.T) {
	p := synced(t, "a.txt", "b.txt")
	jnl, err := filepath.Glob(filepath.Join(p.workdir, "*.jnl"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(jnl[0]))
	p.write(p.path2, "a.txt", "older upload", jBase.Add(time.Hour))
	p.write(p.path1, "a.txt", "newest local!", jBase.Add(2*time.Hour))
	require.NoError(t, p.run(false))

	assert.Empty(t, p.conflicts())
	assert.Equal(t, "newest local!", p.read(p.path2, "a.txt"))

	// After the migration run, a change by someone else is a Cloud Change again.
	p.write(p.path2, "b.txt", "v2 from another device", jBase.Add(3*time.Hour))
	require.NoError(t, p.run(false))
	assert.Equal(t, "v2 from another device", p.read(p.path1, "b.txt"))
}

// A new Offline Item has no history: where both sides differ, the local
// version wins and the cloud version is kept once as a Conflict Copy.
func TestJournalFirstSyncKeepsCloudVersionAsConflictCopy(t *testing.T) {
	p := newJournalPair(t)
	p.write(p.path1, "a.txt", "local version", jBase.Add(time.Hour))
	p.write(p.path2, "a.txt", "cloud version!", jBase.Add(2*time.Hour))
	p.write(p.path2, "only cloud.txt", "c", jBase)
	require.NoError(t, p.run(true))

	assert.Equal(t, "local version", p.read(p.path1, "a.txt"))
	assert.Equal(t, "local version", p.read(p.path2, "a.txt"))
	assert.Equal(t, "c", p.read(p.path1, "only cloud.txt"))
	copies := p.conflicts()
	require.Len(t, copies, 2)
	for _, c := range copies {
		b, err := os.ReadFile(c)
		require.NoError(t, err)
		assert.Equal(t, "cloud version!", string(b))
	}
}

// A take still being recorded is not uploaded half-written; it stays a
// local change until it settled.
func TestJournalSettleTimeDefersFilesStillBeingWritten(t *testing.T) {
	p := synced(t, "a.txt")
	p.settle = time.Minute
	p.write(p.path1, "Audio Files/Bass #63.aif", "half a take", time.Now())
	require.NoError(t, p.run(false))
	assert.NoFileExists(t, filepath.Join(p.path2, "Audio Files/Bass #63.aif"))

	p.write(p.path1, "Audio Files/Bass #63.aif", "the whole take", time.Now().Add(-2*time.Minute))
	require.NoError(t, p.run(false))
	assert.Equal(t, "the whole take", p.read(p.path2, "Audio Files/Bass #63.aif"))
}

// etagObjFs is a local Path2 whose files report an ETag, as Nextcloud's do.
type etagObjFs struct {
	rfs.Fs
	features *rfs.Features
}

func newEtagObjFs(t *testing.T, root string) *etagObjFs {
	t.Helper()
	f, err := local.NewFs(context.Background(), "local", root, configmap.Simple{})
	require.NoError(t, err)
	e := &etagObjFs{Fs: f}
	features := *f.Features()
	features.ListR = nil
	features.ListP = e.ListP
	e.features = &features
	return e
}

func (e *etagObjFs) Features() *rfs.Features { return e.features }

func (e *etagObjFs) List(ctx context.Context, dir string) (rfs.DirEntries, error) {
	return list.WithListP(ctx, dir, e)
}

func (e *etagObjFs) ListP(ctx context.Context, dir string, callback rfs.ListRCallback) error {
	entries, err := e.Fs.List(ctx, dir)
	if err != nil {
		return err
	}
	for i, de := range entries {
		if o, ok := de.(rfs.Object); ok {
			entries[i] = etagObject{o}
		}
	}
	return callback(entries)
}

func (e *etagObjFs) NewObject(ctx context.Context, remote string) (rfs.Object, error) {
	o, err := e.Fs.NewObject(ctx, remote)
	if err != nil {
		return nil, err
	}
	return etagObject{o}, nil
}

type etagObject struct{ rfs.Object }

func (o etagObject) ETag() string {
	return fmt.Sprintf(`"%d-%d"`, o.Size(), o.ModTime(context.Background()).UnixNano())
}

// Path2 ETags are journal data; the listings bisync reads back must stay
// readable (loadListing drops lines whose id column is not "-").
func TestJournalPath2ETagsKeepListingsReadable(t *testing.T) {
	p := newJournalPair(t)
	p.fs2 = newEtagObjFs(t, p.path2)
	p.write(p.path2, "a.txt", "v1 a", jBase)
	p.write(p.path2, "b.txt", "v1 b", jBase)
	require.NoError(t, p.run(true))
	require.NoError(t, p.run(false))
	p.write(p.path1, "a.txt", "v2 local", jBase.Add(time.Hour))
	require.NoError(t, p.run(false))
	assert.Equal(t, "v2 local", p.read(p.path2, "a.txt"))

	jnl, err := filepath.Glob(filepath.Join(p.workdir, "*.jnl"))
	require.NoError(t, err)
	require.Len(t, jnl, 1)
	b, err := os.ReadFile(jnl[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"ce":"4-`, "the journal records the cloud ETag")
}

func TestRestoreFileNeverReplacesAFileThatAppeared(t *testing.T) {
	dir := t.TempDir()
	staged, orig := filepath.Join(dir, "staged"), filepath.Join(dir, "Audio Files", "Bass #54.aif")
	require.NoError(t, os.WriteFile(staged, []byte("the take"), 0o644))
	require.NoError(t, os.Chtimes(staged, jBase, jBase))
	require.NoError(t, restoreFile(staged, orig))
	b, err := os.ReadFile(orig)
	require.NoError(t, err)
	assert.Equal(t, "the take", string(b))
	info, err := os.Stat(orig)
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(jBase))
	assert.NoFileExists(t, staged)

	require.NoError(t, os.WriteFile(staged, []byte("older"), 0o644))
	require.ErrorIs(t, restoreFile(staged, orig), os.ErrExist)
	b, err = os.ReadFile(orig)
	require.NoError(t, err)
	assert.Equal(t, "the take", string(b))
	assert.FileExists(t, staged)
}
