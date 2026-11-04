package bisync

// CloudWire patch (see docs/adr/0012, docs/adr/0014): the transfer journal.
//
// bisync's listings are written once per run, so an aborted, interrupted or
// killed run leaves them without the record of what it already uploaded.
// The next run (or a --recover from the previous listings) then takes
// CloudWire's own uploads for changes made in the cloud: "changed on both
// sides" became a Conflict Copy of the user's own file, and a resync with
// --resync-mode newer let an older upload replace a newer local file.
//
// The journal is a per-file record, written as each transfer completes, of
// the state both sides had when CloudWire last brought them in line: local
// size and modtime, cloud size, modtime and ETag, plus the expected cloud
// state of an upload in flight. A Path2 change counts as a Cloud Change only
// if the journal cannot explain it; without that proof the local version
// wins. Local files that a sync replaces or deletes go to the macOS Trash.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	gosync "sync"
	"time"

	"github.com/rclone/rclone/cmd/bisync/bilib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/lib/terminal"
	"golang.org/x/text/unicode/norm"
)

// LeftForLaterRun is logged when a run left local files that are still being
// written for a later run; the caller schedules that run.
const LeftForLaterRun = "files left for a later run"

// TransfersFailed is logged when files could not be transferred; they are
// rolled back in the listings and retried, and the caller reports it.
const TransfersFailed = "transfers failed and are retried"

// LocalTrash moves a Path1 file that a sync replaced or deleted to the
// user's Trash. CloudWire sets it to the macOS Trash; while it is nil the
// replaced files stay in the item's staging folder.
var LocalTrash func(path string) error

// jState is one side's state of a file.
type jState struct {
	Size int64
	Time time.Time
	ETag string
}

func (s jState) known() bool { return !s.Time.IsZero() || s.Size != 0 || s.ETag != "" }

// jEntry is what the journal knows about one file, keyed by its Path2 name.
type jEntry struct {
	L jState  // Path1 when both sides were last in line
	C jState  // Path2 when both sides were last in line
	P *jState // expected Path2 state of an upload in flight
}

type jHeader struct {
	Journal int  `json:"cloudwireJournal"`
	Seeded  bool `json:"seeded"`
}

type jLine struct {
	P  string `json:"p"`
	X  bool   `json:"x,omitempty"` // entry removed
	LS int64  `json:"ls,omitempty"`
	LT int64  `json:"lt,omitempty"`
	CS int64  `json:"cs,omitempty"`
	CT int64  `json:"ct,omitempty"`
	CE string `json:"ce,omitempty"`
	PS *int64 `json:"ps,omitempty"`
	PT int64  `json:"pt,omitempty"`
}

const journalVersion = 1

type journal struct {
	mu      gosync.Mutex
	path    string
	f       *os.File
	entries map[string]*jEntry
	// seeded is false while an item that synced before the journal existed
	// is migrating: until its first successful run recorded the files that
	// are equal on both sides, a file without an entry is not a Cloud Change.
	seeded  bool
	window  time.Duration
	touched map[string]bool // names this run wrote or removed
	done    map[string]bool // Path1 names this run downloaded
	failed  error           // first write error, reported once
}

