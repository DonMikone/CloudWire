import Foundation

/// Core settings (Appendix E). Missing keys fall back to the documented defaults; `~` is not expanded.
public struct CoreSettings: Decodable, Sendable, Hashable {
    public var autostart: Bool
    public var menuBarIcon: Bool
    public var baseFolder: String
    public var mountFolder: String
    public var quietPeriodSeconds: Int
    public var pollIntervalSeconds: Int
    public var nextcloudEtagSeconds: Int
    public var genericCheckSeconds: Int
    public var safetyFullSyncMinutes: Int
    public var pauseRules: PauseRules
    public var bandwidth: Bandwidth
    public var defaultCacheMaxGB: Int
    public var defaultMountType: MountType
    public var defaultExcludes: [String]
    public var notifications: Notifications
    public var log: Log
    public var updates: Updates
    public var conflictLabel: String

    public static let defaultExcludes = [
        ".DS_Store", "._*", ".Spotlight-V100/**", ".Trashes/**", ".fseventsd/**", ".TemporaryItems/**",
        ".DocumentRevisions-V100/**",
    ]

    /// The documented defaults (Appendix E).
    public static var defaults: CoreSettings {
        // Decoding an empty object yields every default.
        (try? JSONDecoder.coreDecoder.decode(CoreSettings.self, from: Data("{}".utf8)))!
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        autostart = c.bool("autostart", true)
        menuBarIcon = c.bool("menuBarIcon", true)
        baseFolder = c.string("baseFolder", "~/CloudWire")
        mountFolder = c.string("mountFolder", "~/CloudWire/Laufwerke")
        quietPeriodSeconds = c.int("quietPeriodSeconds", 60)
        pollIntervalSeconds = c.int("pollIntervalSeconds", 60)
        nextcloudEtagSeconds = c.int("nextcloudEtagSeconds", 60)
        genericCheckSeconds = c.int("genericCheckSeconds", 300)
        safetyFullSyncMinutes = c.int("safetyFullSyncMinutes", 60)
        pauseRules = c.optional("pauseRules") ?? PauseRules.defaults
        bandwidth = c.optional("bandwidth") ?? Bandwidth.defaults
        defaultCacheMaxGB = c.int("defaultCacheMaxGB", 20)
        defaultMountType = c.value("defaultMountType", .nfsmount)
        defaultExcludes = c.value("defaultExcludes", CoreSettings.defaultExcludes)
        notifications = c.optional("notifications") ?? Notifications.defaults
        log = c.optional("log") ?? Log.defaults
        updates = c.optional("updates") ?? Updates.defaults
        conflictLabel = c.string("conflictLabel")
    }

    public struct PauseRules: Decodable, Sendable, Hashable {
        public var studioMode: StudioMode
        public var battery: Toggle
        public var meteredNetwork: Toggle
        public var cpu: CPU

        static var defaults: PauseRules {
            (try? JSONDecoder.coreDecoder.decode(PauseRules.self, from: Data("{}".utf8)))!
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            studioMode = c.optional("studioMode") ?? StudioMode(enabled: true, apps: [])
            battery = c.optional("battery") ?? Toggle(enabled: true)
            meteredNetwork = c.optional("meteredNetwork") ?? Toggle(enabled: true)
            cpu = c.optional("cpu") ?? CPU(enabled: true, thresholdPercent: 70, windowSeconds: 30)
        }
    }

    public struct StudioMode: Decodable, Sendable, Hashable {
        public var enabled: Bool
        /// Absolute paths of `.app` bundles.
        public var apps: [String]

        init(enabled: Bool, apps: [String]) {
            self.enabled = enabled
            self.apps = apps
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            enabled = c.bool("enabled", true)
            apps = c.value("apps", [String]())
        }
    }

    public struct Toggle: Decodable, Sendable, Hashable {
        public var enabled: Bool

        init(enabled: Bool) { self.enabled = enabled }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            enabled = c.bool("enabled", true)
        }
    }

    public struct CPU: Decodable, Sendable, Hashable {
        public var enabled: Bool
        public var thresholdPercent: Int
        public var windowSeconds: Int

        init(enabled: Bool, thresholdPercent: Int, windowSeconds: Int) {
            self.enabled = enabled
            self.thresholdPercent = thresholdPercent
            self.windowSeconds = windowSeconds
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            enabled = c.bool("enabled", true)
            thresholdPercent = c.int("thresholdPercent", 70)
            windowSeconds = c.int("windowSeconds", 30)
        }
    }

    public struct Bandwidth: Decodable, Sendable, Hashable {
        public var enabled: Bool
        /// MiB/s; 0 = unlimited.
        public var uploadMiBps: Int
        /// MiB/s; 0 = unlimited.
        public var downloadMiBps: Int

        static var defaults: Bandwidth {
            (try? JSONDecoder.coreDecoder.decode(Bandwidth.self, from: Data("{}".utf8)))!
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            enabled = c.bool("enabled", true)
            uploadMiBps = c.int("uploadMiBps", 5)
            downloadMiBps = c.int("downloadMiBps", 20)
        }
    }

    public struct Notifications: Decodable, Sendable, Hashable {
        public var errors: Bool
        public var conflicts: Bool
        public var massDelete: Bool
        public var linkCopied: Bool

        static var defaults: Notifications {
            (try? JSONDecoder.coreDecoder.decode(Notifications.self, from: Data("{}".utf8)))!
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            errors = c.bool("errors", true)
            conflicts = c.bool("conflicts", true)
            massDelete = c.bool("massDelete", true)
            linkCopied = c.bool("linkCopied", true)
        }
    }

    public struct Log: Decodable, Sendable, Hashable {
        public var level: ActivityLevel
        public var retentionDays: Int
        public var maxMB: Int

        static var defaults: Log {
            (try? JSONDecoder.coreDecoder.decode(Log.self, from: Data("{}".utf8)))!
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            level = c.value("level", .info)
            retentionDays = c.int("retentionDays", 30)
            maxMB = c.int("maxMB", 50)
        }
    }

    public struct Updates: Decodable, Sendable, Hashable {
        public var check: Bool
        public var skippedVersion: String

        static var defaults: Updates {
            (try? JSONDecoder.coreDecoder.decode(Updates.self, from: Data("{}".utf8)))!
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: AnyKey.self)
            check = c.bool("check", true)
            skippedVersion = c.string("skippedVersion")
        }
    }
}
