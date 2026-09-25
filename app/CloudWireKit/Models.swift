import Foundation

// MARK: - Open enumerations

public struct ConnectionKind: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let remote: Self = "remote"
    public static let vault: Self = "vault"
}

public struct MountType: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let nfsmount: Self = "nfsmount"
    public static let cmount: Self = "cmount"
}

public struct MountState: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let unmounted: Self = "unmounted"
    public static let mounting: Self = "mounting"
    public static let mounted: Self = "mounted"
    public static let error: Self = "error"
}

public struct OfflineKind: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let folder: Self = "folder"
    public static let files: Self = "files"
}

public struct OfflineState: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let pending: Self = "pending"
    public static let syncing: Self = "syncing"
    public static let idle: Self = "idle"
    public static let paused: Self = "paused"
    public static let error: Self = "error"
    public static let needsConfirmation: Self = "needsConfirmation"
}

public struct ShareKind: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let publicLink: Self = "publicLink"
    public static let user: Self = "user"
    public static let group: Self = "group"
    public static let email: Self = "email"
    /// rclone public link from the local registry (non-Nextcloud providers).
    public static let link: Self = "link"
}

public struct UnlockMode: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let keychain: Self = "keychain"
    public static let ask: Self = "ask"
}

public struct ActivityLevel: OpenStringEnum, CaseIterable {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let debug: Self = "debug"
    public static let info: Self = "info"
    public static let warn: Self = "warn"
    public static let error: Self = "error"
    public static let allCases: [ActivityLevel] = [.debug, .info, .warn, .error]
}

public struct ActivityCategory: OpenStringEnum, CaseIterable {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let core: Self = "core"
    public static let mount: Self = "mount"
    public static let offline: Self = "offline"
    public static let sync: Self = "sync"
    public static let share: Self = "share"
    public static let vault: Self = "vault"
    public static let connection: Self = "connection"
    public static let update: Self = "update"
    public static let allCases: [ActivityCategory] = [.core, .mount, .offline, .sync, .share, .vault, .connection, .update]
}

public struct NotificationKind: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let error: Self = "error"
    public static let conflict: Self = "conflict"
    public static let massDelete: Self = "massDelete"
}

public struct FileSyncStatus: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let synced: Self = "synced"
    public static let syncing: Self = "syncing"
    public static let error: Self = "error"
    public static let conflict: Self = "conflict"
    public static let none: Self = "none"
}

public struct RootKind: OpenStringEnum {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public static let mount: Self = "mount"
    public static let offline: Self = "offline"
}

// MARK: - Core

public struct CoreInfo: Decodable, Sendable, Hashable {
    public var version: String
    public var apiVersion: Int
    public var rcloneVersion: String
    public var pid: Int
    public var startedAt: Int64
    public var appSupport: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        version = c.string("version")
        apiVersion = c.int("apiVersion")
        rcloneVersion = c.string("rcloneVersion")
        pid = c.int("pid")
        startedAt = c.int64("startedAt")
        appSupport = c.string("appSupport")
    }
}

/// Result of methods without a meaningful value (`{}`); accepts anything.
public struct EmptyResult: Decodable, Sendable {
    public init() {}
    public init(from decoder: any Decoder) throws {}
}

// MARK: - Connections

public struct Connection: Decodable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var kind: ConnectionKind
    public var provider: String
    public var rcloneRemote: String
    public var vendor: String
    public var serverURL: String
    public var user: String
    public var parameters: [String: String]
    public var createdAt: Int64

    public var isNextcloudLike: Bool { vendor == "nextcloud" || vendor == "owncloud" }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        name = c.string("name")
        kind = c.value("kind", .remote)
        provider = c.string("provider")
        rcloneRemote = c.string("rcloneRemote")
        vendor = c.string("vendor")
        serverURL = c.string("serverURL")
        user = c.string("user")
        parameters = c.stringMap("parameters")
        createdAt = c.int64("createdAt")
    }
}