// jKey is the journal key of a Path2 name. Nextcloud stores names in NFC;
// a file uploaded under its local NFD name is listed in NFC afterwards.
func jKey(name string) string {
	return norm.NFC.String(name)
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromUnixNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func (l jLine) entry() *jEntry {
	e := &jEntry{
		L: jState{Size: l.LS, Time: fromUnixNano(l.LT)},
		C: jState{Size: l.CS, Time: fromUnixNano(l.CT), ETag: l.CE},
	}
	if l.PS != nil {
		e.P = &jState{Size: *l.PS, Time: fromUnixNano(l.PT)}
	}
	return e
}

func entryLine(name string, e *jEntry) jLine {
	l := jLine{P: name, LS: e.L.Size, LT: unixNano(e.L.Time), CS: e.C.Size, CT: unixNano(e.C.Time), CE: e.C.ETag}
	if e.P != nil {
		ps := e.P.Size
		l.PS, l.PT = &ps, unixNano(e.P.Time)
	}
	return l
}

// openJournal loads the journal at path. A missing journal starts empty:
// seeded when the item has no sync history (a new Offline Item), unseeded
// when it synced before the journal existed (migration). An unreadable
// header is treated like a migration: unproven changes keep the local file.
func openJournal(path string, hasHistory bool, window time.Duration) (*journal, error) {
	j := &journal{path: path, entries: map[string]*jEntry{}, window: window, touched: map[string]bool{}, done: map[string]bool{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		j.seeded = !hasHistory
		if err := j.rewrite(); err != nil {
			return nil, err
		}
		return j, nil
	case err != nil:
		return nil, fmt.Errorf("read journal: %w", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			first = false
			var h jHeader
			if json.Unmarshal(line, &h) == nil && h.Journal == journalVersion {
				j.seeded = h.Seeded
				continue
			}
			fs.Logf(nil, "Journal %s has no valid header; treating unproven changes as local", path)
		}
		var l jLine
		if err := json.Unmarshal(line, &l); err != nil || l.P == "" {
			// a line cut off by a crash; everything before it is valid
			fs.Debugf(nil, "Skipping unreadable journal line: %q", line)
			continue
		}
		if l.X {
			delete(j.entries, l.P)
		} else {
			j.entries[l.P] = l.entry()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, bilib.PermSecure)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	j.f = f
	return j, nil
}

func (j *journal) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f != nil {
		_ = j.f.Close()
		j.f = nil
	}
}

// appendLocked writes one record. Each line is written in one call, so a
// crash can only cut off the last line, which openJournal skips.
func (j *journal) appendLocked(l jLine) {
	if j.f == nil {
		return
	}
	b, err := json.Marshal(l)
	if err == nil {
		_, err = j.f.Write(append(b, '\n'))
	}
	if err != nil && j.failed == nil {
		j.failed = err
		fs.Errorf(nil, "Cannot write the journal %s: %v", j.path, err)
	}
}

func (j *journal) get(name string) (jEntry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.entries[jKey(name)]
	if !ok {
		return jEntry{}, false
	}
	return *e, true
}

func (j *journal) set(name string, e jEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	name = jKey(name)
	j.entries[name] = &e
	j.touched[name] = true
	j.appendLocked(entryLine(name, &e))
}

func (j *journal) remove(name string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	name = jKey(name)
	j.touched[name] = true
	if _, ok := j.entries[name]; !ok {
		return
	}
	delete(j.entries, name)
	j.appendLocked(jLine{P: name, X: true})
}

// setPending records the expected Path2 state of an upload before it starts,
// so an upload that lands but is never confirmed (the process is killed) is
// still recognised as CloudWire's own.
func (j *journal) setPending(name string, s jState) {
	j.mu.Lock()
	defer j.mu.Unlock()
	name = jKey(name)
	e, ok := j.entries[name]
	if !ok {
		e = &jEntry{}
		j.entries[name] = e
	}
	p := s
	e.P = &p
	j.touched[name] = true
	j.appendLocked(entryLine(name, e))
}

func (j *journal) markDownloaded(path1Name string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done[path1Name] = true
}

func (j *journal) downloaded(path1Name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done[path1Name]
}

func (j *journal) sameState(a, b jState, etag bool) bool {
	if a.Size != b.Size {
		return false
	}
	dt := a.Time.Sub(b.Time)
	if dt >= j.window || dt <= -j.window {
		return false
	}
	return !etag || a.ETag == "" || b.ETag == "" || a.ETag == b.ETag
}

// ownCloud reports whether cur is a Path2 state CloudWire itself produced:
// the last state both sides were in line with, or an upload in flight.
func (j *journal) ownCloud(e jEntry, cur jState) bool {
	if e.C.known() && j.sameState(e.C, cur, true) {
		return true
	}
	return e.P != nil && j.sameState(*e.P, cur, false)
}

// sameLocal reports whether the Path1 file is unchanged since both sides
// were last in line.
func (j *journal) sameLocal(e jEntry, cur jState) bool {
	return e.L.known() && j.sameState(e.L, cur, false)
}

// rewrite replaces the file with the current entries, atomically.
func (j *journal) rewrite() error {
	names := make([]string, 0, len(j.entries))
	for name := range j.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, bilib.PermSecure)
	if err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	err = enc.Encode(jHeader{Journal: journalVersion, Seeded: j.seeded})
	for _, name := range names {
		if err != nil {
			break
		}
		err = enc.Encode(entryLine(name, j.entries[name]))
	}
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, j.path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write journal: %w", err)
	}
	if j.f != nil {
		_ = j.f.Close()
	}
	j.f, err = os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND, bilib.PermSecure)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	return nil
}

