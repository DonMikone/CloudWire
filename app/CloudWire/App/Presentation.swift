import CloudWireKit
import SwiftUI

// MARK: - Error presentation

/// A message shown in an alert.
struct AlertContent: Identifiable {
    let id = UUID()
    let title: String
    let message: String
}

enum ErrorText {
    /// Localised headline for an error; the Core's own message becomes the detail.
    static func alert(for error: any Error, title: String? = nil) -> AlertContent {
        if let core = error as? CoreError {
            return AlertContent(title: title ?? headline(for: core), message: detail(for: core))
        }
        if let validation = error as? OptionValidationError {
            return AlertContent(title: title ?? String(localized: "Please check your input"),
                                message: message(for: validation))
        }
        return AlertContent(title: title ?? String(localized: "Something went wrong"),
                            message: error.localizedDescription)
    }

    /// The Core's detail sentence in the user's language (raw rclone text stays as it is); errors without a
    /// message code keep their own message.
    static func detail(for error: CoreError) -> String {
        if ["connection.inUse", "vault.inUse"].contains(error.code), !error.dependents.isEmpty {
            let list = error.dependents.map { "\($0.name) (\(dependentKind($0.kind)))" }.joined(separator: ", ")
            return String(localized: "Still used by: \(list). Remove these first.")
        }
        return error.text?.localized() ?? error.message
    }

    static func dependentKind(_ kind: String) -> String {
        switch kind {
        case "mount": return String(localized: "Mount")
        case "offline": return String(localized: "Offline Item")
        case "vault": return String(localized: "Vault")
        default: return kind
        }
    }

    static func headline(for error: CoreError) -> String {
        switch error.code {
        case "connection.notFound": return String(localized: "Connection not found")
        case "connection.nameTaken": return String(localized: "This name is already in use")
        case "connection.nameReserved": return String(localized: "This name is reserved")
        case "connection.testFailed": return String(localized: "The connection test failed")
        case "connection.inUse": return String(localized: "The connection is still in use")
        case "mount.notFound": return String(localized: "Mount not found")
        case "mount.pointNotEmpty": return String(localized: "The mount point is not empty")
        case "mount.pointInUse": return String(localized: "The mount point is already in use")
        case "mount.fuseUnavailable": return String(localized: "FUSE is not installed")
        case "mount.failed": return String(localized: "The drive could not be mounted")
        case "offline.notFound": return String(localized: "Offline Item not found")
        case "offline.overlap": return String(localized: "This overlaps an existing Offline Item")
        case "offline.insufficientSpace": return String(localized: "Not enough free space")
        case "offline.storageNotEmpty": return String(localized: "The storage location is not empty")
        case "offline.locationMissing": return String(localized: "The storage location is missing")
        case "offline.vaultLocked": return String(localized: "The Vault is locked")
        case "offline.vaultFolder": return String(localized: "This is an encrypted Vault folder")
        case "share.unsupported": return String(localized: "Sharing is not supported here")
        case "share.serverPolicy": return String(localized: "The server's sharing policy requires more settings")
        case "share.failed": return String(localized: "Sharing failed")
        case "share.notFound": return String(localized: "Share not found")
        case "vault.notFound": return String(localized: "Vault not found")
        case "vault.wrongPassword": return String(localized: "Wrong password")
        case "vault.locked": return String(localized: "The Vault is locked")
        case "vault.exists": return String(localized: "A Vault with this name already exists")
        case "vault.invalidFormat": return String(localized: "This is not a valid CloudWire Vault")
        case "vault.sourceIsOffline": return String(localized: "Offline Items cannot be encrypted in place")
        case "vault.ioFailed": return String(localized: "The Vault could not be read or written")
        case "vault.deleteFailed": return String(localized: "The original could not be deleted completely")
        case "vault.inUse": return String(localized: "The Vault is still in use")
        case "vault.alreadyAdded": return String(localized: "This Vault is already in CloudWire")
        case "client.selection": return String(localized: "This selection is not supported")
        case "rpc.-32602": return String(localized: "Please check your input")
        case CoreError.notConnectedCode, CoreError.disconnectedCode, CoreError.socketCode, CoreError.timeoutCode:
            return String(localized: "CloudWire's background service is not reachable")
        default: return String(localized: "Something went wrong")
        }
    }

    static func message(for error: OptionValidationError) -> String {
        let name = error.optionName
        switch error.reason {
        case .required: return String(localized: "“\(name)” is required.")
        case .invalidBool: return String(localized: "“\(name)” must be true or false.")
        case .invalidInteger: return String(localized: "“\(name)” must be a whole number.")
        case .invalidNumber: return String(localized: "“\(name)” must be a number.")
        case .invalidSize: return String(localized: "“\(name)” must be a size such as 100M, 20G or off.")
        case .invalidDuration: return String(localized: "“\(name)” must be a duration such as 30s, 5m, 1h30m or off.")
        case .notAChoice: return String(localized: "“\(name)” must be one of the listed values.")
        }
    }
}