public struct ConfigStep: Decodable, Sendable, Hashable {
    public var connectionId: String
    public var done: Bool
    public var pending: Bool
    public var state: String
    public var option: RcloneOption?
    public var error: String
    public var errorCode: String
    public var errorParams: [String: String]
    public var connection: Connection?

    /// The translatable `error`.
    public var errorText: CoreText { CoreText(code: errorCode, params: errorParams, message: error) }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        connectionId = c.string("connectionId")
        done = c.bool("done")
        pending = c.bool("pending")
        state = c.string("state")
        option = c.optional("option")
        error = c.string("error")
        errorCode = c.string("errorCode")
        errorParams = c.stringMap("errorParams")
        connection = c.optional("connection")
    }
}

public struct ConnectionTestResult: Decodable, Sendable, Hashable {
    public var ok: Bool
    public var total: Int64?
    public var used: Int64?
    public var free: Int64?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        ok = c.bool("ok", true)
        total = c.optionalInt64("total")
        used = c.optionalInt64("used")
        free = c.optionalInt64("free")
    }
}

public struct BrowseEntry: Decodable, Sendable, Hashable, Identifiable {
    public var name: String
    public var path: String
    public var isDir: Bool
    public var size: Int64
    public var modTime: Int64
    public var mimeType: String
    public var id: String { path }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        name = c.string("name")
        path = c.string("path")
        isDir = c.bool("isDir")
        size = c.int64("size")
        modTime = c.int64("modTime")
        mimeType = c.string("mimeType")
    }
}

public struct NextcloudLoginStart: Decodable, Sendable, Hashable {
    public var flowId: String
    public var loginURL: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        flowId = c.string("flowId")
        loginURL = c.string("loginURL")
    }
}

public struct NextcloudLoginEvent: Decodable, Sendable, Hashable {
    public var flowId: String
    public var status: String
    public var connectionId: String?
    public var error: String?
    public var errorCode: String
    public var errorParams: [String: String]

    public var succeeded: Bool { status == "ok" }

    /// The translatable `error`; nil without one.
    public var errorText: CoreText? {
        error.map { CoreText(code: errorCode, params: errorParams, message: $0) }
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        flowId = c.string("flowId")
        status = c.string("status")
        connectionId = c.optionalString("connectionId")
        error = c.optionalString("error")
        errorCode = c.string("errorCode")
        errorParams = c.stringMap("errorParams")
    }
}

// MARK: - rclone metadata

public struct RcloneProvider: Decodable, Sendable, Hashable, Identifiable {
    public var name: String
    public var description: String
    public var prefix: String
    public var options: [RcloneOption]
    public var hide: Bool
    public var id: String { name }

    /// A short provider name for labels and default Connection names: the description without a
    /// parenthetical remark ("Google Cloud Storage (this is not Google Drive)"), or the capitalised
    /// type name when the description is missing or reads like a sentence ("s3" → "S3").
    public var shortName: String {
        var text = description
        if let remark = text.range(of: " (") { text = String(text[..<remark.lowerBound]) }
        text = text.trimmingCharacters(in: .whitespaces)
        guard text.isEmpty || text.count > 30 else { return text }
        return name.prefix(1).uppercased() + name.dropFirst()
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        name = c.string("Name")
        description = c.string("Description")
        prefix = c.string("Prefix", name)
        options = c.value("Options", [RcloneOption]())
        hide = c.bool("Hide")
    }
}

public struct ProvidersList: Decodable, Sendable {
    public var providers: [RcloneProvider]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        providers = c.value("providers", [RcloneProvider]())
    }
}

public struct MountOptionsInfo: Decodable, Sendable, Hashable {
    public var vfs: [RcloneOption]
    public var mount: [RcloneOption]
    public var nfs: [RcloneOption]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        vfs = c.value("vfs", [RcloneOption]())
        mount = c.value("mount", [RcloneOption]())
        nfs = c.value("nfs", [RcloneOption]())
    }
}

public struct MainOptionsInfo: Decodable, Sendable, Hashable {
    public var main: [RcloneOption]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        main = c.value("main", [RcloneOption]())
    }
}

// MARK: - Mounts

