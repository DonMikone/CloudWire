package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/buildinfo"
	"github.com/DonMikone/CloudWire/core/internal/connections"
	"github.com/DonMikone/CloudWire/core/internal/mounts"
	"github.com/DonMikone/CloudWire/core/internal/offline"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/sharing"
	"github.com/DonMikone/CloudWire/core/internal/store"
	"github.com/DonMikone/CloudWire/core/internal/vault"
)

type idParam struct {
	ID string `json:"id"`
}

func requireID(id string) error {
	if id == "" {
		return api.Invalid("id is required")
	}
	return nil
}

func empty() any { return struct{}{} }

// registerMethods wires the plan Appendix A contract.
func (c *Core) registerMethods() {
	s := c.srv
	h := s.Handle

	// core
	h("core.info", api.NoParams(func(context.Context) (any, error) {
		var v struct {
			Version string `json:"version"`
		}
		_ = rcl.CallInto("core/version", nil, &v)
		return map[string]any{"version": buildinfo.Version, "apiVersion": buildinfo.APIVersion, "rcloneVersion": v.Version,
			"pid": os.Getpid(), "startedAt": c.startedAt, "appSupport": c.paths.AppSupport}, nil
	}))
	h("core.shutdown", api.NoParams(func(context.Context) (any, error) {
		c.requestShutdown()
		return empty(), nil
	}))

	// settings
	h("settings.get", api.NoParams(func(context.Context) (any, error) { return c.st.Settings() }))
	h("settings.update", api.Bind(func(_ context.Context, p struct {
		Partial map[string]any `json:"partial"`
	}) (any, error) {
		before, _ := c.st.Settings()
		st, err := c.st.UpdateSettings(p.Partial)
		if err != nil {
			return nil, api.Invalid("%v", err)
		}
		c.log.SetLevel(st.Log.Level)
		if st.Bandwidth != before.Bandwidth {
			c.offline.BandwidthChanged()
		}
		c.srv.Publish("settings.changed", st)
		return st, nil
	}))

	// providers & option metadata
	h("providers.list", api.NoParams(func(context.Context) (any, error) {
		var out json.RawMessage
		return out, rcl.CallInto("config/providers", nil, &out)
	}))
	h("options.mountInfo", api.NoParams(func(context.Context) (any, error) {
		var out json.RawMessage
		return out, rcl.CallInto("options/info", map[string]any{"blocks": "vfs,mount,nfs"}, &out)
	}))
	h("options.mainInfo", api.NoParams(func(context.Context) (any, error) {
		var out json.RawMessage
		return out, rcl.CallInto("options/info", map[string]any{"blocks": "main"}, &out)
	}))

	c.registerConnections()
	c.registerMounts()
	c.registerOffline()
	c.registerShares()
	c.registerVaults()
	c.registerMisc()
}

func (c *Core) invalidateCaps() {
	c.capMu.Lock()
	c.capCache = map[string]sharing.Capabilities{}
	c.capMu.Unlock()
}

func (c *Core) registerConnections() {
	h := c.srv.Handle
	h("connections.list", api.NoParams(func(context.Context) (any, error) { return c.conns.List() }))
	h("connections.create", api.Bind(func(ctx context.Context, p connections.CreateParams) (any, error) {
		return c.conns.Create(ctx, p)
	}))
	h("connections.continue", api.Bind(func(ctx context.Context, p connections.ContinueParams) (any, error) {
		return c.conns.Continue(ctx, p)
	}))
	h("connections.cancelSetup", api.Bind(func(_ context.Context, p struct {
		ConnectionID string `json:"connectionId"`
	}) (any, error) {
		c.conns.CancelSetup(p.ConnectionID)
		return empty(), nil
	}))
	h("connections.update", api.Bind(func(ctx context.Context, p connections.UpdateParams) (any, error) {
		if err := requireID(p.ID); err != nil {
			return nil, err
		}
		c.invalidateCaps()
		return c.conns.Update(ctx, p)
	}))
	h("connections.delete", api.Bind(func(ctx context.Context, p idParam) (any, error) {
		c.invalidateCaps()
		return empty(), c.conns.Delete(ctx, p.ID)
	}))
	h("connections.test", api.Bind(func(ctx context.Context, p idParam) (any, error) { return c.conns.Test(ctx, p.ID) }))
	h("connections.browse", api.Bind(func(ctx context.Context, p struct {
		ConnectionID string `json:"connectionId"`
		Path         string `json:"path"`
	}) (any, error) {
		return c.conns.Browse(ctx, p.ConnectionID, p.Path)
	}))
	h("connections.nextcloudLoginStart", api.Bind(func(ctx context.Context, p connections.LoginStartParams) (any, error) {
		return c.conns.NextcloudLoginStart(ctx, p)
	}))
	h("connections.nextcloudLoginCancel", api.Bind(func(_ context.Context, p struct {
		FlowID string `json:"flowId"`
	}) (any, error) {
		c.conns.NextcloudLoginCancel(p.FlowID)
		return empty(), nil
	}))
	h("connections.nextcloudManual", api.Bind(func(ctx context.Context, p connections.ManualParams) (any, error) {
		return c.conns.NextcloudManual(ctx, p)
	}))
}

