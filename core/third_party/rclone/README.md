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

Both patches carry a `// CloudWire patch (see docs/adr/0012):` comment
marker so an upstream upgrade can reapply them mechanically.

## Upgrading

Replace the tree with the new upstream release, reapply the two patches
(marked with `// CloudWire patch (see docs/adr/0012):`), update this
README, and keep the `replace` directive in `core/go.mod` pointing here.