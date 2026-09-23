import CloudWireKit
import Foundation
import Testing

@Suite("ActionURL")
struct ActionURLTests {
    @Test("round trip keeps spaces, umlauts, ampersands and several paths intact")
    func roundTrip() throws {
        let nfc = "/Users/mike/CloudWire/Laufwerke/Musik/Bass & Drums/Kühlschrank Größe.wav"
        let nfd = "/Users/mike/CloudWire/Musik/Ku\u{0308}hl=1+2 #3 50%.aif"
        let action = ActionURL(name: .copyPublicLink, paths: [nfc, nfd, "/tmp/a&path=/evil"], token: "t0k+en/=&x")

        let url = action.url
        #expect(url.scheme == "cloudwire")
        #expect(url.host == "action")
        let query = try #require(url.query)
        #expect(!query.contains(" "))
        // Exactly one name, three paths and one token: no value leaked an unescaped `&` or `=`.
        let keys = query.split(separator: "&").map { $0.split(separator: "=", maxSplits: 1).first.map(String.init) ?? "" }
        #expect(keys == ["name", "path", "path", "path", "token"])

        let parsed = try #require(ActionURL(url: url))
        #expect(parsed == action)
        #expect(parsed.paths[1].unicodeScalars.elementsEqual(nfd.unicodeScalars))

        // Survives a trip through a string, as when handed over by the Finder extension.
        let reparsedURL = try #require(URL(string: url.absoluteString))
        let reparsed = try #require(ActionURL(url: reparsedURL))
        #expect(reparsed == action)
    }

    @Test("parses standard percent-encoding produced by other clients")
    func parsesExternalEncoding() throws {
        let url = try #require(URL(string: "cloudwire://action?name=share&path=/Users/m/Musik/Bass%20%26%20Drums/K%C3%BChl.wav"))
        let action = try #require(ActionURL(url: url))
        #expect(action.name == .share)
        #expect(action.paths == ["/Users/m/Musik/Bass & Drums/Kühl.wav"])
        #expect(action.token == nil)
    }

    @Test("rejects foreign URLs, unknown names and missing paths")
    func rejectsInvalid() throws {
        let invalid = [
            "https://action?name=share&path=/x",
            "cloudwire://other?name=share&path=/x",
            "cloudwire://action?name=format&path=/x",
            "cloudwire://action?name=share",
        ]
        for text in invalid {
            let url = try #require(URL(string: text))
            #expect(ActionURL(url: url) == nil, "\(text)")
        }
    }

    @Test("state-changing actions are the ones that need a token or confirmation")
    func stateChanging() {
        let changing = ActionURL.Name.allCases.filter(\.isStateChanging)
        #expect(Set(changing) == [.copyPublicLink, .makeOffline, .removeOffline, .encrypt])
    }

    @Test("standardised paths resolve dot segments, keep the folder slash and must be absolute")
    func standardizedPaths() throws {
        let action = ActionURL(name: .makeOffline, paths: [
            "/Users/m/CloudWire/Laufwerke/Musik/../../../Library//Keychains/x.db",
            "/Users/m/CloudWire/Laufwerke/Musik/./Loops/",
        ])
        let standardized = try action.standardized()
        #expect(standardized.paths == ["/Users/m/Library/Keychains/x.db", "/Users/m/CloudWire/Laufwerke/Musik/Loops/"])

        for path in ["relative/x.wav", "~/Music", "../etc/passwd"] {
            #expect(throws: ActionURL.PathProblem.notAbsolute(path)) {
                try ActionURL(name: .share, paths: [path]).standardized()
            }
        }
    }

    @Test("only makeOffline takes several paths")
    func severalPaths() throws {
        let paths = ["/Users/m/CloudWire/a.wav", "/Users/m/CloudWire/b.wav"]
        for name in ActionURL.Name.allCases {
            let action = ActionURL(name: name, paths: paths)
            if name == .makeOffline {
                #expect(try action.standardized().paths == paths)
            } else {
                #expect(throws: ActionURL.PathProblem.severalPaths, "\(name)") { try action.standardized() }
                #expect(try ActionURL(name: name, paths: [paths[0]]).standardized().paths == [paths[0]])
            }
        }
    }
}
