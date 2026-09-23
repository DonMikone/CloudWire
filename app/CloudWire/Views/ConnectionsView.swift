import CloudWireKit
import SwiftUI

struct ConnectionsView: View {
    @Environment(AppModel.self) private var model
    @State private var editing: Connection?
    @State private var deleting: Connection?
    @State private var testing: Set<String> = []
    @State private var testResult: AlertContent?

    var body: some View {
        @Bindable var model = model
        SectionScaffold(title: String(localized: "Connections"),
                        subtitle: String(localized: "Cloud accounts CloudWire can use.")) {
            Button {
                model.showAddConnection = true
            } label: {
                Label("Add Connection", systemImage: "plus")
            }
        } content: {
            if model.remoteConnections.isEmpty {
                ContentUnavailableView {
                    Label("No Connections", systemImage: "network")
                } description: {
                    Text("Add your Nextcloud or any other cloud supported by rclone.")
                } actions: {
                    Button("Add Connection") { model.showAddConnection = true }
                        .buttonStyle(.borderedProminent)
                }
            } else {
                List {
                    ForEach(model.remoteConnections) { connection in
                        row(connection)
                    }
                }
                .listStyle(.inset(alternatesRowBackgrounds: false))
            }
        }
        .sheet(isPresented: $model.showAddConnection) {
            AddConnectionSheet()
        }
        .sheet(item: $editing) { connection in
            EditConnectionSheet(connection: connection)
        }
        .confirmationDialog(
            String(localized: "Delete “\(deleting?.name ?? "")”?"),
            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
            presenting: deleting
        ) { connection in
            Button("Delete Connection", role: .destructive) { delete(connection) }
        } message: { _ in
            Text("CloudWire forgets this account. Files in the cloud are not touched.")
        }
        .alert(item: $testResult) { result in
            Alert(title: Text(result.title), message: Text(result.message), dismissButton: .default(Text("OK")))
        }
    }

    private func row(_ connection: Connection) -> some View {
        HStack(spacing: 12) {
            Image(systemName: connection.isNextcloudLike ? "cloud.fill" : "cloud")
                .font(.title2)
                .foregroundStyle(Color.accentColor)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text(connection.name).font(.headline)
                Text(subtitle(connection)).font(.callout).foregroundStyle(.secondary)
            }
            Spacer()
            if testing.contains(connection.id) {
                ProgressView().controlSize(.small)
            }
            Button("Test") { test(connection) }
                .disabled(testing.contains(connection.id))
            Button("Edit…") { editing = connection }
            Button(role: .destructive) {
                deleting = connection
            } label: {
                Image(systemName: "trash")
            }
            .help(Text("Delete"))
        }
        .padding(.vertical, 6)
    }

    private func subtitle(_ connection: Connection) -> String {
        if connection.isNextcloudLike {
            let vendor = connection.vendor == "owncloud" ? "ownCloud" : "Nextcloud"
            return [vendor, connection.user, connection.serverURL].filter { !$0.isEmpty }.joined(separator: " · ")
        }
        return connection.provider
    }

    private func test(_ connection: Connection) {
        testing.insert(connection.id)
        Task {
            defer { testing.remove(connection.id) }
            do {
                let result = try await model.client.testConnection(id: connection.id)
                var message = String(localized: "CloudWire can reach “\(connection.name)”.")
                if let used = result.used, let total = result.total, total > 0 {
                    message += "\n" + String(localized: "\(Format.bytes(used)) of \(Format.bytes(total)) used.")
                }
                testResult = AlertContent(title: String(localized: "Connection works"), message: message)
            } catch {
                model.present(error)
            }
        }
    }

    private func delete(_ connection: Connection) {
        model.perform {
            try await model.client.deleteConnection(id: connection.id)
            model.connections.removeAll { $0.id == connection.id }
        }
    }
}

// MARK: - Add

