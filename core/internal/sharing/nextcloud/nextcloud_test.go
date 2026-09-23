package nextcloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDAVLocation(t *testing.T) {
	cases := []struct{ in, base, root string }{
		{"https://cloud.example.com/remote.php/dav/files/mike/Music", "https://cloud.example.com", "/Music"},
		{"https://cloud.example.com/nc/remote.php/dav/files/mike%40home/A%20B/", "https://cloud.example.com/nc", "/A B"},
		{"https://cloud.example.com/remote.php/webdav/", "https://cloud.example.com", ""},
		{"http://localhost:8089/remote.php/dav/files/admin", "http://localhost:8089", ""},
	}
	for _, c := range cases {
		base, root, err := DAVLocation(c.in)
		if err != nil || base != c.base || root != c.root {
			t.Errorf("DAVLocation(%q) = %q, %q, %v; want %q, %q", c.in, base, root, err, c.base, c.root)
		}
	}
	if _, _, err := DAVLocation("https://example.com/dav"); err == nil {
		t.Error("non-Nextcloud URL accepted")
	}
	if got := JoinPath("/Music", "Loops/Drums"); got != "/Music/Loops/Drums" {
		t.Errorf("JoinPath = %q", got)
	}
	if got := JoinPath("", ""); got != "/" {
		t.Errorf("JoinPath root = %q", got)
	}
}

func TestNormalizeServerURL(t *testing.T) {
	for in, want := range map[string]string{
		"cloud.example.com":           "https://cloud.example.com",
		"https://cloud.example.com//": "https://cloud.example.com",
		"http://localhost:8089/":      "http://localhost:8089",
	} {
		if got, err := NormalizeServerURL(in); err != nil || got != want {
			t.Errorf("NormalizeServerURL(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := NormalizeServerURL("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func ocsJSON(w http.ResponseWriter, status int, statuscode int, msg, data string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"ocs":{"meta":{"status":"x","statuscode":%d,"message":%q},"data":%s}}`, statuscode, msg, data)
}

func newClient(srv *httptest.Server) *Client {
	return &Client{Base: srv.URL, User: "mike", Pass: "app-pass", DAVURL: srv.URL + "/remote.php/dav/files/mike/Music", HTTP: srv.Client()}
}

func TestCreatePublicLinkWithPasswordAndExpiry(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("OCS-APIRequest") != "true" {
			t.Error("missing OCS-APIRequest header")
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "mike" || p != "app-pass" {
			t.Error("missing basic auth")
		}
		if r.Method != http.MethodPost || r.URL.Path != sharesPath || r.URL.Query().Get("format") != "json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = r.ParseForm()
		form = r.PostForm
		ocsJSON(w, 200, 200, "OK", `{"id":"42","share_type":3,"permissions":1,"path":"/Music/Mix.wav","item_type":"file",
"token":"AbC123","url":"`+"http://x/s/AbC123"+`","expiration":"2026-10-01 00:00:00","password":"hashed","hide_download":1,"label":"Mix","note":"","stime":1790000000}`)
	}))
	defer srv.Close()
	s, err := newClient(srv).CreateShare(context.Background(), CreateParams{
		Path: "/Music/Mix.wav", ShareType: ShareTypeLink, Permissions: PermRead, Password: "Secret-123",
		ExpireDate: "2026-10-01", HideDownload: true, Label: "Mix",
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"path": "/Music/Mix.wav", "shareType": "3", "permissions": "1",
		"password": "Secret-123", "expireDate": "2026-10-01", "label": "Mix", "hideDownload": "true"} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if !strings.Contains(form.Get("attributes"), `"key":"download"`) || !strings.Contains(form.Get("attributes"), `"value":false`) {
		t.Errorf("hide-download attribute missing: %q", form.Get("attributes"))
	}
	if s.ID != "42" || s.Kind != "publicLink" || !s.HasPassword || s.ExpireDate == nil || *s.ExpireDate != "2026-10-01" || !s.HideDownload {
		t.Fatalf("normalised share wrong: %+v", s)
	}
}

func TestListSharesParsesKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "/Music" {
			t.Errorf("path filter missing: %s", r.URL)
		}
		ocsJSON(w, 200, 200, "OK", `[
{"id":1,"share_type":0,"permissions":"19","path":"/Music","item_type":"folder","share_with":"bob","share_with_displayname":"Bob","expiration":null},
{"id":"2","share_type":3,"permissions":1,"path":"/Music","item_type":"folder","token":"tok","password":null},
{"id":"3","share_type":4,"permissions":1,"path":"/Music","item_type":"folder","share_with":"a@b.c"}]`)
	}))
	defer srv.Close()
	got, err := newClient(srv).ListShares(context.Background(), "/Music")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Kind != "user" || got[0].Permissions != 19 || got[0].ShareWithDisplayName != "Bob" ||
		got[1].Kind != "publicLink" || got[1].URL != srv.URL+"/s/tok" || got[1].HasPassword || got[2].Kind != "email" {
		t.Fatalf("unexpected shares: %+v", got)
	}
}

func TestPolicyErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ocsJSON(w, 403, 403, "Passwords are enforced for link and mail shares", `[]`)
	}))
	defer srv.Close()
	_, err := newClient(srv).CreateShare(context.Background(), CreateParams{Path: "/x", ShareType: ShareTypeLink})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected policy error, got %v", err)
	}
	if IsPolicyError(&OCSError{Message: "Wrong path, file/folder does not exist"}) {
		t.Fatal("non-policy error classified as policy")
	}
}

func TestPolicyFromCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ocsJSON(w, 200, 100, "OK", `{"capabilities":{"files_sharing":{"public":{"enabled":true,"password":{"enforced":true},"expire_date":{"enabled":true,"enforced":true,"days":"7"}}}}}`)
	}))
	defer srv.Close()
	p, err := newClient(srv).Policy(context.Background())
	if err != nil || !p.PasswordEnforced || !p.ExpireDateEnforced || p.ExpireDateDays != 7 {
		t.Fatalf("policy %+v %v", p, err)
	}
}

func TestSearchSharees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "bo" || r.URL.Query().Get("itemType") != "folder" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		ocsJSON(w, 200, 100, "OK", `{"exact":{"users":[{"label":"Bob","value":{"shareType":0,"shareWith":"bob"}}],"groups":[],"emails":[]},
"users":[{"label":"Bob","value":{"shareType":0,"shareWith":"bob"}},{"label":"Bobby","value":{"shareType":0,"shareWith":"bobby"}}],
"groups":[{"label":"Band","value":{"shareType":1,"shareWith":"band"}}],"emails":[]}`)
	}))
	defer srv.Close()
	got, err := newClient(srv).SearchSharees(context.Background(), "bo", "folder")
	if err != nil || len(got) != 3 || got[0].ShareWith != "bob" || got[2].ShareType != 1 {
		t.Fatalf("sharees %+v %v", got, err)
	}
}

func TestInternalLinkViaPropfind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "0" || !strings.Contains(string(body), "<oc:fileid/>") {
			t.Errorf("unexpected %s depth=%s body=%s", r.Method, r.Header.Get("Depth"), body)
		}
		if r.URL.EscapedPath() != "/remote.php/dav/files/mike/Music/A%20B.wav" {
			t.Errorf("path %s", r.URL.EscapedPath())
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>/x</d:href><d:propstat><d:prop><oc:fileid>1234</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer srv.Close()
	u, err := newClient(srv).InternalLink(context.Background(), "/Music/A B.wav")
	if err != nil || u != srv.URL+"/f/1234" {
		t.Fatalf("internal link %q %v", u, err)
	}
}

func TestLoginFlowPollsUntilGranted(t *testing.T) {
	var polls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.php/login/v2":
			if r.Header.Get("User-Agent") != UserAgent {
				t.Error("user agent missing")
			}
			fmt.Fprintf(w, `{"poll":{"token":"tok","endpoint":"%s/login/v2/poll"},"login":"%s/login/v2/flow/abc"}`, srv.URL, srv.URL)
		case "/login/v2/poll":
			_ = r.ParseForm()
			if r.PostForm.Get("token") != "tok" {
				t.Error("token not posted")
			}
			if polls.Add(1) < 3 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"server":"%s","loginName":"mike","appPassword":"app-pass"}`, srv.URL)
		}
	}))
	defer srv.Close()
	f, err := StartLogin(context.Background(), srv.Client(), srv.URL)
	if err != nil || f.LoginURL != srv.URL+"/login/v2/flow/abc" {
		t.Fatalf("start: %+v %v", f, err)
	}
	c, err := PollLogin(context.Background(), srv.Client(), f, 5*time.Millisecond, 5*time.Second)
	if err != nil || c.LoginName != "mike" || c.AppPassword != "app-pass" || c.Server != srv.URL || polls.Load() != 3 {
		t.Fatalf("poll: %+v %v polls=%d", c, err, polls.Load())
	}
}

func TestLoginFlowTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	f := Flow{Token: "t", Endpoint: srv.URL + "/poll", Server: srv.URL}
	_, err := PollLogin(context.Background(), srv.Client(), f, 5*time.Millisecond, 30*time.Millisecond)
	if !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestLoginRejectsForeignInsecureURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"poll":{"token":"tok","endpoint":"http://evil.example/poll"},"login":"http://evil.example/login"}`)
	}))
	defer srv.Close()
	if _, err := StartLogin(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("insecure foreign login URL accepted")
	}
}

func TestLoginNeverDowngradesHTTPS(t *testing.T) {
	got, err := sameHostOrHTTPS("http://cloud.example.com/login/v2/poll", "https://cloud.example.com")
	if err != nil || got != "https://cloud.example.com/login/v2/poll" {
		t.Fatalf("same-host http under an https server must be upgraded: %q %v", got, err)
	}
	if got, err := sameHostOrHTTPS("http://localhost:8089/poll", "http://localhost:8089"); err != nil || got != "http://localhost:8089/poll" {
		t.Fatalf("http server keeps http: %q %v", got, err)
	}
	if _, err := sameHostOrHTTPS("http://other.example/poll", "https://cloud.example.com"); err == nil {
		t.Fatal("foreign http host accepted")
	}
	if u, _ := NormalizeServerURL("https://mike:secret@cloud.example.com/"); strings.Contains(u, "secret") {
		t.Fatalf("credentials kept in server URL: %q", u)
	}
}

func TestPollKeepsEnteredServerForForeignHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"server":"https://evil.example","loginName":"mike","appPassword":"p"}`)
	}))
	defer srv.Close()
	c, err := PollLogin(context.Background(), srv.Client(), Flow{Token: "t", Endpoint: srv.URL + "/poll", Server: srv.URL}, time.Millisecond, time.Second)
	if err != nil || c.Server != srv.URL {
		t.Fatalf("server %q %v; the app password must only go to the entered host", c.Server, err)
	}
}
