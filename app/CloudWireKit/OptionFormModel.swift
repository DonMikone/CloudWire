import Foundation

/// Which rclone `Hide` bit applies to a form.
public enum OptionHideContext: Sendable, Hashable {
    /// Connection configuration (`config/providers` options): hidden when bit 2 is set.
    case configurator
    /// Flag blocks (`vfs`, `mount`, `nfs`, `main`): hidden when bit 1 is set.
    case commandLine

    var mask: Int {
        switch self {
        case .configurator: return 2
        case .commandLine: return 1
        }
    }
}

/// How a field is edited and serialised, derived from the option `Type`.
public enum OptionFieldKind: Sendable, Hashable {
    case bool
    case integer(unsigned: Bool)
    case float
    case sizeSuffix
    case duration
    case text
    case password
    case commaList
    case spaceList
    case tristate
    /// Choices from `Examples`; `exclusive` means only those values are valid.
    case choice(exclusive: Bool)
}

public struct OptionValidationError: Error, Sendable, Hashable, LocalizedError {
    public enum Reason: Sendable, Hashable {
        case required
        case invalidBool
        case invalidInteger
        case invalidNumber
        case invalidSize
        case invalidDuration
        case notAChoice
    }

    public let optionName: String
    public let reason: Reason

    public init(optionName: String, reason: Reason) {
        self.optionName = optionName
        self.reason = reason
    }

    public var errorDescription: String? {
        switch reason {
        case .required: return "\(optionName) is required."
        case .invalidBool: return "\(optionName) must be true or false."
        case .invalidInteger: return "\(optionName) must be a whole number."
        case .invalidNumber: return "\(optionName) must be a number."
        case .invalidSize: return "\(optionName) must be a size such as 100M, 20G or off."
        case .invalidDuration: return "\(optionName) must be a duration such as 30s, 5m, 1h30m or off."
        case .notAChoice: return "\(optionName) must be one of the listed values."
        }
    }
}

/// Form state over a list of rclone options: provider conditions, visibility, required fields and
/// serialisation of the entered text to rclone parameter strings or typed JSON.
public struct OptionFormModel: Sendable, Hashable {
    public let options: [RcloneOption]
    public let hideContext: OptionHideContext
    /// Name of the option whose value selects the provider (rclone uses `provider`).
    public var providerOptionName: String
    /// Entered text per option name. Options without an entry use their initial value.
    public private(set) var values: [String: String]

    public init(options: [RcloneOption], hideContext: OptionHideContext = .configurator,
                initialValues: [String: String] = [:], providerOptionName: String = "provider")
    {
        self.options = options
        self.hideContext = hideContext
        self.providerOptionName = providerOptionName
        self.values = initialValues
    }

    // MARK: Provider conditions

    /// The provider value currently selected in the form, if any.
    public var provider: String? {
        guard let option = options.first(where: { $0.name == providerOptionName }) else { return nil }
        let value = text(for: option).trimmingCharacters(in: .whitespaces)
        return value.isEmpty ? nil : value
    }

    /// rclone's `fs.MatchProvider`: an empty condition or an unknown provider matches everything;
    /// `"A,B"` matches only A or B; `"!A,B"` matches everything except A and B.
    public static func matchesProvider(_ condition: String, provider: String?) -> Bool {
        guard let provider, !provider.isEmpty, !condition.isEmpty else { return true }
        var list = Substring(condition)
        var negate = false
        if list.hasPrefix("!") {
            negate = true
            list = list.dropFirst()
        }
        let matched = list.split(separator: ",").contains { $0.trimmingCharacters(in: .whitespaces) == provider }
        return matched != negate
    }

    public func appliesToProvider(_ option: RcloneOption) -> Bool {
        Self.matchesProvider(option.provider, provider: provider)
    }

    // MARK: Visibility

    public func isHidden(_ option: RcloneOption) -> Bool {
        option.hide & hideContext.mask != 0
    }

    /// Options shown in the form: not hidden, matching the provider, and in the requested group
    /// (`advanced == nil` returns both groups).
    public func visibleOptions(advanced: Bool? = nil) -> [RcloneOption] {
        options.filter { option in
            !isHidden(option) && appliesToProvider(option) && (advanced == nil || option.advanced == advanced)
        }
    }

    /// Examples that apply to the selected provider.
    public func examples(for option: RcloneOption) -> [RcloneExample] {
        option.examples.filter { Self.matchesProvider($0.provider, provider: provider) }
    }

