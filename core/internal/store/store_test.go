package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cloudwire.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudwire.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.SchemaVersion(); v != len(migrations) {
		t.Fatalf("schema version %d, want %d", v, len(migrations))
	}
	var av int
	if err := s.DB().QueryRow(`PRAGMA auto_vacuum`).Scan(&av); err != nil || av != 2 {
		t.Fatalf("auto_vacuum = %d (%v), want 2 (incremental)", av, err)
	}
	if err := s.InsertConnection(Connection{ID: "a", Name: "Cloud", Kind: "remote", RcloneRemote: "cw-a", Provider: "webdav", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	c, err := s.ConnectionByName("CLOUD")
	if err != nil || c.ID != "a" {
		t.Fatalf("case-insensitive lookup after reopen: %+v %v", c, err)
	}
	err = s.InsertConnection(Connection{ID: "b", Name: "cloud", Kind: "remote", RcloneRemote: "cw-b", Provider: "webdav", CreatedAt: 1})
	if !IsUniqueViolation(err) {
		t.Fatalf("duplicate name must violate UNIQUE NOCASE, got %v", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := openTest(t)
	err := s.InsertMount(Mount{ID: "m", ConnectionID: "missing", MountPoint: "/x", VolumeName: "x", MountType: "nfsmount", CacheMaxGB: 1})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("expected foreign key failure, got %v", err)
	}
}

func TestSettingsDefaultsAndMergePatch(t *testing.T) {
	s := openTest(t)
	st, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if st.QuietPeriodSeconds != 60 || st.Bandwidth.DownloadMiBps != 20 || !st.PauseRules.CPU.Enabled || st.Log.Level != "info" {
		t.Fatalf("defaults wrong: %+v", st)
	}
	st, err = s.UpdateSettings(map[string]any{"pauseRules": map[string]any{"cpu": map[string]any{"thresholdPercent": 85}}})
	if err != nil {
		t.Fatal(err)
	}
	if st.PauseRules.CPU.ThresholdPercent != 85 || !st.PauseRules.CPU.Enabled || !st.PauseRules.Battery.Enabled {
		t.Fatalf("merge patch must keep siblings: %+v", st.PauseRules)
	}
	st, _ = s.Settings()
	if st.PauseRules.CPU.ThresholdPercent != 85 {
		t.Fatal("setting not persisted")
	}
	if _, err := s.UpdateSettings(map[string]any{"defaultMountType": "fuse"}); err == nil {
		t.Fatal("invalid mount type accepted")
	}
	st, _ = s.UpdateSettings(map[string]any{"pauseRules": nil})
	if st.PauseRules.CPU.ThresholdPercent != 70 {
		t.Fatalf("null must reset to default, got %d", st.PauseRules.CPU.ThresholdPercent)
	}
}

func TestActivityFilters(t *testing.T) {
	s := openTest(t)
	mustAppend := func(level, cat, msg string) {
		if _, err := s.AppendActivity(level, cat, "", msg, map[string]any{"k": msg}); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend("info", "sync", "Synced Music")
	mustAppend("error", "mount", "Mount failed 50%_off")
	mustAppend("warn", "sync", "Mass delete")
	got, _ := s.QueryActivity(ActivityFilter{Levels: []string{"error", "warn"}})
	if len(got) != 2 || got[0].Message != "Mass delete" {
		t.Fatalf("level filter/newest first: %+v", got)
	}
	got, _ = s.QueryActivity(ActivityFilter{Categories: []string{"sync"}, Search: "music"})
	if len(got) != 1 || got[0].Message != "Synced Music" {
		t.Fatalf("category+search: %+v", got)
	}
	got, _ = s.QueryActivity(ActivityFilter{Search: "50%_"})
	if len(got) != 1 {
		t.Fatalf("LIKE wildcards must be escaped: %+v", got)
	}
	got, _ = s.QueryActivity(ActivityFilter{Search: "5_%"})
	if len(got) != 0 {
		t.Fatalf("escaped search matched literally-different text: %+v", got)
	}
	got, _ = s.QueryActivity(ActivityFilter{Limit: 1})
	if len(got) != 1 {
		t.Fatal("limit ignored")
	}
}

func TestPruneByAge(t *testing.T) {
	s := openTest(t)
	old := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	if _, err := s.DB().Exec(`INSERT INTO activity(ts,level,category,message) VALUES (?,?,?,?)`, old, "info", "core", "old"); err != nil {
		t.Fatal(err)
	}
	runID, _ := s.StartRun("item", "bisync")
	_ = s.FinishRun(SyncRun{ID: runID, Status: "ok"}, []SyncRunFile{{Action: "transferred", Path: "a"}})
	_, _ = s.DB().Exec(`UPDATE sync_runs SET started_at=? WHERE id=?`, old, runID)
	_, _ = s.AppendActivity("info", "core", "", "new", nil)
	if err := s.Prune(30, 50); err != nil {
		t.Fatal(err)
	}
	got, _ := s.QueryActivity(ActivityFilter{})
	if len(got) != 1 || got[0].Message != "new" {
		t.Fatalf("age pruning: %+v", got)
	}
	var files int
	_ = s.DB().QueryRow(`SELECT count(*) FROM sync_run_files`).Scan(&files)
	if files != 0 {
		t.Fatal("run files must cascade with their run")
	}
}

func TestPruneBySize(t *testing.T) {
	s := openTest(t)
	big := strings.Repeat("x", 4096)
	tx, _ := s.DB().Begin()
	for i := range 1500 {
		if _, err := tx.Exec(`INSERT INTO activity(ts,level,category,message) VALUES (?,?,?,?)`, time.Now().UnixMilli()+int64(i), "info", "core", big); err != nil {
			t.Fatal(err)
		}
	}
	_ = tx.Commit()
	before, _ := s.Size()
	if before < 5<<20 {
		t.Fatalf("test data too small: %d", before)
	}
	if err := s.Prune(30, 2); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Size()
	if after > 2<<20 {
		t.Fatalf("size after prune %d > 2 MB", after)
	}
	// The newest entries survive.
	got, _ := s.QueryActivity(ActivityFilter{Limit: 1})
	var maxTS int64
	_ = s.DB().QueryRow(`SELECT max(ts) FROM activity`).Scan(&maxTS)
	if len(got) != 1 || got[0].TS != maxTS {
		t.Fatal("oldest entries must be deleted first")
	}
	var n int
	_ = s.DB().QueryRow(`SELECT count(*) FROM activity`).Scan(&n)
	if n == 0 {
		t.Fatal("size pruning deleted everything")
	}
	var free int
	_ = s.DB().QueryRow(`PRAGMA freelist_count`).Scan(&free)
	if free != 0 {
		t.Fatalf("incremental vacuum left %d free pages; the file never shrinks", free)
	}
}

func TestNewIDFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewID()
		if len(id) != 12 || strings.ToLower(id) != id || strings.ContainsAny(id, "018=") {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatal("duplicate id")
		}
		seen[id] = true
	}
}
