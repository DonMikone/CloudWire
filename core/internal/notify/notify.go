// Package notify queues user notifications. The App delivers them through
// UNUserNotificationCenter; when no App is connected the Core launches it
// in the background with --deliver-notifications.
package notify

import (
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/store"
)

// Kinds.
const (
	KindError      = "error"
	KindConflict   = "conflict"
	KindMassDelete = "massDelete"
)

// AppBundleID is launched to deliver notifications.
const AppBundleID = "io.github.donmikone.cloudwire"

// Publisher pushes events.
type Publisher interface {
	Publish(eventType string, data any)
	HasClient(client string) bool
}

// Notifier queues notifications.
type Notifier struct {
	st       *store.Store
	pub      Publisher
	mu       sync.Mutex
	lastOpen time.Time
	deferred bool // a launch is scheduled for when the rate limit ends
	// Launch starts the App to deliver notifications (replaceable in tests).
	Launch func() error
	// Now is the clock (replaceable in tests).
	Now func() time.Time
}

// New creates a Notifier.
func New(st *store.Store, pub Publisher) *Notifier {
	return &Notifier{st: st, pub: pub, Now: time.Now, Launch: func() error {
		return exec.Command("/usr/bin/open", "-g", "-j", "-b", AppBundleID, "--args", "--deliver-notifications").Run()
	}}
}

func enabled(s store.Settings, kind string) bool {
	switch kind {
	case KindError:
		return s.Notifications.Errors
	case KindConflict:
		return s.Notifications.Conflicts
	case KindMassDelete:
		return s.Notifications.MassDelete
	}
	return false
}

// Notify queues a notification of kind if enabled in the settings.
func (n *Notifier) Notify(kind string, params any) {
	s, err := n.st.Settings()
	if err != nil || !enabled(s, kind) {
		return
	}
	rec, err := n.st.InsertNotification(kind, params)
	if err != nil {
		slog.Error("queue notification", "err", err)
		return
	}
	n.pub.Publish("notification.new", rec)
	if n.pub.HasClient("app") {
		return
	}
	n.launchApp()
}

// launchApp starts the App at most once per minute; a request inside that
// minute is deferred so no notification waits for the next event.
func (n *Notifier) launchApp() {
	n.mu.Lock()
	now := n.Now()
	if wait := 60*time.Second - now.Sub(n.lastOpen); wait > 0 {
		if !n.deferred {
			n.deferred = true
			time.AfterFunc(wait, func() {
				n.mu.Lock()
				n.deferred = false
				n.mu.Unlock()
				if pending, err := n.st.PendingNotifications(); err == nil && len(pending) > 0 && !n.pub.HasClient("app") {
					n.launchApp()
				}
			})
		}
		n.mu.Unlock()
		return
	}
	n.lastOpen = now
	n.mu.Unlock()
	go func() {
		if err := n.Launch(); err != nil {
			slog.Warn("launch app for notifications", "err", err)
		}
	}()
}