// --- bisync integration ---

type etagger interface{ ETag() string }

// objectETag returns the ETag a Path2 entry reports, without quotes or weak
// marker; "" if it has none.
func objectETag(o any) string {
	e, ok := o.(etagger)
	if !ok {
		return ""
	}
	s := strings.TrimPrefix(e.ETag(), "W/")
	return strings.Trim(s, `"`)
}

// cloudState is a Path2 file's state at the start of the run.
func (b *bisyncRun) cloudState(f2 string) jState {
	ls := b.march.ls2
	b.march.marchLsLock.Lock()
	etag := b.march.etags2[f2]
	b.march.marchLsLock.Unlock()
	return jState{Size: ls.getSize(f2), Time: ls.getTime(f2), ETag: etag}
}

func localState(ls *fileList, f1 string) jState {
	return jState{Size: ls.getSize(f1), Time: ls.getTime(f1)}
}

func objState(ctx context.Context, o fs.Object) jState {
	return jState{Size: o.Size(), Time: o.ModTime(ctx), ETag: objectETag(o)}
}

// hasHistory reports whether the item synced before: any listing of it exists.
func (b *bisyncRun) hasHistory() bool {
	for _, l := range []string{b.listing1, b.listing2} {
		for _, suffix := range []string{"", "-old", "-err", "-new"} {
			if bilib.FileExists(l + suffix) {
				return true
			}
		}
	}
	return false
}

func (b *bisyncRun) openJournal(ctx context.Context) error {
	if !b.opt.Journal || b.opt.DryRun {
		return nil
	}
	window := fs.GetModifyWindow(ctx, b.fs1, b.fs2)
	if window <= 0 || window == fs.ModTimeNotSupported {
		window = time.Second
	}
	j, err := openJournal(b.basePath+".jnl", b.hasHistory(), window)
	if err != nil {
		return err
	}
	b.journal = j
	if !j.seeded {
		fs.Infof(nil, "Journal: first run since the journal was introduced; changes in the cloud that cannot be proven keep the local file")
	}
	b.trashRun = filepath.Join(b.trashDir(), time.Now().Format("20060102T150405.000000000"))
	b.opt.BackupDir1 = b.trashRun
	b.trashDeleted = bilib.Names{}
	return nil
}

// keepGoing reports whether a journal run continues after a copy batch
// failed. Per-file errors (a file changed while it was copied, a checksum
// mismatch) only roll those files back in the listings; the run must not
// abort and lose its listings over them.
func (b *bisyncRun) keepGoing(err error) bool {
	if b.journal == nil || fserrors.IsFatalError(err) || errors.Is(err, context.Canceled) {
		return false
	}
	if b.transferErr == nil {
		b.transferErr = err
	}
	fs.Errorf(nil, "Some files could not be transferred; they stay as they are and will be retried: %v", err)
	return true
}

// journalTransfer records a completed copy (operations.TransferDoneFn).
func (b *bisyncRun) journalTransfer(ctx context.Context, is1to2 bool, src, dst fs.Object) {
	if is1to2 {
		b.journal.set(dst.Remote(), jEntry{L: objState(ctx, src), C: objState(ctx, dst)})
	} else {
		b.journal.set(src.Remote(), jEntry{L: objState(ctx, dst), C: objState(ctx, src)})
		b.journal.markDownloaded(dst.Remote())
	}
}

