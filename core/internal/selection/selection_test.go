package selection

import (
	"errors"
	"slices"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

func TestNormalize(t *testing.T) {
	got, err := Normalize([]string{"a/b/", "c", "/a", "c/"})
	if err != nil || !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"a//b", "..", "a/../b", "./a", "", "/"} {
		var inv api.InvalidParams
		if _, err := Normalize([]string{"ok", bad}); !errors.As(err, &inv) {
			t.Errorf("%q: want invalid params, got %v", bad, err)
		}
	}
}

func TestMerge(t *testing.T) {
	if got := Merge([]string{"a/b", "c"}, []string{"a", "d/e"}); !slices.Equal(got, []string{"a", "c", "d/e"}) {
		t.Fatalf("merge: %q", got)
	}
	if got := Merge([]string{"a"}, []string{""}); !slices.Equal(got, []string{""}) {
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
		if got := Relevant(entries, rel); got != want {
			t.Errorf("Relevant(%q) = %v, want %v", rel, got, want)
		}
	}
	if Covers(entries, "Meine Daten/123") || !Covers([]string{""}, "x/y") {
		t.Fatal("Covers: ancestors are not covered, the root covers everything")
	}
	if got := Uncovered([]string{"a", "b/c", "d"}, []string{"b", "d/e"}); !slices.Equal(got, []string{"a", "d"}) {
		t.Fatalf("Uncovered: %q", got)
	}
}

func TestPartialFoldersAndFirstUnselected(t *testing.T) {
	entries := []string{"M/123/B", "M/123/C", "top.txt"}
	if got := PartialFolders(entries); !slices.Equal(got, []string{"", "M", "M/123"}) {
		t.Fatalf("partial folders %q", got)
	}
	for rel, want := range map[string]string{
		"M/123/B/x.wav":  "",
		"M/123":          "",
		"M/123/New/a/b":  "M/123/New",
		"M/readme.txt":   "M/readme.txt",
		"Other/deep/one": "Other",
	} {
		if got := FirstUnselected(entries, rel); got != want {
			t.Errorf("FirstUnselected(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestExcluded(t *testing.T) {
	ex := store.DefaultSettings().DefaultExcludes
	for rel, want := range map[string]bool{
		".DS_Store":               true,
		"Sub/.DS_Store":           true,
		"._Bassline.wav":          true,
		".Spotlight-V100/Store-2": true,
		".Trashes":                true,
		"Bassline.wav":            false,
		"Sub/Trashes/x.wav":       false,
		"Mix.wav.x1Y2.partial":    true,
	} {
		if got := Excluded(rel, ex); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", rel, got, want)
		}
	}
}
