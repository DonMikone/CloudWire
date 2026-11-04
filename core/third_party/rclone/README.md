# Vendored rclone fork

This is rclone **v1.75.1** (source from
`github.com/rclone/rclone`, license MIT, see `COPYING`), vendored as an
unpruned subtree and patched locally by CloudWire (see
`docs/adr/0012-rclone-fork.md`). CloudWire links rclone in-process for
the Offline-Item sync engine; the bisync command drives the two-way
sync.

## Local patches

1. **`cmd/bisync/operations.go`, `cmd/bisync/aliases.go` — `--recover`
   validates the old listings across Unicode normalization.**
   `checkSync` compares `*.lst-old` backups byte-exact while the
   `AliasMap` is still empty (it is only filled during the march of the
   running pass), so a local-NFD vs cloud-NFC pair makes every recovery
   fail with "path1 and path2 are out of sync". The patch fills
   `b.aliases` from the backup listings before `checkSync` runs.
   Upstream issue: https://github.com/rclone/rclone/issues/XXXX
   (filed by CloudWire; number recorded when the fork is published).

2. **`cmd/bisync/checkfn.go` — `WhichEqual` falls back to
   size+modtime when no hash is available.** A webdav object uploaded
   by another client (Nextcloud macOS client without `oc:checksums`)
   has no server-side hash; `WhichEqual` reported "not equal" for
   byte-identical files, aborting every `--resync` ("Unable to rollback
   during --resync") and leaving the sync in a resync loop. The patch
   falls back to `operations.Equal` (size+modtime), consistent with
   `checkconflicts()`, which already counts noHash files as matches.
   Upstream issue: https://github.com/rclone/rclone/issues/XXXX
   (filed by CloudWire; number recorded when the fork is published).

3. **`cmd/bisync/deltas.go` — directories new on both sides are looked
   up under each side's own name.** In `applyDeltas`, a directory that
   is new on both sides is recorded as skipped via
   `ls2.getPut(file, …)` with the Path1 name. For normalization twins
   (local NFD, cloud NFC) that name is not in the Path2 listing, so
   `getPut` dereferenced nil and the run panicked ("invalid memory
   address or nil pointer dereference"), every run. The patch looks each
   listing up with `getTryAlias(file, alias)`, as the rest of the
   function already does, so each listing keeps its side's name.
   Upstream issue: https://github.com/rclone/rclone/issues/XXXX
   (filed by CloudWire; number recorded when the fork is published).

4. **`cmd/bisync/operations.go`, `cmd/bisync/lockfile.go` — a panic
   releases the lock and marks the listings failed.** The rc job
   recovers a panic in the run and the process lives on, but the lock
   file stayed behind: the next runs failed with "prior lock file
   found" until `--max-lock` expired. `Bisync` now calls
   `releaseAfterPanic` from a deferred function when `runLocked` does
   not return; it does at once what an expired lock does on the next
   run (listings marked failed, lock removed). The panic is not
   recovered there and keeps its stack for the rc job.
   Upstream issue: https://github.com/rclone/rclone/issues/XXXX
   (filed by CloudWire; number recorded when the fork is published).

5. **`cmd/bisync/listcache.go`, `cmd/bisync/{cmd,rc,march,operations}.go`,
   `backend/webdav/webdav.go`, `backend/webdav/api/types.go` —
   incremental Path2 listing (`--listing-cache off|refresh|use`).**
   The webdav listing asks for `getetag` and `oc:fileid` and reports
   them, plus the mount flag (`M` in `oc:permissions`), on a new
   `webdav.Directory`. Bisync's march lists Path2 through a wrapper that
   records each folder's raw entries with the markers its parent's
   listing reported, and with `use` serves a folder from the previous
   run's record when ETag and file id are unchanged (never for mounts).
   The cache file `*.path2.lsc` is removed at the start of every run and
   written only by a successful one. Default `off` behaves like
   upstream. Rationale: `docs/adr/0013-incremental-cloud-listing.md`.
   Upstream issue: https://github.com/rclone/rclone/issues/XXXX
   (filed by CloudWire; number recorded when the fork is published).

6. **`cmd/bisync/journal.go`, `cmd/bisync/{cmd,rc,operations,deltas,resolve,resync,queue,march,listcache}.go`,
   `fs/operations/transferdone.go`, `fs/sync/sync.go`, `backend/webdav/webdav.go` —
   transfer journal, settle time and local Trash (rc parameters `journal`,
   `settleTime`).** A per-file journal (`*.jnl` in the workdir) records,
   as each copy completes (new `operations.TransferDoneFn`, called by
   sync after a successful `Copy`), the state both sides had when bisync
   last brought them in line, including the webdav file ETag (now kept on
   `webdav.Object` and collected by the march; never written to the
   listing files, whose id column must stay `-`), and the expected
   cloud state of every upload before it starts. A Path2 delta the journal
   explains as bisync's own work is dropped and the Path1 version wins; a
   Path2 deletion counts only for a file the journal saw in the cloud and
   only after a fresh `NewObject` confirms it. A resync decides each file
   present on both sides through the journal instead of `--resync-mode`.
   Per-file transfer errors no longer abort the run critically: the files
   are rolled back and the run logs `TransfersFailed`; Path1 files
   changed within the settle time are left for a later run (`LeftForLaterRun`); Path1 files a
   sync replaces or deletes are moved aside (`--backup-dir` in the
   workdir) and handed to `LocalTrash` once the transfer succeeded, or put
   back if it did not. A graceful stop cancels transfers at once. A journal
   run works on its own copy of the config (`fs.AddConfig`) so the
   backup dir never leaks into another run in the same process. Off by
   default (upstream behaviour). Rationale:
   `docs/adr/0014-local-wins-without-proven-cloud-change.md`.

All patches carry a `// CloudWire patch (see docs/adr/00NN)` comment
marker so an upstream upgrade can find and reapply them (`grep -rn
"CloudWire patch"`).

## Upgrading

Replace the tree with the new upstream release, reapply the patches
(marked with `// CloudWire patch`), update this README, and keep the
`replace` directive in `core/go.mod` pointing here.