import AppKit
import SwiftUI

/// Opens SwiftUI windows from AppKit code (URL handler, notifications, menu bar) and switches the
/// activation policy: the Dock icon (and the Cmd-Tab entry) shows only while a window is open or
/// minimized. With the menu bar icon off, the App quits when its last window closes (the Core
/// keeps running).
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
    private var observers: [NSObjectProtocol] = []
    private var updateScheduled = false
    /// Work (URL action, notification delivery) that must finish before an idle App may quit.
    private var busyCount = 0
    /// The window that last had key focus (sheets count as their parent); decides where errors appear.
    private weak var lastKeyWindow: NSWindow?

    /// Close the main window SwiftUI opens at launch (background launches, onboarding).
    var suppressInitialMainWindow = false
    private var initialMainWindowHandled = false

    /// Windows that justify a Dock icon: titled, main-capable windows that are on screen or
    /// minimized. Excludes the menu bar panel, alerts, and the launch main window while hidden.
    private var appWindows: [NSWindow] {
        NSApp.windows.filter { window in
            window.styleMask.contains(.titled) && window.canBecomeMain && window.alphaValue > 0
                && (window.isVisible || window.isMiniaturized)
        }
    }

    var hasOpenWindows: Bool { !appWindows.isEmpty }

    /// Whether errors go to `model.alert` in the window the user last worked in (main, Settings,
    /// Share, onboarding). False for the menu bar panel or when no window is open.
    var lastKeyWindowShowsModelAlert: Bool {
        guard let window = lastKeyWindow else { return false }
        return window.isVisible && window.styleMask.contains(.titled) && window.canBecomeMain
    }

    /// Cmd+Q and plain quit requests: the Dock icon goes, the menu bar icon and the Core stay.
    func closeAllWindows() {
        appWindows.forEach { $0.close() }
    }

    /// A quit Apple event without a logout/restart/shutdown reason (Dock menu, AppleScript).
    static func isPlainQuitEvent(_ event: NSAppleEventDescriptor?) -> Bool {
        guard let event, event.eventClass == AEEventClass(kCoreEventClass),
              event.eventID == AEEventID(kAEQuitApplication)
        else { return false }
        let reason = AEKeyword(kAEQuitReason)
        return event.attributeDescriptor(forKeyword: reason) == nil && event.paramDescriptor(forKeyword: reason) == nil
    }

    /// An app-modal alert: a SwiftUI alert on the menu bar panel stays unclickable, because the
    /// panel closes as soon as the alert takes key focus.
    func confirmQuitCompletely() {
        DispatchQueue.main.async {
            NSApp.activate()
            let alert = NSAlert()
            alert.messageText = String(localized: "Quit CloudWire completely?")
            alert.informativeText = Self.quitCompletelyMessage(autostart: AppModel.shared.settings.autostart)
            alert.addButton(withTitle: String(localized: "Quit Completely")).hasDestructiveAction = true
            alert.addButton(withTitle: String(localized: "Cancel"))
            guard alert.runModal() == .alertFirstButtonReturn else { return }
            Task {
                await AppModel.shared.shutdownCore()
                NSApp.terminate(nil)
            }
        }
    }

    /// With autostart on, the Core's start gate lets it run again at the next login.
    static func quitCompletelyMessage(autostart: Bool) -> String {
        autostart
            ? String(localized: "Mounts are ejected and syncing stops until you open CloudWire again or next log in.")
            : String(localized: "Mounts are ejected and syncing stops until you open CloudWire again.")
    }

    /// An app-modal error alert for places without a model alert (menu bar panel, no window open).
    func showModalAlert(_ content: AlertContent) {
        DispatchQueue.main.async {
            NSApp.activate()
            let alert = NSAlert()
            alert.alertStyle = .warning
            alert.messageText = content.title
            alert.informativeText = content.message
            alert.runModal()
        }
    }

    private init() {
        let names: [Notification.Name] = [
            NSWindow.didBecomeKeyNotification, NSWindow.didBecomeMainNotification,
            NSWindow.willCloseNotification, NSWindow.didMiniaturizeNotification,
            NSWindow.didDeminiaturizeNotification, NSWindow.didChangeOcclusionStateNotification,
            NSApplication.didUnhideNotification,
        ]
        observers = names.map { name in
            NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { note in
                let window = note.object as? NSWindow
                MainActor.assumeIsolated {
                    if name == NSWindow.didBecomeKeyNotification, let window {
                        WindowRouter.shared.lastKeyWindow = window.sheetParent ?? window
                    }
                    WindowRouter.shared.scheduleUpdate()
                }
            }
        }
    }

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

    /// Called by the main window when it first gets its NSWindow: closes it again when SwiftUI
    /// opened it at launch although it should stay hidden.
    func mainWindowAttached(_ window: NSWindow) {
        guard !initialMainWindowHandled else { return }
        initialMainWindowHandled = true
        guard suppressInitialMainWindow else { return }
        window.alphaValue = 0
        DispatchQueue.main.async {
            window.close()
            window.alphaValue = 1
        }
    }

    /// Re-evaluates on the next run loop turn, when a closing window is gone from the screen.
    private func scheduleUpdate() {
        guard !updateScheduled else { return }
        updateScheduled = true
        DispatchQueue.main.async {
            self.updateScheduled = false
            self.updateActivationPolicy()
        }
    }

    private func updateActivationPolicy() {
        // Cmd+H takes every window off screen; a hidden App keeps its Dock icon.
        guard !NSApp.isHidden else { return }
        if hasOpenWindows {
            if NSApp.activationPolicy() != .regular { NSApp.setActivationPolicy(.regular) }
        } else {
            if NSApp.activationPolicy() != .accessory { NSApp.setActivationPolicy(.accessory) }
            terminateIfIdle()
        }
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
        guard !UserDefaults.standard.bool(forKey: "menuBarIcon"), !NSApp.isHidden, !hasOpenWindows, busyCount == 0
        else { return }
        NSApp.terminate(nil)
    }
}
