# Glossary

CloudWire terms in code and docs. UI labels live in `CONTEXT.md`.

**Offline Item** (code: `store.OfflineItem`, `offline.Engine`):
Real local file(s) at a Storage Location, synced both ways with one cloud folder or file set of a Connection; never a cache entry (docs/adr/0003). _Avoid_: pinned cache.

**Storage Location** (code: `StoragePath`):
The local directory of an Offline Item whose contents mirror an item's cloud root (docs/adr/0010).

**Selection** (code: `Files` on an item, filters):
Which cloud paths an Offline Item covers; an anchored filter list, not a file index (docs/adr/0010). _Avoid_: include pattern list.

**Listings** (code: rclone bisync `*.lst[,-new,-old,-err]` state files under the item's bisync workdir):
rclone-bisync's per-side inventory of both synced paths; a lost or invalid listing pair suspends the item's normal bisync until recover or resync (docs/adr/0012).

**Resync** (code: `resync: true` in bisync, `NeedsResync` on an item):
Rebuild of the listing pair from a full union copy of both sides; never deletes, decides per-file winners (docs/adr/0003).

**Conflict Copy** (UI: Konfliktkopie; code: `ConflictSuffix`/`ConflictMarker`):
The losing version of a bisync conflict, saved beside the file under a distinct name and label; never counts toward the Mass-Delete Guard.

**Offline-Sync** (code: `offline` packages, `supervisor`):
The background engine around the Offline Items: scheduling, triggers, bisync runs, retry backoff (docs/adr/0012).