public struct Mount: Decodable, Sendable, Hashable, Identifiable {
    public var id: String
    public var connectionId: String
    public var remotePath: String
    public var mountPoint: String
    public var volumeName: String
    public var mountType: MountType
    public var autoMount: Bool
    public var readOnly: Bool
    public var cacheMaxGB: Int
    public var options: [String: String]
    public var state: MountState
    public var error: String
    public var errorCode: String
    public var errorParams: [String: String]
    public var createdAt: Int64
    /// When the Mount last came up (Unix ms); only set while mounted.
    public var mountedAt: Int64?

    public var isMounted: Bool { state == .mounted }

    /// The translatable `error`.
    public var errorText: CoreText { CoreText(code: errorCode, params: errorParams, message: error) }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        connectionId = c.string("connectionId")
        remotePath = c.string("remotePath")
        mountPoint = c.string("mountPoint")
        volumeName = c.string("volumeName")
        mountType = c.value("mountType", .nfsmount)
        autoMount = c.bool("autoMount", true)
        readOnly = c.bool("readOnly")
        cacheMaxGB = c.int("cacheMaxGB", 20)
        options = c.stringMap("options")
        state = c.value("state", .unmounted)
        error = c.string("error")
        errorCode = c.string("errorCode")
        errorParams = c.stringMap("errorParams")
        createdAt = c.int64("createdAt")
        mountedAt = c.optionalInt64("mountedAt")
    }
}

public struct FuseStatus: Decodable, Sendable, Hashable {
    public var fuseT: Bool
    public var macFUSE: Bool
    public var available: Bool { fuseT || macFUSE }

    public init(fuseT: Bool = false, macFUSE: Bool = false) {
        self.fuseT = fuseT
        self.macFUSE = macFUSE
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        fuseT = c.bool("fuseT")
        macFUSE = c.bool("macFUSE")
    }
}

// MARK: - Offline Items

public struct SyncProgress: Decodable, Sendable, Hashable {
    public var bytes: Int64
    public var totalBytes: Int64
    public var transfers: Int
    /// Estimated seconds remaining, if known.
    public var eta: Double?

    /// Completed fraction in 0...1, or nil when the total is unknown.
    public var fraction: Double? {
        guard totalBytes > 0 else { return nil }
        return min(1, max(0, Double(bytes) / Double(totalBytes)))
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        bytes = c.int64("bytes")
        totalBytes = c.int64("totalBytes")
        transfers = c.int("transfers")
        eta = c.optionalDouble("eta")
    }
}

public struct OfflineItem: Decodable, Sendable, Hashable, Identifiable {
    public var id: String
    public var connectionId: String
    public var kind: OfflineKind
    public var remotePath: String
    public var files: [String]
    public var storagePath: String
    public var excludes: [String]
    public var advanced: [String: JSONValue]
    public var state: OfflineState
    /// A pause reason id (`paused`) or rclone's error text (`error`).
    public var reason: String
    public var reasonCode: String
    public var reasonParams: [String: String]
    public var lastSyncAt: Int64?
    public var needsResync: Bool
    public var createdAt: Int64
    public var progress: SyncProgress?
    public var isVault: Bool
    /// Why the Mass-Delete Guard stopped the item; only for `needsConfirmation`.
    public var massDelete: MassDeleteInfo?

    /// The translatable error of an item in the `error` state (`reason` with its code).
    public var errorText: CoreText { CoreText(code: reasonCode, params: reasonParams, message: reason) }

    /// A short display name: the folder name, or the names of the selected paths for `files` items.
    public var displayName: String {
        if kind == .files, !files.isEmpty {
            return files.map { ($0 as NSString).lastPathComponent }.joined(separator: ", ")
        }
        let last = remotePath.split(separator: "/").last.map(String.init) ?? ""
        return last.isEmpty ? (storagePath as NSString).lastPathComponent : last
    }

