package identity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/walk"

	"github.com/DonMikone/CloudWire/core/internal/selection"
	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// Cloud is the cloud side of an item: identities, lookups and moves. Paths
// are item-relative, except for the Conn* methods and Find, which work
// relative to the Connection root.
type Cloud interface {
	// Name is the provider (ProviderNextcloud, ProviderIDs).
	Name() string
	// Scan lists the synced paths with their ids: everything below a
	// selected folder and the children of partially selected folders (rename
	// targets). nil entries mean the cloud has no ids (ProviderNone). A
	// missing item root is fs.ErrorDirNotFound.
	Scan(ctx context.Context, files []string, prev Snapshot) (entries map[string]CloudEntry, root CloudEntry, err error)
	// Find returns the Connection-relative path of the folder with id in
	// dir, or "" if there is none.
	Find(ctx context.Context, dir, id string) (string, error)
	Stat(ctx context.Context, rel string) (exists, dir bool, err error)
	Move(ctx context.Context, from, to string, dir bool) error
	ConnStat(ctx context.Context, p string) (exists, dir bool, err error)
	ConnMove(ctx context.Context, from, to string) error
}

// Remote implements the lookups and moves every provider shares through
// rclone: F is the item root, Conn the Connection root.
type Remote struct {
	F, Conn fs.Fs
}

// Stat reports whether an item-relative path exists.
func (r Remote) Stat(ctx context.Context, rel string) (bool, bool, error) { return stat(ctx, r.F, rel) }

// ConnStat reports whether a Connection-relative path exists.
func (r Remote) ConnStat(ctx context.Context, p string) (bool, bool, error) {
	return stat(ctx, r.Conn, p)
}

// Move moves a folder or file server-side (rclone falls back to per-file
// moves where the backend cannot move folders).
func (r Remote) Move(ctx context.Context, from, to string, dir bool) error {
	if dir {
		return dirMove(ctx, r.F, from, to)
	}
	return operations.MoveFile(ctx, r.F, r.F, to, from)
}

// ConnMove moves a folder relative to the Connection root.
func (r Remote) ConnMove(ctx context.Context, from, to string) error {
	return dirMove(ctx, r.Conn, from, to)
}

func dirMove(ctx context.Context, f fs.Fs, from, to string) error {
	if f.Features().CaseInsensitive && strings.EqualFold(from, to) {
		return operations.DirMoveCaseInsensitive(ctx, f, from, to)
	}
	return operations.DirMove(ctx, f, from, to)
}

func stat(ctx context.Context, f fs.Fs, p string) (exists, dir bool, err error) {
	if p != "" {
		_, err := f.NewObject(ctx, p)
		switch {
		case err == nil:
			return true, false, nil
		case errors.Is(err, fs.ErrorIsDir):
			return true, true, nil
		case !errors.Is(err, fs.ErrorObjectNotFound) && !errors.Is(err, fs.ErrorNotAFile):
			return false, false, err
		}
	}
	_, err = f.List(ctx, p)
	switch {
	case err == nil:
		return true, true, nil
	case errors.Is(err, fs.ErrorDirNotFound), errors.Is(err, fs.ErrorIsFile):
		return false, false, nil
	}
	return false, false, err
}

// NewCloud builds the provider of an item: Nextcloud file ids through WebDAV
// or rclone's object ids.
func NewCloud(ctx context.Context, job *sv.IdentityJob) (Cloud, error) {
	f, err := cache.Get(ctx, job.RemoteFs)
	if err != nil && !errors.Is(err, fs.ErrorIsFile) {
		return nil, err
	}
	conn, err := cache.Get(ctx, job.ConnFs)
	if err != nil {
		return nil, err
	}
	r := Remote{F: f, Conn: conn}
	if !job.Nextcloud {
		return &idsCloud{Remote: r, rootPath: job.RemotePath}, nil
	}
	davURL, _ := config.FileGetValue(job.Remote, "url")
	user, _ := config.FileGetValue(job.Remote, "user")
	pass, _ := config.FileGetValue(job.Remote, "pass")
	if pass != "" {
		if pass, err = obscure.Reveal(pass); err != nil {
			return nil, fmt.Errorf("reveal password: %w", err)
		}
	}
	base, root, err := nextcloud.DAVLocation(davURL)
	if err != nil {
		return nil, err
	}
	c := &nextcloud.Client{Base: base, User: user, Pass: pass, DAVURL: davURL}
	return NewNextcloud(r, c, root, job.RemotePath), nil
}

