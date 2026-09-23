// Package connections manages Connections: rclone remotes named cw-<id>,
// provider metadata, the rclone config state machine and Nextcloud login.
package connections

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// Publisher pushes events.
type Publisher interface {
	Publish(eventType string, data any)
}

// MountRestarter restarts the Mounts of a Connection after its config changed.
type MountRestarter interface {
	RestartForConnection(connID string)
}

// Service implements the connections.* methods.
type Service struct {
	st    *store.Store
	log   *activity.Logger
	pub   Publisher
	paths paths.Paths

	Mounts MountRestarter

	mu      sync.Mutex
	pending map[string]pendingConn // id -> config in progress
	flows   map[string]context.CancelFunc

	secretKeysOnce sync.Once
	secretKeys     map[string]map[string]bool // provider -> secret option names
}

type pendingConn struct {
	name, provider string
	started        time.Time
}

// New creates the service.
func New(st *store.Store, log *activity.Logger, pub Publisher, p paths.Paths) *Service {
	return &Service{st: st, log: log, pub: pub, paths: p, pending: map[string]pendingConn{}, flows: map[string]context.CancelFunc{}}
}

// RemoteName is the rclone remote name of a Connection id.
func RemoteName(id string) string { return "cw-" + id }

// DTO is the contract's Connection object.
type DTO struct {
	store.Connection
	Vendor     string            `json:"vendor"`
	ServerURL  string            `json:"serverURL"`
	User       string            `json:"user"`
	Parameters map[string]string `json:"parameters"`
}

// Config returns the raw rclone config section of a Connection (secrets obscured as stored).
func (s *Service) Config(c store.Connection) (map[string]string, error) {
	var out map[string]string
	err := rcl.CallInto("config/get", map[string]any{"name": c.RcloneRemote}, &out)
	return out, err
}

func (s *Service) loadSecretKeys() {
	s.secretKeys = map[string]map[string]bool{}
	var res struct {
		Providers []struct {
			Name    string `json:"Name"`
			Options []struct {
				Name       string `json:"Name"`
				IsPassword bool   `json:"IsPassword"`
				Sensitive  bool   `json:"Sensitive"`
			} `json:"Options"`
		} `json:"providers"`
	}
	if err := rcl.CallInto("config/providers", nil, &res); err != nil {
		return
	}
	for _, p := range res.Providers {
		m := map[string]bool{}
		for _, o := range p.Options {
			if o.IsPassword || o.Sensitive {
				m[o.Name] = true
			}
		}
		s.secretKeys[p.Name] = m
	}
}

func (s *Service) isSecret(provider, key string) bool {
	s.secretKeysOnce.Do(s.loadSecretKeys)
	switch key {
	case "token", "pass", "password", "password2", "client_secret":
		return true
	}
	return s.secretKeys[provider][key]
}

// Describe builds the DTO of a Connection.
func (s *Service) Describe(c store.Connection) DTO {
	d := DTO{Connection: c, Parameters: map[string]string{}}
	cfg, err := s.Config(c)
	if err != nil {
		return d
	}
	for k, v := range cfg {
		if k == "type" || s.isSecret(c.Provider, k) {
			continue
		}
		d.Parameters[k] = v
	}
	if c.Provider == "webdav" {
		d.Vendor = cfg["vendor"]
		if d.Vendor == "nextcloud" || d.Vendor == "owncloud" {
			if base, _, err := nextcloud.DAVLocation(cfg["url"]); err == nil {
				d.ServerURL = base
			}
			d.User = cfg["user"]
		}
	}
	return d
}

// Get returns a Connection or connection.notFound.
func (s *Service) Get(id string) (store.Connection, error) {
	c, err := s.st.Connection(id)
	if errors.Is(err, store.ErrNotFound) {
		return c, api.Fail("connection.notFound", msg.New("connection.notFound", "id", id))
	}
	return c, err
}

// List returns all Connections.
func (s *Service) List() ([]DTO, error) {
	cs, err := s.st.Connections("")
	if err != nil {
		return nil, err
	}
	out := make([]DTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, s.Describe(c))
	}
	return out, nil
}

// ValidateName checks a display name for a new or renamed Connection.
func (s *Service) ValidateName(name, exceptID string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return api.Invalid("name is required")
	}
	if strings.ContainsAny(name, "/:") || name == "." || name == ".." {
		return api.Fail("connection.nameReserved", msg.New("connection.nameNotFolder", "name", name))
	}
	if c, err := s.st.ConnectionByName(name); err == nil && c.ID != exceptID {
		return api.Fail("connection.nameTaken", msg.New("connection.nameTaken", "name", name))
	}
	for id, p := range s.pending {
		if id != exceptID && strings.EqualFold(p.name, name) {
			return api.Fail("connection.nameTaken", msg.New("connection.namePending", "name", name))
		}
	}
	st, err := s.st.Settings()
	if err != nil {
		return err
	}
	if filepath.Clean(filepath.Join(s.paths.Expand(st.BaseFolder), name)) == filepath.Clean(s.paths.Expand(st.MountFolder)) {
		return api.Fail("connection.nameReserved", msg.New("connection.nameMountFolder", "name", name))
	}
	return nil
}

