package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/keychain"
	"github.com/DonMikone/CloudWire/core/internal/offline"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// MinPasswordLength is the minimum vault password length.
const MinPasswordLength = 10

// Publisher pushes events.
type Publisher interface {
	Publish(eventType string, data any)
}

// Mounts starts and stops the Mounts of a Vault.
type Mounts interface {
	StopForConnection(connID string)
	StartForConnection(connID string)
	Forget(connID, remotePath string)
}

// Offline pauses and resumes the Offline Items of a Vault and runs migrations.
type Offline interface {
	PauseForConnection(connID, reason string)
	ResumeForConnection(connID string)
	EnqueueMigration(req *offline.MigrationRequest)
}

// Keychain stores vault passwords (replaceable in tests).
type Keychain interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

type systemKeychain struct{}

func (systemKeychain) Get(s, a string) (string, error) { return keychain.Get(s, a) }
func (systemKeychain) Set(s, a, v string) error        { return keychain.Set(s, a, v) }
func (systemKeychain) Delete(s, a string) error        { return keychain.Delete(s, a) }

// Service implements the vaults.* methods.
type Service struct {
	st    *store.Store
	log   *activity.Logger
	pub   Publisher
	paths paths.Paths

	Mounts   Mounts
	Offline  Offline
	Keychain Keychain

	mu         sync.Mutex
	unlocked   map[string]bool // vault connection ids
	migrations map[string]*migration
}

type migration struct {
	JobID      string   `json:"jobId"`
	VaultID    string   `json:"vaultId"`
	Status     string   `json:"status"`
	Mismatches []string `json:"mismatches"`
	Error      string   `json:"error,omitempty"`
	connID     string
	path       string
	isDir      bool
	verified   []string // source files proven to be in the Vault
}

// NewService creates the service.
func NewService(st *store.Store, log *activity.Logger, pub Publisher, p paths.Paths) *Service {
	return &Service{st: st, log: log, pub: pub, paths: p, Keychain: systemKeychain{},
		unlocked: map[string]bool{}, migrations: map[string]*migration{}}
}

// DTO is the contract's Vault object.
type DTO struct {
	store.Vault
	Unlocked bool `json:"unlocked"`
}

// RemoteName is the crypt remote of a Vault pseudo-Connection.
func RemoteName(vaultConnID string) string { return "cwvault-" + vaultConnID }

// IsLocked reports whether a Connection is a locked Vault.
func (s *Service) IsLocked(connID string) bool {
	c, err := s.st.Connection(connID)
	if err != nil || c.Kind != "vault" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.unlocked[connID]
}

func (s *Service) dto(v store.Vault) DTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return DTO{Vault: v, Unlocked: s.unlocked[v.VaultConnectionID]}
}

func (s *Service) changed() { s.pub.Publish("vaults.changed", struct{}{}) }

// Get returns a Vault or vault.notFound.
func (s *Service) Get(id string) (store.Vault, error) {
	v, err := s.st.Vault(id)
	if errors.Is(err, store.ErrNotFound) {
		return v, api.Errorf("vault.notFound", "Vault %s not found", id)
	}
	return v, err
}

// List returns all Vaults.
func (s *Service) List() ([]DTO, error) {
	vs, err := s.st.Vaults()
	if err != nil {
		return nil, err
	}
	out := make([]DTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, s.dto(v))
	}
	return out, nil
}

// Startup locks "ask" Vaults and unlocks Keychain Vaults (plan step 9).
func (s *Service) Startup() {
	vs, err := s.st.Vaults()
	if err != nil {
		return
	}
	for _, v := range vs {
		if v.UnlockMode == "ask" {
			_, _ = rcl.Call("config/delete", map[string]any{"name": RemoteName(v.VaultConnectionID)})
			continue
		}
		if s.cryptConfigured(v) {
			s.setUnlocked(v.VaultConnectionID, true)
			continue
		}
		go func() {
			if _, err := s.Unlock(context.Background(), v.ID, ""); err != nil {
				slog.Warn("vault auto-unlock", "vault", v.Name, "err", err)
			}
		}()
	}
}

