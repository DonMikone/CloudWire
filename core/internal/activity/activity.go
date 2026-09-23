// Package activity writes the Activity Log (SQLite) and mirrors warnings and
// errors to the Core's stderr log.
package activity

import (
	"log/slog"
	"sync/atomic"

	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// Publisher pushes events to subscribed clients.
type Publisher interface {
	Publish(eventType string, data any)
}

var levels = map[string]int32{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Logger appends Activity Log entries at or above the configured level.
type Logger struct {
	st    *store.Store
	pub   Publisher
	level atomic.Int32
}

// New creates a Logger at level "info".
func New(st *store.Store, pub Publisher) *Logger {
	l := &Logger{st: st, pub: pub}
	l.level.Store(levels["info"])
	return l
}

// SetLevel changes the minimum level ("debug", "info", "warn", "error").
func (l *Logger) SetLevel(level string) {
	if v, ok := levels[level]; ok {
		l.level.Store(v)
	}
}

// Enabled reports whether level would be recorded.
func (l *Logger) Enabled(level string) bool {
	return levels[level] >= l.level.Load()
}

// Log records an entry. details may be nil.
func (l *Logger) Log(level, category, subjectID string, t msg.Text, details any) {
	switch level {
	case "error":
		slog.Error(t.Message, "category", category, "subject", subjectID)
	case "warn":
		slog.Warn(t.Message, "category", category, "subject", subjectID)
	}
	if !l.Enabled(level) {
		return
	}
	a, err := l.st.AppendActivity(level, category, subjectID, t, details)
	if err != nil {
		slog.Error("activity append", "err", err)
		return
	}
	if l.pub != nil {
		l.pub.Publish("activity.appended", a)
	}
}

// Debug records a debug entry.
func (l *Logger) Debug(category, subjectID string, t msg.Text, details any) {
	l.Log("debug", category, subjectID, t, details)
}

// Info records an info entry.
func (l *Logger) Info(category, subjectID string, t msg.Text, details any) {
	l.Log("info", category, subjectID, t, details)
}

// Warn records a warning.
func (l *Logger) Warn(category, subjectID string, t msg.Text, details any) {
	l.Log("warn", category, subjectID, t, details)
}

// Error records an error.
func (l *Logger) Error(category, subjectID string, t msg.Text, details any) {
	l.Log("error", category, subjectID, t, details)
}
