// Package mounts keeps Mounts attached: one worker process per active Mount,
// restarted with backoff, health-checked, and cleaned up on shutdown.
package mounts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/notify"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// Mount states.
const (
	StateUnmounted = "unmounted"
	StateMounting  = "mounting"
	StateMounted   = "mounted"
	StateError     = "error"
)

// FUSE library locations.
var (
	FuseTPath   = "/usr/local/lib/libfuse-t.dylib"
	MacFUSEPath = "/Library/Filesystems/macfuse.fs"
)

// Publisher pushes events.
type Publisher interface {
	Publish(eventType string, data any)
}

// Notifier queues notifications.
type Notifier interface {
	Notify(kind string, params any)
}

// DTO is the contract's Mount object.
type DTO struct {
	store.Mount
	State string `json:"state"`
	Error string `json:"error"`
}

// Service implements the mounts.* methods.
type Service struct {
	st         *store.Store
	log        *activity.Logger
	notify     Notifier
	pub        Publisher
	paths      paths.Paths
	configPass string
	logLevel   func() string

	mu      sync.Mutex
	runs    map[string]*run
	closing bool

	healthCancel  chan struct{} // non-nil while the health loop runs (≥1 Mount active)
	healthRunning atomic.Bool

	nfsOnce sync.Once
	blocks  map[string]string // rclone option name -> vfs | mount | nfs
}

type run struct {
	m          store.Mount
	worker     *sv.Worker
	state      string
	err        string
	stop       chan struct{} // closed to stop supervising
	done       chan struct{} // closed when supervise returned
	retrigger  chan struct{} // wakes a Mount parked in the error state
	failures   []time.Time
	mountedAt  time.Time
	healthFail int
	port       int // NFS server port (nfsmount)
}

// New creates the service.
func New(st *store.Store, log *activity.Logger, n Notifier, pub Publisher, p paths.Paths, configPass string, logLevel func() string) *Service {
	return &Service{st: st, log: log, notify: n, pub: pub, paths: p, configPass: configPass, logLevel: logLevel,
		runs: map[string]*run{}}
}

// FuseStatus reports which FUSE implementations are installed.
type FuseStatus struct {
	FuseT   bool `json:"fuseT"`
	MacFUSE bool `json:"macFUSE"`
}

// Fuse detects FUSE-T and macFUSE.
func Fuse() FuseStatus {
	_, errT := os.Stat(FuseTPath)
	_, errM := os.Stat(MacFUSEPath)
	return FuseStatus{FuseT: errT == nil, MacFUSE: errM == nil}
}

func (s *Service) dto(m store.Mount) DTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := DTO{Mount: m, State: StateUnmounted}
	if r := s.runs[m.ID]; r != nil {
		d.State, d.Error = r.state, r.err
	}
	return d
}

func (s *Service) publish(m store.Mount) {
	s.pub.Publish("mount.status", s.dto(m))
}

// Get returns a Mount or mount.notFound.
func (s *Service) Get(id string) (store.Mount, error) {
	m, err := s.st.Mount(id)
	if errors.Is(err, store.ErrNotFound) {
		return m, api.Errorf("mount.notFound", "Mount %s not found", id)
	}
	return m, err
}

// List returns all Mounts with their state.
func (s *Service) List() ([]DTO, error) {
	ms, err := s.st.Mounts()
	if err != nil {
		return nil, err
	}
	out := make([]DTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, s.dto(m))
	}
	return out, nil
}

// CreateParams are mounts.create params.
type CreateParams struct {
	ConnectionID string            `json:"connectionId"`
	RemotePath   string            `json:"remotePath"`
	MountPoint   string            `json:"mountPoint"`
	VolumeName   string            `json:"volumeName"`
	MountType    string            `json:"mountType"`
	AutoMount    *bool             `json:"autoMount"`
	ReadOnly     bool              `json:"readOnly"`
	CacheMaxGB   int               `json:"cacheMaxGB"`
	Options      map[string]string `json:"options"`
}

