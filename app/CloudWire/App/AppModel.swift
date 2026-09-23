import AppKit
import CloudWireKit
import Observation
import SwiftUI

enum SidebarSection: String, CaseIterable, Identifiable, Hashable {
    case overview, connections, mounts, offline, shares, vaults, activity

    var id: String { rawValue }

    var title: String {
        switch self {
        case .overview: return String(localized: "Overview")
        case .connections: return String(localized: "Connections")
        case .mounts: return String(localized: "Mounts")
        case .offline: return String(localized: "Offline")
        case .shares: return String(localized: "Shares")
        case .vaults: return String(localized: "Vaults")
        case .activity: return String(localized: "Activity")
        }
    }

    var symbol: String {
        switch self {
        case .overview: return "gauge.with.dots.needle.33percent"
        case .connections: return "network"
        case .mounts: return "externaldrive.connected.to.line.below"
        case .offline: return "arrow.down.circle"
        case .shares: return "person.2"
        case .vaults: return "lock.shield"
        case .activity: return "list.bullet.rectangle"
        }
    }
}

enum CoreConnectionState: Equatable {
    case idle
    case starting
    case connected
    case needsApproval
    case failed(String)
}

/// The item a share window is about.
struct ShareTarget: Codable, Hashable {
    var connectionId: String
    var path: String
    var isDir: Bool
    var name: String
    /// Opens on the existing shares ("Freigaben verwalten…") instead of the create form.
    var manage: Bool = false
}

/// Prefill for the Offline add sheet (from Finder or a Vault).
struct OfflineDraft: Identifiable, Hashable {
    let id = UUID()
    var connectionId: String
    var remotePath: String
    var kind: OfflineKind
    var files: [String]
}

/// Prefill for the encrypt-existing sheet.
struct EncryptDraft: Identifiable, Hashable {
    let id = UUID()
    var connectionId: String
    var path: String
    var isDir: Bool
}

/// Overall state shown by the menu bar icon.
enum AggregateStatus {
    case idle, syncing, paused, error
}

@Observable
@MainActor
final class AppModel {
    static let shared = AppModel()

    let client: CoreClient
    /// Snapshot export: static demo data, no Core.
    private(set) var isDemo = false

    // Core state
    var coreState: CoreConnectionState = .idle
    var coreInfo: CoreInfo?
    var settings: CoreSettings = .defaults
    var hasSettings = false

    // Lists
    var connections: [Connection] = []
    var mounts: [Mount] = []
    var offlineItems: [OfflineItem] = []
    var vaults: [Vault] = []
    var pause: PauseStatus?
    var recentProblems: [ActivityEntry] = []
    var latestActivity: ActivityEntry?
    var updateStatus: UpdateStatus?
    var fuseStatus = FuseStatus()
    var migrations: [String: VaultMigrationEvent] = [:]
    var loginEvents: [String: NextcloudLoginEvent] = [:]
    var configStepEvents: [String: ConfigStep] = [:]

    // Navigation
    var selection: SidebarSection = .overview
    var offlineDraft: OfflineDraft?
    var encryptDraft: EncryptDraft?
    var offlineRemoval: OfflineItem?
    var showAddConnection = false
    var alert: AlertContent?

    // Metadata caches
    private(set) var providers: [RcloneProvider] = []
    private(set) var mountOptions: MountOptionsInfo?
    private(set) var mainOptions: MainOptionsInfo?

    @ObservationIgnored private var startTask: Task<Void, Never>?
    @ObservationIgnored private var eventTask: Task<Void, Never>?
    @ObservationIgnored private var stopping = false
    /// A connection loss reported while `connectLoop` still owned `startTask` (so `start()` was a no-op).
    @ObservationIgnored private var reconnectRequested = false
    @ObservationIgnored private var connectWaiters: [CheckedContinuation<Void, Never>] = []

    init(client: CoreClient = CoreClient()) {
        self.client = client
    }

    // MARK: Lookup

    var remoteConnections: [Connection] { connections.filter { $0.kind == .remote } }

    /// Connections usable as a source for Mounts and Offline Items: remotes and unlocked Vaults.
    var mountableConnections: [Connection] {
        connections.filter { connection in
            connection.kind == .remote
                || vaults.contains { $0.vaultConnectionId == connection.id && $0.unlocked }
        }
    }

