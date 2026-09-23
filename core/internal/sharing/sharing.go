// Package sharing implements Shares: the Nextcloud/ownCloud OCS Share API
// for those servers, rclone public links (with a local registry) otherwise.
package sharing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// Connections resolves Connections and their rclone config.
type Connections interface {
	Get(id string) (store.Connection, error)
	Config(c store.Connection) (map[string]string, error)
}

// Service implements the shares.* methods.
type Service struct {
	st    *store.Store
	log   *activity.Logger
	conns Connections
	// Now is the clock (tests).
	Now func() time.Time
}

// New creates the service.
func New(st *store.Store, log *activity.Logger, conns Connections) *Service {
	return &Service{st: st, log: log, conns: conns, Now: time.Now}
}

// Capabilities are the share features of a Connection.
type Capabilities struct {
	PublicLink   bool   `json:"publicLink"`
	InternalLink bool   `json:"internalLink"`
	UserShare    bool   `json:"userShare"`
	EmailShare   bool   `json:"emailShare"`
	WebURL       bool   `json:"webURL"`
	Manage       bool   `json:"manage"`
	Reason       string `json:"reason,omitempty"`
}

type target struct {
	conn store.Connection
	nc   *nextcloud.Client // nil for rclone links
	root string            // OCS path of the WebDAV root
}

func isNextcloud(conn store.Connection, cfg map[string]string) bool {
	return conn.Provider == "webdav" && (cfg["vendor"] == "nextcloud" || cfg["vendor"] == "owncloud")
}

func (s *Service) target(connID string) (target, error) {
	conn, err := s.conns.Get(connID)
	if err != nil {
		return target{}, err
	}
	if conn.Kind == "vault" {
		return target{conn: conn}, api.Errorf("share.unsupported", "Sharing is not available inside a Vault because its files are encrypted")
	}
	cfg, err := s.conns.Config(conn)
	if err != nil {
		return target{}, err
	}
	t := target{conn: conn}
	if isNextcloud(conn, cfg) {
		base, root, err := nextcloud.DAVLocation(cfg["url"])
		if err != nil {
			return t, api.Errorf("share.failed", "%v", err)
		}
		pass, err := obscure.Reveal(cfg["pass"])
		if err != nil {
			return t, api.Errorf("share.failed", "cannot read the app password: %v", err)
		}
		t.nc = &nextcloud.Client{Base: base, User: cfg["user"], Pass: pass, DAVURL: cfg["url"]}
		t.root = root
	}
	return t, nil
}

// Capabilities reports which share kinds a Connection supports.
func (s *Service) Capabilities(ctx context.Context, connID string) (Capabilities, error) {
	t, err := s.target(connID)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Code == "share.unsupported" {
			return Capabilities{Reason: "vault"}, nil
		}
		return Capabilities{}, err
	}
	if t.nc != nil {
		return Capabilities{PublicLink: true, InternalLink: true, UserShare: true, EmailShare: true, WebURL: true, Manage: true}, nil
	}
	var info struct {
		Features map[string]bool `json:"Features"`
	}
	if err := rcl.CallInto("operations/fsinfo", map[string]any{"fs": t.conn.RcloneRemote + ":"}, &info); err != nil {
		return Capabilities{}, api.Errorf("share.failed", "%v", err)
	}
	if info.Features["PublicLink"] {
		return Capabilities{PublicLink: true, Manage: true}, nil
	}
	return Capabilities{Reason: "unsupported"}, nil
}

func (t target) ocsPath(p string) string { return nextcloud.JoinPath(t.root, p) }

// relPath converts a server OCS path back to a Connection-relative path.
func (t target) relPath(ocs string) string {
	root := strings.TrimRight(t.root, "/")
	switch {
	case root == "":
		return strings.TrimPrefix(ocs, "/")
	case ocs == root:
		return ""
	case strings.HasPrefix(ocs, root+"/"):
		return ocs[len(root)+1:]
	}
	return ocs
}

func (s *Service) mapErr(ctx context.Context, t target, err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return err
	}
	if nextcloud.IsPolicyError(err) {
		e := api.Errorf("share.serverPolicy", "%v", err)
		if t.nc != nil {
			if pol, perr := t.nc.Policy(ctx); perr == nil {
				e = e.WithData("policy", pol)
			}
		}
		return e
	}
	return api.Errorf("share.failed", "%v", err)
}

