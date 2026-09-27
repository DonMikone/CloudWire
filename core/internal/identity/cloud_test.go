package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rclone/rclone/fs"

	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
)

type davNode struct {
	dir      bool
	id, etag string
}

// fakeDAV answers PROPFIND Depth 1 from a tree and counts the requests per path.
type fakeDAV struct {
	mu   sync.Mutex
	tree map[string]davNode // OCS path -> node
	hits map[string]int
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/remote.php/dav/files/mike"
	p, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), prefix))
	p = "/" + strings.Trim(p, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[p]++
	node, ok := f.tree[p]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	write := func(p string, n davNode) {
		rt := ""
		if n.dir {
			rt = "<d:collection/>"
		}
		href := prefix + (&url.URL{Path: p}).EscapedPath()
		fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>"%s"</d:getetag><oc:fileid>%s</oc:fileid><d:resourcetype>%s</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
			href, n.etag, n.id, rt)
	}
	write(p, node)
	var children []string
	for c := range f.tree {
		if path.Dir(c) == p && c != p {
			children = append(children, c)
		}
	}
	slices.Sort(children)
	for _, c := range children {
		write(c, f.tree[c])
	}
	b.WriteString(`</d:multistatus>`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(b.String()))
}

func (f *fakeDAV) take() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hits
	f.hits = map[string]int{}
	return h
}

func TestNextcloudScanFollowsETags(t *testing.T) {
	dav := &fakeDAV{hits: map[string]int{}, tree: map[string]davNode{
		"/Musik":               {true, "1", "r1"},
		"/Musik/Tree":          {true, "2", "t1"},
		"/Musik/Tree/B":        {true, "3", "b1"},
		"/Musik/Tree/B/x.wav":  {false, "4", "x1"},
		"/Musik/Tree/C":        {true, "5", "c1"},
		"/Musik/Tree/C/z.wav":  {false, "6", "z1"},
		"/Musik/Tree/Über uns": {true, "7", "a1"},
		"/Musik/Other":         {true, "8", "o1"},
	}}
	srv := httptest.NewServer(dav)
	defer srv.Close()
	c := &nextcloud.Client{Base: srv.URL, User: "mike", Pass: "p", DAVURL: srv.URL + "/remote.php/dav/files/mike", HTTP: srv.Client()}
	cloud := NewNextcloud(Remote{}, c, "", "Musik")
	files := []string{"Tree/B", "Tree/C"}
	ctx := context.Background()

	got, root, err := cloud.Scan(ctx, files, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]CloudEntry{
		"Tree": {Dir: true, ID: "2", ETag: "t1"}, "Other": {Dir: true, ID: "8", ETag: "o1"},
		"Tree/B": {Dir: true, ID: "3", ETag: "b1"}, "Tree/B/x.wav": {ID: "4", ETag: "x1"},
		"Tree/C": {Dir: true, ID: "5", ETag: "c1"}, "Tree/C/z.wav": {ID: "6", ETag: "z1"},
		"Tree/Über uns": {Dir: true, ID: "7", ETag: "a1"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) || root.ID != "1" || root.ETag != "r1" {
		t.Fatalf("scan %v root %+v", got, root)
	}
	if h := dav.take(); len(h) != 4 || h["/Musik/Tree/Über uns"] != 0 || h["/Musik/Other"] != 0 {
		t.Fatalf("first scan requests %v", h)
	}
	prev := Snapshot{Provider: ProviderNextcloud, Files: files, RootCloud: root, Cloud: got}

	// Nothing changed: the root's ETag answers for everything.
	if again, _, err := cloud.Scan(ctx, files, prev); err != nil || fmt.Sprint(again) != fmt.Sprint(want) {
		t.Fatalf("unchanged scan %v %v", again, err)
	}
	if h := dav.take(); len(h) != 1 {
		t.Fatalf("unchanged scan requests %v", h)
	}

	// z.wav changed: B keeps its subtree without a request.
	dav.tree["/Musik"] = davNode{true, "1", "r2"}
	dav.tree["/Musik/Tree"] = davNode{true, "2", "t2"}
	dav.tree["/Musik/Tree/C"] = davNode{true, "5", "c2"}
	dav.tree["/Musik/Tree/C/z.wav"] = davNode{false, "6", "z2"}
	changed, _, err := cloud.Scan(ctx, files, prev)
	if err != nil || changed["Tree/C/z.wav"].ETag != "z2" || changed["Tree/B/x.wav"].ID != "4" {
		t.Fatalf("changed scan %v %v", changed, err)
	}
	if h := dav.take(); h["/Musik/Tree/B"] != 0 || h["/Musik/Tree/C"] != 1 {
		t.Fatalf("changed scan requests %v", h)
	}

	// A renamed root is gone, and found again by its id.
	dav.tree = map[string]davNode{"/": {true, "0", "e"}, "/000 - Musik": {true, "1", "r2"}}
	if _, _, err := cloud.Scan(ctx, files, prev); !errors.Is(err, fs.ErrorDirNotFound) {
		t.Fatalf("missing root: %v", err)
	}
	if p, err := cloud.Find(ctx, "", "1"); err != nil || p != "000 - Musik" {
		t.Fatalf("find %q %v", p, err)
	}
}
