import Foundation

/// Typed wrappers for every method of the Core contract (apiVersion 1).
extension CoreClient {
    // MARK: core

    public func coreInfo(timeout: Duration? = .seconds(5)) async throws -> CoreInfo {
        try await call("core.info", as: CoreInfo.self, timeout: timeout)
    }

    public func shutdown() async throws {
        _ = try await call("core.shutdown", as: EmptyResult.self, timeout: .seconds(30))
    }

    // MARK: settings

    public func settings() async throws -> CoreSettings {
        try await call("settings.get")
    }

    /// Applies an RFC 7396 merge patch and returns the resulting settings.
    public func updateSettings(_ partial: JSONValue) async throws -> CoreSettings {
        try await call("settings.update", params: JSONValue.object(["partial": partial]))
    }

    // MARK: providers and options

    public func providers() async throws -> [RcloneProvider] {
        try await call("providers.list", as: ProvidersList.self).providers
    }

    public func mountOptionsInfo() async throws -> MountOptionsInfo {
        try await call("options.mountInfo")
    }

    public func mainOptionsInfo() async throws -> MainOptionsInfo {
        try await call("options.mainInfo")
    }

    // MARK: connections

    public func connections() async throws -> [Connection] {
        try await call("connections.list")
    }

    public func createConnection(name: String, provider: String, parameters: [String: String]) async throws
        -> ConfigStep
    {
        try await call("connections.create",
                       params: JSONValue.params(["name": name, "provider": provider, "parameters": parameters]))
    }

    public func continueConnection(connectionId: String, state: String, result: String) async throws -> ConfigStep {
        try await call("connections.continue",
                       params: JSONValue.params(["connectionId": connectionId, "state": state, "result": result]))
    }

    /// Abandons a setup still at a question step and deletes its partial remote; unknown ids are ignored.
    public func cancelConnectionSetup(connectionId: String) async throws {
        _ = try await call("connections.cancelSetup", params: JSONValue.params(["connectionId": connectionId]),
                           as: EmptyResult.self)
    }

    public func updateConnection(id: String, name: String? = nil, parameters: [String: String]? = nil)
        async throws -> Connection
    {
        try await call("connections.update", params: JSONValue.params(["id": id, "name": name, "parameters": parameters]))
    }

    public func deleteConnection(id: String) async throws {
        _ = try await call("connections.delete", params: JSONValue.params(["id": id]), as: EmptyResult.self)
    }

    public func testConnection(id: String) async throws -> ConnectionTestResult {
        try await call("connections.test", params: JSONValue.params(["id": id]))
    }

    public func browse(connectionId: String, path: String) async throws -> [BrowseEntry] {
        try await call("connections.browse", params: JSONValue.params(["connectionId": connectionId, "path": path]))
    }

    public func nextcloudLoginStart(serverURL: String, name: String?) async throws -> NextcloudLoginStart {
        try await call("connections.nextcloudLoginStart",
                       params: JSONValue.params(["serverURL": serverURL, "name": name]))
    }

    /// Signs an existing Nextcloud Connection in again; the result arrives as a login event.
    public func nextcloudLoginRenew(connectionId: String) async throws -> NextcloudLoginStart {
        try await call("connections.nextcloudLoginStart", params: JSONValue.params(["connectionId": connectionId]))
    }

    public func nextcloudLoginCancel(flowId: String) async throws {
        _ = try await call("connections.nextcloudLoginCancel", params: JSONValue.params(["flowId": flowId]),
                           as: EmptyResult.self)
    }

    public func nextcloudManual(serverURL: String, user: String, appPassword: String, name: String?) async throws
        -> Connection
    {
        try await call("connections.nextcloudManual",
                       params: JSONValue.params([
                           "serverURL": serverURL, "user": user, "appPassword": appPassword, "name": name,
                       ]))
    }

    // MARK: mounts

    public func mounts() async throws -> [Mount] {
        try await call("mounts.list")
    }

    /// `fields` may contain any Mount field except `id`, `connectionId` and `state`.
    public func createMount(connectionId: String, remotePath: String, fields: [String: JSONValue] = [:])
        async throws -> Mount
    {
        var params = fields
        params["connectionId"] = .string(connectionId)
        params["remotePath"] = .string(remotePath)
        return try await call("mounts.create", params: JSONValue.object(params))
    }

    public func updateMount(id: String, fields: [String: JSONValue]) async throws -> Mount {
        var params = fields
        params["id"] = .string(id)
        return try await call("mounts.update", params: JSONValue.object(params))
    }

    public func deleteMount(id: String) async throws {
        _ = try await call("mounts.delete", params: JSONValue.params(["id": id]), as: EmptyResult.self)
    }

    public func mount(id: String) async throws -> Mount {
        try await call("mounts.mount", params: JSONValue.params(["id": id]))
    }

