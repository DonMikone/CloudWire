import AppKit
import CloudWireKit
import Foundation

/// Executes `cloudwire://action` URLs from the Finder extension and the Services menu.
///
/// A missing or invalid token means the request did not come from the Finder extension; state
/// changing actions (create link, make offline, remove offline, encrypt) then need a confirmation.
@MainActor
enum ActionHandler {
    static func handle(url: URL) {
        guard let action = ActionURL(url: url) else { return }
        handle(action)
    }

    static func handle(_ request: ActionURL) {
        var action: ActionURL
        do {
            // URLs may come from any app: only absolute paths without dot segments, and one path
            // unless the action takes several.
            action = try request.standardized()
        } catch ActionURL.PathProblem.notAbsolute(let path) {
            reject(String(localized: "CloudWire only accepts absolute paths, not “\(path)”."))
            return
        } catch {
            reject(String(localized: "This action works on one item. Select a single file or folder."))
            return
        }
        // Finder marks folders with a trailing slash; remember that, then use clean paths.
        directoryHints.formUnion(action.paths.filter { $0.count > 1 && $0.hasSuffix("/") }.map { String($0.dropLast()) })
        action.paths = action.paths.map { $0.count > 1 && $0.hasSuffix("/") ? String($0.dropLast()) : $0 }
        WindowRouter.shared.beginWork()
        Task { @MainActor in
            await run(action)
            WindowRouter.shared.endWork()
        }
    }

    private static func run(_ action: ActionURL) async {
        let model = AppModel.shared
        guard await model.waitUntilConnected() else {
            model.present(CoreError.notConnected())
            WindowRouter.shared.showMain(section: .overview)
            return
        }
        let client = model.client

        var trusted = false
        if let token = action.token {
            trusted = (try? await client.validateToken(token)) ?? false
        }
        if !trusted && action.name.isStateChanging && !confirm(action) {
            return
        }

        do {
            switch action.name {
            case .share, .manageShares:
                let target = try await shareTarget(for: action.paths[0], manage: action.name == .manageShares)
                WindowRouter.shared.showShare(target)
            case .copyPublicLink:
                var urls: [String] = []
                for path in action.paths {
                    let resolved = try await client.resolvePath(path)
                    do {
                        urls.append(try await client.copyPublicLink(connectionId: resolved.connectionId,
                                                                     path: resolved.remotePath).url)
                    } catch let error as CoreError where error.code == "share.serverPolicy" {
                        // The server enforces a password or expiry: let the user fill in the share window.
                        WindowRouter.shared.showShare(try await shareTarget(for: path, manage: false))
                        return
                    }
                }
                copyToPasteboard(urls)
            case .copyInternalLink:
                var urls: [String] = []
                for path in action.paths {
                    let resolved = try await client.resolvePath(path)
                    urls.append(try await client.internalLink(connectionId: resolved.connectionId,
                                                              path: resolved.remotePath))
                }
                copyToPasteboard(urls)
            case .makeOffline:
                model.offlineDraft = try await offlineDraft(for: action.paths)
                WindowRouter.shared.showMain(section: .offline)
            case .removeOffline:
                let resolved = try await client.resolvePath(action.paths[0])
                await model.refreshOffline()
                guard resolved.context == .offline, let item = model.offlineItems.first(where: { $0.id == resolved.itemId })
                else {
                    throw CoreError(code: "offline.notFound",
                                    message: String(localized: "This file is not part of an Offline Item."))
                }
                model.offlineRemoval = item
                WindowRouter.shared.showMain(section: .offline)
            case .encrypt:
                let path = action.paths[0]
                let resolved = try await client.resolvePath(path)
                model.encryptDraft = EncryptDraft(connectionId: resolved.connectionId, path: resolved.remotePath,
                                                  isDir: isDirectory(path))
                WindowRouter.shared.showMain(section: .vaults)
            case .openWeb:
                let resolved = try await client.resolvePath(action.paths[0])
                let web = try await client.webURL(connectionId: resolved.connectionId, path: resolved.remotePath)
                if let url = URL(string: web) { NSWorkspace.shared.open(url) }
            }
        } catch {
            model.present(error)
            WindowRouter.shared.showMain()
        }
    }

    // MARK: Helpers

