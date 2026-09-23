// Package daemon is the Core supervisor ("cloudwire-core serve"): it owns the
// database, the rclone config, the API socket and all services.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/buildinfo"
	"github.com/DonMikone/CloudWire/core/internal/connections"
	"github.com/DonMikone/CloudWire/core/internal/keychain"
	"github.com/DonMikone/CloudWire/core/internal/mounts"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/notify"
	"github.com/DonMikone/CloudWire/core/internal/offline"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/pauserules"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/sharing"
	"github.com/DonMikone/CloudWire/core/internal/store"
	"github.com/DonMikone/CloudWire/core/internal/updates"
	"github.com/DonMikone/CloudWire/core/internal/vault"
)

// ConfigKeyAccount is the Keychain account of the rclone config key.
const ConfigKeyAccount = "rclone-config-key"

// Options control serve.
type Options struct {
	// Force skips the autostart gate (E2E tests, manual runs).
	Force bool
}

// Core holds the running services.
type Core struct {
	paths     paths.Paths
	st        *store.Store
	srv       *api.Server
	log       *activity.Logger
	notify    *notify.Notifier
	conns     *connections.Service
	mounts    *mounts.Service
	offline   *offline.Engine
	shares    *sharing.Service
	vaults    *vault.Service
	updates   *updates.Checker
	token     string
	startedAt int64
	shutdown  chan struct{}
	stopOnce  sync.Once

	capMu    sync.Mutex
	capCache map[string]sharing.Capabilities
}

// Serve runs the supervisor and returns the process exit code.
func Serve(opt Options) int {
	p := paths.Default()
	// Relative remote paths of local-disk Connections resolve from "/", as under launchd.
	_ = os.Chdir("/")
	if err := p.EnsureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "cloudwire-core:", err)
		return 1
	}
	redirectStderr(p.LogFile)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	lock, err := os.OpenFile(p.CoreLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		slog.Error("open lock", "err", err)
		return 1
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		slog.Info("another Core is running; exiting")
		return 0
	}

	st, err := store.Open(p.DB)
	if err != nil {
		slog.Error("open database", "err", err)
		return 1
	}
	settings, err := st.Settings()
	if err != nil {
		slog.Error("read settings", "err", err)
		_ = st.Close()
		return 1
	}
	if !opt.Force && !startAllowed(p, settings) {
		slog.Info("autostart is off and no start was requested; exiting")
		_ = st.Close()
		return 0
	}
	_ = os.WriteFile(p.ActiveFile, []byte(fmt.Sprint(os.Getpid())), 0o600)

	key, err := configKey(p)
	if err != nil {
		slog.Error("rclone config key", "err", err)
		_ = st.Close()
		return 1
	}
	if err := rcl.Init(p.RcloneConf, key, rcl.Options{CacheDir: p.CacheDir}); err != nil {
		slog.Error("rclone init", "err", err)
		_ = st.Close()
		return 1
	}
	c := newCore(p, st, key)
	c.firstStart()
	return c.run()
}

