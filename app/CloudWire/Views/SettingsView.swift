import CloudWireKit
import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            if model.hasSettings {
                TabView {
                    GeneralSettings()
                        .tabItem { Label("General", systemImage: "gearshape") }
                    SyncSettings()
                        .tabItem { Label("Sync", systemImage: "arrow.triangle.2.circlepath") }
                    MountSettings()
                        .tabItem { Label("Mounts", systemImage: SidebarSection.mounts.symbol) }
                    NotificationSettings()
                        .tabItem { Label("Notifications", systemImage: "bell") }
                    LogSettings()
                        .tabItem { Label("Log", systemImage: "list.bullet.rectangle") }
                    AboutSettings()
                        .tabItem { Label("About", systemImage: "info.circle") }
                }
            } else {
                CoreUnavailableView().frame(height: 300)
            }
        }
        .frame(width: 600)
        .modelAlert(model)
    }
}

/// A folder setting shown with `~` and changed through an open panel.
private struct FolderSetting: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let keyPath: WritableKeyPath<CoreSettings, String>
    let key: String

    var body: some View {
        LabeledContent(title) {
            HStack {
                Text(model.settings[keyPath: keyPath]).lineLimit(1).truncationMode(.middle).foregroundStyle(.secondary)
                Button("Choose…") {
                    if let path = Panels.chooseFolder(message: "", startingAt: model.settings[keyPath: keyPath]) {
                        model.settingBinding(keyPath, [key]).wrappedValue = CorePaths.abbreviate(path)
                    }
                }
            }
        }
    }
}

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
                if !unit.isEmpty { Text(unit).foregroundStyle(.secondary) }
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
                FolderSetting(title: "Base folder for Offline Items", keyPath: \.baseFolder, key: "baseFolder")
                FolderSetting(title: "Folder for Mounts", keyPath: \.mountFolder, key: "mountFolder")
            }
        }
        .formStyle(.grouped)
    }
}

// MARK: - Sync

private struct SyncSettings: View {
    @Environment(AppModel.self) private var model
    @State private var excludes = ""

    var body: some View {
        Form {
            Section("Timing") {
                NumberSetting(title: "Quiet period after local changes",
                              value: model.settingBinding(\.quietPeriodSeconds, ["quietPeriodSeconds"]),
                              range: 5...3600, unit: String(localized: "s"), step: 5)
                NumberSetting(title: "Cloud change polling",
                              value: model.settingBinding(\.pollIntervalSeconds, ["pollIntervalSeconds"]),
                              range: 10...3600, unit: String(localized: "s"), step: 10)
                NumberSetting(title: "Nextcloud check interval",
                              value: model.settingBinding(\.nextcloudEtagSeconds, ["nextcloudEtagSeconds"]),
                              range: 10...3600, unit: String(localized: "s"), step: 10)
                NumberSetting(title: "Check interval for other providers",
                              value: model.settingBinding(\.genericCheckSeconds, ["genericCheckSeconds"]),
                              range: 60...86400, unit: String(localized: "s"), step: 60)
                NumberSetting(title: "Full safety sync every",
                              value: model.settingBinding(\.safetyFullSyncMinutes, ["safetyFullSyncMinutes"]),
                              range: 5...1440, unit: String(localized: "min"), step: 5)
            }
            Section("Pause Rules") {
                Toggle("Studio Mode: pause while these apps run",
                       isOn: model.settingBinding(\.pauseRules.studioMode.enabled, ["pauseRules", "studioMode", "enabled"]))
                StudioAppList()
                Toggle("Pause on battery power or in Low Power Mode",
                       isOn: model.settingBinding(\.pauseRules.battery.enabled, ["pauseRules", "battery", "enabled"]))
                Toggle("Pause on metered networks (personal hotspot)",
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
                Toggle("Limit bandwidth", isOn: model.settingBinding(\.bandwidth.enabled, ["bandwidth", "enabled"]))
                BandwidthSetting(title: "Upload", keyPath: \.bandwidth.uploadMiBps, key: "uploadMiBps", fallback: 5)
                    .disabled(!model.settings.bandwidth.enabled)
                BandwidthSetting(title: "Download", keyPath: \.bandwidth.downloadMiBps, key: "downloadMiBps", fallback: 20)
                    .disabled(!model.settings.bandwidth.enabled)
            }
            Section("Default Excludes") {
                TextEditor(text: $excludes)
                    .font(.body.monospaced())
                    .frame(height: 110)
                HStack {
                    Text("One pattern per line. Applies to new Offline Items.").font(.caption).foregroundStyle(.secondary)
                    Spacer()
                    Button("Restore Defaults") { excludes = CoreSettings.defaultExcludes.joined(separator: "\n") }
                    Button("Apply") {
                        let patterns = excludes.split(whereSeparator: \.isNewline)
                            .map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
                        model.settingBinding(\.defaultExcludes, ["defaultExcludes"]).wrappedValue = patterns
                    }
                    .disabled(excludesUnchanged)
                }
            }
        }
        .formStyle(.grouped)
        .frame(height: 560)
        .onAppear { excludes = model.settings.defaultExcludes.joined(separator: "\n") }
    }

    private var excludesUnchanged: Bool {
        excludes.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty } == model.settings.defaultExcludes
    }
}

