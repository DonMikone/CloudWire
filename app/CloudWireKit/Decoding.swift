import Foundation

/// A coding key built from any string. Lets DTOs decode fields by name without a CodingKeys enum.
struct AnyKey: CodingKey {
    let stringValue: String
    let intValue: Int?

    init(_ string: String) {
        stringValue = string
        intValue = nil
    }

    init?(stringValue: String) { self.init(stringValue) }

    init?(intValue: Int) {
        stringValue = String(intValue)
        self.intValue = intValue
    }
}

extension KeyedDecodingContainer where Key == AnyKey {
    /// Decodes `key` if present and well-typed; otherwise returns `fallback`. Never throws, so an
    /// unexpected field shape from a newer Core cannot break the whole response.
    func value<T: Decodable>(_ key: String, _ fallback: @autoclosure () -> T) -> T {
        if let decoded = try? decodeIfPresent(T.self, forKey: AnyKey(key)) {
            return decoded
        }
        return fallback()
    }

    /// Decodes an optional field; missing, `null` or mistyped values become `nil`.
    func optional<T: Decodable>(_ key: String, as type: T.Type = T.self) -> T? {
        (try? decodeIfPresent(T.self, forKey: AnyKey(key))) ?? nil
    }

    /// Decodes a string field, accepting numbers and bools as well (OCS ids are sometimes numeric).
    func string(_ key: String, _ fallback: String = "") -> String {
        guard let raw = optional(key, as: JSONValue.self) else { return fallback }
        switch raw {
        case .null: return fallback
        case .string(let value): return value
        case .int, .double, .bool: return raw.displayString
        default: return fallback
        }
    }

    /// Decodes an optional string, accepting numbers; empty strings stay empty.
    func optionalString(_ key: String) -> String? {
        guard let raw = optional(key, as: JSONValue.self) else { return nil }
        switch raw {
        case .string(let value): return value
        case .int, .double, .bool: return raw.displayString
        default: return nil
        }
    }

    /// Decodes an integer field, accepting floating point encodings.
    func int64(_ key: String, _ fallback: Int64 = 0) -> Int64 {
        optional(key, as: JSONValue.self)?.int64Value ?? fallback
    }

    func optionalInt64(_ key: String) -> Int64? {
        optional(key, as: JSONValue.self)?.int64Value
    }

    func int(_ key: String, _ fallback: Int = 0) -> Int {
        Int(clamping: int64(key, Int64(fallback)))
    }

    func double(_ key: String, _ fallback: Double = 0) -> Double {
        optional(key, as: JSONValue.self)?.doubleValue ?? fallback
    }

    func optionalDouble(_ key: String) -> Double? {
        optional(key, as: JSONValue.self)?.doubleValue
    }

    func bool(_ key: String, _ fallback: Bool = false) -> Bool {
        guard let raw = optional(key, as: JSONValue.self) else { return fallback }
        switch raw {
        case .bool(let value): return value
        case .int(let value): return value != 0
        case .string(let value): return value == "true" || value == "1"
        default: return fallback
        }
    }

    /// Decodes an array, skipping elements that fail to decode instead of failing the whole array.
    func array<T: Decodable>(_ key: String, of type: T.Type = T.self) -> [T] {
        guard let raw = optional(key, as: [JSONValue].self) else { return [] }
        return raw.compactMap { try? $0.decode(T.self) }
    }

    /// Decodes a `[String: String]` map, converting scalar values to strings.
    func stringMap(_ key: String) -> [String: String] {
        optional(key, as: JSONValue.self)?.stringMap ?? [:]
    }
}

extension JSONValue {
    /// Re-decodes this value as a typed DTO.
    public func decode<T: Decodable>(_ type: T.Type = T.self) throws -> T {
        let data = try JSONEncoder.coreEncoder.encode(self)
        return try JSONDecoder.coreDecoder.decode(T.self, from: data)
    }

    /// An object as `[String: String]`, scalar values converted to strings; empty for other values.
    var stringMap: [String: String] {
        var result: [String: String] = [:]
        for (name, value) in objectValue ?? [:] where !value.isNull {
            result[name] = value.displayString
        }
        return result
    }
}

/// A string-backed enumeration that accepts values it does not know (a newer Core may add states).
public protocol OpenStringEnum: RawRepresentable, Codable, Hashable, Sendable, ExpressibleByStringLiteral,
    JSONRepresentable
where RawValue == String {
    init(rawValue: String)
}

extension OpenStringEnum {
    public init(stringLiteral value: String) { self.init(rawValue: value) }

    public var jsonValue: JSONValue { .string(rawValue) }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.singleValueContainer()
        self.init(rawValue: (try? container.decode(String.self)) ?? "")
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }
}

/// Decodes an array while dropping elements that do not decode.
struct LossyArray<Element: Decodable>: Decodable {
    let elements: [Element]

    init(from decoder: any Decoder) throws {
        let raw = try [JSONValue](from: decoder)
        elements = raw.compactMap { try? $0.decode(Element.self) }
    }
}
