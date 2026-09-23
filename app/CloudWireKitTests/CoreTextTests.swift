import CloudWireKit
import Foundation
import Testing

private final class BundleToken {}

/// The App catalog compiled into this test bundle, one language at a time.
private func strings(_ language: String) throws -> Bundle {
    let path = try #require(Bundle(for: BundleToken.self).path(forResource: language, ofType: "lproj"))
    return try #require(Bundle(path: path))
}

private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
    try JSONDecoder().decode(type, from: Data(json.utf8))
}

/// A value for every param a Core text uses; distinct so a lost or swapped placeholder shows.
private let sampleParams: [String: String] = [
    "name": "Nm1", "path": "/p/Pa2", "storagePath": "/s/St3", "mountPoint": "/m/Mp4", "id": "ID5x",
    "version": "9.8.7", "label": "Lb6", "count": "7", "provider": "Pv8", "server": "https://sv9.example",
    "user": "Us10", "url": "Ur11", "folder": "Fo12.cwvault", "app": "Ap13", "percent": "73", "method": "Me14",
    "files": "Fi15, Fi16", "neededBytes": "3221225472", "freeBytes": "536870912", "untilMs": "1790613000000",
    "transferred": "41", "deleted": "53", "conflicts": "67",
]

@Suite("Core texts")
struct CoreTextTests {
    @Test("every code has a German sentence that keeps all values")
    func germanForEveryCode() throws {
        let en = try strings("en"), de = try strings("de")
        for code in CoreText.knownCodes where code != "detail" {
            let text = CoreText(code: code, params: sampleParams, message: "")
            let english = try #require(text.sentence(bundle: en), "\(code) needs its params")
            let german = try #require(text.sentence(bundle: de))
            #expect(german != english, "\(code) is not translated")
            for value in sampleParams.values {
                #expect(english.contains(value) == german.contains(value), "\(code): \(value) in \(german)")
            }
        }
    }

    @Test("counts pick the plural form")
    func plurals() throws {
        let en = try strings("en"), de = try strings("de")
        let one = CoreText(code: "sync.conflicts", params: ["count": "1", "name": "Drums"], message: "")
        let many = CoreText(code: "sync.conflicts", params: ["count": "3", "name": "Drums"], message: "")
        #expect(one.sentence(bundle: en) == "1 conflict copy created in “Drums”")
        #expect(many.sentence(bundle: en) == "3 conflict copies created in “Drums”")
        #expect(one.sentence(bundle: de) == "1 Konfliktkopie in „Drums“ angelegt")
        #expect(many.sentence(bundle: de) == "3 Konfliktkopien in „Drums“ angelegt")
        let done = CoreText(code: "sync.done", params: ["name": "Drums", "transferred": "12", "deleted": "0",
                                                          "conflicts": "1"], message: "")
        #expect(done.sentence(bundle: de) == "„Drums“ synchronisiert: 12 übertragen, 0 gelöscht, 1 Konflikt")
    }

