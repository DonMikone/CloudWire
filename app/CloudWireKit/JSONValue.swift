import Foundation

/// An arbitrary JSON value. Used for loosely typed parts of the Core contract
/// (rclone option defaults, activity details, notification params, merge patches).
public enum JSONValue: Sendable, Hashable {
    case null
    case bool(Bool)
    case int(Int64)
    case double(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public subscript(key: String) -> JSONValue? {
        if case .object(let object) = self { return object[key] }
        return nil
    }

    public var stringValue: String? {
        if case .string(let value) = self { return value }
        return nil
    }

    public var boolValue: Bool? {
        if case .bool(let value) = self { return value }
        return nil
    }

    public var int64Value: Int64? {
        switch self {
        case .int(let value): return value
        case .double(let value) where value.rounded() == value && abs(value) < 9.2e18: return Int64(value)
        default: return nil
        }
    }

    public var doubleValue: Double? {
        switch self {
        case .int(let value): return Double(value)
        case .double(let value): return value
        default: return nil
        }
    }

    public var arrayValue: [JSONValue]? {
        if case .array(let value) = self { return value }
        return nil
    }

    public var objectValue: [String: JSONValue]? {
        if case .object(let value) = self { return value }
        return nil
    }

    public var isNull: Bool {
        if case .null = self { return true }
        return false
    }

    /// Textual form used to show a value in a form field (rclone style: bools as true/false,
    /// lists comma separated, null as empty).
    public var displayString: String {
        switch self {
        case .null: return ""
        case .bool(let value): return value ? "true" : "false"
        case .int(let value): return String(value)
        case .double(let value):
            if value.rounded() == value && abs(value) < 1e15 { return String(Int64(value)) }
            return String(value)
        case .string(let value): return value
        case .array(let values): return values.map(\.displayString).joined(separator: ",")
        case .object:
            guard let data = try? JSONEncoder.coreEncoder.encode(self) else { return "" }
            return String(decoding: data, as: UTF8.self)
        }
    }

    /// Recursively merges `patch` into `self` following RFC 7396 (JSON merge patch).
    public func merged(with patch: JSONValue) -> JSONValue {
        guard case .object(let patchObject) = patch else { return patch }
        var result = objectValue ?? [:]
        for (key, value) in patchObject {
            if value.isNull {
                result.removeValue(forKey: key)
            } else {
                result[key] = (result[key] ?? .null).merged(with: value)
            }
        }
        return .object(result)
    }
}

extension JSONValue: Codable {
    public init(from decoder: any Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let value = try? container.decode(Bool.self) {
            self = .bool(value)
        } else if let value = try? container.decode(Int64.self) {
            self = .int(value)
        } else if let value = try? container.decode(Double.self) {
            self = .double(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([JSONValue].self) {
            self = .array(value)
        } else if let value = try? container.decode([String: JSONValue].self) {
            self = .object(value)
        } else {
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "Unsupported JSON value")
        }
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .null: try container.encodeNil()
        case .bool(let value): try container.encode(value)
        case .int(let value): try container.encode(value)
        case .double(let value): try container.encode(value)
        case .string(let value): try container.encode(value)
        case .array(let value): try container.encode(value)
        case .object(let value): try container.encode(value)
        }
    }
}

extension JSONValue: ExpressibleByNilLiteral, ExpressibleByBooleanLiteral, ExpressibleByIntegerLiteral,
    ExpressibleByFloatLiteral, ExpressibleByStringLiteral, ExpressibleByArrayLiteral, ExpressibleByDictionaryLiteral
{
    public init(nilLiteral: ()) { self = .null }
    public init(booleanLiteral value: Bool) { self = .bool(value) }
    public init(integerLiteral value: Int64) { self = .int(value) }
    public init(floatLiteral value: Double) { self = .double(value) }
    public init(stringLiteral value: String) { self = .string(value) }
    public init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
    public init(dictionaryLiteral elements: (String, JSONValue)...) {
        self = .object(Dictionary(elements, uniquingKeysWith: { _, last in last }))
    }
}

/// Types that can be written into a JSON merge patch or RPC params.
public protocol JSONRepresentable {
    var jsonValue: JSONValue { get }
}

extension Bool: JSONRepresentable { public var jsonValue: JSONValue { .bool(self) } }
extension Int: JSONRepresentable { public var jsonValue: JSONValue { .int(Int64(self)) } }
extension Int64: JSONRepresentable { public var jsonValue: JSONValue { .int(self) } }
extension Double: JSONRepresentable { public var jsonValue: JSONValue { .double(self) } }
extension String: JSONRepresentable { public var jsonValue: JSONValue { .string(self) } }
extension JSONValue: JSONRepresentable { public var jsonValue: JSONValue { self } }
extension Array: JSONRepresentable where Element: JSONRepresentable {
    public var jsonValue: JSONValue { .array(map(\.jsonValue)) }
}
extension Dictionary: JSONRepresentable where Key == String, Value: JSONRepresentable {
    public var jsonValue: JSONValue { .object(mapValues(\.jsonValue)) }
}
extension Optional: JSONRepresentable where Wrapped: JSONRepresentable {
    public var jsonValue: JSONValue { self?.jsonValue ?? .null }
}

extension JSONValue {
    /// Builds a nested object patch: `JSONValue.patch(["pauseRules", "cpu", "enabled"], true)`
    /// → `{"pauseRules":{"cpu":{"enabled":true}}}`.
    public static func patch(_ path: [String], _ value: JSONValue) -> JSONValue {
        path.reversed().reduce(value) { partial, key in .object([key: partial]) }
    }

    /// Builds an object from key/value pairs, dropping `nil` values (optional RPC params).
    public static func params(_ pairs: KeyValuePairs<String, (any JSONRepresentable)?>) -> JSONValue {
        var object: [String: JSONValue] = [:]
        for (key, value) in pairs {
            if let value { object[key] = value.jsonValue }
        }
        return .object(object)
    }
}

extension JSONEncoder {
    /// Encoder used for everything sent to the Core.
    public static var coreEncoder: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return encoder
    }
}

extension JSONDecoder {
    public static var coreDecoder: JSONDecoder { JSONDecoder() }
}