func (s *Service) cryptConfigured(v store.Vault) bool {
	var cfg map[string]string
	if err := rcl.CallInto("config/get", map[string]any{"name": RemoteName(v.VaultConnectionID)}, &cfg); err != nil {
		return false
	}
	return cfg["type"] == "crypt"
}

func (s *Service) setUnlocked(connID string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.unlocked[connID] = true
	} else {
		delete(s.unlocked, connID)
	}
}

// releaseMemory hands the 64 MiB scrypt working set back to the system; the
// Core otherwise keeps it until the Go scavenger gets to it.
func releaseMemory() { go debug.FreeOSMemory() }

func joinRemote(a, b string) string {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}

func validName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/:\\") || name == "." || name == ".." {
		return api.Invalid("invalid vault name %q", name)
	}
	return nil
}

func validPassword(p string) error {
	if len([]rune(p)) < MinPasswordLength {
		return api.Invalid("the password needs at least %d characters", MinPasswordLength)
	}
	return nil
}

// ---- remote vault.json I/O ----

func (s *Service) tempFile() (string, error) {
	if err := os.MkdirAll(s.paths.TempDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.paths.TempDir, "vault-*.json")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
}

func (s *Service) fetchFile(remote, vaultPath string) (File, error) {
	tmp, err := s.tempFile()
	if err != nil {
		return File{}, err
	}
	defer os.Remove(tmp)
	if _, err := rcl.Call("operations/copyfile", map[string]any{
		"srcFs": remote + ":", "srcRemote": joinRemote(vaultPath, "vault.json"),
		"dstFs": filepath.Dir(tmp), "dstRemote": filepath.Base(tmp),
	}); err != nil {
		return File{}, api.Errorf("vault.invalidFormat", "Cannot read %s/vault.json: %v", vaultPath, err)
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return File{}, err
	}
	f, err := Parse(b)
	if err != nil {
		return File{}, api.Errorf("vault.invalidFormat", "%v", err)
	}
	return f, nil
}

// storeFile uploads vault.json. operations/uploadfile is unavailable in
// librclone, so the file is copied from a temporary local file.
func (s *Service) storeFile(remote, vaultPath string, f File) error {
	b, err := f.Marshal()
	if err != nil {
		return err
	}
	tmp, err := s.tempFile()
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	_, err = rcl.Call("operations/copyfile", map[string]any{
		"srcFs": filepath.Dir(tmp), "srcRemote": filepath.Base(tmp),
		"dstFs": remote + ":", "dstRemote": joinRemote(vaultPath, "vault.json"),
	})
	return err
}

func (s *Service) configureCrypt(v store.Vault, parent store.Connection, f File, sec Secrets) error {
	params := map[string]any{
		"remote":                    parent.RcloneRemote + ":" + joinRemote(v.VaultPath, "d"),
		"password":                  sec.Password,
		"password2":                 sec.Password2,
		"filename_encryption":       f.Crypt.FilenameEncryption,
		"directory_name_encryption": fmt.Sprint(f.Crypt.DirectoryNameEncryption),
		"filename_encoding":         f.Crypt.FilenameEncoding,
	}
	_, err := rcl.Call("config/create", map[string]any{"name": RemoteName(v.VaultConnectionID), "type": "crypt",
		"parameters": params, "opt": map[string]any{"obscure": true, "nonInteractive": true}})
	rcl.ForgetRemote(RemoteName(v.VaultConnectionID))
	return err
}

func (s *Service) conn(id string) (store.Connection, error) {
	c, err := s.st.Connection(id)
	if err != nil {
		return c, api.Errorf("connection.notFound", "Connection %s not found", id)
	}
	if c.Kind != "remote" {
		return c, api.Invalid("a Vault must live on a regular Connection")
	}
	return c, nil
}