func (c *Core) registerMounts() {
	h := c.srv.Handle
	h("mounts.list", api.NoParams(func(context.Context) (any, error) { return c.mounts.List() }))
	h("mounts.create", api.Bind(func(ctx context.Context, p mounts.CreateParams) (any, error) { return c.mounts.Create(ctx, p) }))
	h("mounts.update", api.Bind(func(ctx context.Context, p mounts.UpdateParams) (any, error) {
		if err := requireID(p.ID); err != nil {
			return nil, err
		}
		return c.mounts.Update(ctx, p)
	}))
	h("mounts.delete", api.Bind(func(ctx context.Context, p idParam) (any, error) { return empty(), c.mounts.Delete(ctx, p.ID) }))
	h("mounts.mount", api.Bind(func(ctx context.Context, p idParam) (any, error) { return c.mounts.MountByID(ctx, p.ID) }))
	h("mounts.unmount", api.Bind(func(ctx context.Context, p idParam) (any, error) { return c.mounts.UnmountByID(ctx, p.ID) }))
	h("mounts.stats", api.Bind(func(ctx context.Context, p idParam) (any, error) { return c.mounts.Stats(ctx, p.ID) }))
	h("mounts.fuseStatus", api.NoParams(func(context.Context) (any, error) { return mounts.Fuse(), nil }))
}

func (c *Core) registerOffline() {
	h := c.srv.Handle
	h("offline.list", api.NoParams(func(context.Context) (any, error) { return c.offline.List() }))
	h("offline.preflight", api.Bind(func(ctx context.Context, p offline.PreflightParams) (any, error) {
		return c.offline.Preflight(ctx, p)
	}))
	h("offline.create", api.Bind(func(ctx context.Context, p offline.CreateParams) (any, error) {
		return c.offline.Create(ctx, p)
	}))
	h("offline.update", api.Bind(func(ctx context.Context, p offline.UpdateParams) (any, error) {
		if err := requireID(p.ID); err != nil {
			return nil, err
		}
		return c.offline.Update(ctx, p)
	}))
	h("offline.relocate", api.Bind(func(ctx context.Context, p struct {
		ID      string `json:"id"`
		NewPath string `json:"newPath"`
	}) (any, error) {
		if p.NewPath == "" {
			return nil, api.Invalid("newPath is required")
		}
		return c.offline.Relocate(ctx, p.ID, p.NewPath)
	}))
	h("offline.remove", api.Bind(func(ctx context.Context, p struct {
		ID        string `json:"id"`
		LocalCopy string `json:"localCopy"`
	}) (any, error) {
		return empty(), c.offline.Remove(ctx, p.ID, p.LocalCopy)
	}))
	h("offline.syncNow", api.Bind(func(_ context.Context, p idParam) (any, error) { return empty(), c.offline.SyncNow(p.ID) }))
	h("offline.confirmMassDelete", api.Bind(func(ctx context.Context, p struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}) (any, error) {
		return c.offline.ConfirmMassDelete(ctx, p.ID, p.Action)
	}))
	h("offline.statusForPaths", api.Bind(func(_ context.Context, p struct {
		Paths []string `json:"paths"`
	}) (any, error) {
		st, err := c.offline.StatusForPaths(p.Paths)
		return map[string]any{"statuses": st}, err
	}))
	h("offline.runs", api.Bind(func(_ context.Context, p struct {
		ItemID string `json:"itemId"`
		Limit  int    `json:"limit"`
	}) (any, error) {
		return c.offline.Runs(p.ItemID, p.Limit)
	}))
	h("offline.runFiles", api.Bind(func(_ context.Context, p struct {
		RunID int64 `json:"runId"`
	}) (any, error) {
		return c.offline.RunFiles(p.RunID)
	}))
	h("pause.status", api.NoParams(func(context.Context) (any, error) { return c.offline.PauseStatus(), nil }))
	h("pause.set", api.Bind(func(_ context.Context, p struct {
		Until      *int64 `json:"until"`
		Indefinite bool   `json:"indefinite"`
	}) (any, error) {
		return c.offline.SetPause(p.Until, p.Indefinite), nil
	}))
}