    func connection(_ id: String) -> Connection? { connections.first { $0.id == id } }
    func connectionName(_ id: String) -> String {
        if let connection = connection(id) { return connection.name }
        if let vault = vaults.first(where: { $0.vaultConnectionId == id }) { return vault.name }
        return id
    }
    func vault(forConnection id: String) -> Vault? { vaults.first { $0.vaultConnectionId == id } }

    var aggregateStatus: AggregateStatus {
        if offlineItems.contains(where: { $0.state == .error || $0.state == .needsConfirmation })
            || mounts.contains(where: { $0.state == .error })
        {
            return .error
        }
        if offlineItems.contains(where: { $0.state == .syncing }) { return .syncing }
        if pause?.effective == true || offlineItems.contains(where: { $0.state == .paused }) { return .paused }
        return .idle
    }

    var isConnected: Bool { coreState == .connected }

    // MARK: Lifecycle

    /// Starts the Core (if needed), connects and keeps reconnecting after connection loss.
    func start() {
        guard !isDemo, startTask == nil else { return }
        stopping = false
        startTask = Task { [weak self] in
            await self?.connectLoop()
        }
    }

    /// Waits until connected (or `timeout`); returns whether the Core is reachable.
    func waitUntilConnected(timeout: Duration = .seconds(20)) async -> Bool {
        if isConnected { return true }
        start()
        let waiter = Task { @MainActor [weak self] in
            await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
                guard let self, !self.isConnected else {
                    continuation.resume()
                    return
                }
                self.connectWaiters.append(continuation)
            }
        }
        let timer = Task { @MainActor [weak self] in
            try? await Task.sleep(for: timeout)
            self?.releaseConnectWaiters()
        }
        await waiter.value
        timer.cancel()
        return isConnected
    }

    private func releaseConnectWaiters() {
        let waiters = connectWaiters
        connectWaiters.removeAll()
        waiters.forEach { $0.resume() }
    }

    private func connectLoop() async {
        var delay: Duration = .seconds(1)
        while !Task.isCancelled && !stopping {
            if coreState != .connected { coreState = .starting }
            do {
                let info = try await CoreLauncher.ensureRunning(client: client)
                coreInfo = info
                startEventLoop()
                reconnectRequested = false
                try await client.subscribeEvents(client: "app")
                // Lost again before the hand-over below: connect once more.
                if reconnectRequested { continue }
                coreState = .connected
                releaseConnectWaiters()
                // Hand over before the next suspension: from here on `.connectionLost` starts a new loop.
                releaseStartTask()
                await refreshAll()
                await deliverPendingNotifications()
                return
            } catch CoreLauncherError.requiresApproval {
                coreState = .needsApproval
                releaseConnectWaiters()
                releaseStartTask()
                return
            } catch {
                coreState = .failed(error.localizedDescription)
                releaseConnectWaiters()
                try? await Task.sleep(for: delay)
                delay = min(delay * 2, .seconds(30))
            }
        }
        releaseStartTask()
    }

    /// Ends this loop's ownership of `startTask`, unless `retryStart`/`shutdownCore` cancelled the
    /// loop and took it over.
    private func releaseStartTask() {
        if !Task.isCancelled { startTask = nil }
    }

    /// Re-runs the start sequence (after the user allowed the background item).
    func retryStart() {
        startTask?.cancel()
        startTask = nil
        start()
    }

    private func startEventLoop() {
        guard eventTask == nil else { return }
        let client = client
        eventTask = Task { [weak self] in
            let stream = await client.events()
            for await event in stream {
                guard let self else { return }
                self.handle(event)
            }
        }
    }

    /// "CloudWire vollständig beenden": stops the Core, then the App quits.
    func shutdownCore() async {
        stopping = true
        startTask?.cancel()
        startTask = nil
        try? await client.shutdown()
        await client.disconnect()
        coreState = .idle
    }

    // MARK: Events

    private func handle(_ event: CoreEvent) {
        switch event {
        case .mountStatus(let mount):
            upsert(&mounts, mount)
        case .offlineStatus(let item):
            upsert(&offlineItems, item)
        case .offlineProgress(let progress):
            if let index = offlineItems.firstIndex(where: { $0.id == progress.id }) {
                offlineItems[index].progress = progress.progress
            }
        case .pauseStatus(let status):
            pause = status
        case .activityAppended(let entry):
            latestActivity = entry
            if entry.level == .error || entry.level == .warn {
                recentProblems.insert(entry, at: 0)
                if recentProblems.count > 8 { recentProblems.removeLast(recentProblems.count - 8) }
            }
        case .notificationNew(let notification):
            NotificationManager.shared.post(notification)
            let client = client
            Task { try? await client.acknowledgeNotifications([notification.id]) }
        case .nextcloudLogin(let login):
            loginEvents[login.flowId] = login
        case .configStep(let step):
            configStepEvents[step.connectionId] = step
        case .connectionsChanged:
            Task { await refreshConnections() }
        case .vaultsChanged:
            Task { await refreshVaults() }
        case .vaultMigration(let migration):
            migrations[migration.jobId] = migration
        case .updateAvailable:
            Task { self.updateStatus = try? await self.client.updateStatus() }
        case .settingsChanged(let newSettings):
            apply(newSettings)
        case .unknown:
            break
        case .connectionLost:
            coreState = .starting
            if !stopping {
                if startTask != nil { reconnectRequested = true }
                start()
            }
        }
    }

    private func upsert<T: Identifiable>(_ list: inout [T], _ element: T) where T.ID == String {
        if let index = list.firstIndex(where: { $0.id == element.id }) {
            list[index] = element
        } else {
            list.append(element)
        }
    }

    // MARK: Refresh

    func refreshAll() async {
        guard !isDemo else { return }
        let client = client
        async let settingsResult = try? client.settings()
        async let connectionsResult = try? client.connections()
        async let mountsResult = try? client.mounts()
        async let offlineResult = try? client.offlineItems()
        async let vaultsResult = try? client.vaults()
        async let pauseResult = try? client.pauseStatus()
        async let updateResult = try? client.updateStatus()
        async let fuseResult = try? client.fuseStatus()
        async let problemsResult = try? client.activity(.init(levels: [.error, .warn], limit: 8))

        if let value = await settingsResult { apply(value) }
        if let value = await connectionsResult { connections = value }
        if let value = await mountsResult { mounts = value }
        if let value = await offlineResult { offlineItems = value }
        if let value = await vaultsResult { vaults = value }
        pause = await pauseResult
        updateStatus = await updateResult
        if let value = await fuseResult { fuseStatus = value }
        if let value = await problemsResult { recentProblems = value }
    }

    func refreshConnections() async {
        guard !isDemo, let value = try? await client.connections() else { return }
        connections = value
    }

    func refreshVaults() async {
        guard !isDemo else { return }
        if let value = try? await client.vaults() { vaults = value }
        await refreshConnections()
        if let value = try? await client.offlineItems() { offlineItems = value }
        if let value = try? await client.mounts() { mounts = value }
    }

    func refreshMounts() async {
        guard !isDemo, let value = try? await client.mounts() else { return }
        mounts = value
    }

    func refreshOffline() async {
        guard !isDemo, let value = try? await client.offlineItems() else { return }
        offlineItems = value
    }

    private func apply(_ newSettings: CoreSettings) {
        settings = newSettings
        hasSettings = true
        UserDefaults.standard.set(newSettings.menuBarIcon, forKey: "menuBarIcon")
        if !isDemo {
            CoreLauncher.updateLoginItem(enabled: newSettings.autostart && newSettings.menuBarIcon)
        }
    }

    // MARK: Settings

    /// Sends a merge patch; the model updates from the reply.
    func updateSettings(_ patch: JSONValue) {
        guard !isDemo else { return }
        perform {
            let updated = try await self.client.updateSettings(patch)
            self.apply(updated)
        }
    }

    /// A binding to a settings field that writes a merge patch at `path` on change.
    func settingBinding<Value: Equatable & JSONRepresentable>(
        _ keyPath: WritableKeyPath<CoreSettings, Value>, _ path: [String]
    ) -> Binding<Value> {
        Binding(
            get: { self.settings[keyPath: keyPath] },
            set: { newValue in
                guard self.settings[keyPath: keyPath] != newValue else { return }
                self.settings[keyPath: keyPath] = newValue
                self.updateSettings(JSONValue.patch(path, newValue.jsonValue))
            })
    }

    // MARK: Metadata

    func loadProviders() async throws -> [RcloneProvider] {
        if providers.isEmpty {
            providers = try await client.providers()
        }
        return providers
    }

    func loadMountOptions() async throws -> MountOptionsInfo {
        if let mountOptions { return mountOptions }
        let info = try await client.mountOptionsInfo()
        mountOptions = info
        return info
    }

    func loadMainOptions() async throws -> MainOptionsInfo {
        if let mainOptions { return mainOptions }
        let info = try await client.mainOptionsInfo()
        mainOptions = info
        return info
    }

    // MARK: Data sources (demo-aware)

    func loadShares(connectionId: String, path: String?) async throws -> [Share] {
        #if DEBUG
        if isDemo { return DemoData.shares.filter { path == nil || $0.path == path } }
        #endif
        return try await client.shares(connectionId: connectionId, path: path)
    }

    func loadCapabilities(connectionId: String) async throws -> ShareCapabilities {
        #if DEBUG
        if isDemo { return DemoData.capabilities }
        #endif
        return try await client.shareCapabilities(connectionId: connectionId)
    }

    func loadPolicy(connectionId: String) async throws -> SharePolicy {
        if isDemo { return SharePolicy() }
        return try await client.sharePolicy(connectionId: connectionId)
    }

    func loadActivity(_ filter: CoreClient.ActivityFilter) async throws -> [ActivityEntry] {
        #if DEBUG
        if isDemo { return DemoData.activity }
        #endif
        return try await client.activity(filter)
    }

    func browse(connectionId: String, path: String) async throws -> [BrowseEntry] {
        if isDemo { return [] }
        return try await client.browse(connectionId: connectionId, path: path)
    }

    // MARK: Actions

    /// Runs `operation`, presenting any error as an alert.
    func perform(_ operation: @escaping @MainActor () async throws -> Void) {
        Task { @MainActor in
            do {
                try await operation()
            } catch is CancellationError {
            } catch {
                self.present(error)
            }
        }
    }

    func present(_ error: any Error, title: String? = nil) {
        alert = ErrorText.alert(for: error, title: title)
    }

    func setMounted(_ mount: Mount, _ mounted: Bool) {
        perform {
            let updated = mounted ? try await self.client.mount(id: mount.id) : try await self.client.unmount(id: mount.id)
            self.upsert(&self.mounts, updated)
        }
    }

    func syncNow(_ item: OfflineItem? = nil) {
        perform { try await self.client.syncNow(id: item?.id) }
    }

    func pause(until date: Date?) {
        perform {
            if let date {
                self.pause = try await self.client.pause(until: date)
            } else {
                self.pause = try await self.client.pauseIndefinitely()
            }
        }
    }

    func resumeSync() {
        perform { self.pause = try await self.client.resume() }
    }

    func confirmMassDelete(_ item: OfflineItem, action: CoreClient.MassDeleteAction) {
        perform {
            let updated = try await self.client.confirmMassDelete(id: item.id, action: action)
            self.upsert(&self.offlineItems, updated)
        }
    }

    func removeOffline(_ item: OfflineItem, localCopy: CoreClient.LocalCopyAction) {
        perform {
            try await self.client.removeOfflineItem(id: item.id, localCopy: localCopy)
            self.offlineItems.removeAll { $0.id == item.id }
        }
    }

    func updated(_ item: OfflineItem) { upsert(&offlineItems, item) }
    func updated(_ mount: Mount) { upsert(&mounts, mount) }
    func updated(_ vault: Vault) { upsert(&vaults, vault) }
    func updated(_ connection: Connection) { upsert(&connections, connection) }

    func showInFinder(_ path: String) {
        let url = URL(fileURLWithPath: path)
        if FileManager.default.fileExists(atPath: path) {
            NSWorkspace.shared.activateFileViewerSelecting([url])
        } else {
            alert = AlertContent(title: String(localized: "Folder not found"),
                                 message: String(localized: "\(path) does not exist."))
        }
    }

    // MARK: Notifications

    /// Posts and acknowledges notifications the Core queued while no App was connected.
    func deliverPendingNotifications() async {
        guard !isDemo, let pending = try? await client.pendingNotifications(), !pending.isEmpty else { return }
        for notification in pending {
            NotificationManager.shared.post(notification)
        }
        try? await client.acknowledgeNotifications(pending.map(\.id))
    }

    // MARK: Demo

    #if DEBUG
    func loadDemoData() {
        isDemo = true
        coreState = .connected
        coreInfo = DemoData.coreInfo
        connections = DemoData.connections
        mounts = DemoData.mounts
        offlineItems = DemoData.offlineItems
        vaults = DemoData.vaults
        pause = DemoData.pause
        recentProblems = DemoData.activity.filter { $0.level == .error || $0.level == .warn }
        hasSettings = true
    }
    #endif
}
