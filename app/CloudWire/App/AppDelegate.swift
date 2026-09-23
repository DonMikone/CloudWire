import AppKit
import CloudWireKit
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let serviceProvider = ServiceProvider()
    private var launchDate = Date()
    private var backgroundLaunch = false

    func applicationWillFinishLaunching(_ notification: Notification) {
        launchDate = Date()
        // Never restore windows (the share window would reappear with a stale item).
        UserDefaults.standard.register(defaults: ["NSQuitAlwaysKeepsWindows": false, "menuBarIcon": true])

        // Handle cloudwire:// ourselves so SwiftUI does not open a window for the URL.
        NSAppleEventManager.shared().setEventHandler(
            self, andSelector: #selector(handleGetURL(_:withReplyEvent:)),
            forEventClass: AEEventClass(kInternetEventClass), andEventID: AEEventID(kAEGetURL))

        let arguments = CommandLine.arguments
        let event = NSAppleEventManager.shared().currentAppleEvent
        let launchedForURL = event?.eventID == AEEventID(kAEGetURL)
        let launchedAsLoginItem =
            event?.eventID == AEEventID(kAEOpenApplication)
            && event?.paramDescriptor(forKeyword: keyAEPropData)?.enumCodeValue == OSType(keyAELaunchedAsLogInItem)
        backgroundLaunch =
            launchedForURL || launchedAsLoginItem || arguments.contains("--deliver-notifications")
            || arguments.contains("--export-snapshots") || arguments.contains("--reset-registration")
        let needsOnboarding = !UserDefaults.standard.bool(forKey: "onboardingCompleted")
        WindowRouter.shared.suppressInitialMainWindow = backgroundLaunch || needsOnboarding
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        let arguments = CommandLine.arguments
        #if DEBUG
        if arguments.contains("--reset-registration") {
            CoreLauncher.resetRegistration()
            NSApp.terminate(nil)
            return
        }
        if let index = arguments.firstIndex(of: "--export-snapshots"), index + 1 < arguments.count {
            SnapshotExporter.run(directory: URL(fileURLWithPath: arguments[index + 1]))
            return
        }
        #endif

        NSApp.servicesProvider = serviceProvider
        NotificationManager.shared.configure()
        let model = AppModel.shared
        model.start()

        if arguments.contains("--deliver-notifications") {
            WindowRouter.shared.beginWork()
            Task { @MainActor in
                if await model.waitUntilConnected() {
                    await model.deliverPendingNotifications()
                }
                WindowRouter.shared.endWork()
            }
            return
        }
        if backgroundLaunch {
            // Give a URL or service request that launched the App time to arrive before an idle
            // App (menu bar icon off, no window) quits again.
            WindowRouter.shared.beginWork()
            WindowRouter.shared.endWork()
        } else if !UserDefaults.standard.bool(forKey: "onboardingCompleted") {
            WindowRouter.shared.showOnboarding()
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            if UserDefaults.standard.bool(forKey: "onboardingCompleted") {
                WindowRouter.shared.showMain()
            } else {
                WindowRouter.shared.showOnboarding()
            }
        }
        return false
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    @objc private func handleGetURL(_ event: NSAppleEventDescriptor, withReplyEvent reply: NSAppleEventDescriptor) {
        guard let text = event.paramDescriptor(forKeyword: keyDirectObject)?.stringValue,
            let url = URL(string: text)
        else { return }
        ActionHandler.handle(url: url)
    }
}