    /// The Connection-relative paths the item syncs ("" = the whole Connection).
    public var coveredPaths: [String] {
        guard kind == .files else { return [remotePath] }
        return files.map { remotePath.isEmpty ? $0 : remotePath + "/" + $0 }
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        connectionId = c.string("connectionId")
        kind = c.value("kind", .folder)
        remotePath = c.string("remotePath")
        files = c.value("files", [String]())
        storagePath = c.string("storagePath")
        excludes = c.value("excludes", [String]())
        advanced = c.value("advanced", [String: JSONValue]())
        state = c.value("state", .pending)
        reason = c.string("reason")
        reasonCode = c.string("reasonCode")
        reasonParams = c.stringMap("reasonParams")
        lastSyncAt = c.optionalInt64("lastSyncAt")
        needsResync = c.bool("needsResync")
        createdAt = c.int64("createdAt")
        progress = c.optional("progress")
        isVault = c.bool("isVault")
        massDelete = c.optional("massDelete")
    }
}

/// Details of a Mass-Delete Guard stop.
public struct MassDeleteInfo: Decodable, Sendable, Hashable {
    /// tooManyDeletes | allChanged
    public var reason: String
    /// local | cloud: the side on which the files were deleted or changed.
    public var side: String
    /// Deleted files and files before the run (both 0 for allChanged).
    public var deletes: Int
    public var total: Int

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        reason = c.string("reason")
        side = c.string("side")
        deletes = c.int("deletes")
        total = c.int("total")
    }
}

public struct OfflineProgressEvent: Decodable, Sendable, Hashable {
    public var id: String
    public var progress: SyncProgress

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        progress = try SyncProgress(from: decoder)
    }
}

public struct PreflightResult: Decodable, Sendable, Hashable {
    public var remoteBytes: Int64
    public var freeBytes: Int64
    public var storageNonEmpty: Bool
    public var storagePath: String

    /// Mirrors the Core's rule: free space must be at least 1.1 × the cloud size.
    public var hasEnoughSpace: Bool { Double(freeBytes) >= 1.1 * Double(remoteBytes) }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        remoteBytes = c.int64("remoteBytes")
        freeBytes = c.int64("freeBytes")
        storageNonEmpty = c.bool("storageNonEmpty")
        storagePath = c.string("storagePath")
    }
}

public struct SyncRun: Decodable, Sendable, Hashable, Identifiable {
    public var id: Int64
    public var itemId: String
    public var kind: String
    public var startedAt: Int64
    public var finishedAt: Int64?
    public var status: String
    public var transferred: Int
    public var deleted: Int
    public var conflicts: Int
    public var bytes: Int64
    public var error: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.int64("id")
        itemId = c.string("itemId")
        kind = c.string("kind")
        startedAt = c.int64("startedAt")
        finishedAt = c.optionalInt64("finishedAt")
        status = c.string("status")
        transferred = c.int("transferred")
        deleted = c.int("deleted")
        conflicts = c.int("conflicts")
        bytes = c.int64("bytes")
        error = c.string("error")
    }
}

public struct SyncRunFile: Decodable, Sendable, Hashable, Identifiable {
    public var action: String
    public var path: String
    public var id: String { action + ":" + path }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        action = c.string("action")
        path = c.string("path")
    }
}

public struct StatusForPathsResult: Decodable, Sendable {
    public var statuses: [String: FileSyncStatus]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        statuses = c.stringMap("statuses").mapValues { FileSyncStatus(rawValue: $0) }
    }
}

// MARK: - Pause

public struct ActiveRule: Decodable, Sendable, Hashable, Identifiable {
    public var id: String
    public var detail: String
    public var code: String
    public var params: [String: String]
    public var message: String

    /// What holds syncing, in words; a Core without codes only sends `detail`.
    public var text: CoreText { CoreText(code: code, params: params, message: message.isEmpty ? detail : message) }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        detail = c.string("detail")
        code = c.string("code")
        params = c.stringMap("params")
        message = c.string("message")
    }
}

public struct PauseStatus: Decodable, Sendable, Hashable {
    /// Unix ms; `-1` means "until resumed"; nil means no manual pause.
    public var manualUntil: Int64?
    public var activeRules: [ActiveRule]
    public var effective: Bool

