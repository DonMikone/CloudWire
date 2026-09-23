package vault

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/keychain"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
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
	s := NewService(st, activity.New(st, nopPub{}), nopPub{}, paths.ForHome(home))
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
