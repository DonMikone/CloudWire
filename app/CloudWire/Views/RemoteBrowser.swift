import CloudWireKit
import SwiftUI

/// Browses a Connection: folders navigate, files can be (multi-)selected when `selectsFiles`.
struct RemoteBrowser: View {
    @Environment(AppModel.self) private var model
    let connectionId: String
    /// The current folder (relative to the Connection root, "" = root).
    @Binding var path: String
    var selectsFiles = false
    @Binding var selectedFiles: Set<String>

    @State private var entries: [BrowseEntry] = []
    @State private var loading = false
    @State private var error: String?

    init(connectionId: String, path: Binding<String>, selectsFiles: Bool = false,
         selectedFiles: Binding<Set<String>> = .constant([]))
    {
        self.connectionId = connectionId
        self._path = path
        self.selectsFiles = selectsFiles
        self._selectedFiles = selectedFiles
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            breadcrumbs
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
            .frame(minHeight: 220)
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
        if entry.isDir {
            Button {
                navigate(entry.path)
            } label: {
                HStack {
                    Image(systemName: entry.name.hasSuffix(".cwvault") ? "lock.shield" : "folder")
                        .foregroundStyle(Color.accentColor)
                    Text(entry.name)
                    Spacer()
                    Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
        } else {
            HStack {
                if selectsFiles {
                    Toggle("", isOn: Binding(
                        get: { selectedFiles.contains(entry.name) },
                        set: { isOn in
                            if isOn { selectedFiles.insert(entry.name) } else { selectedFiles.remove(entry.name) }
                        }))
                    .labelsHidden()
                    .toggleStyle(.checkbox)
                }
                Image(systemName: "doc").foregroundStyle(.secondary)
                Text(entry.name)
                Spacer()
                Text(Format.bytes(entry.size)).foregroundStyle(.secondary).font(.caption)
            }
        }
    }

    private func navigate(_ newPath: String) {
        if newPath != path {
            selectedFiles.removeAll()
            path = newPath
        }
    }

    private func load() async {
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
