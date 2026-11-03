// CloudWire patch (see docs/adr/0012): incremental Path2 listing.
//
// A bisync run without changes against Nextcloud spends almost all of its
// time listing Path2, one PROPFIND per folder. Nextcloud changes a folder's
// ETag whenever anything below it changes, so a folder whose ETag and file
// id still match the previous listing still holds the entries that listing
// recorded. The listing cache keeps the raw (unfiltered) Path2 entries of
// every folder the march listed, together with the folder's ETag and file
// id as its parent's listing reported them before the folder itself was
// read: a change in between only makes the markers differ, never hides it.
//
// Lifecycle: every run that is not a dry run first removes the cache file
// and writes a new one only when it ends successfully, so after a failure,
// panic, kill, expired lock, --recover or --resync the next run lists
// everything. Folders that are mount points (external storage, shares), or
// contain one, may not propagate ETags and are always listed. The march is
// the only consumer: every later phase (copies, deletes, conflict checks,
// recheck) works on the real Path2 Fs.
//
// Upstream issue filed by CloudWire.

package bisync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/cmd/bisync/bilib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/list"
)

// ListingCacheMode controls the incremental Path2 listing.
type ListingCacheMode int

// ListingCache modes
const (
	ListingCacheOff     ListingCacheMode = iota // list Path2 completely, keep no cache (default)
	ListingCacheRefresh                         // list Path2 completely and renew the cache
	ListingCacheUse                             // reuse unchanged Path2 folders from the cache, then renew it
)

func (x ListingCacheMode) String() string {
	switch x {
	case ListingCacheOff:
		return "off"
	case ListingCacheRefresh:
		return "refresh"
	case ListingCacheUse:
		return "use"
	}
	return "unknown"
}

// Set a ListingCache mode from a string
func (x *ListingCacheMode) Set(s string) error {
	switch strings.ToLower(s) {
	case "off":
		*x = ListingCacheOff
	case "refresh":
		*x = ListingCacheRefresh
	case "use":
		*x = ListingCacheUse
	default:
		return fmt.Errorf("unknown listing-cache mode for bisync: %q", s)
	}
	return nil
}

// Type of the ListingCache value
func (x *ListingCacheMode) Type() string {
	return "string"
}

const listCacheVersion = 1

type listCacheFile struct {
	Version int                      `json:"version"`
	Fs      string                   `json:"fs"`
	Dirs    map[string]*listCacheDir `json:"dirs"`
}

// listCacheDir is one listed Path2 folder.
type listCacheDir struct {
	// ETag and FileID of the folder as its parent's listing reported them
	// before the folder was read ("" for the root).
	ETag   string `json:"etag,omitempty"`
	FileID string `json:"fileid,omitempty"`
	// SubtreeMount is set when an entry, or an entry of a listed subfolder,
	// is a mount point: such a folder is never reused.
	SubtreeMount bool             `json:"subtreeMount,omitempty"`
	Entries      []listCacheEntry `json:"entries"`
}

type listCacheEntry struct {
	Name    string `json:"n"`
	Dir     bool   `json:"d,omitempty"`
	Size    int64  `json:"s,omitempty"`
	ModTime *int64 `json:"t,omitempty"` // Unix nanoseconds; nil for the zero time
	ETag    string `json:"e,omitempty"`
	FileID  string `json:"i,omitempty"`
	Mounted bool   `json:"m,omitempty"`
}

func (e listCacheEntry) modTime() time.Time {
	if e.ModTime == nil {
		return time.Time{}
	}
	return time.Unix(0, *e.ModTime)
}

// changeMarked is a directory that carries the server's change markers
// (backend/webdav Directory).
type changeMarked interface {
	ETag() string
	FileID() string
	Mounted() bool
}

// listCachePath is the cache file. Its name and its "-new" temp file are no
// longer than bisync's own "*.path2.lst-new", which already has to fit the
// file name limit with long base paths.
func (b *bisyncRun) listCachePath() string {
	return b.basePath + ".path2.lsc"
}

// startListCache runs at the start of every run. It removes the cache file,
// so only a successful run leaves one behind, and sets up b.listCache for the
// march when the cache is enabled.
func (b *bisyncRun) startListCache() {
	if b.opt.DryRun {
		return
	}
	file := b.listCachePath()
	usable := b.opt.ListingCache != ListingCacheOff && !b.opt.Resync &&
		b.opt.Compare.HashType2 == hash.None && !b.opt.Compare.DownloadHash
	var old map[string]*listCacheDir
	if usable && b.opt.ListingCache == ListingCacheUse {
		old = loadListCache(file, bilib.FsPath(b.fs2))
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		fs.Errorf(nil, "cannot remove listing cache %s: %v", file, err)
		return
	}
	if usable {
		b.listCache = newListCacheFs(b.fs2, old)
	}
}

