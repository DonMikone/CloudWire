import CloudWireKit
import SwiftUI

struct OfflineView: View {
    @Environment(AppModel.self) private var model
    @State private var adding: OfflineDraft?
    @State private var editingExcludes: OfflineItem?
    @State private var history: OfflineItem?

    var body: some View {
        @Bindable var model = model
        SectionScaffold(title: String(localized: "Offline"),
                        subtitle: String(localized: "Folders and files kept as real local files, synced in the background.")) {
            HStack {
                Button {
                    model.syncNow()
                } label: {
                    Label("Sync All Now", systemImage: "arrow.triangle.2.circlepath")
                }
                .disabled(model.offlineItems.isEmpty)
                Button {
                    adding = OfflineDraft(connectionId: model.mountableConnections.first?.id ?? "", remotePath: "",
                                          kind: .folder, files: [])
                } label: {
                    Label("Add", systemImage: "plus")
                }
                .disabled(model.mountableConnections.isEmpty)
            }
        } content: {
            VStack(spacing: 0) {
                ForEach(model.offlineItems.filter { $0.state == .needsConfirmation }) { item in
                    MassDeleteBanner(item: item)
                        .padding(.horizontal, 20)
                        .padding(.bottom, 10)
                }
                if model.offlineItems.isEmpty {
                    ContentUnavailableView {
                        Label("No Offline Items", systemImage: SidebarSection.offline.symbol)
                    } description: {
                        Text("Make a cloud folder available offline to work with it at full SSD speed. Changes sync both ways in the background.")
                    } actions: {
                        Button("Add Offline Item") {
                            adding = OfflineDraft(connectionId: model.mountableConnections.first?.id ?? "",
                                                  remotePath: "", kind: .folder, files: [])
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(model.mountableConnections.isEmpty)
                    }
                } else {
                    List {
                        ForEach(model.offlineItems) { item in
                            OfflineRow(item: item, onExcludes: { editingExcludes = item }, onHistory: { history = item })
                        }
                    }
                    .listStyle(.inset)
                }
            }
        }
        .sheet(item: $adding) { draft in
            AddOfflineSheet(draft: draft)
        }
        .sheet(item: $editingExcludes) { item in
            ExcludesSheet(item: item)
        }
        .sheet(item: $history) { item in
            SyncHistorySheet(item: item)
        }
        .confirmationDialog(
            String(localized: "Remove “\(model.offlineRemoval?.displayName ?? "")” from offline?"),
            isPresented: Binding(get: { model.offlineRemoval != nil }, set: { if !$0 { model.offlineRemoval = nil } }),
            presenting: model.offlineRemoval
        ) { item in
            Button("Move Local Copy to Trash") { model.removeOffline(item, localCopy: .trash) }
                .keyboardShortcut(.defaultAction)
            Button("Keep Local Copy") { model.removeOffline(item, localCopy: .keep) }
            Button("Cancel", role: .cancel) {}
        } message: { item in
            Text("Syncing stops for \(CorePaths.abbreviate(item.storagePath)). The cloud is never touched.")
        }
        .onAppear { consumeDraft() }
        .onChange(of: model.offlineDraft) { _, _ in consumeDraft() }
    }

    /// Opens the add sheet for a draft handed over by Finder or a Vault.
    private func consumeDraft() {
        if let draft = model.offlineDraft {
            model.offlineDraft = nil
            adding = draft
        }
    }
}

private struct OfflineRow: View {
    @Environment(AppModel.self) private var model
    let item: OfflineItem
    let onExcludes: () -> Void
    let onHistory: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: item.kind == .files ? "doc.on.doc" : (item.isVault ? "lock.shield" : "folder"))
                .font(.title2)
                .foregroundStyle(Color.accentColor)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 3) {
                Text(item.displayName).font(.headline).lineLimit(1)
                Text(Format.remote(model.connectionName(item.connectionId), item.remotePath))
                    .font(.callout).foregroundStyle(.secondary).lineLimit(1)
                Text(CorePaths.abbreviate(item.storagePath))
                    .font(.caption).foregroundStyle(.tertiary).lineLimit(1).truncationMode(.middle)
                if item.state == .syncing, let progress = item.progress {
                    ProgressView(value: progress.fraction ?? 0) {
                        EmptyView()
                    } currentValueLabel: {
                        Text(progressText(progress)).font(.caption)
                    }
                    .frame(maxWidth: 360)
                }
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 4) {
                StatusLabel(text: stateText, color: item.state.color, symbol: item.state.symbol)
                if let last = item.lastSyncAt {
                    Text("Last synced \(Format.relative(ms: last))").font(.caption).foregroundStyle(.secondary)
                }
            }
            actionsMenu
        }
        .padding(.vertical, 6)
        .contextMenu { menuItems }
    }

    private var stateText: String {
        if (item.state == .paused || item.state == .error) && !item.reason.isEmpty {
            return item.state == .paused ? "\(item.state.label): \(PauseReason.label(item.reason))"
                : "\(item.state.label): \(item.reason)"
        }
        return item.state.label
    }

    private func progressText(_ progress: SyncProgress) -> String {
        var text = String(localized: "\(Format.bytes(progress.bytes)) of \(Format.bytes(progress.totalBytes))")
        if let eta = progress.eta, eta > 0 {
            text += " · " + String(localized: "\(Format.duration(seconds: eta)) left")
        }
        return text
    }

    private var actionsMenu: some View {
        Menu {
            menuItems
        } label: {
            Image(systemName: "ellipsis.circle")
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
    }

    @ViewBuilder
    private var menuItems: some View {
        Button("Sync Now") { model.syncNow(item) }
        Button("Show in Finder") { model.showInFinder(item.storagePath) }
        Button("Change Location…") { relocate() }
        Button("Excludes & Advanced…", action: onExcludes)
        Button("Sync History…", action: onHistory)
        Divider()
        Button("Remove Offline…", role: .destructive) { model.offlineRemoval = item }
    }

    private func relocate() {
        guard let path = Panels.chooseFolder(
            message: String(localized: "Choose the new storage location. The local files are moved there, nothing is downloaded again."),
            startingAt: (item.storagePath as NSString).deletingLastPathComponent)
        else { return }
        model.perform {
            model.updated(try await model.client.relocateOfflineItem(id: item.id, newPath: path))
        }
    }
}

