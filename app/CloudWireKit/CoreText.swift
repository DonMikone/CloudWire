import Foundation

/// A user-facing text from the Core (contract: `core/internal/msg`): a stable `code` naming one sentence,
/// its `params`, and the English `message` the Core rendered from the same template.
///
/// `localized` translates known codes through `Localizable.xcstrings` and falls back to `message` for
/// unknown codes, missing params and Activity entries written before codes existed. The `detail` param is
/// raw rclone or OS text: it is never translated and views show it as secondary text. A `cause` param
/// names a nested code whose sentence follows the main one.
public struct CoreText: Sendable, Hashable {
    public var code: String
    public var params: [String: String]
    public var message: String

    public init(code: String?, params: [String: String] = [:], message: String) {
        self.code = code ?? ""
        self.params = params
        self.message = message
    }

    /// Raw rclone or OS text; nil when the text has none.
    public var detail: String? {
        guard let detail = params["detail"], !detail.isEmpty else { return nil }
        return detail
    }

    /// The translated sentence without the detail: nil for unknown codes or missing params, empty for
    /// texts that consist of their detail only.
    public func sentence(bundle: Bundle = .main) -> String? {
        guard let render = Self.renderers[code], let base = render(Args(values: params, bundle: bundle)) else {
            return nil
        }
        guard let cause = params["cause"] else { return base }
        var nested = params
        nested["cause"] = nil
        guard let reason = CoreText(code: cause, params: nested, message: "").sentence(bundle: bundle) else {
            return nil
        }
        return Self.join(base, reason)
    }

    /// The sentence and the raw detail, for views that show the detail as secondary text. A text that is only
    /// a raw reason gets `rawHeadline` as its sentence when given. Unknown codes give the English message,
    /// which already contains the detail.
    public func parts(bundle: Bundle = .main, rawHeadline: String? = nil) -> (text: String, detail: String?) {
        guard let sentence = sentence(bundle: bundle) else { return (message, nil) }
        if sentence.isEmpty {
            guard let rawHeadline, let detail else { return (detail ?? message, nil) }
            return (rawHeadline, detail)
        }
        return (sentence, detail)
    }

    /// One string for alerts, notifications and list rows: the sentence followed by the detail.
    public func localized(bundle: Bundle = .main, rawHeadline: String? = nil) -> String {
        let parts = parts(bundle: bundle, rawHeadline: rawHeadline)
        return parts.detail.map { Self.join(parts.text, $0) } ?? parts.text
    }

    /// Every code with a translation.
    public static var knownCodes: [String] { Array(renderers.keys) }

    /// Appends `tail` after ": ", or after a space when `head` already ends a sentence (as the Core does).
    static func join(_ head: String, _ tail: String) -> String {
        if head.isEmpty { return tail }
        return head.hasSuffix(".") ? "\(head) \(tail)" : "\(head): \(tail)"
    }

    /// Typed access to the params of one text; formatted values follow the user's locale.
    struct Args {
        let values: [String: String]
        let bundle: Bundle

        subscript(_ key: String) -> String? { values[key] }

        func int(_ key: String) -> Int? { values[key].flatMap { Int($0) } }

        /// A byte count, formatted like Finder.
        func bytes(_ key: String) -> String? {
            values[key].flatMap { Int64($0) }.map { ByteCountFormatter.string(fromByteCount: $0, countStyle: .file) }
        }

        /// Unix milliseconds as date and time.
        func date(_ key: String) -> String? {
            values[key].flatMap { Int64($0) }.map {
                Date(timeIntervalSince1970: TimeInterval($0) / 1000).formatted(date: .abbreviated, time: .shortened)
            }
        }

        /// A whole percentage (`73` → "73%" or "73 %").
        func percent(_ key: String) -> String? {
            int(key).map { (Double($0) / 100).formatted(.percent.precision(.fractionLength(0))) }
        }
    }

