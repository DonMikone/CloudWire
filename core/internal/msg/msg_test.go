package msg

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	until := time.Date(2026, 9, 23, 18, 30, 0, 0, time.Local).UnixMilli()
	tests := []struct {
		name string
		got  Text
		want Text
	}{
		{"params fill the template", New("mount.created", "name", "Nextcloud", "path", "/Volumes/NC"),
			Text{Code: "mount.created", Params: Params{"name": "Nextcloud", "path": "/Volumes/NC"},
				Message: `Mount "Nextcloud" created at /Volumes/NC`}},
		{"no params", New("core.stopped"), Text{Code: "core.stopped", Message: "CloudWire Core stopped"}},
		{"errors become their text", New("sync.failed", "name", "Album", "detail", errors.New("directory not found")),
			Text{Code: "sync.failed", Params: Params{"name": "Album", "detail": "directory not found"},
				Message: `Sync of "Album" failed: directory not found`}},
		{"empty detail is dropped", New("sync.failed", "name", "Album", "detail", ""),
			Text{Code: "sync.failed", Params: Params{"name": "Album"}, Message: `Sync of "Album" failed`}},
		{"detail after a full sentence", New("offline.cloudFolderMissing", "detail", "directory not found"),
			Text{Code: "offline.cloudFolderMissing", Params: Params{"detail": "directory not found"},
				Message: "The cloud folder was not found. Restore it in the cloud or remove the Offline Item. directory not found"}},
		{"byte counts stay raw in params", New("offline.insufficientSpace", "neededBytes", uint64(3<<30), "freeBytes", int64(512<<20)),
			Text{Code: "offline.insufficientSpace", Params: Params{"neededBytes": "3221225472", "freeBytes": "536870912"},
				Message: "Not enough free space: 3.0 GiB needed, 512.0 MiB available"}},
		{"times stay raw in params", New("offline.pausedUntil", "untilMs", until),
			Text{Code: "offline.pausedUntil", Params: Params{"untilMs": value(until)},
				Message: "Syncing paused until 2026-09-23 18:30"}},
		{"raw text only", Detail("exit status 1"),
			Text{Code: CodeDetail, Params: Params{"detail": "exit status 1"}, Message: "exit status 1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("got %#v\nwant %#v", tc.got, tc.want)
			}
		})
	}
}

func TestBecause(t *testing.T) {
	failed := New("mount.failedRepeatedly", "name", "NC")
	tests := []struct {
		name  string
		cause Text
		want  Text
	}{
		{"raw cause becomes the detail", Detail("mount helper exited"),
			Text{Code: "mount.failedRepeatedly", Params: Params{"name": "NC", "detail": "mount helper exited"},
				Message: `Mount "NC" failed repeatedly: mount helper exited`}},
		{"coded cause is nested", New("mount.pointBusy", "path", "/Volumes/NC"),
			Text{Code: "mount.failedRepeatedly", Params: Params{"name": "NC", "cause": "mount.pointBusy", "path": "/Volumes/NC"},
				Message: `Mount "NC" failed repeatedly: /Volumes/NC is used by another volume`}},
		{"a cause keeps its own detail", New("mount.stillAttached").Because(Detail("busy")),
			Text{Code: "mount.failedRepeatedly", Params: Params{"name": "NC", "cause": "mount.stillAttached", "detail": "busy"},
				Message: `Mount "NC" failed repeatedly: The previous mount is still attached: busy`}},
		{"no cause", Text{}, failed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := failed.Because(tc.cause); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
	if _, ok := failed.Params["cause"]; ok {
		t.Fatal("Because must not modify the receiver")
	}
}

func TestProgrammingErrorsPanicUnderTest(t *testing.T) {
	for name, f := range map[string]func(){
		"unknown code":  func() { New("mount.typo") },
		"missing param": func() { New("mount.created", "name", "NC") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			f()
		})
	}
}

// Every placeholder must be closed and named, or New could never fill it.
func TestTemplatesAreWellFormed(t *testing.T) {
	for code, tmpl := range templates {
		rest := tmpl
		for {
			i := strings.IndexAny(rest, "{}")
			if i < 0 {
				break
			}
			end := strings.IndexByte(rest[i:], '}')
			if rest[i] != '{' || end <= 1 || strings.ContainsAny(rest[i+1:i+end], "{ ") {
				t.Errorf("%s: malformed placeholder in %q", code, tmpl)
				break
			}
			rest = rest[i+end+1:]
		}
	}
}
