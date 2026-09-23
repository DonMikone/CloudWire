import CloudWireKit
import Darwin
import Foundation
import Testing

/// The Core side of a socketpair: blocking line reads on a dedicated thread, raw writes.
private final class Peer: @unchecked Sendable {
    let fd: Int32
    private var buffer = Data()
    private let lock = NSLock()

    init(fd: Int32) { self.fd = fd }

    deinit { close(fd) }

    /// Reads one `\n`-terminated JSON line (waits at most 5 s).
    func readRequest() async throws -> Request {
        let line: Data = try await withCheckedThrowingContinuation { continuation in
            let thread = Thread { [self] in
                continuation.resume(with: Result { try self.blockingReadLine() })
            }
            thread.start()
        }
        let object = try #require(try JSONSerialization.jsonObject(with: line) as? [String: Any])
        return Request(
            id: try #require(object["id"] as? Int),
            method: object["method"] as? String ?? "",
            params: object["params"] as? [String: Any] ?? [:],
            jsonrpc: object["jsonrpc"] as? String ?? "")
    }

    private func blockingReadLine() throws -> Data {
        lock.lock()
        defer { lock.unlock() }
        let deadline = Date().addingTimeInterval(5)
        while true {
            if let newline = buffer.firstIndex(of: 0x0A) {
                let line = buffer[buffer.startIndex..<newline]
                buffer = Data(buffer[buffer.index(after: newline)...])
                return Data(line)
            }
            var poller = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
            let remaining = Int32(max(0, deadline.timeIntervalSinceNow) * 1000)
            guard remaining > 0, poll(&poller, 1, remaining) > 0 else {
                throw PeerError.timeout
            }
            var chunk = [UInt8](repeating: 0, count: 4096)
            let count = read(fd, &chunk, chunk.count)
            guard count > 0 else { throw PeerError.closed }
            buffer.append(contentsOf: chunk[0..<count])
        }
    }

    func write(_ text: String) {
        let bytes = Array(text.utf8)
        var offset = 0
        while offset < bytes.count {
            let written = bytes[offset...].withUnsafeBufferPointer { Darwin.write(fd, $0.baseAddress, $0.count) }
            if written <= 0 { return }
            offset += written
        }
    }

    /// Simulates the Core going away (EOF for the client) without releasing the descriptor.
    func hangUp() {
        shutdown(fd, SHUT_RDWR)
    }

    struct Request: @unchecked Sendable {
        let id: Int
        let method: String
        let params: [String: Any]
        let jsonrpc: String
    }

    enum PeerError: Error {
        case timeout
        case closed
    }
}

private func makePair() async throws -> (CoreClient, Peer) {
    var fds: [Int32] = [0, 0]
    try #require(socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0)
    let client = CoreClient(socketPath: nil)
    await client.attach(fileDescriptor: fds[0])
    return (client, Peer(fd: fds[1]))
}

