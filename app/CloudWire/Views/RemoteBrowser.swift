import CloudWireKit
import SwiftUI

/// Browses a Connection: folders navigate.
/// The `selection` mode picks one folder (radio) or files of one folder (checkboxes, or a radio when
/// only one file is allowed); Vault folders hold only encrypted data and show `vaultHint` instead.
/// The `selectedVault` mode picks a Vault folder (`.cwvault`) instead of opening it.
struct RemoteBrowser: View {
    @Environment(AppModel.self) private var model
    let connectionId: String
    /// The current folder (relative to the Connection root, "" = root).
    @Binding var path: String
    /// Picker mode; `path` then mirrors `selection.path`.
    private var selection: Binding<OfflineSelection>?
    private var vaultHint: LocalizedStringKey = ""
    /// Vault picker mode: `.cwvault` rows select the Vault instead of navigating into it.
    private var selectedVault: Binding<String?>?

    @State private var entries: [BrowseEntry] = []
    @State private var loading = false
    @State private var error: String?

    init(connectionId: String, path: Binding<String>) {
        self.connectionId = connectionId
        self._path = path
    }

    init(connectionId: String, selection: Binding<OfflineSelection>,
         vaultHint: LocalizedStringKey = "Vault – unlock it and make it available offline under Vaults")
    {
        self.init(connectionId: connectionId, path: Binding(
            get: { selection.wrappedValue.path },
            set: { selection.wrappedValue.navigate(to: $0) }))
        self.selection = selection
        self.vaultHint = vaultHint
    }

    init(connectionId: String, path: Binding<String>, selectedVault: Binding<String?>) {
        self.init(connectionId: connectionId, path: path)
        self.selectedVault = selectedVault
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let selection {
                HStack {
                    breadcrumbs
                    if selection.wrappedValue.isFolderSelected(path) {
                        // The open folder is the selected one (it stays selected while browsing inside it).
                        Button("✓ Selected") {}.disabled(true)
                    } else if selection.wrappedValue.canSelectFolder(path) {
                        Button("Select This Folder") { selection.wrappedValue.selectFolder(path) }
                    }
                }
            } else {
                breadcrumbs
            }
            ZStack {
                List {
                    ForEach(entries) { entry in
                        row(entry)
                    }
                }
                .listStyle(.bordered(alternatesRowBackgrounds: true))
                if loading {
                    ProgressView().controlSize(.small)
                } else if let error {
                    InlineError(message: error).padding()
                } else if entries.isEmpty {
                    Text("This folder is empty.").foregroundStyle(.secondary)
                }
            }
            .frame(minHeight: 120)
        }
        .task(id: "\(connectionId)|\(path)") { await load() }
    }

    private var breadcrumbs: some View {
        let parts = path.split(separator: "/").map(String.init)
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 2) {
                Button {
                    navigate("")
                } label: {
                    Label(model.connectionName(connectionId), systemImage: "externaldrive")
                }
                .buttonStyle(.borderless)
                ForEach(parts.indices, id: \.self) { index in
                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(.secondary)
                    Button(parts[index]) {
                        navigate(parts[0...index].joined(separator: "/"))
                    }
                    .buttonStyle(.borderless)
                }
            }
        }
    }

    @ViewBuilder
    private func row(_ entry: BrowseEntry) -> some View {
        if let selection {
            selectableRow(entry, selection: selection)
        } else if let selectedVault, entry.isDir, isVault(entry) {
            vaultRow(entry, selectedVault: selectedVault)
        } else if entry.isDir {
            folderButton(entry)
        } else {
            HStack { fileLabel(entry) }
        }
    }

    /// Radio for a folder (the name still navigates), checkbox for a file. Vault folders hold only
    /// encrypted data and cannot be picked.
    @ViewBuilder
    private func selectableRow(_ entry: BrowseEntry, selection: Binding<OfflineSelection>) -> some View {
        if entry.isDir && isVault(entry) {
            HStack {
                Color.clear.frame(width: 18, height: 1)
                Image(systemName: "lock.shield").foregroundStyle(.secondary).frame(width: 18)
                Text(entry.name).foregroundStyle(.secondary)
                Spacer()
                Text(vaultHint)
                    .font(.caption).foregroundStyle(.secondary)
            }
        } else if entry.isDir {
            let selected = selection.wrappedValue.isFolderSelected(entry.path)
            HStack {
                Button {
                    selection.wrappedValue.toggleFolder(entry.path)
                } label: {
                    Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                        .foregroundStyle(selected ? Color.accentColor : Color.secondary)
                        .frame(width: 18)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text("Select “\(entry.name)”"))
                .accessibilityAddTraits(selected ? .isSelected : [])
                folderButton(entry)
            }
        } else {
            let selected = selection.wrappedValue.isFileSelected(entry.name)
            Button {
                selection.wrappedValue.toggleFile(entry.name)
            } label: {
                HStack {
                    Image(systemName: fileSymbol(selected: selected, multiple: selection.wrappedValue.allowsMultipleFiles))
                        .foregroundStyle(selected ? Color.accentColor : Color.secondary)
                        .frame(width: 18)
                    fileLabel(entry)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Select “\(entry.name)”"))
            .accessibilityAddTraits(selected ? .isSelected : [])
        }
    }

    private func fileSymbol(selected: Bool, multiple: Bool) -> String {
        if multiple { return selected ? "checkmark.square.fill" : "square" }
        return selected ? "largecircle.fill.circle" : "circle"
    }

    /// Radio row for a Vault folder: clicking selects the Vault instead of opening its encrypted content.
    private func vaultRow(_ entry: BrowseEntry, selectedVault: Binding<String?>) -> some View {
        let selected = selectedVault.wrappedValue == entry.path
        return Button {
            selectedVault.wrappedValue = selected ? nil : entry.path
        } label: {
            HStack {
                Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                    .foregroundStyle(selected ? Color.accentColor : Color.secondary)
                    .frame(width: 18)
                Image(systemName: "lock.shield").foregroundStyle(Color.accentColor).frame(width: 18)
                Text(entry.name).fontWeight(selected ? .semibold : .regular)
                Spacer()
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text("Select “\(entry.name)”"))
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func isVault(_ entry: BrowseEntry) -> Bool { entry.name.hasSuffix(".cwvault") }

    private func folderButton(_ entry: BrowseEntry) -> some View {
        Button {
            navigate(entry.path)
        } label: {
            HStack {
                Image(systemName: isVault(entry) ? "lock.shield" : "folder")
                    .foregroundStyle(Color.accentColor)
                    .frame(width: 18)
                Text(entry.name)
                Spacer()
                Image(systemName: "chevron.right").foregroundStyle(.tertiary)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder
    private func fileLabel(_ entry: BrowseEntry) -> some View {
        Image(systemName: "doc").foregroundStyle(.secondary).frame(width: 18)
        Text(entry.name)
        Spacer()
        Text(Format.bytes(entry.size)).foregroundStyle(.secondary).font(.caption)
    }

    private func navigate(_ newPath: String) {
        if newPath != path { path = newPath }
    }

    private func load() async {
        // The previous folder's rows must not stay clickable while the new one loads.
        entries = []
        loading = true
        error = nil
        do {
            entries = try await model.browse(connectionId: connectionId, path: path)
        } catch is CancellationError {
            return
        } catch {
            entries = []
            self.error = ErrorText.alert(for: error).message
        }
        loading = false
    }
}
