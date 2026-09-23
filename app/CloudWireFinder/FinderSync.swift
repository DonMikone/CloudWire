import AppKit
import CloudWireKit
import FinderSync
import os

/// Finder integration: context menu in Mounts and Offline Items, sync badges on Offline Item files.
///
/// Roots and capabilities come from `finder.roots` and are cached so `menu(for:)` can answer
/// synchronously. Menu actions open `cloudwire://action` URLs carrying the Core-issued token.
/// If the sandbox denies the socket, the extension falls back to a static menu without token
/// (the App then confirms state-changing actions) and without badges.
@objc(FinderSync)
final class FinderSync: FIFinderSync, @unchecked Sendable {
    private struct State {
        var roots: [FinderRoot] = []
        var token: String?
        var urlOnlyMode = false
        /// URLs waiting for a status batch.
        var pending: [String: URL] = [:]
        var batchScheduled = false
        /// URLs Finder asked badges for, re-queried when an Offline Item changes.
        var known: [String: URL] = [:]
        var rootsRefreshScheduled = false
        /// Paths last assigned to `directoryURLs`; nil before the first assignment.
        var rootPaths: Set<String>?
    }

    private static let log = Logger(subsystem: "io.github.donmikone.cloudwire.finder", category: "FinderSync")
    private static let badgeIDs = ["synced", "syncing", "error", "conflict"]
    private static let maxKnownURLs = 5000

    private let state = OSAllocatedUnfairLock(initialState: State())
    private let client = CoreClient(socketPath: CorePaths.socketPath)

    override init() {
        super.init()
        Self.registerBadges()
        Task { await self.run() }
    }

    // MARK: Core connection

    private func run() async {
        let events = await client.events()
        var iterator = events.makeAsyncIterator()
        var delay: Duration = .seconds(1)
        while true {
            do {
                try await client.connect()
                try await client.subscribeEvents(client: "finder")
                try await refreshRoots()
                delay = .seconds(1)
                eventLoop: while let event = await iterator.next() {
                    switch event {
                    case .connectionLost:
                        break eventLoop
                    case .offlineStatus:
                        // State changes only; `.offlineProgress` (every 2 s while syncing) changes no badge.
                        scheduleRootsRefresh()
                        requeryKnownBadges()
                    case .mountStatus, .connectionsChanged, .vaultsChanged:
                        scheduleRootsRefresh()
                    default:
                        break
                    }
                }
            } catch let error as CoreError where error.posixCode == EPERM || error.posixCode == EACCES {
                Self.log.error("Sandbox denied the Core socket (\(error.message, privacy: .public)); URL-only mode")
                enterURLOnlyMode()
                return
            } catch {
                Self.log.debug("Core not reachable: \(error.localizedDescription, privacy: .public)")
            }
            await client.disconnect()
            try? await Task.sleep(for: delay)
            delay = min(delay * 2, .seconds(60))
        }
    }

    private func refreshRoots() async throws {
        let result = try await client.finderRoots()
        let paths = Set(result.roots.map(\.path))
        let changed = state.withLock { state -> Bool in
            state.roots = result.roots.sorted { $0.path.count > $1.path.count }
            state.token = result.token
            guard state.rootPaths != paths else { return false }
            state.rootPaths = paths
            return true
        }
        // Assigning directoryURLs makes Finder re-observe every root and re-request all badges.
        guard changed else { return }
        let urls = Set(paths.map { URL(fileURLWithPath: $0, isDirectory: true) })
        await MainActor.run {
            FIFinderSyncController.default().directoryURLs = urls
        }
    }

    private func scheduleRootsRefresh() {
        let schedule = state.withLock { state -> Bool in
            guard !state.rootsRefreshScheduled else { return false }
            state.rootsRefreshScheduled = true
            return true
        }
        guard schedule else { return }
        Task {
            try? await Task.sleep(for: .milliseconds(500))
            state.withLock { $0.rootsRefreshScheduled = false }
            try? await refreshRoots()
        }
    }

    private func enterURLOnlyMode() {
        state.withLock {
            $0.urlOnlyMode = true
            $0.roots = []
            $0.token = nil
        }
        let base = URL(fileURLWithPath: CorePaths.realHome, isDirectory: true).appendingPathComponent("CloudWire")
        Task { @MainActor in
            FIFinderSyncController.default().directoryURLs = [base]
        }
    }

