package offline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fsnotify/fsevents"
	"golang.org/x/sys/unix"

	"github.com/DonMikone/CloudWire/core/internal/identity"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

func TestRenameSelection(t *testing.T) {
	moved := func(from, to string) sv.Rename { return sv.Rename{From: from, To: to, Result: sv.RenameMoved} }
	for _, c := range []struct {
		name  string
		files []string
		rs    []sv.Rename
		want  []string
	}{
		{"checked entry renamed", []string{"Tree/B", "Tree/C"}, []sv.Rename{moved("Tree/B", "Tree/B2")}, []string{"Tree/B2", "Tree/C"}},
		{"partial folder renamed", []string{"Tree/B", "Tree/C/x"}, []sv.Rename{moved("Tree", "Tree2")}, []string{"Tree2/B", "Tree2/C/x"}},
		{"moved out of a checked folder stays checked", []string{"Tree", "Other/x"}, []sv.Rename{moved("Tree/B", "Other/B")},
			[]string{"Other/B", "Other/x", "Tree"}},
		{"renamed inside a checked folder: nothing to add", []string{"Tree"}, []sv.Rename{moved("Tree/B", "Tree/B2")}, []string{"Tree"}},
		{"unselected sibling is none of its business", []string{"Tree/B"}, []sv.Rename{moved("Tree/A", "Tree/A2")}, []string{"Tree/B"}},
		{"a rename beats a deletion", []string{"Tree/B"}, []sv.Rename{{From: "Tree/B", To: "Tree/B2", Result: sv.RenameUpload}}, []string{"Tree/B2"}},
		{"renames apply in order", []string{"Tree/B"}, []sv.Rename{moved("Tree", "Tree2"), moved("Tree2/B", "Tree2/B3")}, []string{"Tree2/B3"}},
		{"root renames leave the Selection", []string{"Tree/B"}, []sv.Rename{{From: "Musik", To: "000 - Musik", Result: sv.RenameRoot}}, []string{"Tree/B"}},
		{"the whole root", []string{""}, []sv.Rename{moved("Tree", "Tree2")}, []string{""}},
	} {
		if got := renameSelection(c.files, c.rs); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

const testListing = `# bisync listing v1 from 2026-09-27T15:13:03.000000000+0000
-        1 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B/x.wav"
-        1 - - 2026-09-27T15:13:03.000000000+0000 "Tree/C/z.wav"
`

func TestApplyRenamesIsRepeatableAndMovesNestedItems(t *testing.T) {
	h := newHarness(t)
	it := store.OfflineItem{ID: "t", ConnectionID: "c1", Kind: "files", Files: []string{"Tree/B", "Tree/C"},
		StoragePath: filepath.Join(h.dir, "store"), Excludes: store.DefaultSettings().DefaultExcludes, State: StateIdle}
	nested := store.OfflineItem{ID: "n", ConnectionID: "c1", Kind: "folder", RemotePath: "Tree/B/Sub",
		StoragePath: filepath.Join(h.dir, "store", "Tree/B/Sub"), State: StateIdle}
	other := store.OfflineItem{ID: "o", ConnectionID: "c1", Kind: "folder", RemotePath: "Tree/Bx",
		StoragePath: filepath.Join(h.dir, "elsewhere"), State: StateIdle}
	for _, x := range []store.OfflineItem{it, nested, other} {
		if err := h.st.InsertOfflineItem(x); err != nil {
			t.Fatal(err)
		}
	}
	p := h.e.paths
	workdir := p.BisyncWorkdir("t")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{".path1.lst", ".path2.lst", ".path1.lst-old"} {
		if err := os.WriteFile(filepath.Join(workdir, "session"+side), []byte(testListing), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := identity.Save(p.IdentityFile("t"), identity.Snapshot{
		Local: map[string]identity.LocalEntry{"Tree/B": {Dir: true, Ino: 5}, "Tree/B/x.wav": {Ino: 6}},
		Cloud: map[string]identity.CloudEntry{"Tree/B": {Dir: true, ID: "c5"}},
	}); err != nil {
		t.Fatal(err)
	}
	rs := []sv.Rename{{From: "Tree/B", To: "Tree/B2", Dir: true, Result: sv.RenameMoved}}
	apply := func() {
		h.e.mu.Lock()
		defer h.e.mu.Unlock()
		fresh := h.state("t")
		if err := h.e.applyRenamesLocked(&fresh, rs); err != nil {
			t.Fatal(err)
		}
	}
	snapshotOf := func() string {
		b, _ := os.ReadFile(p.IdentityFile("t"))
		return string(b)
	}

	apply()
	got := h.state("t")
	if !slices.Equal(got.Files, []string{"Tree/B2", "Tree/C"}) || got.NeedsResync {
		t.Fatalf("item %+v: the Selection follows, no resync", got)
	}
	listing := readFile(t, filepath.Join(workdir, "session.path2.lst"))
	if !strings.Contains(listing, `"Tree/B2/x.wav"`) || strings.Contains(listing, `"Tree/B/`) {
		t.Fatalf("listing %s", listing)
	}
	filters := readFile(t, p.FiltersFile("t"))
	sum, err := os.ReadFile(p.FiltersFile("t") + ".md5")
	if !strings.Contains(filters, "+ /Tree/B2/**") || err != nil || len(sum) != 32 {
		t.Fatalf("filters %q, hash %q %v", filters, sum, err)
	}
	s, _ := identity.Load(p.IdentityFile("t"))
	if s.Local["Tree/B2/x.wav"].Ino != 6 || s.Cloud["Tree/B2"].ID != "c5" {
		t.Fatalf("snapshot %+v", s)
	}
	n := h.state("n")
	if n.RemotePath != "Tree/B2/Sub" || n.StoragePath != filepath.Join(h.dir, "store", "Tree/B2/Sub") || !n.NeedsResync {
		t.Fatalf("nested item %+v", n)
	}
	if o := h.state("o"); o.RemotePath != "Tree/Bx" || o.NeedsResync {
		t.Fatalf("unrelated item changed: %+v", o)
	}

	// The same renames again (a run that failed before its next snapshot).
	before := []string{listing, filters, snapshotOf()}
	apply()
	after := []string{readFile(t, filepath.Join(workdir, "session.path2.lst")), readFile(t, p.FiltersFile("t")), snapshotOf()}
	if !slices.Equal(before, after) || !slices.Equal(h.state("t").Files, got.Files) {
		t.Fatalf("second application changed state:\n%q\n%q", before, after)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// startRun starts a run of an item and returns its job.
func (h *harness) startRun(id string) *fakeJob {
	h.t.Helper()
	h.e.mu.Lock()
	h.e.itemRT(id).due = h.now
	h.e.mu.Unlock()
	h.step()
	jobs := h.started()
	if len(jobs) == 0 || jobs[len(jobs)-1].job.ID != id {
		h.t.Fatalf("no run of %s started: %+v", id, jobs)
	}
	return jobs[len(jobs)-1]
}

func TestRenameDuringRunInterruptsIt(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	j := h.startRun("a")
	// bisync's own work: a deleted file and a Conflict Copy it renamed.
	h.e.onLog(h.e.cur, sv.LogLine{Level: "info", Msg: "Deleted", Object: "x.wav"}, "Konflikt")
	h.e.localChange("a", "x.wav", fsevents.ItemRemoved, true, h.now)
	h.e.localChange("a", "Song.Konflikt 2026-09-27 1713.wav", fsevents.ItemRenamed, true, h.now)
	// A folder removed outright is left to the next run; an ordinary change is recorded.
	h.e.localChange("a", "Old", fsevents.ItemRemoved|fsevents.ItemIsDir, true, h.now)
	h.e.localChange("a", "New.wav", fsevents.ItemCreated, false, h.now)
	h.e.mu.Lock()
	stopping := h.e.cur.stopping
	h.e.mu.Unlock()
	if stopping {
		t.Fatal("the sync's own changes stopped it")
	}

	// The user renames a synced folder: the run stops and goes first again.
	h.e.localChange("a", "Tree/B", fsevents.ItemRenamed|fsevents.ItemIsDir, true, h.now)
	<-j.done
	h.waitIdle()
	j.mu.Lock()
	stopped := j.stopped
	j.mu.Unlock()
	h.e.mu.Lock()
	head := len(h.e.queue) > 0 && h.e.queue[0].itemID == "a"
	h.e.mu.Unlock()
	if !stopped || !head || h.state("a").State != StatePending {
		t.Fatalf("stopped=%v head=%v state=%s", stopped, head, h.state("a").State)
	}

	// Without a run, the next one starts at once instead of after the Quiet Period.
	h.e.mu.Lock()
	h.e.queue = nil
	h.e.mu.Unlock()
	h.e.localChange("a", "Tree/C/deleted.wav", fsevents.ItemRemoved, true, h.now)
	if rt := h.e.rt["a"]; !rt.quietUntil.IsZero() || !rt.due.Equal(h.now) {
		t.Fatalf("due %v quiet until %v", rt.due, rt.quietUntil)
	}
}

func TestRenamesMessageContinuesTheRun(t *testing.T) {
	h := newHarness(t)
	it := store.OfflineItem{ID: "t", ConnectionID: "c1", Kind: "files", Files: []string{"Tree/B"},
		StoragePath: filepath.Join(h.dir, "store"), Excludes: store.DefaultSettings().DefaultExcludes, State: StateIdle}
	if err := h.st.InsertOfflineItem(it); err != nil {
		t.Fatal(err)
	}
	if err := mkdir(it.StoragePath); err != nil {
		t.Fatal(err)
	}
	j := h.startRun("t")
	id := j.job.Identity
	if id == nil || id.StoragePath != it.StoragePath || id.RemoteFs != "cw-c1:" || id.ConnFs != "cw-c1:" ||
		!slices.Equal(id.Files, []string{"Tree/B"}) || id.Snapshot != h.e.paths.IdentityFile("t") || id.ConflictMarker != ".Konflikt " {
		t.Fatalf("identity job %+v", id)
	}
	var st unix.Stat_t
	if err := unix.Stat(it.StoragePath, &st); err != nil || h.state("t").RootIno != st.Ino {
		t.Fatalf("root inode %d, want %d (%v)", h.state("t").RootIno, st.Ino, err)
	}

	j.h.OnMsg(sv.Msg{Type: "renames", Renames: []sv.Rename{{From: "Tree/B", To: "Tree/B2", Dir: true, Result: sv.RenameMoved}}})
	j.mu.Lock()
	sent := slices.Clone(j.sent)
	j.mu.Unlock()
	if len(sent) != 1 || sent[0].Cmd != "continue" || sent[0].Identity == nil || !slices.Equal(sent[0].Identity.Files, []string{"Tree/B2"}) {
		t.Fatalf("sent %+v", sent)
	}
	var params map[string]any
	if err := json.Unmarshal(sent[0].Bisync, &params); err != nil || params["path1"] != it.StoragePath || params["resync"] != nil {
		t.Fatalf("continued params %v %v", params, err)
	}
	if got := h.state("t"); !slices.Equal(got.Files, []string{"Tree/B2"}) {
		t.Fatalf("stored Selection %q", got.Files)
	}
}

func TestRenameCollisionStopsTheItem(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	j := h.startRun("a")
	j.complete(sv.StatusCollision, "Tree/B2")
	h.waitIdle()
	it := h.state("a")
	reason := errorReason(it)
	if it.State != StateError || reason.Code != "offline.renameCollision" || reason.Params["path"] != "Tree/B2" {
		t.Fatalf("item %+v reason %+v", it, reason)
	}
	h.notify.mu.Lock()
	defer h.notify.mu.Unlock()
	if len(h.notify.kinds) != 1 {
		t.Fatalf("notifications %v", h.notify.kinds)
	}
}

func TestStorageLocationIsFollowed(t *testing.T) {
	h := newHarness(t)
	resolved := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	add := func(it store.OfflineItem) store.OfflineItem {
		t.Helper()
		if err := mkdir(it.StoragePath); err != nil {
			t.Fatal(err)
		}
		var st unix.Stat_t
		if err := unix.Stat(it.StoragePath, &st); err != nil {
			t.Fatal(err)
		}
		it.RootIno, it.ConnectionID, it.State = st.Ino, "c1", StateIdle
		if err := h.st.InsertOfflineItem(it); err != nil {
			t.Fatal(err)
		}
		return it
	}
	follow := func(id string) (store.OfflineItem, bool) {
		h.e.mu.Lock()
		defer h.e.mu.Unlock()
		it := h.state(id)
		ok := h.e.followRootLocked(&it)
		return h.state(id), ok
	}
	move := func(from, to string) {
		t.Helper()
		if err := mkdir(filepath.Dir(to)); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}

	// An item rooted at the Connection root follows; its cloud stays.
	root := add(store.OfflineItem{ID: "r", Kind: "files", Files: []string{"x"}, StoragePath: filepath.Join(h.dir, "store", "NC")})
	move(root.StoragePath, filepath.Join(h.dir, "store", "Nextcloud"))
	got, ok := follow("r")
	if !ok || got.StoragePath != resolved(filepath.Join(h.dir, "store", "Nextcloud")) || got.RemotePath != "" || !got.NeedsResync {
		t.Fatalf("followed %v: %+v", ok, got)
	}

	// An item created before 0.3.0 renames its cloud folder along with a new name.
	legacy := add(store.OfflineItem{ID: "l", Kind: "folder", RemotePath: "Musik/TAKEOFFANDFLY",
		StoragePath: filepath.Join(h.dir, "store", "TAKEOFFANDFLY")})
	move(legacy.StoragePath, filepath.Join(h.dir, "store", "000 - TAKEOFFANDFLY"))
	got, ok = follow("l")
	rr := h.e.rt["l"].rootRename
	if !ok || got.StoragePath != legacy.StoragePath || rr == nil || rr.From != "Musik/TAKEOFFANDFLY" ||
		rr.To != "Musik/000 - TAKEOFFANDFLY" || rr.NewStoragePath != resolved(filepath.Join(h.dir, "store", "000 - TAKEOFFANDFLY")) {
		t.Fatalf("legacy %v %+v root rename %+v", ok, got, rr)
	}
	if reason := h.e.blockedReason(got); reason != "" {
		t.Fatalf("blocked: %s", reason)
	}
	if j := h.startRun("l"); j.job.Identity.RootRename != rr {
		t.Fatalf("run without the root rename: %+v", j.job.Identity)
	}

	// Moved without a new name: it only follows.
	moved := add(store.OfflineItem{ID: "m", Kind: "folder", RemotePath: "Video", StoragePath: filepath.Join(h.dir, "store", "Video")})
	move(moved.StoragePath, filepath.Join(h.dir, "external", "Video"))
	if got, ok := follow("m"); !ok || got.StoragePath != resolved(filepath.Join(h.dir, "external", "Video")) || got.RemotePath != "Video" {
		t.Fatalf("moved %v %+v", ok, got)
	}

	// A Storage Location moved to the Trash is not followed.
	trashed := add(store.OfflineItem{ID: "x", Kind: "folder", RemotePath: "Docs", StoragePath: filepath.Join(h.dir, "store", "Docs")})
	move(trashed.StoragePath, filepath.Join(h.dir, ".Trash", "Docs"))
	if got, ok := follow("x"); ok || got.StoragePath != trashed.StoragePath {
		t.Fatalf("followed into the Trash: %+v", got)
	}
}