    /// One sentence per code, mirroring the templates in `core/internal/msg/msg.go`. Keys and translations
    /// live in `app/CloudWire/Localizable.xcstrings` (entries marked `manual`: Xcode does not extract them
    /// from this static library).
    private static let renderers: [String: @Sendable (Args) -> String?] = [
        "detail": { _ in "" },
        "rclone.error": { a in String(localized: "rclone reported an error", bundle: a.bundle) },
        "path.createFailed": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Cannot create \(path)", bundle: a.bundle)
        },
        "path.openFailed": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Cannot open \(path)", bundle: a.bundle)
        },
        "path.readFailed": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Cannot read \(path)", bundle: a.bundle)
        },
        "path.writeFailed": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Cannot write \(path)", bundle: a.bundle)
        },
        "folder.notEmpty": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "The folder \(path) is not empty", bundle: a.bundle)
        },
        "core.started": { a in
            guard let version = a["version"] else { return nil }
            return String(localized: "Background service \(version) started", bundle: a.bundle)
        },
        "core.stopped": { a in String(localized: "Background service stopped", bundle: a.bundle) },
        "core.firstStart": { a in
            guard let label = a["label"], let count = a.int("count") else { return nil }
            return String(
                localized: "First start: conflict label “\(label)”, Studio Mode apps detected: \(count)",
                bundle: a.bundle)
        },
        "core.networkOnline": { a in String(localized: "Network changed (online)", bundle: a.bundle) },
        "core.networkOffline": { a in String(localized: "Network changed (offline)", bundle: a.bundle) },
        "core.sleeping": { a in String(localized: "The Mac is going to sleep", bundle: a.bundle) },
        "core.woke": { a in String(localized: "The Mac woke from sleep", bundle: a.bundle) },
        "connection.notFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Connection \(id) not found", bundle: a.bundle)
        },
        "connection.setupNotFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "No connection setup in progress for \(id)", bundle: a.bundle)
        },
        "connection.pathNotFound": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "\(path) is not inside a Mount or Offline Item", bundle: a.bundle)
        },
        "connection.nameNotFolder": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "The name “\(name)” cannot be used as a folder name", bundle: a.bundle)
        },
        "connection.nameMountFolder": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "The name “\(name)” is reserved for the Mount folder", bundle: a.bundle)
        },
        "connection.nameTaken": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "A connection named “\(name)” already exists", bundle: a.bundle)
        },
        "connection.namePending": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "A connection named “\(name)” is being set up", bundle: a.bundle)
        },
        "connection.invalidServerURL": { a in
            guard let url = a["url"] else { return nil }
            return String(localized: "“\(url)” is not a valid server address", bundle: a.bundle)
        },
        "connection.noLoginFlow": { a in
            guard let url = a["url"] else { return nil }
            return String(
                localized: "\(url) offers no Nextcloud login. Is this the address of a Nextcloud server?",
                bundle: a.bundle)
        },
        "connection.signInFailed": { a in String(localized: "Sign-in failed", bundle: a.bundle) },
        "connection.otherAccount": { a in
            guard let user = a["user"], let name = a["name"] else { return nil }
            return String(localized: "Signed in as \(user), but “\(name)” belongs to another account", bundle: a.bundle)
        },
        "connection.signInTimedOut": { a in String(localized: "The sign-in timed out", bundle: a.bundle) },
        "connection.setupCancelled": { a in String(localized: "The connection setup was cancelled", bundle: a.bundle) },
        "connection.loginTimedOut": { a in String(localized: "The login timed out", bundle: a.bundle) },
        "connection.loginCancelled": { a in String(localized: "The login was cancelled", bundle: a.bundle) },
        "connection.removeVault": { a in String(localized: "Remove the Vault instead", bundle: a.bundle) },
        "connection.inUse": { a in
            guard let name = a["name"], let count = a.int("count") else { return nil }
            return String(localized: "“\(name)” is still used by \(count) items", bundle: a.bundle)
        },
        "connection.added": { a in
            guard let name = a["name"], let provider = a["provider"] else { return nil }
            return String(localized: "Connection “\(name)” added (\(provider))", bundle: a.bundle)
        },
        "connection.updated": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Connection “\(name)” updated", bundle: a.bundle)
        },
        "connection.removed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Connection “\(name)” removed", bundle: a.bundle)
        },
        "connection.nextcloudAdded": { a in
            guard let name = a["name"], let server = a["server"], let user = a["user"] else { return nil }
            return String(localized: "Nextcloud connection “\(name)” added (\(server) as \(user))", bundle: a.bundle)
        },
        "connection.nextcloudSignedIn": { a in
            guard let name = a["name"], let user = a["user"] else { return nil }
            return String(localized: "Nextcloud connection “\(name)” signed in again as \(user)", bundle: a.bundle)
        },
        "mount.notFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Mount \(id) not found", bundle: a.bundle)
        },
        "mount.pointInUse": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Another Mount already uses \(path)", bundle: a.bundle)
        },
        "mount.pointOverlapsOffline": { a in
            guard let path = a["path"], let storagePath = a["storagePath"] else { return nil }
            return String(localized: "\(path) overlaps the Offline Item stored at \(storagePath)", bundle: a.bundle)
        },
        "mount.pointIsVolume": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "\(path) is already a mounted volume", bundle: a.bundle)
        },
        "mount.fuseUnavailable": { a in String(localized: "FUSE requires FUSE-T or macFUSE to be installed", bundle: a.bundle) },
        "mount.inactive": { a in String(localized: "The Mount is not active", bundle: a.bundle) },
        "mount.pointBusy": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "\(path) is used by another volume", bundle: a.bundle)
        },
        "mount.stillAttached": { a in String(localized: "The previous mount is still attached", bundle: a.bundle) },
        "mount.workerExited": { a in String(localized: "The mount process exited", bundle: a.bundle) },
        "mount.created": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "Mount “\(name)” created at \(path)", bundle: a.bundle)
        },
        "mount.updated": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” updated", bundle: a.bundle)
        },
        "mount.removed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” removed", bundle: a.bundle)
        },
        "mount.unmounted": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” unmounted", bundle: a.bundle)
        },
        "mount.unmountFailed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Could not unmount “\(name)”", bundle: a.bundle)
        },
        "mount.unmountProblem": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Unmounting “\(name)” reported a problem", bundle: a.bundle)
        },
        "mount.mounted": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "Mounted “\(name)” at \(path)", bundle: a.bundle)
        },
        "mount.reconnected": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "Reconnected “\(name)” at \(path)", bundle: a.bundle)
        },
        "mount.servedFromLocalhost": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "“\(name)” is mounted from localhost", bundle: a.bundle)
        },
        "mount.ejected": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” was ejected in Finder", bundle: a.bundle)
        },
        "mount.stoppedUnexpectedly": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” stopped unexpectedly", bundle: a.bundle)
        },
        "mount.failedRepeatedly": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” failed repeatedly", bundle: a.bundle)
        },
        "mount.notResponding": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mount “\(name)” is not responding; restarting it", bundle: a.bundle)
        },
        "offline.notFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Offline Item \(id) not found", bundle: a.bundle)
        },
        "offline.insufficientSpace": { a in
            guard let neededBytes = a.bytes("neededBytes"), let freeBytes = a.bytes("freeBytes") else { return nil }
            return String(
                localized: "Not enough free space: \(neededBytes) needed, \(freeBytes) available",
                bundle: a.bundle)
        },
        "offline.moveFailed": { a in String(localized: "Moving the files failed", bundle: a.bundle) },
        "offline.trashFailed": { a in String(localized: "Moving the local copy to the Trash failed", bundle: a.bundle) },
        "offline.vaultFolder": { a in
            guard let folder = a["folder"] else { return nil }
            return String(
                localized: "“\(folder)” holds encrypted Vault data. Unlock the Vault and make it available offline under Vaults.",
                bundle: a.bundle)
        },
        "offline.storageTooBroad": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "The storage location \(path) is too broad", bundle: a.bundle)
        },
        "offline.storageNotAllowed": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "The storage location \(path) is not allowed", bundle: a.bundle)
        },
        "offline.storageOverlapsMount": { a in
            guard let path = a["path"], let mountPoint = a["mountPoint"] else { return nil }
            return String(
                localized: "The storage location \(path) overlaps the Mount at \(mountPoint)",
                bundle: a.bundle)
        },
        "offline.overlapsItem": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "This overlaps the Offline Item “\(name)” (\(path))", bundle: a.bundle)
        },
        "offline.storageOverlapsItem": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(
                localized: "The storage location overlaps the Offline Item “\(name)” (\(path))",
                bundle: a.bundle)
        },
        "offline.syncFailed": { a in String(localized: "Syncing failed", bundle: a.bundle) },
        "offline.cloudFolderMissing": { a in String(localized: "The cloud folder was not found. Restore it in the cloud or remove the Offline Item.", bundle: a.bundle) },
        "offline.pausedByRule": { a in String(localized: "Syncing paused", bundle: a.bundle) },
        "offline.rulesInactive": { a in String(localized: "Pause Rules no longer apply; syncing resumes", bundle: a.bundle) },
        "offline.pausedManually": { a in String(localized: "Syncing paused until resumed", bundle: a.bundle) },
        "offline.pausedUntil": { a in
            guard let untilMs = a.date("untilMs") else { return nil }
            return String(localized: "Syncing paused until \(untilMs)", bundle: a.bundle)
        },
        "offline.resumed": { a in String(localized: "Syncing resumed", bundle: a.bundle) },
        "offline.added": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "Offline Item “\(name)” added at \(path)", bundle: a.bundle)
        },
        "offline.filesAdded": { a in
            guard let count = a.int("count"), let name = a["name"] else { return nil }
            return String(localized: "Added \(count) files to Offline Item “\(name)”", bundle: a.bundle)
        },
        "offline.settingsChanged": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Settings of “\(name)” changed", bundle: a.bundle)
        },
        "offline.movedWithLeftovers": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(
                localized: "Moved “\(name)”, but some files could not be removed from \(path)",
                bundle: a.bundle)
        },
        "offline.moved": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "Moved “\(name)” to \(path)", bundle: a.bundle)
        },
        "offline.removedTrash": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Offline Item “\(name)” removed; local copy moved to the Trash", bundle: a.bundle)
        },
        "offline.removedKept": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Offline Item “\(name)” removed; local copy kept", bundle: a.bundle)
        },
        "offline.deletionsConfirmed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Deletions in “\(name)” confirmed", bundle: a.bundle)
        },
        "offline.deletionsRestored": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Deleted files of “\(name)” will be restored", bundle: a.bundle)
        },
        "sync.done": { a in
            guard let name = a["name"], let transferred = a.int("transferred"), let deleted = a.int("deleted"),
                let conflicts = a.int("conflicts")
            else { return nil }
            let counts = [
                String(localized: "\(transferred) transferred", bundle: a.bundle),
                String(localized: "\(deleted) deleted", bundle: a.bundle),
                String(localized: "\(conflicts) conflicts", bundle: a.bundle),
            ].joined(separator: ", ")
            return String(localized: "Synced “\(name)”: \(counts)", bundle: a.bundle)
        },
        "sync.noChanges": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Synced “\(name)”: no changes", bundle: a.bundle)
        },
        "sync.conflicts": { a in
            guard let count = a.int("count"), let name = a["name"] else { return nil }
            return String(localized: "\(count) conflict copies created in “\(name)”", bundle: a.bundle)
        },
        "sync.massDelete": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Mass-Delete Guard stopped “\(name)”", bundle: a.bundle)
        },
        "sync.failed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Sync of “\(name)” failed", bundle: a.bundle)
        },
        "pause.studioMode": { a in
            guard let app = a["app"] else { return nil }
            return String(localized: "Studio Mode (\(app))", bundle: a.bundle)
        },
        "pause.battery": { a in String(localized: "Battery power", bundle: a.bundle) },
        "pause.lowPowerMode": { a in String(localized: "Low Power Mode", bundle: a.bundle) },
        "pause.expensiveNetwork": { a in String(localized: "Personal hotspot", bundle: a.bundle) },
        "pause.constrainedNetwork": { a in String(localized: "Low Data Mode", bundle: a.bundle) },
        "pause.cpu": { a in
            guard let percent = a.percent("percent") else { return nil }
            return String(localized: "High CPU load (\(percent))", bundle: a.bundle)
        },
        "share.vaultUnsupported": { a in String(localized: "Sharing is not available inside a Vault because its files are encrypted", bundle: a.bundle) },
        "share.appPasswordUnreadable": { a in String(localized: "The app password cannot be read", bundle: a.bundle) },
        "share.publicLinksOnly": { a in String(localized: "This provider only supports public links", bundle: a.bundle) },
        "share.linkNotEditable": { a in String(localized: "Links of this provider cannot be edited; delete the link and create it again", bundle: a.bundle) },
        "share.notFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Share \(id) not found", bundle: a.bundle)
        },
        "share.peopleUnsupported": { a in String(localized: "Sharing with people is only available for Nextcloud and ownCloud", bundle: a.bundle) },
        "share.internalLinkUnsupported": { a in String(localized: "Internal links are only available for Nextcloud and ownCloud", bundle: a.bundle) },
        "share.browserUnsupported": { a in String(localized: "Opening in the browser is only available for Nextcloud and ownCloud", bundle: a.bundle) },
        "share.passwordRequired": { a in String(localized: "The server requires a password for public links.", bundle: a.bundle) },
        "share.expiryInPast": { a in String(localized: "The expiry date must be in the future", bundle: a.bundle) },
        "share.createdUser": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Shared “\(path)” with a user", bundle: a.bundle)
        },
        "share.createdGroup": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Shared “\(path)” with a group", bundle: a.bundle)
        },
        "share.createdEmail": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Shared “\(path)” by email", bundle: a.bundle)
        },
        "share.linkCreated": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Created public link for “\(path)”", bundle: a.bundle)
        },
        "share.updated": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Share \(id) updated", bundle: a.bundle)
        },
        "share.deleted": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Share \(id) deleted", bundle: a.bundle)
        },
        "share.linkRemovedLocally": { a in
            guard let path = a["path"] else { return nil }
            return String(
                localized: "Removed the public link for “\(path)” from CloudWire; it stays active at the provider",
                bundle: a.bundle)
        },
        "share.linkDeleted": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Deleted public link for “\(path)”", bundle: a.bundle)
        },
        "vault.notFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Vault \(id) not found", bundle: a.bundle)
        },
        "vault.migrationNotFound": { a in
            guard let id = a["id"] else { return nil }
            return String(localized: "Encryption job \(id) not found", bundle: a.bundle)
        },
        "vault.noVaultFile": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "\(path) has no vault.json", bundle: a.bundle)
        },
        "vault.readFailed": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Cannot read \(path)/vault.json", bundle: a.bundle)
        },
        "vault.exists": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "\(path) already exists", bundle: a.bundle)
        },
        "vault.createFolderFailed": { a in String(localized: "Cannot create the Vault folder", bundle: a.bundle) },
        "vault.writeFailed": { a in String(localized: "Cannot write vault.json", bundle: a.bundle) },
        "vault.alreadyAdded": { a in String(localized: "This Vault is already added", bundle: a.bundle) },
        "vault.configureFailed": { a in String(localized: "Cannot configure the Vault", bundle: a.bundle) },
        "vault.wrongPassword": { a in String(localized: "The password is not correct", bundle: a.bundle) },
        "vault.wrongRecoveryKey": { a in String(localized: "The Recovery Key is not correct", bundle: a.bundle) },
        "vault.passwordRequired": { a in String(localized: "The Vault password is required", bundle: a.bundle) },
        "vault.unlockFirst": { a in String(localized: "Unlock the Vault first", bundle: a.bundle) },
        "vault.inUse": { a in
            guard let name = a["name"], let count = a.int("count") else { return nil }
            return String(localized: "The Vault “\(name)” is still used by \(count) items", bundle: a.bundle)
        },
        "vault.sourceIsOffline": { a in
            guard let path = a["path"] else { return nil }
            return String(
                localized: "Remove the Offline Item \(path) first; its local copy would be uploaded again",
                bundle: a.bundle)
        },
        "vault.deleteFailed": { a in
            guard let count = a.int("count"), let files = a["files"] else { return nil }
            return String(localized: "Deleting \(count) original files failed: \(files)", bundle: a.bundle)
        },
        "vault.invalidName": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "“\(name)” cannot be used as a Vault name", bundle: a.bundle)
        },
        "vault.passwordTooShort": { a in
            guard let count = a.int("count") else { return nil }
            return String(localized: "The password needs at least \(count) characters", bundle: a.bundle)
        },
        "vault.cloudRoot": { a in String(localized: "The cloud root cannot be encrypted as a whole", bundle: a.bundle) },
        "vault.sourceOverlapsVault": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "The source overlaps the Vault “\(name)”", bundle: a.bundle)
        },
        "vault.created": { a in
            guard let name = a["name"], let path = a["path"] else { return nil }
            return String(localized: "Vault “\(name)” created at \(path)", bundle: a.bundle)
        },
        "vault.keychainFailed": { a in String(localized: "Could not store the Vault password in the Keychain", bundle: a.bundle) },
        "vault.opened": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Vault “\(name)” opened", bundle: a.bundle)
        },
        "vault.unlocked": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Vault “\(name)” unlocked", bundle: a.bundle)
        },
        "vault.locked": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Vault “\(name)” locked", bundle: a.bundle)
        },
        "vault.passwordChanged": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Password of Vault “\(name)” changed", bundle: a.bundle)
        },
        "vault.recovered": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Vault “\(name)” reset with its Recovery Key", bundle: a.bundle)
        },
        "vault.rcloneExported": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Emergency rclone configuration of “\(name)” exported", bundle: a.bundle)
        },
        "vault.removed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Vault “\(name)” removed from this Mac (cloud data unchanged)", bundle: a.bundle)
        },
        "vault.encryptStarted": { a in
            guard let path = a["path"], let name = a["name"] else { return nil }
            return String(localized: "Encrypting “\(path)” into Vault “\(name)”", bundle: a.bundle)
        },
        "vault.encryptVerified": { a in
            guard let name = a["name"] else { return nil }
            return String(
                localized: "Encrypted copy in “\(name)” verified; waiting for confirmation to delete the original",
                bundle: a.bundle)
        },
        "vault.encryptMismatch": { a in
            guard let name = a["name"], let count = a.int("count") else { return nil }
            return String(
                localized: "Verification of the encrypted copy in “\(name)” found \(count) differences; the original is kept",
                bundle: a.bundle)
        },
        "vault.encryptFailed": { a in
            guard let name = a["name"] else { return nil }
            return String(localized: "Encrypting into “\(name)” failed", bundle: a.bundle)
        },
        "vault.encryptCanceled": { a in
            guard let path = a["path"], let name = a["name"] else { return nil }
            return String(localized: "Encrypting “\(path)” into Vault “\(name)” canceled", bundle: a.bundle)
        },
        "vault.originalDeleted": { a in
            guard let path = a["path"] else { return nil }
            return String(localized: "Original “\(path)” deleted after encryption", bundle: a.bundle)
        },
        "client.notConnected": { a in String(localized: "Not connected to CloudWire's background service.", bundle: a.bundle) },
        "client.disconnected": { a in String(localized: "The connection to CloudWire's background service was lost.", bundle: a.bundle) },
        "client.timeout": { a in
            guard let method = a["method"] else { return nil }
            return String(
                localized: "CloudWire's background service did not answer \(method) in time.",
                bundle: a.bundle)
        },
    ]
}