func (b *bisyncRun) withJournal(ctx context.Context, fsrc fs.Fs) context.Context {
	if b.journal == nil {
		return ctx
	}
	is1to2 := fsrc == b.fs1
	return operations.WithTransferDoneFn(ctx, func(ctx context.Context, src, dst fs.Object) {
		b.journalTransfer(ctx, is1to2, src, dst)
	})
}

// cloudDeleted checks with a fresh request that a Path2 file the listing
// lacks is really gone: a stale listing cache or a flaky listing must never
// delete a local file.
func (b *bisyncRun) cloudDeleted(ctx context.Context, name string) bool {
	_, err := b.fs2.NewObject(ctx, name)
	return errors.Is(err, fs.ErrorObjectNotFound) || errors.Is(err, fs.ErrorIsDir)
}

// filterOwnChanges removes the Path2 deltas the journal explains as
// CloudWire's own work, so only Cloud Changes remain in ds2. The local
// version of such a file wins: it is queued for upload (rclone skips it if
// the cloud already holds it), or the cloud copy is deleted when the user
// deleted the file locally after CloudWire uploaded it.
func (b *bisyncRun) filterOwnChanges(ctx context.Context, ds1, ds2 *deltaSet) {
	if b.journal == nil {
		return
	}
	b.heal1to2 = bilib.Names{}
	b.healDelete2 = bilib.Names{}
	// march only pairs names it listed; deleted files need their aliases too
	b.updateAliases(ctx, ds1, ds2)
	ls1, ls2 := b.march.ls1, b.march.ls2
	for _, f2 := range ds2.sort() {
		d2 := ds2.deltas[f2]
		f1 := ls1.getTryAlias(f2, b.aliases.Alias(f2))
		if ls2.isDir(f2) || ls1.isDir(f1) {
			continue
		}
		hasLocal := ls1.has(f1)
		e, ok := b.journal.get(f2)
		if ok && !b.journal.seeded && !e.C.known() {
			// only an upload in flight: while migrating, like no record at all
			ok = false
		}
		var own bool
		switch {
		case d2.is(deltaDeleted):
			// only a file CloudWire saw in the cloud and did not remove itself
			// can have been deleted by someone else
			own = !ok || !e.C.known()
			if !own && !b.cloudDeleted(ctx, f2) {
				fs.Logf(f2, "Path2 listing lacks a file that is still in the cloud; not a deletion")
				own = true
			}
		case ok:
			own = b.journal.ownCloud(e, b.cloudState(f2))
		default:
			own = !b.journal.seeded && hasLocal
		}
		if !own {
			continue
		}
		b.indent("Path2", f2, "Change is CloudWire's own (journal), not a Cloud Change")
		delete(ds2.deltas, f2)
		if d2.is(deltaDeleted) {
			ds2.deleted--
		}
		if _, in1 := ds1.deltas[f1]; in1 {
			continue // the Path1 delta decides
		}
		switch {
		case hasLocal:
			b.heal1to2.Add(f1)
		case !d2.is(deltaDeleted) && ok:
			b.healDelete2.Add(f1)
		}
	}
}

// settling reports whether a Path1 file changed too recently to upload: it
// is probably still being written (a take being recorded, a project being
// saved). It stays a change in the listing and is uploaded by a later run.
func (b *bisyncRun) settling(file string) bool {
	if b.opt.SettleTime <= 0 || !b.march.ls1.has(file) || b.march.ls1.isDir(file) {
		return false
	}
	t := b.march.ls1.getTime(file)
	age := time.Since(t)
	// a modtime in the future (clock skew, odd archives) does not mean "being written"
	if t.IsZero() || age < 0 || age >= time.Duration(b.opt.SettleTime) {
		return false
	}
	b.indent("Path1", file, fmt.Sprintf("Not uploading yet: changed %s ago, still being written", time.Since(t).Round(time.Second)))
	b.deferred++
	return true
}

