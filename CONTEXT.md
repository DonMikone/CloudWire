# CloudWire

CloudWire is a macOS client that brings any rclone-supported cloud to the Mac as streamed drives and as truly local, background-synced working copies.

## Language

**Connection** (UI: Verbindung):
A configured account at one cloud provider, backed by one rclone remote.
_Avoid_: Remote (in UI), Account

**Mount** (UI: Laufwerk):
A Connection path permanently attached as a Finder volume whose files are streamed on demand.
_Avoid_: Drive, Volume

**Offline Item** (UI: Offline-Element):
A Selection of one Connection's folders and files, kept as real local files at their cloud path below one Storage Location and synced both ways.
_Avoid_: Pinned file, Cache, Selective sync

**Selection** (UI: Auswahl):
The checked folders and files of an Offline Item. A checked folder includes everything below it, including later additions; parent folders of checked items are kept only as structure.
_Avoid_: Include list, Filter

**Storage Location** (UI: Speicherort):
The local directory that mirrors an Offline Item's root folder.

**Base Folder** (UI: Basisordner):
The default parent directory for new Storage Locations.

**Vault** (UI: Tresor):
A cloud folder whose contents CloudWire encrypts client-side; readable only through CloudWire or rclone.
_Avoid_: Crypt remote, Encrypted drive

**Recovery Key** (UI: Wiederherstellungsschlüssel):
A one-time generated secret that can open a Vault when its password is lost.

**Share** (UI: Freigabe):
A grant of access to a cloud file or folder: Public Link, Internal Link, User/Group Share or Email Share.

**Pause Rule** (UI: Pausenregel):
A condition that suspends Offline Item syncing; Studio Mode pauses while chosen apps run.

**Quiet Period** (UI: Ruhephase):
The time without local changes an Offline Item waits before syncing.

**Conflict Copy** (UI: Konfliktkopie):
The cloud version saved beside a file that changed locally and in the cloud since the last sync.

**Mass-Delete Guard** (UI: Massenlöschschutz):
The stop that requires confirmation when a sync would delete more than half of an Offline Item.

**Core**:
The background service that owns Mounts, syncing, Shares, Vaults and the Activity Log.

**App**:
The user interface client: window, menu bar and Finder extension.

**Activity Log** (UI: Aktivitätsprotokoll):
The persistent record of errors, actions and sync runs.
