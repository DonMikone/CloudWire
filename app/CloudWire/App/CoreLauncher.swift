import CloudWireKit
import Foundation
import Security
import ServiceManagement
import os

enum CoreLauncherError: Error, LocalizedError {
    /// The background item is registered but the user has not allowed it yet.
    case requiresApproval
    case unreachable(String)

    var errorDescription: String? {
        switch self {
        case .requiresApproval:
            return String(localized: "CloudWire needs your permission to run in the background.")
        case .unreachable(let detail):
            return String(localized: "The CloudWire background service does not respond. \(detail)")
        }
    }
}

/// Starts the Core LaunchAgent and connects to it.
///
/// 1. Registers the bundled agent with `SMAppService` (fallback for ad-hoc builds that
///    `SMAppService` rejects: a user LaunchAgent plist loaded with `launchctl bootstrap`).
/// 2. Touches `run/start-request` so the Core's autostart gate lets it start.
/// 3. `launchctl kickstart`, then connects with retries (100 ms doubling to 1 s, 10 s total).
/// 4. Restarts the Core once when its version differs from the App's.
@MainActor
enum CoreLauncher {
    private static let log = Logger(subsystem: CorePaths.bundleIdentifier, category: "CoreLauncher")
    private static var versionRestartDone = false

    static var agentService: SMAppService {
        SMAppService.agent(plistName: CorePaths.launchAgentPlistName)
    }