    public var isManuallyPaused: Bool { manualUntil != nil }
    public var isIndefinite: Bool { manualUntil == -1 }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        manualUntil = c.optionalInt64("manualUntil")
        activeRules = c.array("activeRules")
        effective = c.bool("effective")
    }
}

// MARK: - Shares

public struct Share: Decodable, Sendable, Hashable, Identifiable {
    public var id: String
    public var kind: ShareKind
    public var path: String
    public var itemType: String
    public var url: String
    public var token: String
    public var shareWith: String
    public var shareWithDisplayName: String
    public var permissions: Int
    public var expireDate: String?
    public var hasPassword: Bool
    public var hideDownload: Bool
    public var label: String
    public var note: String
    public var createdAt: Int64

    public var isFolder: Bool { itemType == "folder" }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        kind = c.value("kind", .publicLink)
        path = c.string("path")
        itemType = c.string("itemType", "file")
        url = c.string("url")
        token = c.string("token")
        shareWith = c.string("shareWith")
        shareWithDisplayName = c.string("shareWithDisplayName")
        permissions = c.int("permissions", 1)
        let expire = c.optionalString("expireDate")
        expireDate = (expire?.isEmpty ?? true) ? nil : expire
        hasPassword = c.bool("hasPassword")
        hideDownload = c.bool("hideDownload")
        label = c.string("label")
        note = c.string("note")
        createdAt = c.int64("createdAt")
    }
}

public struct SharePolicy: Decodable, Sendable, Hashable {
    public var passwordEnforced: Bool
    public var expireDateEnforced: Bool
    public var expireDateDays: Int
    public var defaultExpireDate: Bool

    public init(passwordEnforced: Bool = false, expireDateEnforced: Bool = false, expireDateDays: Int = 0,
                defaultExpireDate: Bool = false)
    {
        self.passwordEnforced = passwordEnforced
        self.expireDateEnforced = expireDateEnforced
        self.expireDateDays = expireDateDays
        self.defaultExpireDate = defaultExpireDate
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        passwordEnforced = c.bool("passwordEnforced")
        expireDateEnforced = c.bool("expireDateEnforced")
        expireDateDays = c.int("expireDateDays")
        defaultExpireDate = c.bool("defaultExpireDate")
    }
}

public struct ShareCapabilities: Decodable, Sendable, Hashable {
    public var publicLink: Bool
    public var internalLink: Bool
    public var userShare: Bool
    public var emailShare: Bool
    public var webURL: Bool
    public var manage: Bool
    /// Public links can expire (Nextcloud and the rclone backends that honour an expiry).
    public var linkExpiry: Bool
    public var reason: String?

    public static let none = ShareCapabilities()

    public var anySharing: Bool { publicLink || userShare || emailShare || internalLink }

    public init(publicLink: Bool = false, internalLink: Bool = false, userShare: Bool = false,
                emailShare: Bool = false, webURL: Bool = false, manage: Bool = false, linkExpiry: Bool = false,
                reason: String? = nil)
    {
        self.publicLink = publicLink
        self.internalLink = internalLink
        self.userShare = userShare
        self.emailShare = emailShare
        self.webURL = webURL
        self.manage = manage
        self.linkExpiry = linkExpiry
        self.reason = reason
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        publicLink = c.bool("publicLink")
        internalLink = c.bool("internalLink")
        userShare = c.bool("userShare")
        emailShare = c.bool("emailShare")
        webURL = c.bool("webURL")
        manage = c.bool("manage")
        linkExpiry = c.bool("linkExpiry")
        reason = c.optionalString("reason")
    }
}

/// `shares.delete` result.
public struct ShareDeleteResult: Decodable, Sendable, Hashable {
    /// Removed from CloudWire only: the provider still serves the link (rclone backends without unlink).
    public var remoteStillActive: Bool

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        remoteStillActive = c.bool("remoteStillActive")
    }
}

public struct Sharee: Decodable, Sendable, Hashable, Identifiable {
    public var label: String
    /// 0 = user, 1 = group, 4 = email.
    public var shareType: Int
    public var shareWith: String
    public var id: String { "\(shareType):\(shareWith)" }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        label = c.string("label")
        shareType = c.int("shareType")
        shareWith = c.string("shareWith")
    }
}