// ConfigStep is the contract's ConfigStep object.
type ConfigStep struct {
	ConnectionID string     `json:"connectionId"`
	Done         bool       `json:"done"`
	Pending      bool       `json:"pending"`
	State        string     `json:"state"`
	Option       any        `json:"option"`
	Error        string     `json:"error"`
	ErrorCode    string     `json:"errorCode,omitempty"`
	ErrorParams  msg.Params `json:"errorParams,omitempty"`
	Connection   *DTO       `json:"connection"`
}

// failedStep is the ConfigStep of connection id that failed with t.
func failedStep(id string, t msg.Text) ConfigStep {
	return ConfigStep{ConnectionID: id, Error: t.Message, ErrorCode: t.Code, ErrorParams: t.Params}
}

type configOut struct {
	State  string `json:"State"`
	Option any    `json:"Option"`
	Error  string `json:"Error"`
	Result string `json:"Result"`
}

// CreateParams are connections.create params.
type CreateParams struct {
	Name       string            `json:"name"`
	Provider   string            `json:"provider"`
	Parameters map[string]string `json:"parameters"`
}

// Create starts the rclone config state machine for a new Connection.
func (s *Service) Create(ctx context.Context, p CreateParams) (ConfigStep, error) {
	if p.Provider == "" {
		return ConfigStep{}, api.Invalid("provider is required")
	}
	s.expireSetups()
	s.mu.Lock()
	if err := s.ValidateName(p.Name, ""); err != nil {
		s.mu.Unlock()
		return ConfigStep{}, err
	}
	id := store.NewID()
	s.pending[id] = pendingConn{name: strings.TrimSpace(p.Name), provider: p.Provider, started: time.Now()}
	s.mu.Unlock()
	params := map[string]any{}
	for k, v := range p.Parameters {
		params[k] = v
	}
	return s.runStep(id, "config/create", map[string]any{
		"name": RemoteName(id), "type": p.Provider, "parameters": params,
		"opt": map[string]any{"nonInteractive": true, "obscure": true},
	}), nil
}

// ContinueParams are connections.continue params.
type ContinueParams struct {
	ConnectionID string `json:"connectionId"`
	State        string `json:"state"`
	Result       string `json:"result"`
}

// Continue answers a question of the config state machine.
func (s *Service) Continue(ctx context.Context, p ContinueParams) (ConfigStep, error) {
	s.mu.Lock()
	_, ok := s.pending[p.ConnectionID]
	s.mu.Unlock()
	if !ok {
		return ConfigStep{}, api.Fail("connection.notFound", msg.New("connection.setupNotFound", "id", p.ConnectionID))
	}
	return s.runStep(p.ConnectionID, "config/update", map[string]any{
		"name": RemoteName(p.ConnectionID), "parameters": map[string]any{},
		"opt": map[string]any{"continue": true, "state": p.State, "result": p.Result, "nonInteractive": true, "obscure": true},
	}), nil
}

// runStep runs a (possibly blocking, e.g. OAuth) config call. Results within
// 3 s are returned directly; later results arrive as connection.configStep.
func (s *Service) runStep(id, method string, in map[string]any) ConfigStep {
	done := make(chan ConfigStep, 1)
	go func() {
		ch := make(chan ConfigStep, 1)
		go func() { ch <- s.callStep(id, method, in) }()
		var step ConfigStep
		select {
		case step = <-ch:
		case <-time.After(10 * time.Minute):
			step = failedStep(id, msg.New("connection.signInTimedOut"))
			s.abandon(id)
		}
		select {
		case done <- step:
		default:
		}
		s.pub.Publish("connection.configStep", step)
	}()
	select {
	case step := <-done:
		return step
	case <-time.After(3 * time.Second):
		return ConfigStep{ConnectionID: id, Pending: true}
	}
}

func (s *Service) callStep(id, method string, in map[string]any) ConfigStep {
	var out configOut
	if err := rcl.CallInto(method, in, &out); err != nil {
		s.abandon(id)
		return failedStep(id, api.TextOf(err))
	}
	if out.State != "" {
		step := ConfigStep{ConnectionID: id, State: out.State, Option: out.Option}
		if out.Error != "" {
			t := msg.Detail(out.Error)
			step.Error, step.ErrorCode, step.ErrorParams = t.Message, t.Code, t.Params
		}
		return step
	}
	if out.Error != "" {
		s.abandon(id)
		return failedStep(id, msg.Detail(out.Error))
	}
	c, err := s.finish(id)
	if err != nil {
		return failedStep(id, api.TextOf(err))
	}
	d := s.Describe(c)
	return ConfigStep{ConnectionID: id, Done: true, Connection: &d}
}