    @Test("raw detail stays untranslated and apart from the sentence")
    func detail() throws {
        let de = try strings("de")
        let failed = CoreText(code: "sync.failed", params: ["name": "Album", "detail": "directory not found"],
                              message: "Sync of \"Album\" failed: directory not found")
        #expect(failed.parts(bundle: de).text == "Synchronisation von „Album“ fehlgeschlagen")
        #expect(failed.parts(bundle: de).detail == "directory not found")
        #expect(failed.localized(bundle: de) == "Synchronisation von „Album“ fehlgeschlagen: directory not found")
        // A sentence that ends with a period is followed by the detail, not by a colon.
        let missing = CoreText(code: "offline.cloudFolderMissing", params: ["detail": "directory not found"], message: "")
        #expect(missing.localized(bundle: de).hasSuffix("Offline-Element. directory not found"))

        let raw = CoreText(code: "detail", params: ["detail": "exit status 1"], message: "exit status 1")
        #expect(raw.parts(bundle: de) == ("exit status 1", nil))
        #expect(raw.parts(bundle: de, rawHeadline: "Fehlgeschlagen") == ("Fehlgeschlagen", "exit status 1"))
        #expect(failed.parts(bundle: de, rawHeadline: "Fehlgeschlagen").text != "Fehlgeschlagen",
                "a text with a sentence keeps it")
    }

    @Test("a cause adds its own sentence")
    func cause() throws {
        let de = try strings("de")
        let paused = CoreText(code: "offline.pausedByRule", params: ["cause": "pause.studioMode", "app": "REAPER"],
                              message: "Syncing paused: Studio Mode (REAPER)")
        #expect(paused.sentence(bundle: de) == "Synchronisation pausiert: Studiomodus (REAPER)")
        let unknownCause = CoreText(code: "offline.pausedByRule", params: ["cause": "pause.newRule"],
                                    message: "Syncing paused: new rule")
        #expect(unknownCause.localized(bundle: de) == "Syncing paused: new rule")
    }

    @Test("unknown codes, missing params and old entries show the English message")
    func fallbacks() throws {
        let de = try strings("de")
        #expect(CoreText(code: "mount.renamed", message: "Mount renamed").localized(bundle: de) == "Mount renamed")
        #expect(CoreText(code: "mount.created", params: ["name": "NC"], message: "Mount created")
            .localized(bundle: de) == "Mount created")
        let old = try decode(ActivityEntry.self, #"{"id":1,"ts":1,"level":"info","category":"mount","message":"Mount \"NC\" removed"}"#)
        #expect(old.text.localized(bundle: de) == "Mount \"NC\" removed")
    }

    @Test("Core payloads decode their codes")
    func payloads() throws {
        let de = try strings("de")
        let entry = try decode(ActivityEntry.self, #"""
            {"id":2,"ts":1,"level":"info","category":"mount","message":"Mount \"NC\" removed",
             "code":"mount.removed","params":{"name":"NC"}}
            """#)
        #expect(entry.text.localized(bundle: de) == "Laufwerk „NC“ entfernt")

        let mount = try decode(Mount.self, #"""
            {"id":"m1","state":"error","error":"Mount \"NC\" failed repeatedly: /Volumes/NC is used by another volume",
             "errorCode":"mount.failedRepeatedly","errorParams":{"name":"NC","cause":"mount.pointBusy","path":"/Volumes/NC"}}
            """#)
        #expect(mount.errorText.localized(bundle: de)
            == "Laufwerk „NC“ ist wiederholt ausgefallen: /Volumes/NC wird von einem anderen Volume verwendet")

        let item = try decode(OfflineItem.self, #"""
            {"id":"o1","state":"error","reason":"directory not found","reasonCode":"offline.syncFailed",
             "reasonParams":{"detail":"directory not found"}}
            """#)
        #expect(item.errorText.parts(bundle: de) == ("Synchronisation fehlgeschlagen", "directory not found"))

        let notification = try decode(CoreNotification.self, #"""
            {"id":1,"ts":1,"kind":"error","params":{"title":"Album","subjectId":"o1","message":"Sync of \"Album\" failed: x",
             "code":"sync.failed","params":{"name":"Album","detail":"x"}}}
            """#)
        #expect(notification.text.localized(bundle: de) == "Synchronisation von „Album“ fehlgeschlagen: x")

        let rule = try decode(ActiveRule.self, #"""
            {"id":"cpu","detail":"73%","code":"pause.cpu","params":{"percent":"73"},"message":"High CPU load (73%)"}
            """#)
        #expect(rule.text.sentence(bundle: de)?.hasPrefix("Hohe CPU-Last (73") == true)

        let step = try decode(ConfigStep.self, #"""
            {"connectionId":"c1","error":"The sign-in timed out","errorCode":"connection.signInTimedOut"}
            """#)
        #expect(step.errorText.localized(bundle: de) == "Die Anmeldung hat zu lange gedauert")
    }

    @Test("API errors carry their detail sentence apart from the category")
    func apiErrors() throws {
        let de = try strings("de")
        let locked = CoreError(code: "vault.locked", message: "Unlock the Vault first",
                               data: ["code": "vault.locked", "message": "Unlock the Vault first", "key": "vault.unlockFirst"])
        #expect(locked.text?.localized(bundle: de) == "Entsperre zuerst den Tresor")
        let space = CoreError(code: "offline.insufficientSpace", message: "Not enough free space",
                              data: ["key": "offline.insufficientSpace",
                                     "params": ["neededBytes": 3_221_225_472, "freeBytes": 536_870_912]])
        let needed = ByteCountFormatter.string(fromByteCount: 3_221_225_472, countStyle: .file)
        #expect(space.text?.localized(bundle: de).contains(needed) == true, "numeric params are read as well")
        #expect(CoreError(code: "core.internal", message: "boom").text == nil)
        #expect(CoreError.timeout("mounts.list").text?.localized(bundle: de)
            == "Der Hintergrunddienst von CloudWire hat mounts.list nicht rechtzeitig beantwortet.")
    }
}