// redirectStderr points fd 2 at the log file, truncating it above 5 MB.
func redirectStderr(logFile string) {
	if fi, err := os.Stat(logFile); err == nil && fi.Size() > 5<<20 {
		_ = os.Truncate(logFile, 0)
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_ = unix.Dup2(int(f.Fd()), 2)
	_ = f.Close()
}

// startAllowed implements the autostart gate: autostart on, a fresh start
// request from the App, or a crash restart (run/active left behind).
func startAllowed(p paths.Paths, st store.Settings) bool {
	if st.Autostart {
		return true
	}
	if fi, err := os.Stat(p.StartRequest); err == nil && time.Since(fi.ModTime()) <= 120*time.Second {
		return true
	}
	_, err := os.Stat(p.ActiveFile)
	return err == nil
}

// configKey reads the rclone config key from the Keychain, creating it on
// first run.
func configKey(p paths.Paths) (string, error) {
	svc := paths.KeychainService()
	key, err := keychain.Get(svc, ConfigKeyAccount)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, keychain.ErrNotFound) {
		return "", err
	}
	if _, err := os.Stat(p.RcloneConf); err == nil {
		// The key is gone but an encrypted config exists: keep it aside and start fresh.
		aside := fmt.Sprintf("%s.unreadable-%d", p.RcloneConf, time.Now().Unix())
		slog.Error("rclone config key missing from the Keychain; moving the old config aside", "to", aside)
		_ = os.Rename(p.RcloneConf, aside)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key = base64.StdEncoding.EncodeToString(b)
	if err := keychain.Set(svc, ConfigKeyAccount, key); err != nil {
		return "", err
	}
	return key, nil
}

func newCore(p paths.Paths, st *store.Store, configPass string) *Core {
	tok := make([]byte, 24)
	_, _ = rand.Read(tok)
	srv := api.NewServer(slog.Default())
	c := &Core{paths: p, st: st, srv: srv, token: hex.EncodeToString(tok), startedAt: store.Now(),
		shutdown: make(chan struct{}), capCache: map[string]sharing.Capabilities{}}
	c.log = activity.New(st, srv)
	if s, err := st.Settings(); err == nil {
		c.log.SetLevel(s.Log.Level)
	}
	c.notify = notify.New(st, srv)
	if os.Getenv(paths.EnvHome) != "" {
		c.notify.Launch = func() error { return nil } // isolated test Core: never launch the real App
	}
	logLevel := func() string {
		s, _ := st.Settings()
		return s.Log.Level
	}
	c.conns = connections.New(st, c.log, srv, p)
	c.mounts = mounts.New(st, c.log, c.notify, srv, p, configPass, logLevel)
	c.conns.Mounts = c.mounts
	c.offline = offline.New(st, c.log, c.notify, srv, p, configPass, logLevel)
	c.offline.Conns = c.conns
	c.offline.Mounts = c.mounts
	c.offline.Eval = &pauserules.Evaluator{Probes: pauserules.System{}, Settings: func() store.Settings {
		s, err := st.Settings()
		if err != nil {
			return store.DefaultSettings()
		}
		return s
	}}
	c.offline.Watch = true
	c.shares = sharing.New(st, c.log, c.conns)
	c.vaults = vault.NewService(st, c.log, c.notify, srv, p)
	c.vaults.Mounts = c.mounts
	c.vaults.Offline = c.offline
	c.offline.Vaults = c.vaults
	c.updates = updates.New(st, srv, buildinfo.Version)
	return c
}

// firstStart fills the settings that depend on the machine: the conflict
// label from the system language and the Studio Mode apps.
func (c *Core) firstStart() {
	s, err := c.st.Settings()
	if err != nil || s.ConflictLabel != "" {
		return
	}
	label := "conflict"
	if out, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			l := strings.Trim(strings.TrimSpace(line), `",()`)
			if l == "" {
				continue
			}
			if strings.HasPrefix(l, "de") {
				label = "Konflikt"
			}
			break
		}
	}
	daws := pauserules.DetectDAWs("/Applications", filepath.Join(c.paths.Home, "Applications"))
	if daws == nil {
		daws = []string{}
	}
	if _, err := c.st.UpdateSettings(map[string]any{
		"conflictLabel": label,
		"pauseRules":    map[string]any{"studioMode": map[string]any{"apps": daws}},
	}); err != nil {
		slog.Error("first start settings", "err", err)
	}
	c.log.Info("core", "", msg.New("core.firstStart", "label", label, "count", len(daws)), daws)
}

func (c *Core) run() int {
	c.mounts.CleanupStale()
	c.conns.CleanupOrphans()
	c.vaults.Startup()
	c.registerMethods()
	if err := c.srv.ListenUnix(c.paths.Socket); err != nil {
		slog.Error("listen", "socket", c.paths.Socket, "err", err)
		return 1
	}
	slog.Info("CloudWire Core started", "version", buildinfo.Version, "pid", os.Getpid())
	c.log.Info("core", "", msg.New("core.started", "version", buildinfo.Version), nil)

	c.mounts.StartAuto()
	if err := c.offline.Start(); err != nil {
		slog.Error("offline engine", "err", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.updates.Run(ctx)
	go c.pruneLoop(ctx)
	c.watchSystem()
	debug.SetMemoryLimit(64 << 20)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	select {
	case s := <-sigs:
		slog.Info("signal received", "signal", s)
	case <-c.shutdown:
		// Let the core.shutdown response reach the client.
		time.Sleep(200 * time.Millisecond)
	}
	return c.stop()
}

// stop is the orderly shutdown.
func (c *Core) stop() int {
	slog.Info("shutting down")
	c.srv.Close()
	c.offline.Stop()
	c.mounts.StopAll()
	c.log.Info("core", "", msg.New("core.stopped"), nil)
	_ = c.st.Close()
	_ = os.Remove(c.paths.Socket)
	_ = os.Remove(c.paths.ActiveFile)
	return 0
}

func (c *Core) requestShutdown() {
	c.stopOnce.Do(func() { close(c.shutdown) })
}

func (c *Core) pruneLoop(ctx context.Context) {
	for {
		if s, err := c.st.Settings(); err == nil {
			if err := c.st.Prune(s.Log.RetentionDays, s.Log.MaxMB); err != nil {
				slog.Warn("prune", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// watchSystem reacts to network changes and sleep/wake.
func (c *Core) watchSystem() {
	var mu sync.Mutex
	known, satisfied := false, false
	platform.OnNetworkChange(func(st platform.NetworkState) {
		mu.Lock()
		regained := known && !satisfied && st.Satisfied
		changed := known && st.Satisfied != satisfied
		known, satisfied = true, st.Satisfied
		mu.Unlock()
		if regained || changed {
			code := "core.networkOffline"
			if st.Satisfied {
				code = "core.networkOnline"
			}
			c.log.Debug("core", "", msg.New(code), nil)
		}
		if regained {
			c.mounts.Retrigger()
			c.offline.Wake()
		}
	})
	if err := platform.OnPowerEvents(func() {
		c.log.Debug("core", "", msg.New("core.sleeping"), nil)
		c.offline.Sleep()
	}, func() {
		c.log.Debug("core", "", msg.New("core.woke"), nil)
		c.mounts.Retrigger()
		c.offline.Wake()
	}); err != nil {
		slog.Warn("power notifications", "err", err)
	}
}