// Share is the contract's Share object.
type Share = nextcloud.Share

// List returns the Shares of a Connection, optionally for one path.
func (s *Service) List(ctx context.Context, connID string, path *string) ([]Share, error) {
	t, err := s.target(connID)
	if err != nil {
		return nil, err
	}
	if t.nc != nil {
		ocs := ""
		if path != nil {
			ocs = t.ocsPath(*path)
		}
		shares, err := t.nc.ListShares(ctx, ocs)
		if err != nil {
			return nil, s.mapErr(ctx, t, err)
		}
		out := make([]Share, 0, len(shares))
		for _, sh := range shares {
			sh.Path = t.relPath(sh.Path)
			out = append(out, sh)
		}
		return out, nil
	}
	var p *string
	if path != nil {
		clean := strings.Trim(*path, "/")
		p = &clean
	}
	links, err := s.st.Links(connID, p)
	if err != nil {
		return nil, err
	}
	out := make([]Share, 0, len(links))
	for _, l := range links {
		out = append(out, linkShare(l))
	}
	return out, nil
}

func linkShare(l store.LinkEntry) Share {
	sh := Share{ID: l.ID, Kind: "link", Path: l.RemotePath, URL: l.URL, Permissions: nextcloud.PermRead, CreatedAt: l.CreatedAt, ItemType: "file"}
	if l.ExpiresAt != nil {
		d := time.UnixMilli(*l.ExpiresAt).Format("2006-01-02")
		sh.ExpireDate = &d
	}
	return sh
}

// CreateParams are shares.create params.
type CreateParams struct {
	ConnectionID string `json:"connectionId"`
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Password     string `json:"password"`
	ExpireDate   string `json:"expireDate"`
	Permissions  int    `json:"permissions"`
	HideDownload bool   `json:"hideDownload"`
	Label        string `json:"label"`
	Note         string `json:"note"`
	ShareWith    string `json:"shareWith"`
	SendMail     *bool  `json:"sendMail"`
}

func validDate(d string) error {
	if d == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", d); err != nil {
		return api.Invalid("expireDate must be YYYY-MM-DD")
	}
	return nil
}

// Create creates a Share.
func (s *Service) Create(ctx context.Context, p CreateParams) (Share, error) {
	t, err := s.target(p.ConnectionID)
	if err != nil {
		return Share{}, err
	}
	if err := validDate(p.ExpireDate); err != nil {
		return Share{}, err
	}
	path := strings.Trim(p.Path, "/")
	if t.nc == nil {
		if p.Kind != "publicLink" {
			return Share{}, api.Errorf("share.unsupported", "This provider only supports public links")
		}
		return s.createRcloneLink(t, path, p.ExpireDate)
	}
	cp := nextcloud.CreateParams{Path: t.ocsPath(path), Permissions: p.Permissions, Password: p.Password,
		ExpireDate: p.ExpireDate, HideDownload: p.HideDownload, Label: p.Label, Note: p.Note, ShareWith: p.ShareWith, SendMail: p.SendMail}
	switch p.Kind {
	case "publicLink":
		cp.ShareType = nextcloud.ShareTypeLink
	case "user":
		cp.ShareType = nextcloud.ShareTypeUser
	case "group":
		cp.ShareType = nextcloud.ShareTypeGroup
	case "email":
		cp.ShareType = nextcloud.ShareTypeEmail
	default:
		return Share{}, api.Invalid("unknown share kind %q", p.Kind)
	}
	if cp.ShareType != nextcloud.ShareTypeLink && p.ShareWith == "" {
		return Share{}, api.Invalid("shareWith is required")
	}
	sh, err := t.nc.CreateShare(ctx, cp)
	if err != nil {
		return Share{}, s.mapErr(ctx, t, err)
	}
	sh.Path = t.relPath(sh.Path)
	s.log.Info("share", t.conn.ID, fmt.Sprintf("Created %s share for %q", p.Kind, "/"+path), map[string]any{"shareId": sh.ID})
	return sh, nil
}

