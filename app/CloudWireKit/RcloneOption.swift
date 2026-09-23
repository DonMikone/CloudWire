import Foundation

/// One rclone option, exactly as rclone serialises it (PascalCase keys).
public struct RcloneOption: Decodable, Sendable, Hashable, Identifiable {
    public var name: String
    public var fieldName: String
    public var help: String
    public var groups: String
    /// Provider condition: "" = all, "AWS,Ceph" = only these, "!AWS,Ceph" = all except these.
    public var provider: String
    public var defaultValue: JSONValue
    public var value: JSONValue
    public var examples: [RcloneExample]
    public var shortOpt: String
    /// Bit mask: 1 = hidden on the command line, 2 = hidden in the configurator.
    public var hide: Int
    public var required: Bool
    public var isPassword: Bool
    public var noPrefix: Bool
    public var advanced: Bool
    public var exclusive: Bool
    public var sensitive: Bool
    public var defaultString: String
    public var valueString: String
    public var type: String

    public var id: String { name }

    /// First line of the help text without a closing full stop, used as the field label.
    public var title: String {
        guard let first = help.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: true).first else {
            return name
        }
        let line = first.trimmingCharacters(in: .whitespaces)
        guard line.hasSuffix("."), !line.hasSuffix("..") else { return line }
        return String(line.dropLast())
    }

    /// Help text after the first line.
    public var details: String {
        let parts = help.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false)
        return parts.count > 1 ? String(parts[1]).trimmingCharacters(in: .whitespacesAndNewlines) : ""
    }

    /// rclone's default as text.
    public var defaultText: String {
        defaultString.isEmpty ? defaultValue.displayString : defaultString
    }

    /// The value to prefill in a form: the current value when set, otherwise the default.
    public var initialString: String {
        if !value.isNull { return valueString.isEmpty ? value.displayString : valueString }
        return defaultText
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        name = c.string("Name")
        fieldName = c.string("FieldName")
        help = c.string("Help")
        groups = c.string("Groups")
        provider = c.string("Provider")
        defaultValue = c.value("Default", JSONValue.null)
        value = c.value("Value", JSONValue.null)
        examples = c.array("Examples")
        shortOpt = c.string("ShortOpt")
        if let mask = c.optional("Hide", as: JSONValue.self) {
            switch mask {
            case .int(let raw): hide = Int(raw)
            case .bool(let raw): hide = raw ? 3 : 0
            default: hide = 0
            }
        } else {
            hide = 0
        }
        required = c.bool("Required")
        isPassword = c.bool("IsPassword")
        noPrefix = c.bool("NoPrefix")
        advanced = c.bool("Advanced")
        exclusive = c.bool("Exclusive")
        sensitive = c.bool("Sensitive")
        defaultString = c.string("DefaultStr")
        valueString = c.string("ValueStr")
        type = c.string("Type", "string")
    }
}

public struct RcloneExample: Decodable, Sendable, Hashable, Identifiable {
    public var value: String
    public var help: String
    public var provider: String

    public var id: String { value + "|" + provider }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        value = c.string("Value")
        help = c.string("Help")
        provider = c.string("Provider")
    }
}