// CreateParams are vaults.create params.
type CreateParams struct {
	ConnectionID string `json:"connectionId"`
	ParentPath   string `json:"parentPath"`
	Name         string `json:"name"`
	Password     string `json:"password"`
	UnlockMode   string `json:"unlockMode"`
}

// CreateResult is vaults.create's result.
type CreateResult struct {
	Vault       DTO    `json:"vault"`
	RecoveryKey string `json:"recoveryKey"`
}

func unlockMode(m string) (string, error) {
	switch m {
	case "", "keychain":
		return "keychain", nil
	case "ask":
		return "ask", nil
	}
	return "", api.Invalid("unlockMode must be keychain or ask")
}

// Create creates a new Vault at <parent>/<name>.cwvault.
func (s *Service) Create(ctx context.Context, p CreateParams) (CreateResult, error) {
	defer releaseMemory()
	parent, err := s.conn(p.ConnectionID)
	if err != nil {
		return CreateResult{}, err
	}
	if err := validName(p.Name); err != nil {
		return CreateResult{}, err
	}
	if err := validPassword(p.Password); err != nil {
		return CreateResult{}, err
	}
	mode, err := unlockMode(p.UnlockMode)
	if err != nil {
		return CreateResult{}, err
	}
	vaultPath := joinRemote(p.ParentPath, p.Name+".cwvault")
	var st struct {
		Item *struct{} `json:"item"`
	}
	if err := rcl.CallInto("operations/stat", map[string]any{"fs": parent.RcloneRemote + ":", "remote": vaultPath}, &st); err == nil && st.Item != nil {
		return CreateResult{}, api.Errorf("vault.exists", "%s already exists", vaultPath)
	}
	f, sec, rk, err := New(p.Name, p.Password, time.Now())
	if err != nil {
		return CreateResult{}, err
	}
	if _, err := rcl.Call("operations/mkdir", map[string]any{"fs": parent.RcloneRemote + ":", "remote": joinRemote(vaultPath, "d")}); err != nil {
		return CreateResult{}, api.Errorf("vault.invalidFormat", "Cannot create the Vault folder: %v", err)
	}
	if err := s.storeFile(parent.RcloneRemote, vaultPath, f); err != nil {
		return CreateResult{}, api.Errorf("vault.invalidFormat", "Cannot write vault.json: %v", err)
	}
	v, err := s.register(parent, vaultPath, p.Name, mode, f, sec, p.Password)
	if err != nil {
		return CreateResult{}, err
	}
	s.log.Info("vault", v.ID, fmt.Sprintf("Vault %q created at %s", v.Name, vaultPath), nil)
	return CreateResult{Vault: s.dto(v), RecoveryKey: rk}, nil
}

// register stores a Vault locally, configures its crypt remote and keeps the
// password in the Keychain when requested.
func (s *Service) register(parent store.Connection, vaultPath, name, mode string, f File, sec Secrets, password string) (store.Vault, error) {
	vs, err := s.st.Vaults()
	if err != nil {
		return store.Vault{}, err
	}
	for _, o := range vs {
		if o.ConnectionID == parent.ID && o.VaultPath == vaultPath {
			return store.Vault{}, api.Errorf("vault.exists", "This Vault is already added")
		}
	}
	connID := store.NewID()
	connName := name
	for i := 2; ; i++ {
		if _, err := s.st.ConnectionByName(connName); errors.Is(err, store.ErrNotFound) {
			break
		}
		connName = fmt.Sprintf("%s %d", name, i)
	}
	now := store.Now()
	v := store.Vault{ID: store.NewID(), ConnectionID: parent.ID, VaultPath: vaultPath, Name: name, VaultConnectionID: connID,
		UnlockMode: mode, CreatedAt: now}
	conn := store.Connection{ID: connID, Name: connName, Kind: "vault", RcloneRemote: RemoteName(connID), Provider: "crypt", CreatedAt: now}
	if err := s.st.InsertVault(v, conn); err != nil {
		return v, err
	}
	if err := s.configureCrypt(v, parent, f, sec); err != nil {
		_ = s.st.DeleteVault(v)
		return v, api.Errorf("vault.invalidFormat", "Cannot configure the Vault: %v", err)
	}
	s.setUnlocked(connID, true)
	if mode == "keychain" {
		if err := s.Keychain.Set(paths.VaultKeychainService(), v.ID, password); err != nil {
			s.log.Warn("vault", v.ID, "Could not store the Vault password in the Keychain: "+err.Error(), nil)
		}
	}
	s.pub.Publish("connections.changed", struct{}{})
	s.changed()
	return v, nil
}

