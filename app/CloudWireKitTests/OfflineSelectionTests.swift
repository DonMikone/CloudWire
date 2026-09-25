import CloudWireKit
import Testing

@Suite("OfflineSelection")
struct OfflineSelectionTests {
    @Test("nothing selected cannot be created")
    func emptySelection() {
        var selection = OfflineSelection()
        #expect(selection.target == nil)

        selection.toggleFile("a.txt")
        selection.toggleFile("a.txt")
        #expect(selection.target == nil)

        selection.toggleFolder("Docs")
        selection.toggleFolder("Docs")
        #expect(selection.target == nil)
    }

    @Test("a folder and files are mutually exclusive")
    func mutualExclusion() {
        var selection = OfflineSelection(path: "Docs")
        selection.toggleFile("a.txt")
        selection.toggleFile("b.txt")
        selection.toggleFolder("Docs/Photos")
        #expect(selection.folder == "Docs/Photos")
        #expect(!selection.isFileSelected("a.txt"))
        #expect(selection.target?.kind == .folder)

        selection.toggleFile("a.txt")
        #expect(selection.folder == nil)
        #expect(selection.isFileSelected("a.txt"))
        #expect(!selection.isFileSelected("b.txt"))
        #expect(selection.target?.kind == .files)
    }

    @Test("selecting a folder replaces the previously selected one")
    func singleFolder() {
        var selection = OfflineSelection()
        selection.toggleFolder("A")
        selection.selectFolder("B")
        #expect(!selection.isFolderSelected("A"))
        #expect(selection.isFolderSelected("B"))
    }

    @Test("navigating discards checked files but keeps the folder selection")
    func navigation() {
        var selection = OfflineSelection(path: "Docs")
        selection.toggleFile("a.txt")
        selection.navigate(to: "Docs/Sub")
        #expect(selection.target == nil)
        selection.navigate(to: "Docs")
        #expect(!selection.isFileSelected("a.txt"))

        selection.toggleFolder("Docs/Sub")
        selection.navigate(to: "Other")
        #expect(selection.folder == "Docs/Sub")
        #expect(selection.target?.remotePath == "Docs/Sub")
    }

    @Test("a folder maps to kind folder with its own path")
    func folderMapping() {
        var selection = OfflineSelection(path: "Docs")
        selection.toggleFolder("Docs/Photos")
        let target = selection.target
        #expect(target?.kind == .folder)
        #expect(target?.remotePath == "Docs/Photos")
        #expect(target?.files == nil)
    }

    @Test("the root selected via Select This Folder maps to the whole Connection")
    func rootMapping() {
        var selection = OfflineSelection()
        selection.selectFolder(selection.path)
        #expect(selection.target?.kind == .folder)
        #expect(selection.target?.remotePath == "")
        #expect(selection.target?.files == nil)
    }

    @Test("files map to kind files with their folder and sorted names")
    func filesMapping() {
        var selection = OfflineSelection(path: "Docs")
        selection.toggleFile("b.txt")
        selection.toggleFile("a.txt")
        let target = selection.target
        #expect(target?.kind == .files)
        #expect(target?.remotePath == "Docs")
        #expect(target?.files == ["a.txt", "b.txt"])
    }

    @Test("single-item mode takes one folder or one file, never the root")
    func singleItem() {
        var selection = OfflineSelection.singleItem()
        selection.selectFolder("")
        selection.toggleFolder("")
        #expect(selection.target == nil)

        selection.toggleFile("a.txt")
        selection.toggleFile("b.txt")
        #expect(selection.target?.files == ["b.txt"])

        selection.toggleFolder("Docs")
        #expect(selection.target?.kind == .folder)
        #expect(selection.target?.remotePath == "Docs")
    }
}
