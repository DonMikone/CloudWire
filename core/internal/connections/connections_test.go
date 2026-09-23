package connections

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cw-conn")
	if err != nil {
		panic(err)
	}
	if err := rcl.Init(filepath.Join(dir, "rclone.conf"), "test-pass", rcl.Options{CacheDir: filepath.Join(dir, "cache")}); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type events struct {
	mu  sync.Mutex
	evs []map[string]any
}

func (e *events) Publish(t string, d any) {
	if m, ok := d.(map[string]any); ok && t == "connection.nextcloudLogin" {
		e.mu.Lock()
		e.evs = append(e.evs, m)
		e.mu.Unlock()
	}
}

func (e *events) wait(t *testing.T) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		if len(e.evs) > 0 {
			ev := e.evs[0]
			e.mu.Unlock()
			return ev
		}
		e.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no login event")
	return nil
}

func newService(t *testing.T) (*Service, *events) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ev := &events{}
	return New(st, activity.New(st, ev), ev, paths.ForHome(home)), ev
}

func appCode(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestLoginFlowCreatesRemoteFromUserID(t *testing.T) {
	var polls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.php/login/v2":
			fmt.Fprintf(w, `{"poll":{"token":"tok","endpoint":"%s/login/v2/poll"},"login":"%s/login/v2/flow/x"}`, srv.URL, srv.URL)
		case "/login/v2/poll":
			if polls.Add(1) <= 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"server":"%s","loginName":"mike@example.com","appPassword":"app-pass"}`, srv.URL)
		case "/ocs/v1.php/cloud/user":
			if u, p, _ := r.BasicAuth(); u != "mike@example.com" || p != "app-pass" || r.Header.Get("OCS-APIRequest") != "true" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"ocs":{"meta":{"statuscode":100},"data":{"id":"mike id"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	LoginPollInterval = 5 * time.Millisecond
	s, ev := newService(t)
	start, err := s.NextcloudLoginStart(context.Background(), LoginStartParams{ServerURL: srv.URL, Name: "Studio Cloud"})
	if err != nil || start.LoginURL != srv.URL+"/login/v2/flow/x" {
		t.Fatalf("start: %+v %v", start, err)
	}
	got := ev.wait(t)
	if got["status"] != "ok" || got["flowId"] != start.FlowID {
		t.Fatalf("login event %v", got)
	}
	c, err := s.Get(got["connectionId"].(string))
	if err != nil || c.Name != "Studio Cloud" || c.Provider != "webdav" {
		t.Fatalf("connection %+v %v", c, err)
	}
	cfg, err := s.Config(c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["url"] != srv.URL+"/remote.php/dav/files/mike%20id" || cfg["vendor"] != "nextcloud" || cfg["user"] != "mike@example.com" {
		t.Fatalf("remote config %v", cfg)
	}
	if cfg["pass"] == "" || cfg["pass"] == "app-pass" {
		t.Fatal("app password must be stored obscured")
	}
	d := s.Describe(c)
	if d.ServerURL != srv.URL || d.Vendor != "nextcloud" || d.Parameters["pass"] != "" {
		t.Fatalf("DTO must expose the server but never the password: %+v", d)
	}
}

func TestManualLoginFailureLeavesNothingBehind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	s, _ := newService(t)
	_, err := s.NextcloudManual(context.Background(), ManualParams{ServerURL: srv.URL, User: "mike", AppPassword: "wrong"})
	if appCode(err) != "connection.testFailed" {
		t.Fatalf("got %v", err)
	}
	if cs, _ := s.List(); len(cs) != 0 {
		t.Fatalf("failed login left connections: %+v", cs)
	}
}

func TestNameValidation(t *testing.T) {
	s, _ := newService(t)
	step, err := s.Create(context.Background(), CreateParams{Name: "Disk", Provider: "local"})
	if err != nil || !step.Done {
		t.Fatalf("create local: %+v %v", step, err)
	}
	for name, code := range map[string]string{
		"disk":      "connection.nameTaken",
		"Laufwerke": "connection.nameReserved",
		"a/b":       "connection.nameReserved",
	} {
		if _, err := s.Create(context.Background(), CreateParams{Name: name, Provider: "local"}); appCode(err) != code {
			t.Errorf("%q: got %v, want %s", name, err, code)
		}
	}
	// Dependents block deletion.
	if err := s.st.InsertMount(store.Mount{ID: "m1", ConnectionID: step.ConnectionID, MountPoint: "/x", VolumeName: "X", MountType: "nfsmount", CacheMaxGB: 1}); err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), step.ConnectionID)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != "connection.inUse" || len(ae.Data["dependents"].([]Dependent)) != 1 {
		t.Fatalf("delete with dependents: %v", err)
	}
}

func TestCancelledSetupReleasesNameAndRemote(t *testing.T) {
	s, _ := newService(t)
	step, err := s.Create(context.Background(), CreateParams{Name: "Box", Provider: "dropbox"})
	if err != nil || step.Done || step.State == "" {
		t.Fatalf("expected a question step: %+v %v", step, err)
	}
	if _, err := s.Create(context.Background(), CreateParams{Name: "Box", Provider: "dropbox"}); appCode(err) != "connection.nameTaken" {
		t.Fatalf("name must be reserved while the setup runs: %v", err)
	}
	s.CancelSetup(step.ConnectionID)
	var cfg map[string]string
	_ = rcl.CallInto("config/get", map[string]any{"name": RemoteName(step.ConnectionID)}, &cfg)
	if len(cfg) != 0 {
		t.Fatalf("partial remote left in rclone.conf: %v", cfg)
	}
	if _, err := s.Create(context.Background(), CreateParams{Name: "Box", Provider: "dropbox"}); err != nil {
		t.Fatalf("name not released after cancel: %v", err)
	}
}
