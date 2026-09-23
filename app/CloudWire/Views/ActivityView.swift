import CloudWireKit
import SwiftUI
import UniformTypeIdentifiers

struct ActivityView: View {
    @Environment(AppModel.self) private var model
    @State private var entries: [ActivityEntry] = []
    @State private var levels: Set<ActivityLevel> = []
    @State private var categories: Set<ActivityCategory> = []
    @State private var search = ""
    @State private var appliedSearch = ""
    @State private var selection: ActivityEntry.ID?
    @State private var loading = false
    @State private var hasMore = true
    @State private var error: String?

    private static let pageSize = 200

    var body: some View {
        SectionScaffold(title: String(localized: "Activity"),
                        subtitle: String(localized: "Errors, actions and every sync run.")) {
            HStack {
                filterMenu
                SearchField(prompt: String(localized: "Search"), text: $search)
                    .frame(width: 200)
                Button {
                    export()
                } label: {
                    Label("Export…", systemImage: "square.and.arrow.up")
                }
            }
        } content: {
            VSplitView {
                table
                    .frame(minHeight: 240)
                if let entry = entries.first(where: { $0.id == selection }) {
                    detail(entry)
                        .frame(minHeight: 120, idealHeight: 200, maxHeight: 260)
                }
            }
        }
        .task(id: filterKey) { await reload() }
        .task(id: search) {
            // Live search, debounced while typing; clearing applies at once.
            if !search.isEmpty {
                do {
                    try await Task.sleep(for: .milliseconds(300))
                } catch {
                    return
                }
            }
            appliedSearch = search
        }
        .onChange(of: model.latestActivity) { _, entry in
            if let entry, matches(entry), !entries.contains(where: { $0.id == entry.id }) {
                entries.insert(entry, at: 0)
            }
        }
    }

    private var filterKey: String {
        "\(levels.map(\.rawValue).sorted())|\(categories.map(\.rawValue).sorted())|\(appliedSearch)"
    }

    private var isFiltered: Bool { !levels.isEmpty || !categories.isEmpty || !appliedSearch.isEmpty }

    private var filterMenu: some View {
        Menu {
            Section("Level") {
                ForEach(ActivityLevel.allCases, id: \.self) { level in
                    Toggle(level.label, isOn: Binding(
                        get: { levels.contains(level) },
                        set: { if $0 { levels.insert(level) } else { levels.remove(level) } }))
                }
            }
            Section("Category") {
                ForEach(ActivityCategory.allCases, id: \.self) { category in
                    Toggle(category.label, isOn: Binding(
                        get: { categories.contains(category) },
                        set: { if $0 { categories.insert(category) } else { categories.remove(category) } }))
                }
            }
            Divider()
            Button("Show All") {
                levels.removeAll()
                categories.removeAll()
            }
        } label: {
            Label(levels.isEmpty && categories.isEmpty ? String(localized: "All Entries") : String(localized: "Filtered"),
                  systemImage: "line.3.horizontal.decrease.circle")
        }
        .fixedSize()
    }

    private var table: some View {
        Table(entries, selection: $selection) {
            TableColumn("Time") { entry in
                Text(entry.date.formatted(date: .numeric, time: .standard)).monospacedDigit()
            }
            .width(min: 140, ideal: 160, max: 200)
            TableColumn("Level") { entry in
                Text(entry.level.label).foregroundStyle(entry.level.color)
            }
            .width(min: 60, ideal: 70, max: 90)
            TableColumn("Category") { entry in
                Text(entry.category.label)
            }
            .width(min: 70, ideal: 90, max: 120)
            TableColumn("Message") { entry in
                Text(entry.text.localized()).lineLimit(1)
                    .onAppear {
                        if entry.id == entries.last?.id { Task { await loadMore() } }
                    }
            }
        }
        .onCopyCommand {
            guard let entry = entries.first(where: { $0.id == selection }) else { return [] }
            return [NSItemProvider(object: clipboardText(entry) as NSString)]
        }
        .overlay {
            if let error {
                InlineError(message: error).padding()
            } else if entries.isEmpty && !loading {
                if isFiltered {
                    ContentUnavailableView {
                        Label("No Matching Entries", systemImage: "magnifyingglass")
                    } actions: {
                        Button("Reset Filters") {
                            levels.removeAll()
                            categories.removeAll()
                            search = ""
                            appliedSearch = ""
                        }
                    }
                } else {
                    Text("No entries.").foregroundStyle(.secondary)
                }
            }
        }
    }