    public func unmount(id: String) async throws -> Mount {
        try await call("mounts.unmount", params: JSONValue.params(["id": id]))
    }

    public func mountStats(id: String) async throws -> JSONValue {
        try await call("mounts.stats", params: JSONValue.params(["id": id]))
    }

    public func fuseStatus() async throws -> FuseStatus {
        try await call("mounts.fuseStatus")
    }

    // MARK: offline

    public func offlineItems() async throws -> [OfflineItem] {
        try await call("offline.list")
    }

    public func offlinePreflight(connectionId: String, kind: OfflineKind, remotePath: String, files: [String]?,
                                 storagePath: String?) async throws -> PreflightResult
    {
        try await call("offline.preflight",
                       params: JSONValue.params([
                           "connectionId": connectionId, "kind": kind.rawValue, "remotePath": remotePath,
                           "files": files, "storagePath": storagePath,
                       ]))
    }

    public func createOfflineItem(connectionId: String, kind: OfflineKind, remotePath: String, files: [String]?,
                                  storagePath: String?, excludes: [String]?, mergeExisting: Bool) async throws
        -> OfflineItem
    {
        try await call("offline.create",
                       params: JSONValue.params([
                           "connectionId": connectionId, "kind": kind.rawValue, "remotePath": remotePath,
                           "files": files, "storagePath": storagePath, "excludes": excludes,
                           "mergeExisting": mergeExisting,
                       ]))
    }

    public func updateOfflineItem(id: String, excludes: [String]?, advanced: [String: JSONValue]?) async throws
        -> OfflineItem
    {
        try await call("offline.update",
                       params: JSONValue.params(["id": id, "excludes": excludes, "advanced": advanced]))
    }

    public func relocateOfflineItem(id: String, newPath: String) async throws -> OfflineItem {
        try await call("offline.relocate", params: JSONValue.params(["id": id, "newPath": newPath]))
    }

    public enum LocalCopyAction: String, Sendable {
        case trash
        case keep
    }

    public func removeOfflineItem(id: String, localCopy: LocalCopyAction) async throws {
        _ = try await call("offline.remove", params: JSONValue.params(["id": id, "localCopy": localCopy.rawValue]),
                           as: EmptyResult.self)
    }

    /// Replaces an item's Selection; `localCopy` decides what happens to deselected local parts.
    public func setOfflineSelection(id: String, kind: OfflineKind, files: [String]?,
                                    localCopy: LocalCopyAction?) async throws -> OfflineItem
    {
        try await call("offline.setSelection",
                       params: JSONValue.params([
                           "id": id, "kind": kind.rawValue, "files": files, "localCopy": localCopy?.rawValue,
                       ]))
    }

    /// Syncs one item now, or all items when `id` is nil. Ignores Pause Rules.
    public func syncNow(id: String? = nil) async throws {
        _ = try await call("offline.syncNow", params: JSONValue.params(["id": id]), as: EmptyResult.self)
    }

    public enum MassDeleteAction: String, Sendable {
        case delete
        case restore
    }

    public func confirmMassDelete(id: String, action: MassDeleteAction) async throws -> OfflineItem {
        try await call("offline.confirmMassDelete", params: JSONValue.params(["id": id, "action": action.rawValue]))
    }

    public func statusForPaths(_ paths: [String], timeout: Duration? = .seconds(5)) async throws
        -> [String: FileSyncStatus]
    {
        try await call("offline.statusForPaths", params: JSONValue.params(["paths": paths]),
                       as: StatusForPathsResult.self, timeout: timeout).statuses
    }

    public func syncRuns(itemId: String, limit: Int? = nil) async throws -> [SyncRun] {
        try await call("offline.runs", params: JSONValue.params(["itemId": itemId, "limit": limit]))
    }

    public func syncRunFiles(runId: Int64) async throws -> [SyncRunFile] {
        try await call("offline.runFiles", params: JSONValue.params(["runId": runId]))
    }

    // MARK: pause

    public func pauseStatus() async throws -> PauseStatus {
        try await call("pause.status")
    }

    /// Pauses until `date`.
    public func pause(until date: Date) async throws -> PauseStatus {
        let ms = Int64((date.timeIntervalSince1970 * 1000).rounded())
        return try await call("pause.set", params: JSONValue.params(["until": ms]))
    }

    public func pauseIndefinitely() async throws -> PauseStatus {
        try await call("pause.set", params: JSONValue.params(["indefinite": true]))
    }

    public func resume() async throws -> PauseStatus {
        try await call("pause.set", params: JSONValue.object([:]))
    }

    // MARK: shares

    public func shareCapabilities(connectionId: String) async throws -> ShareCapabilities {
        try await call("shares.capabilities", params: JSONValue.params(["connectionId": connectionId]))
    }

