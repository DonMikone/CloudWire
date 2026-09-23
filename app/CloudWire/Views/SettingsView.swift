import CloudWireKit
import SwiftUI
import UserNotifications

/// The tabs of the Settings window.
enum SettingsTab: Hashable, CaseIterable {
    case general, sync, mounts, notifications, log, about

    var title: LocalizedStringKey {
        switch self {
        case .general: "General"
        case .sync: "Sync"
        case .mounts: "Mounts"
        case .notifications: "Notifications"
        case .log: "Log"
        case .about: "About"
        }
    }

    var symbol: String {
        switch self {
        case .general: "gearshape"
        case .sync: "arrow.triangle.2.circlepath"
        case .mounts: SidebarSection.mounts.symbol
        case .notifications: "bell"
        case .log: "list.bullet.rectangle"
        case .about: "info.circle"
        }
    }

    @MainActor @ViewBuilder
    var content: some View {
        switch self {
        case .general: GeneralSettings()
        case .sync: SyncSettings()
        case .mounts: MountSettings()
        case .notifications: NotificationSettings()
        case .log: LogSettings()
        case .about: AboutSettings()
        }
    }
}

struct SettingsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            if model.hasSettings && model.isConnected {
                TabView {
                    ForEach(SettingsTab.allCases, id: \.self) { tab in
                        tab.content
                            .tabItem { Label(tab.title, systemImage: tab.symbol) }
                    }
                }
            } else {
                CoreUnavailableView().frame(height: 300)
            }
        }
        .frame(width: 600)
        .modelAlert(model)
    }
}

#if DEBUG
extension SettingsView {
    /// The Settings window on `tab`, with a copy of its toolbar tabs, which offscreen rendering cannot
    /// capture (`--export-snapshots`).
    static func snapshot(tab: SettingsTab) -> some View {
        VStack(spacing: 0) {
            HStack(spacing: 2) {
                ForEach(SettingsTab.allCases, id: \.self) { item in
                    VStack(spacing: 3) {
                        Image(systemName: item.symbol).font(.title2).frame(height: 24)
                        Text(item.title).font(.caption)
                    }
                    .frame(width: 84, height: 52)
                    .foregroundStyle(item == tab ? Color.accentColor : Color.secondary)
                    .background(item == tab ? Color.primary.opacity(0.08) : .clear, in: RoundedRectangle(cornerRadius: 6))
                }
            }
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity)
            Divider()
            tab.content
        }
        .frame(width: 600)
    }
}
#endif

/// A folder setting shown with `~` and changed through an open panel.
private struct FolderSetting: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let keyPath: WritableKeyPath<CoreSettings, String>
    let key: String
    let message: String

    var body: some View {
        LabeledContent(title) {
            HStack {
                Text(model.settings[keyPath: keyPath]).lineLimit(1).truncationMode(.middle).foregroundStyle(.secondary)
                Button("Choose…") {
                    if let path = Panels.chooseFolder(message: message, startingAt: model.settings[keyPath: keyPath]) {
                        model.settingBinding(keyPath, [key]).wrappedValue = CorePaths.abbreviate(path)
                    }
                }
            }
        }
    }
}

/// Units get a fixed width so fields and steppers line up across rows.
private let unitWidth: CGFloat = 40

/// Integer setting with a text field and stepper.
private struct NumberSetting: View {
    let title: LocalizedStringKey
    @Binding var value: Int
    let range: ClosedRange<Int>
    var unit: String = ""
    var step = 1

    var body: some View {
        LabeledContent(title) {
            HStack(spacing: 4) {
                TextField("", value: Binding(get: { value }, set: { value = min(max($0, range.lowerBound), range.upperBound) }),
                          format: .number)
                    .labelsHidden()
                    .multilineTextAlignment(.trailing)
                    .frame(width: 70)
                    .textFieldStyle(.roundedBorder)
                Stepper("", value: $value, in: range, step: step).labelsHidden()
                Text(unit).foregroundStyle(.secondary).frame(width: unitWidth, alignment: .leading)
            }
        }
    }
}

// MARK: - General

