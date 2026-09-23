import CloudWireKit
import SwiftUI

struct MountsView: View {
    @Environment(AppModel.self) private var model
    @State private var editorTarget: MountEditorTarget?
    @State private var deleting: Mount?

    var body: some View {
        SectionScaffold(title: String(localized: "Mounts"),
                        subtitle: String(localized: "Cloud folders as drives in Finder, streamed on demand.")) {
            Button {
                editorTarget = .new(connectionId: nil, remotePath: "")
            } label: {
                Label("New Mount", systemImage: "plus")
            }
            .disabled(model.mountableConnections.isEmpty)
        } content: {
            if model.mounts.isEmpty {
                ContentUnavailableView {
                    Label("No Mounts", systemImage: SidebarSection.mounts.symbol)
                } description: {
                    Text("A Mount shows a cloud folder as a drive in Finder. Files download when you open them.")
                } actions: {
                    Button("New Mount") { editorTarget = .new(connectionId: nil, remotePath: "") }
                        .buttonStyle(.borderedProminent)
                        .disabled(model.mountableConnections.isEmpty)
                }
            } else {
                List {
                    ForEach(model.mounts) { mount in
                        MountRow(mount: mount, onEdit: { editorTarget = .edit(mount) }, onDelete: { deleting = mount })
                    }
                }
                .listStyle(.inset)
            }
        }
        .sheet(item: $editorTarget) { target in
            MountEditor(target: target)
        }
        .confirmationDialog(
            String(localized: "Delete the Mount “\(deleting?.volumeName ?? "")”?"),
            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
            presenting: deleting
        ) { mount in
            Button("Delete Mount", role: .destructive) {
                model.perform {
                    try await model.client.deleteMount(id: mount.id)
                    model.mounts.removeAll { $0.id == mount.id }
                }
            }
        } message: { _ in
            Text("The drive is ejected and removed from CloudWire. Files in the cloud are not touched.")
        }
    }
}

private struct MountRow: View {
    @Environment(AppModel.self) private var model
    let mount: Mount
    let onEdit: () -> Void
    let onDelete: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Toggle("", isOn: Binding(
                get: { mount.state == .mounted || mount.state == .mounting },
                set: { model.setMounted(mount, $0) }))
                .toggleStyle(.switch)
                .labelsHidden()
                .disabled(mount.state == .mounting)
            VStack(alignment: .leading, spacing: 3) {
                HStack {
                    Text(mount.volumeName).font(.headline)
                    if mount.readOnly {
                        Text("Read-only").font(.caption).padding(.horizontal, 5)
                            .background(.quaternary, in: Capsule())
                    }
                    if mount.mountType == .cmount {
                        Text("FUSE").font(.caption).padding(.horizontal, 5)
                            .background(.quaternary, in: Capsule())
                    }
                }
                Text(Format.remote(model.connectionName(mount.connectionId), mount.remotePath))
                    .font(.callout).foregroundStyle(.secondary)
                Text(CorePaths.abbreviate(mount.mountPoint))
                    .font(.caption).foregroundStyle(.tertiary)
                if mount.state == .error, !mount.error.isEmpty {
                    InlineError(message: mount.error)
                }
            }
            Spacer()
            StatusLabel(text: mount.state.label, color: mount.state.color)
            Button {
                model.showInFinder(mount.mountPoint)
            } label: {
                Image(systemName: "folder")
            }
            .help(Text("Show in Finder"))
            .disabled(!mount.isMounted)
            Button("Edit…", action: onEdit)
            Button(role: .destructive, action: onDelete) {
                Image(systemName: "trash")
            }
            .help(Text("Delete"))
        }
        .padding(.vertical, 6)
        .contextMenu {
            Button("Show in Finder") { model.showInFinder(mount.mountPoint) }
            Button("Edit…", action: onEdit)
            Divider()
            Button("Delete…", role: .destructive, action: onDelete)
        }
    }
}

enum MountEditorTarget: Identifiable {
    case new(connectionId: String?, remotePath: String)
    case edit(Mount)

    var id: String {
        switch self {
        case .new(let connectionId, let remotePath): return "new:\(connectionId ?? ""):\(remotePath)"
        case .edit(let mount): return mount.id
        }
    }
}