func (s *Service) finish(id string) (store.Connection, error) {
	s.mu.Lock()
	p, ok := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if !ok {
		return store.Connection{}, api.Fail("connection.notFound", msg.New("connection.setupCancelled"))
	}
	c := store.Connection{ID: id, Name: p.name, Kind: "remote", RcloneRemote: RemoteName(id), Provider: p.provider, CreatedAt: store.Now()}
	if err := s.st.InsertConnection(c); err != nil {
		_, _ = rcl.Call("config/delete", map[string]any{"name": RemoteName(id)})
		if store.IsUniqueViolation(err) {
			return c, api.Fail("connection.nameTaken", msg.New("connection.nameTaken", "name", p.name))
		}
		return c, err
	}
	s.log.Info("connection", id, msg.New("connection.added", "name", c.Name, "provider", c.Provider), nil)
	s.pub.Publish("connections.changed", struct{}{})
	return c, nil
}

// setupTTL bounds how long an unfinished connection setup keeps its name.
const setupTTL = 15 * time.Minute

// expireSetups abandons setups the user left at a question step.
func (s *Service) expireSetups() {
	s.mu.Lock()
	var old []string
	for id, p := range s.pending {
		if time.Since(p.started) > setupTTL {
			old = append(old, id)
		}
	}
	s.mu.Unlock()
	for _, id := range old {
		s.abandon(id)
	}
}

// CancelSetup abandons an unfinished setup (the App closed its sheet).
func (s *Service) CancelSetup(id string) {
	s.abandon(id)
}

// CleanupOrphans deletes cw-* remotes without a Connection (setups that
// never finished before the Core stopped).
func (s *Service) CleanupOrphans() {
	var res struct {
		Remotes []string `json:"remotes"`
	}
	if err := rcl.CallInto("config/listremotes", nil, &res); err != nil {
		return
	}
	for _, name := range res.Remotes {
		if !strings.HasPrefix(name, "cw-") {
			continue
		}
		if _, err := s.st.Connection(strings.TrimPrefix(name, "cw-")); errors.Is(err, store.ErrNotFound) {
			slog.Info("removing unfinished connection setup", "remote", name)
			_, _ = rcl.Call("config/delete", map[string]any{"name": name})
		}
	}
}

func (s *Service) abandon(id string) {
	s.mu.Lock()
	_, ok := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if ok {
		_, _ = rcl.Call("config/delete", map[string]any{"name": RemoteName(id)})
	}
}

// UpdateParams are connections.update params.
type UpdateParams struct {
	ID         string            `json:"id"`
	Name       *string           `json:"name"`
	Parameters map[string]string `json:"parameters"`
}

// Update renames a Connection and/or changes rclone parameters.
func (s *Service) Update(ctx context.Context, p UpdateParams) (DTO, error) {
	c, err := s.Get(p.ID)
	if err != nil {
		return DTO{}, err
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != c.Name {
		s.mu.Lock()
		err := s.ValidateName(*p.Name, c.ID)
		s.mu.Unlock()
		if err != nil {
			return DTO{}, err
		}
		if err := s.st.RenameConnection(c.ID, strings.TrimSpace(*p.Name)); err != nil {
			return DTO{}, err
		}
	}
	if len(p.Parameters) > 0 {
		params := map[string]any{}
		for k, v := range p.Parameters {
			params[k] = v
		}
		if _, err := rcl.Call("config/update", map[string]any{"name": c.RcloneRemote, "parameters": params,
			"opt": map[string]any{"nonInteractive": true, "obscure": true}}); err != nil {
			return DTO{}, api.Wrap("connection.testFailed", err)
		}
		rcl.ForgetRemote(c.RcloneRemote)
		if s.Mounts != nil {
			s.Mounts.RestartForConnection(c.ID)
		}
	}
	c, _ = s.st.Connection(c.ID)
	s.log.Info("connection", c.ID, msg.New("connection.updated", "name", c.Name), nil)
	s.pub.Publish("connections.changed", struct{}{})
	return s.Describe(c), nil
}

// Dependent is an object that uses a Connection.
type Dependent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Dependents lists Mounts, Offline Items and Vaults using a Connection.
func (s *Service) Dependents(connID string) ([]Dependent, error) {
	var out []Dependent
	ms, err := s.st.Mounts()
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if m.ConnectionID == connID {
			out = append(out, Dependent{Kind: "mount", ID: m.ID, Name: m.VolumeName})
		}
	}
	its, err := s.st.OfflineItems()
	if err != nil {
		return nil, err
	}
	for _, it := range its {
		if it.ConnectionID == connID {
			out = append(out, Dependent{Kind: "offline", ID: it.ID, Name: it.StoragePath})
		}
	}
	vs, err := s.st.Vaults()
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if v.ConnectionID == connID {
			out = append(out, Dependent{Kind: "vault", ID: v.ID, Name: v.Name})
		}
	}
	return out, nil
}

