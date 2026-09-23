package connections

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// LoginStartParams are connections.nextcloudLoginStart params.
type LoginStartParams struct {
	ServerURL string `json:"serverURL"`
	Name      string `json:"name"`
}

// LoginStart is connections.nextcloudLoginStart's result.
type LoginStart struct {
	FlowID   string `json:"flowId"`
	LoginURL string `json:"loginURL"`
}

// Login Flow v2 timing.
var (
	LoginPollInterval = 2 * time.Second
	LoginTimeout      = 20 * time.Minute
)

// NextcloudLoginStart starts Login Flow v2; the result arrives as event
// connection.nextcloudLogin.
func (s *Service) NextcloudLoginStart(ctx context.Context, p LoginStartParams) (LoginStart, error) {
	server, err := nextcloud.NormalizeServerURL(p.ServerURL)
	if err != nil {
		return LoginStart{}, api.Invalid("%v", err)
	}
	if p.Name != "" {
		s.mu.Lock()
		err := s.ValidateName(p.Name, "")
		s.mu.Unlock()
		if err != nil {
			return LoginStart{}, err
		}
	}
	flow, err := nextcloud.StartLogin(ctx, nil, server)
	if err != nil {
		return LoginStart{}, api.Errorf("connection.testFailed", "%v", err)
	}
	flowID := store.NewID()
	pctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.flows[flowID] = cancel
	s.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.flows, flowID)
			s.mu.Unlock()
		}()
		creds, err := nextcloud.PollLogin(pctx, nil, flow, LoginPollInterval, LoginTimeout)
		if err != nil {
			msg := err.Error()
			if errors.Is(err, nextcloud.ErrLoginTimeout) {
				msg = "timeout"
			} else if errors.Is(err, context.Canceled) {
				msg = "cancelled"
			}
			s.pub.Publish("connection.nextcloudLogin", map[string]any{"flowId": flowID, "status": "error", "error": msg})
			return
		}
		c, err := s.createNextcloud(pctx, creds.Server, creds.LoginName, creds.AppPassword, p.Name)
		if err != nil {
			s.pub.Publish("connection.nextcloudLogin", map[string]any{"flowId": flowID, "status": "error", "error": err.Error()})
			return
		}
		s.pub.Publish("connection.nextcloudLogin", map[string]any{"flowId": flowID, "status": "ok", "connectionId": c.ID})
	}()
	return LoginStart{FlowID: flowID, LoginURL: flow.LoginURL}, nil
}

// NextcloudLoginCancel stops polling a login flow.
func (s *Service) NextcloudLoginCancel(flowID string) {
	s.mu.Lock()
	cancel := s.flows[flowID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ManualParams are connections.nextcloudManual params.
type ManualParams struct {
	ServerURL   string `json:"serverURL"`
	User        string `json:"user"`
	AppPassword string `json:"appPassword"`
	Name        string `json:"name"`
}

// NextcloudManual adds a Nextcloud Connection from an app password.
func (s *Service) NextcloudManual(ctx context.Context, p ManualParams) (DTO, error) {
	server, err := nextcloud.NormalizeServerURL(p.ServerURL)
	if err != nil {
		return DTO{}, api.Invalid("%v", err)
	}
	if p.User == "" || p.AppPassword == "" {
		return DTO{}, api.Invalid("user and appPassword are required")
	}
	if p.Name != "" {
		s.mu.Lock()
		err := s.ValidateName(p.Name, "")
		s.mu.Unlock()
		if err != nil {
			return DTO{}, err
		}
	}
	c, err := s.createNextcloud(ctx, server, p.User, p.AppPassword, p.Name)
	if err != nil {
		return DTO{}, err
	}
	if _, err := testRemote(c.RcloneRemote + ":"); err != nil {
		_, _ = rcl.Call("config/delete", map[string]any{"name": c.RcloneRemote})
		rcl.ForgetRemote(c.RcloneRemote)
		_ = s.st.DeleteConnection(c.ID)
		s.pub.Publish("connections.changed", struct{}{})
		return DTO{}, err
	}
	return s.Describe(c), nil
}

// createNextcloud resolves the user id and creates the WebDAV remote.
func (s *Service) createNextcloud(ctx context.Context, server, loginName, appPassword, name string) (store.Connection, error) {
	cl := &nextcloud.Client{Base: server, User: loginName, Pass: appPassword}
	uid, err := cl.UserID(ctx)
	if err != nil {
		return store.Connection{}, api.Errorf("connection.testFailed", "Sign-in failed: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		name = defaultName(server)
		base := name
		for i := 2; s.ValidateName(name, "") != nil; i++ {
			name = fmt.Sprintf("%s %d", base, i)
		}
	} else if err := s.ValidateName(name, ""); err != nil {
		return store.Connection{}, err
	}
	id := store.NewID()
	c := store.Connection{ID: id, Name: name, Kind: "remote", RcloneRemote: RemoteName(id), Provider: "webdav", CreatedAt: store.Now()}
	if _, err := rcl.Call("config/create", map[string]any{"name": c.RcloneRemote, "type": "webdav",
		"parameters": map[string]any{"url": nextcloud.DAVURLFor(server, uid), "vendor": "nextcloud", "user": loginName, "pass": appPassword},
		"opt":        map[string]any{"obscure": true, "nonInteractive": true}}); err != nil {
		return c, api.Errorf("connection.testFailed", "%v", err)
	}
	if err := s.st.InsertConnection(c); err != nil {
		_, _ = rcl.Call("config/delete", map[string]any{"name": c.RcloneRemote})
		return c, err
	}
	s.log.Info("connection", id, fmt.Sprintf("Nextcloud connection %q added (%s as %s)", name, server, loginName), nil)
	s.pub.Publish("connections.changed", struct{}{})
	return c, nil
}

func defaultName(server string) string {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return "Nextcloud"
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasPrefix(host, "127.") {
		return "Nextcloud"
	}
	return host
}
