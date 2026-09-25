package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Connection is one rclone remote (or a Vault's crypt pseudo-remote).
type Connection struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	RcloneRemote string `json:"rcloneRemote"`
	Provider     string `json:"provider"`
	CreatedAt    int64  `json:"createdAt"`
}

// Mount is a Connection path attached as a Finder volume.
type Mount struct {
	ID           string            `json:"id"`
	ConnectionID string            `json:"connectionId"`
	RemotePath   string            `json:"remotePath"`
	MountPoint   string            `json:"mountPoint"`
	VolumeName   string            `json:"volumeName"`
	MountType    string            `json:"mountType"`
	AutoMount    bool              `json:"autoMount"`
	ReadOnly     bool              `json:"readOnly"`
	CacheMaxGB   int               `json:"cacheMaxGB"`
	Options      map[string]string `json:"options"`
	CreatedAt    int64             `json:"createdAt"`
}

// OfflineItem is a folder or file set kept as real local files.
type OfflineItem struct {
	ID           string         `json:"id"`
	ConnectionID string         `json:"connectionId"`
	Kind         string         `json:"kind"`
	RemotePath   string         `json:"remotePath"`
	Files        []string       `json:"files"` // kind files: selected paths relative to RemotePath (files or folders, may be nested)
	StoragePath  string         `json:"storagePath"`
	Excludes     []string       `json:"excludes"`
	Advanced     map[string]any `json:"advanced"`
	NeedsResync  bool           `json:"needsResync"`
	RemoteETag   string         `json:"-"`
	LastSyncAt   *int64         `json:"lastSyncAt"`
	LastError    string         `json:"reason"`
	State        string         `json:"state"`
	CreatedAt    int64          `json:"createdAt"`
}

// LinkEntry is a public link created through rclone (link registry).
type LinkEntry struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connectionId"`
	RemotePath   string `json:"path"`
	URL          string `json:"url"`
	ExpiresAt    *int64 `json:"expiresAt"`
	CreatedAt    int64  `json:"createdAt"`
}