func (s *Service) createRcloneLink(t target, path, expireDate string) (Share, error) {
	in := map[string]any{"fs": t.conn.RcloneRemote + ":", "remote": path}
	var expires *int64
	if expireDate != "" {
		d, _ := time.ParseInLocation("2006-01-02", expireDate, time.Local)
		end := d.Add(24*time.Hour - time.Second)
		days := int(math.Ceil(end.Sub(s.Now()).Hours() / 24))
		if days < 1 {
			return Share{}, api.Invalid("expireDate must be in the future")
		}
		in["expire"] = fmt.Sprintf("%dd", days)
		ms := end.UnixMilli()
		expires = &ms
	}
	var res struct {
		URL string `json:"url"`
	}
	if err := rcl.CallInto("operations/publiclink", in, &res); err != nil {
		return Share{}, api.Errorf("share.failed", "%v", err)
	}
	l := store.LinkEntry{ID: store.NewID(), ConnectionID: t.conn.ID, RemotePath: path, URL: res.URL, ExpiresAt: expires, CreatedAt: store.Now()}
	if err := s.st.InsertLink(l); err != nil {
		return Share{}, err
	}
	s.log.Info("share", t.conn.ID, fmt.Sprintf("Created public link for %q", "/"+path), nil)
	return linkShare(l), nil
}

// UpdateParams are shares.update params.
type UpdateParams struct {
	ConnectionID string  `json:"connectionId"`
	ID           string  `json:"id"`
	Password     *string `json:"password"`
	ExpireDate   *string `json:"expireDate"`
	Permissions  *int    `json:"permissions"`
	HideDownload *bool   `json:"hideDownload"`
	Label        *string `json:"label"`
	Note         *string `json:"note"`
}

// Update changes a Nextcloud share.
func (s *Service) Update(ctx context.Context, p UpdateParams) (Share, error) {
	t, err := s.target(p.ConnectionID)
	if err != nil {
		return Share{}, err
	}
	if t.nc == nil {
		return Share{}, api.Errorf("share.unsupported", "Links of this provider cannot be edited; delete and recreate it")
	}
	if p.ExpireDate != nil {
		if err := validDate(*p.ExpireDate); err != nil {
			return Share{}, err
		}
	}
	sh, err := t.nc.UpdateShare(ctx, p.ID, nextcloud.UpdateParams{Password: p.Password, ExpireDate: p.ExpireDate,
		Permissions: p.Permissions, HideDownload: p.HideDownload, Label: p.Label, Note: p.Note})
	if err != nil {
		return Share{}, s.mapErr(ctx, t, err)
	}
	sh.Path = t.relPath(sh.Path)
	s.log.Info("share", t.conn.ID, fmt.Sprintf("Updated share %s", p.ID), nil)
	return sh, nil
}

// Delete removes a Share.
func (s *Service) Delete(ctx context.Context, connID, id string) error {
	t, err := s.target(connID)
	if err != nil {
		return err
	}
	if t.nc != nil {
		if err := t.nc.DeleteShare(ctx, id); err != nil {
			return s.mapErr(ctx, t, err)
		}
		s.log.Info("share", connID, fmt.Sprintf("Deleted share %s", id), nil)
		return nil
	}
	links, err := s.st.Links(connID, nil)
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.ID == id {
			// rclone cannot unlink on every backend; the registry entry goes regardless.
			_, _ = rcl.Call("operations/publiclink", map[string]any{"fs": t.conn.RcloneRemote + ":", "remote": l.RemotePath, "unlink": true})
			if err := s.st.DeleteLink(id); err != nil {
				return err
			}
			s.log.Info("share", connID, fmt.Sprintf("Deleted public link for %q", "/"+l.RemotePath), nil)
			return nil
		}
	}
	return api.Errorf("share.notFound", "Share %s not found", id)
}

// SearchSharees searches users, groups and email addresses.
func (s *Service) SearchSharees(ctx context.Context, connID, search, itemType string) ([]nextcloud.Sharee, error) {
	t, err := s.target(connID)
	if err != nil {
		return nil, err
	}
	if t.nc == nil {
		return nil, api.Errorf("share.unsupported", "Sharing with people is only available for Nextcloud and ownCloud")
	}
	if itemType != "file" {
		itemType = "folder"
	}
	res, err := t.nc.SearchSharees(ctx, search, itemType)
	if err != nil {
		return nil, s.mapErr(ctx, t, err)
	}
	return res, nil
}