public struct URLResult: Decodable, Sendable, Hashable {
    public var url: String
    public var created: Bool

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        url = c.string("url")
        created = c.bool("created")
    }
}

// MARK: - Vaults

public struct Vault: Decodable, Sendable, Hashable, Identifiable {
    public var id: String
    public var connectionId: String
    public var vaultPath: String
    public var name: String
    public var vaultConnectionId: String
    public var unlockMode: UnlockMode
    public var unlocked: Bool
    public var createdAt: Int64

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.string("id")
        connectionId = c.string("connectionId")
        vaultPath = c.string("vaultPath")
        name = c.string("name")
        vaultConnectionId = c.string("vaultConnectionId")
        unlockMode = c.value("unlockMode", .keychain)
        unlocked = c.bool("unlocked")
        createdAt = c.int64("createdAt")
    }
}

public struct VaultCreateResult: Decodable, Sendable, Hashable {
    public var vault: Vault
    public var recoveryKey: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        vault = try c.decode(Vault.self, forKey: AnyKey("vault"))
        recoveryKey = c.string("recoveryKey")
    }
}

public struct EncryptExistingResult: Decodable, Sendable, Hashable {
    public var jobId: String
    public var vaultId: String
    public var recoveryKey: String?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        jobId = c.string("jobId")
        vaultId = c.string("vaultId")
        let key = c.optionalString("recoveryKey")
        recoveryKey = (key?.isEmpty ?? true) ? nil : key
    }
}

public struct VaultMigrationEvent: Decodable, Sendable, Hashable, Identifiable {
    public var jobId: String
    public var vaultId: String
    /// Source Connection and the source path relative to it.
    public var connectionId: String
    public var path: String
    public var isDir: Bool
    /// queued | running | verified | mismatch | error | deleted | canceled
    public var status: String
    public var mismatches: [String]
    public var error: String?
    /// Latest copy progress; 0 until the worker reported it.
    public var bytes: Int64
    public var totalBytes: Int64
    public var transfers: Int64
    public var createdAt: Int64

    public var id: String { jobId }

    /// The job is waiting or copying and can still be canceled.
    public var isActive: Bool { status == "queued" || status == "running" }

    /// Completed fraction in 0...1, or nil when the total is unknown.
    public var fraction: Double? {
        guard totalBytes > 0 else { return nil }
        return min(1, max(0, Double(bytes) / Double(totalBytes)))
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        jobId = c.string("jobId")
        vaultId = c.string("vaultId")
        connectionId = c.string("connectionId")
        path = c.string("path")
        isDir = c.bool("isDir")
        status = c.string("status")
        mismatches = c.value("mismatches", [String]())
        error = c.optionalString("error")
        bytes = c.int64("bytes")
        totalBytes = c.int64("totalBytes")
        transfers = c.int64("transfers")
        createdAt = c.int64("createdAt")
    }
}

public struct ExportRcloneResult: Decodable, Sendable {
    public var ini: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        ini = c.string("ini")
    }
}

// MARK: - Paths and Finder

public struct PathResolution: Decodable, Sendable, Hashable {
    public var connectionId: String
    public var remotePath: String
    public var context: RootKind
    public var itemId: String
    public var isVault: Bool

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        connectionId = c.string("connectionId")
        remotePath = c.string("remotePath")
        context = c.value("context", .mount)
        itemId = c.string("itemId")
        isVault = c.bool("isVault")
    }
}

public struct FinderRoot: Decodable, Sendable, Hashable {
    public var path: String
    public var kind: RootKind
    public var itemId: String
    public var connectionId: String
    public var remoteRoot: String
    public var isVault: Bool
    public var offlineKind: String
    public var capabilities: ShareCapabilities

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        path = c.string("path")
        kind = c.value("kind", .mount)
        itemId = c.string("itemId")
        connectionId = c.string("connectionId")
        remoteRoot = c.string("remoteRoot")
        isVault = c.bool("isVault")
        offlineKind = c.string("offlineKind")
        capabilities = c.value("capabilities", ShareCapabilities.none)
    }
}