// Vault is an encrypted folder backed by an rclone crypt remote.
type Vault struct {
	ID                string `json:"id"`
	ConnectionID      string `json:"connectionId"`
	VaultPath         string `json:"vaultPath"`
	Name              string `json:"name"`
	VaultConnectionID string `json:"vaultConnectionId"`
	UnlockMode        string `json:"unlockMode"`
	CreatedAt         int64  `json:"createdAt"`
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---- Connections ----

// InsertConnection stores a new Connection.
func (s *Store) InsertConnection(c Connection) error {
	_, err := s.db.Exec(`INSERT INTO connections(id,name,kind,rclone_remote,provider,created_at) VALUES (?,?,?,?,?,?)`,
		c.ID, c.Name, c.Kind, c.RcloneRemote, c.Provider, c.CreatedAt)
	return err
}

// RenameConnection changes a Connection's display name.
func (s *Store) RenameConnection(id, name string) error {
	_, err := s.db.Exec(`UPDATE connections SET name=? WHERE id=?`, name, id)
	return err
}

// DeleteConnection removes a Connection row.
func (s *Store) DeleteConnection(id string) error {
	_, err := s.db.Exec(`DELETE FROM connections WHERE id=?`, id)
	return err
}

const connCols = `id,name,kind,rclone_remote,provider,created_at`

func scanConnection(r interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	err := r.Scan(&c.ID, &c.Name, &c.Kind, &c.RcloneRemote, &c.Provider, &c.CreatedAt)
	return c, notFound(err)
}

// Connection returns one Connection.
func (s *Store) Connection(id string) (Connection, error) {
	return scanConnection(s.db.QueryRow(`SELECT `+connCols+` FROM connections WHERE id=?`, id))
}

// ConnectionByName finds a Connection by case-insensitive name.
func (s *Store) ConnectionByName(name string) (Connection, error) {
	return scanConnection(s.db.QueryRow(`SELECT `+connCols+` FROM connections WHERE name=?`, name))
}

// Connections lists Connections of the given kind ("" = all).
func (s *Store) Connections(kind string) ([]Connection, error) {
	rows, err := s.db.Query(`SELECT `+connCols+` FROM connections WHERE ?='' OR kind=? ORDER BY name COLLATE NOCASE`, kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- Mounts ----

const mountCols = `id,connection_id,remote_path,mount_point,volume_name,mount_type,auto_mount,read_only,cache_max_gb,options,created_at`

func scanMount(r interface{ Scan(...any) error }) (Mount, error) {
	var m Mount
	var opts string
	err := r.Scan(&m.ID, &m.ConnectionID, &m.RemotePath, &m.MountPoint, &m.VolumeName, &m.MountType, &m.AutoMount,
		&m.ReadOnly, &m.CacheMaxGB, &opts, &m.CreatedAt)
	if err != nil {
		return m, notFound(err)
	}
	m.Options = map[string]string{}
	_ = json.Unmarshal([]byte(opts), &m.Options)
	return m, nil
}

// InsertMount stores a new Mount.
func (s *Store) InsertMount(m Mount) error {
	if m.Options == nil {
		m.Options = map[string]string{}
	}
	_, err := s.db.Exec(`INSERT INTO mounts(`+mountCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ConnectionID, m.RemotePath, m.MountPoint, m.VolumeName, m.MountType, boolInt(m.AutoMount),
		boolInt(m.ReadOnly), m.CacheMaxGB, mustJSON(m.Options), m.CreatedAt)
	return err
}

// UpdateMount rewrites a Mount.
func (s *Store) UpdateMount(m Mount) error {
	if m.Options == nil {
		m.Options = map[string]string{}
	}
	_, err := s.db.Exec(`UPDATE mounts SET remote_path=?,mount_point=?,volume_name=?,mount_type=?,auto_mount=?,read_only=?,
cache_max_gb=?,options=? WHERE id=?`, m.RemotePath, m.MountPoint, m.VolumeName, m.MountType, boolInt(m.AutoMount),
		boolInt(m.ReadOnly), m.CacheMaxGB, mustJSON(m.Options), m.ID)
	return err
}

// DeleteMount removes a Mount.
func (s *Store) DeleteMount(id string) error {
	_, err := s.db.Exec(`DELETE FROM mounts WHERE id=?`, id)
	return err
}

// Mount returns one Mount.
func (s *Store) Mount(id string) (Mount, error) {
	return scanMount(s.db.QueryRow(`SELECT `+mountCols+` FROM mounts WHERE id=?`, id))
}

// Mounts lists all Mounts.
func (s *Store) Mounts() ([]Mount, error) {
	rows, err := s.db.Query(`SELECT ` + mountCols + ` FROM mounts ORDER BY volume_name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mount{}
	for rows.Next() {
		m, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- Offline Items ----

const offlineCols = `id,connection_id,kind,remote_path,files,storage_path,excludes,advanced,needs_resync,remote_etag,
last_sync_at,last_error,state,created_at`

func scanOffline(r interface{ Scan(...any) error }) (OfflineItem, error) {
	var it OfflineItem
	var files, excludes, advanced string
	var etag, lastErr sql.NullString
	var last sql.NullInt64
	err := r.Scan(&it.ID, &it.ConnectionID, &it.Kind, &it.RemotePath, &files, &it.StoragePath, &excludes, &advanced,
		&it.NeedsResync, &etag, &last, &lastErr, &it.State, &it.CreatedAt)
	if err != nil {
		return it, notFound(err)
	}
	it.Files, it.Excludes, it.Advanced = []string{}, []string{}, map[string]any{}
	_ = json.Unmarshal([]byte(files), &it.Files)
	_ = json.Unmarshal([]byte(excludes), &it.Excludes)
	_ = json.Unmarshal([]byte(advanced), &it.Advanced)
	it.RemoteETag, it.LastError, it.LastSyncAt = etag.String, lastErr.String, ptrInt(last)
	return it, nil
}

// InsertOfflineItem stores a new Offline Item.
func (s *Store) InsertOfflineItem(it OfflineItem) error {
	_, err := s.db.Exec(`INSERT INTO offline_items(`+offlineCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.ID, it.ConnectionID, it.Kind, it.RemotePath, mustJSON(nonNilStrings(it.Files)), it.StoragePath,
		mustJSON(nonNilStrings(it.Excludes)), mustJSON(nonNilMap(it.Advanced)), boolInt(it.NeedsResync),
		nullString(it.RemoteETag), nullInt(it.LastSyncAt), nullString(it.LastError), it.State, it.CreatedAt)
	return err
}

// UpdateOfflineItem rewrites an Offline Item (except its remote ETag).
func (s *Store) UpdateOfflineItem(it OfflineItem) error {
	_, err := s.db.Exec(`UPDATE offline_items SET kind=?,files=?,storage_path=?,excludes=?,advanced=?,needs_resync=?,
last_sync_at=?,last_error=?,state=? WHERE id=?`, it.Kind, mustJSON(nonNilStrings(it.Files)), it.StoragePath,
		mustJSON(nonNilStrings(it.Excludes)), mustJSON(nonNilMap(it.Advanced)), boolInt(it.NeedsResync),
		nullInt(it.LastSyncAt), nullString(it.LastError), it.State, it.ID)
	return err
}

// SetOfflineETag stores the remote ETag an item was last synced against.
// It is written separately so concurrent item updates never roll it back.
func (s *Store) SetOfflineETag(id, etag string) error {
	_, err := s.db.Exec(`UPDATE offline_items SET remote_etag=? WHERE id=?`, nullString(etag), id)
	return err
}

// DeleteOfflineItem removes an Offline Item.
func (s *Store) DeleteOfflineItem(id string) error {
	_, err := s.db.Exec(`DELETE FROM offline_items WHERE id=?`, id)
	return err
}

// OfflineItem returns one Offline Item.
func (s *Store) OfflineItem(id string) (OfflineItem, error) {
	return scanOffline(s.db.QueryRow(`SELECT `+offlineCols+` FROM offline_items WHERE id=?`, id))
}

// OfflineItems lists all Offline Items.
func (s *Store) OfflineItems() ([]OfflineItem, error) {
	rows, err := s.db.Query(`SELECT ` + offlineCols + ` FROM offline_items ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OfflineItem{}
	for rows.Next() {
		it, err := scanOffline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// ---- Link registry ----

// InsertLink records an rclone public link.
func (s *Store) InsertLink(l LinkEntry) error {
	_, err := s.db.Exec(`INSERT INTO link_registry(id,connection_id,remote_path,url,expires_at,created_at) VALUES (?,?,?,?,?,?)`,
		l.ID, l.ConnectionID, l.RemotePath, l.URL, nullInt(l.ExpiresAt), l.CreatedAt)
	return err
}

// DeleteLink removes a registry row.
func (s *Store) DeleteLink(id string) error {
	_, err := s.db.Exec(`DELETE FROM link_registry WHERE id=?`, id)
	return err
}

// DeleteLinksForConnection removes all registry rows of a Connection.
func (s *Store) DeleteLinksForConnection(connID string) error {
	_, err := s.db.Exec(`DELETE FROM link_registry WHERE connection_id=?`, connID)
	return err
}

// Links lists registry rows of a Connection, optionally for one path.
func (s *Store) Links(connID string, path *string) ([]LinkEntry, error) {
	q := `SELECT id,connection_id,remote_path,url,expires_at,created_at FROM link_registry WHERE connection_id=?`
	args := []any{connID}
	if path != nil {
		q += ` AND remote_path=?`
		args = append(args, *path)
	}
	rows, err := s.db.Query(q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkEntry{}
	for rows.Next() {
		var l LinkEntry
		var exp sql.NullInt64
		if err := rows.Scan(&l.ID, &l.ConnectionID, &l.RemotePath, &l.URL, &exp, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.ExpiresAt = ptrInt(exp)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- Vaults ----

const vaultCols = `id,connection_id,vault_path,name,vault_connection_id,unlock_mode,created_at`

func scanVault(r interface{ Scan(...any) error }) (Vault, error) {
	var v Vault
	err := r.Scan(&v.ID, &v.ConnectionID, &v.VaultPath, &v.Name, &v.VaultConnectionID, &v.UnlockMode, &v.CreatedAt)
	return v, notFound(err)
}

// InsertVault stores a Vault together with its pseudo-Connection.
func (s *Store) InsertVault(v Vault, conn Connection) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO connections(id,name,kind,rclone_remote,provider,created_at) VALUES (?,?,?,?,?,?)`,
		conn.ID, conn.Name, conn.Kind, conn.RcloneRemote, conn.Provider, conn.CreatedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO vaults(`+vaultCols+`) VALUES (?,?,?,?,?,?,?)`, v.ID, v.ConnectionID, v.VaultPath,
		v.Name, v.VaultConnectionID, v.UnlockMode, v.CreatedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// UpdateVaultUnlockMode changes how a Vault unlocks.
func (s *Store) UpdateVaultUnlockMode(id, mode string) error {
	_, err := s.db.Exec(`UPDATE vaults SET unlock_mode=? WHERE id=?`, mode, id)
	return err
}

// DeleteVault removes a Vault and its pseudo-Connection.
func (s *Store) DeleteVault(v Vault) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM vaults WHERE id=?`, v.ID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM link_registry WHERE connection_id=?`, v.VaultConnectionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM connections WHERE id=?`, v.VaultConnectionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Vault returns one Vault.
func (s *Store) Vault(id string) (Vault, error) {
	return scanVault(s.db.QueryRow(`SELECT `+vaultCols+` FROM vaults WHERE id=?`, id))
}

// VaultByConnection finds the Vault whose pseudo-Connection is connID.
func (s *Store) VaultByConnection(connID string) (Vault, error) {
	return scanVault(s.db.QueryRow(`SELECT `+vaultCols+` FROM vaults WHERE vault_connection_id=?`, connID))
}

// Vaults lists all Vaults.
func (s *Store) Vaults() ([]Vault, error) {
	rows, err := s.db.Query(`SELECT ` + vaultCols + ` FROM vaults ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Vault{}
	for rows.Next() {
		v, err := scanVault(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
