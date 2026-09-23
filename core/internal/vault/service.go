package vault

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/keychain"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/notify"
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
	CancelMigration(jobID string) bool
}

// Notifier queues notifications.
type Notifier interface {
	Notify(kind string, params any)
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
	st     *store.Store
	log    *activity.Logger
	notify Notifier
	pub    Publisher
	paths  paths.Paths

	Mounts   Mounts
	Offline  Offline
	Keychain Keychain

	mu         sync.Mutex
	unlocked   map[string]bool // vault connection ids
	migrations map[string]*Migration
}

// Migration is the contract's vault.migration object: a job encrypting
// existing data into a Vault.
type Migration struct {
	JobID        string   `json:"jobId"`
	VaultID      string   `json:"vaultId"`
	ConnectionID string   `json:"connectionId"` // source Connection
	Path         string   `json:"path"`         // source path relative to the Connection
	IsDir        bool     `json:"isDir"`
	Status       string   `json:"status"` // queued | running | verified | mismatch | error | deleted | canceled
	Mismatches   []string `json:"mismatches"`
	Error        string   `json:"error,omitempty"`
	Bytes        int64    `json:"bytes"`
	TotalBytes   int64    `json:"totalBytes"`
	Transfers    int64    `json:"transfers"`
	CreatedAt    int64    `json:"createdAt"`
	vaultName    string
	verified     []string // source files proven to be in the Vault
}

func (m *Migration) active() bool { return m.Status == "queued" || m.Status == "running" }

// NewService creates the service.
func NewService(st *store.Store, log *activity.Logger, n Notifier, pub Publisher, p paths.Paths) *Service {
	return &Service{st: st, log: log, notify: n, pub: pub, paths: p, Keychain: systemKeychain{},
		unlocked: map[string]bool{}, migrations: map[string]*Migration{}}
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
		return v, api.Fail("vault.notFound", msg.New("vault.notFound", "id", id))
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
		return api.InvalidText(msg.New("vault.invalidName", "name", name))
	}
	return nil
}

