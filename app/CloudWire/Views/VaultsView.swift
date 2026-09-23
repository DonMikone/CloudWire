import CloudWireKit
import SwiftUI

private enum VaultSheet: Identifiable {
    case create
    case open
    case unlock(Vault)
    case changePassword(Vault)
    case recover(connectionId: String, vaultPath: String)
    case export(Vault)
    case recoveryKey(String)
    case encrypt(EncryptDraft)

    var id: String {
        switch self {
        case .create: return "create"
        case .open: return "open"
        case .unlock(let vault): return "unlock:\(vault.id)"
        case .changePassword(let vault): return "password:\(vault.id)"
        case .recover(let connectionId, let path): return "recover:\(connectionId):\(path)"
        case .export(let vault): return "export:\(vault.id)"
        case .recoveryKey(let key): return "key:\(key)"
        case .encrypt(let draft): return "encrypt:\(draft.id)"
        }
    }
}

/// Minimum Vault password length.
let minimumVaultPasswordLength = 10

struct VaultsView: View {
    @Environment(AppModel.self) private var model
    @State private var sheet: VaultSheet?
    @State private var removing: Vault?
    @State private var pendingRecoveryKey: String?

    var body: some View {
        SectionScaffold(title: String(localized: "Vaults"),
                        subtitle: String(localized: "Encrypted cloud folders. Names and contents are encrypted on your Mac.")) {
            HStack {
                Button("Open Existing…") { sheet = .open }
                    .disabled(model.remoteConnections.isEmpty)
                Button {
                    sheet = .encrypt(EncryptDraft(connectionId: model.remoteConnections.first?.id ?? "", path: "",
                                                  isDir: true))
                } label: {
                    Label("Encrypt Existing…", systemImage: "lock.rotation")
                }
                .disabled(model.remoteConnections.isEmpty)
                Button {
                    sheet = .create
                } label: {
                    Label("New Vault", systemImage: "plus")
                }
                .disabled(model.remoteConnections.isEmpty)
            }
        } content: {
            if model.vaults.isEmpty {
                ContentUnavailableView {
                    Label("No Vaults", systemImage: SidebarSection.vaults.symbol)
                } description: {
                    Text("A Vault keeps files encrypted in the cloud. Only CloudWire (or rclone) with your password can read them.")
                } actions: {
                    Button("New Vault") { sheet = .create }
                        .buttonStyle(.borderedProminent)
                        .disabled(model.remoteConnections.isEmpty)
                }
            } else {
                List {
                    ForEach(model.vaults) { vault in
                        VaultRow(vault: vault, sheet: $sheet, removing: $removing)
                    }
                }
                .listStyle(.inset)
            }
        }
        .sheet(item: $sheet, onDismiss: showPendingRecoveryKey) { sheet in
            switch sheet {
            case .create:
                NewVaultSheet { key in pendingRecoveryKey = key }
            case .open:
                OpenVaultSheet()
            case .unlock(let vault):
                UnlockVaultSheet(vault: vault)
            case .changePassword(let vault):
                ChangePasswordSheet(vault: vault)
            case .recover(let connectionId, let vaultPath):
                RecoverVaultSheet(connectionId: connectionId, vaultPath: vaultPath)
            case .export(let vault):
                ExportVaultSheet(vault: vault)
            case .recoveryKey(let key):
                RecoveryKeySheet(recoveryKey: key)
            case .encrypt(let draft):
                EncryptSheet(draft: draft)
            }
        }
        .confirmationDialog(
            String(localized: "Remove the Vault “\(removing?.name ?? "")” from CloudWire?"),
            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
            presenting: removing
        ) { vault in
            Button("Remove Vault", role: .destructive) {
                model.perform {
                    try await model.client.removeVault(id: vault.id)
                    await model.refreshVaults()
                }
            }
        } message: { _ in
            Text("CloudWire forgets the Vault and its stored password. The encrypted data stays in the cloud and can be opened again with the password.")
        }
        .onAppear { consumeDraft() }
        .onChange(of: model.encryptDraft) { _, _ in consumeDraft() }
    }