    private static func reject(_ message: String) {
        AppModel.shared.present(CoreError(code: "client.selection", message: message))
        WindowRouter.shared.showMain()
    }

    private static func confirm(_ action: ActionURL) -> Bool {
        NSApp.activate()
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = String(localized: "Allow this action?")
        let names = action.paths.map { CorePaths.abbreviate($0) }.joined(separator: "\n")
        let what: String
        switch action.name {
        case .copyPublicLink: what = String(localized: "create a public link")
        case .makeOffline: what = String(localized: "make items available offline")
        case .removeOffline: what = String(localized: "remove an Offline Item")
        case .encrypt: what = String(localized: "encrypt items into a Vault")
        default: what = action.name.rawValue
        }
        alert.informativeText = String(
            localized: "Another app asked CloudWire to \(what) for:\n\n\(names)\n\nContinue only if you started this yourself.")
        alert.addButton(withTitle: String(localized: "Continue"))
        alert.addButton(withTitle: String(localized: "Cancel"))
        return alert.runModal() == .alertFirstButtonReturn
    }

    /// Paths the Finder extension marked as folders (no stat needed inside Mounts).
    private static var directoryHints: Set<String> = []

    private static func isDirectory(_ path: String) -> Bool {
        if directoryHints.contains(path) { return true }
        var isDir: ObjCBool = false
        return FileManager.default.fileExists(atPath: path, isDirectory: &isDir) && isDir.boolValue
    }

    private static func shareTarget(for path: String, manage: Bool) async throws -> ShareTarget {
        let resolved = try await AppModel.shared.client.resolvePath(path)
        return ShareTarget(connectionId: resolved.connectionId, path: resolved.remotePath, isDir: isDirectory(path),
                           name: (path as NSString).lastPathComponent, manage: manage)
    }

    /// One folder → folder item; files of one folder → files item.
    private static func offlineDraft(for paths: [String]) async throws -> OfflineDraft {
        let client = AppModel.shared.client
        if paths.count == 1, isDirectory(paths[0]) {
            let resolved = try await client.resolvePath(paths[0])
            return OfflineDraft(connectionId: resolved.connectionId, remotePath: resolved.remotePath, kind: .folder,
                                files: [])
        }
        let parents = Set(paths.map { ($0 as NSString).deletingLastPathComponent })
        guard parents.count == 1, !paths.contains(where: isDirectory) else {
            throw CoreError(code: "client.selection",
                            message: String(localized: "Select one folder, or files from one folder."))
        }
        let resolved = try await client.resolvePath(paths[0])
        let parent = (resolved.remotePath as NSString).deletingLastPathComponent
        return OfflineDraft(connectionId: resolved.connectionId, remotePath: parent, kind: .files,
                            files: paths.map { ($0 as NSString).lastPathComponent })
    }

    private static func copyToPasteboard(_ urls: [String]) {
        guard !urls.isEmpty else { return }
        let text = urls.joined(separator: "\n")
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        if AppModel.shared.settings.notifications.linkCopied {
            NotificationManager.shared.postLinkCopied(text)
        }
    }
}

/// Services menu fallback ("Mit CloudWire teilen", "CloudWire-Link kopieren", "Offline verfügbar machen").
@MainActor
final class ServiceProvider: NSObject {
    @objc func shareService(_ pasteboard: NSPasteboard, userData: String?,
                            error: AutoreleasingUnsafeMutablePointer<NSString?>)
    {
        dispatch(.share, pasteboard)
    }

    @objc func copyLinkService(_ pasteboard: NSPasteboard, userData: String?,
                               error: AutoreleasingUnsafeMutablePointer<NSString?>)
    {
        dispatch(.copyPublicLink, pasteboard)
    }

    @objc func makeOfflineService(_ pasteboard: NSPasteboard, userData: String?,
                                  error: AutoreleasingUnsafeMutablePointer<NSString?>)
    {
        dispatch(.makeOffline, pasteboard)
    }

    private func dispatch(_ name: ActionURL.Name, _ pasteboard: NSPasteboard) {
        let urls = pasteboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL]
        let paths = (urls ?? []).map(\.path)
        guard !paths.isEmpty else { return }
        // No token: the App confirms state-changing actions.
        ActionHandler.handle(ActionURL(name: name, paths: paths, token: nil))
    }
}
