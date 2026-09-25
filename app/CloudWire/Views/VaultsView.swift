import CloudWireKit
import SwiftUI

private enum VaultSheet: Identifiable {
    case create
    case open
    case unlock(Vault)
    case changePassword(Vault)
    case recover(connectionId: String, vaultPath: String)
    case export(Vault)
    case recoveryKey(String, Vault)
    case encrypt(EncryptDraft)
    case job(VaultMigrationEvent)

    var id: String {
        switch self {
        case .create: return "create"
        case .open: return "open"
        case .unlock(let vault): return "unlock:\(vault.id)"
        case .changePassword(let vault): return "password:\(vault.id)"
        case .recover(let connectionId, let path): return "recover:\(connectionId):\(path)"
        case .export(let vault): return "export:\(vault.id)"
        case .recoveryKey(let key, _): return "key:\(key)"
        case .encrypt(let draft): return "encrypt:\(draft.id)"
        case .job(let job): return "job:\(job.jobId)"
        }
    }
}

/// Minimum Vault password length.
let minimumVaultPasswordLength = 10

struct VaultsView: View {
    @Environment(AppModel.self) private var model
    @State private var sheet: VaultSheet?
    @State private var removing: Vault?
    @State private var pendingRecoveryKey: (key: String, vault: Vault)?