struct MountEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let target: MountEditorTarget

    @State private var detail: FormDetail = .simple
    @State private var volumeName = ""
    @State private var connectionId = ""
    @State private var remotePath = ""
    @State private var mountPoint = ""
    @State private var readOnly = false
    @State private var cacheGB = 20
    @State private var autoMount = true
    @State private var mountType: MountType = .nfsmount
    @State private var form = OptionFormModel(options: [], hideContext: .commandLine)
    @State private var optionBlocks: [(String, [RcloneOption])] = []
    @State private var browsing = false
    @State private var busy = false
    @State private var error: String?
    @State private var loaded = false

    /// Options covered by the simple form.
    private static let simpleOptionNames: Set<String> = ["vfs_cache_max_size", "read_only", "volname", "vfs_cache_mode"]

    private var existing: Mount? {
        if case .edit(let mount) = target { return mount }
        return nil
    }

    var body: some View {
        SheetScaffold(title: existing == nil ? String(localized: "New Mount") : String(localized: "Edit Mount"),
                      width: 640) {
            FormDetailPicker(selection: $detail)
            if detail == .simple {
                simpleForm
            } else {
                advancedForm
            }
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button(existing == nil ? "Create" : "Save") { save() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || connectionId.isEmpty)
        }
        .task { await load() }
    }

    private var defaultMountPoint: String {
        let folder = model.settings.mountFolder
        let name = volumeName.isEmpty ? String(localized: "Name") : volumeName
        return "\(folder)/\(name)"
    }

    private var simpleForm: some View {
        Form {
            TextField("Name", text: $volumeName, prompt: Text(model.connectionName(connectionId)))
            Picker("Connection", selection: $connectionId) {
                ForEach(model.mountableConnections) { connection in
                    Text(connection.kind == .vault
                         ? String(localized: "\(model.connectionName(connection.id)) (Vault)") : connection.name)
                        .tag(connection.id)
                }
            }
            .disabled(existing != nil)
            LabeledContent("Cloud folder") {
                HStack {
                    TextField("Cloud folder", text: $remotePath, prompt: Text("Whole connection"))
                        .labelsHidden()
                        .textFieldStyle(.roundedBorder)
                    Button("Browse…") { browsing = true }
                        .disabled(connectionId.isEmpty)
                        .popover(isPresented: $browsing) {
                            VStack(alignment: .trailing) {
                                RemoteBrowser(connectionId: connectionId, path: $remotePath)
                                    .frame(width: 420, height: 320)
                                Button("Use This Folder") { browsing = false }
                            }
                            .padding()
                        }
                }
            }
            FolderField(title: "Mount point", path: $mountPoint, placeholder: defaultMountPoint,
                        message: String(localized: "Choose an empty folder for the drive."))
            Toggle("Read-only", isOn: $readOnly)
            Stepper(value: $cacheGB, in: 1...4096) {
                LabeledContent("Cache size", value: String(localized: "\(cacheGB) GB"))
            }
            Toggle("Mount automatically", isOn: $autoMount)
            Picker("Technology", selection: $mountType) {
                Text(MountType.nfsmount.label).tag(MountType.nfsmount)
                Text(MountType.cmount.label).tag(MountType.cmount)
                    .selectionDisabled(!model.fuseStatus.available)
            }
            if !model.fuseStatus.available {
                Text("FUSE needs FUSE-T or macFUSE. Install one of them to use it; NFS works without any installation.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .frame(height: 420)
    }

    private var advancedForm: some View {
        Form {
            if optionBlocks.isEmpty {
                ProgressView()
            }
            ForEach(optionBlocks, id: \.0) { block in
                Section(block.0) {
                    OptionFormView(form: $form, options: block.1, showsNames: true)
                }
            }
        }
        .formStyle(.grouped)
        .frame(height: 420)
    }

    private func load() async {
        guard !loaded else { return }
        loaded = true
        cacheGB = model.settings.defaultCacheMaxGB
        mountType = model.fuseStatus.available ? model.settings.defaultMountType : .nfsmount
        switch target {
        case .new(let preset, let path):
            connectionId = preset ?? model.mountableConnections.first?.id ?? ""
            remotePath = path
        case .edit(let mount):
            connectionId = mount.connectionId
            remotePath = mount.remotePath
            volumeName = mount.volumeName
            mountPoint = mount.mountPoint
            readOnly = mount.readOnly
            cacheGB = mount.cacheMaxGB
            autoMount = mount.autoMount
            mountType = mount.mountType
        }
        do {
            let info = try await model.loadMountOptions()
            func filtered(_ options: [RcloneOption]) -> [RcloneOption] {
                options.filter { !Self.simpleOptionNames.contains($0.name) && $0.hide & 1 == 0 }
            }
            let blocks = [("VFS", filtered(info.vfs)), ("Mount", filtered(info.mount)), ("NFS", filtered(info.nfs))]
            optionBlocks = blocks.filter { !$0.1.isEmpty }
            form = OptionFormModel(options: info.vfs + info.mount + info.nfs, hideContext: .commandLine,
                                   initialValues: existing?.options ?? [:])
        } catch {
            self.error = ErrorText.alert(for: error).message
        }
    }

    private func save() {
        let options: [String: String]
        do {
            options = try form.parameters().filter { !Self.simpleOptionNames.contains($0.key) }
        } catch {
            detail = .advanced
            self.error = ErrorText.alert(for: error).message
            return
        }
        var fields: [String: JSONValue] = [
            "mountType": .string(mountType.rawValue),
            "autoMount": .bool(autoMount),
            "readOnly": .bool(readOnly),
            "cacheMaxGB": .int(Int64(cacheGB)),
            "options": options.jsonValue,
        ]
        if !volumeName.trimmingCharacters(in: .whitespaces).isEmpty {
            fields["volumeName"] = .string(volumeName.trimmingCharacters(in: .whitespaces))
        }
        if !mountPoint.isEmpty {
            fields["mountPoint"] = .string(CorePaths.expandTilde(mountPoint))
        }
        let path = remotePath.trimmingCharacters(in: CharacterSet(charactersIn: "/ "))
        busy = true
        error = nil
        Task {
            do {
                let mount: Mount
                if let existing {
                    fields["remotePath"] = .string(path)
                    mount = try await model.client.updateMount(id: existing.id, fields: fields)
                } else {
                    mount = try await model.client.createMount(connectionId: connectionId, remotePath: path,
                                                               fields: fields)
                }
                model.updated(mount)
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}
