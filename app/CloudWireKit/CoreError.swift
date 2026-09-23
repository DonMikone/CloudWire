import Foundation

/// An error returned by the Core (`-32000` application errors carry an app code such as
/// `offline.overlap`; other JSON-RPC errors map to `rpc.<n>`) or raised by the client itself
/// (`client.*` codes).
public struct CoreError: Error, Sendable, Hashable, LocalizedError, CustomStringConvertible {
    public let code: String
    public let message: String
    /// The complete `error.data` object, for codes that carry extra fields (`dependents`, `policy`).
    public let data: JSONValue?

    public init(code: String, message: String, data: JSONValue? = nil) {
        self.code = code
        self.message = message
        self.data = data
    }

    public var errorDescription: String? { message }
    public var description: String { "\(code): \(message)" }

    /// The translatable detail sentence (`data.key` and `data.params`); nil when the error has none.
    /// `code` stays the error category.
    public var text: CoreText? {
        guard let key = data?["key"]?.stringValue, !key.isEmpty else { return nil }
        return CoreText(code: key, params: data?["params"]?.stringMap ?? [:],
                        message: data?["message"]?.stringValue ?? message)
    }

    /// Dependents listed by `connection.inUse`.
    public var dependents: [Dependent] {
        guard let items = data?["dependents"]?.arrayValue else { return [] }
        return items.compactMap { item in
            guard let kind = item["kind"]?.stringValue else { return nil }
            return Dependent(kind: kind, id: item["id"]?.stringValue ?? "", name: item["name"]?.stringValue ?? "")
        }
    }

    /// Server policy attached to `share.serverPolicy`.
    public var policy: SharePolicy? {
        guard let object = data?["policy"], case .object = object,
            let encoded = try? JSONEncoder.coreEncoder.encode(object)
        else { return nil }
        return try? JSONDecoder.coreDecoder.decode(SharePolicy.self, from: encoded)
    }

    public struct Dependent: Sendable, Hashable, Identifiable {
        public let kind: String
        public let id: String
        public let name: String
    }

    // Client-side codes.
    public static let notConnectedCode = "client.notConnected"
    public static let disconnectedCode = "client.disconnected"
    public static let timeoutCode = "client.timeout"
    public static let decodingCode = "client.decoding"
    public static let socketCode = "client.socket"

    public static func notConnected() -> CoreError {
        CoreError(code: notConnectedCode, message: "Not connected to the CloudWire Core.",
                  data: .object(["key": .string(notConnectedCode)]))
    }

    public static func disconnected() -> CoreError {
        CoreError(code: disconnectedCode, message: "The connection to the CloudWire Core was lost.",
                  data: .object(["key": .string(disconnectedCode)]))
    }

    public static func timeout(_ method: String) -> CoreError {
        CoreError(code: timeoutCode, message: "The CloudWire Core did not answer \(method) in time.",
                  data: .object(["key": .string(timeoutCode), "params": .object(["method": .string(method)])]))
    }

    /// True for errors that mean the Core is not reachable (as opposed to a failed operation).
    public var isConnectionProblem: Bool {
        code == Self.notConnectedCode || code == Self.disconnectedCode || code == Self.socketCode
            || code == Self.timeoutCode
    }
}

/// The JSON-RPC error object as sent by the Core.
struct RPCErrorObject: Decodable, Sendable {
    let code: Int
    let message: String
    let data: JSONValue?

    var coreError: CoreError {
        if code == -32000, let appCode = data?["code"]?.stringValue {
            return CoreError(code: appCode, message: data?["message"]?.stringValue ?? message, data: data)
        }
        return CoreError(code: "rpc.\(code)", message: message, data: data)
    }
}