// InternalLink returns the Nextcloud internal link (/f/<fileid>).
func (s *Service) InternalLink(ctx context.Context, connID, path string) (string, error) {
	t, err := s.target(connID)
	if err != nil {
		return "", err
	}
	if t.nc == nil {
		return "", api.Errorf("share.unsupported", "Internal links are only available for Nextcloud and ownCloud")
	}
	u, err := t.nc.InternalLink(ctx, t.ocsPath(path))
	return u, s.mapErr(ctx, t, err)
}

// WebURL returns the browser URL of a file or folder.
func (s *Service) WebURL(ctx context.Context, connID, path string) (string, error) {
	t, err := s.target(connID)
	if err != nil {
		return "", err
	}
	if t.nc == nil {
		return "", api.Errorf("share.unsupported", "Opening in the browser is only available for Nextcloud and ownCloud")
	}
	var st struct {
		Item *struct {
			IsDir bool `json:"IsDir"`
		} `json:"item"`
	}
	path = strings.Trim(path, "/")
	isDir := path == ""
	if !isDir {
		if err := rcl.CallInto("operations/stat", map[string]any{"fs": t.conn.RcloneRemote + ":", "remote": path}, &st); err == nil && st.Item != nil {
			isDir = st.Item.IsDir
		}
	}
	u, err := t.nc.WebURL(ctx, t.ocsPath(path), isDir)
	return u, s.mapErr(ctx, t, err)
}

// Policy returns the server's public-link policy.
func (s *Service) Policy(ctx context.Context, connID string) (nextcloud.Policy, error) {
	t, err := s.target(connID)
	if err != nil {
		return nextcloud.Policy{}, err
	}
	if t.nc == nil {
		return nextcloud.Policy{}, nil
	}
	pol, err := t.nc.Policy(ctx)
	return pol, s.mapErr(ctx, t, err)
}

// CopyLinkResult is shares.copyPublicLink's result.
type CopyLinkResult struct {
	URL     string `json:"url"`
	Created bool   `json:"created"`
}

// CopyPublicLink reuses the public link of path or creates one with the
// defaults. An enforced password yields share.serverPolicy (the App then
// opens the share window).
func (s *Service) CopyPublicLink(ctx context.Context, connID, path string) (CopyLinkResult, error) {
	t, err := s.target(connID)
	if err != nil {
		return CopyLinkResult{}, err
	}
	path = strings.Trim(path, "/")
	if t.nc == nil {
		links, err := s.st.Links(connID, &path)
		if err != nil {
			return CopyLinkResult{}, err
		}
		now := s.Now().UnixMilli()
		for _, l := range links {
			if l.ExpiresAt == nil || *l.ExpiresAt > now {
				return CopyLinkResult{URL: l.URL}, nil
			}
		}
		sh, err := s.createRcloneLink(t, path, "")
		if err != nil {
			return CopyLinkResult{}, err
		}
		return CopyLinkResult{URL: sh.URL, Created: true}, nil
	}
	shares, err := t.nc.ListShares(ctx, t.ocsPath(path))
	if err != nil {
		return CopyLinkResult{}, s.mapErr(ctx, t, err)
	}
	for _, sh := range shares {
		if sh.Kind == "publicLink" && sh.URL != "" {
			return CopyLinkResult{URL: sh.URL}, nil
		}
	}
	pol, err := t.nc.Policy(ctx)
	if err != nil {
		return CopyLinkResult{}, s.mapErr(ctx, t, err)
	}
	if pol.PasswordEnforced {
		return CopyLinkResult{}, api.Errorf("share.serverPolicy", "The server requires a password for public links").WithData("policy", pol)
	}
	cp := nextcloud.CreateParams{Path: t.ocsPath(path), ShareType: nextcloud.ShareTypeLink, Permissions: nextcloud.PermRead}
	if pol.ExpireDateEnforced && pol.ExpireDateDays > 0 {
		cp.ExpireDate = s.Now().AddDate(0, 0, pol.ExpireDateDays).Format("2006-01-02")
	}
	sh, err := t.nc.CreateShare(ctx, cp)
	if err != nil {
		return CopyLinkResult{}, s.mapErr(ctx, t, err)
	}
	s.log.Info("share", connID, fmt.Sprintf("Created public link for %q", "/"+path), map[string]any{"shareId": sh.ID})
	return CopyLinkResult{URL: sh.URL, Created: true}, nil
}