// OpenParams are vaults.open params.
type OpenParams struct {
	ConnectionID string `json:"connectionId"`
	VaultPath    string `json:"vaultPath"`
	Password     string `json:"password"`
	UnlockMode   string `json:"unlockMode"`
}

// Open adds a Vault created elsewhere.
func (s *Service) Open(ctx context.Context, p OpenParams) (DTO, error) {
	defer releaseMemory()
	parent, err := s.conn(p.ConnectionID)
	if err != nil {
		return DTO{}, err
	}
	mode, err := unlockMode(p.UnlockMode)
	if err != nil {
		return DTO{}, err
	}
	vaultPath := strings.Trim(p.VaultPath, "/")
	f, err := s.fetchFile(parent.RcloneRemote, vaultPath)
	if err != nil {
		return DTO{}, err
	}
	sec, err := f.Unlock(p.Password)
	if err != nil {
		return DTO{}, mapUnwrap(err)
	}
	name := f.Name
	if name == "" {
		name = strings.TrimSuffix(path.Base(vaultPath), ".cwvault")
	}
	v, err := s.register(parent, vaultPath, name, mode, f, sec, p.Password)
	if err != nil {
		return DTO{}, err
	}
	s.log.Info("vault", v.ID, fmt.Sprintf("Vault %q opened", v.Name), nil)
	return s.dto(v), nil
}

func mapUnwrap(err error) error {
	if errors.Is(err, ErrWrongPassword) {
		return api.Errorf("vault.wrongPassword", "The password is not correct")
	}
	if errors.Is(err, ErrInvalidFormat) {
		return api.Errorf("vault.invalidFormat", "%v", err)
	}
	return err
}

// Unlock configures the crypt remote. An empty password reads the Keychain.
func (s *Service) Unlock(ctx context.Context, id, password string) (DTO, error) {
	defer releaseMemory()
	v, err := s.Get(id)
	if err != nil {
		return DTO{}, err
	}
	parent, err := s.conn(v.ConnectionID)
	if err != nil {
		return DTO{}, err
	}
	if password == "" {
		password, err = s.Keychain.Get(paths.VaultKeychainService(), v.ID)
		if err != nil {
			return DTO{}, api.Errorf("vault.locked", "The Vault password is required")
		}
	}
	f, err := s.fetchFile(parent.RcloneRemote, v.VaultPath)
	if err != nil {
		return DTO{}, err
	}
	sec, err := f.Unlock(password)
	if err != nil {
		return DTO{}, mapUnwrap(err)
	}
	if err := s.configureCrypt(v, parent, f, sec); err != nil {
		return DTO{}, api.Errorf("vault.invalidFormat", "%v", err)
	}
	s.setUnlocked(v.VaultConnectionID, true)
	if v.UnlockMode == "keychain" {
		_ = s.Keychain.Set(paths.VaultKeychainService(), v.ID, password)
	}
	if s.Mounts != nil {
		s.Mounts.StartForConnection(v.VaultConnectionID)
	}
	if s.Offline != nil {
		s.Offline.ResumeForConnection(v.VaultConnectionID)
	}
	s.log.Info("vault", v.ID, fmt.Sprintf("Vault %q unlocked", v.Name), nil)
	s.changed()
	return s.dto(v), nil
}