func sanitizeName(s string) string {
	s = strings.NewReplacer("/", "-", ":", "-").Replace(strings.TrimSpace(s))
	if s == "" || s == "." || s == ".." {
		s = "CloudWire"
	}
	return s
}

// Create stores a new Mount and mounts it when autoMount is set.
func (s *Service) Create(ctx context.Context, p CreateParams) (DTO, error) {
	conn, err := s.st.Connection(p.ConnectionID)
	if err != nil {
		return DTO{}, api.Errorf("connection.notFound", "Connection %s not found", p.ConnectionID)
	}
	st, err := s.st.Settings()
	if err != nil {
		return DTO{}, err
	}
	m := store.Mount{
		ID: store.NewID(), ConnectionID: conn.ID, RemotePath: strings.Trim(p.RemotePath, "/"), MountType: p.MountType,
		AutoMount: true, ReadOnly: p.ReadOnly, CacheMaxGB: p.CacheMaxGB, Options: p.Options, CreatedAt: store.Now(),
	}
	if m.Options == nil {
		m.Options = map[string]string{}
	}
	if p.AutoMount != nil {
		m.AutoMount = *p.AutoMount
	}
	if m.MountType == "" {
		m.MountType = st.DefaultMountType
	}
	if m.CacheMaxGB <= 0 {
		m.CacheMaxGB = st.DefaultCacheMaxGB
	}
	m.VolumeName = strings.TrimSpace(p.VolumeName)
	if m.VolumeName == "" {
		m.VolumeName = conn.Name
		if m.RemotePath != "" {
			m.VolumeName += " – " + path.Base(m.RemotePath)
		}
	}
	m.MountPoint = p.MountPoint
	if m.MountPoint == "" {
		m.MountPoint = filepath.Join(s.paths.Expand(st.MountFolder), sanitizeName(m.VolumeName))
	}
	if err := s.validate(&m, ""); err != nil {
		return DTO{}, err
	}
	if err := s.st.InsertMount(m); err != nil {
		if store.IsUniqueViolation(err) {
			return DTO{}, api.Errorf("mount.pointInUse", "Another Mount already uses %s", m.MountPoint)
		}
		return DTO{}, err
	}
	s.log.Info("mount", m.ID, fmt.Sprintf("Mount %q created at %s", m.VolumeName, m.MountPoint), nil)
	if m.AutoMount {
		s.start(m)
	}
	d := s.dto(m)
	s.pub.Publish("mount.status", d)
	return d, nil
}

// validate checks type, FUSE availability and the mount point.
func (s *Service) validate(m *store.Mount, existingID string) error {
	switch m.MountType {
	case "nfsmount":
	case "cmount":
		if f := Fuse(); !f.FuseT && !f.MacFUSE {
			return api.Errorf("mount.fuseUnavailable", "FUSE requires FUSE-T or macFUSE to be installed")
		}
	default:
		return api.Invalid("unknown mount type %q", m.MountType)
	}
	if m.CacheMaxGB < 1 {
		return api.Invalid("cacheMaxGB must be at least 1")
	}
	mp := filepath.Clean(s.paths.Expand(m.MountPoint))
	if !filepath.IsAbs(mp) {
		return api.Invalid("mount point must be an absolute path")
	}
	m.MountPoint = mp
	all, err := s.st.Mounts()
	if err != nil {
		return err
	}
	for _, o := range all {
		if o.ID != existingID && o.MountPoint == mp {
			return api.Errorf("mount.pointInUse", "Another Mount already uses %s", mp)
		}
	}
	items, err := s.st.OfflineItems()
	if err != nil {
		return err
	}
	for _, it := range items {
		// An Offline Item would sync the streamed Mount content back into the cloud.
		if paths.IsWithin(mp, it.StoragePath) || paths.IsWithin(it.StoragePath, mp) {
			return api.Errorf("mount.pointInUse", "%s overlaps the Offline Item stored at %s", mp, it.StoragePath)
		}
	}
	if IsMounted(mp) {
		return api.Errorf("mount.pointInUse", "%s is already a mounted volume", mp)
	}
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return api.Errorf("mount.failed", "Cannot create %s: %v", mp, err)
	}
	f, err := os.Open(mp)
	if err != nil {
		return api.Errorf("mount.failed", "Cannot open %s: %v", mp, err)
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil && err != io.EOF {
		return api.Errorf("mount.failed", "Cannot read %s: %v", mp, err)
	}
	for _, n := range names {
		if n != ".DS_Store" && n != ".localized" {
			return api.Errorf("mount.pointNotEmpty", "The folder %s is not empty", mp)
		}
	}
	return nil
}

