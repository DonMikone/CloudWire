import CloudWireKit
import Foundation
import Testing

/// A loaded cloud tree: folder path → child paths.
private let tree: [String: [String]] = [
    "": ["Meine Daten", "Top.txt"],
    "Meine Daten": ["Meine Daten/123", "Meine Daten/other.txt"],
    "Meine Daten/123": ["Meine Daten/123/A", "Meine Daten/123/B", "Meine Daten/123/C", "Meine Daten/123/readme.txt"],
]

private func item(_ fields: [String: Any]) throws -> OfflineItem {
    try JSONDecoder().decode(OfflineItem.self, from: JSONSerialization.data(withJSONObject: fields))
}

@Suite("OfflineTreeSelection")
struct OfflineTreeSelectionTests {
    @Test("checked rows make their parents partial, not checked")
    func partialParents() {
        var selection = OfflineTreeSelection()
        selection.toggle("Meine Daten/123/B") { tree[$0] }
        selection.toggle("Meine Daten/123/C") { tree[$0] }
        #expect(selection.state(of: "Meine Daten/123") == .partial)
        #expect(selection.state(of: "Meine Daten") == .partial)
        #expect(selection.state(of: "") == .partial)
        #expect(selection.state(of: "Meine Daten/123/A") == .unchecked)
        #expect(selection.state(of: "Meine Daten/123/B/deep/x.wav") == .checked)
        #expect(selection.state(of: "Meine Daten/123/BB") == .unchecked)
        let target = selection.target
        #expect(target?.kind == .files)
        #expect(target?.remotePath == "")
        #expect(target?.files == ["Meine Daten/123/B", "Meine Daten/123/C"])
    }

    @Test("checking a partial folder replaces the entries below it")
    func checkPartial() {
        var selection = OfflineTreeSelection(paths: ["Meine Daten/123/B", "Meine Daten/123/C", "Top.txt"])
        selection.toggle("Meine Daten/123") { tree[$0] }
        #expect(selection.paths == ["Meine Daten/123", "Top.txt"])
        #expect(selection.state(of: "Meine Daten/123/B") == .checked)
    }

    @Test("unchecking inside a checked folder checks the siblings on every level")
    func uncheckInside() {
        var selection = OfflineTreeSelection(paths: ["Meine Daten"])
        selection.toggle("Meine Daten/123/B") { tree[$0] }
        #expect(selection.paths == ["Meine Daten/other.txt", "Meine Daten/123/A", "Meine Daten/123/C",
                                    "Meine Daten/123/readme.txt"])
        #expect(selection.state(of: "Meine Daten/123") == .partial)
        #expect(selection.state(of: "Meine Daten/123/B") == .unchecked)
    }

    @Test("unchecking inside a checked folder changes nothing while a level is not loaded")
    func uncheckWithoutChildren() {
        var selection = OfflineTreeSelection(paths: ["Meine Daten"])
        selection.toggle("Meine Daten/123/B") { $0 == "Meine Daten" ? tree[$0] : nil }
        #expect(selection.paths == ["Meine Daten"])
    }

    @Test("unchecking a selected row removes only it")
    func uncheckEntry() {
        var selection = OfflineTreeSelection(paths: ["Meine Daten/123/B", "Top.txt"])
        selection.toggle("Top.txt") { _ in nil }
        #expect(selection.paths == ["Meine Daten/123/B"])
        selection.toggle("Meine Daten/123/B") { _ in nil }
        #expect(selection.isEmpty)
        #expect(selection.target == nil)
    }

    @Test("the checked root is the whole Connection")
    func root() {
        var selection = OfflineTreeSelection(paths: ["Meine Daten/123/B"])
        selection.toggle("") { tree[$0] }
        #expect(selection.paths == [""])
        #expect(selection.state(of: "Top.txt") == .checked)
        #expect(selection.target?.kind == .folder)
        #expect(selection.target?.remotePath == "")
        #expect(selection.target?.files == nil)
    }

    @Test("init keeps paths within the root and drops covered ones")
    func normalizedInit() {
        let selection = OfflineTreeSelection(root: "X", paths: ["X/a", "X/a/b", "Y/c", "X/a"])
        #expect(selection.paths == ["X/a"])
        #expect(selection.target?.kind == .files)
        #expect(selection.target?.remotePath == "X")
        #expect(selection.target?.files == ["a"])
        #expect(OfflineTreeSelection(root: "X", paths: ["X"]).target?.kind == .folder)
    }

    @Test("uncovered lists what the other Selection no longer checks")
    func uncoveredBy() {
        let original = OfflineTreeSelection(paths: ["M/123/B", "M/123/C"])
        let narrowed = OfflineTreeSelection(paths: ["M/123/B/x"])
        let widened = OfflineTreeSelection(paths: ["M"])
        #expect(original.uncovered(by: narrowed) == ["M/123/B", "M/123/C"])
        #expect(original.uncovered(by: widened).isEmpty)
        #expect(widened.uncovered(by: original) == ["M"])
    }

    @Test("items expose their Connection-relative paths")
    func coveredPaths() throws {
        let folder = try item(["id": "a", "kind": "folder", "remotePath": "Music/Live"])
        #expect(folder.coveredPaths == ["Music/Live"])
        let root = try item(["id": "b", "kind": "files", "remotePath": "", "files": ["M/123/B", "x.wav"]])
        #expect(root.coveredPaths == ["M/123/B", "x.wav"])
        #expect(root.displayName == "B, x.wav")
        let legacy = try item(["id": "c", "kind": "files", "remotePath": "Samples", "files": ["kick.wav"]])
        #expect(legacy.coveredPaths == ["Samples/kick.wav"])
        #expect(OfflineTreeSelection(item: legacy).target?.files == ["kick.wav"])
    }
}