    private func consumeDraft() {
        if let draft = model.encryptDraft {
            model.encryptDraft = nil
            sheet = .encrypt(draft)
        }
    }

    private func showPendingRecoveryKey() {
        if let key = pendingRecoveryKey {
            pendingRecoveryKey = nil
            sheet = .recoveryKey(key)
        }
    }
}

private struct VaultRow: View {
    @Environment(AppModel.self) private var model
    let vault: Vault
    @Binding var sheet: VaultSheet?
    @Binding var removing: Vault?

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: vault.unlocked ? "lock.open.fill" : "lock.fill")
                .font(.title2)
                .foregroundStyle(vault.unlocked ? Color.accentColor : .secondary)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 3) {
                Text(vault.name).font(.headline)
                Text(Format.remote(model.connectionName(vault.connectionId), vault.vaultPath))
                    .font(.callout).foregroundStyle(.secondary)
                Text(vault.unlockMode.label).font(.caption).foregroundStyle(.tertiary)
            }
            Spacer()
            StatusLabel(text: vault.unlocked ? String(localized: "Unlocked") : String(localized: "Locked"),
                        color: vault.unlocked ? .green : .secondary)
            if vault.unlocked {
                Button("Lock") {
                    model.perform {
                        model.updated(try await model.client.lockVault(id: vault.id))
                        await model.refreshVaults()
                    }
                }
            } else {
                Button("Unlock…") { unlock() }
            }
            Menu {
                Button("Mount as Drive") { mountVault() }
                    .disabled(!vault.unlocked)
                Button("Make Available Offline") {
                    model.offlineDraft = OfflineDraft(connectionId: vault.vaultConnectionId, remotePath: "",
                                                      kind: .folder, files: [])
                    model.selection = .offline
                }
                .disabled(!vault.unlocked)
                Button("Encrypt Existing Files Into This Vault…") {
                    sheet = .encrypt(EncryptDraft(connectionId: vault.connectionId, path: "", isDir: true))
                }
                .disabled(!vault.unlocked)
                Divider()
                Button("Change Password…") { sheet = .changePassword(vault) }
                Button("Reset with Recovery Key…") {
                    sheet = .recover(connectionId: vault.connectionId, vaultPath: vault.vaultPath)
                }
                Button("Emergency Export…") { sheet = .export(vault) }
                Divider()
                Button("Remove…", role: .destructive) { removing = vault }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
        }
        .padding(.vertical, 6)
    }

    private func unlock() {
        if vault.unlockMode == .keychain {
            Task {
                do {
                    model.updated(try await model.client.unlockVault(id: vault.id))
                    await model.refreshVaults()
                } catch {
                    sheet = .unlock(vault)
                }
            }
        } else {
            sheet = .unlock(vault)
        }
    }

    private func mountVault() {
        model.perform {
            let mount = try await model.client.createMount(connectionId: vault.vaultConnectionId, remotePath: "",
                                                           fields: ["volumeName": .string(vault.name)])
            model.updated(mount)
            model.selection = .mounts
        }
    }
}

// MARK: - Password entry

/// Password plus repetition with the minimum length rule.
struct NewPasswordFields: View {
    @Binding var password: String
    @Binding var repeated: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            SecureField("Password", text: $password).textFieldStyle(.roundedBorder)
            SecureField("Repeat password", text: $repeated).textFieldStyle(.roundedBorder)
            if !password.isEmpty && password.count < minimumVaultPasswordLength {
                Text("At least \(minimumVaultPasswordLength) characters.").font(.caption).foregroundStyle(.red)
            } else if !repeated.isEmpty && repeated != password {
                Text("The passwords do not match.").font(.caption).foregroundStyle(.red)
            }
        }
    }

    static func isValid(_ password: String, _ repeated: String) -> Bool {
        password.count >= minimumVaultPasswordLength && password == repeated
    }
}

struct UnlockModePicker: View {
    @Binding var mode: UnlockMode

