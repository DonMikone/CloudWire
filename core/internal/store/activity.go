package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/msg"
)

// Activity is one Activity Log entry. Its text is a msg.Text; entries from
// before Migration 4 have no code.
type Activity struct {
	ID        int64  `json:"id"`
	TS        int64  `json:"ts"`
	Level     string `json:"level"`
	Category  string `json:"category"`
	SubjectID string `json:"subjectId"`
	msg.Text
	Details json.RawMessage `json:"details"`
}

// ActivityFilter selects Activity entries (newest first).
type ActivityFilter struct {
	Levels     []string `json:"levels"`
	Categories []string `json:"categories"`
	Search     string   `json:"search"`
	Before     int64    `json:"before"`
	Limit      int      `json:"limit"`
}

// AppendActivity writes an Activity entry. details may be nil.
func (s *Store) AppendActivity(level, category, subjectID string, t msg.Text, details any) (Activity, error) {
	a := Activity{TS: Now(), Level: level, Category: category, SubjectID: subjectID, Text: t}
	var det, params sql.NullString
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			return a, err
		}
		a.Details = b
		det = sql.NullString{String: string(b), Valid: true}
	}
	if len(t.Params) > 0 {
		b, err := json.Marshal(t.Params)
		if err != nil {
			return a, err
		}
		params = sql.NullString{String: string(b), Valid: true}
	}
	res, err := s.db.Exec(`INSERT INTO activity(ts,level,category,subject_id,message,details,code,params) VALUES (?,?,?,?,?,?,?,?)`,
		a.TS, a.Level, a.Category, nullString(subjectID), t.Message, det, nullString(t.Code), params)
	if err != nil {
		return a, err
	}
	a.ID, _ = res.LastInsertId()
	return a, nil
}