// UpdateParams are mounts.update params (nil = unchanged).
type UpdateParams struct {
	ID         string             `json:"id"`
	RemotePath *string            `json:"remotePath"`
	MountPoint *string            `json:"mountPoint"`
	VolumeName *string            `json:"volumeName"`
	MountType  *string            `json:"mountType"`
	AutoMount  *bool              `json:"autoMount"`
	ReadOnly   *bool              `json:"readOnly"`
	CacheMaxGB *int               `json:"cacheMaxGB"`
	Options    *map[string]string `json:"options"`
}

// Update changes a Mount and remounts it if it was active.
func (s *Service) Update(ctx context.Context, p UpdateParams) (DTO, error) {
	m, err := s.Get(p.ID)
	if err != nil {
		return DTO{}, err
	}
	wasActive := s.active(m.ID)
	if wasActive {
		s.stop(m.ID, true)
	}
	old := m
	if p.RemotePath != nil {
		m.RemotePath = strings.Trim(*p.RemotePath, "/")
	}
	if p.MountPoint != nil {
		m.MountPoint = *p.MountPoint
	}
	if p.VolumeName != nil && strings.TrimSpace(*p.VolumeName) != "" {
		m.VolumeName = strings.TrimSpace(*p.VolumeName)
	}
	if p.MountType != nil {
		m.MountType = *p.MountType
	}
	if p.AutoMount != nil {
		m.AutoMount = *p.AutoMount
	}
	if p.ReadOnly != nil {
		m.ReadOnly = *p.ReadOnly
	}
	if p.CacheMaxGB != nil {
		m.CacheMaxGB = *p.CacheMaxGB
	}
	if p.Options != nil {
		m.Options = *p.Options
	}
	if err := s.validate(&m, m.ID); err != nil {
		if wasActive {
			s.start(old)
		}
		return DTO{}, err
	}
	if err := s.st.UpdateMount(m); err != nil {
		return DTO{}, err
	}
	if old.MountPoint != m.MountPoint && !IsMounted(old.MountPoint) {
		_ = os.Remove(old.MountPoint) // only succeeds when empty
	}
	if wasActive {
		s.start(m)
	}
	s.log.Info("mount", m.ID, fmt.Sprintf("Mount %q updated", m.VolumeName), nil)
	d := s.dto(m)
	s.pub.Publish("mount.status", d)
	return d, nil
}

// Delete unmounts and removes a Mount.
func (s *Service) Delete(ctx context.Context, id string) error {
	m, err := s.Get(id)
	if err != nil {
		return err
	}
	s.stop(id, true)
	if err := s.st.DeleteMount(id); err != nil {
		return err
	}
	if !IsMounted(m.MountPoint) {
		_ = os.Remove(m.MountPoint) // only succeeds when empty
	}
	s.log.Info("mount", id, fmt.Sprintf("Mount %q removed", m.VolumeName), nil)
	s.pub.Publish("mount.status", DTO{Mount: m, State: "deleted"})
	return nil
}

// MountByID starts a Mount.
func (s *Service) MountByID(ctx context.Context, id string) (DTO, error) {
	m, err := s.Get(id)
	if err != nil {
		return DTO{}, err
	}
	s.mu.Lock()
	r := s.runs[id]
	s.mu.Unlock()
	switch {
	case r == nil:
		if IsOwnMount(m.MountPoint) {
			_ = ForceUnmount(m.MountPoint)
		}
		s.start(m)
	default:
		// A Mount parked in the error state retries on user action.
		select {
		case r.retrigger <- struct{}{}:
		default:
		}
	}
	return s.dto(m), nil
}