func validPassword(p string) error {
	if len([]rune(p)) < MinPasswordLength {
		return api.InvalidText(msg.New("vault.passwordTooShort", "count", MinPasswordLength))
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
		if rcl.IsNotFound(err) {
			return File{}, api.Fail("vault.invalidFormat", msg.New("vault.noVaultFile", "path", vaultPath))
		}
		return File{}, api.Fail("vault.ioFailed", msg.New("vault.readFailed", "path", vaultPath, "detail", err))
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return File{}, err
	}
	f, err := Parse(b)
	if err != nil {
		return File{}, api.Wrap("vault.invalidFormat", err)
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
		return c, api.Fail("connection.notFound", msg.New("connection.notFound", "id", id))
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
		return CreateResult{}, api.Fail("vault.exists", msg.New("vault.exists", "path", vaultPath))
	}
	f, sec, rk, err := New(p.Name, p.Password, time.Now())
	if err != nil {
		return CreateResult{}, err
	}
	if _, err := rcl.Call("operations/mkdir", map[string]any{"fs": parent.RcloneRemote + ":", "remote": joinRemote(vaultPath, "d")}); err != nil {
		return CreateResult{}, api.Fail("vault.ioFailed", msg.New("vault.createFolderFailed", "detail", err))
	}
	if err := s.storeFile(parent.RcloneRemote, vaultPath, f); err != nil {
		return CreateResult{}, api.Fail("vault.ioFailed", msg.New("vault.writeFailed", "detail", err))
	}
	v, err := s.register(parent, vaultPath, p.Name, mode, f, sec, p.Password)
	if err != nil {
		return CreateResult{}, err
	}
	s.log.Info("vault", v.ID, msg.New("vault.created", "name", v.Name, "path", vaultPath), nil)
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
			return store.Vault{}, api.Fail("vault.alreadyAdded", msg.New("vault.alreadyAdded"))
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
		return v, api.Fail("vault.invalidFormat", msg.New("vault.configureFailed", "detail", err))
	}
	s.setUnlocked(connID, true)
	if mode == "keychain" {
		if err := s.Keychain.Set(paths.VaultKeychainService(), v.ID, password); err != nil {
			s.log.Warn("vault", v.ID, msg.New("vault.keychainFailed", "detail", err), nil)
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
	s.log.Info("vault", v.ID, msg.New("vault.opened", "name", v.Name), nil)
	return s.dto(v), nil
}

func mapUnwrap(err error) error {
	if errors.Is(err, ErrWrongPassword) {
		return api.Fail("vault.wrongPassword", msg.New("vault.wrongPassword"))
	}
	if errors.Is(err, ErrInvalidFormat) {
		return api.Wrap("vault.invalidFormat", err)
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
			return DTO{}, api.Fail("vault.locked", msg.New("vault.passwordRequired"))
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
		return DTO{}, api.Wrap("vault.invalidFormat", err)
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
	s.log.Info("vault", v.ID, msg.New("vault.unlocked", "name", v.Name), nil)
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
	s.log.Info("vault", v.ID, msg.New("vault.locked", "name", v.Name), nil)
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
		return api.Fail("vault.ioFailed", msg.New("vault.writeFailed", "detail", err))
	}
	if v.UnlockMode == "keychain" {
		_ = s.Keychain.Set(paths.VaultKeychainService(), v.ID, newPassword)
	}
	s.log.Info("vault", v.ID, msg.New("vault.passwordChanged", "name", v.Name), nil)
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
			return DTO{}, api.Fail("vault.wrongPassword", msg.New("vault.wrongRecoveryKey"))
		}
		return DTO{}, mapUnwrap(err)
	}
	if err := f.SetPassword(p.NewPassword, sec); err != nil {
		return DTO{}, err
	}
	if err := s.storeFile(parent.RcloneRemote, vaultPath, f); err != nil {
		return DTO{}, api.Fail("vault.ioFailed", msg.New("vault.writeFailed", "detail", err))
	}
	vs, _ := s.st.Vaults()
	for _, v := range vs {
		if v.ConnectionID == parent.ID && v.VaultPath == vaultPath {
			s.log.Info("vault", v.ID, msg.New("vault.recovered", "name", v.Name), nil)
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
	s.log.Info("vault", v.ID, msg.New("vault.rcloneExported", "name", v.Name), nil)
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
			deps = append(deps, map[string]string{"kind": "offline", "id": it.ID, "name": offline.ItemName(it)})
		}
	}
	if len(deps) > 0 {
		return api.Fail("vault.inUse", msg.New("vault.inUse", "name", v.Name, "count", len(deps))).WithData("dependents", deps)
	}
	_ = s.Keychain.Delete(paths.VaultKeychainService(), v.ID)
	_, _ = rcl.Call("config/delete", map[string]any{"name": RemoteName(v.VaultConnectionID)})
	rcl.ForgetRemote(RemoteName(v.VaultConnectionID))
	if err := s.st.DeleteVault(v); err != nil {
		return err
	}
	s.setUnlocked(v.VaultConnectionID, false)
	s.log.Info("vault", v.ID, msg.New("vault.removed", "name", v.Name), nil)
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
		return EncryptResult{}, api.InvalidText(msg.New("vault.cloudRoot"))
	}
	its, _ := s.st.OfflineItems()
	for _, it := range its {
		if it.ConnectionID == parent.ID && (within(src, it.RemotePath) || within(it.RemotePath, src)) {
			return EncryptResult{}, api.Fail("vault.sourceIsOffline", msg.New("vault.sourceIsOffline", "path", it.StoragePath))
		}
	}
	vs, _ := s.st.Vaults()
	for _, v := range vs {
		if v.ConnectionID == parent.ID && (within(src, v.VaultPath) || within(v.VaultPath, src)) {
			return EncryptResult{}, api.InvalidText(msg.New("vault.sourceOverlapsVault", "name", v.Name))
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
			return EncryptResult{}, api.Fail("vault.locked", msg.New("vault.unlockFirst"))
		}
		dst = joinRemote(p.Target.SubPath, dst)
	default:
		return EncryptResult{}, api.Invalid("target is required")
	}
	m := &Migration{JobID: store.NewID(), VaultID: v.ID, ConnectionID: parent.ID, Path: src, IsDir: p.IsDir, Status: "queued",
		Mismatches: []string{}, CreatedAt: time.Now().UnixMilli(), vaultName: v.Name}
	s.mu.Lock()
	s.migrations[m.JobID] = m
	s.mu.Unlock()
	res.JobID, res.VaultID = m.JobID, v.ID
	s.publishMigration(m)
	s.log.Info("vault", v.ID, msg.New("vault.encryptStarted", "path", "/"+src, "name", v.Name), map[string]string{"jobId": m.JobID})
	s.Offline.EnqueueMigration(&offline.MigrationRequest{
		JobID: m.JobID, VaultID: v.ID, IgnorePause: p.IgnorePauseRules,
		Job: sv.MigrateJob{SrcFs: parent.RcloneRemote + ":", SrcPath: src, IsDir: p.IsDir, DstFs: RemoteName(v.VaultConnectionID) + ":", DstPath: dst},
		OnStart: func() {
			s.updateMigration(m.JobID, func(m *Migration) bool {
				if m.Status != "queued" {
					return false // canceled meanwhile
				}
				m.Status = "running"
				return true
			})
		},
		OnProgress: func(p sv.Msg) {
			s.updateMigration(m.JobID, func(m *Migration) bool {
				if !m.active() {
					return false
				}
				m.Bytes, m.TotalBytes, m.Transfers = p.Bytes, p.TotalBytes, p.Transfers
				return true
			})
		},
		OnResult: func(out sv.Msg) { s.onMigrationResult(m.JobID, v, out) },
	})
	return res, nil
}

