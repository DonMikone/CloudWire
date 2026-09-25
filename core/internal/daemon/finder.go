package daemon

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/offline"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/sharing"
)

// Resolved is paths.resolve's result.
type Resolved struct {
	ConnectionID string `json:"connectionId"`
	RemotePath   string `json:"remotePath"`
	Context      string `json:"context"`
	ItemID       string `json:"itemId"`
	IsVault      bool   `json:"isVault"`
}

func joinRemote(a, b string) string {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}

// resolvePath maps a local path inside a Mount or Offline Item to its cloud
// path, choosing the longest matching root.
func (c *Core) resolvePath(local string) (Resolved, error) {
	p := filepath.Clean(local)
	if !filepath.IsAbs(p) {
		return Resolved{}, api.Invalid("localPath must be absolute")
	}
	best, bestLen := Resolved{}, -1
	ms, err := c.st.Mounts()
	if err != nil {
		return best, err
	}
	for _, m := range ms {
		if paths.IsWithin(p, m.MountPoint) && len(m.MountPoint) > bestLen {
			rel, _ := filepath.Rel(m.MountPoint, p)
			if rel == "." {
				rel = ""
			}
			best, bestLen = Resolved{ConnectionID: m.ConnectionID, RemotePath: joinRemote(m.RemotePath, filepath.ToSlash(rel)),
				Context: "mount", ItemID: m.ID}, len(m.MountPoint)
		}
	}
	items, err := c.st.OfflineItems()
	if err != nil {
		return best, err
	}
	for _, it := range items {
		if !paths.IsWithin(p, it.StoragePath) || len(it.StoragePath) <= bestLen {
			continue
		}
		rel, _ := filepath.Rel(it.StoragePath, p)
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)
		if !offline.Includes(it, rel) {
			continue // a files item owns only its Selection and the parent folders
		}
		best, bestLen = Resolved{ConnectionID: it.ConnectionID, RemotePath: joinRemote(it.RemotePath, rel),
			Context: "offline", ItemID: it.ID}, len(it.StoragePath)
	}
	if bestLen < 0 {
		return best, api.Fail("connection.notFound", msg.New("connection.pathNotFound", "path", local))
	}
	if conn, err := c.st.Connection(best.ConnectionID); err == nil {
		best.IsVault = conn.Kind == "vault"
	}
	return best, nil
}

// Root is one Finder Sync root.
type Root struct {
	Path         string               `json:"path"`
	Kind         string               `json:"kind"`
	ItemID       string               `json:"itemId"`
	ConnectionID string               `json:"connectionId"`
	RemoteRoot   string               `json:"remoteRoot"`
	IsVault      bool                 `json:"isVault"`
	OfflineKind  string               `json:"offlineKind"`
	Capabilities sharing.Capabilities `json:"capabilities"`
}

func (c *Core) capabilities(ctx context.Context, connID string) sharing.Capabilities {
	c.capMu.Lock()
	cp, ok := c.capCache[connID]
	c.capMu.Unlock()
	if ok {
		return cp
	}
	cp, err := c.shares.Capabilities(ctx, connID)
	if err != nil {
		return sharing.Capabilities{Reason: "unavailable"}
	}
	c.capMu.Lock()
	c.capCache[connID] = cp
	c.capMu.Unlock()
	return cp
}

// finderRoots lists the Mount points and Storage Locations with the
// capabilities the Finder menu needs, plus the action token.
func (c *Core) finderRoots(ctx context.Context) (any, error) {
	roots := []Root{}
	isVault := map[string]bool{}
	conns, err := c.st.Connections("")
	if err != nil {
		return nil, err
	}
	for _, cn := range conns {
		isVault[cn.ID] = cn.Kind == "vault"
	}
	ms, err := c.st.Mounts()
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		roots = append(roots, Root{Path: m.MountPoint, Kind: "mount", ItemID: m.ID, ConnectionID: m.ConnectionID,
			RemoteRoot: m.RemotePath, IsVault: isVault[m.ConnectionID], Capabilities: c.capabilities(ctx, m.ConnectionID)})
	}
	items, err := c.st.OfflineItems()
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		roots = append(roots, Root{Path: it.StoragePath, Kind: "offline", ItemID: it.ID, ConnectionID: it.ConnectionID,
			RemoteRoot: it.RemotePath, IsVault: isVault[it.ConnectionID], OfflineKind: it.Kind, Capabilities: c.capabilities(ctx, it.ConnectionID)})
	}
	return map[string]any{"token": c.token, "roots": roots}, nil
}
