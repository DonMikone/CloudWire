package identity

import (
	"maps"
	"testing"
)

func dir(ino uint64) LocalEntry { return LocalEntry{Dir: true, Ino: ino} }
func file(ino uint64, size, mtime int64) LocalEntry {
	return LocalEntry{Ino: ino, Size: size, MTime: mtime}
}

func wantRenames(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Fatalf("renames %v, want %v", got, want)
	}
}

func TestDetectLocalByInode(t *testing.T) {
	prev := map[string]LocalEntry{
		"Tree": dir(1), "Tree/B": dir(2), "Tree/B/x.wav": file(3, 10, 1), "Tree/B/Sub": dir(4), "Tree/B/Sub/y.wav": file(5, 20, 2),
		"Tree/Song.wav": file(6, 30, 3), "Tree/C": dir(7), "Tree/C/z.wav": file(8, 40, 4), "Top.txt": file(9, 5, 5),
	}
	cur := map[string]LocalEntry{
		// folder renamed with its content: one rename
		"Tree": dir(1), "Tree/B2": dir(2), "Tree/B2/x.wav": file(3, 10, 1), "Tree/B2/Sub": dir(4), "Tree/B2/Sub/y.wav": file(5, 20, 2),
		// case-only rename
		"Tree/song.wav": file(6, 30, 3),
		// moved within the tree, content changed on the way
		"Archiv": dir(10), "Archiv/C": dir(7), "Archiv/C/z.wav": file(8, 41, 9),
		// file renamed
		"Top renamed.txt": file(9, 5, 5),
	}
	wantRenames(t, DetectLocal(prev, cur, true, ".Konflikt "), map[string]string{
		"Tree/B": "Tree/B2", "Tree/Song.wav": "Tree/song.wav", "Tree/C": "Archiv/C", "Top.txt": "Top renamed.txt",
	})
}

func TestDetectLocalKeepsNestedRenamesAndIgnoresAmbiguity(t *testing.T) {
	prev := map[string]LocalEntry{"A": dir(1), "A/x": file(2, 1, 1), "A/y": file(3, 1, 1), "L": file(4, 1, 1)}
	cur := map[string]LocalEntry{
		"A2": dir(1), "A2/x renamed": file(2, 1, 1), "A2/y": file(3, 1, 1),
		// a hard link: the inode is at two new paths
		"L1": file(4, 1, 1), "L2": file(4, 1, 1),
	}
	wantRenames(t, DetectLocal(prev, cur, true, ""), map[string]string{"A": "A2", "A/x": "A2/x renamed"})
}

func TestDetectLocalIgnoresConflictCopiesAndKnownPaths(t *testing.T) {
	prev := map[string]LocalEntry{"Song.wav": file(1, 1, 1), "Other.wav": file(2, 1, 1), "Keep.wav": file(3, 1, 1)}
	cur := map[string]LocalEntry{
		"Song.Konflikt 2026-09-27 1713.wav": file(1, 1, 1),
		// the inode now sits at a path that existed before: not a rename
		"Keep.wav": file(2, 1, 1),
	}
	wantRenames(t, DetectLocal(prev, cur, true, ".Konflikt "), map[string]string{})
}

func TestDetectLocalWithoutInodes(t *testing.T) {
	// exFAT: inodes change on every mount, the tree itself identifies a folder.
	prev := map[string]LocalEntry{
		"B": dir(1), "B/x.wav": file(2, 10, 100), "B/Sub": dir(3), "B/Sub/y.wav": file(4, 20, 200),
		"single.wav": file(5, 7, 700), "twin1.wav": file(6, 8, 800), "twin2.wav": file(7, 8, 800),
		"empty": dir(8),
	}
	cur := map[string]LocalEntry{
		"B2": dir(11), "B2/x.wav": file(12, 10, 100), "B2/Sub": dir(13), "B2/Sub/y.wav": file(14, 20, 200),
		"single renamed.wav": file(15, 7, 700),
		// two files with equal size and time: ambiguous
		"twinA.wav": file(16, 8, 800), "twinB.wav": file(17, 8, 800),
		// an empty folder has no tree to compare
		"empty2": dir(18),
	}
	wantRenames(t, DetectLocal(prev, cur, false, ""), map[string]string{"B": "B2", "single.wav": "single renamed.wav"})

	// A file changed inside the folder: no longer the same tree.
	cur["B2/x.wav"] = file(12, 11, 100)
	got := DetectLocal(prev, cur, false, "")
	if _, ok := got["B"]; ok {
		t.Fatalf("changed tree matched: %v", got)
	}
}

func TestDetectCloudByID(t *testing.T) {
	prev := map[string]CloudEntry{
		"Tree": {Dir: true, ID: "1"}, "Tree/B": {Dir: true, ID: "2"}, "Tree/B/x": {ID: "3"}, "Tree/C": {Dir: true, ID: "4"},
		"noid": {ID: ""},
	}
	cur := map[string]CloudEntry{
		"Tree": {Dir: true, ID: "1"}, "Tree/B3": {Dir: true, ID: "2"}, "Tree/B3/x": {ID: "3"},
		"elsewhere": {ID: ""},
	}
	// Tree/C vanished (deleted or moved out of the scanned area): not a rename.
	wantRenames(t, DetectCloud(prev, cur, ""), map[string]string{"Tree/B": "Tree/B3"})
}
