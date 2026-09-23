package nextcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Flow is a started Login Flow v2.
type Flow struct {
	LoginURL string
	Token    string
	Endpoint string
	Server   string // normalised server URL the user entered
}

// Credentials are the result of a successful login.
type Credentials struct {
	Server      string `json:"server"`
	LoginName   string `json:"loginName"`
	AppPassword string `json:"appPassword"`
}

// ErrLoginTimeout is returned when the user did not finish the login in time.
var ErrLoginTimeout = errors.New("timeout")

// NoLoginFlowError is returned when the server does not offer Login Flow v2,
// usually because the address is not a Nextcloud server.
type NoLoginFlowError struct{ Status int }

func (e NoLoginFlowError) Error() string {
	return fmt.Sprintf("login flow not available (HTTP %d) - is this a Nextcloud server?", e.Status)
}

// sameHostOrHTTPS accepts https URLs, and http URLs on the server's host when
// the server itself was entered as http. For an https server, a same-host http
// URL (a proxy without overwriteprotocol) is upgraded to https: credentials
// never travel in cleartext when the user asked for https.
func sameHostOrHTTPS(raw, server string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", raw)
	}
	s, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	u.User = nil
	switch {
	case u.Scheme == "https":
		return u.String(), nil
	case u.Scheme == "http" && strings.EqualFold(u.Host, s.Host):
		if s.Scheme == "https" {
			u.Scheme = "https"
		}
		return u.String(), nil
	}
	return "", fmt.Errorf("refusing insecure URL %q", raw)
}

// sameHost reports whether two URLs name the same host (and port).
func sameHost(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && strings.EqualFold(ua.Host, ub.Host)
}

// StartLogin begins Login Flow v2 at serverURL (already normalised).
func StartLogin(ctx context.Context, hc *http.Client, serverURL string) (Flow, error) {
	if hc == nil {
		hc = DefaultHTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/index.php/login/v2", nil)
	if err != nil {
		return Flow{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return Flow{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Flow{}, NoLoginFlowError{Status: resp.StatusCode}
	}
	var d struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return Flow{}, fmt.Errorf("unexpected login flow response: %w", err)
	}
	login, err := sameHostOrHTTPS(d.Login, serverURL)
	if err != nil {
		return Flow{}, err
	}
	endpoint, err := sameHostOrHTTPS(d.Poll.Endpoint, serverURL)
	if err != nil {
		return Flow{}, err
	}
	if d.Poll.Token == "" {
		return Flow{}, errors.New("login flow returned no token")
	}
	return Flow{LoginURL: login, Token: d.Poll.Token, Endpoint: endpoint, Server: serverURL}, nil
}

// PollLogin polls until the user granted access, ctx ends, or timeout passes.
func PollLogin(ctx context.Context, hc *http.Client, f Flow, interval, timeout time.Duration) (Credentials, error) {
	if hc == nil {
		hc = DefaultHTTP
	}
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		creds, done, err := pollOnce(ctx, hc, f)
		if err != nil {
			return Credentials{}, err
		}
		if done {
			return creds, nil
		}
		if time.Now().After(deadline) {
			return Credentials{}, ErrLoginTimeout
		}
		select {
		case <-ctx.Done():
			return Credentials{}, ctx.Err()
		case <-t.C:
		}
	}
}

func pollOnce(ctx context.Context, hc *http.Client, f Flow) (Credentials, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint, strings.NewReader(url.Values{"token": {f.Token}}.Encode()))
	if err != nil {
		return Credentials{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Credentials{}, false, ctx.Err()
		}
		return Credentials{}, false, nil // transient network error: keep polling
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return Credentials{}, false, nil
	case http.StatusOK:
		var c Credentials
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
			return Credentials{}, false, fmt.Errorf("unexpected poll response: %w", err)
		}
		if c.LoginName == "" || c.AppPassword == "" {
			return Credentials{}, false, errors.New("login returned no credentials")
		}
		// Use the server's canonical URL (e.g. a sub-path) only for the host
		// the user entered, and never with a weaker scheme.
		if s, err := sameHostOrHTTPS(c.Server, f.Server); c.Server != "" && err == nil && sameHost(c.Server, f.Server) {
			c.Server = strings.TrimRight(s, "/")
		} else {
			c.Server = f.Server
		}
		return c, true, nil
	default:
		return Credentials{}, false, nil
	}
}
