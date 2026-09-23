package connections

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// LoginStartParams are connections.nextcloudLoginStart params.
type LoginStartParams struct {
	ServerURL string `json:"serverURL"`
	Name      string `json:"name"`
	// ConnectionID signs an existing Nextcloud Connection in again (new app password) instead of
	// adding one; ServerURL and Name are then ignored.
	ConnectionID string `json:"connectionId"`
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
	var existing *store.Connection
	var server string
	if p.ConnectionID != "" {
		c, base, err := s.nextcloudConnection(p.ConnectionID)
		if err != nil {
			return LoginStart{}, err
		}
		existing, server = &c, base
	} else {
		var err error
		if server, err = nextcloud.NormalizeServerURL(p.ServerURL); err != nil {
			return LoginStart{}, api.InvalidText(msg.New("connection.invalidServerURL", "url", p.ServerURL))
		}
		if p.Name != "" {
			s.mu.Lock()
			err := s.ValidateName(p.Name, "")
			s.mu.Unlock()
			if err != nil {
				return LoginStart{}, err
			}
		}
	}
	flow, err := nextcloud.StartLogin(ctx, nil, server)
	var noFlow nextcloud.NoLoginFlowError
	if errors.As(err, &noFlow) {
		return LoginStart{}, api.Fail("connection.testFailed",
			msg.New("connection.noLoginFlow", "url", server, "detail", fmt.Sprintf("HTTP %d", noFlow.Status)))
	}
	if err != nil {
		return LoginStart{}, api.Wrap("connection.testFailed", err)
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
			t := api.TextOf(err)
			if errors.Is(err, nextcloud.ErrLoginTimeout) {
				t = msg.New("connection.loginTimedOut")
			} else if errors.Is(err, context.Canceled) {
				t = msg.New("connection.loginCancelled")
			}
			s.publishLoginError(flowID, t)
			return
		}
		var c store.Connection
		if existing != nil {
			c, err = *existing, s.renewNextcloud(pctx, *existing, creds.LoginName, creds.AppPassword)
		} else {
			c, err = s.createNextcloud(pctx, creds.Server, creds.LoginName, creds.AppPassword, p.Name)
		}
		if err != nil {
			s.publishLoginError(flowID, api.TextOf(err))
			return
		}
		s.pub.Publish("connection.nextcloudLogin", map[string]any{"flowId": flowID, "status": "ok", "connectionId": c.ID})
	}()
	return LoginStart{FlowID: flowID, LoginURL: flow.LoginURL}, nil
}

// publishLoginError ends a login flow with t.
func (s *Service) publishLoginError(flowID string, t msg.Text) {
	s.pub.Publish("connection.nextcloudLogin", map[string]any{"flowId": flowID, "status": "error",
		"error": t.Message, "errorCode": t.Code, "errorParams": t.Params})
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
		return DTO{}, api.InvalidText(msg.New("connection.invalidServerURL", "url", p.ServerURL))
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
		return store.Connection{}, api.Fail("connection.testFailed", msg.New("connection.signInFailed", "detail", err))
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
		return c, api.Wrap("connection.testFailed", err)
	}
	if err := s.st.InsertConnection(c); err != nil {
		_, _ = rcl.Call("config/delete", map[string]any{"name": c.RcloneRemote})
		return c, err
	}
	s.log.Info("connection", id, msg.New("connection.nextcloudAdded", "name", name, "server", server, "user", loginName), nil)
	s.pub.Publish("connections.changed", struct{}{})
	return c, nil
}

// nextcloudConnection returns a Nextcloud or ownCloud Connection and its server address.
func (s *Service) nextcloudConnection(id string) (store.Connection, string, error) {
	c, err := s.Get(id)
	if err != nil {
		return c, "", err
	}
	cfg, err := s.Config(c)
	if err != nil {
		return c, "", err
	}
	if c.Provider != "webdav" || (cfg["vendor"] != "nextcloud" && cfg["vendor"] != "owncloud") {
		return c, "", api.Invalid("%q is not a Nextcloud Connection", c.Name)
	}
	base, _, err := nextcloud.DAVLocation(cfg["url"])
	if err != nil {
		return c, "", api.Invalid("%q has no valid server address: %v", c.Name, err)
	}
	return c, base, nil
}

// renewNextcloud stores the app password of a new login in an existing Nextcloud Connection and
// revokes the old one. The login must be for the account the Connection already uses.
func (s *Service) renewNextcloud(ctx context.Context, c store.Connection, loginName, appPassword string) error {
	cfg, err := s.Config(c)
	if err != nil {
		return err
	}
	base, _, err := nextcloud.DAVLocation(cfg["url"])
	if err != nil {
		return api.Invalid("%q has no valid server address: %v", c.Name, err)
	}
	cl := &nextcloud.Client{Base: base, User: loginName, Pass: appPassword}
	uid, err := cl.UserID(ctx)
	if err != nil {
		return api.Fail("connection.testFailed", msg.New("connection.signInFailed", "detail", err))
	}
	if root := nextcloud.DAVURLFor(base, uid); cfg["url"] != root && !strings.HasPrefix(cfg["url"], root+"/") {
		// Another account: keep the Connection as it is and drop the unused app password.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = cl.DeleteAppPassword(ctx)
		}()
		return api.Fail("connection.testFailed", msg.New("connection.otherAccount", "user", uid, "name", c.Name))
	}
	if _, err := rcl.Call("config/update", map[string]any{"name": c.RcloneRemote,
		"parameters": map[string]any{"user": loginName, "pass": appPassword},
		"opt":        map[string]any{"nonInteractive": true, "obscure": true}}); err != nil {
		return api.Wrap("connection.testFailed", err)
	}
	rcl.ForgetRemote(c.RcloneRemote)
	if s.Mounts != nil {
		s.Mounts.RestartForConnection(c.ID)
	}
	go revokeAppPassword(cfg)
	s.log.Info("connection", c.ID, msg.New("connection.nextcloudSignedIn", "name", c.Name, "user", loginName), nil)
	s.pub.Publish("connections.changed", struct{}{})
	return nil
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
