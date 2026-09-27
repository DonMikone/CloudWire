package identity

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// scenario builds Inputs for one synced folder B (with file x) in Tree.
type scenario struct {
	localNow, cloudNow []string // paths present now besides Tree
	local, cloud       map[string]string
	cloudStat          map[string]bool // for id-less clouds and targets outside the scan
	localDir           string          // real Storage Location for LocalStat, "" = empty
	idless             bool
}

func (s scenario) inputs() Inputs {
	prevLocal := map[string]LocalEntry{"Tree": {Dir: true, Ino: 1}, "Tree/B": {Dir: true, Ino: 2}, "Tree/B/x": {Ino: 3}}
	prevCloud := map[string]CloudEntry{"Tree": {Dir: true, ID: "c1"}, "Tree/B": {Dir: true, ID: "c2"}, "Tree/B/x": {ID: "c3"}}
	curLocal := map[string]LocalEntry{"Tree": {Dir: true, Ino: 1}}
	for _, p := range s.localNow {
		curLocal[p] = LocalEntry{Dir: filepath.Ext(p) == "", Ino: 99}
	}
	curCloud := map[string]CloudEntry{"Tree": {Dir: true, ID: "c1"}}
	for _, p := range s.cloudNow {
		curCloud[p] = CloudEntry{Dir: filepath.Ext(p) == "", ID: "other-" + p}
	}
	// Renamed entries keep their identity.
	for from, to := range s.cloud {
		curCloud[to] = prevCloud[from]
	}
	in := Inputs{PrevLocal: prevLocal, CurLocal: curLocal, PrevCloud: prevCloud, CurCloud: curCloud,
		Local: s.local, Cloud: s.cloud,
		LocalStat: func(rel string) (fs.FileInfo, error) {
			if s.localDir == "" {
				return nil, fs.ErrNotExist
			}
			return os.Lstat(filepath.Join(s.localDir, rel))
		},
		CloudStat: func(rel string) (bool, bool, error) { return s.cloudStat[rel], true, nil },
	}
	if s.idless {
		in.CurCloud, in.Cloud = nil, nil
	}
	return in
}

func TestReconcileTable(t *testing.T) {
	type want struct {
		ops     []Op
		renames []sv.Rename
	}
	moved := func(to string) []sv.Rename {
		return []sv.Rename{{From: "Tree/B", To: to, Dir: true, Result: sv.RenameMoved}}
	}
	for _, tc := range []struct {
		name string
		s    scenario
		want want
	}{
		{"local renamed, cloud same", scenario{localNow: []string{"Tree/B2"}, cloudNow: []string{"Tree/B"},
			local: map[string]string{"Tree/B": "Tree/B2"}},
			want{[]Op{{Side: OpCloud, From: "Tree/B", To: "Tree/B2", Dir: true}}, moved("Tree/B2")}},
		{"local renamed, cloud deleted", scenario{localNow: []string{"Tree/B2"},
			local: map[string]string{"Tree/B": "Tree/B2"}},
			want{nil, []sv.Rename{{From: "Tree/B", To: "Tree/B2", Dir: true, Result: sv.RenameUpload}}}},
		{"cloud renamed, local same", scenario{localNow: []string{"Tree/B"},
			cloud: map[string]string{"Tree/B": "Tree/B3"}},
			want{[]Op{{Side: OpLocal, From: "Tree/B", To: "Tree/B3", Dir: true}}, moved("Tree/B3")}},
		{"cloud renamed, local deleted", scenario{
			cloud: map[string]string{"Tree/B": "Tree/B3"}},
			want{nil, []sv.Rename{{From: "Tree/B", To: "Tree/B3", Dir: true, Result: sv.RenameDownload}}}},
		{"both renamed alike", scenario{localNow: []string{"Tree/B2"},
			local: map[string]string{"Tree/B": "Tree/B2"}, cloud: map[string]string{"Tree/B": "Tree/B2"}},
			want{nil, moved("Tree/B2")}},
		{"both renamed differently: local wins", scenario{localNow: []string{"Tree/B2"},
			local: map[string]string{"Tree/B": "Tree/B2"}, cloud: map[string]string{"Tree/B": "Tree/B3"}},
			want{[]Op{{Side: OpCloud, From: "Tree/B3", To: "Tree/B2", Dir: true}}, moved("Tree/B2")}},
		{"id-less cloud still follows a local rename", scenario{localNow: []string{"Tree/B2"}, idless: true,
			cloudStat: map[string]bool{"Tree/B": true}, local: map[string]string{"Tree/B": "Tree/B2"}},
			want{[]Op{{Side: OpCloud, From: "Tree/B", To: "Tree/B2", Dir: true}}, moved("Tree/B2")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, renames, collision, err := Reconcile(tc.s.inputs())
			if err != nil || collision != "" {
				t.Fatalf("collision %q, err %v", collision, err)
			}
			if !reflect.DeepEqual(ops, tc.want.ops) || !reflect.DeepEqual(renames, tc.want.renames) {
				t.Fatalf("ops %+v renames %+v\nwant ops %+v renames %+v", ops, renames, tc.want.ops, tc.want.renames)
			}
		})
	}
}

