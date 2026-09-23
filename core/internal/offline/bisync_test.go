package offline

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

func testPaths() paths.Paths { return paths.ForHome("/Users/mike") }

func TestBisyncParamsFolder(t *testing.T) {
	it := store.OfflineItem{ID: "abc", Kind: "folder", RemotePath: "Music/Projekte", StoragePath: "/Users/mike/CloudWire/Nextcloud/Music/Projekte",
		Advanced: map[string]any{"Checkers": 4}}
	got := BisyncParams(it, "cw-x1", testPaths(), "Konflikt", false)
	want := map[string]any{
		"path1":              "/Users/mike/CloudWire/Nextcloud/Music/Projekte",
		"path2":              "cw-x1:Music/Projekte",
		"workdir":            "/Users/mike/Library/Application Support/CloudWire/bisync/abc",
		"filtersFile":        "/Users/mike/Library/Application Support/CloudWire/filters/abc.txt",
		"resilient":          true,
		"recover":            true,
		"maxLock":            "2m",
		"maxDelete":          50,
		"createEmptySrcDirs": true,
		"compare":            "size,modtime",
		"conflictResolve":    "path1",
		"conflictLoser":      "pathname",
		"conflictSuffix":     "lokal,Konflikt {2006-01-02 1504}",
		"_config":            map[string]any{"SuffixKeepExtension": true, "Checkers": 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("params mismatch\n got %#v\nwant %#v", got, want)
	}
	if _, ok := got["resyncMode"]; ok {
		t.Fatal("resyncMode implies --resync in rclone and must only be sent when a resync is due")
	}
}

func TestBisyncParamsResyncAndForce(t *testing.T) {
	it := store.OfflineItem{ID: "f1", Kind: "files", RemotePath: "Music", Files: []string{"a.wav"}, StoragePath: "/Users/mike/CloudWire/NC/Music", NeedsResync: true}
	got := BisyncParams(it, "cw-x1", testPaths(), "conflict", false)
	if got["resync"] != true || got["resyncMode"] != "newer" || got["force"] != nil {
		t.Fatalf("resync item: %#v", got)
	}
	if got["path2"] != "cw-x1:Music" || got["conflictSuffix"] != "lokal,conflict {2006-01-02 1504}" {
		t.Fatalf("files item uses the parent folder: %#v", got)
	}
	it.NeedsResync = false
	got = BisyncParams(it, "cw-x1", testPaths(), "conflict", true)
	if got["force"] != true || got["resync"] != nil {
		t.Fatalf("confirmed mass delete: %#v", got)
	}
}

func TestFilterLines(t *testing.T) {
	folder := store.OfflineItem{Kind: "folder", Excludes: []string{".DS_Store", "._*", " ", ".Trashes/**"}}
	if got := FilterLines(folder); !reflect.DeepEqual(got, []string{"- .DS_Store", "- ._*", "- .Trashes/**"}) {
		t.Fatalf("folder filters %q", got)
	}
	files := store.OfflineItem{Kind: "files", Excludes: []string{".DS_Store"}, Files: []string{"Mix [final].wav", "a*b?.aif", "Take {1}.wav"}}
	want := []string{"- .DS_Store", `+ /Mix \[final\].wav`, `+ /a\*b\?.aif`, `+ /Take \{1\}.wav`, "- **"}
	if got := FilterLines(files); !reflect.DeepEqual(got, want) {
		t.Fatalf("files filters\n got %q\nwant %q", got, want)
	}
	dir := t.TempDir()
	p := paths.ForHome(dir)
	files.ID = "id1"
	if err := WriteFilters(p, files); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.FiltersFile("id1"))
	if string(b) != strings.Join(want, "\n")+"\n" {
		t.Fatalf("filters file %q", b)
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

func TestDefaultStoragePath(t *testing.T) {
	if got := DefaultStoragePath("/Users/mike/CloudWire", "My: Cloud/1", "Music/Live:Sets"); got != "/Users/mike/CloudWire/My- Cloud-1/Music/Live-Sets" {
		t.Fatalf("got %q", got)
	}
	if got := DefaultStoragePath("/b", "NC", "../../etc"); got != "/b/NC/etc" {
		t.Fatalf("path traversal not removed: %q", got)
	}
}

func appCode(err error) string {
	if ae, ok := err.(*api.Error); ok {
		return ae.Code
	}
	return ""
}

func TestValidateOverlapAndMerge(t *testing.T) {
	p := testPaths()
	existing := []store.OfflineItem{
		{ID: "f", ConnectionID: "c1", Kind: "folder", RemotePath: "Music/Projekte", StoragePath: "/Users/mike/CloudWire/NC/Music/Projekte"},
		{ID: "s", ConnectionID: "c1", Kind: "files", RemotePath: "Samples", Files: []string{"kick.wav"}, StoragePath: "/Users/mike/CloudWire/NC/Samples"},
	}
	cases := []struct {
		name string
		it   store.OfflineItem
		code string
	}{
		{"parent folder", store.OfflineItem{ConnectionID: "c1", Kind: "folder", RemotePath: "Music", StoragePath: "/Users/mike/X"}, "offline.overlap"},
		{"child folder", store.OfflineItem{ConnectionID: "c1", Kind: "folder", RemotePath: "Music/Projekte/2026", StoragePath: "/Users/mike/Y"}, "offline.overlap"},
		{"other connection", store.OfflineItem{ConnectionID: "c2", Kind: "folder", RemotePath: "Music", StoragePath: "/Users/mike/Z"}, ""},
		{"file inside folder item", store.OfflineItem{ConnectionID: "c1", Kind: "files", RemotePath: "Music/Projekte", Files: []string{"a.wav"}, StoragePath: "/Users/mike/W"}, "offline.overlap"},
		{"same file elsewhere", store.OfflineItem{ConnectionID: "c1", Kind: "files", RemotePath: "Samples", Files: []string{"kick.wav"}, StoragePath: "/Users/mike/V"}, "offline.overlap"},
		{"other file elsewhere", store.OfflineItem{ConnectionID: "c1", Kind: "files", RemotePath: "Samples", Files: []string{"snare.wav"}, StoragePath: "/Users/mike/V"}, ""},
		{"local inside other", store.OfflineItem{ConnectionID: "c2", Kind: "folder", RemotePath: "A", StoragePath: "/Users/mike/CloudWire/NC/Music/Projekte/A"}, "offline.overlap"},
		{"local contains other", store.OfflineItem{ConnectionID: "c2", Kind: "folder", RemotePath: "A", StoragePath: "/Users/mike/CloudWire/NC"}, "offline.overlap"},
		{"inside mount", store.OfflineItem{ConnectionID: "c2", Kind: "folder", RemotePath: "A", StoragePath: "/Users/mike/CloudWire/Laufwerke/NC/A"}, "offline.overlap"},
		{"home itself", store.OfflineItem{ConnectionID: "c2", Kind: "folder", RemotePath: "A", StoragePath: "/Users/mike"}, "offline.overlap"},
		{"app support", store.OfflineItem{ConnectionID: "c2", Kind: "folder", RemotePath: "A", StoragePath: "/Users/mike/Library/Application Support/CloudWire/x"}, "offline.overlap"},
	}
	for _, c := range cases {
		_, err := validateNew(c.it, existing, []string{"/Users/mike/CloudWire/Laufwerke/NC"}, p)
		if appCode(err) != c.code || (c.code == "" && err != nil) {
			t.Errorf("%s: got %v, want code %q", c.name, err, c.code)
		}
	}
	merge := store.OfflineItem{ConnectionID: "c1", Kind: "files", RemotePath: "Samples", Files: []string{"snare.wav"}, StoragePath: "/Users/mike/CloudWire/NC/Samples"}
	v, err := validateNew(merge, existing, nil, p)
	if err != nil || v.mergeInto == nil || v.mergeInto.ID != "s" {
		t.Fatalf("files merge case: %+v %v", v, err)
	}
}
