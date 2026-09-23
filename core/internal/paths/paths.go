// Package paths derives every on-disk location CloudWire uses. Setting the
// environment variable CLOUDWIRE_HOME relocates all of them (tests, E2E).
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Environment variables that relocate CloudWire state. Workers inherit them.
const (
	EnvHome            = "CLOUDWIRE_HOME"
	EnvKeychainService = "CLOUDWIRE_KEYCHAIN_SERVICE"
)

// DefaultKeychainService is the Keychain service prefix.
const DefaultKeychainService = "io.github.donmikone.cloudwire"

// Paths holds the resolved file system layout.
type Paths struct {
	Home         string // user home, or CLOUDWIRE_HOME
	AppSupport   string // ~/Library/Application Support/CloudWire (0700)
	DB           string
	RcloneConf   string
	Socket       string
	RunDir       string
	CoreLock     string
	ActiveFile   string
	StartRequest string
	BisyncDir    string
	FiltersDir   string
	TempDir      string
	CacheDir     string // rclone cache dir; the VFS cache lives in CacheDir/vfs
	LogDir       string
	LogFile      string
}

// Default resolves the layout from the environment.
func Default() Paths {
	home := os.Getenv(EnvHome)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return ForHome(home)
}

// ForHome resolves the layout below home.
func ForHome(home string) Paths {
	as := filepath.Join(home, "Library", "Application Support", "CloudWire")
	run := filepath.Join(as, "run")
	logs := filepath.Join(home, "Library", "Logs", "CloudWire")
	return Paths{
		Home:         home,
		AppSupport:   as,
		DB:           filepath.Join(as, "cloudwire.db"),
		RcloneConf:   filepath.Join(as, "rclone.conf"),
		Socket:       filepath.Join(as, "core.sock"),
		RunDir:       run,
		CoreLock:     filepath.Join(run, "core.lock"),
		ActiveFile:   filepath.Join(run, "active"),
		StartRequest: filepath.Join(run, "start-request"),
		BisyncDir:    filepath.Join(as, "bisync"),
		FiltersDir:   filepath.Join(as, "filters"),
		TempDir:      filepath.Join(as, "tmp"),
		CacheDir:     filepath.Join(home, "Library", "Caches", "CloudWire"),
		LogDir:       logs,
		LogFile:      filepath.Join(logs, "core.log"),
	}
}

// EnsureDirs creates every directory with mode 0700.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.AppSupport, p.RunDir, p.BisyncDir, p.FiltersDir, p.TempDir, p.CacheDir, p.LogDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Expand replaces a leading "~" with the (possibly relocated) home directory.
func (p Paths) Expand(s string) string {
	if s == "~" {
		return p.Home
	}
	if strings.HasPrefix(s, "~/") {
		return filepath.Join(p.Home, s[2:])
	}
	return s
}

// Collapse replaces a leading home directory with "~" (for display and storage).
func (p Paths) Collapse(s string) string {
	if s == p.Home {
		return "~"
	}
	if strings.HasPrefix(s, p.Home+"/") {
		return "~/" + s[len(p.Home)+1:]
	}
	return s
}

// BisyncWorkdir is the bisync working directory of an Offline Item.
func (p Paths) BisyncWorkdir(itemID string) string {
	return filepath.Join(p.BisyncDir, itemID)
}

// FiltersFile is the bisync filters file of an Offline Item.
func (p Paths) FiltersFile(itemID string) string {
	return filepath.Join(p.FiltersDir, itemID+".txt")
}

// KeychainService returns the Keychain service prefix.
func KeychainService() string {
	if s := os.Getenv(EnvKeychainService); s != "" {
		return s
	}
	return DefaultKeychainService
}

// VaultKeychainService returns the Keychain service for Vault passwords.
func VaultKeychainService() string {
	return KeychainService() + ".vault"
}

// IsWithin reports whether path equals root or lies below it. Both must be
// clean absolute paths.
func IsWithin(path, root string) bool {
	if path == root {
		return true
	}
	if root == "/" {
		return strings.HasPrefix(path, "/")
	}
	return strings.HasPrefix(path, root+"/")
}
