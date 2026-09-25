import CloudWireKit
import SwiftUI

struct OfflineView: View {
    @Environment(AppModel.self) private var model
    @State private var adding: OfflineDraft?
    @State private var editingExcludes: OfflineItem?
    @State private var editingSelection: OfflineItem?
    @State private var history: OfflineItem?

    var body: some View {
        @Bindable var model = model
        SectionScaffold(title: String(localized: "Offline"),
                        subtitle: String(localized: "Cloud folders and files as real local copies, synced in the background.")) {
            HStack {
                Button {
                    model.syncNow()
                } label: {
                    Label("Sync All Now", systemImage: "arrow.triangle.2.circlepath")
                }
                .disabled(model.offlineItems.isEmpty)
                Button {
                    adding = OfflineDraft(connectionId: model.mountableConnections.first?.id ?? "", paths: [])
                } label: {
                    Label("Add", systemImage: "plus")
                }
                .disabled(model.mountableConnections.isEmpty)
            }
        } content: {
            VStack(spacing: 0) {
                ForEach(model.offlineItems.filter { $0.state == .needsConfirmation }) { item in
                    MassDeleteBanner(item: item, onHistory: { history = item })
                        .padding(.horizontal, 20)
                        .padding(.bottom, 10)
                }
                if model.offlineItems.isEmpty {
                    ContentUnavailableView {
                        Label("No Offline Items", systemImage: SidebarSection.offline.symbol)
                    } description: {
                        if model.mountableConnections.isEmpty {
                            Text("Add a Connection first.")
                        } else {
                            Text("Make a cloud folder available offline to work with it at full SSD speed. Changes sync both ways in the background.")
                        }
                    } actions: {
                        if model.mountableConnections.isEmpty {
                            Button("Add Connection…") { model.showAddConnection = true }
                                .buttonStyle(.borderedProminent)
                        } else {
                            Button("Add Offline Item") {
                                adding = OfflineDraft(connectionId: model.mountableConnections.first?.id ?? "", paths: [])
                            }
                            .buttonStyle(.borderedProminent)
                        }
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List {
                        ForEach(model.offlineItems) { item in
                            OfflineRow(item: item, onSelection: { editingSelection = item },
                                       onExcludes: { editingExcludes = item }, onHistory: { history = item })
                        }
                    }
                    .listStyle(.inset)
                }
            }
        }
        .sheet(item: $adding, onDismiss: consumeDraft) { draft in
            AddOfflineSheet(draft: draft)
        }
        .sheet(item: $editingSelection, onDismiss: consumeDraft) { item in
            EditOfflineSelectionSheet(item: item)
        }
        .sheet(item: $editingExcludes, onDismiss: consumeDraft) { item in
            ExcludesSheet(item: item)
        }
        .sheet(item: $history, onDismiss: consumeDraft) { item in
            SyncHistorySheet(item: item)
        }
        .confirmationDialog(
            String(localized: "Remove “\(model.offlineRemoval?.displayName ?? "")” from offline?"),
            isPresented: Binding(get: { model.offlineRemoval != nil }, set: { if !$0 { model.offlineRemoval = nil } }),
            presenting: model.offlineRemoval
        ) { item in
            Button("Move Local Copy to Trash") { model.removeOffline(item, localCopy: .trash) }
            Button("Keep Local Copy") { model.removeOffline(item, localCopy: .keep) }
                .keyboardShortcut(.defaultAction)
            Button("Cancel", role: .cancel) {}
        } message: { item in
            Text(removalMessage(item))
        }
        .onAppear { consumeDraft() }
        .onChange(of: model.offlineDraft) { _, _ in consumeDraft() }
    }

    private func removalMessage(_ item: OfflineItem) -> String {
        let message = String(localized: "Syncing stops for \(CorePaths.abbreviate(item.storagePath)). The cloud is never touched.")
        guard item.state != .idle else { return message }
        return message + " " + String(localized: "Changes that are not synced yet are no longer uploaded.")
    }

    /// Opens the add sheet for a draft handed over by Finder or a Vault. While another sheet is open the
    /// draft waits in the model and is taken when that sheet closes.
    private func consumeDraft() {
        guard adding == nil, editingSelection == nil, editingExcludes == nil, history == nil,
              let draft = model.offlineDraft else { return }
        model.offlineDraft = nil
        adding = draft
    }
}

private struct OfflineRow: View {
    @Environment(AppModel.self) private var model
    let item: OfflineItem
    let onSelection: () -> Void
    let onExcludes: () -> Void
    let onHistory: () -> Void
    @State private var relocating = false

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: item.kind == .files ? "checklist" : (item.isVault ? "lock.shield" : "folder"))
                .font(.title2)
                .foregroundStyle(Color.accentColor)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 3) {
                Text(item.displayName).font(.headline).lineLimit(1)
                Text(Format.remote(model.connectionName(item.connectionId), item.remotePath))
                    .font(.callout).foregroundStyle(.secondary).lineLimit(1)
                Text(CorePaths.abbreviate(item.storagePath))
                    .font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                if relocating {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text("Moving…").font(.caption)
                    }
                } else if item.state == .syncing, let progress = item.progress {
                    progressView(progress)
                }
                if item.state == .error && !item.reason.isEmpty {
                    InlineError(item.errorText)
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
        .contextMenu { if !relocating { menuItems } }
    }

    private var stateText: String {
        if item.state == .paused && !item.reason.isEmpty {
            return "\(item.state.label): \(PauseReason.label(item.reason))"
        }
        return item.state.label
    }

    /// Without a known total the bar is indeterminate and only the transferred bytes are shown.
    private func progressView(_ progress: SyncProgress) -> some View {
        ProgressView(value: progress.fraction) {
            EmptyView()
        } currentValueLabel: {
            Text(progressText(progress)).font(.caption)
        }
        .frame(maxWidth: 360)
    }

    private func progressText(_ progress: SyncProgress) -> String {
        guard progress.fraction != nil else {
            return String(localized: "\(Format.bytes(progress.bytes)) transferred")
        }
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
        .disabled(relocating)
        .help("More Actions")
        .accessibilityLabel(Text("Actions for “\(item.displayName)”"))
    }

    @ViewBuilder
    private var menuItems: some View {
        Button("Sync Now") { model.syncNow(item) }
        Button("Show in Finder") { model.showInFinder(item.storagePath) }
        Button("Change Selection…", action: onSelection)
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
        relocating = true
        model.perform {
            defer { relocating = false }
            model.updated(try await model.client.relocateOfflineItem(id: item.id, newPath: path))
        }
    }
}