    public func shares(connectionId: String, path: String? = nil) async throws -> [Share] {
        try await call("shares.list", params: JSONValue.params(["connectionId": connectionId, "path": path]))
    }

    public struct ShareRequest: Sendable, Hashable {
        public var kind: ShareKind
        public var password: String?
        /// YYYY-MM-DD
        public var expireDate: String?
        public var permissions: Int?
        public var hideDownload: Bool?
        public var label: String?
        public var note: String?
        public var shareWith: String?
        public var sendMail: Bool?

        public init(kind: ShareKind, password: String? = nil, expireDate: String? = nil, permissions: Int? = nil,
                    hideDownload: Bool? = nil, label: String? = nil, note: String? = nil, shareWith: String? = nil,
                    sendMail: Bool? = nil)
        {
            self.kind = kind
            self.password = password
            self.expireDate = expireDate
            self.permissions = permissions
            self.hideDownload = hideDownload
            self.label = label
            self.note = note
            self.shareWith = shareWith
            self.sendMail = sendMail
        }
    }

    public func createShare(connectionId: String, path: String, request: ShareRequest) async throws -> Share {
        try await call("shares.create",
                       params: JSONValue.params([
                           "connectionId": connectionId, "path": path, "kind": request.kind.rawValue,
                           "password": request.password, "expireDate": request.expireDate,
                           "permissions": request.permissions, "hideDownload": request.hideDownload,
                           "label": request.label, "note": request.note, "shareWith": request.shareWith,
                           "sendMail": request.sendMail,
                       ]))
    }

    /// Only non-nil fields change. `expireDate: ""` clears the expiry.
    public func updateShare(connectionId: String, id: String, password: String? = nil, expireDate: String? = nil,
                            permissions: Int? = nil, hideDownload: Bool? = nil, label: String? = nil,
                            note: String? = nil) async throws -> Share
    {
        try await call("shares.update",
                       params: JSONValue.params([
                           "connectionId": connectionId, "id": id, "password": password, "expireDate": expireDate,
                           "permissions": permissions, "hideDownload": hideDownload, "label": label, "note": note,
                       ]))
    }

    @discardableResult
    public func deleteShare(connectionId: String, id: String) async throws -> ShareDeleteResult {
        try await call("shares.delete", params: JSONValue.params(["connectionId": connectionId, "id": id]))
    }

    public func searchSharees(connectionId: String, search: String, itemType: String) async throws -> [Sharee] {
        try await call("shares.searchSharees",
                       params: JSONValue.params(["connectionId": connectionId, "search": search, "itemType": itemType]))
    }

    public func internalLink(connectionId: String, path: String) async throws -> String {
        try await call("shares.internalLink", params: JSONValue.params(["connectionId": connectionId, "path": path]),
                       as: URLResult.self).url
    }

    public func webURL(connectionId: String, path: String) async throws -> String {
        try await call("shares.webURL", params: JSONValue.params(["connectionId": connectionId, "path": path]),
                       as: URLResult.self).url
    }

    public func copyPublicLink(connectionId: String, path: String) async throws -> URLResult {
        try await call("shares.copyPublicLink", params: JSONValue.params(["connectionId": connectionId, "path": path]))
    }

    public func sharePolicy(connectionId: String) async throws -> SharePolicy {
        try await call("shares.policy", params: JSONValue.params(["connectionId": connectionId]))
    }

    // MARK: vaults

    public func vaults() async throws -> [Vault] {
        try await call("vaults.list")
    }

    public func createVault(connectionId: String, parentPath: String, name: String, password: String,
                            unlockMode: UnlockMode) async throws -> VaultCreateResult
    {
        try await call("vaults.create",
                       params: JSONValue.params([
                           "connectionId": connectionId, "parentPath": parentPath, "name": name,
                           "password": password, "unlockMode": unlockMode.rawValue,
                       ]))
    }

    public func openVault(connectionId: String, vaultPath: String, password: String, unlockMode: UnlockMode)
        async throws -> Vault
    {
        try await call("vaults.open",
                       params: JSONValue.params([
                           "connectionId": connectionId, "vaultPath": vaultPath, "password": password,
                           "unlockMode": unlockMode.rawValue,
                       ]))
    }

    /// Unlocks with `password`, or from the Keychain when nil.
    public func unlockVault(id: String, password: String? = nil) async throws -> Vault {
        try await call("vaults.unlock", params: JSONValue.params(["id": id, "password": password]))
    }

    public func lockVault(id: String) async throws -> Vault {
        try await call("vaults.lock", params: JSONValue.params(["id": id]))
    }

