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
        .commands {
            // Cmd+Q closes the windows; the menu bar icon and the Core keep running.
            CommandGroup(replacing: .appTermination) {
                Button("Close Windows") { WindowRouter.shared.closeAllWindows() }
                    .keyboardShortcut("q")
                Button("Quit CloudWire Completely…") { WindowRouter.shared.confirmQuitCompletely() }
            }
        }

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
                    .modifier(ActionCapture())
            }
        }
        .windowResizability(.contentSize)

        Window("Welcome to CloudWire", id: WindowRouter.onboardingID) {
            OnboardingView()
                .environment(model)
                .modifier(ActionCapture())
        }
        .windowResizability(.contentSize)

        Settings {
            SettingsView()
                .environment(model)
                .modifier(ActionCapture())
        }
    }
}

/// Captures the SwiftUI window actions for AppKit callers. The Dock icon follows the visible
/// NSWindows (WindowRouter), not SwiftUI view lifecycles.
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