private struct GeneralSettings: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Form {
            Section {
                Toggle("Start CloudWire at login", isOn: model.settingBinding(\.autostart, ["autostart"]))
                Toggle("Show icon in the menu bar", isOn: model.settingBinding(\.menuBarIcon, ["menuBarIcon"]))
                Text("Without the menu bar icon, only the background service runs; open CloudWire from the Applications folder to change settings.")
                    .font(.caption).foregroundStyle(.secondary)
                Toggle("Check daily for updates", isOn: model.settingBinding(\.updates.check, ["updates", "check"]))
            }
            Section("Folders") {
                FolderSetting(title: "Base folder for Offline Items", keyPath: \.baseFolder, key: "baseFolder",
                              message: String(localized: "Choose the base folder for new Offline Items. Existing Offline Items stay where they are."))
                FolderSetting(title: "Folder for Mounts", keyPath: \.mountFolder, key: "mountFolder",
                              message: String(localized: "Choose the folder in which new Mounts appear."))
            }
        }
        .formStyle(.grouped)
    }
}

// MARK: - Sync

private struct SyncSettings: View {
    @Environment(AppModel.self) private var model
    @Environment(\.isSnapshot) private var isSnapshot
    @State private var excludes = ""

    var body: some View {
        Form {
            Section("Intervals") {
                NumberSetting(title: "Quiet period after local changes",
                              value: model.settingBinding(\.quietPeriodSeconds, ["quietPeriodSeconds"]),
                              range: 5...3600, unit: String(localized: "s"), step: 5)
                NumberSetting(title: "Full safety sync every",
                              value: model.settingBinding(\.safetyFullSyncMinutes, ["safetyFullSyncMinutes"]),
                              range: 5...1440, unit: String(localized: "min"), step: 5)
            }
            Section("Check the Cloud for Changes") {
                NumberSetting(title: "Providers with change notifications (e.g. Google Drive, OneDrive)",
                              value: model.settingBinding(\.pollIntervalSeconds, ["pollIntervalSeconds"]),
                              range: 10...3600, unit: String(localized: "s"), step: 10)
                NumberSetting(title: "Nextcloud",
                              value: model.settingBinding(\.nextcloudEtagSeconds, ["nextcloudEtagSeconds"]),
                              range: 10...3600, unit: String(localized: "s"), step: 10)
                NumberSetting(title: "All other providers",
                              value: model.settingBinding(\.genericCheckSeconds, ["genericCheckSeconds"]),
                              range: 60...86400, unit: String(localized: "s"), step: 60)
            }
            Section("Pause Rules") {
                Toggle("Studio Mode: pause while these apps run",
                       isOn: model.settingBinding(\.pauseRules.studioMode.enabled, ["pauseRules", "studioMode", "enabled"]))
                StudioAppList()
                Toggle("Pause on battery power or in Low Power Mode",
                       isOn: model.settingBinding(\.pauseRules.battery.enabled, ["pauseRules", "battery", "enabled"]))
                Toggle("Pause on a personal hotspot or in Low Data Mode",
                       isOn: model.settingBinding(\.pauseRules.meteredNetwork.enabled,
                                                  ["pauseRules", "meteredNetwork", "enabled"]))
                Toggle("Pause while the CPU is busy",
                       isOn: model.settingBinding(\.pauseRules.cpu.enabled, ["pauseRules", "cpu", "enabled"]))
                NumberSetting(title: "CPU threshold",
                              value: model.settingBinding(\.pauseRules.cpu.thresholdPercent,
                                                          ["pauseRules", "cpu", "thresholdPercent"]),
                              range: 10...100, unit: "%", step: 5)
                    .disabled(!model.settings.pauseRules.cpu.enabled)
            }
            Section("Bandwidth") {
                BandwidthSetting(title: "Upload", side: \.uploadMiBps, fallback: 5)
                BandwidthSetting(title: "Download", side: \.downloadMiBps, fallback: 20)
            }
            Section("Default Excludes") {
                TextEditor(text: $excludes)
                    .font(.body.monospaced())
                    .frame(height: 110)
                HStack {
                    Text("One pattern per line. Applies to new Offline Items.").font(.caption).foregroundStyle(.secondary)
                    Spacer()
                    Button("Restore Defaults") {
                        excludes = CoreSettings.defaultExcludes.joined(separator: "\n")
                        applyExcludes()
                    }
                    Button("Apply", action: applyExcludes)
                        .disabled(excludesUnchanged)
                }
            }
        }
        .formStyle(.grouped)
        // Snapshots show the whole tab instead of a scroll view.
        .frame(height: isSnapshot ? nil : 560)
        .onAppear { excludes = model.settings.defaultExcludes.joined(separator: "\n") }
        // Tab switch or closing the window: keep edits instead of dropping them silently.
        .onDisappear(perform: applyExcludes)
    }