    public func changeVaultPassword(id: String, oldPassword: String, newPassword: String) async throws {
        _ = try await call("vaults.changePassword",
                           params: JSONValue.params(["id": id, "oldPassword": oldPassword, "newPassword": newPassword]),
                           as: EmptyResult.self)
    }

    public func recoverVault(connectionId: String, vaultPath: String, recoveryKey: String, newPassword: String)
        async throws -> Vault
    {
        try await call("vaults.recover",
                       params: JSONValue.params([
                           "connectionId": connectionId, "vaultPath": vaultPath, "recoveryKey": recoveryKey,
                           "newPassword": newPassword,
                       ]))
    }

    public func exportVaultRclone(id: String, password: String) async throws -> String {
        try await call("vaults.exportRclone", params: JSONValue.params(["id": id, "password": password]),
                       as: ExportRcloneResult.self).ini
    }

    public func removeVault(id: String) async throws {
        _ = try await call("vaults.remove", params: JSONValue.params(["id": id]), as: EmptyResult.self)
    }

    public enum EncryptTarget: Sendable, Hashable {
        case newVault(name: String, password: String, unlockMode: UnlockMode)
        case existingVault(vaultId: String, subPath: String)

        var json: JSONValue {
            switch self {
            case .newVault(let name, let password, let unlockMode):
                return .object([
                    "newVault": .object([
                        "name": .string(name), "password": .string(password), "unlockMode": .string(unlockMode.rawValue),
                    ]),
                ])
            case .existingVault(let vaultId, let subPath):
                return .object(["vaultId": .string(vaultId), "subPath": .string(subPath)])
            }
        }
    }

    public func encryptExisting(connectionId: String, path: String, isDir: Bool, target: EncryptTarget,
                                ignorePauseRules: Bool) async throws -> EncryptExistingResult
    {
        try await call("vaults.encryptExisting",
                       params: JSONValue.params([
                           "connectionId": connectionId, "path": path, "isDir": isDir, "target": target.json,
                           "ignorePauseRules": ignorePauseRules,
                       ]))
    }

    public func migrationConfirmDelete(jobId: String) async throws {
        _ = try await call("vaults.migrationConfirmDelete", params: JSONValue.params(["jobId": jobId]),
                           as: EmptyResult.self)
    }

    /// All encryption jobs the Core knows, newest first.
    public func vaultMigrations() async throws -> [VaultMigrationEvent] {
        try await call("vaults.migrations")
    }

    /// Cancels a queued or running encryption job.
    public func cancelMigration(jobId: String) async throws {
        _ = try await call("vaults.migrationCancel", params: JSONValue.params(["jobId": jobId]), as: EmptyResult.self)
    }

    // MARK: paths, Finder, activity, notifications, updates

    public func resolvePath(_ localPath: String) async throws -> PathResolution {
        try await call("paths.resolve", params: JSONValue.params(["localPath": localPath]))
    }

    public func finderRoots() async throws -> FinderRoots {
        try await call("finder.roots", timeout: .seconds(10))
    }

    public func validateToken(_ token: String) async throws -> Bool {
        try await call("finder.validateToken", params: JSONValue.params(["token": token]), as: TokenValidation.self)
            .valid
    }

    public struct ActivityFilter: Sendable, Hashable {
        public var levels: [ActivityLevel]
        public var categories: [ActivityCategory]
        public var search: String
        public var before: Int64?
        public var limit: Int?

        public init(levels: [ActivityLevel] = [], categories: [ActivityCategory] = [], search: String = "",
                    before: Int64? = nil, limit: Int? = nil)
        {
            self.levels = levels
            self.categories = categories
            self.search = search
            self.before = before
            self.limit = limit
        }
    }

    public func activity(_ filter: ActivityFilter) async throws -> [ActivityEntry] {
        try await call("activity.query",
                       params: JSONValue.params([
                           "levels": filter.levels.isEmpty ? nil : filter.levels.map(\.rawValue),
                           "categories": filter.categories.isEmpty ? nil : filter.categories.map(\.rawValue),
                           "search": filter.search.isEmpty ? nil : filter.search,
                           "before": filter.before, "limit": filter.limit,
                       ]))
    }

    public enum ExportFormat: String, Sendable {
        case json
        case csv
    }

    public func exportActivity(format: ExportFormat, path: String) async throws -> Int {
        try await call("activity.export", params: JSONValue.params(["format": format.rawValue, "path": path]),
                       as: ExportResult.self).count
    }

    public func pendingNotifications() async throws -> [CoreNotification] {
        try await call("notifications.pending")
    }

    public func acknowledgeNotifications(_ ids: [Int64]) async throws {
        _ = try await call("notifications.ack", params: JSONValue.params(["ids": ids]), as: EmptyResult.self)
    }

    public func updateStatus() async throws -> UpdateStatus {
        try await call("updates.status")
    }
}