    // MARK: Roots

    private func root(for path: String) -> FinderRoot? {
        state.withLock { state in
            state.roots.first { path == $0.path || path.hasPrefix($0.path.hasSuffix("/") ? $0.path : $0.path + "/") }
        }
    }

    // MARK: Badges

    private static func registerBadges() {
        let controller = FIFinderSyncController.default()
        let specs: [(String, String, NSColor, String)] = [
            ("synced", "checkmark.circle.fill", .systemGreen, String(localized: "Synced")),
            ("syncing", "arrow.triangle.2.circlepath.circle.fill", .systemOrange, String(localized: "Syncing")),
            ("error", "exclamationmark.circle.fill", .systemRed, String(localized: "Sync error")),
            ("conflict", "exclamationmark.triangle.fill", .systemYellow, String(localized: "Conflict")),
        ]
        for (id, symbol, color, label) in specs {
            let config = NSImage.SymbolConfiguration(pointSize: 64, weight: .regular)
                .applying(NSImage.SymbolConfiguration(paletteColors: id == "conflict" ? [.black, color] : [.white, color]))
            guard let image = NSImage(systemSymbolName: symbol, accessibilityDescription: label)?
                .withSymbolConfiguration(config)
            else { continue }
            controller.setBadgeImage(image, label: label, forBadgeIdentifier: id)
        }
    }

    override func requestBadgeIdentifier(for url: URL) {
        let path = url.path
        guard let root = root(for: path), root.kind == .offline else { return }
        let schedule = state.withLock { state -> Bool in
            state.pending[path] = url
            if state.known.count < Self.maxKnownURLs { state.known[path] = url }
            guard !state.batchScheduled else { return false }
            state.batchScheduled = true
            return true
        }
        if schedule {
            Task {
                try? await Task.sleep(for: .milliseconds(100))
                await flushBadges()
            }
        }
    }

    override func endObservingDirectory(at url: URL) {
        let prefix = url.path.hasSuffix("/") ? url.path : url.path + "/"
        state.withLock { state in
            state.known = state.known.filter { !$0.key.hasPrefix(prefix) || $0.key.dropFirst(prefix.count).contains("/") }
        }
    }

    private func requeryKnownBadges() {
        let schedule = state.withLock { state -> Bool in
            guard !state.known.isEmpty else { return false }
            state.pending.merge(state.known) { current, _ in current }
            guard !state.batchScheduled else { return false }
            state.batchScheduled = true
            return true
        }
        if schedule {
            Task {
                try? await Task.sleep(for: .milliseconds(100))
                await flushBadges()
            }
        }
    }

    private func flushBadges() async {
        let batch = state.withLock { state -> [String: URL] in
            let batch = state.pending
            state.pending.removeAll()
            state.batchScheduled = false
            return batch
        }
        guard !batch.isEmpty else { return }
        guard let statuses = try? await client.statusForPaths(Array(batch.keys)) else { return }
        let updates = batch.map { path, url -> (URL, String) in
            let status = statuses[path]?.rawValue ?? ""
            return (url, Self.badgeIDs.contains(status) ? status : "")
        }
        await MainActor.run {
            let controller = FIFinderSyncController.default()
            for (url, badge) in updates {
                controller.setBadgeIdentifier(badge, for: url)
            }
        }
    }

    // MARK: Menu

    override func menu(for menuKind: FIMenuKind) -> NSMenu? {
        guard menuKind == .contextualMenuForItems || menuKind == .contextualMenuForContainer else { return nil }
        // Finder asks on the main thread; hop there otherwise. NSMenu is main-actor bound.
        nonisolated(unsafe) var menu: NSMenu?
        let build = { MainActor.assumeIsolated { menu = self.buildMenu(kind: menuKind) } }
        if Thread.isMainThread {
            build()
        } else {
            DispatchQueue.main.sync(execute: build)
        }
        return menu
    }

