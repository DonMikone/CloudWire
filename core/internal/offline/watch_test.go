package offline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// etagDAV answers PROPFIND Depth 0 and 1 from a map of OCS path -> ETag
// (folders end in "/") and counts the requests per path.
type etagDAV struct {
	mu   sync.Mutex
	tree map[string]string
	hits map[string]int
	fail bool
}

func (f *etagDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/remote.php/dav/files/mike"
	p, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), prefix))
	p = "/" + strings.Trim(p, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[p]++
	if f.fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	etag, dir, ok := f.lookup(p)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	write := func(p, etag string, dir bool) {
		rt := ""
		if dir {
			rt = "<d:collection/>"
		}
		fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>"%s"</d:getetag><d:resourcetype>%s</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
			prefix+(&url.URL{Path: p}).EscapedPath(), etag, rt)
	}
	write(p, etag, dir)
	if r.Header.Get("Depth") == "1" {
		var children []string
		for k := range f.tree {
			if c := strings.TrimSuffix(k, "/"); path.Dir(c) == p && c != p {
				children = append(children, k)
			}
		}
		slices.Sort(children)
		for _, k := range children {
			write(strings.TrimSuffix(k, "/"), f.tree[k], strings.HasSuffix(k, "/"))
		}
	}
	b.WriteString(`</d:multistatus>`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(b.String()))
}

func (f *etagDAV) lookup(p string) (etag string, dir, ok bool) {
	if etag, ok := f.tree[p+"/"]; ok {
		return etag, true, true
	}
	etag, ok = f.tree[p]
	return etag, false, ok
}

func (f *etagDAV) set(p, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tree[p] = etag
}

func (f *etagDAV) take() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hits
	f.hits = map[string]int{}
	return h
}

func newEtagDAV(t *testing.T, tree map[string]string) (*etagDAV, *nextcloud.Client) {
	t.Helper()
	dav := &etagDAV{tree: tree, hits: map[string]int{}}
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)
	return dav, &nextcloud.Client{Base: srv.URL, User: "mike", Pass: "p", DAVURL: srv.URL + "/remote.php/dav/files/mike", HTTP: srv.Client()}
}

// A files item at the cloud root (like "Presets" + "Sample Selection" on the
// Mac Studio) must not be triggered by changes elsewhere under the same root,
// e.g. uploads of another Offline Item: the root ETag changes, the Selection
// digest does not.
func TestSelectionETagIgnoresChangesOutsideSelection(t *testing.T) {
	dav, c := newEtagDAV(t, map[string]string{
		"/":                         "r1",
		"/Presets/":                 "p1",
		"/Sample Selection/":        "s1",
		"/Musikprojekte/":           "m1",
		"/Musikprojekte/Song/":      "g1",
		"/Musikprojekte/Song/a.wav": "a1",
	})
	it := store.OfflineItem{Kind: "files", Files: []string{"Presets", "Sample Selection"}}
	ctx := context.Background()
	before, err := itemETag(ctx, c, "/", it)
	if err != nil {
		t.Fatal(err)
	}
	if h := dav.take(); len(h) != 1 || h["/"] != 1 {
		t.Fatalf("one PROPFIND per parent folder, got %v", h)
	}

	// Another item uploads below Musikprojekte: ETags propagate up to the root.
	dav.set("/Musikprojekte/Song/a.wav", "a2")
	dav.set("/Musikprojekte/Song/", "g2")
	dav.set("/Musikprojekte/", "m2")
	dav.set("/", "r2")
	if after, err := itemETag(ctx, c, "/", it); err != nil || after != before {
		t.Fatalf("change outside the Selection moved the digest: %q -> %q (%v)", before, after, err)
	}

	// A change inside a selected folder does.
	dav.set("/Sample Selection/", "s2")
	dav.set("/", "r3")
	if after, err := itemETag(ctx, c, "/", it); err != nil || after == before {
		t.Fatalf("change inside the Selection kept the digest %q (%v)", after, err)
	}
}

