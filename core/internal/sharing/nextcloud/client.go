// Package nextcloud talks to Nextcloud / ownCloud servers: Login Flow v2,
// the OCS Share API, capabilities and WebDAV properties.
package nextcloud

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// UserAgent is sent with every request.
var UserAgent = "CloudWire (macOS)"

// DefaultHTTP is the shared HTTP client.
var DefaultHTTP = &http.Client{Timeout: 30 * time.Second}

// NormalizeServerURL adds https:// when no scheme is given and trims trailing slashes.
func NormalizeServerURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("server URL is empty")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("server URL has no host")
	}
	// Credentials in the URL would end up in the rclone url, logs and links.
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// DAVLocation splits a Nextcloud WebDAV URL into the server base URL and the
// OCS path of the WebDAV root (URL-decoded, "" or "/sub/dir").
func DAVLocation(davURL string) (base, rootPath string, err error) {
	u, err := url.Parse(davURL)
	if err != nil {
		return "", "", err
	}
	p := u.EscapedPath()
	i, rest, ok := splitDAVPath(p)
	if i < 0 {
		return "", "", fmt.Errorf("not a Nextcloud WebDAV URL: %s", davURL)
	}
	if !ok {
		return "", "", fmt.Errorf("unsupported Nextcloud WebDAV path: %s", u.Path)
	}
	baseURL := *u
	baseURL.Path, baseURL.RawPath, baseURL.RawQuery, baseURL.Fragment = "", "", "", ""
	base = strings.TrimRight(baseURL.String(), "/") + p[:i]
	rest, err = url.PathUnescape(strings.TrimRight(rest, "/"))
	if err != nil {
		return "", "", err
	}
	return base, rest, nil
}

// splitDAVPath splits an escaped URL path at "/remote.php/": i is its index
// (-1 if missing) and rest the still escaped path below the user's files root
// (…/remote.php/dav/files/<user> or …/remote.php/webdav). ok is false for
// other endpoints.
func splitDAVPath(p string) (i int, rest string, ok bool) {
	i = strings.Index(p, "/remote.php/")
	if i < 0 {
		return -1, "", false
	}
	rest = p[i+len("/remote.php"):]
	switch {
	case strings.HasPrefix(rest, "/dav/files/"):
		rest = rest[len("/dav/files/"):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return i, rest[j:], true
		}
		return i, "", true
	case rest == "/webdav" || strings.HasPrefix(rest, "/webdav/"):
		return i, rest[len("/webdav"):], true
	}
	return i, "", false
}

// JoinPath joins the WebDAV root path and a remote path into an OCS path.
func JoinPath(rootPath, remotePath string) string {
	p := strings.Trim(rootPath, "/")
	r := strings.Trim(remotePath, "/")
	switch {
	case p == "" && r == "":
		return "/"
	case p == "":
		return "/" + r
	case r == "":
		return "/" + p
	}
	return "/" + p + "/" + r
}

// Client is an authenticated Nextcloud client.
type Client struct {
	Base string // https://cloud.example.com
	User string // login name
	Pass string // app password
	// DAVURL is the WebDAV URL of the user's root (…/remote.php/dav/files/<id>).
	DAVURL string
	HTTP   *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return DefaultHTTP
}

func (c *Client) newRequest(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	if c.User != "" {
		req.SetBasicAuth(c.User, c.Pass)
	}
	return req, nil
}

// OCSError is a failed OCS call.
type OCSError struct {
	HTTPStatus int
	StatusCode int
	Message    string
}

func (e *OCSError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("server returned HTTP %d (OCS %d)", e.HTTPStatus, e.StatusCode)
}

type ocsEnvelope struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

