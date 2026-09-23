package vault

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/keychain"
	"github.com/DonMikone/CloudWire/core/internal/notify"
	"github.com/DonMikone/CloudWire/core/internal/offline"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

var cloudDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cw-vault")
	if err != nil {
		panic(err)
	}
	if err := rcl.Init(filepath.Join(dir, "rclone.conf"), "test-pass", rcl.Options{CacheDir: filepath.Join(dir, "cache")}); err != nil {
		panic(err)
	}
	cloudDir = filepath.Join(dir, "cloud")
	if err := os.MkdirAll(cloudDir, 0o755); err != nil {
		panic(err)
	}
	// An alias remote makes "cw-p:" the cloud root, like a real provider.
	if _, err := rcl.Call("config/create", map[string]any{"name": "cw-p", "type": "alias", "parameters": map[string]any{"remote": cloudDir}}); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type memKeychain map[string]string

func (m memKeychain) Get(s, a string) (string, error) {
	v, ok := m[s+"/"+a]
	if !ok {
		return "", keychain.ErrNotFound
	}
	return v, nil
}
func (m memKeychain) Set(s, a, v string) error { m[s+"/"+a] = v; return nil }
func (m memKeychain) Delete(s, a string) error { delete(m, s+"/"+a); return nil }

type nopPub struct{}

func (nopPub) Publish(string, any) {}

type nopNotify struct{}

func (nopNotify) Notify(string, any) {}