    private func detail(_ entry: ActivityEntry) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text(entry.level.label).foregroundStyle(entry.level.color).font(.headline)
                    Text("\(entry.category.label) · \(entry.date.formatted(date: .abbreviated, time: .standard))")
                        .foregroundStyle(.secondary)
                    Spacer()
                    Button("Copy") { copyToPasteboard(clipboardText(entry)) }
                }
                let parts = entry.text.parts()
                Text(parts.text).textSelection(.enabled)
                if let detail = parts.detail {
                    DetailText(detail)
                }
                if let runId = entry.runId {
                    Text("Files of this sync run").font(.headline).padding(.top, 4)
                    RunFilesView(runId: runId).frame(height: 160)
                } else if let details = entry.details {
                    Text(details.displayString)
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    /// Time, level, category, text and details, for bug reports.
    private func clipboardText(_ entry: ActivityEntry) -> String {
        var text = "\(entry.date.formatted(date: .abbreviated, time: .standard)) [\(entry.level.label)] "
            + "\(entry.category.label): \(entry.text.localized())"
        if let details = entry.details { text += "\n" + details.displayString }
        return text
    }

    private var filter: CoreClient.ActivityFilter {
        CoreClient.ActivityFilter(levels: Array(levels), categories: Array(categories), search: appliedSearch,
                                  limit: Self.pageSize)
    }

    /// Mirrors the Core's search, which knows only the English message, and also matches the shown text.
    private func matches(_ entry: ActivityEntry) -> Bool {
        (levels.isEmpty || levels.contains(entry.level)) && (categories.isEmpty || categories.contains(entry.category))
            && (appliedSearch.isEmpty || entry.message.localizedCaseInsensitiveContains(appliedSearch)
                || entry.text.localized().localizedCaseInsensitiveContains(appliedSearch))
    }

    private func reload() async {
        loading = true
        error = nil
        defer { loading = false }
        do {
            entries = try await model.loadActivity(filter)
            hasMore = entries.count >= Self.pageSize
        } catch is CancellationError {
        } catch {
            entries = []
            self.error = ErrorText.alert(for: error).message
        }
    }

    private func loadMore() async {
        guard hasMore, !loading, let last = entries.last else { return }
        loading = true
        defer { loading = false }
        var next = filter
        next.before = last.ts
        if let more = try? await model.loadActivity(next) {
            let known = Set(entries.map(\.id))
            entries.append(contentsOf: more.filter { !known.contains($0.id) })
            hasMore = more.count >= Self.pageSize
        }
    }

    private func export() {
        exportActivityLog(model)
    }
}

/// Save panel with a CSV/JSON format popup → `activity.export`.
@MainActor
func exportActivityLog(_ model: AppModel) {
    let panel = NSSavePanel()
    panel.nameFieldStringValue = "CloudWire-Activity.csv"
    panel.message = String(localized: "Export the activity log")
    let accessory = ExportFormatAccessory(panel: panel)
    panel.accessoryView = accessory.view
    guard panel.runModal() == .OK, let url = panel.url else { return }
    let format = accessory.format
    model.perform {
        let count = try await model.client.exportActivity(format: format, path: url.path)
        model.alert = AlertContent(title: String(localized: "Export complete"),
                                   message: String(localized: "\(count) entries saved to \(url.lastPathComponent)."))
    }
}

/// "Format: CSV/JSON" popup that keeps the panel's content type and file extension in step.
@MainActor
private final class ExportFormatAccessory: NSObject {
    private static let formats: [(format: CoreClient.ExportFormat, title: String, type: UTType)] = [
        (.csv, "CSV", .commaSeparatedText),
        (.json, "JSON", .json),
    ]

    let view: NSView
    private let panel: NSSavePanel
    private let popup = NSPopUpButton(frame: .zero, pullsDown: false)

    var format: CoreClient.ExportFormat { Self.formats[max(popup.indexOfSelectedItem, 0)].format }

    init(panel: NSSavePanel) {
        self.panel = panel
        popup.addItems(withTitles: Self.formats.map(\.title))
        let stack = NSStackView(views: [NSTextField(labelWithString: String(localized: "Format:")), popup])
        stack.edgeInsets = NSEdgeInsets(top: 8, left: 12, bottom: 8, right: 12)
        stack.frame = NSRect(origin: .zero, size: stack.fittingSize)
        view = stack
        super.init()
        popup.target = self
        popup.action = #selector(formatChanged)
        formatChanged()
    }

    @objc private func formatChanged() {
        let selected = Self.formats[max(popup.indexOfSelectedItem, 0)]
        panel.allowedContentTypes = [selected.type]
        let base = (panel.nameFieldStringValue as NSString).deletingPathExtension
        panel.nameFieldStringValue = "\(base).\(selected.type.preferredFilenameExtension ?? selected.title.lowercased())"
    }
}

#if DEBUG
// MARK: - Snapshot seams

extension ActivityView {
    /// Opens with an entry selected, showing its details (`--export-snapshots`).
    static func snapshot(selecting id: ActivityEntry.ID) -> ActivityView {
        var view = ActivityView()
        view._selection = State(initialValue: id)
        return view
    }
}
#endif
