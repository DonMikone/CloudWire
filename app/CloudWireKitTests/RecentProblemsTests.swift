import CloudWireKit
import Foundation
import Testing

private func decode<T: Decodable>(_ type: T.Type, _ fields: [String: Any]) throws -> T {
    try JSONDecoder().decode(type, from: JSONSerialization.data(withJSONObject: fields))
}

private func problem(_ category: String, subject: String, ts: Int64 = 1_000) throws -> ActivityEntry {
    try decode(ActivityEntry.self, ["id": 1, "ts": ts, "level": "error", "category": category, "subjectId": subject,
                                    "message": "failed"])
}

private func mount(state: String, mountedAt: Int64? = nil) throws -> Mount {
    var fields: [String: Any] = ["id": "m1", "state": state]
    if let mountedAt { fields["mountedAt"] = mountedAt }
    return try decode(Mount.self, fields)
}

private func item(state: String, lastSyncAt: Int64? = nil) throws -> OfflineItem {
    var fields: [String: Any] = ["id": "o1", "state": state]
    if let lastSyncAt { fields["lastSyncAt"] = lastSyncAt }
    return try decode(OfflineItem.self, fields)
}

@Suite("Recent problems")
struct RecentProblemsTests {
    @Test("a Mount problem clears only once the Mount came up after it")
    func mountProblems() throws {
        let entry = try problem("mount", subject: "m1")
        #expect(!entry.isResolved(mounts: [try mount(state: "error")], offlineItems: []))
        #expect(!entry.isResolved(mounts: [try mount(state: "mounted", mountedAt: 900)], offlineItems: []))
        #expect(!entry.isResolved(mounts: [try mount(state: "mounted")], offlineItems: []))
        #expect(entry.isResolved(mounts: [try mount(state: "mounted", mountedAt: 1_100)], offlineItems: []))
        #expect(entry.isResolved(mounts: [], offlineItems: []), "a deleted Mount has no problem left")
    }

    @Test("a sync problem clears only after a later successful sync", arguments: ["sync", "offline"])
    func syncProblems(category: String) throws {
        let entry = try problem(category, subject: "o1")
        #expect(!entry.isResolved(mounts: [], offlineItems: [try item(state: "error", lastSyncAt: 1_100)]))
        #expect(!entry.isResolved(mounts: [], offlineItems: [try item(state: "idle", lastSyncAt: 1_000)]),
                "a warning logged by the run that set lastSyncAt stays")
        #expect(entry.isResolved(mounts: [], offlineItems: [try item(state: "idle", lastSyncAt: 1_100)]))
        #expect(entry.isResolved(mounts: [], offlineItems: []))
    }

    @Test("problems without a checkable subject stay")
    func otherProblems() throws {
        #expect(!(try problem("vault", subject: "v1")).isResolved(mounts: [], offlineItems: []))
        #expect(!(try problem("mount", subject: "")).isResolved(mounts: [], offlineItems: []))
    }
}
