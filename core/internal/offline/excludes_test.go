package offline

import (
	"context"
	"path"
	"testing"

	"github.com/rclone/rclone/fs/filter"

	"github.com/DonMikone/CloudWire/core/internal/selection"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// The default excludes skip DAW autosave folders in bisync's filters exactly
// where selection.Excluded (watcher, local scans, listing cleanup) skips them.
func TestDefaultExcludesSkipDAWAutosaves(t *testing.T) {
	excludes := store.DefaultSettings().DefaultExcludes
	for _, kind := range []string{"folder", "files"} {
		it := store.OfflineItem{Kind: kind, Excludes: excludes, Files: []string{"Musikprojekte", "Cubase"}}
		f, err := filter.NewFilter(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range FilterLines(it) {
			if err := f.AddRule(rule); err != nil {
				t.Fatalf("rule %q: %v", rule, err)
			}
		}
		dirs := f.IncludeDirectory(context.Background(), nil)
		// rclone skips a file if its rule excludes it or bisync never lists its folder.
		rcloneSkips := func(p string) bool {
			for d := path.Dir(p); d != "."; d = path.Dir(d) {
				if ok, err := dirs(d); err != nil || !ok {
					return true
				}
			}
			return !f.IncludeRemote(p)
		}
		for p, want := range map[string]bool{
			"Musikprojekte/X/Song.logicx/Alternatives/000/Autosave/2026-11-02 12:14:48Z.songData": true,
			"Musikprojekte/Song.logicx/Alternatives/001/Autosave/x.songData":                      true,
			"Musikprojekte/Song.band/Alternatives/000/Autosave/x":                                 true,
			"Cubase/Song/Auto Saves/Song-01.bak":                                                  true,
			"Musikprojekte/X/Song.logicx/Alternatives/000/ProjectData":                            false,
			"Musikprojekte/X/Song.logicx/Alternatives/000/Project File Backups/00.cst":            false,
			"Musikprojekte/X/Song.logicx/Resources/Autosave/x":                                    false,
			"Cubase/Song/Project File Backups/Song-01.cpr":                                        false,
			"Cubase/Song/Song.cpr": false,
		} {
			if got := rcloneSkips(p); got != want {
				t.Errorf("%s: rclone skips %q = %v, want %v", kind, p, got, want)
			}
			if got := selection.Excluded(p, it.Excludes); got != want {
				t.Errorf("selection.Excluded(%q) = %v, want %v", p, got, want)
			}
		}
		if ok, _ := dirs("Musikprojekte/X/Song.logicx/Alternatives/000/Autosave"); ok {
			t.Errorf("%s: the Autosave folder itself is listed", kind)
		}
	}
}