struct AddConnectionSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    private enum Step: Equatable {
        case choose
        case nextcloud
        case provider(String)
        case question(ConfigStep)
        case waiting(connectionId: String)
        case browserLogin(flowId: String)
    }

    @State private var step: Step = .choose
    @State private var providers: [RcloneProvider] = []
    @State private var search = ""
    @State private var loadError: String?

    // Shared fields
    @State private var name = ""
    @State private var busy = false
    @State private var error: String?

    // Nextcloud
    @State private var serverURL = ""
    @State private var manual = false
    @State private var user = ""
    @State private var appPassword = ""

    // Generic provider
    @State private var form = OptionFormModel(options: [])
    @State private var detail: FormDetail = .simple
    @State private var questionForm = OptionFormModel(options: [])
    @State private var setup = SetupTracker()

    var body: some View {
        SheetScaffold(title: title, width: 620) {
            content
            if let error { InlineError(message: error) }
        } buttons: {
            buttons
        }
        .task { await loadProviders() }
        .onDisappear {
            setup.dismissed = true
            if let connectionId = setup.connectionId { cancelSetup(connectionId) }
        }
        .onChange(of: model.configStepEvents) { _, events in
            if case .waiting(let connectionId) = step, let event = events[connectionId] {
                model.configStepEvents[connectionId] = nil
                handle(event)
            }
        }
        .onChange(of: model.loginEvents) { _, events in
            if case .browserLogin(let flowId) = step, let event = events[flowId] {
                model.loginEvents[flowId] = nil
                busy = false
                if event.succeeded {
                    finish()
                } else {
                    error = event.error ?? String(localized: "The login did not complete.")
                    step = .nextcloud
                }
            }
        }
    }

    private var title: String {
        switch step {
        case .choose: return String(localized: "Add Connection")
        case .nextcloud: return String(localized: "Add Nextcloud")
        case .provider(let name): return String(localized: "Add \(providerDescription(name))")
        case .question: return String(localized: "Additional Setup")
        case .waiting, .browserLogin: return String(localized: "Waiting for Your Browser")
        }
    }

    @ViewBuilder
    private var content: some View {
        switch step {
        case .choose:
            chooseProvider
        case .nextcloud:
            nextcloudForm
        case .provider:
            providerForm
        case .question(let configStep):
            questionView(configStep)
        case .waiting:
            waitingView(String(localized: "Complete the sign-in in your browser. CloudWire continues automatically."))
        case .browserLogin:
            waitingView(String(localized: "Log in to your Nextcloud in the browser and grant access. CloudWire continues automatically."))
        }
    }

    @ViewBuilder
    private var buttons: some View {
        switch step {
        case .choose:
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
        case .nextcloud:
            Button("Back") { step = .choose; error = nil }
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            if manual {
                Button("Connect") { connectNextcloudManually() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(busy || serverURL.isEmpty || user.isEmpty || appPassword.isEmpty)
            } else {
                Button("Log In with Browser") { startBrowserLogin() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(busy || serverURL.isEmpty)
            }
        case .provider(let providerName):
            Button("Back") { step = .choose; error = nil }
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Create") { create(providerName) }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || name.trimmingCharacters(in: .whitespaces).isEmpty || !form.missingRequired().isEmpty)
        case .question(let configStep):
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Continue") { answer(configStep) }
                .keyboardShortcut(.defaultAction)
                .disabled(busy)
        case .waiting:
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
        case .browserLogin(let flowId):
            Button("Cancel Login") {
                let client = model.client
                Task { try? await client.nextcloudLoginCancel(flowId: flowId) }
                step = .nextcloud
                busy = false
            }
        }
    }

    // MARK: Choose

    private var filteredProviders: [RcloneProvider] {
        let visible = providers.filter { !$0.hide && $0.name != "crypt" }
        guard !search.isEmpty else { return visible }
        return visible.filter {
            $0.name.localizedCaseInsensitiveContains(search) || $0.description.localizedCaseInsensitiveContains(search)
        }
    }

    private var chooseProvider: some View {
        VStack(alignment: .leading, spacing: 10) {
            TextField("Search providers", text: $search)
                .textFieldStyle(.roundedBorder)
            List {
                if search.isEmpty || "nextcloud owncloud".localizedCaseInsensitiveContains(search) {
                    Button {
                        name = ""
                        step = .nextcloud
                    } label: {
                        HStack {
                            Image(systemName: "cloud.fill").foregroundStyle(Color.accentColor)
                            VStack(alignment: .leading) {
                                Text("Nextcloud").font(.headline)
                                Text("Recommended: log in with your browser, sharing included")
                                    .font(.caption).foregroundStyle(.secondary)
                            }
                            Spacer()
                            Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                ForEach(filteredProviders) { provider in
                    Button {
                        select(provider)
                    } label: {
                        HStack {
                            VStack(alignment: .leading) {
                                Text(provider.description.isEmpty ? provider.name : provider.description)
                                Text(provider.name).font(.caption).foregroundStyle(.secondary)
                            }
                            Spacer()
                            Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
            .frame(height: 360)
            .overlay {
                if providers.isEmpty {
                    if let loadError { InlineError(message: loadError) } else { ProgressView() }
                }
            }
        }
    }

    private func select(_ provider: RcloneProvider) {
        form = OptionFormModel(options: provider.options, hideContext: .configurator)
        name = ""
        detail = .simple
        error = nil
        step = .provider(provider.name)
    }

    private func providerDescription(_ name: String) -> String {
        providers.first { $0.name == name }.map { $0.description.isEmpty ? $0.name : $0.description } ?? name
    }

    // MARK: Nextcloud

    private var nextcloudForm: some View {
        Form {
            TextField("Name", text: $name, prompt: Text("Optional, e.g. Nextcloud"))
            TextField("Server address", text: $serverURL, prompt: Text("cloud.example.com"))
            Picker("Sign-in", selection: $manual) {
                Text("In the browser").tag(false)
                Text("Manually with an app password").tag(true)
            }
            .pickerStyle(.radioGroup)
            if manual {
                TextField("User name", text: $user)
                SecureField("App password", text: $appPassword)
                Text("Create an app password in Nextcloud under Settings > Security > Devices & sessions.")
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                Text("Your browser opens the Nextcloud login. CloudWire only stores an app password, never your account password.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .frame(minHeight: 260)
    }

    private func startBrowserLogin() {
        busy = true
        error = nil
        let serverURL = serverURL
        let name = name.isEmpty ? nil : name
        Task {
            do {
                let start = try await model.client.nextcloudLoginStart(serverURL: serverURL, name: name)
                step = .browserLogin(flowId: start.flowId)
                if let loginEvent = model.loginEvents[start.flowId] {
                    model.loginEvents[start.flowId] = nil
                    if loginEvent.succeeded { finish() } else { error = loginEvent.error; step = .nextcloud }
                    return
                }
                if let url = URL(string: start.loginURL) {
                    NSWorkspace.shared.open(url)
                }
            } catch {
                busy = false
                self.error = ErrorText.alert(for: error).message
            }
        }
    }

    private func connectNextcloudManually() {
        busy = true
        error = nil
        let (serverURL, user, appPassword) = (serverURL, user, appPassword)
        let name = name.isEmpty ? nil : name
        Task {
            do {
                _ = try await model.client.nextcloudManual(serverURL: serverURL, user: user, appPassword: appPassword,
                                                           name: name)
                finish()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }

    // MARK: Generic provider

    private var providerForm: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                TextField("Name", text: $name, prompt: Text("e.g. Google Drive"))
                    .textFieldStyle(.roundedBorder)
                FormDetailPicker(selection: $detail)
            }
            Form {
                OptionFormView(form: $form, options: form.visibleOptions(advanced: detail == .advanced))
            }
            .formStyle(.grouped)
            .frame(height: 380)
        }
    }

    private func create(_ providerName: String) {
        do {
            let parameters = try form.parameters()
            busy = true
            error = nil
            let name = name.trimmingCharacters(in: .whitespaces)
            Task {
                do {
                    let result = try await model.client.createConnection(name: name, provider: providerName,
                                                                         parameters: parameters)
                    handle(result)
                } catch {
                    busy = false
                    let alert = ErrorText.alert(for: error)
                    self.error = "\(alert.title): \(alert.message)"
                }
            }
        } catch {
            self.error = ErrorText.alert(for: error).message
        }
    }

    // MARK: Config state machine

    private func handle(_ configStep: ConfigStep) {
        guard !setup.dismissed || configStep.done else {
            // The sheet closed while the Core was still answering.
            cancelSetup(configStep.connectionId)
            return
        }
        busy = false
        setup.connectionId = configStep.done ? nil : configStep.connectionId
        if configStep.done {
            finish()
        } else if configStep.pending {
            step = .waiting(connectionId: configStep.connectionId)
            // The event may already have arrived.
            if let event = model.configStepEvents[configStep.connectionId] {
                model.configStepEvents[configStep.connectionId] = nil
                handle(event)
            }
        } else if let option = configStep.option {
            questionForm = OptionFormModel(options: [option], hideContext: .configurator)
            error = configStep.error.isEmpty ? nil : configStep.error
            step = .question(configStep)
        } else {
            error = configStep.error.isEmpty ? String(localized: "The provider setup stopped unexpectedly.") : configStep.error
        }
    }

    private func questionView(_ configStep: ConfigStep) -> some View {
        Form {
            if let option = configStep.option {
                OptionFieldRow(form: $questionForm, option: option)
                if !option.help.isEmpty {
                    Text(option.help).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
        }
        .formStyle(.grouped)
        .frame(minHeight: 180)
    }

    private func answer(_ configStep: ConfigStep) {
        guard let option = configStep.option else { return }
        let result: String
        do {
            result = try questionForm.normalizedValue(of: option)
        } catch {
            self.error = ErrorText.alert(for: error).message
            return
        }
        busy = true
        error = nil
        Task {
            do {
                let next = try await model.client.continueConnection(connectionId: configStep.connectionId,
                                                                     state: configStep.state, result: result)
                handle(next)
            } catch {
                busy = false
                self.error = ErrorText.alert(for: error).message
            }
        }
    }

    private func waitingView(_ text: String) -> some View {
        HStack(spacing: 12) {
            ProgressView().controlSize(.small)
            Text(text).fixedSize(horizontal: false, vertical: true)
        }
        .padding(.vertical, 20)
    }

    private func loadProviders() async {
        do {
            providers = try await model.loadProviders()
        } catch {
            loadError = ErrorText.alert(for: error).message
        }
    }

    /// Abandons an unfinished rclone config state machine (best effort).
    private func cancelSetup(_ connectionId: String) {
        setup.connectionId = nil
        let client = model.client
        Task { try? await client.cancelConnectionSetup(connectionId: connectionId) }
    }

    private func finish() {
        Task { await model.refreshConnections() }
        UserDefaults.standard.set(true, forKey: "hasConnection")
        if !setup.dismissed { dismiss() }
    }
}

/// The Core-side setup of an `AddConnectionSheet`, kept in a reference so tasks that finish after
/// the sheet closed still see it.
@MainActor
private final class SetupTracker {
    /// Connection id of a setup that has started and is not done.
    var connectionId: String?
    var dismissed = false
}

// MARK: - Edit

struct EditConnectionSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let connection: Connection

    @State private var name = ""
    @State private var form = OptionFormModel(options: [])
    @State private var detail: FormDetail = .simple
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SheetScaffold(title: String(localized: "Edit “\(connection.name)”"), width: 620) {
            HStack {
                TextField("Name", text: $name).textFieldStyle(.roundedBorder)
                FormDetailPicker(selection: $detail)
            }
            Form {
                OptionFormView(form: $form, options: form.visibleOptions(advanced: detail == .advanced))
            }
            .formStyle(.grouped)
            .frame(height: 380)
            Text("Leave password fields empty to keep the stored value.")
                .font(.caption).foregroundStyle(.secondary)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Save") { save() }.keyboardShortcut(.defaultAction).disabled(busy || name.isEmpty)
        }
        .task {
            name = connection.name
            if let provider = try? await model.loadProviders().first(where: { $0.name == connection.provider }) {
                form = OptionFormModel(options: provider.options, hideContext: .configurator,
                                       initialValues: connection.parameters)
            }
        }
    }

    private func save() {
        let changed: [String: String]
        do {
            // Includes options reset to rclone's default, which `parameters()` alone would drop.
            changed = try form.changedParameters(from: connection.parameters)
        } catch {
            self.error = ErrorText.alert(for: error).message
            return
        }
        busy = true
        let newName = name == connection.name ? nil : name
        Task {
            do {
                let updated = try await model.client.updateConnection(
                    id: connection.id, name: newName, parameters: changed.isEmpty ? nil : changed)
                model.updated(updated)
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}
