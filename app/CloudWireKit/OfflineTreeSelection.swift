import Foundation

/// The checkbox state of a tree row.
public enum CheckState: Sendable {
    case unchecked
    /// Something below the row is checked; the row itself is kept only as structure.
    case partial
    case checked
}

/// The Selection of an Offline Item in a checkbox tree: checked folders and files of one Connection. A
/// checked folder includes everything below it; parents of checked rows are partially checked. Paths are
/// relative to the Connection root ("" = root) and use "/".
public struct OfflineTreeSelection: Sendable, Hashable {
    /// The item a Selection maps to.
    public struct Target: Sendable, Hashable {
        public let kind: OfflineKind
        /// The item's root folder.
        public let remotePath: String
        /// Sorted paths relative to `remotePath` for `.files`, `nil` for `.folder`.
        public let files: [String]?
    }

    /// The tree root, relative to the Connection root ("" = Connection root).
    public let root: String
    /// Selected paths relative to the Connection root, within `root`; none lies below another.
    public private(set) var paths: Set<String>

    /// Keeps only paths within `root` and drops those below another selected path.
    public init(root: String = "", paths: [String] = []) {
        self.root = root
        let inRoot = Set(paths.filter { Self.isWithin($0, root) })
        self.paths = inRoot.filter { p in !inRoot.contains { $0 != p && Self.isWithin(p, $0) } }
    }

    /// The current Selection of an item, rooted at its folder.
    public init(item: OfflineItem) {
        self.init(root: item.remotePath, paths: item.coveredPaths)
    }

    public var isEmpty: Bool { paths.isEmpty }

    /// Whether `path` equals `ancestor` or lies below it ("" contains everything).
    public static func isWithin(_ path: String, _ ancestor: String) -> Bool {
        ancestor.isEmpty || path == ancestor || path.hasPrefix(ancestor + "/")
    }

    /// Checked when `path` or an ancestor is selected, partial when something below it is.
    public func state(of path: String) -> CheckState {
        if paths.contains(where: { Self.isWithin(path, $0) }) { return .checked }
        if paths.contains(where: { Self.isWithin($0, path) }) { return .partial }
        return .unchecked
    }

    /// Checks an unchecked or partial row (replacing the entries below it), or unchecks a checked one.
    /// Unchecking a row inside a checked folder checks its siblings on every level instead, which needs
    /// `children` (child paths of a folder, `nil` while not loaded); without them nothing changes.
    public mutating func toggle(_ path: String, children: (String) -> [String]?) {
        guard let covering = paths.first(where: { Self.isWithin(path, $0) }) else {
            paths = paths.filter { !Self.isWithin($0, path) }
            paths.insert(path)
            return
        }
        var next = paths
        next.remove(covering)
        var level = covering
        while level != path {
            guard let kids = children(level) else { return }
            let rest = level.isEmpty ? Substring(path) : path.dropFirst(level.count + 1)
            let name = rest.split(separator: "/", maxSplits: 1).first.map(String.init) ?? String(rest)
            let step = level.isEmpty ? name : level + "/" + name
            for kid in kids where kid != step {
                next.insert(kid)
            }
            level = step
        }
        paths = next
    }

    /// The sorted entries of this Selection that `other` does not check.
    public func uncovered(by other: OfflineTreeSelection) -> [String] {
        paths.filter { other.state(of: $0) != .checked }.sorted()
    }

    /// The Offline Item to create or store, `nil` while nothing is selected.
    public var target: Target? {
        guard !paths.isEmpty else { return nil }
        if paths == [root] {
            return Target(kind: .folder, remotePath: root, files: nil)
        }
        let files = paths.map { root.isEmpty ? $0 : String($0.dropFirst(root.count + 1)) }.sorted()
        return Target(kind: .files, remotePath: root, files: files)
    }
}
