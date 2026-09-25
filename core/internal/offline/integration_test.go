package offline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fslog "github.com/rclone/rclone/fs/log"

	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
	"github.com/DonMikone/CloudWire/core/internal/worker"
)

var (
	logMu    sync.Mutex
	logLines []sv.LogLine
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cw-offline")
	if err != nil {
		panic(err)
	}
	if err := rcl.Init(filepath.Join(dir, "rclone.conf"), "test-pass", rcl.Options{JSONLog: true, CacheDir: filepath.Join(dir, "cache")}); err != nil {
		panic(err)
	}
	if _, err := rcl.Call("config/create", map[string]any{"name": "loc", "type": "local", "parameters": map[string]any{}}); err != nil {
		panic(err)
	}
	// The worker runs rclone at INFO; copies and deletes are logged at that level.
	if _, err := rcl.Call("options/set", map[string]any{"main": map[string]any{"LogLevel": "INFO"}}); err != nil {
		panic(err)
	}
	// Capture rclone's JSON log lines like the supervisor does from worker stderr.
	fslog.Handler.AddOutput(true, func(_ slog.Level, text string) {
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			var l sv.LogLine
			if json.Unmarshal([]byte(line), &l) == nil {
				l.Raw = line
				logMu.Lock()
				logLines = append(logLines, l)
				logMu.Unlock()
			}
		}
	})
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type pair struct {
	t     *testing.T
	local string
	cloud string
	p     paths.Paths
	item  store.OfflineItem
}

func newPair(t *testing.T, kind string, files ...string) *pair {
	t.Helper()
	root := t.TempDir()
	pr := &pair{t: t, local: filepath.Join(root, "local"), cloud: filepath.Join(root, "cloud"), p: paths.ForHome(root)}
	for _, d := range []string{pr.local, pr.cloud} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pr.item = store.OfflineItem{ID: "it" + fmt.Sprint(time.Now().UnixNano()), Kind: kind, RemotePath: pr.cloud, Files: files,
		StoragePath: pr.local, Excludes: store.DefaultSettings().DefaultExcludes, NeedsResync: true}
	return pr
}