// newMac simulates a Mac with its own database and the parent Connection.
func newMac(t *testing.T) *Service {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.InsertConnection(store.Connection{ID: "p", Name: "Cloud", Kind: "remote", RcloneRemote: "cw-p", Provider: "alias", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	s := NewService(st, activity.New(st, nopPub{}), nopNotify{}, nopPub{}, paths.ForHome(home))
	s.Keychain = memKeychain{}
	return s
}

func putFile(t *testing.T, fsName, remote string, content []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl.Call("operations/copyfile", map[string]any{"srcFs": filepath.Dir(tmp), "srcRemote": "src.bin", "dstFs": fsName, "dstRemote": remote}); err != nil {
		t.Fatal(err)
	}
}

func getFile(t *testing.T, fsName, remote string) []byte {
	t.Helper()
	dir := t.TempDir()
	if _, err := rcl.Call("operations/copyfile", map[string]any{"srcFs": fsName, "srcRemote": remote, "dstFs": dir, "dstRemote": "out.bin"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out.bin"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func code(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestVaultLifecycleAcrossMacs(t *testing.T) {
	ctx := context.Background()
	mac1 := newMac(t)
	res, err := mac1.Create(ctx, CreateParams{ConnectionID: "p", ParentPath: "Music", Name: "Geheim", Password: "correct horse", UnlockMode: "keychain"})
	if err != nil {
		t.Fatal(err)
	}
	v := res.Vault
	if v.VaultPath != "Music/Geheim.cwvault" || !v.Unlocked || res.RecoveryKey == "" {
		t.Fatalf("created vault %+v", res)
	}
	if _, err := os.Stat(filepath.Join(cloudDir, "Music/Geheim.cwvault/vault.json")); err != nil {
		t.Fatal("vault.json missing in the cloud")
	}
	if _, err := mac1.Create(ctx, CreateParams{ConnectionID: "p", ParentPath: "Music", Name: "Geheim", Password: "correct horse"}); code(err) != "vault.exists" {
		t.Fatalf("second create: %v", err)
	}
	content := []byte("secret bassline take 3")
	cryptFs := RemoteName(v.VaultConnectionID) + ":"
	putFile(t, cryptFs, "Sub Folder/Bassline.wav", content)

	// Names and contents are encrypted in the cloud.
	var plainNames []string
	_ = filepath.Walk(filepath.Join(cloudDir, "Music/Geheim.cwvault/d"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && (strings.Contains(fi.Name(), "Bassline") || strings.Contains(fi.Name(), "Sub Folder")) {
			plainNames = append(plainNames, p)
		}
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, content) {
				t.Errorf("plaintext content stored in %s", p)
			}
		}
		return nil
	})
	if len(plainNames) > 0 {
		t.Fatalf("plaintext names in the cloud: %v", plainNames)
	}

	// Locking removes access.
	if _, err := mac1.Lock(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if !mac1.IsLocked(v.VaultConnectionID) {
		t.Fatal("vault still unlocked")
	}
	if _, err := rcl.Call("operations/list", map[string]any{"fs": cryptFs, "remote": ""}); err == nil {
		t.Fatal("crypt remote still usable after lock")
	}
	// Keychain unlock (no password given).
	if _, err := mac1.Unlock(ctx, v.ID, ""); err != nil {
		t.Fatalf("keychain unlock: %v", err)
	}
	if !bytes.Equal(getFile(t, cryptFs, "Sub Folder/Bassline.wav"), content) {
		t.Fatal("content changed after unlock")
	}
	if err := mac1.ChangePassword(ctx, v.ID, "correct horse", "new staple password"); err != nil {
		t.Fatal(err)
	}
	if err := mac1.Remove(ctx, v.ID); err != nil {
		t.Fatal(err)
	}

	// Another Mac opens the Vault with the (changed) password only.
	mac2 := newMac(t)
	if _, err := mac2.Open(ctx, OpenParams{ConnectionID: "p", VaultPath: "Music/Geheim.cwvault", Password: "correct horse"}); code(err) != "vault.wrongPassword" {
		t.Fatalf("old password must fail after the change, got %v", err)
	}
	v2, err := mac2.Open(ctx, OpenParams{ConnectionID: "p", VaultPath: "Music/Geheim.cwvault", Password: "new staple password", UnlockMode: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	if got := getFile(t, RemoteName(v2.VaultConnectionID)+":", "Sub Folder/Bassline.wav"); !bytes.Equal(got, content) {
		t.Fatalf("read back %q", got)
	}
	// Recovery Key resets the password.
	if _, err := mac2.Recover(ctx, RecoverParams{ConnectionID: "p", VaultPath: "Music/Geheim.cwvault", RecoveryKey: res.RecoveryKey, NewPassword: "recovered password"}); err != nil {
		t.Fatal(err)
	}
	ini, err := mac2.ExportRclone(ctx, v2.ID, "recovered password")
	if err != nil || !strings.Contains(ini, "type = crypt") || !strings.Contains(ini, "remote = cw-p:Music/Geheim.cwvault/d") {
		t.Fatalf("export: %v\n%s", err, ini)
	}
}

func TestShortPasswordRejected(t *testing.T) {
	s := newMac(t)
	if _, err := s.Create(context.Background(), CreateParams{ConnectionID: "p", Name: "x", Password: "short"}); err == nil {
		t.Fatal("short password accepted")
	}
}

type fakeOffline struct{ reqs []*offline.MigrationRequest }

func (*fakeOffline) PauseForConnection(string, string)              {}
func (*fakeOffline) ResumeForConnection(string)                     {}
func (f *fakeOffline) EnqueueMigration(r *offline.MigrationRequest) { f.reqs = append(f.reqs, r) }
func (*fakeOffline) CancelMigration(string) bool                    { return true }

type recNotify struct {
	mu     sync.Mutex
	params []map[string]any
}

func (r *recNotify) Notify(kind string, p any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == notify.KindVaultMigration {
		r.params = append(r.params, p.(map[string]any))
	}
}

func migrationOf(t *testing.T, s *Service, jobID string) Migration {
	t.Helper()
	for _, m := range s.Migrations() {
		if m.JobID == jobID {
			return m
		}
	}
	t.Fatalf("migration %s not listed", jobID)
	return Migration{}
}

func TestMigrationCancelAndResult(t *testing.T) {
	ctx := context.Background()
	s := newMac(t)
	off, notes := &fakeOffline{}, &recNotify{}
	s.Offline, s.notify = off, notes
	res, err := s.Create(ctx, CreateParams{ConnectionID: "p", Name: "Jobs", Password: "correct horse", UnlockMode: "keychain"})
	if err != nil {
		t.Fatal(err)
	}
	encrypt := func(src string) string {
		p := EncryptParams{ConnectionID: "p", Path: src, IsDir: true}
		p.Target.VaultID = res.Vault.ID
		r, err := s.EncryptExisting(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return r.JobID
	}
	isInvalid := func(err error) bool { return errors.As(err, new(api.InvalidParams)) }

	// A canceled job ignores the engine's late callbacks.
	canceled := encrypt("Docs")
	req := off.reqs[0]
	if err := s.MigrationCancel(ctx, canceled); err != nil {
		t.Fatal(err)
	}
	req.OnStart()
	req.OnProgress(sv.Msg{Type: "progress", Bytes: 5, TotalBytes: 10})
	req.OnResult(sv.Msg{Type: "result", Status: sv.StatusError, Error: "context canceled"})
	if m := migrationOf(t, s, canceled); m.Status != "canceled" || m.Bytes != 0 || m.Error != "" ||
		m.ConnectionID != "p" || m.Path != "Docs" || !m.IsDir || m.CreatedAt == 0 {
		t.Fatalf("canceled job %+v", m)
	}
	if err := s.MigrationCancel(ctx, canceled); !isInvalid(err) {
		t.Fatalf("second cancel: %v", err)
	}

	// A finished job reports progress, its result and a notification.
	done := encrypt("Photos")
	req = off.reqs[1]
	req.OnStart()
	req.OnProgress(sv.Msg{Type: "progress", Bytes: 5, TotalBytes: 10, Transfers: 1})
	if m := migrationOf(t, s, done); m.Status != "running" || m.Bytes != 5 || m.TotalBytes != 10 || m.Transfers != 1 {
		t.Fatalf("running job %+v", m)
	}
	req.OnResult(sv.Msg{Type: "result", Status: sv.StatusMismatch, Mismatches: []string{"a.jpg"}})
	if m := migrationOf(t, s, done); m.Status != "mismatch" {
		t.Fatalf("finished job %+v", m)
	}
	if err := s.MigrationCancel(ctx, done); !isInvalid(err) {
		t.Fatalf("cancel after the end: %v", err)
	}
	if len(notes.params) != 1 || notes.params[0]["jobId"] != done || notes.params[0]["status"] != "mismatch" ||
		notes.params[0]["count"] != 1 || notes.params[0]["path"] != "Photos" || notes.params[0]["vaultName"] != "Jobs" {
		t.Fatalf("notifications %v", notes.params)
	}
}