// UnmountByID stops a Mount.
func (s *Service) UnmountByID(ctx context.Context, id string) (DTO, error) {
	m, err := s.Get(id)
	if err != nil {
		return DTO{}, err
	}
	s.stop(id, true)
	s.log.Info("mount", id, fmt.Sprintf("Mount %q unmounted", m.VolumeName), nil)
	d := s.dto(m)
	s.pub.Publish("mount.status", d)
	return d, nil
}

// Stats returns the VFS statistics of an active Mount.
func (s *Service) Stats(ctx context.Context, id string) (any, error) {
	s.mu.Lock()
	r := s.runs[id]
	var w *sv.Worker
	if r != nil {
		w = r.worker
	}
	s.mu.Unlock()
	if w == nil {
		return nil, api.Errorf("mount.failed", "Mount is not active")
	}
	msg, err := w.Stats(5 * time.Second)
	if err != nil {
		return nil, api.Errorf("mount.failed", "%v", err)
	}
	return msg.VFS, nil
}

func (s *Service) active(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id] != nil
}

// StartAuto mounts every Mount with autoMount (Core start).
func (s *Service) StartAuto() {
	ms, err := s.st.Mounts()
	if err != nil {
		slog.Error("list mounts", "err", err)
		return
	}
	for _, m := range ms {
		if m.AutoMount {
			s.start(m)
		}
	}
}

// CleanupStale force-unmounts leftovers of a crashed Core at configured mount points.
func (s *Service) CleanupStale() {
	ms, err := s.st.Mounts()
	if err != nil {
		return
	}
	for _, m := range ms {
		if IsOwnMount(m.MountPoint) {
			slog.Info("cleaning up stale mount", "mountPoint", m.MountPoint)
			_ = ForceUnmount(m.MountPoint)
		}
	}
}

// RestartForConnection restarts the active Mounts of a Connection.
func (s *Service) RestartForConnection(connID string) {
	for _, m := range s.activeMounts(connID) {
		s.stop(m.ID, true)
		if fresh, err := s.st.Mount(m.ID); err == nil {
			s.start(fresh)
		}
	}
}

// StopForConnection unmounts the active Mounts of a Connection (Vault lock).
func (s *Service) StopForConnection(connID string) {
	for _, m := range s.activeMounts(connID) {
		s.stop(m.ID, true)
		s.publish(m)
	}
}

// StartForConnection mounts the auto-mount Mounts of a Connection (Vault unlock).
func (s *Service) StartForConnection(connID string) {
	ms, _ := s.st.Mounts()
	for _, m := range ms {
		if m.ConnectionID == connID && m.AutoMount && !s.active(m.ID) {
			s.start(m)
		}
	}
}

func (s *Service) activeMounts(connID string) []store.Mount {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Mount
	for _, r := range s.runs {
		if r.m.ConnectionID == connID {
			out = append(out, r.m)
		}
	}
	return out
}

// Forget tells every active Mount of connID whose remote root contains
// remotePath to drop its cached directory listing of that path.
func (s *Service) Forget(connID, remotePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.m.ConnectionID != connID || r.worker == nil {
			continue
		}
		rel, ok := relativeTo(remotePath, r.m.RemotePath)
		if !ok {
			continue
		}
		_ = r.worker.Send(sv.Command{Cmd: "forget", Dir: rel})
	}
}

// relativeTo returns p relative to root when p lies within root.
func relativeTo(p, root string) (string, bool) {
	p, root = strings.Trim(p, "/"), strings.Trim(root, "/")
	switch {
	case root == "":
		return p, true
	case p == root:
		return "", true
	case strings.HasPrefix(p, root+"/"):
		return p[len(root)+1:], true
	}
	return "", false
}

