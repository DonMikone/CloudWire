import Darwin
import Foundation

/// File-system locations shared with the Core. Every path is derived from the real home directory
/// (not the sandbox container of the Finder extension), or from `$CLOUDWIRE_HOME` when set.
public enum CorePaths {
    public static let bundleIdentifier = "io.github.donmikone.cloudwire"
    public static let finderExtensionIdentifier = "io.github.donmikone.cloudwire.finder"
    public static let launchAgentLabel = "io.github.donmikone.cloudwire.core"
    public static let launchAgentPlistName = "io.github.donmikone.cloudwire.core.plist"
    public static let urlScheme = "cloudwire"

    /// The user's real home directory. Inside the App Sandbox `NSHomeDirectory()` points into the
    /// container, so the password database is consulted instead.
    public static var realHome: String {
        if let entry = getpwuid(getuid()), let dir = entry.pointee.pw_dir {
            return String(cString: dir)
        }
        return NSHomeDirectory()
    }

    /// Root under which all CloudWire paths live (`$CLOUDWIRE_HOME` or the real home).
    public static var root: String {
        if let override = ProcessInfo.processInfo.environment["CLOUDWIRE_HOME"], !override.isEmpty {
            return override
        }
        return realHome
    }

    public static var applicationSupport: URL {
        URL(fileURLWithPath: root, isDirectory: true)
            .appendingPathComponent("Library/Application Support/CloudWire", isDirectory: true)
    }

    public static var socketPath: String {
        applicationSupport.appendingPathComponent("core.sock").path
    }

    public static var runDirectory: URL {
        applicationSupport.appendingPathComponent("run", isDirectory: true)
    }

    public static var startRequestFile: URL {
        runDirectory.appendingPathComponent("start-request")
    }

    public static var logsDirectory: URL {
        URL(fileURLWithPath: root, isDirectory: true).appendingPathComponent("Library/Logs/CloudWire", isDirectory: true)
    }

    public static var userLaunchAgentPlist: URL {
        URL(fileURLWithPath: realHome, isDirectory: true)
            .appendingPathComponent("Library/LaunchAgents/\(launchAgentPlistName)")
    }

    /// Expands a leading `~` the way the Core does (against `root`).
    public static func expandTilde(_ path: String) -> String {
        if path == "~" { return root }
        if path.hasPrefix("~/") { return root + path.dropFirst(1) }
        return path
    }

    /// Replaces the `root` prefix with `~` for display.
    public static func abbreviate(_ path: String) -> String {
        let home = root
        if path == home { return "~" }
        if path.hasPrefix(home + "/") { return "~" + path.dropFirst(home.count) }
        return path
    }
}