    private var patterns: [String] {
        excludes.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }

    private var excludesUnchanged: Bool { patterns == model.settings.defaultExcludes }

    private func applyExcludes() {
        guard !excludesUnchanged else { return }
        model.settingBinding(\.defaultExcludes, ["defaultExcludes"]).wrappedValue = patterns
    }
}

/// One direction of the bandwidth limit. Both rows keep their layout: "Unlimited" only disables the field.
private struct BandwidthSetting: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let side: WritableKeyPath<CoreSettings.Bandwidth, Int>
    let fallback: Int

    var body: some View {
        let bandwidth = model.settingBinding(\.bandwidth, ["bandwidth"])
        let limit = { bandwidth.wrappedValue.limit(side) }
        let setLimit = { (value: Int) in bandwidth.wrappedValue = bandwidth.wrappedValue.settingLimit(side, to: value) }
        LabeledContent(title) {
            HStack {
                Toggle("Unlimited", isOn: Binding(get: { limit() == 0 }, set: { setLimit($0 ? 0 : fallback) }))
                    .toggleStyle(.checkbox)
                TextField("", value: Binding(get: { limit() == 0 ? fallback : limit() }, set: { setLimit(max(1, $0)) }),
                          format: .number)
                    .labelsHidden()
                    .multilineTextAlignment(.trailing)
                    .frame(width: 60)
                    .textFieldStyle(.roundedBorder)
                    .disabled(limit() == 0)
                Text("MiB/s").foregroundStyle(.secondary).frame(width: unitWidth, alignment: .leading)
            }
        }
    }
}

private struct StudioAppList: View {
    @Environment(AppModel.self) private var model
    @State private var selection: String?

    private var apps: [String] { model.settings.pauseRules.studioMode.apps }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            List(apps, id: \.self, selection: $selection) { path in
                HStack {
                    Image(nsImage: NSWorkspace.shared.icon(forFile: path))
                        .resizable()
                        .frame(width: 18, height: 18)
                    Text(FileManager.default.displayName(atPath: path))
                    Spacer()
                    Text(CorePaths.abbreviate((path as NSString).deletingLastPathComponent))
                        .font(.caption).foregroundStyle(.secondary)
                }
                .tag(path)
            }
            .listStyle(.bordered)
            .frame(height: 120)
            HStack(spacing: 4) {
                Button {
                    let added = Panels.chooseApplications().filter { !apps.contains($0) }
                    if !added.isEmpty { save(apps + added) }
                } label: {
                    Image(systemName: "plus")
                }
                Button {
                    if let selection { save(apps.filter { $0 != selection }) }
                    selection = nil
                } label: {
                    Image(systemName: "minus")
                }
                .disabled(selection == nil)
            }
            .buttonStyle(.borderless)
        }
        .disabled(!model.settings.pauseRules.studioMode.enabled)
    }

    private func save(_ newApps: [String]) {
        model.settingBinding(\.pauseRules.studioMode.apps, ["pauseRules", "studioMode", "apps"]).wrappedValue = newApps
    }
}

// MARK: - Mounts

private struct MountSettings: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Form {
            NumberSetting(title: "Default cache size",
                          value: model.settingBinding(\.defaultCacheMaxGB, ["defaultCacheMaxGB"]),
                          range: 1...4096, unit: String(localized: "GB"))
            Picker("Default technology", selection: model.settingBinding(\.defaultMountType, ["defaultMountType"])) {
                Text(MountType.nfsmount.label).tag(MountType.nfsmount)
                Text(MountType.cmount.label).tag(MountType.cmount)
                    .selectionDisabled(!model.fuseStatus.available)
            }
            LabeledContent("FUSE") {
                if model.fuseStatus.fuseT {
                    Text("FUSE-T installed").foregroundStyle(.secondary)
                } else if model.fuseStatus.macFUSE {
                    Text("macFUSE installed").foregroundStyle(.secondary)
                } else {
                    HStack {
                        Text("Not installed").foregroundStyle(.secondary)
                        Link("Get FUSE-T…", destination: URL(string: "https://github.com/macos-fuse-t/fuse-t/releases")!)
                    }
                }
            }
            Text("NFS uses the file system client built into macOS and needs no installation. FUSE is optional.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .formStyle(.grouped)
        .task {
            if let status = try? await model.client.fuseStatus() { model.fuseStatus = status }
        }
    }
}

