import AppKit
import SwiftUI

/// Opens SwiftUI windows from AppKit code (URL handler, notifications, menu bar) and switches the
/// activation policy: the Dock icon shows only while a window is open. With the menu bar icon off,
/// the App quits when its last window closes (the Core keeps running).
@MainActor
final class WindowRouter {
    static let shared = WindowRouter()

    static let mainID = "main"
    static let shareID = "share"
    static let onboardingID = "onboarding"
    static let settingsID = "settings"

    private var openWindowAction: OpenWindowAction?
    private var openSettingsAction: OpenSettingsAction?
    private var pendingOpens: [@MainActor () -> Void] = []
    private var openWindows: Set<String> = []
    /// Work (URL action, notification delivery) that must finish before an idle App may quit.
    private var busyCount = 0

    /// Close the main window SwiftUI opens at launch (background launches, onboarding).
    var suppressInitialMainWindow = false
    private var initialMainWindowHandled = false

    var hasOpenWindows: Bool { !openWindows.isEmpty }

    // MARK: Capturing SwiftUI actions

    func capture(openWindow: OpenWindowAction, openSettings: OpenSettingsAction) {
        openWindowAction = openWindow
        openSettingsAction = openSettings
        let pending = pendingOpens
        pendingOpens.removeAll()
        pending.forEach { $0() }
    }

    private func withOpenWindow(_ body: @escaping @MainActor (OpenWindowAction) -> Void) {
        if let openWindowAction {
            body(openWindowAction)
        } else {
            pendingOpens.append { [weak self] in
                if let action = self?.openWindowAction { body(action) }
            }
        }
    }

    // MARK: Opening

    func showMain(section: SidebarSection? = nil) {
        if let section { AppModel.shared.selection = section }
        activate()
        withOpenWindow { $0(id: Self.mainID) }
    }

    func showShare(_ target: ShareTarget) {
        activate()
        withOpenWindow { $0(id: Self.shareID, value: target) }
    }

    func showOnboarding() {
        activate()
        withOpenWindow { $0(id: Self.onboardingID) }
    }

    func showSettings() {
        activate()
        if let openSettingsAction {
            openSettingsAction()
        } else {
            pendingOpens.append { [weak self] in self?.openSettingsAction?() }
        }
    }

    private func activate() {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
    }

    // MARK: Window tracking

    /// Called by the main window when it first gets its NSWindow. Returns false when the window
    /// was opened by SwiftUI at launch although it should stay hidden.
    func mainWindowAttached(_ window: NSWindow) -> Bool {
        guard !initialMainWindowHandled else { return true }
        initialMainWindowHandled = true
        guard suppressInitialMainWindow else { return true }
        window.alphaValue = 0
        DispatchQueue.main.async {
            window.close()
            window.alphaValue = 1
        }
        return false
    }

    func windowAppeared(_ id: String) {
        openWindows.insert(id)
        NSApp.setActivationPolicy(.regular)
    }

    func windowDisappeared(_ id: String) {
        openWindows.remove(id)
        guard openWindows.isEmpty else { return }
        NSApp.setActivationPolicy(.accessory)
        terminateIfIdle()
    }

    // MARK: Idle termination

    func beginWork() { busyCount += 1 }

    func endWork(terminateAfter delay: Duration = .seconds(5)) {
        busyCount = max(0, busyCount - 1)
        Task { @MainActor in
            try? await Task.sleep(for: delay)
            self.terminateIfIdle()
        }
    }

    /// Quits when nothing is visible and the menu bar icon is off.
    func terminateIfIdle() {
        // Registered default: true (AppDelegate).
        guard !UserDefaults.standard.bool(forKey: "menuBarIcon"), openWindows.isEmpty, busyCount == 0 else { return }
        NSApp.terminate(nil)
    }
}