func (c *Core) registerShares() {
	h := c.srv.Handle
	type connPath struct {
		ConnectionID string `json:"connectionId"`
		Path         string `json:"path"`
	}
	h("shares.capabilities", api.Bind(func(ctx context.Context, p connPath) (any, error) {
		return c.shares.Capabilities(ctx, p.ConnectionID)
	}))
	h("shares.list", api.Bind(func(ctx context.Context, p struct {
		ConnectionID string  `json:"connectionId"`
		Path         *string `json:"path"`
	}) (any, error) {
		return c.shares.List(ctx, p.ConnectionID, p.Path)
	}))
	h("shares.create", api.Bind(func(ctx context.Context, p sharing.CreateParams) (any, error) { return c.shares.Create(ctx, p) }))
	h("shares.update", api.Bind(func(ctx context.Context, p sharing.UpdateParams) (any, error) { return c.shares.Update(ctx, p) }))
	h("shares.delete", api.Bind(func(ctx context.Context, p struct {
		ConnectionID string `json:"connectionId"`
		ID           string `json:"id"`
	}) (any, error) {
		return empty(), c.shares.Delete(ctx, p.ConnectionID, p.ID)
	}))
	h("shares.searchSharees", api.Bind(func(ctx context.Context, p struct {
		ConnectionID string `json:"connectionId"`
		Search       string `json:"search"`
		ItemType     string `json:"itemType"`
	}) (any, error) {
		return c.shares.SearchSharees(ctx, p.ConnectionID, p.Search, p.ItemType)
	}))
	h("shares.internalLink", api.Bind(func(ctx context.Context, p connPath) (any, error) {
		u, err := c.shares.InternalLink(ctx, p.ConnectionID, p.Path)
		return map[string]string{"url": u}, err
	}))
	h("shares.webURL", api.Bind(func(ctx context.Context, p connPath) (any, error) {
		u, err := c.shares.WebURL(ctx, p.ConnectionID, p.Path)
		return map[string]string{"url": u}, err
	}))
	h("shares.copyPublicLink", api.Bind(func(ctx context.Context, p connPath) (any, error) {
		return c.shares.CopyPublicLink(ctx, p.ConnectionID, p.Path)
	}))
	h("shares.policy", api.Bind(func(ctx context.Context, p connPath) (any, error) { return c.shares.Policy(ctx, p.ConnectionID) }))
}