/// Mass-Delete Guard banner with both choices.
struct MassDeleteBanner: View {
    @Environment(AppModel.self) private var model
    let item: OfflineItem

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "exclamationmark.triangle.fill").font(.title2).foregroundStyle(.orange)
            VStack(alignment: .leading, spacing: 6) {
                Text("Sync stopped: “\(item.displayName)”").font(.headline)
                Text("More than half of the files would be deleted. Confirm the deletion only if you removed them on purpose.")
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button("Confirm Deletion", role: .destructive) { model.confirmMassDelete(item, action: .delete) }
                    Button("Don't Delete – Restore Files") { model.confirmMassDelete(item, action: .restore) }
                        .buttonStyle(.borderedProminent)
                }
            }
            Spacer()
        }
        .padding(14)
        .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 12))
    }
}

// MARK: - Add

struct AddOfflineSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let draft: OfflineDraft

    @State private var connectionId = ""
    @State private var path = ""
    @State private var kind: OfflineKind = .folder
    @State private var selectedFiles: Set<String> = []
    @State private var storagePath: String?
    @State private var preflight: PreflightResult?
    @State private var checking = false
    @State private var creating = false
    @State private var error: String?
    @State private var askMerge = false
    @State private var prepared = false

    var body: some View {
        SheetScaffold(title: String(localized: "Make Available Offline"), width: 640) {
            Form {
                Picker("Connection", selection: $connectionId) {
                    ForEach(model.mountableConnections) { connection in
                        Text(model.connectionName(connection.id)).tag(connection.id)
                    }
                }
                Picker("Make offline", selection: $kind) {
                    Text("This folder").tag(OfflineKind.folder)
                    Text("Selected files").tag(OfflineKind.files)
                }
                .pickerStyle(.segmented)
            }
            .formStyle(.grouped)
            .frame(height: 110)

            if !connectionId.isEmpty {
                RemoteBrowser(connectionId: connectionId, path: $path, selectsFiles: kind == .files,
                              selectedFiles: $selectedFiles)
                    .frame(height: 240)
            }

            GroupBox {
                VStack(alignment: .leading, spacing: 6) {
                    LabeledContent("Storage location") {
                        HStack {
                            Text(CorePaths.abbreviate(preflight?.storagePath ?? storagePath ?? "…"))
                                .lineLimit(1).truncationMode(.middle)
                                .foregroundStyle(.secondary)
                            Button("Change…") { chooseLocation() }
                        }
                    }
                    if checking {
                        ProgressView().controlSize(.small)
                    } else if let preflight {
                        Text("Cloud size \(Format.bytes(preflight.remoteBytes)) · free on disk \(Format.bytes(preflight.freeBytes))")
                            .font(.caption).foregroundStyle(.secondary)
                        if !preflight.hasEnoughSpace {
                            InlineError(message: String(localized: "Not enough free space: at least 1.1 × the cloud size is required."))
                        }
                    }
                }
                .padding(4)
            }
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Make Available Offline") { create(merge: false) }
                .keyboardShortcut(.defaultAction)
                .disabled(!canCreate)
        }
        .task {
            guard !prepared else { return }
            prepared = true
            connectionId = draft.connectionId.isEmpty ? (model.mountableConnections.first?.id ?? "") : draft.connectionId
            path = draft.remotePath
            kind = draft.kind
            selectedFiles = Set(draft.files)
        }
        .task(id: preflightKey) { await runPreflight() }
        .confirmationDialog(String(localized: "The chosen folder is not empty"), isPresented: $askMerge) {
            Button("Merge (Newer Version Wins)") { create(merge: true) }
            Button("Choose Another Folder") { chooseLocation() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("CloudWire can merge the existing files with the cloud; for files present on both sides the newer version wins.")
        }
    }

    private var preflightKey: String {
        "\(connectionId)|\(path)|\(kind.rawValue)|\(selectedFiles.sorted().joined(separator: "/"))|\(storagePath ?? "")"
    }

    private var canCreate: Bool {
        guard !connectionId.isEmpty, !creating, !checking else { return false }
        if kind == .files && selectedFiles.isEmpty { return false }
        if let preflight, !preflight.hasEnoughSpace { return false }
        return preflight != nil
    }

    private func runPreflight() async {
        guard prepared, !connectionId.isEmpty, kind == .folder || !selectedFiles.isEmpty else {
            preflight = nil
            return
        }
        try? await Task.sleep(for: .milliseconds(250))
        guard !Task.isCancelled else { return }
        checking = true
        error = nil
        defer { checking = false }
        do {
            let result = try await model.client.offlinePreflight(
                connectionId: connectionId, kind: kind, remotePath: path,
                files: kind == .files ? selectedFiles.sorted() : nil, storagePath: storagePath)
            guard !Task.isCancelled else { return }
            preflight = result
        } catch is CancellationError {
        } catch {
            preflight = nil
            let alert = ErrorText.alert(for: error)
            self.error = "\(alert.title): \(alert.message)"
        }
    }

    private func chooseLocation() {
        if let chosen = Panels.chooseFolder(
            message: String(localized: "Choose where the offline copy is stored, for example on an external SSD."),
            startingAt: preflight?.storagePath ?? model.settings.baseFolder)
        {
            storagePath = chosen
        }
    }

    private func create(merge: Bool) {
        if !merge, preflight?.storageNonEmpty == true {
            askMerge = true
            return
        }
        creating = true
        error = nil
        let files = kind == .files ? selectedFiles.sorted() : nil
        let (connectionId, path, kind, storagePath) = (connectionId, path, kind, storagePath)
        Task {
            do {
                let item = try await model.client.createOfflineItem(
                    connectionId: connectionId, kind: kind, remotePath: path, files: files,
                    storagePath: storagePath, excludes: nil, mergeExisting: merge)
                model.updated(item)
                dismiss()
            } catch let failure as CoreError where failure.code == "offline.storageNotEmpty" {
                creating = false
                askMerge = true
            } catch {
                creating = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

// MARK: - Excludes & advanced

struct ExcludesSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let item: OfflineItem

    @State private var excludes = ""
    @State private var form = OptionFormModel(options: [], hideContext: .commandLine)
    @State private var filter = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "Excludes & Advanced"), width: 640) {
            VStack(alignment: .leading, spacing: 6) {
                Text("Excluded files and folders (one pattern per line)").font(.headline)
                TextEditor(text: $excludes)
                    .font(.body.monospaced())
                    .frame(height: 120)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(.quaternary))
                Button("Restore Defaults") { excludes = model.settings.defaultExcludes.joined(separator: "\n") }
                    .buttonStyle(.link)
            }
            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text("Advanced rclone options").font(.headline)
                    Spacer()
                    TextField("Filter", text: $filter).textFieldStyle(.roundedBorder).frame(width: 200)
                }
                Form {
                    OptionFormView(form: $form, options: visibleOptions, showsNames: true)
                }
                .formStyle(.grouped)
                .frame(height: 260)
            }
            Text("Changing excludes or options makes the next sync compare both sides completely.")
                .font(.caption).foregroundStyle(.secondary)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Save") { save() }.keyboardShortcut(.defaultAction).disabled(busy)
        }
        .task {
            excludes = item.excludes.joined(separator: "\n")
            if let info = try? await model.loadMainOptions() {
                form = OptionFormModel(options: info.main, hideContext: .commandLine,
                                       initialValues: OptionFormModel.textValues(fromTyped: item.advanced, options: info.main))
            }
        }
    }

    private var visibleOptions: [RcloneOption] {
        let all = form.visibleOptions()
        guard !filter.isEmpty else { return all }
        return all.filter { $0.name.localizedCaseInsensitiveContains(filter) || $0.help.localizedCaseInsensitiveContains(filter) }
    }

    private func save() {
        let advanced: [String: JSONValue]
        do {
            advanced = try form.typedValues(keyedBy: .fieldName)
        } catch {
            self.error = ErrorText.alert(for: error).message
            return
        }
        let patterns = excludes.split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        busy = true
        Task {
            do {
                model.updated(try await model.client.updateOfflineItem(
                    id: item.id, excludes: patterns == item.excludes ? nil : patterns,
                    advanced: advanced == item.advanced ? nil : advanced))
                dismiss()
            } catch {
                busy = false
                self.error = ErrorText.alert(for: error).message
            }
        }
    }
}

