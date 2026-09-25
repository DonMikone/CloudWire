import CloudWireKit
import SwiftUI

/// A lazily loaded checkbox tree of one Connection below `selection.root`: folders expand on demand,
/// checking a folder includes everything below it, and parents of checked rows show the partial
/// state. Paths of other Offline Items (`locked`) and Vault folders cannot be checked.
struct OfflineTree: View {
    @Environment(AppModel.self) private var model
    let connectionId: String
    /// The name of the root row: the Connection, or the item's root folder.
    let rootName: String
    @Binding var selection: OfflineTreeSelection
    /// Connection-relative paths of other Offline Items.
    let locked: [String]

    @State private var expanded: Set<String> = []
    @State private var children: [String: [BrowseEntry]] = [:]
    @State private var loading: Set<String> = []
    @State private var errors: [String: String] = [:]
    /// The Connection the loaded state belongs to; results for another one are dropped.
    @State private var loadedFor = ""

    private struct Node {
        let path: String
        let name: String
        let isDir: Bool
        let size: Int64?
    }

    private enum Row: Identifiable {
        case node(Node, depth: Int)
        case loading(String, depth: Int)
        case error(String, String, depth: Int)
        case empty(String, depth: Int)

        var id: String {
            switch self {
            case let .node(node, _): "n:" + node.path
            case let .loading(path, _): "l:" + path
            case let .error(path, _, _): "e:" + path
            case let .empty(path, _): "0:" + path
            }
        }
    }

    var body: some View {
        List {
            ForEach(rows) { row in
                rowView(row)
            }
        }
        .listStyle(.bordered(alternatesRowBackgrounds: true))
        .task(id: connectionId) {
            loadedFor = connectionId
            children = [:]
            errors = [:]
            loading = []
            expanded = initiallyExpanded
            for path in expanded.sorted() {
                await load(path)
            }
        }
    }

    /// The root and every folder above a selected path, so each selected row is visible.
    private var initiallyExpanded: Set<String> {
        var out: Set<String> = [selection.root]
        for path in selection.paths {
            var parent = (path as NSString).deletingLastPathComponent
            while parent != selection.root, OfflineTreeSelection.isWithin(parent, selection.root) {
                out.insert(parent)
                if parent.isEmpty { break }
                parent = (parent as NSString).deletingLastPathComponent
            }
        }
        return out
    }

    private var rows: [Row] {
        var out: [Row] = [.node(Node(path: selection.root, name: rootName, isDir: true, size: nil), depth: 0)]
        appendChildren(of: selection.root, depth: 1, to: &out)
        return out
    }

    private func appendChildren(of path: String, depth: Int, to out: inout [Row]) {
        guard expanded.contains(path) else { return }
        if let error = errors[path] {
            out.append(.error(path, error, depth: depth))
            return
        }
        guard let entries = children[path] else {
            if loading.contains(path) { out.append(.loading(path, depth: depth)) }
            return
        }
        if entries.isEmpty {
            out.append(.empty(path, depth: depth))
        }
        for entry in entries {
            out.append(.node(Node(path: entry.path, name: entry.name, isDir: entry.isDir,
                                  size: entry.isDir ? nil : entry.size), depth: depth))
            if entry.isDir, isExpandable(entry.path, name: entry.name) {
                appendChildren(of: entry.path, depth: depth + 1, to: &out)
            }
        }
    }

    @ViewBuilder
    private func rowView(_ row: Row) -> some View {
        switch row {
        case let .node(node, depth):
            nodeRow(node, depth: depth)
        case let .loading(_, depth):
            ProgressView().controlSize(.small).padding(.leading, indent(depth))
        case let .error(_, message, depth):
            InlineError(message: message).padding(.leading, indent(depth))
        case let .empty(_, depth):
            Text("This folder is empty.").foregroundStyle(.secondary).padding(.leading, indent(depth))
        }
    }

    /// Chevron and checkbox columns line up across depths.
    private func indent(_ depth: Int) -> CGFloat { CGFloat(depth) * 16 + 12 + 18 + 12 }

