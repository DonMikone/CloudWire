import AppKit
import CloudWireKit
import SwiftUI

/// The menu bar icon: the template cloud-cable image with a small status symbol.
struct MenuBarLabel: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Image(nsImage: MenuBarIconRenderer.image(for: model.isConnected ? model.aggregateStatus : .error))
            .modifier(ActionCapture())
            .accessibilityLabel(Text("CloudWire"))
    }
}

@MainActor
enum MenuBarIconRenderer {
    private static var cache: [String: NSImage] = [:]

    static func image(for status: AggregateStatus) -> NSImage {
        let key = "\(status)"
        if let cached = cache[key] { return cached }
        let size = NSSize(width: 22, height: 18)
        let base = NSImage(named: "MenuBarIcon")
            ?? NSImage(systemSymbolName: "cloud", accessibilityDescription: "CloudWire")
            ?? NSImage(size: size)
        let badgeName: String?
        switch status {
        case .idle: badgeName = nil
        case .syncing: badgeName = "arrow.triangle.2.circlepath"
        case .paused: badgeName = "pause.fill"
        case .error: badgeName = "exclamationmark"
        }
        let image = NSImage(size: size, flipped: false) { rect in
            let iconRect = NSRect(x: 0, y: 1, width: 18, height: 16)
            base.draw(in: iconRect)
            if let badgeName,
                let badge = NSImage(systemSymbolName: badgeName, accessibilityDescription: nil)?
                    .withSymbolConfiguration(.init(pointSize: 8, weight: .bold))
            {
                let badgeRect = NSRect(x: rect.maxX - 10, y: 0, width: 10, height: 10)
                // Punch a hole behind the badge so it stays legible on the template image.
                NSGraphicsContext.current?.compositingOperation = .clear
                NSBezierPath(ovalIn: badgeRect.insetBy(dx: -1, dy: -1)).fill()
                NSGraphicsContext.current?.compositingOperation = .sourceOver
                badge.draw(in: badgeRect.insetBy(dx: 1, dy: 1))
            }
            return true
        }
        image.isTemplate = true
        cache[key] = image
        return image
    }
}

struct MenuBarView: View {
    @Environment(AppModel.self) private var model
    @State private var confirmQuit = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header
            if model.isConnected {
                if !model.mounts.isEmpty {
                    Divider()
                    sectionTitle(String(localized: "Mounts"))
                    ForEach(model.mounts) { mount in
                        HStack {
                            Image(systemName: "externaldrive").foregroundStyle(mount.state.color)
                            VStack(alignment: .leading, spacing: 0) {
                                Text(mount.volumeName).lineLimit(1)
                                if mount.state != .mounted {
                                    Text(mount.state.label).font(.caption).foregroundStyle(.secondary)
                                }
                            }
                            Spacer()
                            Toggle("", isOn: Binding(
                                get: { mount.state == .mounted || mount.state == .mounting },
                                set: { model.setMounted(mount, $0) }))
                                .toggleStyle(.switch)
                                .controlSize(.mini)
                                .labelsHidden()
                        }
                    }
                }
                if !model.offlineItems.isEmpty {
                    Divider()
                    sectionTitle(String(localized: "Offline"))
                    ForEach(model.offlineItems) { item in
                        VStack(alignment: .leading, spacing: 2) {
                            HStack {
                                Image(systemName: item.state.symbol).foregroundStyle(item.state.color)
                                Text(item.displayName).lineLimit(1)
                                Spacer()
                                Text(item.state == .paused && !item.reason.isEmpty
                                     ? PauseReason.label(item.reason) : item.state.label)
                                    .font(.caption).foregroundStyle(.secondary)
                            }
                            if item.state == .syncing, let fraction = item.progress?.fraction {
                                ProgressView(value: fraction).controlSize(.small)
                            }
                        }
                    }
                }
                Divider()
                HStack {
                    Button("Sync Now") { model.syncNow() }
                        .disabled(model.offlineItems.isEmpty)
                    Spacer()
                    PauseControls().fixedSize()
                }
                if let update = model.updateStatus, update.available, let link = update.url, let url = URL(string: link) {
                    Link(destination: url) {
                        Label("CloudWire \(update.latest ?? "") is available", systemImage: "arrow.down.app")
                    }
                }
            }
            Divider()
            VStack(alignment: .leading, spacing: 6) {
                menuButton(String(localized: "Open CloudWire")) { WindowRouter.shared.showMain() }
                menuButton(String(localized: "Settings…")) { WindowRouter.shared.showSettings() }
                Divider()
                menuButton(String(localized: "Close Interface")) { NSApp.terminate(nil) }
                menuButton(String(localized: "Quit CloudWire Completely…")) { confirmQuit = true }
            }
        }
        .padding(14)
        .frame(width: 320)
        .alert(String(localized: "Quit CloudWire completely?"), isPresented: $confirmQuit) {
            Button("Quit Completely", role: .destructive) {
                Task {
                    await model.shutdownCore()
                    NSApp.terminate(nil)
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Mounts are ejected and syncing stops until you open CloudWire again.")
        }
    }

    private var header: some View {
        HStack(spacing: 10) {
            Image(nsImage: NSApp.applicationIconImage).resizable().frame(width: 28, height: 28)
            VStack(alignment: .leading, spacing: 1) {
                Text("CloudWire").font(.headline)
                Text(statusLine).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
        }
    }

    private var statusLine: String {
        guard model.isConnected else {
            switch model.coreState {
            case .needsApproval: return String(localized: "Background service not allowed")
            case .failed: return String(localized: "Background service not reachable")
            default: return String(localized: "Starting…")
            }
        }
        switch model.aggregateStatus {
        case .idle: return String(localized: "Everything is up to date")
        case .syncing: return String(localized: "Syncing…")
        case .paused:
            if let rule = model.pause?.activeRules.first { return String(localized: "Paused: \(PauseReason.label(rule.id))") }
            if model.pause?.manualUntil != nil { return String(localized: "Paused manually") }
            return String(localized: "Paused")
        case .error: return String(localized: "Needs your attention")
        }
    }

    private func sectionTitle(_ text: String) -> some View {
        Text(text).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
    }

    private func menuButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}