// MARK: - Notifications

private struct NotificationSettings: View {
    @Environment(AppModel.self) private var model
    @State private var denied = false

    var body: some View {
        Form {
            if denied {
                Label("Notifications are turned off in System Settings.", systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            }
            Section("Notify Me About") {
                Toggle("Errors", isOn: model.settingBinding(\.notifications.errors, ["notifications", "errors"]))
                Toggle("Conflicts (Conflict Copy created)",
                       isOn: model.settingBinding(\.notifications.conflicts, ["notifications", "conflicts"]))
                Toggle("Stopped syncs (Mass-Delete Guard)",
                       isOn: model.settingBinding(\.notifications.massDelete, ["notifications", "massDelete"]))
                Toggle("Copied links",
                       isOn: model.settingBinding(\.notifications.linkCopied, ["notifications", "linkCopied"]))
            }
            Button("Open Notification Settings…") { NotificationManager.openSystemSettings() }
                .buttonStyle(.link)
        }
        .formStyle(.grouped)
        .task { await refreshAuthorization() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            Task { await refreshAuthorization() }
        }
    }

    private func refreshAuthorization() async {
        denied = await NotificationManager.shared.authorizationStatus() == .denied
    }
}

// MARK: - Log

private struct LogSettings: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Form {
            Section("Activity Log") {
                Picker("Log level", selection: model.settingBinding(\.log.level, ["log", "level"])) {
                    ForEach(ActivityLevel.allCases, id: \.self) { Text($0.label).tag($0) }
                }
                NumberSetting(title: "Keep entries for", value: model.settingBinding(\.log.retentionDays, ["log", "retentionDays"]),
                              range: 1...365, unit: String(localized: "days"))
                NumberSetting(title: "Maximum size", value: model.settingBinding(\.log.maxMB, ["log", "maxMB"]),
                              range: 5...1024, unit: "MB", step: 5)
                Button("Export…") { exportActivityLog(model) }
            }
            Section("Service Log") {
                Button("Show in Finder") {
                    NSWorkspace.shared.activateFileViewerSelecting([CorePaths.logsDirectory])
                }
            }
        }
        .formStyle(.grouped)
    }
}

// MARK: - About

private struct AboutSettings: View {
    @Environment(AppModel.self) private var model

    private var appVersion: String {
        let info = Bundle.main.infoDictionary
        let version = info?["CFBundleShortVersionString"] as? String ?? ""
        let build = info?["CFBundleVersion"] as? String ?? ""
        return "\(version) (\(build))"
    }

    var body: some View {
        Form {
            Section {
                HStack(spacing: 14) {
                    Image(nsImage: NSApp.applicationIconImage)
                        .resizable()
                        .frame(width: 64, height: 64)
                    VStack(alignment: .leading) {
                        Text("CloudWire").font(.title2.weight(.semibold))
                        Text("Your cloud, wired to your Mac.").foregroundStyle(.secondary)
                    }
                }
                LabeledContent("App", value: appVersion)
                LabeledContent("Background service", value: model.coreInfo?.version ?? "–")
                LabeledContent("rclone", value: model.coreInfo?.rcloneVersion ?? "–")
            }
            Section("Links") {
                Link("Project on GitHub", destination: URL(string: "https://github.com/DonMikone/CloudWire")!)
                Link("Releases", destination: URL(string: "https://github.com/DonMikone/CloudWire/releases")!)
            }
            Section("Acknowledgements") {
                Group {
                    Text("CloudWire is open source under the MIT license.")
                    Text("rclone – MIT license, © Nick Craig-Wood and contributors.")
                    Text("libfuse headers (FUSE-T) – LGPL-2.1.")
                }
                .font(.callout)
            }
        }
        .formStyle(.grouped)
    }
}