// Lock removes the crypt remote, unmounts and pauses the Vault's items.
func (s *Service) Lock(ctx context.Context, id string) (DTO, error) {
	v, err := s.Get(id)
	if err != nil {
		return DTO{}, err
	}
	if s.Mounts != nil {
		s.Mounts.StopForConnection(v.VaultConnectionID)
	}
	if s.Offline != nil {
		s.Offline.PauseForConnection(v.VaultConnectionID, offline.ReasonVaultLocked)
	}
	_, _ = rcl.Call("config/delete", map[string]any{"name": RemoteName(v.VaultConnectionID)})
	rcl.ForgetRemote(RemoteName(v.VaultConnectionID))
	s.setUnlocked(v.VaultConnectionID, false)
	s.log.Info("vault", v.ID, fmt.Sprintf("Vault %q locked", v.Name), nil)
	s.changed()
	return s.dto(v), nil
}

// ChangePassword re-wraps the password slot only.
func (s *Service) ChangePassword(ctx context.Context, id, oldPassword, newPassword string) error {
	defer releaseMemory()
	v, err := s.Get(id)
	if err != nil {
		return err
	}
	if err := validPassword(newPassword); err != nil {
		return err
	}
	parent, err := s.conn(v.ConnectionID)
	if err != nil {
		return err
	}
	f, err := s.fetchFile(parent.RcloneRemote, v.VaultPath)
	if err != nil {
		return err
	}
	sec, err := f.Unlock(oldPassword)
	if err != nil {
		return mapUnwrap(err)
	}
	if err := f.SetPassword(newPassword, sec); err != nil {
		return err
	}
	if err := s.storeFile(parent.RcloneRemote, v.VaultPath, f); err != nil {
		return api.Errorf("vault.invalidFormat", "Cannot write vault.json: %v", err)
	}
	if v.UnlockMode == "keychain" {
		_ = s.Keychain.Set(paths.VaultKeychainService(), v.ID, newPassword)
	}
	s.log.Info("vault", v.ID, fmt.Sprintf("Password of Vault %q changed", v.Name), nil)
	return nil
}

// RecoverParams are vaults.recover params.
type RecoverParams struct {
	ConnectionID string `json:"connectionId"`
	VaultPath    string `json:"vaultPath"`
	RecoveryKey  string `json:"recoveryKey"`
	NewPassword  string `json:"newPassword"`
}

// Recover sets a new password using the Recovery Key.
func (s *Service) Recover(ctx context.Context, p RecoverParams) (DTO, error) {
	defer releaseMemory()
	parent, err := s.conn(p.ConnectionID)
	if err != nil {
		return DTO{}, err
	}
	if err := validPassword(p.NewPassword); err != nil {
		return DTO{}, err
	}
	vaultPath := strings.Trim(p.VaultPath, "/")
	f, err := s.fetchFile(parent.RcloneRemote, vaultPath)
	if err != nil {
		return DTO{}, err
	}
	sec, err := f.UnlockRecovery(p.RecoveryKey)
	if err != nil {
		if errors.Is(err, ErrWrongPassword) {
			return DTO{}, api.Errorf("vault.wrongPassword", "The Recovery Key is not correct")
		}
		return DTO{}, mapUnwrap(err)
	}
	if err := f.SetPassword(p.NewPassword, sec); err != nil {
		return DTO{}, err
	}
	if err := s.storeFile(parent.RcloneRemote, vaultPath, f); err != nil {
		return DTO{}, api.Errorf("vault.invalidFormat", "Cannot write vault.json: %v", err)
	}
	vs, _ := s.st.Vaults()
	for _, v := range vs {
		if v.ConnectionID == parent.ID && v.VaultPath == vaultPath {
			s.log.Info("vault", v.ID, fmt.Sprintf("Vault %q reset with its Recovery Key", v.Name), nil)
			return s.Unlock(ctx, v.ID, p.NewPassword)
		}
	}
	return s.Open(ctx, OpenParams{ConnectionID: parent.ID, VaultPath: vaultPath, Password: p.NewPassword, UnlockMode: "keychain"})
}