// Retrigger restarts Mounts parked in the error state and health-checks the
// others (wake, network change, user action).
func (s *Service) Retrigger() {
	s.mu.Lock()
	for _, r := range s.runs {
		select {
		case r.retrigger <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	go s.healthCheck()
}

// ---- supervision ----

func (s *Service) start(m store.Mount) {
	s.mu.Lock()
	if s.closing || s.runs[m.ID] != nil {
		s.mu.Unlock()
		return
	}
	r := &run{m: m, state: StateMounting, stop: make(chan struct{}), done: make(chan struct{}), retrigger: make(chan struct{}, 1)}
	s.runs[m.ID] = r
	s.updateHealthLocked()
	s.mu.Unlock()
	s.publish(m)
	go func() {
		defer close(r.done)
		s.supervise(r)
	}()
}

// updateHealthLocked runs the health loop only while at least one Mount is active.
func (s *Service) updateHealthLocked() {
	switch {
	case len(s.runs) > 0 && s.healthCancel == nil:
		s.healthCancel = make(chan struct{})
		go s.healthLoop(s.healthCancel)
	case len(s.runs) == 0 && s.healthCancel != nil:
		close(s.healthCancel)
		s.healthCancel = nil
	}
}

// stop ends supervision and detaches the Mount; wait blocks until done.
func (s *Service) stop(id string, wait bool) {
	s.mu.Lock()
	r := s.runs[id]
	delete(s.runs, id)
	s.updateHealthLocked()
	s.mu.Unlock()
	if r == nil {
		return
	}
	close(r.stop)
	f := func() {
		// Wait for supervise so it cannot touch the mount point any more.
		<-r.done
		s.mu.Lock()
		w := r.worker
		s.mu.Unlock()
		if w != nil {
			s.stopWorker(r.m, w)
		}
		// A worker that crashed leaves its NFS mount attached for reattach; with
		// no server behind it any more it must be detached here.
		if IsOwnMount(r.m.MountPoint) {
			if err := ForceUnmount(r.m.MountPoint); err != nil {
				s.log.Error("mount", r.m.ID, fmt.Sprintf("Could not unmount %q: %v", r.m.VolumeName, err), nil)
			}
		}
	}
	if wait {
		f()
	} else {
		go f()
	}
}

func (s *Service) stopWorker(m store.Mount, w *sv.Worker) {
	w.Stop(10 * time.Second)
	if out := w.Outcome(); out.Error != "" {
		s.log.Warn("mount", m.ID, fmt.Sprintf("Unmounting %q: %s", m.VolumeName, out.Error), nil)
	}
}

// park waits in the error state for a user action, wake or network change.
func (s *Service) park(r *run) bool {
	select {
	case <-r.stop:
		return false
	case <-r.retrigger:
		return true
	}
}

func (s *Service) setState(r *run, state, errText string) {
	s.mu.Lock()
	r.state, r.err = state, errText
	s.mu.Unlock()
	s.publish(r.m)
}

// optionBlock returns the rclone option block ("vfs", "mount", "nfs") of an option name.
func (s *Service) optionBlock(name string) string {
	s.nfsOnce.Do(s.loadOptionBlocks)
	return s.blocks[name]
}

func (s *Service) job(m store.Mount, port int) (sv.Job, error) {
	conn, err := s.st.Connection(m.ConnectionID)
	if err != nil {
		return sv.Job{}, err
	}
	lvl := "INFO"
	if s.logLevel != nil && s.logLevel() == "debug" {
		lvl = "DEBUG"
	}
	job := sv.Job{Type: sv.JobMount, ID: m.ID, LogLevel: lvl}
	params := map[string]any{
		"fs":                 conn.RcloneRemote + ":" + m.RemotePath,
		"vfs_cache_mode":     "full",
		"vfs_cache_max_size": fmt.Sprintf("%dG", m.CacheMaxGB),
		"read_only":          m.ReadOnly,
	}
	if m.MountType == "nfsmount" {
		// Handles on disk survive a worker restart, so the kernel mount keeps working.
		params["nfs_cache_type"] = "disk"
		mj := &sv.MountJob{Serve: true, Port: port, MountPoint: m.MountPoint, Params: params}
		for k, v := range m.Options {
			switch s.optionBlock(k) {
			case "vfs":
				params[k] = v
			case "nfs":
				if k != "addr" {
					params[k] = v
				}
			case "mount":
				if k == "option" {
					for _, o := range strings.Split(v, ",") {
						if o = strings.TrimSpace(o); o != "" {
							mj.MountOptions = append(mj.MountOptions, o)
						}
					}
				}
			}
		}
		job.Mount = mj
		return job, nil
	}
	params["mountPoint"] = m.MountPoint
	params["mountType"] = m.MountType
	params["volname"] = m.VolumeName
	params["noappledouble"] = true
	params["noapplexattr"] = true
	for k, v := range m.Options {
		switch s.optionBlock(k) {
		case "vfs", "mount":
			params[k] = v
		}
	}
	job.Mount = &sv.MountJob{Params: params}
	return job, nil
}

func (s *Service) loadOptionBlocks() {
	s.blocks = map[string]string{}
	var res map[string][]struct {
		Name string `json:"Name"`
	}
	if err := rcl.CallInto("options/info", map[string]any{"blocks": "vfs,mount,nfs"}, &res); err == nil {
		for block, opts := range res {
			for _, o := range opts {
				s.blocks[o.Name] = block
			}
		}
	}
}

// freePort picks an unused localhost TCP port for a Mount's NFS server.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (s *Service) supervise(r *run) {
	backoff := time.Second
	port := 0
	attachedBefore := false
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		// A kernel NFS mount left by a crashed worker is kept: the new worker
		// serves the same port and the system NFS client reconnects.
		reattach := r.m.MountType == "nfsmount" && port != 0 && attachedBefore && IsOwnMount(r.m.MountPoint)
		if !reattach {
			if IsMounted(r.m.MountPoint) && !IsOwnMount(r.m.MountPoint) {
				s.setState(r, StateError, fmt.Sprintf("%s is used by another volume", r.m.MountPoint))
				if !s.park(r) {
					return
				}
				continue
			}
			if IsOwnMount(r.m.MountPoint) {
				_ = ForceUnmount(r.m.MountPoint)
			}
			if IsMounted(r.m.MountPoint) {
				// Still attached (unmount failed): never stat it, retry later.
				s.setState(r, StateMounting, "the previous mount is still attached")
				if !s.failed(r, &backoff) {
					return
				}
				continue
			}
			_ = os.MkdirAll(r.m.MountPoint, 0o755)
			if r.m.MountType == "nfsmount" {
				p, err := freePort()
				if err != nil {
					s.setState(r, StateError, err.Error())
					if !s.park(r) {
						return
					}
					continue
				}
				port = p
				s.mu.Lock()
				r.port = p
				s.mu.Unlock()
			}
		}
		job, err := s.job(r.m, port)
		if err != nil {
			s.setState(r, StateError, err.Error())
			if !s.park(r) {
				return
			}
			continue
		}
		s.setState(r, StateMounting, "")
		mounted := make(chan struct{}, 1)
		w, err := sv.Spawn(job, sv.Secrets{ConfigPass: s.configPass}, sv.Handlers{
			OnMsg: func(m sv.Msg) {
				if m.Type == "mounted" {
					select {
					case mounted <- struct{}{}:
					default:
					}
				}
			},
			OnLog: func(l sv.LogLine) { s.onLog(r.m, l) },
		})
		if err != nil {
			s.setState(r, StateError, err.Error())
			if !s.failed(r, &backoff) {
				return
			}
			continue
		}
		s.mu.Lock()
		stopped := false
		select {
		case <-r.stop:
			stopped = true
		default:
			r.worker = w
		}
		s.mu.Unlock()
		if stopped {
			s.stopWorker(r.m, w)
			return
		}
		select {
		case <-mounted:
			attachedBefore = true
			s.mu.Lock()
			r.mountedAt, r.healthFail = time.Now(), 0
			s.mu.Unlock()
			s.setState(r, StateMounted, "")
			how := "Mounted"
			if reattach {
				how = "Reconnected"
			}
			s.log.Info("mount", r.m.ID, fmt.Sprintf("%s %q at %s", how, r.m.VolumeName, r.m.MountPoint), nil)
		case <-w.Done():
			if reattach {
				// The port could not be served again: start over with a fresh mount.
				attachedBefore = false
			}
		case <-r.stop:
			return // stop() handles the worker
		}
		select {
		case <-w.Done():
		case <-r.stop:
			return
		}
		out := w.Outcome()
		s.mu.Lock()
		r.worker = nil
		s.mu.Unlock()
		if out.Status == sv.StatusStopped && out.Error == "ejected" {
			s.log.Info("mount", r.m.ID, fmt.Sprintf("Mount %q was ejected in Finder", r.m.VolumeName), nil)
			s.mu.Lock()
			if s.runs[r.m.ID] == r {
				delete(s.runs, r.m.ID)
				s.updateHealthLocked()
			}
			s.mu.Unlock()
			s.publish(r.m)
			return
		}
		msg := out.Error
		if msg == "" {
			msg = "mount worker exited"
		}
		s.log.Warn("mount", r.m.ID, fmt.Sprintf("Mount %q stopped unexpectedly: %s", r.m.VolumeName, msg), nil)
		s.setState(r, StateMounting, msg)
		if !s.failed(r, &backoff) {
			return
		}
	}
}