    static var appVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
    }

    static var needsApproval: Bool { agentService.status == .requiresApproval }

    /// With `CLOUDWIRE_HOME` set (tests, E2E, development) the App only connects to the Core started
    /// for that home (`cloudwire-core serve --force`) and never touches launchd or login items.
    static var isIsolatedHome: Bool {
        !(ProcessInfo.processInfo.environment["CLOUDWIRE_HOME"] ?? "").isEmpty
    }

    static func ensureRunning(client: CoreClient) async throws -> CoreInfo {
        if await client.isConnected, let info = try? await client.coreInfo() {
            return isIsolatedHome ? info : try await checkVersion(info, client: client)
        }
        if isIsolatedHome {
            return try await connectWithRetries(client)
        }
        try registerAgent()
        let info = try await startAndConnect(client)
        return try await checkVersion(info, client: client)
    }

    /// Kickstarts the agent and connects. When launchd cannot start the `SMAppService` agent (it
    /// resolves the app by bundle identifier, which fails when several copies of CloudWire are
    /// registered or the bundle was replaced), the Core is started from a user LaunchAgent with an
    /// absolute path instead. `allowFallback: false` only throws, so the caller's retry loop runs.
    private static func startAndConnect(_ client: CoreClient, allowFallback: Bool = true) async throws -> CoreInfo {
        touchStartRequest()
        await kickstart()
        do {
            return try await connectWithRetries(client)
        } catch {
            guard allowFallback, agentService.status == .enabled else { throw error }
            log.error("The Core did not start as SMAppService agent; using a user LaunchAgent")
            try? await agentService.unregister()
            UserDefaults.standard.removeObject(forKey: registeredHashKey)
            // Stick to the fallback for this build; a new build tries SMAppService again.
            UserDefaults.standard.set(coreCDHash(), forKey: fallbackHashKey)
            try installFallbackAgent()
            touchStartRequest()
            await kickstart()
            return try await connectWithRetries(client)
        }
    }

    // MARK: Registration

    /// UserDefaults key holding the code directory hash of the `cloudwire-core` that was registered.
    private static let registeredHashKey = "registeredCoreCDHash"
    /// UserDefaults key holding the hash of a `cloudwire-core` build that needs the user LaunchAgent.
    private static let fallbackHashKey = "fallbackCoreCDHash"

    static func registerAgent() throws {
        let service = agentService
        let currentHash = coreCDHash()
        if let currentHash, UserDefaults.standard.string(forKey: fallbackHashKey) == currentHash {
            try installFallbackAgent()
            return
        }
        switch service.status {
        case .enabled:
            // Background Task Management pins the registered executable. An ad-hoc signed update
            // has a new code identity, and launchd then refuses to spawn it (EX_CONFIG) until the
            // agent is registered again.
            let registeredHash = UserDefaults.standard.string(forKey: registeredHashKey)
            if currentHash == nil || registeredHash == currentHash {
                removeFallbackAgentIfPresent()
                return
            }
            log.notice("cloudwire-core changed since registration; registering the agent again")
            try? service.unregister()
        case .requiresApproval:
            throw CoreLauncherError.requiresApproval
        default:
            break
        }
        do {
            try service.register()
            UserDefaults.standard.set(currentHash, forKey: registeredHashKey)
            if service.status == .requiresApproval { throw CoreLauncherError.requiresApproval }
            removeFallbackAgentIfPresent()
        } catch let error as CoreLauncherError {
            throw error
        } catch {
            if service.status == .requiresApproval { throw CoreLauncherError.requiresApproval }
            log.error("SMAppService.register failed: \(error.localizedDescription, privacy: .public); using launchctl")
            try installFallbackAgent()
        }
    }

    /// The code directory hash of the bundled `cloudwire-core`, identifying this exact build.
    static func coreCDHash() -> String? {
        guard let url = Bundle.main.url(forAuxiliaryExecutable: "cloudwire-core") else { return nil }
        var staticCode: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, [], &staticCode) == errSecSuccess, let staticCode else {
            return nil
        }
        var info: CFDictionary?
        guard SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &info) == errSecSuccess,
              let dict = info as? [String: Any], let hash = dict[kSecCodeInfoUnique as String] as? Data
        else { return nil }
        return hash.map { String(format: "%02x", $0) }.joined()
    }

    /// A LaunchAgent plist in `~/Library/LaunchAgents` with an absolute `Program` path, for builds
    /// that `SMAppService` refuses (ad-hoc signatures).
    static func installFallbackAgent() throws {
        guard let program = Bundle.main.url(forAuxiliaryExecutable: "cloudwire-core")?.path else {
            throw CoreLauncherError.unreachable(String(localized: "cloudwire-core is missing from the app bundle."))
        }
        let plist: [String: Any] = [
            "Label": CorePaths.launchAgentLabel,
            "Program": program,
            "ProgramArguments": [program, "serve"],
            "RunAtLoad": true,
            "KeepAlive": ["SuccessfulExit": false],
            "ProcessType": "Standard",
            "ThrottleInterval": 10,
            "AssociatedBundleIdentifiers": [CorePaths.bundleIdentifier],
        ]
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        let url = CorePaths.userLaunchAgentPlist
        let existing = try? Data(contentsOf: url)
        if existing != data {
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            if existing != nil {
                runLaunchctl(["bootout", "gui/\(getuid())/\(CorePaths.launchAgentLabel)"])
            }
            try data.write(to: url, options: .atomic)
        }
        // Exit status 5/37 means "already loaded"; both are fine.
        runLaunchctl(["bootstrap", "gui/\(getuid())", url.path])
    }

    static func removeFallbackAgentIfPresent() {
        let url = CorePaths.userLaunchAgentPlist
        guard FileManager.default.fileExists(atPath: url.path) else { return }
        runLaunchctl(["bootout", "gui/\(getuid())", url.path])
        try? FileManager.default.removeItem(at: url)
    }

    /// Autostart ON and menu bar icon ON register the App as login item; otherwise it is removed.
    static func updateLoginItem(enabled: Bool) {
        guard !isIsolatedHome else { return }
        let service = SMAppService.mainApp
        do {
            if enabled, service.status != .enabled {
                try service.register()
            } else if !enabled, service.status == .enabled {
                try service.unregister()
            }
        } catch {
            log.error("Login item update failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    #if DEBUG
    /// `--reset-registration`: removes both background registrations.
    static func resetRegistration() {
        try? agentService.unregister()
        try? SMAppService.mainApp.unregister()
        removeFallbackAgentIfPresent()
        UserDefaults.standard.removeObject(forKey: registeredHashKey)
        UserDefaults.standard.removeObject(forKey: fallbackHashKey)
    }
    #endif

    // MARK: Start

    static func touchStartRequest() {
        let fm = FileManager.default
        let run = CorePaths.runDirectory
        try? fm.createDirectory(at: run, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let file = CorePaths.startRequestFile
        if fm.fileExists(atPath: file.path) {
            try? fm.setAttributes([.modificationDate: Date()], ofItemAtPath: file.path)
        } else {
            fm.createFile(atPath: file.path, contents: Data(), attributes: [.posixPermissions: 0o600])
        }
    }

    static func kickstart() async {
        await runLaunchctlAsync(["kickstart", "gui/\(getuid())/\(CorePaths.launchAgentLabel)"])
    }

    static func connectWithRetries(_ client: CoreClient) async throws -> CoreInfo {
        let deadline = ContinuousClock.now + .seconds(10)
        var delay: Duration = .milliseconds(100)
        var lastError: any Error = CoreError.notConnected()
        while ContinuousClock.now < deadline {
            do {
                try await client.connect()
                return try await client.coreInfo()
            } catch {
                lastError = error
                await client.disconnect()
                try await Task.sleep(for: delay)
                delay = min(delay * 2, .seconds(1))
            }
        }
        throw CoreLauncherError.unreachable(ErrorText.alert(for: lastError).message)
    }

    private static func checkVersion(_ info: CoreInfo, client: CoreClient) async throws -> CoreInfo {
        guard info.version != appVersion, !versionRestartDone, !appVersion.isEmpty else { return info }
        versionRestartDone = true
        log.notice("Core \(info.version, privacy: .public) differs from App \(appVersion, privacy: .public); restarting")
        try? await client.shutdown()
        await client.disconnect()
        // Stopping sync jobs and Mounts can take a while; kickstart is a no-op while the old Core runs.
        let exited = try await waitForExit(pid: info.pid, timeout: .seconds(45))
        if !exited {
            log.error("The previous Core (pid \(info.pid)) is still running; starting anyway")
        }
        try registerAgent()
        // A failure here says nothing about launchd refusing the agent: no user LaunchAgent fallback.
        return try await startAndConnect(client, allowFallback: false)
    }

    /// Polls until process `pid` is gone (`kill(pid, 0)` fails with `ESRCH`); false on timeout.
    private static func waitForExit(pid: Int, timeout: Duration) async throws -> Bool {
        guard pid > 0 else { return true }
        let deadline = ContinuousClock.now + timeout
        while kill(pid_t(pid), 0) == 0 || errno != ESRCH {
            guard ContinuousClock.now < deadline else { return false }
            try await Task.sleep(for: .milliseconds(200))
        }
        return true
    }

    // MARK: launchctl

    @discardableResult
    private static func runLaunchctl(_ arguments: [String]) -> Int32 {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = arguments
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
            process.waitUntilExit()
            return process.terminationStatus
        } catch {
            return -1
        }
    }

    private static func runLaunchctlAsync(_ arguments: [String]) async {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = arguments
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            process.terminationHandler = { _ in continuation.resume() }
            do {
                try process.run()
            } catch {
                process.terminationHandler = nil
                continuation.resume()
            }
        }
    }
}