// ExportRclone returns an rclone config section for emergencies.
func (s *Service) ExportRclone(ctx context.Context, id, password string) (string, error) {
	defer releaseMemory()
	v, err := s.Get(id)
	if err != nil {
		return "", err
	}
	parent, err := s.conn(v.ConnectionID)
	if err != nil {
		return "", err
	}
	f, err := s.fetchFile(parent.RcloneRemote, v.VaultPath)
	if err != nil {
		return "", err
	}
	sec, err := f.Unlock(password)
	if err != nil {
		return "", mapUnwrap(err)
	}
	section := strings.NewReplacer(" ", "-", "[", "", "]", "").Replace(v.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "# CloudWire Vault %q - rclone crypt configuration\n", v.Name)
	fmt.Fprintf(&b, "# Replace %q with the name of your own rclone remote for this cloud account.\n", parent.RcloneRemote)
	fmt.Fprintf(&b, "[%s]\ntype = crypt\n", section)
	fmt.Fprintf(&b, "remote = %s:%s\n", parent.RcloneRemote, joinRemote(v.VaultPath, "d"))
	fmt.Fprintf(&b, "password = %s\n", obscure.MustObscure(sec.Password))
	fmt.Fprintf(&b, "password2 = %s\n", obscure.MustObscure(sec.Password2))
	fmt.Fprintf(&b, "filename_encryption = %s\n", f.Crypt.FilenameEncryption)
	fmt.Fprintf(&b, "directory_name_encryption = %v\n", f.Crypt.DirectoryNameEncryption)
	fmt.Fprintf(&b, "filename_encoding = %s\n", f.Crypt.FilenameEncoding)
	s.log.Info("vault", v.ID, fmt.Sprintf("Emergency rclone configuration of %q exported", v.Name), nil)
	return b.String(), nil
}

// Remove forgets a Vault locally; the cloud data stays untouched.
func (s *Service) Remove(ctx context.Context, id string) error {
	v, err := s.Get(id)
	if err != nil {
		return err
	}
	var deps []map[string]string
	ms, _ := s.st.Mounts()
	for _, m := range ms {
		if m.ConnectionID == v.VaultConnectionID {
			deps = append(deps, map[string]string{"kind": "mount", "id": m.ID, "name": m.VolumeName})
		}
	}
	its, _ := s.st.OfflineItems()
	for _, it := range its {
		if it.ConnectionID == v.VaultConnectionID {
			deps = append(deps, map[string]string{"kind": "offline", "id": it.ID, "name": it.StoragePath})
		}
	}
	if len(deps) > 0 {
		return api.Errorf("connection.inUse", "The Vault %q is still used by %d item(s)", v.Name, len(deps)).WithData("dependents", deps)
	}
	_ = s.Keychain.Delete(paths.VaultKeychainService(), v.ID)
	_, _ = rcl.Call("config/delete", map[string]any{"name": RemoteName(v.VaultConnectionID)})
	rcl.ForgetRemote(RemoteName(v.VaultConnectionID))
	if err := s.st.DeleteVault(v); err != nil {
		return err
	}
	s.setUnlocked(v.VaultConnectionID, false)
	s.log.Info("vault", v.ID, fmt.Sprintf("Vault %q removed from this Mac (cloud data unchanged)", v.Name), nil)
	s.pub.Publish("connections.changed", struct{}{})
	s.changed()
	return nil
}

// ---- encrypting existing data ----