// ocs performs an OCS call and decodes ocs.data into out.
func (c *Client) ocs(ctx context.Context, method, path string, form url.Values, out any) error {
	u := c.Base + path
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += sep + "format=json"
	var body io.Reader
	if form != nil && method != http.MethodGet {
		body = strings.NewReader(form.Encode())
	} else if form != nil {
		u += "&" + form.Encode()
	}
	req, err := c.newRequest(ctx, method, u, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var env ocsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 400 {
			return &OCSError{HTTPStatus: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		}
		return fmt.Errorf("unexpected OCS response (HTTP %d)", resp.StatusCode)
	}
	sc := env.OCS.Meta.StatusCode
	ok := sc == 100 || sc == 200
	if !ok || resp.StatusCode >= 400 {
		return &OCSError{HTTPStatus: resp.StatusCode, StatusCode: sc, Message: env.OCS.Meta.Message}
	}
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return fmt.Errorf("decode OCS data: %w", err)
		}
	}
	return nil
}

// UserID returns the user id (which can differ from the login name).
func (c *Client) UserID(ctx context.Context) (string, error) {
	var d struct {
		ID string `json:"id"`
	}
	if err := c.ocs(ctx, http.MethodGet, "/ocs/v1.php/cloud/user", nil, &d); err != nil {
		return "", err
	}
	if d.ID == "" {
		return "", errors.New("server returned no user id")
	}
	return d.ID, nil
}

// DAVURLFor builds the WebDAV root URL for a user id.
func DAVURLFor(base, userID string) string {
	return base + "/remote.php/dav/files/" + url.PathEscape(userID)
}

// DeleteAppPassword revokes the app password used by this client.
func (c *Client) DeleteAppPassword(ctx context.Context) error {
	return c.ocs(ctx, http.MethodDelete, "/ocs/v2.php/core/apppassword", nil, nil)
}

// ---- WebDAV properties ----