// MARK: - History

struct SyncHistorySheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let item: OfflineItem

    @State private var runs: [SyncRun] = []
    @State private var selected: SyncRun.ID?

    var body: some View {
        SheetScaffold(title: String(localized: "Sync History: \(item.displayName)"), width: 720) {
            HSplitView {
                List(runs, selection: $selected) { run in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(Format.dateTime(ms: run.startedAt))
                        Text(runSummary(run)).font(.caption).foregroundStyle(run.status == "ok" ? Color.secondary : Color.red)
                    }
                    .tag(run.id)
                }
                .frame(minWidth: 240)
                Group {
                    if let run = runs.first(where: { $0.id == selected }) {
                        RunFilesView(runId: run.id, error: run.error)
                    } else {
                        Text("Select a sync run to see its files.").foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                }
                .frame(minWidth: 320)
            }
            .frame(height: 380)
        } buttons: {
            Button("Close") { dismiss() }.keyboardShortcut(.defaultAction)
        }
        .task {
            runs = (try? await model.client.syncRuns(itemId: item.id, limit: 100)) ?? []
            selected = runs.first?.id
        }
    }

    private func runSummary(_ run: SyncRun) -> String {
        String(localized: "\(run.status): \(run.transferred) transferred, \(run.deleted) deleted, \(run.conflicts) conflicts")
    }
}

/// Files of one sync run.
struct RunFilesView: View {
    @Environment(AppModel.self) private var model
    let runId: Int64
    var error: String = ""
    @State private var files: [SyncRunFile] = []

    var body: some View {
        VStack(alignment: .leading) {
            if !error.isEmpty { InlineError(message: error) }
            List(files) { file in
                HStack {
                    Image(systemName: symbol(file.action)).foregroundStyle(color(file.action))
                    Text(file.path).textSelection(.enabled)
                }
            }
            .overlay {
                if files.isEmpty { Text("No files changed.").foregroundStyle(.secondary) }
            }
        }
        .task(id: runId) {
            files = (try? await model.client.syncRunFiles(runId: runId)) ?? []
        }
    }

    private func symbol(_ action: String) -> String {
        switch action {
        case "transferred": return "arrow.up.arrow.down"
        case "deleted": return "trash"
        case "conflict": return "exclamationmark.triangle.fill"
        default: return "doc"
        }
    }

    private func color(_ action: String) -> Color {
        switch action {
        case "deleted": return .red
        case "conflict": return .orange
        default: return .secondary
        }
    }
}