/// Mass-Delete Guard banner: what happened on which side, both choices and the way to the history.
struct MassDeleteBanner: View {
    @Environment(AppModel.self) private var model
    let item: OfflineItem
    let onHistory: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "exclamationmark.triangle.fill").font(.title2).foregroundStyle(.orange)
            VStack(alignment: .leading, spacing: 6) {
                Text("Sync stopped: “\(item.displayName)”").font(.headline)
                Text(explanation + " " + String(localized: "Confirm the deletion only if it is intended."))
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button("Confirm Deletion", role: .destructive) { model.confirmMassDelete(item, action: .delete) }
                    Button("Don't Delete – Restore Files") { model.confirmMassDelete(item, action: .restore) }
                        .buttonStyle(.borderedProminent)
                    Button("Show Sync History…", action: onHistory)
                        .buttonStyle(.link)
                }
            }
            Spacer()
        }
        .padding(14)
        .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 12))
    }

    /// Neutral wording: the deletions may come from someone else, e.g. in a shared cloud folder.
    private var explanation: String {
        guard let info = item.massDelete else {
            return String(localized: "More than half of the files would be deleted.")
        }
        switch (info.reason, info.side) {
        case ("tooManyDeletes", "cloud"):
            return String(localized: "\(info.deletes) of \(info.total) files were deleted in the cloud. Syncing would delete them in the local copy as well.")
        case ("tooManyDeletes", _):
            return String(localized: "\(info.deletes) of \(info.total) files were deleted in the local copy. Syncing would delete them in the cloud as well.")
        case (_, "cloud"):
            return String(localized: "All files were changed in the cloud.")
        default:
            return String(localized: "All files were changed in the local copy.")
        }
    }
}