// ErrNotFound is returned for a path that does not exist on the server.
var ErrNotFound = errors.New("not found")

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Prop struct {
				ETag          string `xml:"getetag"`
				FileID        string `xml:"fileid"`
				ContentLength string `xml:"getcontentlength"`
				LastModified  string `xml:"getlastmodified"`
				ResourceType  struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
			Status string `xml:"status"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// davURL returns the WebDAV URL of an OCS path below the user's files root.
func (c *Client) davURL(ocsPath string) string {
	root := c.DAVURL
	if i := strings.Index(root, "/remote.php/"); i >= 0 {
		// Always address /remote.php/dav/files/<user>, independent of the configured sub-path.
		rest := root[i+len("/remote.php/"):]
		if strings.HasPrefix(rest, "dav/files/") {
			seg := strings.SplitN(rest[len("dav/files/"):], "/", 2)[0]
			root = root[:i] + "/remote.php/dav/files/" + seg
		} else {
			root = root[:i] + "/remote.php/webdav"
		}
	}
	var parts []string
	for _, s := range strings.Split(strings.Trim(ocsPath, "/"), "/") {
		if s != "" {
			parts = append(parts, url.PathEscape(s))
		}
	}
	if len(parts) == 0 {
		return root + "/"
	}
	return root + "/" + strings.Join(parts, "/")
}

// propfind requests props of an OCS path; depth is "0" (the path only) or
// "1" (with its children, the answer size then grows with the folder).
func (c *Client) propfind(ctx context.Context, ocsPath, props, depth string) (multistatus, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop>` + props + `</d:prop></d:propfind>`
	req, err := c.newRequest(ctx, "PROPFIND", c.davURL(ocsPath), strings.NewReader(body))
	if err != nil {
		return multistatus{}, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml")
	resp, err := c.http().Do(req)
	if err != nil {
		return multistatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return multistatus{}, fmt.Errorf("PROPFIND %s: %w", ocsPath, ErrNotFound)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return multistatus{}, fmt.Errorf("PROPFIND %s: HTTP %d", ocsPath, resp.StatusCode)
	}
	var r io.Reader = resp.Body
	if depth == "0" {
		r = io.LimitReader(resp.Body, 1<<20)
	}
	var ms multistatus
	if err := xml.NewDecoder(r).Decode(&ms); err != nil {
		return ms, err
	}
	return ms, nil
}

// DAVEntry is a folder or file of a WebDAV listing.
type DAVEntry struct {
	Path     string // OCS path, URL-decoded, without trailing "/"
	Dir      bool
	FileID   string
	ETag     string
	Size     int64
	Modified time.Time
}

// ListDir lists a folder and its direct children (PROPFIND Depth 1). The
// folder itself comes first, with Path == ocsPath. ErrNotFound if it is gone.
func (c *Client) ListDir(ctx context.Context, ocsPath string) ([]DAVEntry, error) {
	ms, err := c.propfind(ctx, ocsPath,
		`<d:getetag/><oc:fileid/><d:resourcetype/><d:getcontentlength/><d:getlastmodified/>`, "1")
	if err != nil {
		return nil, err
	}
	self := strings.Trim(ocsPath, "/")
	var out []DAVEntry
	for _, r := range ms.Responses {
		p, err := hrefPath(r.Href)
		if err != nil {
			return nil, err
		}
		e := DAVEntry{Path: p}
		for _, ps := range r.Propstat {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200 ") {
				continue
			}
			pr := ps.Prop
			e.Dir = e.Dir || pr.ResourceType.Collection != nil
			if pr.FileID != "" {
				e.FileID = pr.FileID
			}
			if pr.ETag != "" {
				e.ETag = strings.Trim(pr.ETag, `"`)
			}
			if n, err := strconv.ParseInt(pr.ContentLength, 10, 64); err == nil {
				e.Size = n
			}
			if t, err := http.ParseTime(pr.LastModified); err == nil {
				e.Modified = t
			}
		}
		if strings.Trim(p, "/") == self {
			e.Path = ocsPath
			out = append([]DAVEntry{e}, out...)
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 || out[0].Path != ocsPath {
		return nil, fmt.Errorf("PROPFIND %s: folder missing in response", ocsPath)
	}
	return out, nil
}

// hrefPath maps a WebDAV href (…/remote.php/dav/files/<user>/a/b or
// …/remote.php/webdav/a/b) to its URL-decoded OCS path ("/a/b").
func hrefPath(href string) (string, error) {
	if u, err := url.Parse(href); err == nil && u.Scheme != "" {
		href = u.EscapedPath()
	}
	_, rest, ok := splitDAVPath(href)
	if !ok {
		return "", fmt.Errorf("unexpected WebDAV href %q", href)
	}
	p, err := url.PathUnescape(strings.TrimRight(rest, "/"))
	if err != nil {
		return "", err
	}
	if p == "" {
		p = "/"
	}
	return p, nil
}

// ETag returns the WebDAV ETag of an OCS path (PROPFIND Depth 0).
func (c *Client) ETag(ctx context.Context, ocsPath string) (string, error) {
	ms, err := c.propfind(ctx, ocsPath, `<d:getetag/>`, "0")
	if err != nil {
		return "", err
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			if ps.Prop.ETag != "" {
				return strings.Trim(ps.Prop.ETag, `"`), nil
			}
		}
	}
	return "", errors.New("no ETag in PROPFIND response")
}

// FileID returns the Nextcloud file id of an OCS path.
func (c *Client) FileID(ctx context.Context, ocsPath string) (string, error) {
	ms, err := c.propfind(ctx, ocsPath, `<oc:fileid/>`, "0")
	if err != nil {
		return "", err
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			if ps.Prop.FileID != "" {
				return ps.Prop.FileID, nil
			}
		}
	}
	return "", errors.New("no file id in PROPFIND response")
}

// InternalLink returns <base>/f/<fileid>.
func (c *Client) InternalLink(ctx context.Context, ocsPath string) (string, error) {
	id, err := c.FileID(ctx, ocsPath)
	if err != nil {
		return "", err
	}
	return c.Base + "/f/" + id, nil
}

// WebURL returns the browser URL of a folder or file.
func (c *Client) WebURL(ctx context.Context, ocsPath string, isDir bool) (string, error) {
	if isDir {
		return c.Base + "/apps/files/?dir=" + url.QueryEscape(ocsPath), nil
	}
	return c.InternalLink(ctx, ocsPath)
}