// failed applies the restart policy. It returns false when supervision ends.
func (s *Service) failed(r *run, backoff *time.Duration) bool {
	now := time.Now()
	s.mu.Lock()
	if !r.mountedAt.IsZero() && now.Sub(r.mountedAt) >= 5*time.Minute {
		*backoff = time.Second
		r.failures = nil
	}
	r.mountedAt = time.Time{}
	r.failures = append(r.failures, now)
	recent := r.failures[:0]
	for _, t := range r.failures {
		if now.Sub(t) <= 10*time.Minute {
			recent = append(recent, t)
		}
	}
	r.failures = recent
	tooMany := len(recent) >= 5
	errText := r.err
	s.mu.Unlock()
	if tooMany {
		s.setState(r, StateError, errText)
		s.log.Error("mount", r.m.ID, fmt.Sprintf("Mount %q failed repeatedly: %s", r.m.VolumeName, errText), nil)
		s.notify.Notify(notify.KindError, map[string]any{"title": r.m.VolumeName, "message": errText, "subjectId": r.m.ID})
		select {
		case <-r.stop:
			return false
		case <-r.retrigger:
			s.mu.Lock()
			r.failures = nil
			s.mu.Unlock()
			*backoff = time.Second
			return true
		}
	}
	t := time.NewTimer(*backoff)
	defer t.Stop()
	*backoff = min(*backoff*2, 60*time.Second)
	select {
	case <-r.stop:
		return false
	case <-t.C:
		return true
	case <-r.retrigger:
		return true
	}
}

