import Foundation

/// A Finder → App action: `cloudwire://action?name=<name>&path=<abs path>[&path=…]&token=<token>`.
public struct ActionURL: Sendable, Hashable {
    public enum Name: String, Sendable, CaseIterable {
        case share
        case manageShares
        case copyPublicLink
        case copyInternalLink
        case makeOffline
        case removeOffline
        case encrypt
        case openWeb

        /// Actions that create or change state and therefore need a valid token or a confirmation.
        public var isStateChanging: Bool {
            switch self {
            case .copyPublicLink, .makeOffline, .removeOffline, .encrypt: return true
            case .share, .manageShares, .copyInternalLink, .openWeb: return false
            }
        }

        /// Whether the action takes several paths (files of one folder); all others act on one item.
        public var acceptsSeveralPaths: Bool { self == .makeOffline }
    }

    public static let scheme = "cloudwire"
    public static let host = "action"

    public var name: Name
    public var paths: [String]
    public var token: String?

    public init(name: Name, paths: [String], token: String? = nil) {
        self.name = name
        self.paths = paths
        self.token = token
    }

    /// Why `standardized()` rejected an action.
    public enum PathProblem: Error, Sendable, Hashable {
        case notAbsolute(String)
        case severalPaths
    }

    /// The action with every path standardised (`.`, `..` and repeated slashes resolved). A trailing
    /// `/` (folders marked by the Finder extension) is kept. Throws for relative paths and for
    /// several paths on actions that act on one item.
    public func standardized() throws -> ActionURL {
        if paths.count > 1 && !name.acceptsSeveralPaths { throw PathProblem.severalPaths }
        var result = self
        result.paths = try paths.map { raw in
            guard raw.hasPrefix("/") else { throw PathProblem.notAbsolute(raw) }
            let path = (raw as NSString).standardizingPath
            return raw.hasSuffix("/") && path != "/" ? path + "/" : path
        }
        return result
    }

    /// Characters left unescaped in query values. Everything else (including `&`, `=`, `+`, `#`,
    /// spaces and non-ASCII) is percent-encoded as UTF-8.
    private static let valueAllowed: CharacterSet = {
        var set = CharacterSet()
        set.insert(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~/")
        return set
    }()

    private static func encode(_ value: String) -> String {
        value.addingPercentEncoding(withAllowedCharacters: valueAllowed) ?? value
    }

    public var url: URL {
        var components = URLComponents()
        components.scheme = Self.scheme
        components.host = Self.host
        var items = [URLQueryItem(name: "name", value: name.rawValue)]
        items += paths.map { URLQueryItem(name: "path", value: Self.encode($0)) }
        if let token, !token.isEmpty {
            items.append(URLQueryItem(name: "token", value: Self.encode(token)))
        }
        components.percentEncodedQueryItems = items
        // Components built from valid percent-encoded parts always form a URL.
        return components.url!
    }

    /// Parses a `cloudwire://action` URL; returns nil for other URLs, unknown names or no paths.
    public init?(url: URL) {
        guard url.scheme?.lowercased() == Self.scheme,
            let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
            components.host?.lowercased() == Self.host,
            let items = components.percentEncodedQueryItems
        else { return nil }

        var name: Name?
        var paths: [String] = []
        var token: String?
        for item in items {
            guard let raw = item.value, let value = raw.removingPercentEncoding else { continue }
            switch item.name {
            case "name": name = Name(rawValue: value)
            case "path": if !value.isEmpty { paths.append(value) }
            case "token": token = value.isEmpty ? nil : value
            default: continue
            }
        }
        guard let name, !paths.isEmpty else { return nil }
        self.init(name: name, paths: paths, token: token)
    }
}
