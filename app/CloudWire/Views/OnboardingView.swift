import CloudWireKit
import FinderSync
import ServiceManagement
import SwiftUI
import UserNotifications

/// First launch: Welcome → background service + autostart → Finder extension + notifications → first connection.
struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismissWindow) private var dismissWindow

    private enum Step: Int, CaseIterable {
        case welcome, background, finder, connection
    }

    @State private var step: Step = .welcome
    @State private var autostart = true
    @State private var notificationsAllowed = false
    @State private var finderEnabled = FIFinderSyncController.isExtensionEnabled

    var body: some View {
        VStack(spacing: 0) {
            Group {
                switch step {
                case .welcome: welcome
                case .background: background
                case .finder: finder
                case .connection: connection
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .padding(28)

            Divider()
            HStack {
                pageDots
                Spacer()
                if step != .welcome {
                    Button("Back") { step = Step(rawValue: step.rawValue - 1) ?? .welcome }
                }
                primaryButton
            }
            .padding(16)
        }
        .frame(width: 560, height: 420)
    }

    private var pageDots: some View {
        HStack(spacing: 6) {
            ForEach(Step.allCases, id: \.self) { item in
                Circle()
                    .fill(item == step ? Color.accentColor : Color.secondary.opacity(0.3))
                    .frame(width: 7, height: 7)
            }
        }
    }

    @ViewBuilder
    private var primaryButton: some View {
        switch step {
        case .welcome:
            Button("Continue") { step = .background }.keyboardShortcut(.defaultAction)
        case .background:
            Button("Continue") { applyAutostart() }
                .keyboardShortcut(.defaultAction)
                .disabled(!model.isConnected)
        case .finder:
            Button("Continue") { step = .connection }.keyboardShortcut(.defaultAction)
        case .connection:
            Button("Done") { finish(addConnection: false) }.keyboardShortcut(.defaultAction)
        }
    }

    // MARK: Pages

    private var welcome: some View {
        VStack(alignment: .leading, spacing: 16) {
            Image(nsImage: NSApp.applicationIconImage).resizable().frame(width: 96, height: 96)
            Text("Welcome to CloudWire").font(.largeTitle.weight(.semibold))
            Text("Your cloud, wired to your Mac. Stream everything, keep what matters offline at SSD speed.")
                .font(.title3)
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 8) {
                Label("Mounts show cloud folders as drives in Finder.", systemImage: SidebarSection.mounts.symbol)
                Label("Offline Items keep real local copies, synced in the background.", systemImage: SidebarSection.offline.symbol)
                Label("Share links from Finder and encrypt folders in Vaults.", systemImage: "lock.shield")
            }
        }
    }

    private var background: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Background Service").font(.title.weight(.semibold))
            Text("A small background service keeps your drives mounted and your Offline Items in sync, even when this window is closed.")
                .fixedSize(horizontal: false, vertical: true)
            Toggle("Start CloudWire automatically at login", isOn: $autostart)
            switch model.coreState {
            case .connected:
                Label("The background service is running.", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
            case .needsApproval:
                VStack(alignment: .leading, spacing: 8) {
                    Label("macOS needs your permission to run CloudWire in the background.",
                          systemImage: "hand.raised.fill")
                        .foregroundStyle(.orange)
                    HStack {
                        Button("Allow in Background") { SMAppService.openSystemSettingsLoginItems() }
                            .buttonStyle(.borderedProminent)
                        Button("Check Again") { model.retryStart() }
                    }
                }
            case .failed(let message):
                VStack(alignment: .leading, spacing: 8) {
                    InlineError(message: message)
                    Button("Try Again") { model.retryStart() }
                }
            default:
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Starting the background service…")
                }
            }
        }
        .onAppear { model.start() }
    }

    private var finder: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Finder and Notifications").font(.title.weight(.semibold))
            Text("The Finder extension adds CloudWire to the context menu (share, copy links, make available offline) and shows sync badges.")
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Enable Finder Extension…") {
                    FIFinderSyncController.showExtensionManagementInterface()
                }
                if finderEnabled {
                    Label("Enabled", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
                }
            }
            Text("CloudWire tells you about errors, conflicts and deletions that need your decision.")
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Allow Notifications") {
                    Task { notificationsAllowed = await NotificationManager.shared.requestAuthorization() }
                }
                if notificationsAllowed {
                    Label("Allowed", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
                }
            }
        }
        .task {
            notificationsAllowed = await NotificationManager.shared.authorizationStatus() == .authorized
        }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            finderEnabled = FIFinderSyncController.isExtensionEnabled
        }
    }

    private var connection: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Your First Connection").font(.title.weight(.semibold))
            Text("Connect your Nextcloud with a browser login, or any other cloud supported by rclone.")
                .fixedSize(horizontal: false, vertical: true)
            Button("Add Connection…") { finish(addConnection: true) }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
            Text("You can also do this later in the Connections section.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    // MARK: Actions

    private func applyAutostart() {
        model.updateSettings(JSONValue.patch(["autostart"], .bool(autostart)))
        step = .finder
    }

    private func finish(addConnection: Bool) {
        UserDefaults.standard.set(true, forKey: "onboardingCompleted")
        if addConnection {
            model.showAddConnection = true
            WindowRouter.shared.showMain(section: .connections)
        } else {
            WindowRouter.shared.showMain(section: .overview)
        }
        dismissWindow(id: WindowRouter.onboardingID)
    }
}