func TestReconcileCollisionAppliesNothing(t *testing.T) {
	// The cloud has a different B2 where the local rename wants to go.
	s := scenario{localNow: []string{"Tree/B2"}, cloudNow: []string{"Tree/B", "Tree/B2"},
		local: map[string]string{"Tree/B": "Tree/B2"}}
	ops, renames, collision, err := Reconcile(s.inputs())
	if err != nil || collision != "Tree/B2" || ops != nil || renames != nil {
		t.Fatalf("ops %+v renames %+v collision %q err %v", ops, renames, collision, err)
	}
	// Outside the scanned area only a lookup finds it.
	s = scenario{localNow: []string{"Elsewhere/B"}, cloudNow: []string{"Tree/B"}, cloudStat: map[string]bool{"Elsewhere/B": true},
		local: map[string]string{"Tree/B": "Elsewhere/B"}}
	if _, _, collision, _ := Reconcile(s.inputs()); collision != "Elsewhere/B" {
		t.Fatalf("stat collision %q", collision)
	}

	// Locally a different B3 is in the way of the cloud rename.
	dir := t.TempDir()
	for _, d := range []string{"Tree/B", "Tree/B3"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s = scenario{localNow: []string{"Tree/B", "Tree/B3"}, localDir: dir, cloud: map[string]string{"Tree/B": "Tree/B3"}}
	ops, _, collision, err = Reconcile(s.inputs())
	if err != nil || collision != "Tree/B3" || ops != nil {
		t.Fatalf("local collision: ops %+v collision %q err %v", ops, collision, err)
	}
}

func TestReconcileCaseOnlyRenameIsNoCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Tree/B"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "Tree/b")); err != nil {
		t.Skip("case-sensitive volume")
	}
	s := scenario{localNow: []string{"Tree/B"}, localDir: dir, cloud: map[string]string{"Tree/B": "Tree/b"}}
	ops, _, collision, err := Reconcile(s.inputs())
	if err != nil || collision != "" || len(ops) != 1 || ops[0].To != "Tree/b" {
		t.Fatalf("ops %+v collision %q err %v", ops, collision, err)
	}
}

func TestReconcileNestedRenamesFollowTheirFolder(t *testing.T) {
	// The cloud renamed Tree/B to Tree/B3; locally x inside it became y.
	s := scenario{localNow: []string{"Tree/B", "Tree/B/y.wav"},
		local: map[string]string{"Tree/B/x": "Tree/B/y.wav"}, cloud: map[string]string{"Tree/B": "Tree/B3"}}
	in := s.inputs()
	in.CurCloud["Tree/B3/x"] = CloudEntry{ID: "c3"}
	ops, renames, collision, err := Reconcile(in)
	if err != nil || collision != "" {
		t.Fatal(collision, err)
	}
	wantOps := []Op{
		{Side: OpLocal, From: "Tree/B", To: "Tree/B3", Dir: true},
		{Side: OpCloud, From: "Tree/B3/x", To: "Tree/B3/y.wav"},
	}
	wantRenames := []sv.Rename{
		{From: "Tree/B", To: "Tree/B3", Dir: true, Result: sv.RenameMoved},
		{From: "Tree/B3/x", To: "Tree/B3/y.wav", Result: sv.RenameMoved},
	}
	if !reflect.DeepEqual(ops, wantOps) || !reflect.DeepEqual(renames, wantRenames) {
		t.Fatalf("ops %+v\nrenames %+v", ops, renames)
	}
}