func TestSelectionETagNestedEntriesAndNormalization(t *testing.T) {
	dav, c := newEtagDAV(t, map[string]string{
		"/Nextcloud/":                      "r1",
		"/Nextcloud/Musik/":                "m1",
		"/Nextcloud/Musik/H\u00f6hner/":    "h1",
		"/Nextcloud/Musik/Other/":          "o1",
		"/Nextcloud/Musik/Projekt/":        "p1",
		"/Nextcloud/Musik/Projekt/Mix.wav": "x1",
		"/Nextcloud/Musik/Projekt/Stems/":  "t1",
	})
	// The Selection spells the folder NFD (as Finder does); the cloud has NFC.
	it := store.OfflineItem{Kind: "files", Files: []string{"Musik/Ho\u0308hner", "Musik/Projekt/Mix.wav"}}
	ctx := context.Background()
	base, err := itemETag(ctx, c, "/Nextcloud", it)
	if err != nil {
		t.Fatal(err)
	}
	if h := dav.take(); len(h) != 2 || h["/Nextcloud/Musik"] != 1 || h["/Nextcloud/Musik/Projekt"] != 1 {
		t.Fatalf("requests %v", h)
	}
	// A sibling of a selected file is not part of the Selection.
	dav.set("/Nextcloud/Musik/Projekt/Stems/", "t2")
	dav.set("/Nextcloud/Musik/Projekt/", "p2")
	if got, _ := itemETag(ctx, c, "/Nextcloud", it); got != base {
		t.Fatal("change of an unselected sibling moved the digest")
	}
	// The NFD Selection entry found its NFC twin: a change there counts.
	dav.set("/Nextcloud/Musik/H\u00f6hner/", "h2")
	changed, _ := itemETag(ctx, c, "/Nextcloud", it)
	if changed == base {
		t.Fatal("change of the NFC twin of an NFD Selection entry was missed")
	}
	// A selected file deleted in the cloud changes the digest, and the
	// result is stable while it stays deleted.
	dav.mu.Lock()
	delete(dav.tree, "/Nextcloud/Musik/Projekt/Mix.wav")
	dav.mu.Unlock()
	gone, err := itemETag(ctx, c, "/Nextcloud", it)
	if err != nil || gone == changed {
		t.Fatalf("deleted entry: %q %v", gone, err)
	}
	if again, _ := itemETag(ctx, c, "/Nextcloud", it); again != gone {
		t.Fatal("digest of a deleted entry is not stable")
	}
	// A deleted parent folder is a state, not an error.
	dav.mu.Lock()
	delete(dav.tree, "/Nextcloud/Musik/Projekt/")
	delete(dav.tree, "/Nextcloud/Musik/Projekt/Stems/")
	dav.mu.Unlock()
	if noParent, err := itemETag(ctx, c, "/Nextcloud", it); err != nil || noParent != gone {
		// Mix.wav is missing either way; the parent's absence alone adds nothing.
		t.Fatalf("deleted parent: %q %v", noParent, err)
	}
}

func TestItemETagFolderItemAndErrors(t *testing.T) {
	dav, c := newEtagDAV(t, map[string]string{
		"/Musik/":      "m1",
		"/Musik/a.wav": "",
	})
	ctx := context.Background()
	if got, err := itemETag(ctx, c, "/Musik", store.OfflineItem{Kind: "folder"}); err != nil || got != "m1" {
		t.Fatalf("folder item: %q %v", got, err)
	}
	// An entry without ETag cannot be tracked: fall back to the root ETag.
	if got, err := itemETag(ctx, c, "/Musik", store.OfflineItem{Kind: "files", Files: []string{"a.wav"}}); err != nil || got != "m1" {
		t.Fatalf("entry without ETag: %q %v", got, err)
	}
	// A server error is not a change.
	dav.mu.Lock()
	dav.fail = true
	dav.mu.Unlock()
	if _, err := itemETag(ctx, c, "/Musik", store.OfflineItem{Kind: "files", Files: []string{"a.wav"}}); err == nil {
		t.Fatal("server error must be reported, not digested")
	}
}
