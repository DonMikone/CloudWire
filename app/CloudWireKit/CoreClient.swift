import Darwin
import Dispatch
import Foundation

/// JSON-RPC 2.0 client for the Core's unix socket (newline-delimited JSON).
///
/// Requests may be pipelined; responses are matched by id. After `subscribeEvents(client:)` the
/// Core pushes events, which are fanned out to every stream returned by `events()`. When the
/// socket closes, pending calls fail with `client.disconnected`, subscribers receive
/// `.connectionLost`, and `connect()` may be called again (the event subscription is renewed).
public actor CoreClient {
    public nonisolated let socketPath: String?

    private var channel: DispatchIO?
    private var generation = 0
    private var nextID = 1
    private var pending: [Int: CheckedContinuation<Data, any Error>] = [:]
    private var framer = LineFramer()
    private var subscribers: [UUID: AsyncStream<CoreEvent>.Continuation] = [:]
    private var subscribedClient: String?
    private let queue = DispatchQueue(label: "io.github.donmikone.cloudwire.core-client")

    /// - Parameter socketPath: the Core socket; `nil` for clients that are only ever attached to an
    ///   existing descriptor (tests).
    public init(socketPath: String? = CorePaths.socketPath) {
        self.socketPath = socketPath
    }

    deinit {
        channel?.close(flags: .stop)
        for continuation in subscribers.values { continuation.finish() }
    }

    public var isConnected: Bool { channel != nil }

    // MARK: Connection

    /// Connects to `socketPath`. No-op while connected. Renews the event subscription after a reconnect.
    public func connect() async throws {
        guard channel == nil else { return }
        guard let socketPath else { throw CoreError.notConnected() }
        let fd = try Self.openSocket(path: socketPath)
        attach(fileDescriptor: fd)
        if let client = subscribedClient {
            _ = try await send("events.subscribe", params: JSONValue.object(["client": .string(client)]),
                               as: EmptyResult.self, timeout: .seconds(10))
        }
    }

    /// Uses an already connected stream socket (takes ownership of `fd`).
    public func attach(fileDescriptor fd: Int32) {
        if channel != nil { closeConnection(notify: false) }
        var on: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        _ = fcntl(fd, F_SETFD, FD_CLOEXEC)

        generation += 1
        let currentGeneration = generation
        let (chunks, chunkContinuation) = AsyncStream<ReadChunk>.makeStream()
        let io = DispatchIO(type: .stream, fileDescriptor: fd, queue: queue, cleanupHandler: { _ in
            close(fd)
        })
        io.setLimit(lowWater: 1)
        io.read(offset: 0, length: Int.max, queue: queue) { done, data, error in
            if let data, !data.isEmpty {
                chunkContinuation.yield(.data(Data(data)))
            }
            if done {
                chunkContinuation.yield(.closed(error))
                chunkContinuation.finish()
            }
        }
        channel = io
        framer = LineFramer()

        Task { [weak self] in
            for await chunk in chunks {
                guard let self else { return }
                await self.receive(chunk, generation: currentGeneration)
            }
        }
    }

    /// Closes the connection without emitting `.connectionLost`.
    public func disconnect() {
        closeConnection(notify: false)
    }

    // MARK: Events

    /// A new stream of events. Streams live until the consumer stops iterating.
    public func events() -> AsyncStream<CoreEvent> {
        let id = UUID()
        let (stream, continuation) = AsyncStream<CoreEvent>.makeStream(bufferingPolicy: .unbounded)
        subscribers[id] = continuation
        continuation.onTermination = { [weak self] _ in
            Task { await self?.removeSubscriber(id) }
        }
        return stream
    }

    /// Calls `events.subscribe {"client": client}` now (when connected) and after every reconnect.
    public func subscribeEvents(client: String) async throws {
        subscribedClient = client
        if channel != nil {
            _ = try await send("events.subscribe", params: JSONValue.object(["client": .string(client)]),
                               as: EmptyResult.self, timeout: .seconds(10))
        }
    }

    private func removeSubscriber(_ id: UUID) {
        subscribers.removeValue(forKey: id)
    }

    private func broadcast(_ event: CoreEvent) {
        for continuation in subscribers.values {
            continuation.yield(event)
        }
    }

    // MARK: Calls

    /// Calls `method` without params and decodes `result` as `R`.
    public func call<R: Decodable & Sendable>(_ method: String, as type: R.Type = R.self,
                                              timeout: Duration? = nil) async throws -> R
    {
        try await send(method, params: JSONValue.object([:]), as: type, timeout: timeout)
    }

    /// Calls `method` with `params` and decodes `result` as `R`.
    public func call<P: Encodable & Sendable, R: Decodable & Sendable>(
        _ method: String, params: P, as type: R.Type = R.self, timeout: Duration? = nil
    ) async throws -> R {
        try await send(method, params: params, as: type, timeout: timeout)
    }

    private func send<P: Encodable, R: Decodable>(_ method: String, params: P, as type: R.Type,
                                                  timeout: Duration?) async throws -> R
    {
        let line = try await rawCall(method, params: params, timeout: timeout)
        if R.self == EmptyResult.self, let empty = EmptyResult() as? R {
            return empty
        }
        do {
            return try JSONDecoder.coreDecoder.decode(ResultEnvelope<R>.self, from: line).result
        } catch {
            throw CoreError(code: CoreError.decodingCode, message: "Unexpected response to \(method): \(error)")
        }
    }

    private func rawCall<P: Encodable>(_ method: String, params: P, timeout: Duration?) async throws -> Data {
        try Task.checkCancellation()
        guard let channel else { throw CoreError.notConnected() }
        let id = nextID
        nextID += 1
        var line: Data
        do {
            line = try JSONEncoder.coreEncoder.encode(RPCRequest(id: id, method: method, params: params))
        } catch {
            throw CoreError(code: "client.encoding", message: "Cannot encode \(method): \(error)")
        }
        line.append(0x0A)

        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Data, any Error>) in
                pending[id] = continuation
                let bytes = line.withUnsafeBytes { DispatchData(bytes: $0) }
                channel.write(offset: 0, data: bytes, queue: queue) { _, _, _ in }
                if let timeout {
                    Task { [weak self] in
                        try? await Task.sleep(for: timeout)
                        await self?.fail(id, with: CoreError.timeout(method))
                    }
                }
            }
        } onCancel: {
            Task { [weak self] in await self?.fail(id, with: CancellationError()) }
        }
    }

    private func fail(_ id: Int, with error: any Error) {
        pending.removeValue(forKey: id)?.resume(throwing: error)
    }

    // MARK: Reading

    private func receive(_ chunk: ReadChunk, generation chunkGeneration: Int) {
        guard chunkGeneration == generation else { return }
        switch chunk {
        case .data(let data):
            for line in framer.append(data) {
                handle(line: line)
            }
        case .closed:
            closeConnection(notify: true)
        }
    }

    private func handle(line: Data) {
        guard let envelope = try? JSONDecoder.coreDecoder.decode(IncomingEnvelope.self, from: line) else {
            return
        }
        if let id = envelope.id {
            guard let continuation = pending.removeValue(forKey: id) else { return }
            if let error = envelope.error {
                continuation.resume(throwing: error.coreError)
            } else {
                continuation.resume(returning: line)
            }
        } else if envelope.method == "event", let params = envelope.params {
            broadcast(CoreEvent.decode(type: params.type, data: params.data ?? .null))
        }
    }

    private func closeConnection(notify: Bool) {
        let hadChannel = channel != nil
        channel?.close(flags: .stop)
        channel = nil
        generation += 1
        framer = LineFramer()
        let calls = pending
        pending.removeAll()
        for continuation in calls.values {
            continuation.resume(throwing: CoreError.disconnected())
        }
        if notify && hadChannel {
            broadcast(.connectionLost)
        }
    }

    // MARK: Socket

    static func openSocket(path: String) throws -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw posixError("socket", errno) }
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = Array(path.utf8)
        let capacity = MemoryLayout.size(ofValue: address.sun_path)
        guard pathBytes.count < capacity else {
            close(fd)
            throw CoreError(code: CoreError.socketCode, message: "The socket path is too long: \(path)")
        }
        withUnsafeMutableBytes(of: &address.sun_path) { buffer in
            buffer.copyBytes(from: pathBytes)
            buffer[pathBytes.count] = 0
        }
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        let result = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard result == 0 else {
            let code = errno
            close(fd)
            throw posixError("connect", code)
        }
        return fd
    }

    private static func posixError(_ operation: String, _ code: Int32) -> CoreError {
        CoreError(code: CoreError.socketCode,
                  message: "\(operation) failed: \(String(cString: strerror(code)))",
                  data: .object(["errno": .int(Int64(code))]))
    }
}

