import CloudWireKit
import Foundation
import Testing

@Suite("CoreSettings.Bandwidth")
struct CoreSettingsBandwidthTests {
    private func bandwidth(enabled: Bool, up: Int, down: Int) throws -> CoreSettings.Bandwidth {
        let json = #"{"enabled": \#(enabled), "uploadMiBps": \#(up), "downloadMiBps": \#(down)}"#
        return try JSONDecoder().decode(CoreSettings.Bandwidth.self, from: Data(json.utf8))
    }

    @Test("a disabled limit shows as unlimited in both directions")
    func disabledIsUnlimited() throws {
        let b = try bandwidth(enabled: false, up: 5, down: 20)
        #expect(b.limit(\.uploadMiBps) == 0)
        #expect(b.limit(\.downloadMiBps) == 0)
    }

    @Test("setting one direction of a disabled limit keeps the other unlimited")
    func enablingKeepsOtherSideUnlimited() throws {
        let b = try bandwidth(enabled: false, up: 5, down: 20).settingLimit(\.uploadMiBps, to: 8)
        #expect(b.enabled)
        #expect(b.limit(\.uploadMiBps) == 8)
        #expect(b.limit(\.downloadMiBps) == 0)
    }

    @Test("setting one direction of an enabled limit keeps the other limit")
    func changingKeepsOtherSide() throws {
        let b = try bandwidth(enabled: true, up: 5, down: 20).settingLimit(\.downloadMiBps, to: 0)
        #expect(b.enabled)
        #expect(b.limit(\.uploadMiBps) == 5)
        #expect(b.limit(\.downloadMiBps) == 0)
    }
}