    public static func kind(for option: RcloneOption) -> OptionFieldKind {
        if option.isPassword { return .password }
        switch option.type {
        case "bool": return .bool
        case "int", "int8", "int16", "int32", "int64": return .integer(unsigned: false)
        case "uint", "uint8", "uint16", "uint32", "uint64": return .integer(unsigned: true)
        case "float32", "float64": return .float
        case "SizeSuffix": return .sizeSuffix
        case "Duration": return .duration
        case "CommaSepList", "[]string", "stringArray", "stringSlice": return .commaList
        case "SpaceSepList": return .spaceList
        case "Tristate": return .tristate
        default:
            return option.examples.isEmpty ? .text : .choice(exclusive: option.exclusive)
        }
    }

    // MARK: Values

    /// The text shown for an option: the entered text, or the initial value (never for passwords).
    /// Flag blocks start from rclone's default, not from the Core process's current global value,
    /// so an untouched form never turns global settings into per-item overrides.
    public func text(for option: RcloneOption) -> String {
        if let entered = values[option.name] { return entered }
        if option.isPassword { return "" }
        return hideContext == .commandLine ? option.defaultText : option.initialString
    }

    public func text(forName name: String) -> String {
        if let option = options.first(where: { $0.name == name }) { return text(for: option) }
        return values[name] ?? ""
    }

    public mutating func setText(_ text: String, for option: RcloneOption) {
        values[option.name] = text
    }

    public mutating func setText(_ text: String, forName name: String) {
        values[name] = text
    }

    /// Forgets the entered text so the option falls back to its initial value.
    public mutating func reset(_ option: RcloneOption) {
        values.removeValue(forKey: option.name)
    }

    /// Normalises the current text of `option`; throws when it does not parse for its type.
    public func normalizedValue(of option: RcloneOption) throws -> String {
        try Self.normalize(text(for: option), for: option, examples: examples(for: option))
    }

    /// Normalises `raw` to the string rclone expects for the option's type.
    public static func normalize(_ raw: String, for option: RcloneOption, examples: [RcloneExample]? = nil)
        throws -> String
    {
        let kind = kind(for: option)
        if kind == .password { return raw }
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        func fail(_ reason: OptionValidationError.Reason) -> OptionValidationError {
            OptionValidationError(optionName: option.name, reason: reason)
        }
        switch kind {
        case .bool:
            if trimmed.isEmpty { return "" }
            switch trimmed.lowercased() {
            case "true", "yes", "on", "1": return "true"
            case "false", "no", "off", "0": return "false"
            default: throw fail(.invalidBool)
            }
        case .tristate:
            switch trimmed.lowercased() {
            case "", "unset": return ""
            case "true", "yes", "on", "1": return "true"
            case "false", "no", "off", "0": return "false"
            default: throw fail(.invalidBool)
            }
        case .integer(let unsigned):
            if trimmed.isEmpty { return "" }
            guard let number = Int64(trimmed) else { throw fail(.invalidInteger) }
            if unsigned && number < 0 { throw fail(.invalidInteger) }
            if option.type == "uint32" && number > Int64(UInt32.max) { throw fail(.invalidInteger) }
            return String(number)
        case .float:
            if trimmed.isEmpty { return "" }
            guard let number = Double(trimmed), number.isFinite else { throw fail(.invalidNumber) }
            return trimmed
        case .sizeSuffix:
            if trimmed.isEmpty { return "" }
            let compact = trimmed.replacingOccurrences(of: " ", with: "")
            guard compact.wholeMatch(of: sizePattern) != nil else { throw fail(.invalidSize) }
            return compact.lowercased() == "off" ? "off" : compact
        case .duration:
            if trimmed.isEmpty { return "" }
            let compact = trimmed.replacingOccurrences(of: " ", with: "")
            guard compact.wholeMatch(of: durationPattern) != nil else { throw fail(.invalidDuration) }
            return compact.lowercased() == "off" ? "off" : compact
        case .commaList:
            return trimmed.split(separator: ",")
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .filter { !$0.isEmpty }
                .joined(separator: ",")
        case .spaceList:
            return trimmed.split(whereSeparator: { $0.isWhitespace }).joined(separator: " ")
        case .choice(let exclusive):
            if exclusive && !trimmed.isEmpty {
                let allowed = examples ?? option.examples
                guard allowed.contains(where: { $0.value == trimmed }) else { throw fail(.notAChoice) }
            }
            return trimmed
        case .text, .password:
            return trimmed
        }
    }

    /// Normalised default of `option`, used to decide whether a value differs from rclone's default.
    static func normalizedDefault(of option: RcloneOption) -> String {
        let raw = option.defaultText
        return (try? normalize(raw, for: option, examples: option.examples)) ?? raw
    }

    // MARK: Validation

