import AppKit
import CloudWireKit
import SwiftUI

@main
struct CloudWireApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @AppStorage("menuBarIcon") private var menuBarIcon = true
    @State private var model = AppModel.shared

    var body: some Scene {
        // The main window comes first: SwiftUI opens the first window scene at launch, which lets the
        // App capture `openWindow` even without a menu bar icon. Background launches close it again.
        Window("CloudWire", id: WindowRouter.mainID) {
            MainWindow()
                .environment(model)
        }
        .defaultSize(width: 1100, height: 720)

        MenuBarExtra(isInserted: $menuBarIcon) {
            MenuBarView()
                .environment(model)
        } label: {
            MenuBarLabel()
                .environment(model)
        }
        .menuBarExtraStyle(.window)

        WindowGroup("Share", id: WindowRouter.shareID, for: ShareTarget.self) { $target in
            if let target {
                ShareWindow(target: target)
                    .environment(model)
                    .windowLifecycle(WindowRouter.shareID)
            }
        }
        .windowResizability(.contentSize)

        Window("Welcome to CloudWire", id: WindowRouter.onboardingID) {
            OnboardingView()
                .environment(model)
                .windowLifecycle(WindowRouter.onboardingID)
        }
        .windowResizability(.contentSize)

        Settings {
            SettingsView()
                .environment(model)
                .windowLifecycle(WindowRouter.settingsID)
        }
    }
}

// MARK: - Window lifecycle

extension View {
    /// Captures the SwiftUI window actions for AppKit callers and reports visibility to the router.
    /// Every window instance counts on its own, so two share windows never share one entry.
    func windowLifecycle(_ id: String) -> some View {
        modifier(WindowLifecycle(id: id))
    }
}

private struct WindowLifecycle: ViewModifier {
    let id: String
    @State private var instance = UUID()
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings

    private var key: String { id + ":" + instance.uuidString }

    func body(content: Content) -> some View {
        content
            .onAppear {
                WindowRouter.shared.capture(openWindow: openWindow, openSettings: openSettings)
                WindowRouter.shared.windowAppeared(key)
            }
            .onDisappear {
                WindowRouter.shared.windowDisappeared(key)
            }
    }
}

/// Captures window actions without counting as a visible window (menu bar label).
struct ActionCapture: ViewModifier {
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings

    func body(content: Content) -> some View {
        content.onAppear {
            WindowRouter.shared.capture(openWindow: openWindow, openSettings: openSettings)
        }
    }
}

/// Hands out the hosting NSWindow once it exists.
struct WindowAccessor: NSViewRepresentable {
    let onAttach: @MainActor (NSWindow) -> Void

    func makeNSView(context: Context) -> AccessorView {
        let view = AccessorView()
        view.onAttach = onAttach
        return view
    }

    func updateNSView(_ nsView: AccessorView, context: Context) {}

    final class AccessorView: NSView {
        var onAttach: (@MainActor (NSWindow) -> Void)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let window, let onAttach {
                self.onAttach = nil
                onAttach(window)
            }
        }
    }
}