// MARK: - Add

struct AddOfflineSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let draft: OfflineDraft

    @State private var connectionId = ""
    @State private var selection = OfflineTreeSelection()
    /// The folder chosen via "Change…"; it becomes the Storage Location itself.
    @State private var storageChoice: String?
    @State private var preflight: PreflightResult?
    @State private var checking = false
    @State private var creating = false
    @State private var error: String?
    @State private var askMerge = false
    @State private var prepared = false

    var body: some View {
        SheetScaffold(title: String(localized: "Make Available Offline"), width: 640) {
            Picker("Connection", selection: $connectionId) {
                ForEach(model.mountableConnections) { connection in
                    Text(model.connectionName(connection.id)).tag(connection.id)
                }
            }
            .fixedSize()

            VStack(alignment: .leading, spacing: 8) {
                Text("① What should be available offline?").font(.headline)
                if !connectionId.isEmpty {
                    OfflineTree(connectionId: connectionId, rootName: model.connectionName(connectionId),
                                selection: $selection, locked: lockedPaths)
                        .frame(height: 300)
                }
                SelectionSummary(selection: selection)
            }

            VStack(alignment: .leading, spacing: 8) {
                Text("② Where should the offline copy be stored?").font(.headline)
                GroupBox {
                    VStack(alignment: .leading, spacing: 6) {
                        LabeledContent("Storage location") {
                            HStack {
                                Text(storageLocationText)
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
                                InlineError(message: String(localized: "Not enough free space: about \(Format.bytes(Int64(Double(preflight.remoteBytes) * 1.1))) needed, \(Format.bytes(preflight.freeBytes)) free. Use “Change…” to choose a storage location on another volume."))
                            }
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(4)
                }
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
            selection = OfflineTreeSelection(root: "", paths: draft.paths)
        }
        .onChange(of: connectionId) { old, _ in
            // Paths belong to the previous Connection: start over with nothing selected.
            if !old.isEmpty { selection = OfflineTreeSelection() }
        }
        .task(id: PreflightKey(connectionId: connectionId, target: selection.target, storagePath: storagePath)) {
            await runPreflight()
        }
        .confirmationDialog(
            String(localized: "“\(CorePaths.abbreviate(storagePath ?? preflight?.storagePath ?? ""))” already exists and is not empty"),
            isPresented: $askMerge
        ) {
            Button("Merge (Newer Version Wins)") { create(merge: true) }
            Button("Choose Another Folder") { chooseLocation() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Merging syncs everything already in this folder with the cloud: files only here are uploaded, and for files present on both sides the newer version wins.")
        }
    }

    private struct PreflightKey: Equatable {
        let connectionId: String
        let target: OfflineTreeSelection.Target?
        let storagePath: String?
    }

    /// Paths of the Connection's existing Offline Items; they cannot be selected again.
    private var lockedPaths: [String] {
        model.offlineItems.filter { $0.connectionId == connectionId }.flatMap(\.coveredPaths)
    }

    /// The chosen Storage Location; nil leaves the default (base folder/Connection) to the Core.
    private var storagePath: String? { storageChoice }

    private var storageLocationText: String {
        if let path = storageChoice ?? preflight?.storagePath {
            return CorePaths.abbreviate(path)
        }
        return String(localized: "Set after the selection (in \(CorePaths.abbreviate(model.settings.baseFolder)))")
    }

    private var canCreate: Bool {
        guard !connectionId.isEmpty, selection.target != nil, !creating, !checking else { return false }
        if let preflight, !preflight.hasEnoughSpace { return false }
        return preflight != nil
    }

    private func runPreflight() async {
        guard prepared, !connectionId.isEmpty, let target = selection.target else {
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
                connectionId: connectionId, kind: target.kind, remotePath: target.remotePath,
                files: target.files, storagePath: storagePath)
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
        let message = String(localized: "Choose the folder for the offline copy, for example on an external SSD. The selected items appear inside it with their cloud path.")
        let start = storageChoice ?? preflight?.storagePath ?? model.settings.baseFolder
        if let chosen = Panels.chooseFolder(message: message, startingAt: start) {
            storageChoice = chosen
        }
    }

    private func create(merge: Bool) {
        guard let target = selection.target else { return }
        if !merge, preflight?.storageNonEmpty == true {
            askMerge = true
            return
        }
        creating = true
        error = nil
        let (connectionId, storagePath) = (connectionId, storagePath)
        Task {
            do {
                let item = try await model.client.createOfflineItem(
                    connectionId: connectionId, kind: target.kind, remotePath: target.remotePath, files: target.files,
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

/// One line below the tree that names what is checked.
private struct SelectionSummary: View {
    let selection: OfflineTreeSelection

    var body: some View {
        if selection.isEmpty {
            Text("Select folders or files.").font(.callout).foregroundStyle(.secondary)
        } else {
            Label(text, systemImage: "checklist")
                .font(.callout)
                .lineLimit(1)
                .truncationMode(.middle)
        }
    }

    private var text: String {
        if selection.paths == [""] {
            return String(localized: "Selected: entire Connection")
        }
        if selection.paths.count == 1, let path = selection.paths.first {
            return String(localized: "Selected: “\((path as NSString).lastPathComponent)”")
        }
        return String(localized: "Selected: \(selection.paths.count) items")
    }
}

// MARK: - Change Selection

struct EditOfflineSelectionSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let item: OfflineItem

    @State private var selection: OfflineTreeSelection
    @State private var preflight: PreflightResult?
    @State private var checking = false
    @State private var saving = false
    @State private var error: String?
    @State private var askLocalCopy = false

    init(item: OfflineItem) {
        self.item = item
        _selection = State(initialValue: OfflineTreeSelection(item: item))
    }

    var body: some View {
        SheetScaffold(title: String(localized: "Change Selection"), width: 640) {
            VStack(alignment: .leading, spacing: 8) {
                OfflineTree(connectionId: item.connectionId, rootName: rootName, selection: $selection,
                            locked: lockedPaths)
                    .frame(height: 300)
                SelectionSummary(selection: selection)
            }
            GroupBox {
                VStack(alignment: .leading, spacing: 6) {
                    LabeledContent("Storage location") {
                        Text(CorePaths.abbreviate(item.storagePath))
                            .lineLimit(1).truncationMode(.middle)
                            .foregroundStyle(.secondary)
                    }
                    if checking {
                        ProgressView().controlSize(.small)
                    } else if let preflight {
                        Text("Additional cloud size \(Format.bytes(preflight.remoteBytes)) · free on disk \(Format.bytes(preflight.freeBytes))")
                            .font(.caption).foregroundStyle(.secondary)
                        if !preflight.hasEnoughSpace {
                            InlineError(message: String(localized: "Not enough free space: \(Format.bytes(Int64(Double(preflight.remoteBytes) * 1.1))) needed, \(Format.bytes(preflight.freeBytes)) available"))
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(4)
            }
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Save") { save() }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSave)
        }
        .task(id: added.target) { await runPreflight() }
        .confirmationDialog("Remove the local copy of deselected items?", isPresented: $askLocalCopy) {
            Button("Move Local Copy to Trash") { apply(localCopy: .trash) }
                .keyboardShortcut(.defaultAction)
            Button("Keep Local Copy") { apply(localCopy: .keep) }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("No longer synced: \(goneNames). The cloud is never touched.")
        }
    }

    /// The Selection when the sheet opened.
    private var original: OfflineTreeSelection { OfflineTreeSelection(item: item) }

    /// Newly checked paths; only they are downloaded.
    private var added: OfflineTreeSelection {
        OfflineTreeSelection(root: item.remotePath, paths: selection.uncovered(by: original))
    }

    private var rootName: String {
        item.remotePath.isEmpty
            ? model.connectionName(item.connectionId) : (item.remotePath as NSString).lastPathComponent
    }

    /// Paths of the Connection's other Offline Items.
    private var lockedPaths: [String] {
        model.offlineItems.filter { $0.connectionId == item.connectionId && $0.id != item.id }.flatMap(\.coveredPaths)
    }

    private var goneNames: String {
        original.uncovered(by: selection).map {
            $0.isEmpty ? model.connectionName(item.connectionId) : ($0 as NSString).lastPathComponent
        }
        .joined(separator: ", ")
    }

    private var canSave: Bool {
        guard !selection.isEmpty, selection != original, !saving, !checking else { return false }
        if added.isEmpty { return true }
        return preflight?.hasEnoughSpace == true
    }

    private func runPreflight() async {
        guard let target = added.target else {
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
                connectionId: item.connectionId, kind: target.kind, remotePath: target.remotePath,
                files: target.files, storagePath: item.storagePath)
            guard !Task.isCancelled else { return }
            preflight = result
        } catch is CancellationError {
        } catch {
            preflight = nil
            let alert = ErrorText.alert(for: error)
            self.error = "\(alert.title): \(alert.message)"
        }
    }

    private func save() {
        if original.uncovered(by: selection).isEmpty {
            apply(localCopy: nil)
        } else {
            askLocalCopy = true
        }
    }

    private func apply(localCopy: CoreClient.LocalCopyAction?) {
        guard let target = selection.target else { return }
        saving = true
        error = nil
        Task {
            do {
                let updated = try await model.client.setOfflineSelection(
                    id: item.id, kind: target.kind, files: target.files, localCopy: localCopy)
                model.updated(updated)
                dismiss()
            } catch {
                saving = false
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
    /// nil while the option list loads; `.failure` keeps the item's advanced options untouched on save.
    @State private var options: Result<Void, any Error>?

    var body: some View {
        SheetScaffold(title: String(localized: "Excludes & Advanced: \(item.displayName)"), width: 640) {
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
                Group {
                    switch options {
                    case nil:
                        ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                    case .failure(let failure)?:
                        InlineError(message: String(localized: "The rclone options could not be loaded and stay unchanged: \(ErrorText.alert(for: failure).message)"))
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    case .success?:
                        Form {
                            OptionFormView(form: $form, options: visibleOptions, showsNames: true)
                        }
                        .formStyle(.grouped)
                    }
                }
                .frame(height: 260)
            }
            Text("Changing excludes or options makes the next sync compare both sides completely.")
                .font(.caption).foregroundStyle(.secondary)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Save") { save() }.keyboardShortcut(.defaultAction).disabled(busy || options == nil)
        }
        .task {
            excludes = item.excludes.joined(separator: "\n")
            do {
                let info = try await model.loadMainOptions()
                form = OptionFormModel(options: info.main, hideContext: .commandLine,
                                       initialValues: OptionFormModel.textValues(fromTyped: item.advanced, options: info.main))
                options = .success(())
            } catch is CancellationError {
            } catch {
                options = .failure(error)
            }
        }
    }

    private var visibleOptions: [RcloneOption] {
        let all = form.visibleOptions().map(Self.localized)
        guard !filter.isEmpty else { return all }
        return all.filter { $0.name.localizedCaseInsensitiveContains(filter) || $0.help.localizedCaseInsensitiveContains(filter) }
    }

    /// rclone's help is English; the options people actually change get CloudWire's own text
    /// (first line = label, rest = details), everything else keeps rclone's text.
    private static func localized(_ option: RcloneOption) -> RcloneOption {
        let help: String
        switch option.name {
        case "transfers":
            help = String(localized: "Parallel transfers\nHow many files are copied at the same time.")
        case "checkers":
            help = String(localized: "Parallel checks\nHow many files are compared at the same time.")
        case "retries":
            help = String(localized: "Retries of a failed sync\nHow often a whole sync is tried again after an error.")
        case "low_level_retries":
            help = String(localized: "Retries of single requests\nHow often a single failed request to the cloud is repeated.")
        case "bwlimit":
            help = String(localized: "Bandwidth limit\nMaximum transfer speed, e.g. 10M for 10 MiB/s, or off.")
        case "buffer_size":
            help = String(localized: "Buffer per transfer\nMemory used to read ahead for each file being transferred.")
        case "timeout":
            help = String(localized: "Idle timeout\nA transfer without any data for this long is aborted.")
        case "contimeout":
            help = String(localized: "Connection timeout\nHow long to wait while connecting to the server.")
        case "tpslimit":
            help = String(localized: "Requests per second\nLimits requests to the cloud per second; 0 means no limit.")
        case "multi_thread_streams":
            help = String(localized: "Streams per large file\nHow many parts of one large file are transferred at the same time.")
        case "max_size":
            help = String(localized: "Maximum file size\nLarger files are skipped, e.g. 2G, or off.")
        case "min_size":
            help = String(localized: "Minimum file size\nSmaller files are skipped, e.g. 1k, or off.")
        default:
            return option
        }
        var copy = option
        copy.help = help
        return copy
    }

    private func save() {
        // Without the option list the form is empty: sending it would erase the item's options.
        var advanced: [String: JSONValue]?
        if case .success = options {
            do {
                advanced = try form.typedValues(keyedBy: .fieldName)
            } catch {
                self.error = ErrorText.alert(for: error).message
                return
            }
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

    /// nil while loading.
    @State private var runs: [SyncRun]?
    @State private var loadError: String?
    @State private var selected: SyncRun.ID?

    var body: some View {
        SheetScaffold(title: String(localized: "Sync History: \(item.displayName)"), width: 720) {
            Group {
                if let loadError {
                    InlineError(message: loadError)
                        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                } else if let runs {
                    if runs.isEmpty {
                        Text("No sync runs yet.").foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                    } else {
                        runList(runs)
                    }
                } else {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            .frame(height: 380)
        } buttons: {
            Button("Close") { dismiss() }.keyboardShortcut(.defaultAction)
        }
        .task {
            do {
                let loaded = try await model.client.syncRuns(itemId: item.id, limit: 100)
                runs = loaded
                selected = loaded.first?.id
            } catch is CancellationError {
            } catch {
                let alert = ErrorText.alert(for: error)
                loadError = "\(alert.title): \(alert.message)"
            }
        }
    }

    private func runList(_ runs: [SyncRun]) -> some View {
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
    }

    private func runSummary(_ run: SyncRun) -> String {
        [statusText(run.status),
         String(localized: "\(run.transferred) transferred"),
         String(localized: "\(run.deleted) deleted"),
         String(localized: "\(run.conflicts) conflicts")].joined(separator: " · ")
    }

    private func statusText(_ status: String) -> String {
        switch status {
        case "ok": return String(localized: "Successful")
        case "error": return String(localized: "Failed")
        case "massDelete": return String(localized: "Stopped by the Mass-Delete Guard")
        case "stopped": return String(localized: "Stopped")
        case "": return String(localized: "Running")
        default: return status
        }
    }
}

/// Files of one sync run.
struct RunFilesView: View {
    @Environment(AppModel.self) private var model
    let runId: Int64
    var error: String = ""
    /// nil while loading.
    @State private var files: [SyncRunFile]?
    @State private var loadError: String?

    var body: some View {
        VStack(alignment: .leading) {
            if !error.isEmpty {
                InlineError(message: String(localized: "This sync run did not complete."), detail: error)
            }
            if let loadError { InlineError(message: loadError) }
            List(files ?? []) { file in
                HStack {
                    Image(systemName: symbol(file.action))
                        .foregroundStyle(color(file.action))
                        .frame(width: 18)
                        .help(actionLabel(file.action))
                        .accessibilityLabel(actionLabel(file.action))
                    Text(file.path).textSelection(.enabled)
                }
            }
            .overlay {
                if files == nil && loadError == nil {
                    ProgressView().controlSize(.small)
                } else if files?.isEmpty == true {
                    Text("No files changed.").foregroundStyle(.secondary)
                }
            }
        }
        .task(id: runId) {
            // The previous run's files must not stay visible while this one loads.
            files = nil
            loadError = nil
            do {
                files = try await model.client.syncRunFiles(runId: runId)
            } catch is CancellationError {
            } catch {
                let alert = ErrorText.alert(for: error)
                loadError = "\(alert.title): \(alert.message)"
            }
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

    private func actionLabel(_ action: String) -> String {
        switch action {
        case "transferred": return String(localized: "Transferred")
        case "deleted": return String(localized: "Deleted")
        case "conflict": return String(localized: "Conflict")
        default: return action
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