extension CoreError {
    /// POSIX errno of a `client.socket` error (e.g. `EPERM` when the sandbox denies the socket).
    public var posixCode: Int32? {
        data?["errno"]?.int64Value.map { Int32($0) }
    }
}

// MARK: - Wire types

private enum ReadChunk: Sendable {
    case data(Data)
    case closed(Int32)
}

private struct RPCRequest<P: Encodable>: Encodable {
    let jsonrpc = "2.0"
    let id: Int
    let method: String
    let params: P
}

private struct IncomingEnvelope: Decodable {
    let id: Int?
    let method: String?
    let error: RPCErrorObject?
    let params: EventParams?

    struct EventParams: Decodable {
        let type: String
        let data: JSONValue?
    }
}

private struct ResultEnvelope<R: Decodable>: Decodable {
    let result: R
}

/// Splits a byte stream into `\n`-terminated lines.
struct LineFramer: Sendable {
    private var buffer = Data()

    mutating func append(_ chunk: Data) -> [Data] {
        buffer.append(chunk)
        var lines: [Data] = []
        var lineStart = buffer.startIndex
        while let newline = buffer[lineStart...].firstIndex(of: 0x0A) {
            var line = buffer[lineStart..<newline]
            if line.last == 0x0D { line = line.dropLast() }
            if !line.isEmpty { lines.append(Data(line)) }
            lineStart = buffer.index(after: newline)
        }
        if lineStart != buffer.startIndex {
            buffer = Data(buffer[lineStart...])
        }
        return lines
    }
}
