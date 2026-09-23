// Package store is CloudWire's SQLite persistence: settings, Connections,
// Mounts, Offline Items, Vaults, the link registry and the Activity Log.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the CloudWire database.
type Store struct {
	db *sql.DB
}

var migrations = []string{
	// Migration 1 (plan Appendix D).
	`CREATE TABLE schema_version(version INTEGER NOT NULL);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE connections(id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kind TEXT NOT NULL CHECK(kind IN ('remote','vault')), rclone_remote TEXT NOT NULL UNIQUE,
  provider TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE mounts(id TEXT PRIMARY KEY, connection_id TEXT NOT NULL REFERENCES connections(id),
  remote_path TEXT NOT NULL, mount_point TEXT NOT NULL UNIQUE, volume_name TEXT NOT NULL,
  mount_type TEXT NOT NULL CHECK(mount_type IN ('nfsmount','cmount')), auto_mount INTEGER NOT NULL DEFAULT 1,
  read_only INTEGER NOT NULL DEFAULT 0, cache_max_gb INTEGER NOT NULL, options TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL);
CREATE TABLE offline_items(id TEXT PRIMARY KEY, connection_id TEXT NOT NULL REFERENCES connections(id),
  kind TEXT NOT NULL CHECK(kind IN ('folder','files')), remote_path TEXT NOT NULL, files TEXT NOT NULL DEFAULT '[]',
  storage_path TEXT NOT NULL, excludes TEXT NOT NULL, advanced TEXT NOT NULL DEFAULT '{}',
  needs_resync INTEGER NOT NULL DEFAULT 1, remote_etag TEXT, last_sync_at INTEGER, last_error TEXT,
  state TEXT NOT NULL DEFAULT 'pending', created_at INTEGER NOT NULL);
CREATE TABLE link_registry(id TEXT PRIMARY KEY, connection_id TEXT NOT NULL REFERENCES connections(id),
  remote_path TEXT NOT NULL, url TEXT NOT NULL, expires_at INTEGER, created_at INTEGER NOT NULL);
CREATE TABLE vaults(id TEXT PRIMARY KEY, connection_id TEXT NOT NULL REFERENCES connections(id),
  vault_path TEXT NOT NULL, name TEXT NOT NULL, vault_connection_id TEXT NOT NULL REFERENCES connections(id),
  unlock_mode TEXT NOT NULL CHECK(unlock_mode IN ('keychain','ask')), created_at INTEGER NOT NULL,
  UNIQUE(connection_id, vault_path));
CREATE TABLE activity(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, level TEXT NOT NULL,
  category TEXT NOT NULL, subject_id TEXT, message TEXT NOT NULL, details TEXT);
CREATE INDEX activity_ts ON activity(ts);
CREATE TABLE sync_runs(id INTEGER PRIMARY KEY AUTOINCREMENT, item_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('bisync','vault-migrate')), started_at INTEGER NOT NULL, finished_at INTEGER,
  status TEXT, transferred INTEGER NOT NULL DEFAULT 0, deleted INTEGER NOT NULL DEFAULT 0,
  conflicts INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0, error TEXT);
CREATE INDEX sync_runs_item ON sync_runs(item_id, started_at);
CREATE TABLE sync_run_files(run_id INTEGER NOT NULL REFERENCES sync_runs(id) ON DELETE CASCADE,
  action TEXT NOT NULL CHECK(action IN ('transferred','deleted','conflict')), path TEXT NOT NULL);
CREATE TABLE notifications(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, kind TEXT NOT NULL,
  params TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0);`,
	// Migration 2: cascade deletes and RunFiles look up files by run.
	`CREATE INDEX sync_run_files_run ON sync_run_files(run_id);`,
}

// Open opens (and if needed creates and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_foreign_keys=on&_busy_timeout=10000&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// Single writer: one connection serialises every statement.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	ctx := context.Background()
	var tables int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if tables == 0 {
		// auto_vacuum must be chosen before the first table is created.
		if _, err := s.db.ExecContext(ctx, `PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return err
	}
	return s.migrate(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	version := 0
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(version),0) FROM schema_version`).Scan(&version); err != nil {
			return err
		}
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this CloudWire (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the applied migration version.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT coalesce(max(version),0) FROM schema_version`).Scan(&v)
	return v, err
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns a random 12-character lowercase base32 identifier.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return strings.ToLower(idEncoding.EncodeToString(b[:]))[:12]
}

// Now returns the current time in unix milliseconds.
func Now() int64 { return time.Now().UnixMilli() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func ptrInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// IsUniqueViolation reports whether err is a SQLite UNIQUE constraint error.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