// beforeUploads records the expected cloud state of each upload and forgets
// the files this run deletes in the cloud.
func (b *bisyncRun) beforeUploads(copy1to2, delete2 bilib.Names) {
	if b.journal == nil {
		return
	}
	for f1 := range delete2 {
		b.journal.remove(b.march.ls2.getTryAlias(f1, b.aliases.Alias(f1)))
	}
	for f1 := range copy1to2 {
		if delete2.Has(f1) || !b.march.ls1.has(f1) || b.march.ls1.isDir(f1) {
			continue
		}
		f2 := b.march.ls2.getTryAlias(f1, b.aliases.Alias(f1))
		b.journal.setPending(f2, localState(b.march.ls1, f1))
	}
}

// afterFailedUploads settles the journal entries of uploads that failed: the
// cloud may still hold the previous version, nothing, or (if the failure came
// after the upload landed, e.g. a checksum mismatch) the new one.
func (b *bisyncRun) afterFailedUploads(ctx context.Context, results []Results) {
	if b.journal == nil {
		return
	}
	for _, r := range results {
		if !r.IsSrc || r.Src == "" || (r.Err == nil && r.Winner.Err == nil) || r.Flags == "d" {
			continue
		}
		f2 := b.march.ls2.getTryAlias(r.Name, b.aliases.Alias(r.Name))
		e, ok := b.journal.get(f2)
		if !ok {
			continue
		}
		obj, err := b.fs2.NewObject(ctx, f2)
		if errors.Is(err, fs.ErrorObjectNotFound) {
			b.journal.remove(f2)
			continue
		}
		if err != nil {
			continue // cannot tell (e.g. offline): keep what the journal knows
		}
		cur := objState(ctx, obj)
		switch {
		case e.C.known() && b.journal.sameState(e.C, cur, true):
			e.P = nil
			b.journal.set(f2, e)
		case e.P != nil && b.journal.sameState(*e.P, cur, false):
			b.journal.set(f2, jEntry{L: *e.P, C: cur})
		default:
			b.journal.remove(f2)
		}
	}
}

// seedJournal records, after a successful run, every file that was equal on
// both sides at the start of the run and not touched since: this keeps the
// ETags current, migrates items that synced before the journal existed and
// drops entries of files that no longer exist.
func (b *bisyncRun) seedJournal() {
	j := b.journal
	if j == nil || b.march.ls1 == nil || b.march.ls2 == nil {
		return
	}
	ls1, ls2 := b.march.ls1, b.march.ls2
	j.mu.Lock()
	defer j.mu.Unlock()
	present := map[string]bool{}
	for _, f2 := range ls2.list {
		if ls2.isDir(f2) {
			continue
		}
		key := jKey(f2)
		present[key] = true
		if j.touched[key] {
			continue
		}
		f1 := ls1.getTryAlias(f2, b.aliases.Alias(f2))
		if !ls1.has(f1) || ls1.isDir(f1) {
			continue
		}
		l, c := localState(ls1, f1), b.cloudState(f2)
		// While migrating, the cloud state of a file CloudWire synced before the
		// journal existed counts as its own, equal or not (docs/adr/0014).
		if j.seeded && !j.sameState(l, c, false) {
			continue
		}
		j.entries[key] = &jEntry{L: l, C: c}
	}
	for name := range j.entries {
		if !present[name] && !j.touched[name] {
			delete(j.entries, name)
		}
	}
	j.seeded = true
	if err := j.rewrite(); err != nil {
		fs.Errorf(nil, "Cannot compact the journal: %v", err)
	}
}

// --- local Trash ---

func (b *bisyncRun) trashDir() string {
	return b.basePath + ".trash"
}