func write(t *testing.T, p, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// sync runs bisync in-process with exactly the params the worker would use.
func (pr *pair) sync(force bool) error {
	pr.t.Helper()
	if err := WriteFilters(pr.p, pr.item); err != nil {
		pr.t.Fatal(err)
	}
	if err := os.MkdirAll(pr.p.BisyncWorkdir(pr.item.ID), 0o700); err != nil {
		pr.t.Fatal(err)
	}
	logMu.Lock()
	logLines = nil
	logMu.Unlock()
	_, err := rcl.Call("sync/bisync", BisyncParams(pr.item, "loc", pr.p, "Konflikt", force))
	if err == nil {
		pr.item.NeedsResync = false
	}
	return err
}

func classified(label string) map[string]string {
	logMu.Lock()
	defer logMu.Unlock()
	out := map[string]string{}
	for _, l := range logLines {
		if a := ClassifyLog(l, label); a != "" && out[l.Object] != "conflict" {
			out[l.Object] = a
		}
	}
	return out
}

func TestBisyncInitialResyncAndConflict(t *testing.T) {
	pr := newPair(t, "folder")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	write(t, filepath.Join(pr.local, "a.txt"), "local a", base)
	write(t, filepath.Join(pr.cloud, "b.txt"), "cloud b", base)
	write(t, filepath.Join(pr.cloud, "Song.wav"), "v0", base)
	write(t, filepath.Join(pr.local, ".DS_Store"), "junk", base)
	if err := pr.sync(false); err != nil {
		t.Fatalf("initial resync: %v", err)
	}
	for _, d := range []string{pr.local, pr.cloud} {
		for _, n := range []string{"a.txt", "b.txt", "Song.wav"} {
			if _, err := os.Stat(filepath.Join(d, n)); err != nil {
				t.Fatalf("initial resync must copy both ways: %s missing in %s", n, d)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(pr.cloud, ".DS_Store")); err == nil {
		t.Fatal("excluded .DS_Store uploaded")
	}
	if got := classified("Konflikt"); got["b.txt"] != "transferred" || got["a.txt"] != "transferred" {
		t.Fatalf("log classification of initial copies: %v", got)
	}

	// Change the same file on both sides.
	write(t, filepath.Join(pr.local, "Song.wav"), "local edit", base.Add(10*time.Second))
	write(t, filepath.Join(pr.cloud, "Song.wav"), "cloud edit!!", base.Add(20*time.Second))
	if err := pr.sync(false); err != nil {
		t.Fatalf("conflict run: %v", err)
	}
	re := regexp.MustCompile(`^Song\.Konflikt \d{4}-\d{2}-\d{2} \d{4}\.wav$`)
	for _, d := range []string{pr.local, pr.cloud} {
		if got := read(t, filepath.Join(d, "Song.wav")); got != "local edit" {
			t.Fatalf("%s/Song.wav = %q; the local version must keep the name", d, got)
		}
		var conflict string
		for _, n := range names(t, d) {
			if re.MatchString(n) {
				conflict = n
			}
		}
		if conflict == "" {
			t.Fatalf("no conflict copy in %s: %v", d, names(t, d))
		}
		if got := read(t, filepath.Join(d, conflict)); got != "cloud edit!!" {
			t.Fatalf("conflict copy holds %q, want the cloud version", got)
		}
	}
	got := classified("Konflikt")
	found := false
	for obj, a := range got {
		if a == "conflict" && re.MatchString(filepath.Base(obj)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflict not recognised in the log: %v", got)
	}
}

func TestBisyncMassDeleteGuard(t *testing.T) {
	pr := newPair(t, "folder")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := range 10 {
		write(t, filepath.Join(pr.local, fmt.Sprintf("f%02d.wav", i)), "x", base)
	}
	if err := pr.sync(false); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		if err := os.Remove(filepath.Join(pr.local, fmt.Sprintf("f%02d.wav", i))); err != nil {
			t.Fatal(err)
		}
	}
	err := pr.sync(false)
	if err == nil || !worker.IsMassDelete(err.Error()) {
		t.Fatalf("deleting 6 of 10 must trip the Mass-Delete Guard, got %v", err)
	}
	if n := len(names(t, pr.cloud)); n != 10 {
		t.Fatalf("cloud must keep all 10 files after the guard stopped, has %d", n)
	}
	// "Nicht löschen – Dateien wiederherstellen": resync (newer) restores them locally.
	pr.item.NeedsResync = true
	if err := pr.sync(false); err != nil {
		t.Fatal(err)
	}
	if n := len(names(t, pr.local)); n != 10 {
		t.Fatalf("restore must bring back the deleted files, local has %d", n)
	}
}

func TestBisyncFilesItemSyncsOnlyListedFiles(t *testing.T) {
	pr := newPair(t, "files", "Keep [1].wav")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	write(t, filepath.Join(pr.cloud, "Keep [1].wav"), "keep", base)
	write(t, filepath.Join(pr.cloud, "Other.wav"), "other", base)
	write(t, filepath.Join(pr.cloud, "Sub", "Deep.wav"), "deep", base)
	write(t, filepath.Join(pr.local, "Private.txt"), "mine", base)
	if err := pr.sync(false); err != nil {
		t.Fatal(err)
	}
	got := names(t, pr.local)
	slices.Sort(got)
	if !slices.Equal(got, []string{"Keep [1].wav", "Private.txt"}) {
		t.Fatalf("local files %v", got)
	}
	if _, err := os.Stat(filepath.Join(pr.cloud, "Private.txt")); err == nil {
		t.Fatal("an unlisted local file was uploaded")
	}
}

// allFiles lists the regular files below dir, relative and sorted.
func allFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestBisyncNestedSelectionMirrorsTree(t *testing.T) {
	pr := newPair(t, "files", "Meine Daten/123/B", "Meine Daten/123/C")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, f := range []string{"Meine Daten/123/A/a.txt", "Meine Daten/123/B/b.txt", "Meine Daten/123/C/c.txt",
		"Meine Daten/123/readme.txt", "Meine Daten/other.txt"} {
		write(t, filepath.Join(pr.cloud, f), f, base)
	}
	if err := pr.sync(false); err != nil {
		t.Fatal(err)
	}
	if got := allFiles(t, pr.local); !slices.Equal(got, []string{"Meine Daten/123/B/b.txt", "Meine Daten/123/C/c.txt"}) {
		t.Fatalf("local files %v", got)
	}
	if _, err := os.Stat(filepath.Join(pr.local, "Meine Daten/123/A")); err == nil {
		t.Fatal("an unselected folder was created locally")
	}
	// A checked folder includes later additions; parents stay structure only.
	write(t, filepath.Join(pr.cloud, "Meine Daten/123/B/New/n.txt"), "new", base)
	write(t, filepath.Join(pr.local, "Meine Daten/123/local.txt"), "mine", base)
	if err := pr.sync(false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(pr.local, "Meine Daten/123/B/New/n.txt")); got != "new" {
		t.Fatalf("new cloud file %q", got)
	}
	if _, err := os.Stat(filepath.Join(pr.cloud, "Meine Daten/123/local.txt")); err == nil {
		t.Fatal("a local file in a structure-only parent was uploaded")
	}
}
