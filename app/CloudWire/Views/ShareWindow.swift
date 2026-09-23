import CloudWireKit
import SwiftUI

/// Public link permission presets (Nextcloud).
enum LinkPermission: String, CaseIterable, Identifiable {
    case readOnly, uploadEdit, fileDrop
    var id: String { rawValue }

    var title: String {
        switch self {
        case .readOnly: return String(localized: "Read only")
        case .uploadEdit: return String(localized: "Upload & edit")
        case .fileDrop: return String(localized: "File drop (upload only)")
        }
    }

    func permissions(isDir: Bool) -> Int {
        switch self {
        case .readOnly: return 1
        case .uploadEdit: return isDir ? 15 : 3
        case .fileDrop: return 4
        }
    }

    static func from(_ permissions: Int) -> LinkPermission {
        switch permissions {
        case 4: return .fileDrop
        case 3, 15: return .uploadEdit
        default: return .readOnly
        }
    }
}

/// Expiry quick picks: none, 1, 7, 30 days, or a custom date.
enum ExpiryChoice: Hashable {
    case none
    case days(Int)
    case custom

    static let quickPicks: [ExpiryChoice] = [.none, .days(1), .days(7), .days(30), .custom]

    /// Quick picks the server policy allows: no "Never" and no longer periods when expiry is enforced.
    static func choices(for policy: SharePolicy) -> [ExpiryChoice] {
        guard policy.expireDateEnforced, policy.expireDateDays > 0 else { return quickPicks }
        return quickPicks.filter {
            switch $0 {
            case .none: return false
            case .days(let days): return days <= policy.expireDateDays
            case .custom: return true
            }
        }
    }

    /// Latest custom date the server policy allows.
    static func maxDate(for policy: SharePolicy) -> Date {
        if policy.expireDateEnforced, policy.expireDateDays > 0 {
            return Calendar.current.date(byAdding: .day, value: policy.expireDateDays, to: Date()) ?? Date()
        }
        return Date.distantFuture
    }

    var title: String {
        switch self {
        case .none: return String(localized: "Never")
        case .days(1): return String(localized: "1 day")
        case .days(let days): return String(localized: "\(days) days")
        case .custom: return String(localized: "Custom date")
        }
    }

    /// `YYYY-MM-DD`, or nil for no expiry.
    func dateString(custom: Date) -> String? {
        switch self {
        case .none: return nil
        case .days(let days):
            return Self.format(Calendar.current.date(byAdding: .day, value: days, to: Date()) ?? Date())
        case .custom: return Self.format(custom)
        }
    }

    static func format(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter.string(from: date)
    }

    static func parse(_ text: String) -> Date? {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter.date(from: text)
    }
}

enum ShareMode: String, CaseIterable, Identifiable {
    case publicLink, people, email, internalLink
    var id: String { rawValue }

    var title: String {
        switch self {
        case .publicLink: return String(localized: "Public Link")
        case .people: return String(localized: "People & Groups")
        case .email: return String(localized: "Email")
        case .internalLink: return String(localized: "Internal Link")
        }
    }
}

/// State of the people & groups search below its field.
private enum ShareeSearch: Equatable {
    case idle, searching, done
    case failed(String)
}

struct ShareWindow: View {
    @Environment(AppModel.self) private var model
    let target: ShareTarget

    @State private var capabilities: ShareCapabilities?
    @State private var policy = SharePolicy()
    @State private var shares: [Share] = []
    @State private var mode: ShareMode = .publicLink
    @State private var error: String?
    @State private var busy = false
    @State private var editing: Share?
    @State private var deleting: Share?
    /// Info after a delete that left the link active at the provider.
    @State private var notice: String?
    /// Manage mode: the create form is collapsed below the existing shares.
    @State private var createExpanded = false

    // Public link
    @State private var usePassword = false
    @State private var password = ""
    @State private var expiry: ExpiryChoice = .none
    @State private var customDate = Date().addingTimeInterval(7 * 86400)
    @State private var permission: LinkPermission = .readOnly
    @State private var hideDownload = false
    @State private var label = ""
    @State private var note = ""
    @State private var createdURL: String?

    // People & groups
    @State private var search = ""
    @State private var sharees: [Sharee] = []
    @State private var sharee: Sharee?
    @State private var searchTask: Task<Void, Never>?
    @State private var shareeSearch: ShareeSearch = .idle
    @State private var canEdit = false
    @State private var canCreate = false
    @State private var canDelete = false
    @State private var canReshare = false

