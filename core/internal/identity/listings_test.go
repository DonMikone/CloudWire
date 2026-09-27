package identity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

const listingSample = `# bisync listing v1 from 2026-09-27T15:13:03.000000000+0000
d        0 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B"
-     4096 md5:0123456789abcdef0123456789abcdef - 2026-09-27T15:13:03.000000000+0000 "Tree/B/x.wav"
-       12 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B/Take \"1\" final.wav"
-        7 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B2old/y"
-        9 - - 2026-09-27T15:13:03.000000000+0000 "Tree/C/z"
`

func writeListing(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRewriteListings(t *testing.T) {
	dir := t.TempDir()
	session := "Users_mike_Musik..cw-x1_Musik"
	var files []string
	for _, suffix := range []string{".path1.lst", ".path2.lst", ".path1.lst-old", ".path2.lst-old"} {
		files = append(files, writeListing(t, dir, session+suffix, listingSample))
	}
	other := writeListing(t, dir, session+".path1.lst-new", listingSample)
	rs := []sv.Rename{
		{From: "Tree/B", To: "Tree/B 2", Dir: true, Result: sv.RenameMoved},
		{From: "Tree/C", To: "Tree/C2", Dir: true, Result: sv.RenameUpload},
		{From: "", To: "Neu", Dir: true, Result: sv.RenameRoot},
	}
	if err := RewriteListings(dir, rs); err != nil {
		t.Fatal(err)
	}
	want := `# bisync listing v1 from 2026-09-27T15:13:03.000000000+0000
d        0 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B 2"
-     4096 md5:0123456789abcdef0123456789abcdef - 2026-09-27T15:13:03.000000000+0000 "Tree/B 2/x.wav"
-       12 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B 2/Take \"1\" final.wav"
-        7 - - 2026-09-27T15:13:03.000000000+0000 "Tree/B2old/y"
`
	for _, f := range files {
		if got := readString(t, f); got != want {
			t.Fatalf("%s:\n%s\nwant\n%s", filepath.Base(f), got, want)
		}
	}
	if readString(t, other) != listingSample {
		t.Fatal("a listing bisync rebuilds itself was rewritten")
	}
	listed, err := listedPaths(dir, "path1")
	if err != nil || !listed["Tree/B 2/Take \"1\" final.wav"] || listed["Tree/C/z"] {
		t.Fatalf("listed paths %v %v", listed, err)
	}
}

func TestRewriteListingsRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	p := writeListing(t, dir, "s.path1.lst", strings.Replace(listingSample, "listing v1", "listing v2", 1))
	if err := RewriteListings(dir, []sv.Rename{{From: "Tree/B", To: "X", Result: sv.RenameMoved}}); err == nil {
		t.Fatal("unknown header accepted")
	}
	if !strings.Contains(readString(t, p), `"Tree/B/x.wav"`) {
		t.Fatal("listing of unknown format changed")
	}
}

func TestWriteFiltersHashMatchesMD5(t *testing.T) {
	f := writeListing(t, t.TempDir(), "id.txt", "+ /Tree/B2\n+ /Tree/B2/**\n- **\n")
	if err := WriteFiltersHash(f); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("md5", "-q", f).Output()
	if err != nil {
		t.Skip("md5 not available")
	}
	if got := readString(t, f+".md5"); got != strings.TrimSpace(string(out)) {
		t.Fatalf("hash %q, md5 -q %q", got, out)
	}
}

func TestSnapshotApplyAndRoundTrip(t *testing.T) {
	s := Snapshot{
		Local: map[string]LocalEntry{"Tree/B": {Dir: true, Ino: 2}, "Tree/B/x": {Ino: 3}, "Tree/Bx": {Ino: 4}, "Tree/C/z": {Ino: 5}},
		Cloud: map[string]CloudEntry{"Tree/B": {Dir: true, ID: "2"}, "Tree/B/x": {ID: "3"}, "Tree/C/z": {ID: "5"}},
	}
	s.Apply([]sv.Rename{
		{From: "Tree/B", To: "Tree/B2", Result: sv.RenameMoved},
		{From: "Tree/C", To: "Tree/C2", Result: sv.RenameDownload},
		{From: "x", To: "y", Result: sv.RenameRoot},
	})
	p := filepath.Join(t.TempDir(), "identity.json")
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	got, ok := Load(p)
	if !ok || len(got.Local) != 3 || got.Local["Tree/B2/x"].Ino != 3 || got.Local["Tree/Bx"].Ino != 4 ||
		len(got.Cloud) != 2 || got.Cloud["Tree/B2"].ID != "2" {
		t.Fatalf("snapshot %+v", got)
	}
	if err := os.WriteFile(p, []byte(`{"version":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(p); ok {
		t.Fatal("snapshot of another version loaded")
	}
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