// marchFs2 is the Path2 Fs the march lists.
func (b *bisyncRun) marchFs2() fs.Fs {
	if b.listCache != nil {
		return b.listCache
	}
	return b.fs2
}

// saveListCache writes the cache of a successful run.
func (b *bisyncRun) saveListCache() {
	if b.listCache == nil || b.abort || b.critical || b.InGracefulShutdown {
		return
	}
	live, reused := b.listCache.counts()
	fs.Infof(nil, "Path2 listing: %d folders listed, %d reused from the listing cache", live, reused)
	if err := b.listCache.save(b.listCachePath(), bilib.FsPath(b.fs2)); err != nil {
		fs.Errorf(nil, "cannot save listing cache: %v", err)
	}
}

func loadListCache(file, fsPath string) map[string]*listCacheDir {
	data, err := os.ReadFile(file)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fs.Logf(nil, "cannot read listing cache %s: %v", file, err)
		}
		return nil
	}
	var c listCacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		fs.Logf(nil, "ignoring unreadable listing cache %s: %v", file, err)
		return nil
	}
	if c.Version != listCacheVersion || c.Fs != fsPath {
		return nil
	}
	return c.Dirs
}

// listCacheFs wraps Path2 for the march: it records every listing and serves
// folders that did not change from the previous run's records.
type listCacheFs struct {
	fs.Fs
	features *fs.Features
	old      map[string]*listCacheDir // nil: list everything

	mu      sync.Mutex
	next    map[string]*listCacheDir
	trusted map[string]bool           // folders whose old record may be served
	marks   map[string]listCacheEntry // folder -> its entry in its parent's listing
	live    int
	reused  int
}

func newListCacheFs(f fs.Fs, old map[string]*listCacheDir) *listCacheFs {
	w := &listCacheFs{
		Fs:      f,
		old:     old,
		next:    map[string]*listCacheDir{},
		trusted: map[string]bool{},
		marks:   map[string]listCacheEntry{},
	}
	features := *f.Features()
	features.ListR = nil
	features.ListP = w.ListP
	w.features = &features
	return w
}

// Features returns the features of Path2 with listing routed through the cache.
func (w *listCacheFs) Features() *fs.Features { return w.features }

// List lists dir through the cache.
func (w *listCacheFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return list.WithListP(ctx, dir, w)
}

// ListP serves dir from the previous run if it is unchanged, else lists it.
func (w *listCacheFs) ListP(ctx context.Context, dir string, callback fs.ListRCallback) error {
	w.mu.Lock()
	var rec *listCacheDir
	if w.old != nil && dir != "" && w.trusted[dir] {
		rec = w.old[dir]
	}
	if rec != nil {
		w.next[dir] = rec
		w.reused++
		// Everything below an unchanged folder is unchanged.
		for _, e := range rec.Entries {
			if e.Dir {
				child := path.Join(dir, e.Name)
				w.marks[child] = e
				o := w.old[child]
				w.trusted[child] = o != nil && o.ETag == e.ETag && o.FileID == e.FileID
			}
		}
	}
	w.mu.Unlock()
	if rec != nil {
		return callback(w.entries(dir, rec))
	}
	return w.listLive(ctx, dir, callback)
}

func (w *listCacheFs) listLive(ctx context.Context, dir string, callback fs.ListRCallback) error {
	var entries []listCacheEntry
	err := w.innerListP(ctx, dir, func(des fs.DirEntries) error {
		for _, de := range des {
			e := listCacheEntry{Name: path.Base(de.Remote())}
			if t := de.ModTime(ctx); !t.IsZero() {
				n := t.UnixNano()
				e.ModTime = &n
			}
			switch x := de.(type) {
			case fs.Directory:
				e.Dir = true
				if m, ok := de.(changeMarked); ok {
					e.ETag, e.FileID, e.Mounted = m.ETag(), m.FileID(), m.Mounted()
				}
			case fs.Object:
				e.Size = x.Size()
			}
			entries = append(entries, e)
		}
		return callback(des)
	})
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	rec := &listCacheDir{Entries: entries}
	if m, ok := w.marks[dir]; ok {
		rec.ETag, rec.FileID = m.ETag, m.FileID
	}
	w.next[dir] = rec
	w.live++
	// Decided before the march queues the subfolders, which happens only
	// after this listing returned.
	for _, e := range entries {
		if !e.Dir {
			continue
		}
		child := path.Join(dir, e.Name)
		w.marks[child] = e
		o := w.old[child]
		w.trusted[child] = o != nil && e.ETag != "" && e.FileID != "" && !e.Mounted &&
			o.ETag == e.ETag && o.FileID == e.FileID && !o.SubtreeMount
	}
	return nil
}