public struct FinderRoots: Decodable, Sendable, Hashable {
    public var token: String
    public var roots: [FinderRoot]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        token = c.string("token")
        roots = c.array("roots")
    }
}

public struct TokenValidation: Decodable, Sendable {
    public var valid: Bool

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        valid = c.bool("valid")
    }
}

// MARK: - Activity, notifications, updates

public struct ActivityEntry: Decodable, Sendable, Hashable, Identifiable {
    public var id: Int64
    public var ts: Int64
    public var level: ActivityLevel
    public var category: ActivityCategory
    public var subjectId: String
    /// English text; entries written before message codes existed only have this.
    public var message: String
    public var code: String
    public var params: [String: String]
    public var details: JSONValue?

    /// The translatable text of the entry.
    public var text: CoreText { CoreText(code: code, params: params, message: message) }

    public var date: Date { Date(timeIntervalSince1970: TimeInterval(ts) / 1000) }

    /// The sync run this entry summarises, when the Core attached one.
    public var runId: Int64? { details?["runId"]?.int64Value }

    /// Whether the element this problem is about is healthy again since the problem was logged: a
    /// Mount that came up afterwards, an Offline Item that synced without errors afterwards, or an
    /// element that no longer exists. Problems of other categories stay until dismissed.
    public func isResolved(mounts: [Mount], offlineItems: [OfflineItem]) -> Bool {
        guard !subjectId.isEmpty else { return false }
        switch category {
        case .mount:
            guard let mount = mounts.first(where: { $0.id == subjectId }) else { return true }
            return mount.state == .mounted && (mount.mountedAt ?? .min) > ts
        case .offline, .sync:
            guard let item = offlineItems.first(where: { $0.id == subjectId }) else { return true }
            return item.state == .idle && (item.lastSyncAt ?? .min) > ts
        default:
            return false
        }
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.int64("id")
        ts = c.int64("ts")
        level = c.value("level", .info)
        category = c.value("category", .core)
        subjectId = c.string("subjectId")
        message = c.string("message")
        code = c.string("code")
        params = c.stringMap("params")
        let raw = c.optional("details", as: JSONValue.self)
        details = (raw?.isNull ?? true) ? nil : raw
    }
}

public struct CoreNotification: Decodable, Sendable, Hashable, Identifiable {
    public var id: Int64
    public var ts: Int64
    public var kind: NotificationKind
    public var params: JSONValue

    public var title: String { params["title"]?.stringValue ?? "" }
    public var message: String { params["message"]?.stringValue ?? "" }
    public var subjectId: String { params["subjectId"]?.stringValue ?? "" }
    public var itemId: String { params["itemId"]?.stringValue ?? "" }
    public var itemName: String { params["itemName"]?.stringValue ?? "" }
    public var files: [String] { params["files"]?.arrayValue?.compactMap(\.stringValue) ?? [] }

    /// The translatable body of an `error` notification (`message` with `code` and `params`).
    public var text: CoreText {
        CoreText(code: params["code"]?.stringValue, params: params["params"]?.stringMap ?? [:], message: message)
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        id = c.int64("id")
        ts = c.int64("ts")
        kind = c.value("kind", .error)
        params = c.value("params", JSONValue.object([:]))
    }
}

public struct UpdateStatus: Decodable, Sendable, Hashable {
    public var current: String
    public var latest: String?
    public var url: String?
    public var available: Bool
    public var checkedAt: Int64?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        current = c.string("current")
        latest = c.optionalString("latest")
        url = c.optionalString("url")
        available = c.bool("available")
        checkedAt = c.optionalInt64("checkedAt")
    }
}

public struct UpdateAvailableEvent: Decodable, Sendable, Hashable {
    public var version: String
    public var url: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        version = c.string("version")
        url = c.string("url")
    }
}

public struct ExportResult: Decodable, Sendable {
    public var count: Int

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        count = c.int("count")
    }
}