// settleTrash handles the Path1 files sync moved aside (--backup-dir) before
// replacing or deleting them. A file whose transfer completed (the original
// path holds the new version, this run downloaded it, or the journal records
// a newer local state) or that this run deleted on purpose goes to the Trash;
// a file moved aside by a transfer that did not complete is put back.
func (b *bisyncRun) settleTrash(deleted bilib.Names) {
	root := b.trashDir()
	runs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	local := b.fs1.Root()
	for _, run := range runs {
		runDir := filepath.Join(root, run.Name())
		_ = filepath.WalkDir(runDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(runDir, p)
			if err != nil {
				return nil
			}
			name := filepath.ToSlash(rel)
			orig := filepath.Join(local, rel)
			_ = os.Remove(orig + ".cwrestore.partial") // left by a restore a kill interrupted
			_, statErr := os.Lstat(orig)
			switch {
			case statErr == nil:
				b.trashStaged(p, name, "Replaced; the previous local version is in the Trash")
			case deleted.Has(name):
				b.trashStaged(p, name, "Deleted (moved to the Trash)")
			case b.transferCompleted(name, p):
				b.trashStaged(p, name, "Replaced and removed since; the previous local version is in the Trash")
			default:
				err := restoreFile(p, orig)
				switch {
				case err == nil:
					fs.Infof(name, "Put back the local file a failed transfer moved aside")
				case errors.Is(err, os.ErrExist):
					b.trashStaged(p, name, "Replaced; the previous local version is in the Trash")
				default:
					fs.Errorf(name, "Cannot put back the local file a failed transfer moved aside (it is in %s): %v", p, err)
				}
			}
			return nil
		})
		removeEmptyDirs(runDir)
	}
	_ = os.Remove(root)
}

// transferCompleted reports whether the download that moved the staged file
// aside completed: in this run, or (for leftovers of a killed run) according
// to the journal, whose local state is no longer the staged version.
func (b *bisyncRun) transferCompleted(name, staged string) bool {
	if b.journal.downloaded(name) {
		return true
	}
	e, ok := b.journal.get(name)
	if !ok || !e.L.known() {
		return false
	}
	info, err := os.Stat(staged)
	if err != nil {
		return false
	}
	return !b.journal.sameLocal(e, jState{Size: info.Size(), Time: info.ModTime()})
}

func (b *bisyncRun) trashStaged(p, name, done string) {
	if LocalTrash == nil {
		return
	}
	if err := LocalTrash(p); err != nil {
		fs.Errorf(name, "Cannot move the previous local version to the Trash (it is in %s): %v", p, err)
		return
	}
	fs.Infof(name, "%s", done)
}

// restoreFile puts a staged file back at its original path without ever
// replacing a file that appeared there meanwhile (os.ErrExist) and without
// leaving a partial file under the original name: the content is linked
// into place, across volumes after a full copy next to the target.
func restoreFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	err := os.Link(from, to)
	if err == nil {
		return os.Remove(from)
	}
	if errors.Is(err, os.ErrExist) {
		return err
	}
	tmp := to + ".cwrestore.partial" // ignored by the watcher like rclone's partial files
	_ = os.Remove(tmp)
	if err := copyFile(from, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Link(tmp, to); err != nil {
		if errors.Is(err, os.ErrExist) {
			_ = os.Remove(tmp)
			return err
		}
		// a volume without hard links: rename (replaces only in a tiny window)
		if _, statErr := os.Lstat(to); statErr == nil {
			_ = os.Remove(tmp)
			return os.ErrExist
		}
		if err := os.Rename(tmp, to); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return os.Remove(from)
	}
	_ = os.Remove(tmp)
	return os.Remove(from)
}

// copyFile copies from to a new file to, durably, keeping the modtime.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Chtimes(to, info.ModTime(), info.ModTime())
}

// removeEmptyDirs removes dir and every folder below it that holds no files.
func removeEmptyDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // fails while files remain
}

// --- resync ---

