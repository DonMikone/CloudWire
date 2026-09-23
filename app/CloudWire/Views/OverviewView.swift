import CloudWireKit
import SwiftUI

struct OverviewView: View {
    @Environment(AppModel.self) private var model
    /// Ids of Recent Problems the user hid, comma-separated.
    @AppStorage("hiddenProblems") private var hiddenProblems = ""

    private let columns = [GridItem(.adaptive(minimum: 250), spacing: 14)]

    var body: some View {
        SectionScaffold(title: String(localized: "Overview")) {
            EmptyView()
        } content: {
            ScrollView {
                content
                    .padding(.horizontal, 20)
                    .padding(.bottom, 20)
            }
        }
    }

    /// Recent Problems without those the user hid or whose element is healthy again.
    private var visibleProblems: [ActivityEntry] {
        let hidden = Set(hiddenProblems.split(separator: ",").compactMap { Int64($0) })
        return model.recentProblems.filter {
            !hidden.contains($0.id) && !$0.isResolved(mounts: model.mounts, offlineItems: model.offlineItems)
        }
    }

    private func hide(_ entry: ActivityEntry) {
        // Only ids that can still appear are kept, so the list stays short.
        let current = Set(model.recentProblems.map(\.id))
        let hidden = Set(hiddenProblems.split(separator: ",").compactMap { Int64($0) }).intersection(current)
        hiddenProblems = hidden.union([entry.id]).sorted().map(String.init).joined(separator: ",")
    }

    private var content: some View {
            VStack(alignment: .leading, spacing: 20) {
                LazyVGrid(columns: columns, alignment: .leading, spacing: 14) {
                    statusCard
                    mountsCard
                    offlineCard
                    pauseCard
                    if let update = model.updateStatus, update.available {
                        updateCard(update)
                    }
                }

                VStack(alignment: .leading, spacing: 8) {
                    Text("Recent Problems").font(.headline)
                    let problems = Array(visibleProblems.prefix(6))
                    if problems.isEmpty {
                        Card {
                            Label("No errors or warnings. Everything is running smoothly.",
                                  systemImage: "checkmark.seal")
                                .foregroundStyle(.secondary)
                        }
                    } else {
                        Card {
                            VStack(alignment: .leading, spacing: 10) {
                                ForEach(problems) { entry in
                                    ProblemRow(entry: entry) { hide(entry) }
                                    if entry.id != problems.last?.id { Divider() }
                                }
                                Button("Show Activity Log") { model.selection = .activity }
                                    .buttonStyle(.link)
                            }
                        }
                    }
                }
            }
    }