// EncryptParams are vaults.encryptExisting params.
type EncryptParams struct {
	ConnectionID string `json:"connectionId"`
	Path         string `json:"path"`
	IsDir        bool   `json:"isDir"`
	Target       struct {
		NewVault *struct {
			Name       string `json:"name"`
			Password   string `json:"password"`
			UnlockMode string `json:"unlockMode"`
		} `json:"newVault"`
		VaultID string `json:"vaultId"`
		SubPath string `json:"subPath"`
	} `json:"target"`
	IgnorePauseRules bool `json:"ignorePauseRules"`
}

// EncryptResult is vaults.encryptExisting's result.
type EncryptResult struct {
	JobID       string `json:"jobId"`
	VaultID     string `json:"vaultId"`
	RecoveryKey string `json:"recoveryKey,omitempty"`
}

func within(p, root string) bool {
	p, root = strings.Trim(p, "/"), strings.Trim(root, "/")
	return root == "" || p == root || strings.HasPrefix(p, root+"/")
}

// EncryptExisting copies a file or folder into a Vault, verifies it and waits
// for the user to confirm deleting the original.
func (s *Service) EncryptExisting(ctx context.Context, p EncryptParams) (EncryptResult, error) {
	parent, err := s.conn(p.ConnectionID)
	if err != nil {
		return EncryptResult{}, err
	}
	src := strings.Trim(p.Path, "/")
	if src == "" {
		return EncryptResult{}, api.Invalid("the cloud root cannot be encrypted as a whole")
	}
	its, _ := s.st.OfflineItems()
	for _, it := range its {
		if it.ConnectionID == parent.ID && (within(src, it.RemotePath) || within(it.RemotePath, src)) {
			return EncryptResult{}, api.Errorf("vault.sourceIsOffline", "Remove the Offline Item %s first; its local copy would be re-uploaded", it.StoragePath)
		}
	}
	vs, _ := s.st.Vaults()
	for _, v := range vs {
		if v.ConnectionID == parent.ID && (within(src, v.VaultPath) || within(v.VaultPath, src)) {
			return EncryptResult{}, api.Invalid("the source overlaps the Vault %q", v.Name)
		}
	}
	var res EncryptResult
	var v store.Vault
	dst := path.Base(src)
	switch {
	case p.Target.NewVault != nil:
		nv := p.Target.NewVault
		cr, err := s.Create(ctx, CreateParams{ConnectionID: parent.ID, ParentPath: path.Dir("/" + src), Name: nv.Name,
			Password: nv.Password, UnlockMode: nv.UnlockMode})
		if err != nil {
			return EncryptResult{}, err
		}
		v, res.RecoveryKey = cr.Vault.Vault, cr.RecoveryKey
	case p.Target.VaultID != "":
		v, err = s.Get(p.Target.VaultID)
		if err != nil {
			return EncryptResult{}, err
		}
		if v.ConnectionID != parent.ID {
			return EncryptResult{}, api.Invalid("the Vault lives on another Connection")
		}
		if s.IsLocked(v.VaultConnectionID) {
			return EncryptResult{}, api.Errorf("vault.locked", "Unlock the Vault first")
		}
		dst = joinRemote(p.Target.SubPath, dst)
	default:
		return EncryptResult{}, api.Invalid("target is required")
	}
	m := &migration{JobID: store.NewID(), VaultID: v.ID, Status: "queued", Mismatches: []string{}, connID: parent.ID, path: src, isDir: p.IsDir}
	s.mu.Lock()
	s.migrations[m.JobID] = m
	s.mu.Unlock()
	res.JobID, res.VaultID = m.JobID, v.ID
	s.publishMigration(m)
	s.log.Info("vault", v.ID, fmt.Sprintf("Encrypting %q into Vault %q", "/"+src, v.Name), map[string]string{"jobId": m.JobID})
	s.Offline.EnqueueMigration(&offline.MigrationRequest{
		JobID: m.JobID, VaultID: v.ID, IgnorePause: p.IgnorePauseRules,
		Job: sv.MigrateJob{SrcFs: parent.RcloneRemote + ":", SrcPath: src, IsDir: p.IsDir, DstFs: RemoteName(v.VaultConnectionID) + ":", DstPath: dst},
		OnStart: func() {
			s.updateMigration(m.JobID, func(m *migration) { m.Status = "running" })
		},
		OnResult: func(out sv.Msg) { s.onMigrationResult(m.JobID, v, out) },
	})
	return res, nil
}

