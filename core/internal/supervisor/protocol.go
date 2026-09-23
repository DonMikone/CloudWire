// Package supervisor runs jobs in isolated worker processes: one per active
// Mount, and short-lived background-priority workers for sync and Vault
// migration (plan Appendix A, "Worker protocol").
package supervisor

import "encoding/json"

// Job types.
const (
	JobMount        = "mount"
	JobBisync       = "bisync"
	JobVaultMigrate = "vault-migrate"
)

// Result statuses.
const (
	StatusOK         = "ok"
	StatusError      = "error"
	StatusMassDelete = "massDelete"
	StatusStopped    = "stopped"
	StatusVerified   = "verified"
	StatusMismatch   = "mismatch"
)

// Job is the first stdin line's "job" object.
type Job struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Background bool   `json:"background"`
	// LogLevel is rclone's level: "INFO" or "DEBUG".
	LogLevel string `json:"logLevel"`
	// BwLimit is the core/bwlimit rate applied before the job ("off" = none).
	BwLimit string          `json:"bwLimit,omitempty"`
	Mount   *MountJob       `json:"mount,omitempty"`
	Bisync  json.RawMessage `json:"bisync,omitempty"` // sync/bisync params, passed through verbatim
	Migrate *MigrateJob     `json:"migrate,omitempty"`
}

// MountJob describes a Mount.
//
// FUSE Mounts use rclone's mount/mount with Params. NFS Mounts (Serve) run
// rclone's NFS server with serve/start on a fixed localhost Port, with Params
// as its options, and attach MountPoint with the system NFS client. Because
// the port and the (disk-persisted) NFS file handles stay the same, a
// restarted worker is picked up by the existing kernel mount without
// unmounting (Reattach).
type MountJob struct {
	Params       map[string]any `json:"params"`
	Serve        bool           `json:"serve,omitempty"`
	Port         int            `json:"port,omitempty"`
	MountPoint   string         `json:"mountPoint,omitempty"`
	MountOptions []string       `json:"mountOptions,omitempty"`
}

// MigrateJob copies a file or folder into a Vault and verifies the copy.
type MigrateJob struct {
	SrcFs   string `json:"srcFs"`   // source remote root, e.g. "cw-abc:"
	SrcPath string `json:"srcPath"` // file or folder path below SrcFs
	IsDir   bool   `json:"isDir"`
	DstFs   string `json:"dstFs"`   // crypt remote root, e.g. "cwvault-xyz:"
	DstPath string `json:"dstPath"` // destination path inside the Vault
}

// Secrets travel only over stdin.
type Secrets struct {
	ConfigPass string `json:"configPass"`
}

// Start is the first line written to a worker's stdin.
type Start struct {
	Job     Job     `json:"job"`
	Secrets Secrets `json:"secrets"`
}

// Command is a later stdin line.
type Command struct {
	Cmd  string `json:"cmd"`            // stop | stats | bwlimit | forget
	Rate string `json:"rate,omitempty"` // bwlimit
	Dir  string `json:"dir,omitempty"`  // forget
}

// Msg is one stdout line of a worker.
type Msg struct {
	Type       string          `json:"type"` // ready | mounted | progress | stats | result
	MountPoint string          `json:"mountPoint,omitempty"`
	Bytes      int64           `json:"bytes,omitempty"`
	TotalBytes int64           `json:"totalBytes,omitempty"`
	Transfers  int64           `json:"transfers,omitempty"`
	ETA        *float64        `json:"eta,omitempty"`
	VFS        json.RawMessage `json:"vfs,omitempty"`
	Status     string          `json:"status,omitempty"`
	Error      string          `json:"error,omitempty"` // result: failure; mounted: warning
	Stats      json.RawMessage `json:"stats,omitempty"`
	Mismatches []string        `json:"mismatches,omitempty"`
	// Files are the verified source files of a Vault migration, relative to
	// the migrated folder (or the file name for a single file).
	Files []string `json:"files,omitempty"`
}

// LogLine is one rclone JSON log line from a worker's stderr.
type LogLine struct {
	Time       string `json:"time"`
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Object     string `json:"object"`
	ObjectType string `json:"objectType"`
	Source     string `json:"source"`
	Raw        string `json:"-"`
}
