package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/rclone/rclone/backend/crypt"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/operations"

	"github.com/DonMikone/CloudWire/core/internal/rcl"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// runMigrate copies a file or folder into a Vault and verifies every file.
func runMigrate(job sv.Job, cmds <-chan sv.Command, out *emitter) int {
	m := job.Migrate
	if m == nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: "missing migrate job"})
		return 1
	}
	applyBwLimit(job.BwLimit)
	var jobID int64
	var err error
	if m.IsDir {
		jobID, err = startAsync("sync/copy", map[string]any{
			"srcFs": m.SrcFs + m.SrcPath, "dstFs": m.DstFs + m.DstPath, "createEmptySrcDirs": true, "_async": true,
		})
	} else {
		jobID, err = startAsync("operations/copyfile", map[string]any{
			"srcFs": m.SrcFs, "srcRemote": m.SrcPath, "dstFs": m.DstFs, "dstRemote": m.DstPath, "_async": true,
		})
	}
	if err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	st, err := waitJob(jobID, cmds, out, func() {
		_, _ = rcl.Call("job/stop", map[string]any{"jobid": jobID})
	})
	if err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	if !st.Success {
		status := sv.StatusError
		if strings.Contains(st.Error, "context canceled") {
			status = sv.StatusStopped
		}
		out.emit(sv.Msg{Type: "result", Status: status, Error: st.Error})
		return 0
	}
	mismatches, verified, err := verifyMigration(jobContext(), m)
	if err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: "verify: " + err.Error()})
		return 0
	}
	if len(mismatches) > 0 {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusMismatch, Mismatches: mismatches})
		return 0
	}
	out.emit(sv.Msg{Type: "result", Status: sv.StatusVerified, Files: verified})
	return 0
}

// verifyMigration compares the source with the encrypted copy. Files whose
// hashes are unavailable are re-checked by downloading both sides. It returns
// the mismatching and the verified source files; only verified files may be
// deleted afterwards.
func verifyMigration(ctx context.Context, m *sv.MigrateJob) (mismatches, verified []string, err error) {
	srcDir, dstDir := m.SrcPath, m.DstPath
	if !m.IsDir {
		srcDir, dstDir = parentDir(m.SrcPath), parentDir(m.DstPath)
		fi, err := filter.NewFilter(nil)
		if err != nil {
			return nil, nil, err
		}
		if err := fi.AddFile(path.Base(m.SrcPath)); err != nil {
			return nil, nil, err
		}
		ctx = filter.ReplaceConfig(ctx, fi)
	}
	fsrc, err := cache.Get(ctx, m.SrcFs+srcDir)
	if err != nil {
		return nil, nil, err
	}
	fdst, err := cache.Get(ctx, m.DstFs+dstDir)
	if err != nil {
		return nil, nil, err
	}
	res, matched, downloaded := &checkResult{}, &checkResult{}, &checkResult{}
	noHash, err := cryptCheck(ctx, fdst, fsrc, res, matched)
	if errors.Is(err, errNoHashes) {
		// Underlying backend has no hashes: verify everything by download.
		if err := downloadCheck(ctx, fdst, fsrc, nil, res, downloaded); err != nil && !res.any() {
			return nil, nil, err
		}
		return res.list(), downloaded.list(), nil
	}
	if err != nil && !res.any() {
		return nil, nil, err
	}
	if len(noHash) > 0 {
		if err := downloadCheck(ctx, fdst, fsrc, noHash, res, downloaded); err != nil && !res.any() {
			return nil, nil, err
		}
	}
	// A file counts as verified if its hash matched, or if it had no hash and
	// the download comparison matched.
	skip := map[string]bool{}
	for _, f := range noHash {
		skip[f] = true
	}
	for _, f := range matched.list() {
		if !skip[f] {
			verified = append(verified, f)
		}
	}
	verified = append(verified, downloaded.list()...)
	return res.list(), verified, nil
}

func parentDir(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// checkResult collects file names written by rclone's check writers.
type checkResult struct {
	mu    sync.Mutex
	names []string
	seen  map[string]bool
}

func (r *checkResult) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		name := string(bytes.TrimSpace(line))
		if name != "" && !r.seen[name] {
			r.seen[name] = true
			r.names = append(r.names, name)
		}
	}
	return len(p), nil
}

func (r *checkResult) any() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.names) > 0
}

func (r *checkResult) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.names...)
}

func downloadCheck(ctx context.Context, fdst, fsrc fs.Fs, only []string, res, matched *checkResult) error {
	if only != nil {
		fi, err := filter.NewFilter(nil)
		if err != nil {
			return err
		}
		for _, f := range only {
			if err := fi.AddFile(f); err != nil {
				return err
			}
		}
		ctx = filter.ReplaceConfig(ctx, fi)
	}
	return operations.CheckDownload(ctx, &operations.CheckOpt{
		Fdst: fdst, Fsrc: fsrc, OneWay: true, MissingOnDst: res, Differ: res, Error: res, Match: matched,
	})
}

var errNoHashes = errors.New("underlying remote supports no hashes")

// cryptCheck is adapted from rclone v1.75.0 cmd/cryptcheck/cryptcheck.go.
//
// Copyright (C) 2012 by Nick Craig-Wood http://www.craig-wood.com/nick/
// Licensed under the MIT License (see https://github.com/rclone/rclone/blob/master/COPYING).
//
// It checks the integrity of an encrypted remote against its source and
// returns the source files that could not be hash-checked.
func cryptCheck(ctx context.Context, fdst, fsrc fs.Fs, res, matched *checkResult) (noHash []string, err error) {
	fcrypt, ok := fdst.(*crypt.Fs)
	if !ok {
		return nil, fmt.Errorf("%s:%s is not a crypt remote", fdst.Name(), fdst.Root())
	}
	funderlying := fcrypt.UnWrap()
	hashType := funderlying.Hashes().GetOne()
	if hashType == hash.None {
		return nil, errNoHashes
	}
	var mu sync.Mutex
	opt := &operations.CheckOpt{
		Fdst: fcrypt, Fsrc: fsrc, OneWay: true, MissingOnDst: res, Differ: res, Error: res, Match: matched,
	}
	opt.Check = func(ctx context.Context, dst, src fs.Object) (differ bool, noHashFound bool, err error) {
		cryptDst := dst.(*crypt.Object)
		underlyingDst := cryptDst.UnWrap()
		underlyingHash, err := underlyingDst.Hash(ctx, hashType)
		if err != nil {
			return true, false, fmt.Errorf("error reading hash from underlying %v: %w", underlyingDst, err)
		}
		if underlyingHash == "" {
			mu.Lock()
			noHash = append(noHash, src.Remote())
			mu.Unlock()
			return false, true, nil
		}
		cryptHash, err := fcrypt.ComputeHash(ctx, cryptDst, src, hashType)
		if err != nil {
			return true, false, fmt.Errorf("error computing hash: %w", err)
		}
		if cryptHash == "" {
			mu.Lock()
			noHash = append(noHash, src.Remote())
			mu.Unlock()
			return false, true, nil
		}
		if cryptHash != underlyingHash {
			fs.Errorf(src, "hashes differ (%s:%s) %q vs (%s:%s) %q", fdst.Name(), fdst.Root(), cryptHash, fsrc.Name(), fsrc.Root(), underlyingHash)
			return true, false, nil
		}
		return false, false, nil
	}
	err = operations.CheckFn(ctx, opt)
	return noHash, err
}