// planResync decides, before a resync copies anything, which version of a
// file present on both sides with different content survives. The local
// version wins unless the journal proves a Cloud Change: then the cloud
// version replaces an unchanged local file, and a changed local file keeps
// its place while the cloud version becomes a Conflict Copy. A file the
// journal does not know is new on both sides and becomes a Conflict Copy as
// well, except while an item migrates to the journal.
func (b *bisyncRun) planResync(ctx context.Context, fctx context.Context) error {
	b.resyncCloudWins = bilib.Names{}
	if b.journal == nil {
		return nil
	}
	var err error
	b.march.ls1, b.march.ls2, err = b.makeMarchListing(fctx)
	if err != nil {
		b.critical = true
		b.retryable = true
		return err
	}
	ls1, ls2 := b.march.ls1, b.march.ls2
	var conflicts []string
	for _, f1 := range ls1.list {
		f2 := ls2.getTryAlias(f1, b.aliases.Alias(f1))
		if ls1.isDir(f1) || !ls2.has(f2) || ls2.isDir(f2) {
			continue
		}
		s1, s2 := localState(ls1, f1), b.cloudState(f2)
		if !sizeDiffers(s1.Size, s2.Size) && !timeDiffers(ctx, s1.Time, s2.Time, b.fs1, b.fs2) {
			continue
		}
		e, ok := b.journal.get(f2)
		if ok && !b.journal.seeded && !e.C.known() {
			ok = false // only an upload in flight: while migrating, like no record
		}
		switch {
		case !ok && !b.journal.seeded:
			fs.Infof(f1, "Resync: local version wins (no journal record yet)")
		case !ok:
			conflicts = append(conflicts, f2)
		case b.journal.ownCloud(e, s2):
			fs.Infof(f1, "Resync: local version wins (no Cloud Change)")
		case b.journal.sameLocal(e, s1):
			fs.Infof(f1, "Resync: Cloud Change replaces the unchanged local file")
			b.resyncCloudWins.Add(f2)
		default:
			conflicts = append(conflicts, f2)
		}
	}
	for _, f2 := range conflicts {
		name := b.conflictName(ctx, f2)
		b.indent("!Path2", f2, "Changed on both sides; renaming Path2 copy to "+name)
		b.journal.remove(f2)
		if err := operations.MoveFile(ctx, b.fs2, b.fs2, name, f2); err != nil {
			b.critical = true
			return fmt.Errorf("%s rename failed for %s: %w", bilib.FsPath(b.fs2), f2, err)
		}
	}
	return nil
}

// conflictName is the Path2 Conflict Copy name of f2 that neither side uses.
func (b *bisyncRun) conflictName(ctx context.Context, f2 string) string {
	name := SuffixName(ctx, f2, b.opt.ConflictSuffix2)
	for i := 2; b.march.ls1.has(name) || b.march.ls2.has(name); i++ {
		name = SuffixName(ctx, f2, fmt.Sprintf("%s %d", b.opt.ConflictSuffix2, i))
	}
	return name
}

// resyncEqualFn decides the Path2 to Path1 copy of a journal resync: a file
// present on both sides is copied only where planResync let the cloud win.
func (b *bisyncRun) resyncEqualFn(ctx context.Context) context.Context {
	return operations.WithEqualFn(ctx, func(ctx context.Context, src fs.ObjectInfo, dst fs.Object) bool {
		logger, _ := operations.GetLogger(ctx)
		if b.resyncCloudWins.Has(src.Remote()) {
			logger(ctx, operations.Differ, src, dst, nil)
			return false
		}
		logger(ctx, operations.Match, src, dst, nil)
		return true
	})
}

// journalSummary logs what the journal did in this run.
func (b *bisyncRun) journalSummary() {
	if b.deferred > 0 {
		fs.Infoc(nil, Color(terminal.YellowFg, fmt.Sprintf("%d local files are still being written and will be uploaded by a later run", b.deferred)))
	}
	// the caller acts on these lines: schedule the next run, report failures
	if b.deferred > 0 {
		fs.Logf(nil, "%d %s", b.deferred, LeftForLaterRun)
	}
	if b.transferErr != nil {
		fs.Logf(nil, "%s: %v", TransfersFailed, b.transferErr)
	}
}
