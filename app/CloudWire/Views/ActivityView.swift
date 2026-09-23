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
                TextField("Search", text: $search)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 200)
                    .onSubmit { appliedSearch = search }
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
                detail
                    .frame(minHeight: 120, idealHeight: 200)
            }
        }
        .task(id: filterKey) { await reload() }
        .onChange(of: search) { _, value in
            if value.isEmpty { appliedSearch = "" }
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
                Text(entry.message).lineLimit(1)
                    .onAppear {
                        if entry.id == entries.last?.id { Task { await loadMore() } }
                    }
            }
        }
        .overlay {
            if let error {
                InlineError(message: error).padding()
            } else if entries.isEmpty && !loading {
                Text("No entries.").foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private var detail: some View {
        if let entry = entries.first(where: { $0.id == selection }) {
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    HStack {
                        Text(entry.level.label).foregroundStyle(entry.level.color).font(.headline)
                        Text("\(entry.category.label) · \(Format.dateTime(ms: entry.ts))").foregroundStyle(.secondary)
                        Spacer()
                        Button("Copy") { copyToPasteboard(entry.message) }
                    }
                    Text(entry.message).textSelection(.enabled)
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
        } else {
            Text("Select an entry to see its details.")
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private var filter: CoreClient.ActivityFilter {
        CoreClient.ActivityFilter(levels: Array(levels), categories: Array(categories), search: appliedSearch,
                                  limit: Self.pageSize)
    }

    private func matches(_ entry: ActivityEntry) -> Bool {
        (levels.isEmpty || levels.contains(entry.level)) && (categories.isEmpty || categories.contains(entry.category))
            && (appliedSearch.isEmpty || entry.message.localizedCaseInsensitiveContains(appliedSearch))
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

/// Save panel → `activity.export` (CSV or JSON by file extension).
@MainActor
func exportActivityLog(_ model: AppModel) {
    let panel = NSSavePanel()
    panel.nameFieldStringValue = "CloudWire-Activity.csv"
    panel.allowedContentTypes = [.commaSeparatedText, .json]
    panel.message = String(localized: "Export the activity log as CSV or JSON (choose the file extension).")
    guard panel.runModal() == .OK, let url = panel.url else { return }
    let format: CoreClient.ExportFormat = url.pathExtension.lowercased() == "json" ? .json : .csv
    model.perform {
        let count = try await model.client.exportActivity(format: format, path: url.path)
        model.alert = AlertContent(title: String(localized: "Export complete"),
                                   message: String(localized: "\(count) entries saved to \(url.lastPathComponent)."))
    }
}