func (c *Core) registerVaults() {
	h := c.srv.Handle
	h("vaults.list", api.NoParams(func(context.Context) (any, error) { return c.vaults.List() }))
	h("vaults.create", api.Bind(func(ctx context.Context, p vault.CreateParams) (any, error) { return c.vaults.Create(ctx, p) }))
	h("vaults.open", api.Bind(func(ctx context.Context, p vault.OpenParams) (any, error) { return c.vaults.Open(ctx, p) }))
	h("vaults.unlock", api.Bind(func(ctx context.Context, p struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}) (any, error) {
		return c.vaults.Unlock(ctx, p.ID, p.Password)
	}))
	h("vaults.lock", api.Bind(func(ctx context.Context, p idParam) (any, error) { return c.vaults.Lock(ctx, p.ID) }))
	h("vaults.changePassword", api.Bind(func(ctx context.Context, p struct {
		ID          string `json:"id"`
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}) (any, error) {
		return empty(), c.vaults.ChangePassword(ctx, p.ID, p.OldPassword, p.NewPassword)
	}))
	h("vaults.recover", api.Bind(func(ctx context.Context, p vault.RecoverParams) (any, error) { return c.vaults.Recover(ctx, p) }))
	h("vaults.exportRclone", api.Bind(func(ctx context.Context, p struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}) (any, error) {
		ini, err := c.vaults.ExportRclone(ctx, p.ID, p.Password)
		return map[string]string{"ini": ini}, err
	}))
	h("vaults.remove", api.Bind(func(ctx context.Context, p idParam) (any, error) { return empty(), c.vaults.Remove(ctx, p.ID) }))
	h("vaults.encryptExisting", api.Bind(func(ctx context.Context, p vault.EncryptParams) (any, error) {
		return c.vaults.EncryptExisting(ctx, p)
	}))
	h("vaults.migrationConfirmDelete", api.Bind(func(ctx context.Context, p struct {
		JobID string `json:"jobId"`
	}) (any, error) {
		return empty(), c.vaults.MigrationConfirmDelete(ctx, p.JobID)
	}))
}

func (c *Core) registerMisc() {
	h := c.srv.Handle
	h("paths.resolve", api.Bind(func(_ context.Context, p struct {
		LocalPath string `json:"localPath"`
	}) (any, error) {
		return c.resolvePath(p.LocalPath)
	}))
	h("finder.roots", api.NoParams(func(ctx context.Context) (any, error) { return c.finderRoots(ctx) }))
	h("finder.validateToken", api.Bind(func(_ context.Context, p struct {
		Token string `json:"token"`
	}) (any, error) {
		return map[string]bool{"valid": p.Token != "" && constantTimeEqual(p.Token, c.token)}, nil
	}))
	h("activity.query", api.Bind(func(_ context.Context, f store.ActivityFilter) (any, error) { return c.st.QueryActivity(f) }))
	h("activity.export", api.Bind(func(_ context.Context, p struct {
		Format string `json:"format"`
		Path   string `json:"path"`
	}) (any, error) {
		n, err := c.exportActivity(p.Format, p.Path)
		return map[string]int{"count": n}, err
	}))
	h("notifications.pending", api.NoParams(func(context.Context) (any, error) { return c.st.PendingNotifications() }))
	h("notifications.ack", api.Bind(func(_ context.Context, p struct {
		IDs []int64 `json:"ids"`
	}) (any, error) {
		return empty(), c.st.AckNotifications(p.IDs)
	}))
	h("updates.status", api.NoParams(func(context.Context) (any, error) { return c.updates.Status(), nil }))
}

// exportActivity writes the Activity Log as JSON or RFC 4180 CSV.
func (c *Core) exportActivity(format, path string) (int, error) {
	if !filepath.IsAbs(path) {
		return 0, api.Invalid("path must be absolute")
	}
	if format != "json" && format != "csv" {
		return 0, api.Invalid("format must be json or csv")
	}
	entries, err := c.st.QueryActivity(store.ActivityFilter{Limit: 1 << 30})
	if err != nil {
		return 0, err
	}
	// Write next to the target and rename, so a failure never leaves a
	// truncated file where the user's file was.
	f, err := os.CreateTemp(filepath.Dir(path), ".cloudwire-export-*")
	if err != nil {
		return 0, api.Errorf("core.internal", "cannot write %s: %v", path, err)
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	switch format {
	case "json":
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			return 0, err
		}
	case "csv":
		w := csv.NewWriter(f)
		w.UseCRLF = true
		_ = w.Write([]string{"ts", "level", "category", "subject", "message"})
		for _, a := range entries {
			_ = w.Write([]string{time.UnixMilli(a.TS).UTC().Format(time.RFC3339), a.Level, a.Category, a.SubjectID, a.Message})
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return 0, err
		}
	}
	if err := f.Chmod(0o644); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, api.Errorf("core.internal", "cannot write %s: %v", path, err)
	}
	return len(entries), nil
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