// benign are rclone NFS server messages logged as errors during normal Finder use.
var benign = []string{
	"failing create to indicate lack of support for 'exclusive' mode",
}

func (s *Service) onLog(m store.Mount, l sv.LogLine) {
	level := l.Level
	for _, b := range benign {
		if strings.Contains(l.Msg, b) {
			level = "debug"
		}
	}
	switch level {
	case "error", "critical":
		s.log.Error("mount", m.ID, l.Msg, map[string]string{"object": l.Object})
	default:
		if s.log.Enabled("debug") {
			s.log.Debug("mount", m.ID, l.Msg, map[string]string{"object": l.Object, "level": l.Level})
		}
	}
}

// ---- health ----

func (s *Service) healthLoop(cancel <-chan struct{}) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-cancel:
			return
		case <-t.C:
			s.healthCheck()
		}
	}
}

// healthCheck asks every mounted worker for stats and probes its NFS server.
// Overlapping calls (timer, wake and network change at once) are skipped so
// one slow round is never counted twice.
func (s *Service) healthCheck() {
	if !s.healthRunning.CompareAndSwap(false, true) {
		return
	}
	defer s.healthRunning.Store(false)
	type probe struct {
		r    *run
		w    *sv.Worker
		port int
	}
	s.mu.Lock()
	var check []probe
	for _, r := range s.runs {
		if r.state == StateMounted && r.worker != nil {
			check = append(check, probe{r: r, w: r.worker, port: r.port})
		}
	}
	s.mu.Unlock()
	for _, c := range check {
		r, w := c.r, c.w
		ok := true
		if _, err := w.Stats(5 * time.Second); err != nil {
			ok = false
		} else if r.m.MountType == "nfsmount" && !serverResponds(c.port, 5*time.Second) {
			ok = false
		}
		s.mu.Lock()
		if ok {
			r.healthFail = 0
		} else {
			r.healthFail++
		}
		fails := r.healthFail
		s.mu.Unlock()
		if fails >= 2 {
			s.log.Warn("mount", r.m.ID, fmt.Sprintf("Mount %q is not responding; restarting it", r.m.VolumeName), nil)
			w.Kill()
			if r.m.MountType != "nfsmount" && IsOwnMount(r.m.MountPoint) {
				// NFS Mounts are reconnected by the next worker on the same port.
				_ = ForceUnmount(r.m.MountPoint)
			}
		}
	}
}

