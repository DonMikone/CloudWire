// Package identity finds renames and moves inside an Offline Item by the
// identity of its folders and files (ADR 0011): the local inode (or, on
// volumes without persistent inodes, an exact tree match) and the cloud's
// file id. A worker detects them before a bisync run and applies each as one
// local rename or one server-side move, so bisync never sees them as a
// deletion plus a new transfer.
package identity

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/DonMikone/CloudWire/core/internal/selection"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// snapshotVersion is the current Snapshot format.
const snapshotVersion = 1

// Providers of cloud identities.
const (
	ProviderNextcloud = "nextcloud" // WebDAV oc:fileid
	ProviderIDs       = "ids"       // rclone object and directory IDs
	ProviderNone      = "none"      // no ids: cloud renames stay undetected
)

// Snapshot records the identities of an item's synced paths after its last
// successful run; the next run compares against it.
type Snapshot struct {
	Version    int                   `json:"version"`
	Persistent bool                  `json:"persistent"` // local inodes stay stable (APFS, HFS+)
	Provider   string                `json:"provider"`
	Files      []string              `json:"files"`     // Selection the snapshot was taken with
	RootCloud  CloudEntry            `json:"rootCloud"` // the item's root in the cloud
	Local      map[string]LocalEntry `json:"local"`     // item-relative path -> entry
	Cloud      map[string]CloudEntry `json:"cloud"`
}

// LocalEntry is a local folder or file.
type LocalEntry struct {
	Dir   bool   `json:"dir"`
	Ino   uint64 `json:"ino"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // Unix nanoseconds
}

// CloudEntry is a cloud folder or file.
type CloudEntry struct {
	Dir  bool   `json:"dir"`
	ID   string `json:"id"`
	ETag string `json:"etag,omitempty"`
}

// Load reads a snapshot; false if it is missing, corrupt or of another version.
func Load(path string) (Snapshot, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, false
	}
	var s Snapshot
	if json.Unmarshal(b, &s) != nil || s.Version != snapshotVersion {
		return Snapshot{}, false
	}
	return s, true
}

// Save writes a snapshot atomically.
func Save(path string, s Snapshot) error {
	s.Version = snapshotVersion
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}

// writeAtomic replaces path with data through a temporary file next to it.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// Apply records applied renames, in order: a move re-keys the moved paths on
// both sides; an upload or download forgets them (the run's capture records
// them again). Root renames change no item-relative path.
func (s *Snapshot) Apply(rs []sv.Rename) {
	for _, r := range rs {
		switch r.Result {
		case sv.RenameMoved:
			s.Local = rekey(s.Local, r.From, r.To)
			s.Cloud = rekey(s.Cloud, r.From, r.To)
		case sv.RenameUpload, sv.RenameDownload:
			s.Local = rekey(s.Local, r.From, "")
			s.Cloud = rekey(s.Cloud, r.From, "")
		}
	}
}

// rekey moves the entries at and below from to to; an empty to drops them.
func rekey[E any](m map[string]E, from, to string) map[string]E {
	moved := map[string]E{}
	for p, e := range m {
		if q, ok := selection.Rebase(p, from, to); ok {
			delete(m, p)
			moved[q] = e
		}
	}
	if to != "" {
		for q, e := range moved {
			m[q] = e
		}
	}
	return m
}