    @MainActor
    private func buildMenu(kind: FIMenuKind) -> NSMenu? {
        let controller = FIFinderSyncController.default()
        let urls = kind == .contextualMenuForItems
            ? (controller.selectedItemURLs() ?? []) : [controller.targetedURL()].compactMap { $0 }
        guard let first = urls.first else { return nil }
        // The App accepts several paths only for makeOffline; the other actions act on one item.
        let offered = { (name: ActionURL.Name) in urls.count == 1 || name.acceptsSeveralPaths }
        let menu = NSMenu(title: "CloudWire")

        let urlOnly = state.withLock { $0.urlOnlyMode }
        if urlOnly {
            for name in ActionURL.Name.allCases where offered(name) {
                menu.addItem(item(for: name))
            }
            return wrap(menu)
        }

        guard let root = root(for: first.path) else { return nil }
        let caps = root.capabilities
        var sharing: [ActionURL.Name] = []
        if !root.isVault {
            if caps.publicLink || caps.userShare || caps.emailShare { sharing.append(.share) }
            if caps.manage { sharing.append(.manageShares) }
            if caps.publicLink { sharing.append(.copyPublicLink) }
            if caps.internalLink { sharing.append(.copyInternalLink) }
        }
        let offline: [ActionURL.Name] = switch root.kind {
        case .offline: [.removeOffline]
        default: root.isVault ? [.makeOffline] : [.makeOffline, .encrypt]
        }
        let web: [ActionURL.Name] = caps.webURL && !root.isVault ? [.openWeb] : []
        let groups = [sharing, offline, web].map { $0.filter(offered) }.filter { !$0.isEmpty }
        guard !groups.isEmpty else { return nil }

        if root.isVault {
            let notice = NSMenuItem(title: String(localized: "Sharing is not available in Vaults"), action: nil,
                                    keyEquivalent: "")
            notice.isEnabled = false
            menu.addItem(notice)
        }
        for group in groups {
            if !menu.items.isEmpty { menu.addItem(.separator()) }
            for name in group { menu.addItem(item(for: name)) }
        }
        return wrap(menu)
    }

    /// Groups the items under a "CloudWire" submenu.
    @MainActor
    private func wrap(_ items: NSMenu) -> NSMenu {
        let menu = NSMenu(title: "")
        let parent = NSMenuItem(title: "CloudWire", action: nil, keyEquivalent: "")
        parent.image = NSImage(systemSymbolName: "cloud", accessibilityDescription: nil)
        parent.submenu = items
        menu.addItem(parent)
        return menu
    }

    @MainActor
    private func item(for name: ActionURL.Name) -> NSMenuItem {
        let item = NSMenuItem(title: Self.title(for: name), action: #selector(performAction(_:)), keyEquivalent: "")
        item.target = self
        // Finder copies the menu; only the tag survives the trip back to the action.
        item.tag = ActionURL.Name.allCases.firstIndex(of: name) ?? 0
        return item
    }

    private static func title(for name: ActionURL.Name) -> String {
        switch name {
        case .share: return String(localized: "Share…")
        case .manageShares: return String(localized: "Manage Shares…")
        case .copyPublicLink: return String(localized: "Copy Public Link")
        case .copyInternalLink: return String(localized: "Copy Internal Link")
        case .makeOffline: return String(localized: "Make Available Offline")
        case .removeOffline: return String(localized: "Remove Offline")
        case .encrypt: return String(localized: "Encrypt (Vault)…")
        case .openWeb: return String(localized: "Open in Browser")
        }
    }

    /// Menu actions arrive on the main thread.
    @MainActor @objc private func performAction(_ sender: NSMenuItem) {
        let names = ActionURL.Name.allCases
        guard names.indices.contains(sender.tag) else { return }
        let controller = FIFinderSyncController.default()
        var urls = controller.selectedItemURLs() ?? []
        if urls.isEmpty, let target = controller.targetedURL() { urls = [target] }
        guard !urls.isEmpty else { return }
        // Folders keep a trailing slash so the App need not stat inside Mounts.
        let paths = urls.map { $0.hasDirectoryPath ? $0.path + "/" : $0.path }
        let token = state.withLock { $0.token }
        let action = ActionURL(name: names[sender.tag], paths: paths, token: token)
        NSWorkspace.shared.open(action.url)
    }
}