func (w *listCacheFs) innerListP(ctx context.Context, dir string, callback fs.ListRCallback) error {
	if listP := w.Fs.Features().ListP; listP != nil {
		return listP(ctx, dir, callback)
	}
	entries, err := w.Fs.List(ctx, dir)
	if err != nil {
		return err
	}
	return callback(entries)
}

func (w *listCacheFs) entries(dir string, rec *listCacheDir) fs.DirEntries {
	out := make(fs.DirEntries, 0, len(rec.Entries))
	for _, e := range rec.Entries {
		remote := path.Join(dir, e.Name)
		if e.Dir {
			out = append(out, fs.NewDir(remote, e.modTime()))
		} else {
			out = append(out, &cachedObject{f: w.Fs, remote: remote, size: e.Size, modTime: e.modTime()})
		}
	}
	return out
}

func (w *listCacheFs) counts() (live, reused int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.live, w.reused
}

// save writes the records of this run atomically.
func (w *listCacheFs) save(file, fsPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Deepest folders first, so each folder sees its subfolders' flags.
	keys := make([]string, 0, len(w.next))
	for k := range w.next {
		keys = append(keys, k)
	}
	depth := func(k string) int {
		if k == "" {
			return -1
		}
		return strings.Count(k, "/")
	}
	slices.SortFunc(keys, func(a, b string) int { return depth(b) - depth(a) })
	for _, k := range keys {
		rec := w.next[k]
		mount := false
		for _, e := range rec.Entries {
			if e.Mounted {
				mount = true
			} else if e.Dir {
				if c := w.next[path.Join(k, e.Name)]; c != nil && c.SubtreeMount {
					mount = true
				}
			}
		}
		rec.SubtreeMount = mount
	}
	data, err := json.Marshal(listCacheFile{Version: listCacheVersion, Fs: fsPath, Dirs: w.next})
	if err != nil {
		return err
	}
	// The lock file guarantees a single writer.
	tmp := file + "-new"
	err = os.WriteFile(tmp, data, bilib.PermSecure)
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// cachedObject is a Path2 file served from the listing cache. The march only
// reads its name, size and modtime; anything else goes to the real object.
type cachedObject struct {
	f       fs.Fs
	remote  string
	size    int64
	modTime time.Time
}

func (o *cachedObject) real(ctx context.Context) (fs.Object, error) {
	return o.f.NewObject(ctx, o.remote)
}

func (o *cachedObject) Fs() fs.Info                       { return o.f }
func (o *cachedObject) String() string                    { return o.remote }
func (o *cachedObject) Remote() string                    { return o.remote }
func (o *cachedObject) ModTime(context.Context) time.Time { return o.modTime }
func (o *cachedObject) Size() int64                       { return o.size }
func (o *cachedObject) Storable() bool                    { return true }
func (o *cachedObject) Hash(ctx context.Context, t hash.Type) (string, error) {
	r, err := o.real(ctx)
	if err != nil {
		return "", err
	}
	return r.Hash(ctx, t)
}

func (o *cachedObject) SetModTime(ctx context.Context, t time.Time) error {
	r, err := o.real(ctx)
	if err != nil {
		return err
	}
	return r.SetModTime(ctx, t)
}

func (o *cachedObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.real(ctx)
	if err != nil {
		return nil, err
	}
	return r.Open(ctx, options...)
}

func (o *cachedObject) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	r, err := o.real(ctx)
	if err != nil {
		return err
	}
	return r.Update(ctx, in, src, options...)
}

func (o *cachedObject) Remove(ctx context.Context) error {
	r, err := o.real(ctx)
	if err != nil {
		return err
	}
	return r.Remove(ctx)
}

// Metadata returns the real object's metadata (metadata filters).
func (o *cachedObject) Metadata(ctx context.Context) (fs.Metadata, error) {
	r, err := o.real(ctx)
	if err != nil {
		return nil, err
	}
	return fs.GetMetadata(ctx, r)
}

var (
	_ fs.Object     = (*cachedObject)(nil)
	_ fs.Metadataer = (*cachedObject)(nil)
	_ fs.ListPer    = (*listCacheFs)(nil)
)