    private func nodeRow(_ node: Node, depth: Int) -> some View {
        let isLocked = isLocked(node.path)
        let isVault = node.isDir && isVault(node.name)
        let expandable = node.isDir && isExpandable(node.path, name: node.name)
        let isOpen = expanded.contains(node.path)
        return HStack(spacing: 6) {
            Color.clear.frame(width: CGFloat(depth) * 16, height: 1)
            if expandable {
                Button {
                    toggleExpanded(node.path)
                } label: {
                    Image(systemName: isOpen ? "chevron.down" : "chevron.right")
                        .foregroundStyle(.secondary)
                        .frame(width: 12)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(isOpen ? Text("Collapse “\(node.name)”") : Text("Expand “\(node.name)”"))
            } else {
                Color.clear.frame(width: 12, height: 1)
            }
            checkbox(node, isLocked: isLocked, isVault: isVault)
            Button {
                if expandable {
                    toggleExpanded(node.path)
                } else if !node.isDir, !isLocked {
                    toggle(node.path)
                }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: symbol(node, isVault: isVault))
                        .foregroundStyle(node.isDir && !isVault && !isLocked ? Color.accentColor : Color.secondary)
                        .frame(width: 18)
                    Text(node.name)
                        .foregroundStyle(isLocked || isVault ? .secondary : .primary)
                        .lineLimit(1).truncationMode(.middle)
                    Spacer()
                    if isLocked {
                        Text("Already offline").font(.caption).foregroundStyle(.secondary)
                    } else if isVault {
                        Text("Vault – unlock it and make it available offline under Vaults")
                            .font(.caption).foregroundStyle(.secondary)
                    } else if let size = node.size {
                        Text(Format.bytes(size)).font(.caption).foregroundStyle(.secondary)
                    }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(isVault || (isLocked && !expandable))
        }
    }

    @ViewBuilder
    private func checkbox(_ node: Node, isLocked: Bool, isVault: Bool) -> some View {
        if isLocked {
            Image(systemName: "checkmark.square").foregroundStyle(.secondary).frame(width: 18)
                .accessibilityLabel(Text("Already offline"))
        } else if isVault {
            Image(systemName: "square").foregroundStyle(.tertiary).frame(width: 18)
                .accessibilityHidden(true)
        } else {
            let state = selection.state(of: node.path)
            Button {
                toggle(node.path)
            } label: {
                Image(systemName: checkSymbol(state))
                    .foregroundStyle(state == .unchecked ? Color.secondary : Color.accentColor)
                    .frame(width: 18)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Select “\(node.name)”"))
            .accessibilityAddTraits(state == .checked ? .isSelected : [])
            .accessibilityValue(state == .partial ? Text("Partially selected") : Text(verbatim: ""))
        }
    }

    private func checkSymbol(_ state: CheckState) -> String {
        switch state {
        case .checked: "checkmark.square.fill"
        case .partial: "minus.square.fill"
        case .unchecked: "square"
        }
    }

    private func symbol(_ node: Node, isVault: Bool) -> String {
        if isVault { return "lock.shield" }
        if node.path == selection.root, node.path.isEmpty { return "externaldrive" }
        return node.isDir ? "folder" : "doc"
    }

    private func isVault(_ name: String) -> Bool { name.hasSuffix(".cwvault") }

    private func isLocked(_ path: String) -> Bool {
        locked.contains { OfflineTreeSelection.isWithin(path, $0) }
    }

    /// Locked rows and Vault folders stay closed; the root can always open.
    private func isExpandable(_ path: String, name: String) -> Bool {
        path == selection.root || (!isVault(name) && !isLocked(path))
    }

    private func toggle(_ path: String) {
        selection.toggle(path) { children[$0]?.map(\.path) }
    }

    private func toggleExpanded(_ path: String) {
        if expanded.remove(path) != nil { return }
        expanded.insert(path)
        if children[path] == nil || errors[path] != nil {
            Task { await load(path) }
        }
    }

    private func load(_ path: String) async {
        let connection = connectionId
        guard !loading.contains(path) else { return }
        loading.insert(path)
        errors[path] = nil
        defer {
            if loadedFor == connection { loading.remove(path) }
        }
        do {
            let entries = try await model.browse(connectionId: connection, path: path)
            guard loadedFor == connection else { return }
            children[path] = entries
        } catch is CancellationError {
        } catch {
            guard loadedFor == connection else { return }
            errors[path] = ErrorText.alert(for: error).message
        }
    }
}
