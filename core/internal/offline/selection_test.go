package offline

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/selection"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

func TestSelectionFromParams(t *testing.T) {
	if got, err := selectionFromParams("folder", []string{"ignored"}); err != nil || !slices.Equal(got, []string{""}) {
		t.Fatalf("folder: %q %v", got, err)
	}
	if _, err := selectionFromParams("files", nil); err == nil {
		t.Fatal("empty files: want error")
	}
	if _, err := selectionFromParams("tree", []string{"a"}); err == nil {
		t.Fatal("unknown kind: want error")
	}
}

func TestApplySelection(t *testing.T) {
	var it store.OfflineItem
	applySelection(&it, []string{""})
	if it.Kind != "folder" || it.Files != nil || !slices.Equal(selectionOf(it), []string{""}) {
		t.Fatalf("root selection: %+v", it)
	}
	applySelection(&it, []string{"a/b"})
	if it.Kind != "files" || !slices.Equal(selectionOf(it), []string{"a/b"}) {
		t.Fatalf("files selection: %+v", it)
	}
}

func TestChangedDirs(t *testing.T) {
	root := store.OfflineItem{Kind: "files", Files: []string{"M/123/B", "x.wav"}}
	if got := changedDirs(root); !slices.Equal(got, []string{"", "M/123", "M/123/B", "x.wav"}) {
		t.Fatalf("root selection: %q", got)
	}
	legacy := store.OfflineItem{Kind: "folder", RemotePath: "Music/Live"}
	if got := changedDirs(legacy); !slices.Equal(got, []string{"Music/Live"}) {
		t.Fatalf("folder item: %q", got)
	}
}

func writeFiles(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		p := filepath.Join(root, r)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(r), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeselectedLocal(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "M/123/B/x", "M/123/B/y", "M/123/C/c")
	old := []string{"M/123/B", "M/123/C"}
	next := []string{"M/123/B/y"}
	got, err := deselectedLocal(dir, selection.Uncovered(old, next), next)
	want := []string{filepath.Join(dir, "M/123/B/x"), filepath.Join(dir, "M/123/C")}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}

	// Narrowing the whole root keeps the parent folders as structure only.
	root := t.TempDir()
	writeFiles(t, root, "other.txt", "M/readme.txt", "M/123/B/b", "M/123/A/a")
	got, err = deselectedLocal(root, []string{""}, []string{"M/123/B"})
	want = []string{filepath.Join(root, "M/123/A"), filepath.Join(root, "M/readme.txt"), filepath.Join(root, "other.txt")}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("root: got %q, %v; want %q", got, err, want)
	}

	if got, err := deselectedLocal(dir, []string{"missing"}, next); err != nil || len(got) != 0 {
		t.Fatalf("missing path: %q %v", got, err)
	}
}

func TestRemoveEmptyParents(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "M/123/C/.DS_Store", "M/123/B/b", "M/other/o")
	removed := filepath.Join(dir, "M/123/C/c") // already moved to the Trash
	removeEmptyParents(dir, removed, []string{"M/123/B"}, nil)
	if _, err := os.Lstat(filepath.Join(dir, "M/123/C")); !os.IsNotExist(err) {
		t.Fatalf("folder with only .DS_Store left: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "M/123")); err != nil {
		t.Fatalf("relevant parent removed: %v", err)
	}

	// Without a Selection every empty parent goes, up to the storage root.
	if err := os.RemoveAll(filepath.Join(dir, "M/other/o")); err != nil {
		t.Fatal(err)
	}
	removeEmptyParents(dir, filepath.Join(dir, "M/other/o"), nil, nil)
	if _, err := os.Lstat(filepath.Join(dir, "M/other")); !os.IsNotExist(err) {
		t.Fatalf("empty parent left: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "M")); err != nil {
		t.Fatalf("non-empty parent removed: %v", err)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("storage root removed: %v", err)
	}

	// Another item's Storage Location stays even when it is empty.
	writeFiles(t, dir, "Samples/.DS_Store")
	inner := filepath.Join(dir, "Samples")
	removeEmptyParents(dir, filepath.Join(inner, "snare.wav"), nil, []string{inner})
	if _, err := os.Lstat(inner); err != nil {
		t.Fatalf("nested Storage Location removed: %v", err)
	}
}

func TestNeverAdopted(t *testing.T) {
	excludes := store.DefaultSettings().DefaultExcludes
	marker := ConflictMarker("Konflikt")
	for rel, want := range map[string]bool{
		"M/New":                              false,
		"M/Report.docx":                      false,
		"M/.DS_Store":                        true,
		"M/._Report.docx":                    true,
		"M/~$Report.docx":                    true,
		"M/.~lock.Report.odt#":               true,
		"M/Mix.Konflikt 2026-09-23 1200.wav": true,
		"M/Safe.cwvault":                     true,
	} {
		if got := neverAdopted(rel, excludes, marker); got != want {
			t.Errorf("neverAdopted(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestLocalAdditionsAndMissingLocal(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "M/123/B/b", "M/123/New/n", "M/123/A.txt", "M/.DS_Store", "Other/o", "Nested/x")
	it := store.OfflineItem{Kind: "files", Files: []string{"M/123/B", "M/123/C"}, StoragePath: dir,
		Excludes: store.DefaultSettings().DefaultExcludes}
	got, err := localAdditions(it, ConflictMarker("Konflikt"), []string{filepath.Join(dir, "Nested")})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"M/123/A.txt", "M/123/New", "Other"}) {
		t.Fatalf("additions %q", got)
	}
	if got, _ := localAdditions(store.OfflineItem{Kind: "folder", StoragePath: dir}, "x", nil); got != nil {
		t.Fatalf("folder item additions %q", got)
	}
	if got := missingLocal(dir, it.Files); !slices.Equal(got, []string{"M/123/C"}) {
		t.Fatalf("missing %q", got)
	}
}
