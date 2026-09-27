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
	// StatusCollision: a rename could not be applied because its target
	// exists on the other side; Error is the colliding path (item-relative).
	StatusCollision = "collision"
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
	// Identity makes a bisync job detect and apply renames before the sync
	// (package identity): the worker reports them in a "renames" message and
	// waits for "continue".
	Identity *IdentityJob `json:"identity,omitempty"`
}

// IdentityJob describes an Offline Item for rename detection.
type IdentityJob struct {
	StoragePath string   `json:"storagePath"`
	Remote      string   `json:"remote"`     // rclone remote name
	RemotePath  string   `json:"remotePath"` // item root below the Connection root
	RemoteFs    string   `json:"remoteFs"`   // "<remote>:<RemotePath>"
	ConnFs      string   `json:"connFs"`     // "<remote>:"
	Files       []string `json:"files"`      // Selection; [""] = whole root
	Excludes    []string `json:"excludes"`
	OtherRoots  []string `json:"otherRoots"` // Storage Locations of the other items
	Snapshot    string   `json:"snapshot"`   // identity snapshot file
	Nextcloud   bool     `json:"nextcloud"`  // file ids through WebDAV (oc:fileid)
	// ConflictMarker marks Conflict Copy names; renames to them are bisync's own.
	ConflictMarker string `json:"conflictMarker"`
	// RootRename renames the cloud root of an item created before 0.3.0
	// whose Storage Location was renamed.
	RootRename *RootRename `json:"rootRename,omitempty"`
}

// RootRename moves an item's cloud root From -> To (Connection-relative);
// the Storage Location already is at NewStoragePath.
type RootRename struct {
	From           string `json:"from"`
	To             string `json:"to"`
	NewStoragePath string `json:"newStoragePath"`
}

// Rename results.
const (
	RenameMoved    = "moved"    // both sides now have To
	RenameUpload   = "upload"   // only local To exists (the cloud deleted From); bisync uploads it
	RenameDownload = "download" // only cloud To exists (local From was deleted); bisync downloads it
	RenameRoot     = "root"     // the item's cloud root moved
)

// Rename is one rename a worker detected (paths item-relative, except for
// RenameRoot, where they are the Connection-relative cloud roots).
type Rename struct {
	From           string `json:"from"`
	To             string `json:"to"`
	Dir            bool   `json:"dir"`
	Result         string `json:"result"`
	NewStoragePath string `json:"newStoragePath,omitempty"` // RenameRoot only
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
	Cmd  string `json:"cmd"`            // stop | stats | bwlimit | forget | continue
	Rate string `json:"rate,omitempty"` // bwlimit
	Dir  string `json:"dir,omitempty"`  // forget
	// Bisync and Identity replace the job's sync/bisync params and item
	// description once renames changed them (continue, optional).
	Bisync   json.RawMessage `json:"bisync,omitempty"`
	Identity *IdentityJob    `json:"identity,omitempty"`
}

// Msg is one stdout line of a worker.
type Msg struct {
	Type       string          `json:"type"` // ready | mounted | progress | stats | renames | result
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
	// Renames are the renames applied before a bisync run (type "renames").
	Renames []Rename `json:"renames,omitempty"`
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
