import CloudWireKit
import SwiftUI

struct SharesView: View {
    @Environment(AppModel.self) private var model
    @State private var connectionId = ""
    @State private var shares: [Share] = []
    @State private var filter = ""
    @State private var loading = false
    @State private var error: String?
    @State private var deleting: Share?
    @State private var reload = 0

    var body: some View {
        SectionScaffold(title: String(localized: "Shares"),
                        subtitle: String(localized: "Links and shares of your cloud files.")) {
            HStack {
                Picker("Connection", selection: $connectionId) {
                    ForEach(model.remoteConnections) { Text($0.name).tag($0.id) }
                }
                .labelsHidden()
                .fixedSize()
                TextField("Filter", text: $filter)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 200)
                Button {
                    reload += 1
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .help(Text("Reload"))
            }
        } content: {
            Group {
                if model.remoteConnections.isEmpty {
                    ContentUnavailableView("No Connections", systemImage: "person.2",
                                           description: Text("Add a connection to share files."))
                } else if let error {
                    InlineError(message: error).padding(20)
                } else if loading && shares.isEmpty {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if filtered.isEmpty {
                    ContentUnavailableView {
                        Label("No Shares", systemImage: "person.2")
                    } description: {
                        Text("Right-click a file in a Mount or Offline folder and choose Share… to create one.")
                    }
                } else {
                    ScrollView {
                        VStack(spacing: 6) {
                            ForEach(filtered) { share in
                                ShareRow(share: share, showsPath: true, onEdit: {
                                    WindowRouter.shared.showShare(ShareTarget(
                                        connectionId: connectionId, path: share.path, isDir: share.isFolder,
                                        name: (share.path as NSString).lastPathComponent, manage: true))
                                }, onDelete: { deleting = share })
                            }
                        }
                        .padding(.horizontal, 20)
                        .padding(.bottom, 20)
                    }
                }
            }
        }
        .task(id: "\(connectionId)|\(reload)") { await load() }
        .onAppear {
            if connectionId.isEmpty { connectionId = model.remoteConnections.first?.id ?? "" }
        }
        .confirmationDialog(String(localized: "Delete this share?"),
                            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            presenting: deleting) { share in
            Button("Delete Share", role: .destructive) {
                let connectionId = connectionId
                model.perform {
                    try await model.client.deleteShare(connectionId: connectionId, id: share.id)
                    shares.removeAll { $0.id == share.id }
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

    private func load() async {
        guard !connectionId.isEmpty else { return }
        loading = true
        error = nil
        defer { loading = false }
        do {
            shares = try await model.loadShares(connectionId: connectionId, path: nil)
                .sorted { $0.path.localizedStandardCompare($1.path) == .orderedAscending }
        } catch is CancellationError {
        } catch {
            shares = []
            let alert = ErrorText.alert(for: error)
            self.error = "\(alert.title): \(alert.message)"
        }
    }
}