private struct BandwidthSetting: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let keyPath: WritableKeyPath<CoreSettings, Int>
    let key: String
    let fallback: Int

    var body: some View {
        let binding = model.settingBinding(keyPath, ["bandwidth", key])
        LabeledContent(title) {
            HStack {
                Toggle("Unlimited", isOn: Binding(get: { binding.wrappedValue == 0 },
                                                  set: { binding.wrappedValue = $0 ? 0 : fallback }))
                    .toggleStyle(.checkbox)
                if binding.wrappedValue != 0 {
                    TextField("", value: Binding(get: { binding.wrappedValue }, set: { binding.wrappedValue = max(1, $0) }),
                              format: .number)
                        .labelsHidden()
                        .multilineTextAlignment(.trailing)
                        .frame(width: 60)
                        .textFieldStyle(.roundedBorder)
                    Text("MiB/s").foregroundStyle(.secondary)
                }
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
                        Link("Get FUSE-T", destination: URL(string: "https://github.com/macos-fuse-t/fuse-t/releases")!)
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

    var body: some View {
        Form {
            Toggle("Errors", isOn: model.settingBinding(\.notifications.errors, ["notifications", "errors"]))
            Toggle("Conflicts", isOn: model.settingBinding(\.notifications.conflicts, ["notifications", "conflicts"]))
            Toggle("Mass-Delete Guard", isOn: model.settingBinding(\.notifications.massDelete,
                                                                   ["notifications", "massDelete"]))
            Toggle("Link copied", isOn: model.settingBinding(\.notifications.linkCopied, ["notifications", "linkCopied"]))
            Button("Open Notification Settings…") {
                if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") {
                    NSWorkspace.shared.open(url)
                }
            }
            .buttonStyle(.link)
        }
        .formStyle(.grouped)
    }
}

// MARK: - Log

private struct LogSettings: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Form {
            Picker("Log level", selection: model.settingBinding(\.log.level, ["log", "level"])) {
                ForEach(ActivityLevel.allCases, id: \.self) { Text($0.label).tag($0) }
            }
            NumberSetting(title: "Keep entries for", value: model.settingBinding(\.log.retentionDays, ["log", "retentionDays"]),
                          range: 1...365, unit: String(localized: "days"))
            NumberSetting(title: "Maximum size", value: model.settingBinding(\.log.maxMB, ["log", "maxMB"]),
                          range: 5...1024, unit: "MB", step: 5)
            LabeledContent("Activity log") {
                Button("Export…") { exportActivityLog(model) }
            }
            LabeledContent("Service log") {
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
                Text("CloudWire is open source under the MIT license.")
                Text("rclone – MIT license, © Nick Craig-Wood and contributors.")
                Text("libfuse headers (FUSE-T) – LGPL-2.1.")
            }
            .font(.callout)
        }
        .formStyle(.grouped)
    }
}