    /// Required options (visible and matching the provider) that have no value.
    public func missingRequired() -> [RcloneOption] {
        options.filter { option in
            guard option.required, appliesToProvider(option), !isHidden(option) else { return false }
            let value = (try? normalizedValue(of: option)) ?? text(for: option)
            return value.isEmpty
        }
    }

    /// The first validation problem of `option`, if any.
    public func validationError(for option: RcloneOption) -> OptionValidationError? {
        do {
            let value = try normalizedValue(of: option)
            if option.required && value.isEmpty && appliesToProvider(option) && !isHidden(option) {
                return OptionValidationError(optionName: option.name, reason: .required)
            }
            return nil
        } catch let error as OptionValidationError {
            return error
        } catch {
            return nil
        }
    }

    /// Throws the first validation error over all options that apply.
    public func validate() throws {
        for option in options where appliesToProvider(option) {
            if let error = validationError(for: option) { throw error }
        }
    }

    // MARK: Serialisation

    /// Options whose value should be sent: they apply to the provider and differ from rclone's
    /// default, or are non-empty passwords.
    private func changedOptions() throws -> [(RcloneOption, String)] {
        try validate()
        var result: [(RcloneOption, String)] = []
        for option in options where appliesToProvider(option) {
            let value = try normalizedValue(of: option)
            if option.isPassword {
                if !value.isEmpty { result.append((option, value)) }
                continue
            }
            if Self.kind(for: option) == .tristate && value.isEmpty { continue }
            // Defaults (also of required options) are left to rclone.
            if value != Self.normalizedDefault(of: option) {
                result.append((option, value))
            }
        }
        return result
    }

    /// rclone parameters as strings keyed by option name (connection parameters, Mount overrides).
    /// Only values that differ from the default are included.
    public func parameters() throws -> [String: String] {
        var result: [String: String] = [:]
        for (option, value) in try changedOptions() {
            result[option.name] = value
        }
        return result
    }

    /// Parameters that turn `stored` (a saved config) into the form's values: changed values, plus
    /// rclone's default for every stored non-default value the user reset, which `parameters()`
    /// omits. Passwords are sent only when entered, so an empty field keeps the stored secret.
    public func changedParameters(from stored: [String: String]) throws -> [String: String] {
        let current = try parameters()
        var result = current.filter { stored[$0.key] != $0.value }
        for option in options where appliesToProvider(option) && !option.isPassword && current[option.name] == nil {
            guard let old = stored[option.name], !old.isEmpty else { continue }
            let reset = Self.normalizedDefault(of: option)
            if ((try? Self.normalize(old, for: option, examples: option.examples)) ?? old) != reset {
                result[option.name] = reset
            }
        }
        return result
    }

    public enum KeyStyle: Sendable {
        case name
        case fieldName
    }

    /// Typed JSON values (bool, number or string) for `_config`-style overrides such as the
    /// advanced main options of an Offline Item.
    public func typedValues(keyedBy style: KeyStyle = .fieldName) throws -> [String: JSONValue] {
        var result: [String: JSONValue] = [:]
        for (option, value) in try changedOptions() {
            let key = style == .fieldName && !option.fieldName.isEmpty ? option.fieldName : option.name
            result[key] = Self.typed(value, kind: Self.kind(for: option))
        }
        return result
    }

    static func typed(_ value: String, kind: OptionFieldKind) -> JSONValue {
        switch kind {
        case .bool, .tristate:
            return .bool(value == "true")
        case .integer:
            return Int64(value).map(JSONValue.int) ?? .string(value)
        case .float:
            return Double(value).map(JSONValue.double) ?? .string(value)
        default:
            return .string(value)
        }
    }

    /// Converts typed overrides (as stored by the Core) back to form text.
    public static func textValues(fromTyped typed: [String: JSONValue], options: [RcloneOption],
                                  keyedBy style: KeyStyle = .fieldName) -> [String: String]
    {
        var result: [String: String] = [:]
        for option in options {
            let key = style == .fieldName && !option.fieldName.isEmpty ? option.fieldName : option.name
            if let value = typed[key] { result[option.name] = value.displayString }
        }
        return result
    }
}

// rclone SizeSuffix: a number with an optional binary suffix (b, k, M, G, T, P, E; optional "i"/"iB"), or "off".
nonisolated(unsafe) private let sizePattern = /(?i:off)|[0-9]+(\.[0-9]+)?([bBkKmMgGtTpPeE][iI]?[bB]?)?/
// rclone Duration: "off", a bare number (seconds), or number+unit sequences (Go units plus d, w, M, y).
nonisolated(unsafe) private let durationPattern =
    /(?i:off)|[0-9]+(\.[0-9]+)?|([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h|d|w|M|y))+/