    // Email
    @State private var email = ""
    @State private var emailExpiry: ExpiryChoice = .none
    @State private var emailCustomDate = Date().addingTimeInterval(7 * 86400)
    @State private var emailNote = ""

    // Internal link
    @State private var internalURL: String?
    @State private var internalError: String?

    private var isVault: Bool { model.vault(forConnection: target.connectionId) != nil }

    private var displayName: String { target.name.isEmpty ? model.connectionName(target.connectionId) : target.name }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            header
            if isVault || capabilities?.reason == "vault" {
                vaultNotice
            } else if let capabilities {
                let modes = availableModes(capabilities)
                if modes.isEmpty {
                    Card {
                        Text("This connection does not support sharing.").foregroundStyle(.secondary)
                    }
                    messages
                    if capabilities.manage { existingShares }
                } else if target.manage {
                    existingShares
                    messages
                    DisclosureGroup("Create New Share", isExpanded: $createExpanded) {
                        createForm(modes).padding(.top, 8)
                    }
                } else {
                    createForm(modes)
                    messages
                    existingShares
                }
            } else if let error {
                InlineError(message: error)
                Button("Try Again") {
                    self.error = nil
                    Task { await load() }
                }
            } else {
                ProgressView().frame(maxWidth: .infinity)
            }
        }
        .padding(20)
        .frame(width: 560, alignment: .topLeading)
        .navigationTitle(String(localized: "Share “\(displayName)”"))
        .task(id: target) { await load() }
        .onChange(of: search) { _, _ in searchSharees() }
        .onChange(of: mode) { _, _ in error = nil }
        .sheet(item: $editing) { share in
            ShareEditSheet(share: share, connectionId: target.connectionId, isDir: target.isDir, policy: policy) { updated in
                if let index = shares.firstIndex(where: { $0.id == updated.id }) { shares[index] = updated }
            }
        }
        .confirmationDialog(deleting?.deleteTitle ?? String(localized: "Delete this share?"),
                            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            presenting: deleting) { share in
            Button("Delete Share", role: .destructive) { delete(share) }
        } message: { _ in
            Text("People using this share lose access.")
        }
    }

    private var header: some View {
        HStack(spacing: 12) {
            Image(systemName: target.isDir ? "folder.fill" : "doc.fill")
                .font(.largeTitle)
                .foregroundStyle(Color.accentColor)
            VStack(alignment: .leading, spacing: 2) {
                Text(displayName)
                    .font(.title3.weight(.semibold))
                Text(Format.remote(model.connectionName(target.connectionId), target.path))
                    .font(.callout).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
        }
    }

    private var vaultNotice: some View {
        Card {
            Label {
                Text("Sharing is not available inside a Vault. Its files are stored encrypted in the cloud, so a link would only reveal unreadable data.")
            } icon: {
                Image(systemName: "lock.shield")
            }
        }
    }

    private func availableModes(_ capabilities: ShareCapabilities) -> [ShareMode] {
        var modes: [ShareMode] = []
        if capabilities.publicLink { modes.append(.publicLink) }
        if capabilities.userShare { modes.append(.people) }
        if capabilities.emailShare { modes.append(.email) }
        if capabilities.internalLink { modes.append(.internalLink) }
        return modes
    }

    /// Mode picker and the form of the selected mode.
    @ViewBuilder
    private func createForm(_ modes: [ShareMode]) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            if modes.count > 1 {
                Picker("", selection: $mode) {
                    ForEach(modes) { Text($0.title).tag($0) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .frame(maxWidth: .infinity)
            }
            Card { modeForm }
        }
    }

    @ViewBuilder
    private var messages: some View {
        if let notice { ShareNotice(message: notice) }
        if let error { InlineError(message: error) }
    }

    /// True for Nextcloud/ownCloud (full OCS sharing); rclone providers only get a plain public link.
    private var isNextcloud: Bool { capabilities?.userShare == true || capabilities?.internalLink == true }

    @ViewBuilder
    private var modeForm: some View {
        switch mode {
        case .publicLink: publicLinkForm
        case .people: peopleForm
        case .email: emailForm
        case .internalLink: internalLinkForm
        }
    }

    // MARK: Public link

    private var publicLinkForm: some View {
        VStack(alignment: .leading, spacing: 10) {
            if isNextcloud {
                Toggle("Protect with password", isOn: $usePassword)
                    .disabled(policy.passwordEnforced)
                if usePassword {
                    HStack {
                        PasswordField(title: "Password", text: $password)
                        Button("Generate") { password = Self.generatePassword() }
                    }
                }
                if policy.passwordEnforced {
                    Text("The server requires a password for public links.").font(.caption).foregroundStyle(.secondary)
                }
            }
            if capabilities?.linkExpiry == true {
                expiryPicker(choice: $expiry, date: $customDate)
                if policy.expireDateEnforced {
                    Text("The server requires links to expire within \(policy.expireDateDays) days.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            } else {
                Text("Links of this provider don't expire.").font(.caption).foregroundStyle(.secondary)
            }
            if isNextcloud {
                Picker("Permissions", selection: $permission) {
                    ForEach(LinkPermission.allCases.filter { target.isDir || $0 != .fileDrop }) { Text($0.title).tag($0) }
                }
                .fixedSize()
                Toggle("Hide download", isOn: $hideDownload)
                TextField("Label", text: $label, prompt: Text("Label (optional)"))
                    .textFieldStyle(.roundedBorder)
                TextField("Note to recipient", text: $note, prompt: Text("Note to recipient (optional)"))
                    .textFieldStyle(.roundedBorder)
            }
            HStack {
                Button(createdURL == nil ? String(localized: "Create Link") : String(localized: "Create Another Link")) {
                    createPublicLink()
                }
                .buttonStyle(.borderedProminent)
                .disabled(busy || (usePassword && password.isEmpty))
                if busy { ProgressView().controlSize(.small) }
                Spacer()
            }
            if let createdURL {
                // `createPublicLink` already copied it.
                LinkRow(url: createdURL, initiallyCopied: true).id(createdURL)
            }
        }
    }

    private func expiryPicker(choice: Binding<ExpiryChoice>, date: Binding<Date>) -> some View {
        HStack {
            Picker("Expires", selection: choice) {
                ForEach(ExpiryChoice.choices(for: policy), id: \.self) { Text($0.title).tag($0) }
            }
            .fixedSize()
            if choice.wrappedValue == .custom {
                DatePicker("", selection: date, in: Date()...ExpiryChoice.maxDate(for: policy), displayedComponents: .date)
                    .labelsHidden()
            }
        }
    }

    private func createPublicLink() {
        let request = CoreClient.ShareRequest(
            kind: .publicLink,
            password: isNextcloud && usePassword ? password : nil,
            expireDate: capabilities?.linkExpiry == true ? expiry.dateString(custom: customDate) : nil,
            permissions: isNextcloud ? permission.permissions(isDir: target.isDir) : nil,
            hideDownload: isNextcloud ? hideDownload : nil,
            label: isNextcloud && !label.isEmpty ? label : nil,
            note: isNextcloud && !note.isEmpty ? note : nil)
        create(request) { share in
            createdURL = share.url
            // Fresh form for the next link; the server's password rule stays.
            label = ""
            note = ""
            password = ""
            usePassword = policy.passwordEnforced
            if !share.url.isEmpty {
                copyToPasteboard(share.url)
                if model.settings.notifications.linkCopied { NotificationManager.shared.postLinkCopied(share.url) }
            }
        }
    }

    static func generatePassword() -> String {
        let alphabet = Array("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789")
        var generator = SystemRandomNumberGenerator()
        let groups = (0..<4).map { _ in String((0..<5).map { _ in alphabet.randomElement(using: &generator)! }) }
        return groups.joined(separator: "-")
    }

    // MARK: People & groups

    private var peopleForm: some View {
        VStack(alignment: .leading, spacing: 10) {
            TextField("Search users and groups", text: $search)
                .textFieldStyle(.roundedBorder)
            if shareeSearch == .searching {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small)
                    Text("Searching…").font(.callout).foregroundStyle(.secondary)
                }
            } else if case .failed(let message) = shareeSearch {
                InlineError(message: message)
            } else if shareeSearch == .done && sharees.isEmpty {
                Text("No matching users or groups.").font(.callout).foregroundStyle(.secondary)
            }
            if !sharees.isEmpty {
                List(sharees, selection: Binding(get: { sharee?.id }, set: { id in sharee = sharees.first { $0.id == id } })) { item in
                    Label(item.label, systemImage: item.shareType == 1 ? "person.3" : "person")
                        .tag(item.id)
                }
                .frame(height: 120)
                .listStyle(.bordered)
            }
            Text("Recipients can always view. Also allow:").font(.callout)
            HStack(spacing: 16) {
                Toggle("Edit", isOn: $canEdit)
                if target.isDir {
                    Toggle("Create", isOn: $canCreate)
                    Toggle("Delete", isOn: $canDelete)
                }
                Toggle("Reshare", isOn: $canReshare)
            }
            .toggleStyle(.checkbox)
            HStack {
                Button(sharee.map { String(localized: "Share with \($0.label)") } ?? String(localized: "Share")) {
                    sharePeople()
                }
                .buttonStyle(.borderedProminent)
                .disabled(busy || sharee == nil)
                if busy { ProgressView().controlSize(.small) }
            }
        }
    }

    private var userPermissions: Int {
        var value = 1
        if canEdit { value |= 2 }
        if target.isDir && canCreate { value |= 4 }
        if target.isDir && canDelete { value |= 8 }
        if canReshare { value |= 16 }
        return value
    }

    private func searchSharees() {
        searchTask?.cancel()
        let query = search.trimmingCharacters(in: .whitespaces)
        guard query.count >= 2 else {
            sharees = []
            sharee = nil
            shareeSearch = .idle
            return
        }
        let connectionId = target.connectionId
        let itemType = target.isDir ? "folder" : "file"
        searchTask = Task {
            do {
                try await Task.sleep(for: .milliseconds(300))
            } catch {
                return
            }
            shareeSearch = .searching
            do {
                let found = try await model.client.searchSharees(connectionId: connectionId, search: query,
                                                                 itemType: itemType)
                guard !Task.isCancelled, query == search.trimmingCharacters(in: .whitespaces) else { return }
                sharees = found.filter { $0.shareType != 4 }
                if let sharee, !sharees.contains(sharee) { self.sharee = nil }
                shareeSearch = .done
            } catch {
                guard !Task.isCancelled, query == search.trimmingCharacters(in: .whitespaces) else { return }
                sharees = []
                sharee = nil
                let alert = ErrorText.alert(for: error)
                shareeSearch = .failed("\(alert.title): \(alert.message)")
            }
        }
    }

    private func sharePeople() {
        guard let sharee else { return }
        let request = CoreClient.ShareRequest(kind: sharee.shareType == 1 ? .group : .user,
                                              permissions: userPermissions, shareWith: sharee.shareWith)
        create(request) { _ in
            self.sharee = nil
            search = ""
        }
    }

    // MARK: Email

    private var emailForm: some View {
        VStack(alignment: .leading, spacing: 10) {
            TextField("Email address", text: $email, prompt: Text("name@example.com"))
                .textFieldStyle(.roundedBorder)
            expiryPicker(choice: $emailExpiry, date: $emailCustomDate)
            TextField("Note to recipient", text: $emailNote, prompt: Text("Note to recipient (optional)"))
                .textFieldStyle(.roundedBorder)
            Text("The server sends the link by email.").font(.caption).foregroundStyle(.secondary)
            HStack {
                Button("Send Link") {
                    let request = CoreClient.ShareRequest(
                        kind: .email, expireDate: emailExpiry.dateString(custom: emailCustomDate), permissions: 1,
                        note: emailNote.isEmpty ? nil : emailNote, shareWith: email.trimmingCharacters(in: .whitespaces),
                        sendMail: true)
                    create(request) { _ in email = "" }
                }
                .buttonStyle(.borderedProminent)
                .disabled(busy || !email.contains("@"))
                if busy { ProgressView().controlSize(.small) }
            }
        }
    }

    // MARK: Internal link

    private var internalLinkForm: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Only people who already have access to this item can open the internal link.")
                .font(.callout).foregroundStyle(.secondary)
            if let internalURL {
                LinkRow(url: internalURL)
            } else if let internalError {
                InlineError(message: internalError)
                Button("Try Again") { Task { await loadInternalLink() } }
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .task { await loadInternalLink() }
    }

    private func loadInternalLink() async {
        guard internalURL == nil else { return }
        internalError = nil
        do {
            internalURL = try await model.client.internalLink(connectionId: target.connectionId, path: target.path)
        } catch is CancellationError {
        } catch {
            let alert = ErrorText.alert(for: error)
            internalError = "\(alert.title): \(alert.message)"
        }
    }

    // MARK: Existing shares

    private var existingShares: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(target.manage ? String(localized: "Shares of this item") : String(localized: "Existing shares"))
                .font(.headline)
            if shares.isEmpty {
                Text("This item is not shared yet.").foregroundStyle(.secondary).font(.callout)
            } else {
                VStack(spacing: 6) {
                    ForEach(shares) { share in
                        ShareRow(share: share, onEdit: { editing = share }, onDelete: { deleting = share })
                    }
                }
            }
        }
    }

    // MARK: Actions

    private func load() async {
        do {
            let caps = try await model.loadCapabilities(connectionId: target.connectionId)
            capabilities = caps
            if caps.userShare || caps.internalLink {
                policy = (try? await model.loadPolicy(connectionId: target.connectionId)) ?? SharePolicy()
                if policy.passwordEnforced { usePassword = true }
                if policy.expireDateEnforced || policy.defaultExpireDate, policy.expireDateDays > 0 {
                    expiry = [1, 7, 30].contains(policy.expireDateDays) ? .days(policy.expireDateDays) : .custom
                    customDate = Calendar.current.date(byAdding: .day, value: policy.expireDateDays, to: Date()) ?? Date()
                    emailExpiry = expiry
                    emailCustomDate = customDate
                }
            }
            if let first = availableModes(caps).first, !availableModes(caps).contains(mode) { mode = first }
            if caps.manage || caps.publicLink {
                shares = try await model.loadShares(connectionId: target.connectionId, path: target.path)
            }
        } catch {
            let alert = ErrorText.alert(for: error)
            self.error = "\(alert.title): \(alert.message)"
        }
    }

    private func create(_ request: CoreClient.ShareRequest, then: @escaping (Share) -> Void) {
        busy = true
        error = nil
        notice = nil
        let connectionId = target.connectionId
        let path = target.path
        Task {
            defer { busy = false }
            do {
                let share = try await model.client.createShare(connectionId: connectionId, path: path, request: request)
                shares.insert(share, at: 0)
                then(share)
            } catch let failure as CoreError where failure.code == "share.serverPolicy" {
                if let serverPolicy = failure.policy {
                    policy = serverPolicy
                    if serverPolicy.passwordEnforced { usePassword = true }
                }
                error = "\(ErrorText.headline(for: failure)): \(ErrorText.detail(for: failure))"
            } catch {
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }

    private func delete(_ share: Share) {
        busy = true
        error = nil
        notice = nil
        let connectionId = target.connectionId
        Task {
            defer { busy = false }
            do {
                let result = try await model.client.deleteShare(connectionId: connectionId, id: share.id)
                shares.removeAll { $0.id == share.id }
                if result.remoteStillActive { notice = ShareNotice.stillActive }
            } catch {
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

/// Info (not an error) shown above or below the shares.
struct ShareNotice: View {
    let message: String

    /// A deleted rclone link the provider still serves.
    static var stillActive: String {
        String(localized: "Removed from CloudWire; still active at the provider – please remove it on the web.")
    }

    var body: some View {
        Label(message, systemImage: "exclamationmark.triangle")
            .foregroundStyle(.orange)
            .font(.callout)
            .fixedSize(horizontal: false, vertical: true)
    }
}

/// A URL with Copy and Open buttons.
struct LinkRow: View {
    let url: String
    @State private var copied: Bool

    /// `initiallyCopied`: the link was already put on the pasteboard automatically.
    init(url: String, initiallyCopied: Bool = false) {
        self.url = url
        _copied = State(initialValue: initiallyCopied)
    }

    var body: some View {
        HStack {
            Text(url)
                .font(.callout.monospaced())
                .lineLimit(1)
                .truncationMode(.middle)
                .textSelection(.enabled)
            Spacer()
            Button(copied ? "Copied" : "Copy") {
                copyToPasteboard(url)
                copied = true
            }
            if let link = URL(string: url) {
                Link(destination: link) {
                    Label("Open in Browser", systemImage: "safari").labelStyle(.iconOnly)
                }
                .help(Text("Open in Browser"))
            }
        }
        .padding(8)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
    }
}

/// One existing share with its details.
struct ShareRow: View {
    let share: Share
    var showsPath = false
    let onEdit: () -> Void
    let onDelete: () -> Void
    @State private var copied = false

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: share.kind.symbol).frame(width: 20).foregroundStyle(Color.accentColor)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).lineLimit(1).truncationMode(.middle)
                if !details.isEmpty {
                    Text(details).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer()
            if share.hasPassword {
                Image(systemName: "lock.fill").foregroundStyle(.secondary)
                    .help(Text("Password protected"))
                    .accessibilityLabel(Text("Password protected"))
            }
            if !share.url.isEmpty {
                Button {
                    copyToPasteboard(share.url)
                    copied = true
                } label: {
                    Label(copied ? "Copied" : "Copy Link", systemImage: copied ? "checkmark" : "doc.on.doc")
                        .labelStyle(.iconOnly)
                }
                .help(Text(copied ? "Copied" : "Copy Link"))
            }
            if share.kind != .link {
                Button("Edit…", action: onEdit)
            }
            Button(role: .destructive, action: onDelete) {
                Label("Delete", systemImage: "trash").labelStyle(.iconOnly)
            }
            .help(Text("Delete"))
        }
        .padding(8)
        .background(.quaternary.opacity(0.35), in: RoundedRectangle(cornerRadius: 8))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(title)
        .task(id: copied) {
            guard copied else { return }
            do {
                try await Task.sleep(for: .seconds(2))
                copied = false
            } catch {}
        }
    }

    private var title: String {
        var parts: [String] = []
        if showsPath { parts.append(share.path.isEmpty ? "/" : share.path) }
        parts.append(share.displayTitle)
        return parts.joined(separator: " · ")
    }

    private var details: String {
        // The kind is the title of unnamed shares without URL; don't repeat it.
        var parts = share.displayTitle == share.kind.label ? [] : [share.kind.label]
        if let expire = share.expireDate {
            let date = ExpiryChoice.parse(expire).map { $0.formatted(date: .abbreviated, time: .omitted) } ?? expire
            parts.append(String(localized: "expires \(date)"))
        }
        if share.hideDownload { parts.append(String(localized: "download hidden")) }
        return parts.joined(separator: " · ")
    }
}

extension Share {
    private var isLink: Bool { kind == .publicLink || kind == .link }

    /// Person, group or address the share is for; nil for links and unnamed shares.
    private var recipientName: String? {
        guard !isLink else { return nil }
        let name = shareWithDisplayName.isEmpty ? shareWith : shareWithDisplayName
        return name.isEmpty ? nil : name
    }

    /// Label of a link, else its URL without scheme; nil if it has neither.
    private var linkName: String? {
        if !label.isEmpty { return label }
        guard !url.isEmpty else { return nil }
        guard let scheme = url.range(of: "://") else { return url }
        return String(url[scheme.upperBound...])
    }

    /// Row title: recipient, link label or short URL, else the kind.
    var displayTitle: String { recipientName ?? linkName ?? kind.label }

    var editTitle: String {
        if let recipientName { return String(localized: "Edit Share with \(recipientName)") }
        if isLink, let linkName { return String(localized: "Edit Link “\(linkName)”") }
        return String(localized: "Edit Share")
    }

    var deleteTitle: String {
        if let recipientName { return String(localized: "Delete the share with \(recipientName)?") }
        if isLink, let linkName { return String(localized: "Delete the link “\(linkName)”?") }
        return String(localized: "Delete this share?")
    }
}

/// Edits an existing Nextcloud share.
struct ShareEditSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let share: Share
    let connectionId: String
    let isDir: Bool
    let policy: SharePolicy
    let onSaved: (Share) -> Void

    @State private var usePassword = false
    @State private var newPassword = ""
    @State private var expiry: ExpiryChoice = .none
    @State private var customDate = Date()
    @State private var permission: LinkPermission = .readOnly
    @State private var permissions = 1
    @State private var hideDownload = false
    @State private var label = ""
    @State private var note = ""
    @State private var busy = false
    @State private var error: String?

    private var isLink: Bool { share.kind == .publicLink }

    /// nil keeps the password, "" removes it (switched off), else the new one.
    private var passwordChange: String? {
        guard isLink else { return nil }
        if !usePassword { return share.hasPassword ? "" : nil }
        return newPassword.isEmpty ? nil : newPassword
    }

    var body: some View {
        SheetScaffold(title: share.editTitle, width: 480) {
            Form {
                if isLink {
                    Toggle("Password Protection", isOn: $usePassword)
                        .toggleStyle(.checkbox)
                        .disabled(policy.passwordEnforced && usePassword)
                    if usePassword {
                        HStack {
                            PasswordField(title: share.hasPassword ? "New Password" : "Password", text: $newPassword,
                                          prompt: share.hasPassword ? Text("Leave empty to keep the current one") : nil)
                            Button("Generate") { newPassword = ShareWindow.generatePassword() }
                        }
                    }
                    Picker("Permissions", selection: $permission) {
                        ForEach(LinkPermission.allCases.filter { isDir || $0 != .fileDrop }) { Text($0.title).tag($0) }
                    }
                    Toggle("Hide download", isOn: $hideDownload)
                        .toggleStyle(.checkbox)
                    TextField("Label", text: $label, prompt: Text("Optional"))
                } else if share.kind == .user || share.kind == .group {
                    Group {
                        Toggle("Edit", isOn: bit(2))
                        if isDir {
                            Toggle("Create", isOn: bit(4))
                            Toggle("Delete", isOn: bit(8))
                        }
                        Toggle("Reshare", isOn: bit(16))
                    }
                    .toggleStyle(.checkbox)
                }
                Picker("Expires", selection: $expiry) {
                    ForEach(ExpiryChoice.choices(for: policy), id: \.self) {
                        Text($0.title).tag($0)
                    }
                }
                if expiry == .custom {
                    DatePicker("Date", selection: $customDate, in: Date()...ExpiryChoice.maxDate(for: policy),
                               displayedComponents: .date)
                }
                TextField("Note to recipient", text: $note, prompt: Text("Optional"))
            }
            .formStyle(.grouped)
            if let error { InlineError(message: error) }
        } buttons: {
            Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            Button("Save") { save() }.keyboardShortcut(.defaultAction)
                .disabled(busy || (isLink && usePassword && !share.hasPassword && newPassword.isEmpty))
        }
        .onAppear {
            usePassword = share.hasPassword || (isLink && policy.passwordEnforced)
            permission = LinkPermission.from(share.permissions)
            permissions = share.permissions
            hideDownload = share.hideDownload
            label = share.label
            note = share.note
            if let expire = share.expireDate, let date = ExpiryChoice.parse(expire) {
                expiry = .custom
                customDate = date
            }
        }
    }

    private func bit(_ value: Int) -> Binding<Bool> {
        Binding(get: { permissions & value != 0 },
                set: { permissions = $0 ? (permissions | value) : (permissions & ~value) })
    }

    private func save() {
        busy = true
        let expireDate = expiry.dateString(custom: customDate) ?? ""
        let newPermissions = isLink ? permission.permissions(isDir: isDir) : permissions
        Task {
            do {
                let updated = try await model.client.updateShare(
                    connectionId: connectionId, id: share.id,
                    password: passwordChange,
                    expireDate: expireDate == (share.expireDate ?? "") ? nil : expireDate,
                    permissions: newPermissions == share.permissions ? nil : newPermissions,
                    hideDownload: isLink && hideDownload != share.hideDownload ? hideDownload : nil,
                    label: isLink && label != share.label ? label : nil,
                    note: note != share.note ? note : nil)
                onSaved(updated)
                dismiss()
            } catch {
                busy = false
                let alert = ErrorText.alert(for: error)
                self.error = "\(alert.title): \(alert.message)"
            }
        }
    }
}

#if DEBUG
// MARK: - Snapshot seams

extension ShareWindow {
    /// Opens on `mode`; `sharees` fill the people search (the first one selected), `createdURL` shows a
    /// just created, password-protected link (`--export-snapshots`).
    static func snapshot(target: ShareTarget, mode: ShareMode, sharees: [Sharee] = [],
                         createdURL: String? = nil) -> ShareWindow
    {
        var window = ShareWindow(target: target)
        window._mode = State(initialValue: mode)
        if let first = sharees.first {
            window._search = State(initialValue: String(first.label.prefix(4)))
            window._sharees = State(initialValue: sharees)
            window._sharee = State(initialValue: first)
            window._canEdit = State(initialValue: true)
        }
        if let createdURL {
            window._usePassword = State(initialValue: true)
            window._password = State(initialValue: "gK7mt-Qw2Zp-Hn4Rx-Vb8Ls")
            window._expiry = State(initialValue: .days(7))
            window._label = State(initialValue: "Mastering")
            window._createdURL = State(initialValue: createdURL)
        }
        return window
    }
}
#endif