func (s *Service) publishMigration(m *migration) {
	s.mu.Lock()
	cp := *m
	cp.Mismatches = append([]string{}, m.Mismatches...)
	s.mu.Unlock()
	s.pub.Publish("vault.migration", cp)
}

func (s *Service) updateMigration(id string, f func(*migration)) {
	s.mu.Lock()
	m := s.migrations[id]
	if m != nil {
		f(m)
	}
	s.mu.Unlock()
	if m != nil {
		s.publishMigration(m)
	}
}

func (s *Service) onMigrationResult(id string, v store.Vault, out sv.Msg) {
	s.updateMigration(id, func(m *migration) {
		switch out.Status {
		case sv.StatusVerified:
			m.Status, m.verified = "verified", out.Files
		case sv.StatusMismatch:
			m.Status, m.Mismatches = "mismatch", out.Mismatches
		default:
			m.Status, m.Error = "error", out.Error
		}
	})
	switch out.Status {
	case sv.StatusVerified:
		s.log.Info("vault", v.ID, fmt.Sprintf("Encrypted copy in %q verified; waiting for confirmation to delete the original", v.Name), map[string]string{"jobId": id})
	case sv.StatusMismatch:
		s.log.Error("vault", v.ID, fmt.Sprintf("Verification of the encrypted copy in %q found %d mismatches; the original is kept", v.Name, len(out.Mismatches)), out.Mismatches)
	default:
		s.log.Error("vault", v.ID, fmt.Sprintf("Encrypting into %q failed: %s", v.Name, out.Error), nil)
	}
}

// MigrationConfirmDelete deletes the original after a verified migration.
func (s *Service) MigrationConfirmDelete(ctx context.Context, jobID string) error {
	s.mu.Lock()
	m := s.migrations[jobID]
	var ok bool
	if m != nil {
		ok = m.Status == "verified"
	}
	s.mu.Unlock()
	if m == nil {
		return api.Errorf("vault.notFound", "Migration %s not found", jobID)
	}
	if !ok {
		return api.Invalid("the original can only be deleted after a successful verification")
	}
	parent, err := s.conn(m.connID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	verified := append([]string{}, m.verified...)
	s.mu.Unlock()
	// Delete only files proven to be in the Vault: anything added to the
	// source after the verification stays where it is.
	fsName := parent.RcloneRemote + ":"
	var failed []string
	if m.isDir {
		for _, f := range verified {
			if _, err := rcl.Call("operations/deletefile", map[string]any{"fs": fsName, "remote": joinRemote(m.path, f)}); err != nil {
				failed = append(failed, f)
			}
		}
		// Remove the folders that are empty now; folders with new files stay.
		_, _ = rcl.Call("operations/rmdirs", map[string]any{"fs": fsName, "remote": m.path, "leaveRoot": false})
	} else if len(verified) > 0 {
		if _, err := rcl.Call("operations/deletefile", map[string]any{"fs": fsName, "remote": m.path}); err != nil {
			failed = append(failed, m.path)
		}
	}
	if len(failed) > 0 {
		return api.Errorf("vault.invalidFormat", "Deleting %d original file(s) failed", len(failed)).WithData("files", failed)
	}
	if s.Mounts != nil {
		s.Mounts.Forget(parent.ID, path.Dir("/" + m.path)[1:])
	}
	s.updateMigration(jobID, func(m *migration) { m.Status = "deleted" })
	s.log.Info("vault", m.VaultID, fmt.Sprintf("Original %q deleted after encryption", "/"+m.path), nil)
	return nil
}
