import Foundation

/// What a sheet has picked while browsing a Connection: exactly one folder, or files of one folder,
/// never both. "Make Available Offline" allows several files and the Connection root; encrypting
/// (`singleItem()`) takes exactly one folder or one file below the root. Paths are relative to the
/// Connection root ("" = root).
public struct OfflineSelection: Sendable, Hashable {
    /// The item a selection maps to.
    public struct Target: Sendable, Hashable {
        public let kind: OfflineKind
        /// The folder itself (`.folder`) or the folder containing `files` (`.files`).
        public let remotePath: String
        /// Sorted file names in `remotePath` for `.files`, `nil` for `.folder`.
        public let files: [String]?

        /// The folder CloudWire creates for the offline copy inside a chosen parent folder: the cloud
        /// folder's name, or the Connection's name for its root (cleaned like the Core's default path).
        public func storageFolderName(connectionName: String) -> String {
            guard let last = remotePath.split(separator: "/").last else {
                let name = connectionName.trimmingCharacters(in: .whitespaces)
                    .replacingOccurrences(of: "/", with: "-").replacingOccurrences(of: ":", with: "-")
                return name.isEmpty || name == "." || name == ".." ? "Cloud" : name
            }
            return last.replacingOccurrences(of: ":", with: "-")
        }
    }

    /// The folder being browsed.
    public private(set) var path: String
    /// The selected folder, if any.
    public private(set) var folder: String?
    /// Checked file names in `filesFolder`.
    public private(set) var files: Set<String> = []
    /// The folder the checked files belong to.
    public private(set) var filesFolder: String
    /// Whether several files may be checked (otherwise checking one replaces the others).
    public private(set) var allowsMultipleFiles = true
    /// Whether the Connection root itself may be selected.
    public private(set) var allowsRoot = true

    /// Browses `path` with nothing selected.
    public init(path: String = "") {
        self.path = path
        filesFolder = path
    }

    /// Prefill from a draft: a folder ("" = the Connection root) is selected with its parent open, so
    /// the selected row is visible; files are checked inside their folder.
    public init(kind: OfflineKind, remotePath: String, files: [String]) {
        if kind == .files {
            self.init(path: remotePath)
            self.files = Set(files)
        } else {
            self.init(path: remotePath.split(separator: "/").dropLast().joined(separator: "/"))
            folder = remotePath
        }
    }

    /// One folder or one file, never the Connection root (encrypting existing data).
    public static func singleItem(path: String = "") -> OfflineSelection {
        var selection = OfflineSelection(path: path)
        selection.allowsMultipleFiles = false
        selection.allowsRoot = false
        return selection
    }

    /// Opens another folder: checked files are discarded, the folder selection stays.
    public mutating func navigate(to newPath: String) {
        guard newPath != path else { return }
        path = newPath
        files = []
        filesFolder = newPath
    }

    /// Selects `folderPath` (clearing the files); ignored for the root when `allowsRoot` is false.
    public mutating func selectFolder(_ folderPath: String) {
        guard canSelectFolder(folderPath) else { return }
        folder = folderPath
        files = []
    }

    public func canSelectFolder(_ folderPath: String) -> Bool {
        allowsRoot || !folderPath.isEmpty
    }

    /// Selects `folderPath`, or clears the folder selection when it is already selected.
    public mutating func toggleFolder(_ folderPath: String) {
        if folder == folderPath {
            folder = nil
        } else {
            selectFolder(folderPath)
        }
    }

    /// Checks or unchecks a file of the browsed folder; checking clears the folder selection.
    public mutating func toggleFile(_ name: String) {
        if isFileSelected(name) {
            files.remove(name)
            return
        }
        if filesFolder != path || !allowsMultipleFiles {
            files = []
            filesFolder = path
        }
        files.insert(name)
        folder = nil
    }

    public func isFolderSelected(_ folderPath: String) -> Bool {
        folder == folderPath
    }

    /// Whether `name` in the browsed folder is checked.
    public func isFileSelected(_ name: String) -> Bool {
        filesFolder == path && files.contains(name)
    }

    /// The Offline item to create, `nil` while nothing is selected.
    public var target: Target? {
        if let folder {
            return Target(kind: .folder, remotePath: folder, files: nil)
        }
        if !files.isEmpty {
            return Target(kind: .files, remotePath: filesFolder, files: files.sorted())
        }
        return nil
    }
}
