import CloudWireKit
import SwiftUI

struct SharesView: View {
    @Environment(AppModel.self) private var model
    @State private var connectionId = ""
    /// Share capabilities per remote Connection; nil until loaded.
    @State private var capabilities: [String: ShareCapabilities]?
    @State private var policy = SharePolicy()
    @State private var shares: [Share] = []
    @State private var filter = ""
    @State private var loading = false
    @State private var error: String?
    @State private var notice: String?
    @State private var editing: Share?
    @State private var deleting: Share?
    @State private var reload = 0

    /// Connections that can share at all; the picker offers only these.
    private var shareableConnections: [Connection] {
        model.remoteConnections.filter { capabilities?[$0.id]?.anySharing == true }
    }

    /// rclone providers: only links created in CloudWire are known.
    private var isLinkRegistry: Bool {
        guard let caps = capabilities?[connectionId] else { return false }
        return !caps.userShare && !caps.internalLink
    }

    var body: some View {
        SectionScaffold(title: String(localized: "Shares"),
                        subtitle: String(localized: "Links and shares of your cloud files.")) {
            if !shareableConnections.isEmpty {
                HStack {
                    Picker("Connection", selection: $connectionId) {
                        ForEach(shareableConnections) { Text($0.name).tag($0.id) }
                    }
                    .labelsHidden()
                    .fixedSize()
                    SearchField(prompt: String(localized: "Filter"), text: $filter)
                        .frame(width: 200)
                    if loading {
                        ProgressView().controlSize(.small)
                    } else {
                        Button {
                            reload += 1
                        } label: {
                            Label("Reload", systemImage: "arrow.clockwise").labelStyle(.iconOnly)
                        }
                        .help(Text("Reload"))
                    }
                }
            }
        } content: {
            Group {
                if model.remoteConnections.isEmpty {
                    ContentUnavailableView {
                        Label("No Connections", systemImage: "person.2")
                    } description: {
                        Text("Add a connection to share files.")
                    } actions: {
                        Button("Add Connection") {
                            model.selection = .connections
                            model.showAddConnection = true
                        }
                        .buttonStyle(.borderedProminent)
                    }
                } else if capabilities == nil {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if shareableConnections.isEmpty {
                    ContentUnavailableView {
                        Label("Sharing Not Available", systemImage: "person.2.slash")
                    } description: {
                        Text("None of your connections supports sharing.")
                    }
                } else if let error {
                    InlineError(message: error).padding(20)
                } else if loading && shares.isEmpty {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if shares.isEmpty {
                    ContentUnavailableView {
                        Label(isLinkRegistry ? String(localized: "No Links Created in CloudWire") : String(localized: "No Shares"),
                              systemImage: "person.2")
                    } description: {
                        if isLinkRegistry {
                            Text("CloudWire only knows the links it created. Right-click a file in a Mount or Offline folder and choose Share… to create one.")
                        } else {
                            Text("Right-click a file in a Mount or Offline folder and choose Share… to create one.")
                        }
                    }
                } else if filtered.isEmpty {
                    ContentUnavailableView.search(text: filter)
                } else {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 6) {
                            if let notice { ShareNotice(message: notice).padding(.bottom, 4) }
                            if isLinkRegistry {
                                Text("Links created in CloudWire").font(.callout).foregroundStyle(.secondary)
                            }
                            ForEach(filtered) { share in
                                ShareRow(share: share, showsPath: true, onEdit: { editing = share },
                                         onDelete: { deleting = share })
                            }
                        }
                        .padding(.horizontal, 20)
                        .padding(.bottom, 20)
                    }
                }
            }
        }
        .task(id: model.remoteConnections.map(\.id)) { await loadCapabilities() }
        .task(id: "\(connectionId)|\(reload)") { await load() }
        .sheet(item: $editing) { share in
            ShareEditSheet(share: share, connectionId: connectionId, isDir: share.isFolder, policy: policy) { updated in
                if let index = shares.firstIndex(where: { $0.id == updated.id }) { shares[index] = updated }
            }
        }
        .confirmationDialog(deleting?.deleteTitle ?? String(localized: "Delete this share?"),
                            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            presenting: deleting) { share in
            Button("Delete Share", role: .destructive) {
                let connectionId = connectionId
                notice = nil
                model.perform {
                    let result = try await model.client.deleteShare(connectionId: connectionId, id: share.id)
                    shares.removeAll { $0.id == share.id }
                    if result.remoteStillActive { notice = ShareNotice.stillActive }
                }
            }
        } message: { _ in
            Text("People using this share lose access.")
        }
    }

    private var filtered: [Share] {
        guard !filter.isEmpty else { return shares }
        return shares.filter {
            $0.path.localizedCaseInsensitiveContains(filter) || $0.shareWith.localizedCaseInsensitiveContains(filter)
                || $0.shareWithDisplayName.localizedCaseInsensitiveContains(filter)
                || $0.label.localizedCaseInsensitiveContains(filter)
        }
    }

    /// Asks every remote Connection whether it can share, then selects the first one that can.
    private func loadCapabilities() async {
        var found: [String: ShareCapabilities] = [:]
        for connection in model.remoteConnections {
            found[connection.id] = (try? await model.loadCapabilities(connectionId: connection.id)) ?? ShareCapabilities.none
        }
        guard !Task.isCancelled else { return }
        capabilities = found
        if !shareableConnections.contains(where: { $0.id == connectionId }) {
            connectionId = shareableConnections.first?.id ?? ""
        }
    }

    private func load() async {
        guard !connectionId.isEmpty else { return }
        loading = true
        error = nil
        notice = nil
        defer { loading = false }
        do {
            shares = try await model.loadShares(connectionId: connectionId, path: nil)
                .sorted { $0.path.localizedStandardCompare($1.path) == .orderedAscending }
            if capabilities?[connectionId]?.userShare == true {
                policy = (try? await model.loadPolicy(connectionId: connectionId)) ?? SharePolicy()
            }
        } catch is CancellationError {
        } catch {
            shares = []
            let alert = ErrorText.alert(for: error)
            self.error = "\(alert.title): \(alert.message)"
        }
    }
}