// serverResponds checks that a Mount's NFS server accepts connections. The
// Core never touches paths inside a Mount: that would need the "Network
// Volumes" privacy consent and could hang on a dead server.
func serverResponds(port int, timeout time.Duration) bool {
	if port == 0 {
		return true
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// StopAll unmounts everything (Core shutdown).
func (s *Service) StopAll() {
	s.mu.Lock()
	s.closing = true
	ids := make([]string, 0, len(s.runs))
	for id := range s.runs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stop(id, true)
		}()
	}
	wg.Wait()
}

// ---- system helpers ----

// IsMounted reports whether p is the mount point of a mounted file system.
//
// It never touches p itself: stat on a mount point whose NFS server died
// blocks, so only the (local) parent directory is resolved for symlinks.
func IsMounted(p string) bool {
	_, _, ok := mountInfo(p)
	return ok
}

// IsOwnMount reports whether p is a file system CloudWire mounted: an NFS
// mount of a localhost server, or a FUSE (macFUSE / FUSE-T) mount of a
// CloudWire remote. Only those are ever force-unmounted.
func IsOwnMount(p string) bool {
	fstype, from, ok := mountInfo(p)
	if !ok {
		return false
	}
	switch {
	case fstype == "nfs" && strings.HasPrefix(from, "localhost:"):
		return true
	case strings.HasPrefix(from, "cw-") || strings.HasPrefix(from, "cwvault-") || strings.HasPrefix(from, "fuse-t"):
		return true // FUSE mounts carry the rclone remote name
	}
	return false
}

// mountInfo returns the file system type and source mounted at p, using
// getfsstat only (it never touches p itself).
func mountInfo(p string) (fstype, from string, ok bool) {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		p = filepath.Join(r, filepath.Base(p))
	}
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return "", "", false
	}
	buf := make([]unix.Statfs_t, n+8)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return "", "", false
	}
	for _, st := range buf[:n] {
		if unix.ByteSliceToString(st.Mntonname[:]) == p {
			return unix.ByteSliceToString(st.Fstypename[:]), unix.ByteSliceToString(st.Mntfromname[:]), true
		}
	}
	return "", "", false
}

// ForceUnmount detaches a Mount with diskutil. diskutil runs responsible for
// itself: as the Core's child it would inherit the Core's missing "Network
// Volumes" privacy consent and block on a prompt.
func ForceUnmount(p string) error {
	out, err := platform.RunDisclaimed(30*time.Second, "/usr/sbin/diskutil", "unmount", "force", p)
	if err != nil {
		err = fmt.Errorf("diskutil unmount force %s: %w: %s", p, err, strings.TrimSpace(string(out)))
		slog.Warn(err.Error())
	}
	return err
}
