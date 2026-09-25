package offline

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

func TestNormalizeSelection(t *testing.T) {
	got, err := normalizeSelection([]string{"a/b/", "c", "/a", "c/"})
	if err != nil || !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"a//b", "..", "a/../b", "./a", "", "/"} {
		var inv api.InvalidParams
		if _, err := normalizeSelection([]string{"ok", bad}); !errors.As(err, &inv) {
			t.Errorf("%q: want invalid params, got %v", bad, err)
		}
	}
}

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

func TestApplyAndMergeSelection(t *testing.T) {
	var it store.OfflineItem
	applySelection(&it, []string{""})
	if it.Kind != "folder" || it.Files != nil || !slices.Equal(selectionOf(it), []string{""}) {
		t.Fatalf("root selection: %+v", it)
	}
	applySelection(&it, []string{"a/b"})
	if it.Kind != "files" || !slices.Equal(selectionOf(it), []string{"a/b"}) {
		t.Fatalf("files selection: %+v", it)
	}
	if got := mergeSelections([]string{"a/b", "c"}, []string{"a", "d/e"}); !slices.Equal(got, []string{"a", "c", "d/e"}) {
		t.Fatalf("merge: %q", got)
	}
	if got := mergeSelections([]string{"a"}, []string{""}); !slices.Equal(got, []string{""}) {
		t.Fatalf("merge with root: %q", got)
	}
}

func TestRelevantAndCovers(t *testing.T) {
	entries := []string{"Meine Daten/123/B", "Meine Daten/123/C"}
	for rel, want := range map[string]bool{
		"":                           true,
		"Meine Daten":                true,
		"Meine Daten/123":            true,
		"Meine Daten/123/B":          true,
		"Meine Daten/123/B/x.wav":    true,
		"Meine Daten/123/A":          false,
		"Meine Daten/123/readme.txt": false,
		"Meine Daten/123/BB":         false,
		"Meine":                      false,
	} {
		if got := relevant(entries, rel); got != want {
			t.Errorf("relevant(%q) = %v, want %v", rel, got, want)
		}
	}
	if covers(entries, "Meine Daten/123") || !covers([]string{""}, "x/y") {
		t.Fatal("covers: ancestors are not covered, the root covers everything")
	}
	if got := uncovered([]string{"a", "b/c", "d"}, []string{"b", "d/e"}); !slices.Equal(got, []string{"a", "d"}) {
		t.Fatalf("uncovered: %q", got)
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
	got, err := deselectedLocal(dir, uncovered(old, next), next)
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
