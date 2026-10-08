// Tests for the CloudWire bisync patches (see docs/adr/0012).

package bisync

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/cmd/bisync/bilib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fstest/mockobject"
)

// TestFillAliasesFromListings pins the CloudWire patch to checkSync during
// --recover: backup listings whose names differ only in Unicode
// normalization (NFD on macOS, NFC in the cloud) must be paired into the
// AliasMap before the byte-exact comparison, or every recovery fails with
// "path1 and path2 are out of sync".
func TestFillAliasesFromListings(t *testing.T) {
	ctx := context.Background()

	b := &bisyncRun{
		aliases: bilib.AliasMap{},
		fctx:    ctx,
		opt:     &Options{},
	}
	// bisync defaults these; the byte-exact comparison only needs
	// size+modtime in play, as in the documented defaults.
	b.opt.Compare.Size = true
	b.opt.Compare.Modtime = true
	b.opt.Compare.Checksum = false

	// Save two listings with the real writer so loadListing reads them back.
	dir := t.TempDir()
	// NFD umlaut, macOS APFS form.
	nfd := "Stell d'\u0072 vu\u0308r RP.RPP"
	// NFC form as a webdav/Nextcloud listing carries it.
	nfc := "Stell d'r v\u00fcr RP.RPP"
	mtime := bisyncTZLoc()

	lst1 := newFileList()
	lst1.put(nfd, 4096, mtime, "", "-", "-")
	lst2 := newFileList()
	lst2.put(nfc, 4096, mtime, "", "-", "-")
	// A name that is fully equal on both sides must not get an alias.
	lst1.put("Equal Name.mp3", 42, mtime, "", "-", "-")
	lst2.put("Equal Name.mp3", 42, mtime, "", "-", "-")

	l1Path := filepath.Join(dir, "backup.path1.lst")
	l2Path := filepath.Join(dir, "backup.path2.lst")
	if err := lst1.save(l1Path); err != nil {
		t.Fatal(err)
	}
	if err := lst2.save(l2Path); err != nil {
		t.Fatal(err)
	}

	if err := b.fillAliasesFromListings(l1Path, l2Path); err != nil {
		t.Fatalf("fillAliasesFromListings: %v", err)
	}

	if got := b.aliases.Alias(nfd); got != nfc {
		t.Fatalf("alias of NFD name = %q, want %q", got, nfc)
	}
	if got := b.aliases.Alias(nfc); got != nfd {
		t.Fatalf("alias of NFC name = %q, want %q", got, nfd)
	}
	if got := b.aliases.Alias("Equal Name.mp3"); got != "Equal Name.mp3" {
		t.Fatalf("equal name must not be aliased, got %q", got)
	}

	// checkSync must now accept the backup pair; without the aliases it
	// fails with "path1 and path2 are out of sync".
	if err := b.checkSync(l1Path, l2Path); err != nil {
		t.Fatalf("checkSync after filling aliases: %v", err)
	}
}

// bisyncTZLoc returns a fixed modtime comparable on both sides.
func bisyncTZLoc() time.Time {
	return time.Date(2024, 1, 1, 12, 0, 0, 0, TZ)
}

// TestWhichEqualNoHashFallsBackToSizeModtime pins the CloudWire patch: a
// check that cannot compare hashes (e.g. a webdav object uploaded by
// another client without oc:checksums) must fall back to size+modtime
// like operations.Equal instead of reporting "not equal", which aborts
// every --resync with "Unable to rollback".
func TestWhichEqualNoHashFallsBackToSizeModtime(t *testing.T) {
	ctx := context.Background()

	b := &bisyncRun{
		aliases: bilib.AliasMap{},
		fctx:    ctx,
	}

	content := []byte("Stell d'r vür NEU Suno-Demo V4")
	mtime := bisyncTZLoc()
	// Both sides can hash normally; the objects must compare by size+modtime
	// inside noHashEqual when the Fs pair has no shared hash (the checkfn
	// noHash path). Use disjoint hash sets like local (SHA1) vs a cloud
	// backend (MD5) so no common type exists.
	src := contentObj("Stell d'r vür NEU Suno-Demo V4.mp3", content, mtime, hash.NewHashSet(hash.SHA1))
	dst := contentObj("Stell d'r vür NEU Suno-Demo V4.mp3", content, mtime, hash.NewHashSet(hash.MD5))

	// The noHash fallback must be size+modtime based.
	if !b.noHashEqual(ctx, src, dst) {
		t.Fatal("identical size+modtime objects must be equal when no hash is available")
	}
	dstLater := contentObj("Stell d'r vür NEU Suno-Demo V4.mp3", content, mtime.Add(2*time.Hour), hash.NewHashSet(hash.MD5))
	if b.noHashEqual(ctx, src, dstLater) {
		t.Fatal("objects with differing modtime must not be equal when no hash is available")
	}
	different := contentObj("Stell d'r vür NEU Suno-Demo V4.mp3", []byte("shorter"), mtime, hash.NewHashSet(hash.MD5))
	if b.noHashEqual(ctx, src, different) {
		t.Fatal("objects with differing size must not be equal when no hash is available")
	}
}

// contentObj builds a mock object on the hashEmptyFs: the Fs advertises
// SHA1 but the object's hash values are empty, exactly the shape of a
// webdav object uploaded by another client without oc:checksums.
func contentObj(remote string, content []byte, mtime time.Time, hashSet hash.Set) *mockobject.ContentMockObject {
	o := mockobject.New(remote).WithContent(content, mockobject.SeekModeNone)
	o.SetFs(emptyHashFs{set: hashSet})
	o.SetModTime(context.Background(), mtime)
	return o
}

// emptyHashFs advertises the given hashes; objects created for it answer
// every Hash request with an empty string (no server-side checksums).
type emptyHashFs struct {
	set hash.Set
	hashOnlyFs
}

func (f emptyHashFs) Hashes() hash.Set { return f.set }

// hashOnlyFs is a minimal fs.Fs whose Hashes() covers the types a
// listing-level check would ask for; the objects carry no hash values.
type hashOnlyFs struct{}

func (hashOnlyFs) Name() string             { return "hashOnly" }
func (hashOnlyFs) Root() string             { return "" }
func (hashOnlyFs) String() string           { return "hashOnly" }
func (hashOnlyFs) Precision() time.Duration { return time.Second }
func (hashOnlyFs) Hashes() hash.Set         { return hash.NewHashSet(hash.MD5) }
func (hashOnlyFs) Features() *fs.Features   { return &fs.Features{} }
func (hashOnlyFs) List(context.Context, string) (fs.DirEntries, error) {
	return nil, fs.ErrorDirNotFound
}
func (hashOnlyFs) NewObject(context.Context, string) (fs.Object, error) {
	return nil, fs.ErrorObjectNotFound
}
func (hashOnlyFs) Put(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) (fs.Object, error) {
	return nil, errors.New("not implemented")
}
func (hashOnlyFs) Mkdir(context.Context, string) error { return errors.New("not implemented") }
func (hashOnlyFs) Rmdir(context.Context, string) error { return errors.New("not implemented") }