// QueryActivity returns matching entries, newest first.
func (s *Store) QueryActivity(f ActivityFilter) ([]Activity, error) {
	var where []string
	var args []any
	if len(f.Levels) > 0 {
		where = append(where, `level IN (`+placeholders(len(f.Levels))+`)`)
		for _, l := range f.Levels {
			args = append(args, l)
		}
	}
	if len(f.Categories) > 0 {
		where = append(where, `category IN (`+placeholders(len(f.Categories))+`)`)
		for _, c := range f.Categories {
			args = append(args, c)
		}
	}
	if f.Search != "" {
		where = append(where, `(message LIKE ? ESCAPE '\' OR details LIKE ? ESCAPE '\')`)
		pat := "%" + escapeLike(f.Search) + "%"
		args = append(args, pat, pat)
	}
	if f.Before > 0 {
		where = append(where, `ts < ?`)
		args = append(args, f.Before)
	}
	q := `SELECT id,ts,level,category,subject_id,message,details,code,params FROM activity`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		var subj, det, code, params sql.NullString
		if err := rows.Scan(&a.ID, &a.TS, &a.Level, &a.Category, &subj, &a.Message, &det, &code, &params); err != nil {
			return nil, err
		}
		a.SubjectID, a.Code = subj.String, code.String
		if det.Valid {
			a.Details = json.RawMessage(det.String)
		}
		if params.Valid {
			_ = json.Unmarshal([]byte(params.String), &a.Params)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ---- Sync runs ----

// SyncRun is one bisync or Vault migration run.
type SyncRun struct {
	ID          int64  `json:"id"`
	ItemID      string `json:"itemId"`
	Kind        string `json:"kind"`
	StartedAt   int64  `json:"startedAt"`
	FinishedAt  *int64 `json:"finishedAt"`
	Status      string `json:"status"`
	Transferred int    `json:"transferred"`
	Deleted     int    `json:"deleted"`
	Conflicts   int    `json:"conflicts"`
	Bytes       int64  `json:"bytes"`
	Error       string `json:"error"`
}

// SyncRunFile is one file touched by a run.
type SyncRunFile struct {
	Action string `json:"action"`
	Path   string `json:"path"`
}

// StartRun records the start of a run.
func (s *Store) StartRun(itemID, kind string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO sync_runs(item_id,kind,started_at) VALUES (?,?,?)`, itemID, kind, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun stores the outcome and file list of a run.
func (s *Store) FinishRun(r SyncRun, files []SyncRunFile) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sync_runs SET finished_at=?,status=?,transferred=?,deleted=?,conflicts=?,bytes=?,error=? WHERE id=?`,
		Now(), r.Status, r.Transferred, r.Deleted, r.Conflicts, r.Bytes, nullString(r.Error), r.ID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if len(files) > 0 {
		stmt, err := tx.Prepare(`INSERT INTO sync_run_files(run_id,action,path) VALUES (?,?,?)`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, f := range files {
			if _, err := stmt.Exec(r.ID, f.Action, f.Path); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		_ = stmt.Close()
	}
	return tx.Commit()
}

// Runs lists the latest runs of an item.
func (s *Store) Runs(itemID string, limit int) ([]SyncRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,item_id,kind,started_at,finished_at,coalesce(status,''),transferred,deleted,conflicts,bytes,coalesce(error,'')
FROM sync_runs WHERE item_id=? ORDER BY started_at DESC, id DESC LIMIT ?`, itemID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncRun{}
	for rows.Next() {
		var r SyncRun
		var fin sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ItemID, &r.Kind, &r.StartedAt, &fin, &r.Status, &r.Transferred, &r.Deleted,
			&r.Conflicts, &r.Bytes, &r.Error); err != nil {
			return nil, err
		}
		r.FinishedAt = ptrInt(fin)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunFiles lists the files of a run.
func (s *Store) RunFiles(runID int64) ([]SyncRunFile, error) {
	rows, err := s.db.Query(`SELECT action, path FROM sync_run_files WHERE run_id=? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncRunFile{}
	for rows.Next() {
		var f SyncRunFile
		if err := rows.Scan(&f.Action, &f.Path); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- Notifications ----

// Notification is queued for delivery by the App.
type Notification struct {
	ID     int64           `json:"id"`
	TS     int64           `json:"ts"`
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params"`
}

// InsertNotification queues a notification.
func (s *Store) InsertNotification(kind string, params any) (Notification, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return Notification{}, err
	}
	n := Notification{TS: Now(), Kind: kind, Params: b}
	res, err := s.db.Exec(`INSERT INTO notifications(ts,kind,params) VALUES (?,?,?)`, n.TS, kind, string(b))
	if err != nil {
		return n, err
	}
	n.ID, _ = res.LastInsertId()
	return n, nil
}

// PendingNotifications lists undelivered notifications, oldest first.
func (s *Store) PendingNotifications() ([]Notification, error) {
	rows, err := s.db.Query(`SELECT id,ts,kind,params FROM notifications WHERE delivered=0 ORDER BY id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var p string
		if err := rows.Scan(&n.ID, &n.TS, &n.Kind, &p); err != nil {
			return nil, err
		}
		n.Params = json.RawMessage(p)
		out = append(out, n)
	}
	return out, rows.Err()
}

// AckNotifications marks notifications delivered.
func (s *Store) AckNotifications(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := s.db.Exec(`UPDATE notifications SET delivered=1 WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// ---- Retention ----

// Size returns the bytes used by live pages.
func (s *Store) Size() (int64, error) {
	var pages, free, size int64
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		return 0, err
	}
	return (pages - free) * size, nil
}

// Prune deletes entries older than retentionDays, then the oldest 10 % of
// activity and sync runs while the database exceeds maxMB, then vacuums.
func (s *Store) Prune(retentionDays, maxMB int) error {
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM activity WHERE ts < ?`, cutoff); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM sync_runs WHERE started_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM notifications WHERE delivered=1 AND ts < ?`, cutoff); err != nil {
		return err
	}
	limit := int64(maxMB) << 20
	for range 50 {
		size, err := s.Size()
		if err != nil {
			return err
		}
		if size <= limit {
			break
		}
		var nAct, nRuns int64
		_ = s.db.QueryRow(`SELECT count(*) FROM activity`).Scan(&nAct)
		_ = s.db.QueryRow(`SELECT count(*) FROM sync_runs`).Scan(&nRuns)
		if nAct == 0 && nRuns == 0 {
			break
		}
		if _, err := s.db.Exec(`DELETE FROM activity WHERE id IN (SELECT id FROM activity ORDER BY ts, id LIMIT ?)`, max(nAct/10, 1)); err != nil {
			return err
		}
		if _, err := s.db.Exec(`DELETE FROM sync_runs WHERE id IN (SELECT id FROM sync_runs ORDER BY started_at, id LIMIT ?)`, max(nRuns/10, 1)); err != nil {
			return err
		}
	}
	// incremental_vacuum frees one page per step; run it to completion.
	rows, err := s.db.Query(`PRAGMA incremental_vacuum`)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}