// MARK: - Labels

extension MountState {
    var label: String {
        switch self {
        case .unmounted: return String(localized: "Not mounted")
        case .mounting: return String(localized: "Mounting…")
        case .mounted: return String(localized: "Mounted")
        case .error: return String(localized: "Error")
        default: return rawValue
        }
    }

    var color: Color {
        switch self {
        case .mounted: return .green
        case .mounting: return .orange
        case .error: return .red
        default: return .secondary
        }
    }
}

extension MountType {
    var label: String {
        switch self {
        case .nfsmount: return String(localized: "NFS (built in)")
        case .cmount: return String(localized: "FUSE")
        default: return rawValue
        }
    }
}

extension OfflineState {
    var label: String {
        switch self {
        case .pending: return String(localized: "Waiting")
        case .syncing: return String(localized: "Syncing…")
        case .idle: return String(localized: "Up to date")
        case .paused: return String(localized: "Paused")
        case .error: return String(localized: "Error")
        case .needsConfirmation: return String(localized: "Needs confirmation")
        default: return rawValue
        }
    }

    var color: Color {
        switch self {
        case .idle: return .green
        case .syncing: return .orange
        case .pending, .paused: return .secondary
        case .error, .needsConfirmation: return .red
        default: return .secondary
        }
    }

    var symbol: String {
        switch self {
        case .idle: return "checkmark.circle.fill"
        case .syncing: return "arrow.triangle.2.circlepath.circle.fill"
        case .pending: return "clock.fill"
        case .paused: return "pause.circle.fill"
        case .error: return "exclamationmark.circle.fill"
        case .needsConfirmation: return "exclamationmark.triangle.fill"
        default: return "circle"
        }
    }
}

enum PauseReason {
    /// Localised text for a pause rule id or Offline Item pause reason.
    static func label(_ id: String) -> String {
        switch id {
        case "studioMode": return String(localized: "Studio Mode")
        case "battery": return String(localized: "Battery power")
        case "meteredNetwork": return String(localized: "Personal hotspot or Low Data Mode")
        case "cpu": return String(localized: "High CPU load")
        case "manual": return String(localized: "Paused manually")
        case "location-missing": return String(localized: "Storage location missing")
        case "vault-locked": return String(localized: "Vault locked")
        default: return id
        }
    }
}

extension ActivityLevel {
    var label: String {
        switch self {
        case .debug: return String(localized: "Debug")
        case .info: return String(localized: "Info")
        case .warn: return String(localized: "Warning")
        case .error: return String(localized: "Error")
        default: return rawValue
        }
    }

    var color: Color {
        switch self {
        case .error: return .red
        case .warn: return .orange
        case .debug: return .secondary
        default: return .primary
        }
    }
}

extension ActivityCategory {
    var label: String {
        switch self {
        case .core: return String(localized: "Background Service")
        case .mount: return String(localized: "Mounts")
        case .offline: return String(localized: "Offline")
        case .sync: return String(localized: "Sync")
        case .share: return String(localized: "Shares")
        case .vault: return String(localized: "Vaults")
        case .connection: return String(localized: "Connections")
        case .update: return String(localized: "Updates")
        default: return rawValue
        }
    }
}

extension ShareKind {
    var label: String {
        switch self {
        case .publicLink, .link: return String(localized: "Public link")
        case .user: return String(localized: "User")
        case .group: return String(localized: "Group")
        case .email: return String(localized: "Email")
        default: return String(localized: "Other share")
        }
    }

    var symbol: String {
        switch self {
        case .publicLink, .link: return "link"
        case .user: return "person"
        case .group: return "person.3"
        case .email: return "envelope"
        default: return "questionmark"
        }
    }
}

extension UnlockMode {
    var label: String {
        switch self {
        case .keychain: return String(localized: "Unlock automatically (Keychain)")
        case .ask: return String(localized: "Ask at every login")
        default: return rawValue
        }
    }
}

// MARK: - Formatting

enum Format {
    static func bytes(_ value: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: max(0, value), countStyle: .file)
    }

    static func date(ms: Int64) -> Date {
        Date(timeIntervalSince1970: TimeInterval(ms) / 1000)
    }

    static func relative(ms: Int64) -> String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .full
        return formatter.localizedString(for: date(ms: ms), relativeTo: Date())
    }

    static func dateTime(ms: Int64) -> String {
        date(ms: ms).formatted(date: .abbreviated, time: .shortened)
    }

    static func duration(seconds: Double) -> String {
        let formatter = DateComponentsFormatter()
        formatter.allowedUnits = seconds >= 3600 ? [.hour, .minute] : [.minute, .second]
        formatter.unitsStyle = .abbreviated
        return formatter.string(from: seconds) ?? ""
    }

    /// `Connection name: remote/path` for display.
    static func remote(_ connectionName: String, _ path: String) -> String {
        path.isEmpty ? connectionName : "\(connectionName): \(path)"
    }
}