    var body: some View {
        Picker("Unlock", selection: $mode) {
            Text(UnlockMode.keychain.label).tag(UnlockMode.keychain)
            Text(UnlockMode.ask.label).tag(UnlockMode.ask)
        }
        .pickerStyle(.radioGroup)
    }
}

// MARK: - Create

struct NewVaultSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let onRecoveryKey: (String) -> Void

    @State private var connectionId = ""
    @State private var parentPath = ""
    @State private var name = ""
    @State private var password = ""
    @State private var repeated = ""
    @State private var unlockMode: UnlockMode = .keychain
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "New Vault"), width: 600) {
            Form {
                Picker("Connection", selection: $connectionId) {
                    ForEach(model.remoteConnections) { Text($0.name).tag($0.id) }
                }
                TextField("Name", text: $name, prompt: Text("e.g. Private"))
                LabeledContent("Location", value: parentPath.isEmpty ? String(localized: "Top level") : parentPath)
            }
            .formStyle(.grouped)
            .frame(height: 150)
            if !connectionId.isEmpty {
                RemoteBrowser(connectionId: connectionId, path: $parentPath).frame(height: 180)
            }
            NewPasswordFields(password: $password, repeated: $repeated)
            UnlockModePicker(mode: $unlockMode)
            Text("You get a one-time Recovery Key next. Without the password or the Recovery Key the data cannot be recovered.")
                .font(.caption).foregroundStyle(.secondary)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Create Vault") { create() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || connectionId.isEmpty || name.trimmingCharacters(in: .whitespaces).isEmpty
                          || !NewPasswordFields.isValid(password, repeated))
        }
        .onAppear { if connectionId.isEmpty { connectionId = model.remoteConnections.first?.id ?? "" } }
    }

    private func create() {
        busy = true
        error = nil
        let (connectionId, parentPath, name, password, unlockMode) = (connectionId, parentPath, name, password, unlockMode)
        Task {
            do {
                let result = try await model.client.createVault(
                    connectionId: connectionId, parentPath: parentPath,
                    name: name.trimmingCharacters(in: .whitespaces), password: password, unlockMode: unlockMode)
                model.updated(result.vault)
                await model.refreshVaults()
                onRecoveryKey(result.recoveryKey)
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

/// Shows the Recovery Key once; closing requires the confirmation checkbox.
struct RecoveryKeySheet: View {
    @Environment(\.dismiss) private var dismiss
    let recoveryKey: String
    @State private var confirmed = false

    var body: some View {
        SheetScaffold(title: String(localized: "Your Recovery Key"), width: 560) {
            Text("This key opens the Vault if you forget the password. CloudWire does not keep a copy and cannot show it again.")
                .fixedSize(horizontal: false, vertical: true)
            Text(recoveryKey)
                .font(.system(.title3, design: .monospaced))
                .textSelection(.enabled)
                .padding(12)
                .frame(maxWidth: .infinity)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
            HStack {
                Button("Copy") { copyToPasteboard(recoveryKey) }
                Button("Print…") { print() }
            }
            Toggle("I have stored the Recovery Key in a safe place.", isOn: $confirmed)
                .toggleStyle(.checkbox)
        } buttons: {
            Button("Done") { dismiss() }
                .keyboardShortcut(.defaultAction)
                .disabled(!confirmed)
        }
        .interactiveDismissDisabled()
    }

    private func print() {
        let text = String(localized: "CloudWire Vault Recovery Key") + "\n\n" + recoveryKey + "\n"
        let view = NSTextView(frame: NSRect(x: 0, y: 0, width: 480, height: 200))
        view.string = text
        view.font = .monospacedSystemFont(ofSize: 14, weight: .regular)
        NSPrintOperation(view: view).run()
    }
}

// MARK: - Open / unlock / password

struct OpenVaultSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    @State private var connectionId = ""
    @State private var path = ""
    @State private var password = ""
    @State private var unlockMode: UnlockMode = .keychain
    @State private var busy = false
    @State private var error: String?

    private var isVaultFolder: Bool { path.hasSuffix(".cwvault") }

    var body: some View {
        SheetScaffold(title: String(localized: "Open Existing Vault"), width: 600) {
            Picker("Connection", selection: $connectionId) {
                ForEach(model.remoteConnections) { Text($0.name).tag($0.id) }
            }
            if !connectionId.isEmpty {
                RemoteBrowser(connectionId: connectionId, path: $path).frame(height: 220)
            }
            Text(isVaultFolder ? String(localized: "Vault: \(path)") : String(localized: "Open a folder ending in .cwvault."))
                .font(.callout)
                .foregroundStyle(isVaultFolder ? .primary : .secondary)
            SecureField("Password", text: $password).textFieldStyle(.roundedBorder)
            UnlockModePicker(mode: $unlockMode)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Open Vault") { open() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || !isVaultFolder || password.isEmpty)
        }
        .onAppear { if connectionId.isEmpty { connectionId = model.remoteConnections.first?.id ?? "" } }
    }

    private func open() {
        busy = true
        error = nil
        let (connectionId, path, password, unlockMode) = (connectionId, path, password, unlockMode)
        Task {
            do {
                model.updated(try await model.client.openVault(connectionId: connectionId, vaultPath: path,
                                                                password: password, unlockMode: unlockMode))
                await model.refreshVaults()
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

struct UnlockVaultSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let vault: Vault
    @State private var password = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "Unlock “\(vault.name)”"), width: 420) {
            SecureField("Password", text: $password).textFieldStyle(.roundedBorder)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Unlock") { unlock() }.keyboardShortcut(.defaultAction).disabled(busy || password.isEmpty)
        }
    }

    private func unlock() {
        busy = true
        error = nil
        let password = password
        Task {
            do {
                model.updated(try await model.client.unlockVault(id: vault.id, password: password))
                await model.refreshVaults()
                dismiss()
            } catch {
                busy = false
                self.error = ErrorText.alert(for: error).title
            }
        }
    }
}

struct ChangePasswordSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let vault: Vault
    @State private var oldPassword = ""
    @State private var password = ""
    @State private var repeated = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "Change Password of “\(vault.name)”"), width: 460) {
            SecureField("Current password", text: $oldPassword).textFieldStyle(.roundedBorder)
            NewPasswordFields(password: $password, repeated: $repeated)
            Text("Only the key file is re-encrypted; your files stay as they are.")
                .font(.caption).foregroundStyle(.secondary)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Change Password") { change() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || oldPassword.isEmpty || !NewPasswordFields.isValid(password, repeated))
        }
    }

    private func change() {
        busy = true
        error = nil
        let (oldPassword, password) = (oldPassword, password)
        Task {
            do {
                try await model.client.changeVaultPassword(id: vault.id, oldPassword: oldPassword, newPassword: password)
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

struct RecoverVaultSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let connectionId: String
    let vaultPath: String
    @State private var recoveryKey = ""
    @State private var password = ""
    @State private var repeated = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "Reset Password with Recovery Key"), width: 520) {
            Text(Format.remote(model.connectionName(connectionId), vaultPath)).foregroundStyle(.secondary)
            TextField("Recovery Key", text: $recoveryKey, prompt: Text("XXXX-XXXX-…"))
                .textFieldStyle(.roundedBorder)
                .font(.body.monospaced())
            NewPasswordFields(password: $password, repeated: $repeated)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Set New Password") { recover() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || recoveryKey.isEmpty || !NewPasswordFields.isValid(password, repeated))
        }
    }

    private func recover() {
        busy = true
        error = nil
        let (recoveryKey, password) = (recoveryKey, password)
        Task {
            do {
                model.updated(try await model.client.recoverVault(connectionId: connectionId, vaultPath: vaultPath,
                                                                  recoveryKey: recoveryKey, newPassword: password))
                await model.refreshVaults()
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

struct ExportVaultSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let vault: Vault
    @State private var password = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "Emergency Export"), width: 480) {
            Text("Saves an rclone configuration section that opens this Vault without CloudWire. Anyone with this file can read the Vault, so keep it safe.")
                .fixedSize(horizontal: false, vertical: true)
            SecureField("Vault password", text: $password).textFieldStyle(.roundedBorder)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Export…") { export() }.keyboardShortcut(.defaultAction).disabled(busy || password.isEmpty)
        }
    }

    private func export() {
        busy = true
        error = nil
        let password = password
        Task {
            do {
                let ini = try await model.client.exportVaultRclone(id: vault.id, password: password)
                busy = false
                guard let path = Panels.savePanel(name: "\(vault.name)-rclone.conf",
                                                  message: String(localized: "Save the rclone configuration"))
                else { return }
                try ini.write(toFile: path, atomically: true, encoding: .utf8)
                try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path)
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

// MARK: - Encrypt existing

struct EncryptSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let draft: EncryptDraft

    private enum TargetKind: Hashable { case newVault, existingVault }

    @State private var connectionId = ""
    @State private var browsePath = ""
    @State private var selectedFiles: Set<String> = []
    @State private var sourcePath = ""
    @State private var isDir = true
    @State private var targetKind: TargetKind = .newVault
    @State private var vaultName = ""
    @State private var password = ""
    @State private var repeated = ""
    @State private var unlockMode: UnlockMode = .keychain
    @State private var vaultId = ""
    @State private var subPath = ""
    @State private var ignorePauseRules = false
    @State private var jobId: String?
    @State private var recoveryKey: String?
    @State private var keyStored = false
    @State private var busy = false
    @State private var error: String?
    @State private var confirmDelete = false
    @State private var prepared = false

    private var migration: VaultMigrationEvent? { jobId.flatMap { model.migrations[$0] } }

    var body: some View {
        SheetScaffold(title: String(localized: "Encrypt into a Vault"), width: 620) {
            if let jobId {
                progressView(jobId)
            } else {
                setupForm
            }
            if let error { InlineError(message: error) }
        } buttons: {
            if jobId == nil {
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Encrypt") { start() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!canStart)
            } else {
                Button("Close") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                    .disabled(recoveryKey != nil && !keyStored)
                if migration?.status == "verified" {
                    Button("Delete Original…", role: .destructive) { confirmDelete = true }
                }
            }
        }
        .onAppear(perform: prepare)
        .confirmationDialog(String(localized: "Delete the unencrypted original?"), isPresented: $confirmDelete) {
            Button("Delete Original", role: .destructive) { deleteOriginal() }
        } message: {
            Text("The encrypted copy was verified. The original “\(sourcePath)” is deleted from the cloud.")
        }
    }

    private var setupForm: some View {
        VStack(alignment: .leading, spacing: 12) {
            if draft.path.isEmpty {
                Picker("Connection", selection: $connectionId) {
                    ForEach(model.remoteConnections) { Text($0.name).tag($0.id) }
                }
                if !connectionId.isEmpty {
                    RemoteBrowser(connectionId: connectionId, path: $browsePath, selectsFiles: true,
                                  selectedFiles: $selectedFiles)
                        .frame(height: 200)
                }
                Text(selectedFiles.count == 1
                     ? String(localized: "Encrypts the selected file.")
                     : String(localized: "Encrypts the folder “\(browsePath.isEmpty ? "/" : browsePath)”. Select a single file to encrypt only that file."))
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                LabeledContent("Source", value: Format.remote(model.connectionName(connectionId), sourcePath))
            }
            Picker("Target", selection: $targetKind) {
                Text("New Vault").tag(TargetKind.newVault)
                Text("Existing Vault").tag(TargetKind.existingVault)
                    .selectionDisabled(unlockedVaults.isEmpty)
            }
            .pickerStyle(.segmented)
            if targetKind == .newVault {
                TextField("Vault name", text: $vaultName).textFieldStyle(.roundedBorder)
                NewPasswordFields(password: $password, repeated: $repeated)
                UnlockModePicker(mode: $unlockMode)
            } else {
                Picker("Vault", selection: $vaultId) {
                    ForEach(unlockedVaults) { Text($0.name).tag($0.id) }
                }
                TextField("Folder inside the Vault", text: $subPath, prompt: Text("Top level")).textFieldStyle(.roundedBorder)
            }
            Toggle("Run without pauses", isOn: $ignorePauseRules)
            Text("The original is only deleted after the encrypted copy has been verified and you confirm.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var unlockedVaults: [Vault] {
        model.vaults.filter { $0.unlocked && $0.connectionId == connectionId }
    }

    private var canStart: Bool {
        guard !busy, !connectionId.isEmpty else { return false }
        switch targetKind {
        case .newVault:
            return !vaultName.trimmingCharacters(in: .whitespaces).isEmpty && NewPasswordFields.isValid(password, repeated)
        case .existingVault:
            return !vaultId.isEmpty
        }
    }

    private func progressView(_ jobId: String) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if let recoveryKey {
                GroupBox("Recovery Key of the new Vault") {
                    VStack(alignment: .leading, spacing: 8) {
                        Text(recoveryKey).font(.body.monospaced()).textSelection(.enabled)
                        HStack {
                            Button("Copy") { copyToPasteboard(recoveryKey) }
                            Toggle("I have stored the Recovery Key in a safe place.", isOn: $keyStored)
                                .toggleStyle(.checkbox)
                        }
                    }
                    .padding(4)
                }
            }
            let status = migration?.status ?? "queued"
            HStack(spacing: 10) {
                if ["queued", "running"].contains(status) { ProgressView().controlSize(.small) }
                Text(statusText(status)).font(.headline)
            }
            if let mismatches = migration?.mismatches, !mismatches.isEmpty {
                Text("Files that differ:").font(.callout)
                List(mismatches, id: \.self) { Text($0) }.frame(height: 120)
            }
            if let failure = migration?.error, !failure.isEmpty {
                InlineError(message: failure)
            }
            Text("You can close this window; the job continues in the background.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private func statusText(_ status: String) -> String {
        switch status {
        case "queued": return String(localized: "Waiting to start…")
        case "running": return String(localized: "Encrypting and copying…")
        case "verified": return String(localized: "The encrypted copy is complete and verified.")
        case "mismatch": return String(localized: "Verification found differences. The original was kept.")
        case "error": return String(localized: "Encryption failed. The original was kept.")
        case "deleted": return String(localized: "Done. The original was deleted.")
        default: return status
        }
    }

    private func prepare() {
        guard !prepared else { return }
        prepared = true
        connectionId = draft.connectionId.isEmpty ? (model.remoteConnections.first?.id ?? "") : draft.connectionId
        sourcePath = draft.path
        isDir = draft.isDir
        if !draft.path.isEmpty {
            vaultName = (draft.path as NSString).lastPathComponent
        }
        if let vault = unlockedVaults.first { vaultId = vault.id }
    }

    private func start() {
        if draft.path.isEmpty {
            if selectedFiles.count == 1, let file = selectedFiles.first {
                sourcePath = browsePath.isEmpty ? file : "\(browsePath)/\(file)"
                isDir = false
            } else {
                sourcePath = browsePath
                isDir = true
            }
        }
        let target: CoreClient.EncryptTarget = targetKind == .newVault
            ? .newVault(name: vaultName.trimmingCharacters(in: .whitespaces), password: password, unlockMode: unlockMode)
            : .existingVault(vaultId: vaultId, subPath: subPath.trimmingCharacters(in: CharacterSet(charactersIn: "/ ")))
        busy = true
        error = nil
        let (connectionId, sourcePath, isDir, ignorePauseRules) = (connectionId, sourcePath, isDir, ignorePauseRules)
        Task {
            do {
                let result = try await model.client.encryptExisting(connectionId: connectionId, path: sourcePath,
                                                                    isDir: isDir, target: target,
                                                                    ignorePauseRules: ignorePauseRules)
                jobId = result.jobId
                recoveryKey = result.recoveryKey
                busy = false
                await model.refreshVaults()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }

    private func deleteOriginal() {
        guard let jobId else { return }
        model.perform {
            try await model.client.migrationConfirmDelete(jobId: jobId)
        }
    }
}