    var body: some View {
        SectionScaffold(title: String(localized: "Vaults"),
                        subtitle: String(localized: "Encrypted cloud folders. Names and contents are encrypted on your Mac.")) {
            HStack {
                Button("Open Vault…") { sheet = .open }
                    .disabled(model.remoteConnections.isEmpty)
                Button {
                    sheet = .encrypt(EncryptDraft(connectionId: model.remoteConnections.first?.id ?? "", path: "",
                                                  isDir: true))
                } label: {
                    Label("Encrypt Files…", systemImage: "lock.rotation")
                }
                .disabled(model.remoteConnections.isEmpty)
                Button {
                    sheet = .create
                } label: {
                    Label("New Vault", systemImage: "plus")
                }
                .disabled(model.remoteConnections.isEmpty)
            }
            .fixedSize()
        } content: {
            if model.vaults.isEmpty {
                ContentUnavailableView {
                    Label("No Vaults", systemImage: SidebarSection.vaults.symbol)
                } description: {
                    if model.remoteConnections.isEmpty {
                        Text("Add a Connection first.")
                    } else {
                        Text("A Vault keeps files encrypted in the cloud. Only CloudWire (or rclone) with your password can read them.")
                    }
                } actions: {
                    if model.remoteConnections.isEmpty {
                        Button("Add Connection…") { model.showAddConnection = true }
                            .buttonStyle(.borderedProminent)
                    } else {
                        Button("New Vault") { sheet = .create }
                            .buttonStyle(.borderedProminent)
                        Button("Open Existing Vault…") { sheet = .open }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List {
                    ForEach(model.vaults) { vault in
                        VaultRow(vault: vault, sheet: $sheet, removing: $removing)
                    }
                }
                .listStyle(.inset)
            }
        }
        .sheet(item: $sheet, onDismiss: sheetDismissed) { sheet in
            switch sheet {
            case .create:
                NewVaultSheet { key, vault in pendingRecoveryKey = (key, vault) }
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
            case .recoveryKey(let key, let vault):
                RecoveryKeySheet(recoveryKey: key, vault: vault)
            case .encrypt(let draft):
                EncryptSheet(draft: draft)
            case .job(let job):
                EncryptSheet(job: job)
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
        .onAppear { consumeRequests() }
        .onChange(of: model.encryptDraft) { _, _ in consumeRequests() }
        .onChange(of: model.vaultUnlockRequest) { _, _ in consumeRequests() }
    }

    /// Opens the sheet for a request from outside: an encrypt draft (Finder, `cloudwire://`) or a locked
    /// Vault to unlock (App start). While another sheet is open (possibly showing a one-time Recovery Key)
    /// the request waits in the model until it closes.
    private func consumeRequests() {
        guard sheet == nil else { return }
        if let vault = model.vaultUnlockRequest {
            model.vaultUnlockRequest = nil
            if let current = model.vaults.first(where: { $0.id == vault.id }), !current.unlocked {
                sheet = .unlock(current)
                return
            }
        }
        if let draft = model.encryptDraft {
            model.encryptDraft = nil
            sheet = .encrypt(draft)
        }
    }

    private func sheetDismissed() {
        if let pending = pendingRecoveryKey {
            pendingRecoveryKey = nil
            sheet = .recoveryKey(pending.key, pending.vault)
        } else {
            consumeRequests()
        }
    }
}

private struct VaultRow: View {
    @Environment(AppModel.self) private var model
    let vault: Vault
    @Binding var sheet: VaultSheet?
    @Binding var removing: Vault?
    @State private var busy = false
    @State private var confirmLock = false
    @State private var deletingOriginal: VaultMigrationEvent?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 12) {
                Image(systemName: vault.unlocked ? "lock.open.fill" : "lock.fill")
                    .font(.title2)
                    .foregroundStyle(vault.unlocked ? Color.accentColor : .secondary)
                    .frame(width: 32)
                VStack(alignment: .leading, spacing: 3) {
                    Text(vault.name).font(.headline)
                    Text(Format.remote(model.connectionName(vault.connectionId), vault.vaultPath))
                        .font(.callout).foregroundStyle(.secondary)
                    Text(vault.unlockMode.label).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                StatusLabel(text: vault.unlocked ? String(localized: "Unlocked") : String(localized: "Locked"),
                            color: vault.unlocked ? .green : .secondary)
                if busy {
                    ProgressView().controlSize(.small)
                }
                if vault.unlocked {
                    Button("Lock") {
                        if dependents.isEmpty { lock() } else { confirmLock = true }
                    }
                    .disabled(busy)
                } else {
                    Button("Unlock…") { unlock() }
                        .disabled(busy)
                }
                actionsMenu
            }
            ForEach(jobs) { job in
                jobRow(job)
                    .padding(.leading, 44)
            }
        }
        .padding(.vertical, 6)
        .confirmationDialog(String(localized: "Lock “\(vault.name)”?"), isPresented: $confirmLock) {
            Button("Lock") { lock() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("These stop until the Vault is unlocked again: \(dependents.joined(separator: ", ")).")
        }
        .confirmationDialog(String(localized: "Delete the unencrypted original?"),
                            isPresented: Binding(get: { deletingOriginal != nil },
                                                 set: { if !$0 { deletingOriginal = nil } }),
                            presenting: deletingOriginal)
        { job in
            Button("Delete Original", role: .destructive) {
                model.perform { try await model.client.migrationConfirmDelete(jobId: job.jobId) }
            }
        } message: { job in
            Text("The encrypted copy was verified. The original “\(job.path)” is deleted from the cloud.")
        }
    }

    private var actionsMenu: some View {
        Menu {
            Button("Mount as Drive") { mountVault() }
                .disabled(!vault.unlocked)
            Button("Make Available Offline") {
                model.offlineDraft = OfflineDraft(connectionId: vault.vaultConnectionId, paths: [""])
                model.selection = .offline
            }
            .disabled(!vault.unlocked)
            Button("Encrypt Existing Files Into This Vault…") {
                sheet = .encrypt(EncryptDraft(connectionId: vault.connectionId, path: "", isDir: true,
                                              vaultId: vault.id))
            }
            .disabled(!vault.unlocked)
            Divider()
            Button("Change Password…") { sheet = .changePassword(vault) }
            Button("Reset Password with Recovery Key…") {
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
        .help("More Actions")
        .accessibilityLabel(Text("Actions for “\(vault.name)”"))
    }

    /// Encryption jobs into this Vault that are running or still need attention; deleted and stopped
    /// jobs are done.
    private var jobs: [VaultMigrationEvent] {
        model.migrations.values
            .filter { $0.vaultId == vault.id && !["deleted", "canceled"].contains($0.status) }
            .sorted { $0.createdAt > $1.createdAt }
    }

    private func jobRow(_ job: VaultMigrationEvent) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "lock.rotation").foregroundStyle(.secondary).frame(width: 18)
            VStack(alignment: .leading, spacing: 4) {
                Text(jobTitle(job)).font(.callout).lineLimit(1).truncationMode(.middle)
                if job.status == "running" {
                    MigrationProgress(migration: job).frame(maxWidth: 360)
                } else {
                    Text(jobStatus(job)).font(.caption)
                        .foregroundStyle(["mismatch", "error"].contains(job.status) ? Color.red : Color.secondary)
                }
            }
            Spacer()
            if job.isActive {
                Button("Stop") {
                    model.perform { try await model.client.cancelMigration(jobId: job.jobId) }
                }
            }
            if job.status == "verified" {
                Button("Delete Original…") { deletingOriginal = job }
            } else {
                Button("Details…") { sheet = .job(job) }
            }
        }
    }

    private func jobTitle(_ job: VaultMigrationEvent) -> String {
        let source = Format.remote(model.connectionName(job.connectionId), job.path)
        return job.isActive ? String(localized: "Encrypting “\(source)”")
            : String(localized: "Encryption of “\(source)”")
    }

    private func jobStatus(_ job: VaultMigrationEvent) -> String {
        switch job.status {
        case "queued": return String(localized: "Waiting to start…")
        case "verified": return String(localized: "Verified. The original can be deleted now.")
        case "mismatch": return String(localized: "Verification found differences. The original was kept.")
        case "error": return String(localized: "Encryption failed. The original was kept.")
        default: return job.status
        }
    }

    /// Mounts and Offline Items that stop working while the Vault is locked.
    private var dependents: [String] {
        model.mounts.filter { $0.connectionId == vault.vaultConnectionId }.map(\.volumeName)
            + model.offlineItems.filter { $0.connectionId == vault.vaultConnectionId }.map(\.displayName)
    }

    private func lock() {
        busy = true
        model.perform {
            defer { busy = false }
            model.updated(try await model.client.lockVault(id: vault.id))
            await model.refreshVaults()
        }
    }

    private func unlock() {
        guard vault.unlockMode == .keychain else {
            sheet = .unlock(vault)
            return
        }
        busy = true
        Task {
            defer { busy = false }
            do {
                model.updated(try await model.client.unlockVault(id: vault.id))
                await model.refreshVaults()
            } catch {
                sheet = .unlock(vault)
            }
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

/// Password plus repetition with the minimum length rule. `replacing` labels them as the new password,
/// next to a current password or Recovery Key.
struct NewPasswordFields: View {
    @Binding var password: String
    @Binding var repeated: String
    var replacing = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            SecureField(replacing ? LocalizedStringKey("New password") : "Password", text: $password)
                .textFieldStyle(.roundedBorder)
            SecureField(replacing ? LocalizedStringKey("Repeat new password") : "Repeat password", text: $repeated)
                .textFieldStyle(.roundedBorder)
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
    let onRecoveryKey: (String, Vault) -> Void

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
            Picker("Connection", selection: $connectionId) {
                ForEach(model.remoteConnections) { Text($0.name).tag($0.id) }
            }
            LabeledContent("Name") {
                TextField("Name", text: $name, prompt: Text("e.g. Private"))
                    .labelsHidden()
                    .textFieldStyle(.roundedBorder)
            }
            if !connectionId.isEmpty {
                RemoteBrowser(connectionId: connectionId, path: $parentPath).frame(height: 250)
                Text("The Vault is created in: \(Format.remote(model.connectionName(connectionId), parentPath))")
                    .font(.callout)
                    .lineLimit(1).truncationMode(.middle)
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
        .onChange(of: connectionId) { old, _ in
            // The folder belongs to the previous Connection: start over at its root.
            if !old.isEmpty { parentPath = "" }
        }
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
                onRecoveryKey(result.recoveryKey, result.vault)
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
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let recoveryKey: String
    let vault: Vault
    @State private var confirmed = false

    var body: some View {
        SheetScaffold(title: String(localized: "Recovery Key for “\(vault.name)”"), width: 560) {
            RecoveryKeyPanel(recoveryKey: recoveryKey, vaultName: vault.name,
                             location: Format.remote(model.connectionName(vault.connectionId), vault.vaultPath),
                             confirmed: $confirmed)
        } buttons: {
            Button("Done") { dismiss() }
                .keyboardShortcut(.defaultAction)
                .disabled(!confirmed)
        }
        .interactiveDismissDisabled()
    }
}

/// The one-time Recovery Key with copy, print and the "stored safely" confirmation.
struct RecoveryKeyPanel: View {
    let recoveryKey: String
    let vaultName: String
    /// `Connection: path` of the Vault, when known.
    let location: String?
    @Binding var confirmed: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let location {
                Text(location).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
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
        }
    }

    /// The printout names the Vault, so it can still be matched to its Vault years later.
    private func print() {
        var lines = [String(localized: "CloudWire Vault Recovery Key"), "",
                     String(localized: "Vault: \(vaultName)")]
        if let location { lines.append(String(localized: "Location: \(location)")) }
        lines.append(String(localized: "Date: \(Date().formatted(date: .long, time: .shortened))"))
        lines += ["", recoveryKey, ""]
        let view = NSTextView(frame: NSRect(x: 0, y: 0, width: 480, height: 260))
        view.string = lines.joined(separator: "\n")
        view.font = .monospacedSystemFont(ofSize: 14, weight: .regular)
        NSPrintOperation(view: view).run()
    }
}

// MARK: - Open / unlock / password

struct OpenVaultSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    @State private var connectionId = ""
    @State private var browsePath = ""
    /// The picked `.cwvault` folder.
    @State private var vaultPath: String?
    @State private var password = ""
    @State private var unlockMode: UnlockMode = .keychain
    @State private var busy = false
    @State private var error: String?

    private var vaultName: String? {
        vaultPath.map { (($0 as NSString).lastPathComponent as NSString).deletingPathExtension }
    }

    var body: some View {
        SheetScaffold(title: String(localized: "Open Existing Vault"), width: 600) {
            Picker("Connection", selection: $connectionId) {
                ForEach(model.remoteConnections) { Text($0.name).tag($0.id) }
            }
            if !connectionId.isEmpty {
                RemoteBrowser(connectionId: connectionId, path: $browsePath, selectedVault: $vaultPath)
                    .frame(height: 250)
            }
            Text(vaultName.map { String(localized: "Selected: Vault “\($0)”") }
                 ?? String(localized: "Select a Vault (shield symbol)."))
                .font(.callout)
                .foregroundStyle(vaultName == nil ? .secondary : .primary)
            SecureField("Password", text: $password).textFieldStyle(.roundedBorder)
            UnlockModePicker(mode: $unlockMode)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Open Vault") { open() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || vaultPath == nil || password.isEmpty)
        }
        .onAppear { if connectionId.isEmpty { connectionId = model.remoteConnections.first?.id ?? "" } }
        .onChange(of: connectionId) { old, _ in
            // Folder and Vault belong to the previous Connection: start over at its root.
            if !old.isEmpty {
                browsePath = ""
                vaultPath = nil
            }
        }
    }

    private func open() {
        guard let path = vaultPath else { return }
        busy = true
        error = nil
        let (connectionId, password, unlockMode) = (connectionId, password, unlockMode)
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
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
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
            NewPasswordFields(password: $password, repeated: $repeated, replacing: true)
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
            LabeledContent("Recovery Key") {
                TextField("Recovery Key", text: $recoveryKey, prompt: Text("XXXX-XXXX-…"))
                    .labelsHidden()
                    .textFieldStyle(.roundedBorder)
                    .font(.body.monospaced())
            }
            NewPasswordFields(password: $password, repeated: $repeated, replacing: true)
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
    @State private var selection = OfflineSelection.singleItem()
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
    /// The Vault the job encrypts into, known once it started.
    @State private var targetVaultId: String?
    @State private var keyStored = false
    @State private var busy = false
    @State private var error: String?
    @State private var confirmDelete = false
    @State private var prepared = false

    private var migration: VaultMigrationEvent? { jobId.flatMap { model.migrations[$0] } }

    init(draft: EncryptDraft) {
        self.draft = draft
    }

    /// Shows a started job, e.g. from "Details…" in the Vaults list.
    init(job: VaultMigrationEvent) {
        draft = EncryptDraft(connectionId: job.connectionId, path: job.path, isDir: job.isDir)
        _jobId = State(initialValue: job.jobId)
    }

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
                if migration?.isActive ?? true {
                    Button("Stop Encryption") { cancelJob() }
                }
                if migration?.status == "verified" {
                    Button("Delete Original…", role: .destructive) { confirmDelete = true }
                }
            }
        }
        .onAppear(perform: prepare)
        .onChange(of: connectionId) { old, _ in
            // Folder, file and Vault belong to the previous Connection: start over at its root.
            guard !old.isEmpty else { return }
            selection = .singleItem()
            vaultId = unlockedVaults.first?.id ?? ""
        }
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
                    RemoteBrowser(connectionId: connectionId, selection: $selection,
                                  vaultHint: "Vault – already encrypted")
                        .frame(height: 250)
                }
                Text(sourceSummary)
                    .font(.callout)
                    .foregroundStyle(selection.target == nil ? .secondary : .primary)
                    .lineLimit(1).truncationMode(.middle)
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
                LabeledContent("Folder inside the Vault") {
                    TextField("Folder inside the Vault", text: $subPath, prompt: Text("Top level"))
                        .labelsHidden()
                        .textFieldStyle(.roundedBorder)
                        .multilineTextAlignment(.leading)
                }
            }
            Toggle("Ignore Pause Rules (e.g. Studio Mode)", isOn: $ignorePauseRules)
            Text("The original is only deleted after the encrypted copy has been verified and you confirm.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var unlockedVaults: [Vault] {
        model.vaults.filter { $0.unlocked && $0.connectionId == connectionId }
    }

    private var sourceSummary: String {
        guard let target = selection.target else {
            return String(localized: "Select one folder or one file.")
        }
        if let file = target.files?.first {
            return String(localized: "Encrypts the file “\(file)”.")
        }
        return String(localized: "Encrypts the folder “\(target.remotePath)”.")
    }

    private var canStart: Bool {
        guard !busy, !connectionId.isEmpty, !draft.path.isEmpty || selection.target != nil else { return false }
        switch targetKind {
        case .newVault:
            return !vaultName.trimmingCharacters(in: .whitespaces).isEmpty && NewPasswordFields.isValid(password, repeated)
        case .existingVault:
            return unlockedVaults.contains { $0.id == vaultId }
        }
    }

    private func progressView(_ jobId: String) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if let recoveryKey {
                let vault = model.vaults.first { $0.id == targetVaultId }
                let name = vault?.name ?? vaultName.trimmingCharacters(in: .whitespaces)
                GroupBox(String(localized: "Recovery Key for “\(name)”")) {
                    RecoveryKeyPanel(recoveryKey: recoveryKey, vaultName: name,
                                     location: vault.map { Format.remote(model.connectionName($0.connectionId), $0.vaultPath) },
                                     confirmed: $keyStored)
                        .padding(4)
                }
            }
            let status = migration?.status ?? "queued"
            HStack(spacing: 10) {
                if status == "queued" { ProgressView().controlSize(.small) }
                Text(statusText(status)).font(.headline)
            }
            if status == "running", let migration {
                MigrationProgress(migration: migration)
            }
            if let mismatches = migration?.mismatches, !mismatches.isEmpty {
                Text("Files that differ:").font(.callout)
                List(mismatches, id: \.self) { Text($0) }.frame(height: 120)
                Text("The encrypted copy stays in the Vault as it is. Encrypt again to copy the differing files once more; the original is only deleted after a successful verification.")
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let failure = migration?.error, !failure.isEmpty {
                // statusText above says what happened; this is rclone's own untranslated reason.
                DetailText(failure)
            }
            if migration?.isActive ?? true {
                Text("You can close this window; the job continues in the background.")
                    .font(.caption).foregroundStyle(.secondary)
            }
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
        case "canceled": return String(localized: "Stopped. The original was kept.")
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
        if let id = draft.vaultId, unlockedVaults.contains(where: { $0.id == id }) {
            // "Encrypt Existing Files Into This Vault…" targets exactly that Vault.
            targetKind = .existingVault
            vaultId = id
        } else if let vault = unlockedVaults.first {
            vaultId = vault.id
        }
    }

    private func start() {
        if draft.path.isEmpty, let target = selection.target {
            if let file = target.files?.first {
                sourcePath = target.remotePath.isEmpty ? file : "\(target.remotePath)/\(file)"
                isDir = false
            } else {
                sourcePath = target.remotePath
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
                targetVaultId = result.vaultId
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

    private func cancelJob() {
        guard let jobId else { return }
        model.perform {
            try await model.client.cancelMigration(jobId: jobId)
        }
    }
}

/// Bytes and files of a running encryption job.
struct MigrationProgress: View {
    let migration: VaultMigrationEvent

    var body: some View {
        ProgressView(value: migration.fraction) {
            EmptyView()
        } currentValueLabel: {
            Text(text).font(.caption)
        }
    }

    private var text: String {
        let bytes = migration.fraction == nil
            ? String(localized: "\(Format.bytes(migration.bytes)) transferred")
            : String(localized: "\(Format.bytes(migration.bytes)) of \(Format.bytes(migration.totalBytes))")
        return bytes + " · " + String(localized: "\(migration.transfers) files")
    }
}

#if DEBUG
// MARK: - Snapshot seams

extension EncryptSheet {
    /// Targets an existing Vault, or shows a started job (`model.migrations[jobId]`) with an optional
    /// Recovery Key (`--export-snapshots`).
    static func snapshot(draft: EncryptDraft, existingVault: Bool = false, jobId: String? = nil,
                         recoveryKey: String? = nil) -> EncryptSheet
    {
        var sheet = EncryptSheet(draft: draft)
        if existingVault { sheet._targetKind = State(initialValue: .existingVault) }
        sheet._jobId = State(initialValue: jobId)
        sheet._recoveryKey = State(initialValue: recoveryKey)
        return sheet
    }
}
#endif