// ---- Nextcloud ----

type nextcloudCloud struct {
	Remote
	c        *nextcloud.Client
	davRoot  string // OCS path of the WebDAV root
	rootPath string // item root below the Connection root
}

// NewNextcloud returns the Nextcloud provider: davRoot is the OCS path of
// the remote's WebDAV root, rootPath the item root below it.
func NewNextcloud(r Remote, c *nextcloud.Client, davRoot, rootPath string) Cloud {
	return &nextcloudCloud{Remote: r, c: c, davRoot: davRoot, rootPath: rootPath}
}

func (n *nextcloudCloud) Name() string { return ProviderNextcloud }

// ocs returns the OCS path of a Connection-relative path.
func (n *nextcloudCloud) ocs(p string) string { return nextcloud.JoinPath(n.davRoot, p) }

// Scan walks the synced folders with PROPFIND Depth 1. Nextcloud changes a
// folder's ETag whenever something below it changes, so an unchanged root
// returns the previous snapshot and an unchanged selected folder keeps its
// previous subtree without a request.
func (n *nextcloudCloud) Scan(ctx context.Context, files []string, prev Snapshot) (map[string]CloudEntry, CloudEntry, error) {
	rootOCS := n.ocs(n.rootPath)
	list, err := n.c.ListDir(ctx, rootOCS)
	if errors.Is(err, nextcloud.ErrNotFound) {
		return nil, CloudEntry{}, fmt.Errorf("%s: %w", rootOCS, fs.ErrorDirNotFound)
	}
	if err != nil {
		return nil, CloudEntry{}, err
	}
	root := CloudEntry{Dir: true, ID: list[0].FileID, ETag: list[0].ETag}
	if root.ID == "" {
		return nil, root, nil // no oc:fileid
	}
	same := prev.Provider == ProviderNextcloud && prev.RootCloud.ID == root.ID
	if same && root.ETag != "" && prev.RootCloud.ETag == root.ETag && slices.Equal(prev.Files, files) {
		out := maps.Clone(prev.Cloud)
		if out == nil {
			out = map[string]CloudEntry{}
		}
		return out, root, nil
	}
	base := strings.TrimSuffix(rootOCS, "/") + "/"
	out := map[string]CloudEntry{}
	var visit func(children []nextcloud.DAVEntry) error
	visit = func(children []nextcloud.DAVEntry) error {
		for _, c := range children {
			if !strings.HasPrefix(c.Path, base) {
				continue
			}
			rel := c.Path[len(base):]
			if c.FileID == "" {
				return errNoIDs
			}
			e := CloudEntry{Dir: c.Dir, ID: c.FileID, ETag: c.ETag}
			out[rel] = e
			if !c.Dir || !selection.Relevant(files, rel) {
				continue
			}
			// A selected folder whose ETag did not change keeps its subtree.
			if pe, ok := prev.Cloud[rel]; same && ok && pe.ID == e.ID && e.ETag != "" && pe.ETag == e.ETag &&
				selection.Covers(files, rel) && selection.Covers(prev.Files, rel) {
				for p, x := range prev.Cloud {
					if strings.HasPrefix(p, rel+"/") {
						out[p] = x
					}
				}
				continue
			}
			sub, err := n.c.ListDir(ctx, c.Path)
			if errors.Is(err, nextcloud.ErrNotFound) {
				delete(out, rel) // gone meanwhile
				continue
			}
			if err != nil {
				return err
			}
			if err := visit(sub[1:]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(list[1:]); err != nil {
		if errors.Is(err, errNoIDs) {
			return nil, root, nil
		}
		return nil, root, err
	}
	return out, root, nil
}

var errNoIDs = errors.New("no ids")

func (n *nextcloudCloud) Find(ctx context.Context, dir, id string) (string, error) {
	list, err := n.c.ListDir(ctx, n.ocs(dir))
	if err != nil {
		return "", err
	}
	for _, c := range list[1:] {
		if c.Dir && c.FileID == id {
			return selection.Join(dir, path.Base(c.Path)), nil
		}
	}
	return "", nil
}

// ---- rclone object ids ----

type idsCloud struct {
	Remote
	rootPath string
}

func (c *idsCloud) Name() string { return ProviderIDs }

// Scan lists the partially selected folders and every selected folder
// recursively. Any file without an id makes the cloud id-less.
func (c *idsCloud) Scan(ctx context.Context, files []string, _ Snapshot) (map[string]CloudEntry, CloudEntry, error) {
	root := CloudEntry{Dir: true}
	if c.rootPath != "" {
		id, err := c.dirID(ctx, c.rootPath)
		if err != nil {
			return nil, CloudEntry{}, err
		}
		root.ID = id
	}
	out, err := c.scan(ctx, files)
	if errors.Is(err, errNoIDs) {
		return nil, root, nil
	}
	if err != nil {
		return nil, CloudEntry{}, err
	}
	return out, root, nil
}

func (c *idsCloud) scan(ctx context.Context, files []string) (map[string]CloudEntry, error) {
	out := map[string]CloudEntry{}
	add := func(entries fs.DirEntries) error {
		for _, x := range entries {
			switch o := x.(type) {
			case fs.Directory:
				out[x.Remote()] = CloudEntry{Dir: true, ID: o.ID()}
			case fs.Object:
				id := ""
				if ider, ok := o.(fs.IDer); ok {
					id = ider.ID()
				}
				if id == "" {
					return errNoIDs
				}
				out[x.Remote()] = CloudEntry{ID: id}
			}
		}
		return nil
	}
	whole := slices.Contains(files, "")
	if !whole {
		for _, d := range selection.PartialFolders(files) {
			entries, err := c.F.List(ctx, d)
			if errors.Is(err, fs.ErrorDirNotFound) && d != "" {
				continue
			}
			if err == nil {
				err = add(entries)
			}
			if err != nil {
				return nil, err
			}
		}
	}
	for _, e := range files {
		if !whole && !out[e].Dir {
			continue // a file, or missing
		}
		err := walk.ListR(ctx, c.F, e, true, -1, walk.ListAll, add)
		if errors.Is(err, fs.ErrorDirNotFound) && e != "" {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// dirID returns the id of a Connection-relative folder.
func (c *idsCloud) dirID(ctx context.Context, p string) (string, error) {
	parent := path.Dir(p)
	if parent == "." {
		parent = ""
	}
	entries, err := c.Conn.List(ctx, parent)
	if err != nil {
		return "", err
	}
	for _, x := range entries {
		if d, ok := x.(fs.Directory); ok && d.Remote() == p {
			return d.ID(), nil
		}
	}
	return "", fmt.Errorf("%s: %w", p, fs.ErrorDirNotFound)
}

func (c *idsCloud) Find(ctx context.Context, dir, id string) (string, error) {
	entries, err := c.Conn.List(ctx, dir)
	if err != nil {
		return "", err
	}
	for _, x := range entries {
		if d, ok := x.(fs.Directory); ok && d.ID() != "" && d.ID() == id {
			return d.Remote(), nil
		}
	}
	return "", nil
}