// Delete removes a Connection that nothing depends on.
func (s *Service) Delete(ctx context.Context, id string) error {
	c, err := s.Get(id)
	if err != nil {
		return err
	}
	if c.Kind == "vault" {
		return api.Fail("connection.inUse", msg.New("connection.removeVault"))
	}
	deps, err := s.Dependents(id)
	if err != nil {
		return err
	}
	if len(deps) > 0 {
		return api.Fail("connection.inUse", msg.New("connection.inUse", "name", c.Name, "count", len(deps))).WithData("dependents", deps)
	}
	cfg, _ := s.Config(c)
	if err := s.st.DeleteLinksForConnection(id); err != nil {
		return err
	}
	if _, err := rcl.Call("config/delete", map[string]any{"name": c.RcloneRemote}); err != nil {
		slog.Warn("config/delete", "err", err)
	}
	rcl.ForgetRemote(c.RcloneRemote)
	if err := s.st.DeleteConnection(id); err != nil {
		return err
	}
	if c.Provider == "webdav" && (cfg["vendor"] == "nextcloud" || cfg["vendor"] == "owncloud") {
		go revokeAppPassword(cfg)
	}
	s.log.Info("connection", id, msg.New("connection.removed", "name", c.Name), nil)
	s.pub.Publish("connections.changed", struct{}{})
	return nil
}

func revokeAppPassword(cfg map[string]string) {
	base, _, err := nextcloud.DAVLocation(cfg["url"])
	if err != nil {
		return
	}
	pass, err := obscure.Reveal(cfg["pass"])
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := &nextcloud.Client{Base: base, User: cfg["user"], Pass: pass}
	if err := cl.DeleteAppPassword(ctx); err != nil {
		slog.Info("revoke app password (best effort)", "err", err)
	}
}

// TestResult is connections.test's result.
type TestResult struct {
	OK    bool   `json:"ok"`
	Total *int64 `json:"total,omitempty"`
	Used  *int64 `json:"used,omitempty"`
	Free  *int64 `json:"free,omitempty"`
}

// Test checks that a Connection works.
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	c, err := s.Get(id)
	if err != nil {
		return TestResult{}, err
	}
	return testRemote(c.RcloneRemote + ":")
}

func testRemote(fsName string) (TestResult, error) {
	var info struct {
		Features map[string]bool `json:"Features"`
	}
	if err := rcl.CallInto("operations/fsinfo", map[string]any{"fs": fsName}, &info); err != nil {
		return TestResult{}, api.Wrap("connection.testFailed", err)
	}
	if info.Features["About"] {
		var about struct {
			Total *int64 `json:"total"`
			Used  *int64 `json:"used"`
			Free  *int64 `json:"free"`
		}
		if err := rcl.CallInto("operations/about", map[string]any{"fs": fsName}, &about); err == nil {
			return TestResult{OK: true, Total: about.Total, Used: about.Used, Free: about.Free}, nil
		}
	}
	if _, err := rcl.Call("operations/list", map[string]any{"fs": fsName, "remote": "", "opt": map[string]any{"dirsOnly": true}}); err != nil {
		return TestResult{}, api.Wrap("connection.testFailed", err)
	}
	return TestResult{OK: true}, nil
}

// Entry is one connections.browse element.
type Entry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"modTime"`
	MimeType string `json:"mimeType"`
}

// Browse lists a remote directory, directories first.
func (s *Service) Browse(ctx context.Context, connID, dir string) ([]Entry, error) {
	c, err := s.Get(connID)
	if err != nil {
		return nil, err
	}
	var res struct {
		List []struct {
			Path     string    `json:"Path"`
			Name     string    `json:"Name"`
			Size     int64     `json:"Size"`
			MimeType string    `json:"MimeType"`
			ModTime  time.Time `json:"ModTime"`
			IsDir    bool      `json:"IsDir"`
		} `json:"list"`
	}
	dir = strings.Trim(dir, "/")
	if err := rcl.CallInto("operations/list", map[string]any{"fs": c.RcloneRemote + ":", "remote": dir}, &res); err != nil {
		return nil, api.Wrap("connection.testFailed", err)
	}
	out := make([]Entry, 0, len(res.List))
	for _, e := range res.List {
		out = append(out, Entry{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, ModTime: e.ModTime.UnixMilli(), MimeType: e.MimeType})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}