// Migrations returns the migrations of this Core run, newest first.
func (s *Service) Migrations() []Migration {
	s.mu.Lock()
	out := make([]Migration, 0, len(s.migrations))
	for _, m := range s.migrations {
		cp := *m
		cp.Mismatches = append([]string{}, m.Mismatches...)
		out = append(out, cp)
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b Migration) int {
		return cmp.Or(cmp.Compare(b.CreatedAt, a.CreatedAt), strings.Compare(b.JobID, a.JobID))
	})
	return out
}

func (s *Service) publishMigration(m *Migration) {
	s.mu.Lock()
	cp := *m
	cp.Mismatches = append([]string{}, m.Mismatches...)
	s.mu.Unlock()
	s.pub.Publish("vault.migration", cp)
}

// updateMigration applies f and publishes the migration when f reports a
// change. It returns whether f changed it.
func (s *Service) updateMigration(id string, f func(*Migration) bool) bool {
	s.mu.Lock()
	m := s.migrations[id]
	changed := m != nil && f(m)
	s.mu.Unlock()
	if changed {
		s.publishMigration(m)
	}
	return changed
}

func (s *Service) onMigrationResult(id string, v store.Vault, out sv.Msg) {
	var src string
	if !s.updateMigration(id, func(m *Migration) bool {
		if !m.active() {
			return false // canceled
		}
		switch out.Status {
		case sv.StatusVerified:
			m.Status, m.verified = "verified", out.Files
		case sv.StatusMismatch:
			m.Status, m.Mismatches = "mismatch", out.Mismatches
		default:
			m.Status, m.Error = "error", out.Error
		}
		src = m.Path
		return true
	}) {
		return
	}
	status, count, message := "verified", 0, ""
	switch out.Status {
	case sv.StatusVerified:
		s.log.Info("vault", v.ID, msg.New("vault.encryptVerified", "name", v.Name), map[string]string{"jobId": id})
	case sv.StatusMismatch:
		status, count = "mismatch", len(out.Mismatches)
		s.log.Error("vault", v.ID, msg.New("vault.encryptMismatch", "count", len(out.Mismatches), "name", v.Name), out.Mismatches)
	default:
		status, message = "error", out.Error
		s.log.Error("vault", v.ID, msg.New("vault.encryptFailed", "name", v.Name, "detail", out.Error), nil)
	}
	s.notify.Notify(notify.KindVaultMigration, map[string]any{"jobId": id, "vaultId": v.ID, "vaultName": v.Name,
		"status": status, "path": src, "count": count, "message": message})
}

// MigrationCancel stops a queued or running migration. Files already copied
// into the Vault stay there; the original is untouched.
func (s *Service) MigrationCancel(ctx context.Context, jobID string) error {
	s.mu.Lock()
	m := s.migrations[jobID]
	active := m != nil && m.active()
	s.mu.Unlock()
	if m == nil {
		return api.Fail("vault.notFound", msg.New("vault.migrationNotFound", "id", jobID))
	}
	if !active || !s.Offline.CancelMigration(jobID) {
		return api.Invalid("only a queued or running job can be canceled")
	}
	s.updateMigration(jobID, func(m *Migration) bool {
		m.Status = "canceled"
		return true
	})
	s.log.Info("vault", m.VaultID, msg.New("vault.encryptCanceled", "path", "/"+m.Path, "name", m.vaultName), map[string]string{"jobId": jobID})
	return nil
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
		return api.Fail("vault.notFound", msg.New("vault.migrationNotFound", "id", jobID))
	}
	if !ok {
		return api.Invalid("the original can only be deleted after a successful verification")
	}
	parent, err := s.conn(m.ConnectionID)
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
	if m.IsDir {
		for _, f := range verified {
			if _, err := rcl.Call("operations/deletefile", map[string]any{"fs": fsName, "remote": joinRemote(m.Path, f)}); err != nil {
				failed = append(failed, f)
			}
		}
		// Remove the folders that are empty now; folders with new files stay.
		_, _ = rcl.Call("operations/rmdirs", map[string]any{"fs": fsName, "remote": m.Path, "leaveRoot": false})
	} else if len(verified) > 0 {
		if _, err := rcl.Call("operations/deletefile", map[string]any{"fs": fsName, "remote": m.Path}); err != nil {
			failed = append(failed, m.Path)
		}
	}
	if len(failed) > 0 {
		return api.Fail("vault.deleteFailed", msg.New("vault.deleteFailed", "count", len(failed), "files", strings.Join(failed, ", "))).
			WithData("files", failed)
	}
	if s.Mounts != nil {
		s.Mounts.Forget(parent.ID, path.Dir("/" + m.Path)[1:])
	}
	s.updateMigration(jobID, func(m *Migration) bool {
		m.Status = "deleted"
		return true
	})
	s.log.Info("vault", m.VaultID, msg.New("vault.originalDeleted", "path", "/"+m.Path), nil)
	return nil
}