    private var statusCard: some View {
        Card(minHeight: 150) {
            VStack(alignment: .leading, spacing: 8) {
                Label("CloudWire", systemImage: "bolt.horizontal.circle.fill")
                    .font(.headline)
                    .foregroundStyle(Color.accentColor)
                StatusLabel(text: String(localized: "Background service running"), color: .green)
                if let info = model.coreInfo {
                    Text("Version \(info.version) · rclone \(info.rcloneVersion)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Text("\(model.remoteConnections.count) connections")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var mountsCard: some View {
        let mounted = model.mounts.filter(\.isMounted).count
        let failed = model.mounts.filter { $0.state == .error }.count
        return Card(minHeight: 150) {
            VStack(alignment: .leading, spacing: 8) {
                Label("Mounts", systemImage: SidebarSection.mounts.symbol).font(.headline)
                Text("\(mounted) of \(model.mounts.count) mounted")
                    .font(.title3.weight(.medium))
                if failed > 0 {
                    StatusLabel(text: String(localized: "\(failed) with errors"), color: .red)
                }
                Button("Manage Mounts") { model.selection = .mounts }
                    .buttonStyle(.link)
            }
        }
    }

    private var offlineCard: some View {
        let syncing = model.offlineItems.filter { $0.state == .syncing }.count
        let problems = model.offlineItems.filter { $0.state == .error || $0.state == .needsConfirmation }.count
        let upToDate = model.offlineItems.filter { $0.state == .idle }.count
        return Card(minHeight: 150) {
            VStack(alignment: .leading, spacing: 8) {
                Label("Offline Items", systemImage: SidebarSection.offline.symbol).font(.headline)
                Text("\(upToDate) of \(model.offlineItems.count) up to date")
                    .font(.title3.weight(.medium))
                if syncing > 0 {
                    StatusLabel(text: String(localized: "\(syncing) syncing"), color: .orange)
                }
                if problems > 0 {
                    StatusLabel(text: String(localized: "\(problems) need attention"), color: .red)
                }
                Button("Manage Offline Items") { model.selection = .offline }
                    .buttonStyle(.link)
            }
        }
    }

    private var pauseCard: some View {
        Card(minHeight: 150) {
            VStack(alignment: .leading, spacing: 8) {
                Label("Sync", systemImage: "pause.circle").font(.headline)
                PauseSummary()
                PauseControls()
            }
        }
    }

    private func updateCard(_ update: UpdateStatus) -> some View {
        Card(minHeight: 150) {
            VStack(alignment: .leading, spacing: 8) {
                Label("Update Available", systemImage: "arrow.down.app").font(.headline)
                Text("CloudWire \(update.latest ?? "") is available (you have \(update.current)).")
                    .font(.callout)
                if let link = update.url, let url = URL(string: link) {
                    Link("Download", destination: url)
                }
            }
        }
    }
}

struct ProblemRow: View {
    let entry: ActivityEntry
    var onHide: (() -> Void)?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: entry.level == .error ? "exclamationmark.circle.fill" : "exclamationmark.triangle.fill")
                .foregroundStyle(entry.level.color)
            VStack(alignment: .leading, spacing: 2) {
                let parts = entry.text.parts()
                Text(parts.text).lineLimit(2)
                if let detail = parts.detail {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                Text("\(entry.category.label) · \(Format.relative(ms: entry.ts))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if let onHide {
                Spacer(minLength: 0)
                Button("Hide", action: onHide)
                    .buttonStyle(.borderless)
                    .font(.callout)
            }
        }
        .contextMenu {
            if let onHide { Button("Hide", action: onHide) }
        }
    }
}

/// Text describing the current pause state.
struct PauseSummary: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let pause = model.pause, pause.effective {
                if let until = pause.manualUntil {
                    if until == -1 {
                        StatusLabel(text: String(localized: "Paused until you resume"), color: .orange)
                    } else {
                        StatusLabel(
                            text: String(localized: "Paused until \(Format.date(ms: until).formatted(date: .omitted, time: .shortened))"),
                            color: .orange)
                    }
                }
                ForEach(pause.activeRules) { rule in
                    StatusLabel(text: rule.text.localized(), color: .orange)
                }
            } else {
                StatusLabel(text: String(localized: "Syncing is active"), color: .green)
            }
        }
    }
}

/// Classic pause/play controls for syncing. The pause button pauses until resumed; its arrow
/// offers 1 hour or until tomorrow morning. While paused manually, a play button resumes. While
/// Pause Rules hold syncing, a play button starts a sync right away (background priority and
/// bandwidth limit still apply). Stacks vertically when the row does not fit.
struct PauseControls: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) { controls }.fixedSize()
            VStack(alignment: .leading, spacing: 8) { controls }.fixedSize()
        }
        .labelStyle(.titleAndIcon)
    }

    @ViewBuilder
    private var controls: some View {
        if model.pause?.manualUntil != nil {
            Button { model.resumeSync() } label: {
                Label("Resume", systemImage: "play.fill")
            }
            .help("Resume syncing")
        } else {
            Menu {
                Button("For 1 Hour") { model.pause(until: Date().addingTimeInterval(3600)) }
                Button(Self.tomorrowMorningTitle()) { model.pause(until: Self.tomorrowMorning()) }
                Button("Until I Resume") { model.pause(until: nil) }
            } label: {
                Label("Pause", systemImage: "pause.fill")
            } primaryAction: {
                model.pause(until: nil)
            }
            .help(Text("Pause syncing until you resume. The arrow offers 1 hour or until tomorrow \(Self.tomorrowMorningTime())."))
            if Self.offersSyncNow(model.pause) {
                Button { model.syncNow() } label: {
                    Label("Sync Now", systemImage: "play.fill")
                }
                .help("Sync now despite the active Pause Rules")
            }
        }
    }

    /// Whether the controls show their own "Sync Now" button: while Pause Rules hold syncing and
    /// the user has not paused manually.
    static func offersSyncNow(_ pause: PauseStatus?) -> Bool {
        guard let pause, pause.manualUntil == nil else { return false }
        return !pause.activeRules.isEmpty
    }

    static func tomorrowMorning(now: Date = Date(), calendar: Calendar = .current) -> Date {
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: now)) ?? now
        return calendar.date(bySettingHour: 8, minute: 0, second: 0, of: tomorrow) ?? tomorrow
    }

    /// "Until Tomorrow 08:00" in the user's time format.
    static func tomorrowMorningTitle() -> String {
        String(localized: "Until Tomorrow \(tomorrowMorningTime())")
    }

    private static func tomorrowMorningTime() -> String {
        tomorrowMorning().formatted(date: .omitted, time: .shortened)
    }
}