@Suite("CoreClient over a socketpair", .timeLimit(.minutes(1)))
struct CoreClientTests {
    @Test("responses are matched by id, not by order")
    func outOfOrderResponses() async throws {
        let (client, peer) = try await makePair()

        async let first = client.browse(connectionId: "c1", path: "Alpha")
        async let second = client.browse(connectionId: "c1", path: "Beta")

        let requestA = try await peer.readRequest()
        let requestB = try await peer.readRequest()
        #expect(requestA.jsonrpc == "2.0")
        #expect(requestA.method == "connections.browse")
        #expect(requestA.id != requestB.id)

        // Answer in reverse order; each result echoes the requested path.
        for request in [requestB, requestA] {
            let path = try #require(request.params["path"] as? String)
            peer.write(#"{"jsonrpc":"2.0","id":\#(request.id),"result":[{"name":"\#(path)-file","path":"\#(path)/x","isDir":false,"size":7,"modTime":1,"mimeType":""}]}"# + "\n")
        }

        let (resultA, resultB) = try await (first, second)
        #expect(resultA.map(\.name) == ["Alpha-file"])
        #expect(resultB.map(\.name) == ["Beta-file"])
        #expect(resultA.first?.size == 7)
    }

    @Test("application errors map to CoreError(code, message) with extra data")
    func applicationError() async throws {
        let (client, peer) = try await makePair()

        let call = Task { try await client.deleteConnection(id: "abc") }
        let request = try await peer.readRequest()
        #expect(request.method == "connections.delete")
        #expect(request.params["id"] as? String == "abc")
        peer.write(#"{"jsonrpc":"2.0","id":\#(request.id),"error":{"code":-32000,"message":"generic","data":{"code":"connection.inUse","message":"Still used by 1 Mount","dependents":[{"kind":"mount","id":"m1","name":"Musik"}]}}}"# + "\n")

        let error = await #expect(throws: CoreError.self) { try await call.value }
        #expect(error?.code == "connection.inUse")
        #expect(error?.message == "Still used by 1 Mount")
        #expect(error?.dependents.map(\.name) == ["Musik"])
        #expect(error?.dependents.first?.kind == "mount")
    }

    @Test("protocol errors map to rpc.<code>")
    func protocolError() async throws {
        let (client, peer) = try await makePair()

        let call = Task { try await client.call("does.not.exist", as: EmptyResult.self) }
        let request = try await peer.readRequest()
        peer.write(#"{"jsonrpc":"2.0","id":\#(request.id),"error":{"code":-32601,"message":"unknown method"}}"# + "\n")

        let error = await #expect(throws: CoreError.self) { _ = try await call.value }
        #expect(error?.code == "rpc.-32601")
        #expect(error?.message == "unknown method")
    }

    @Test("events reach subscriber streams, typed or unknown")
    func eventsAreDelivered() async throws {
        let (client, peer) = try await makePair()
        let stream = await client.events()
        var iterator = stream.makeAsyncIterator()

        async let subscribe: Void = client.subscribeEvents(client: "app")
        let request = try await peer.readRequest()
        #expect(request.method == "events.subscribe")
        #expect(request.params["client"] as? String == "app")
        peer.write(#"{"jsonrpc":"2.0","id":\#(request.id),"result":{}}"# + "\n")
        try await subscribe

        peer.write(#"{"jsonrpc":"2.0","method":"event","params":{"type":"mount.status","data":{"id":"m1","connectionId":"c1","remotePath":"","mountPoint":"/Users/x/CloudWire/Laufwerke/Musik","volumeName":"Musik","mountType":"nfsmount","autoMount":true,"readOnly":false,"cacheMaxGB":20,"options":{},"state":"mounted","error":"","createdAt":5}}}"# + "\n")
        peer.write(#"{"jsonrpc":"2.0","method":"event","params":{"type":"future.thing","data":{"x":1}}}"# + "\n")

        guard case .mountStatus(let mount) = await iterator.next() else {
            Issue.record("expected mount.status")
            return
        }
        #expect(mount.id == "m1")
        #expect(mount.state == .mounted)
        #expect(mount.volumeName == "Musik")

        guard case .unknown(let type, let data) = await iterator.next() else {
            Issue.record("expected unknown event")
            return
        }
        #expect(type == "future.thing")
        #expect(data["x"]?.int64Value == 1)
    }

    @Test("lines split across writes and several messages in one write are framed correctly")
    func partialLines() async throws {
        let (client, peer) = try await makePair()
        let stream = await client.events()
        var iterator = stream.makeAsyncIterator()

        async let info = client.coreInfo(timeout: nil)
        async let pause = client.pauseStatus()
        let r1 = try await peer.readRequest()
        let r2 = try await peer.readRequest()
        let infoRequest = r1.method == "core.info" ? r1 : r2
        let pauseRequest = r1.method == "core.info" ? r2 : r1

        let infoLine = #"{"jsonrpc":"2.0","id":\#(infoRequest.id),"result":{"version":"0.1.0","apiVersion":1,"rcloneVersion":"v1.75.0","pid":42,"startedAt":1,"appSupport":"/tmp/cw"}}"# + "\n"
        let eventLine = #"{"jsonrpc":"2.0","method":"event","params":{"type":"connections.changed","data":{}}}"# + "\n"
        let pauseLine = #"{"jsonrpc":"2.0","id":\#(pauseRequest.id),"result":{"manualUntil":-1,"activeRules":[{"id":"studioMode","detail":"REAPER"}],"effective":true}}"# + "\n"

        // Fragment 1 ends mid-line; fragment 2 completes line 1 and starts line 2; fragment 3 holds the rest.
        let all = Array((infoLine + eventLine + pauseLine).utf8)
        let cut1 = 17
        let cut2 = infoLine.utf8.count + 20
        peer.write(String(decoding: all[..<cut1], as: UTF8.self))
        try await Task.sleep(for: .milliseconds(50))
        peer.write(String(decoding: all[cut1..<cut2], as: UTF8.self))
        try await Task.sleep(for: .milliseconds(50))
        peer.write(String(decoding: all[cut2...], as: UTF8.self))

        let (coreInfo, pauseStatus) = try await (info, pause)
        #expect(coreInfo.version == "0.1.0")
        #expect(coreInfo.pid == 42)
        #expect(pauseStatus.isIndefinite)
        #expect(pauseStatus.activeRules.map(\.detail) == ["REAPER"])
        guard case .connectionsChanged = await iterator.next() else {
            Issue.record("expected connections.changed")
            return
        }
    }

    @Test("a closed socket fails pending calls and reports connectionLost")
    func disconnectFailsPendingCalls() async throws {
        let (client, peer) = try await makePair()
        let stream = await client.events()
        var iterator = stream.makeAsyncIterator()

        let call = Task { try await client.offlineItems() }
        _ = try await peer.readRequest()
        peer.hangUp()

        let error = await #expect(throws: CoreError.self) { _ = try await call.value }
        #expect(error?.code == CoreError.disconnectedCode)
        guard case .connectionLost = await iterator.next() else {
            Issue.record("expected connectionLost")
            return
        }
        #expect(await client.isConnected == false)
        let later = await #expect(throws: CoreError.self) { _ = try await client.offlineItems() }
        #expect(later?.code == CoreError.notConnectedCode)
    }
}
