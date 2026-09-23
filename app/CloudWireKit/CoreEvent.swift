import Foundation

/// An event pushed by the Core after `events.subscribe`, or a client-side connection change.
public enum CoreEvent: Sendable {
    case mountStatus(Mount)
    case offlineStatus(OfflineItem)
    case offlineProgress(OfflineProgressEvent)
    case pauseStatus(PauseStatus)
    case activityAppended(ActivityEntry)
    case notificationNew(CoreNotification)
    case nextcloudLogin(NextcloudLoginEvent)
    case configStep(ConfigStep)
    case connectionsChanged
    case vaultsChanged
    case vaultMigration(VaultMigrationEvent)
    case updateAvailable(UpdateAvailableEvent)
    case settingsChanged(CoreSettings)
    /// An event type this client does not know, or whose payload did not decode.
    case unknown(type: String, data: JSONValue)
    /// The socket closed unexpectedly (Core stopped or crashed). Emitted by the client.
    case connectionLost

    /// Decodes `params` of an `event` notification (`{"type":…,"data":…}`).
    static func decode(type: String, data: JSONValue) -> CoreEvent {
        do {
            switch type {
            case "mount.status": return .mountStatus(try data.decode())
            case "offline.status": return .offlineStatus(try data.decode())
            case "offline.progress": return .offlineProgress(try data.decode())
            case "pause.status": return .pauseStatus(try data.decode())
            case "activity.appended": return .activityAppended(try data.decode())
            case "notification.new": return .notificationNew(try data.decode())
            case "connection.nextcloudLogin": return .nextcloudLogin(try data.decode())
            case "connection.configStep": return .configStep(try data.decode())
            case "connections.changed": return .connectionsChanged
            case "vaults.changed": return .vaultsChanged
            case "vault.migration": return .vaultMigration(try data.decode())
            case "update.available": return .updateAvailable(try data.decode())
            case "settings.changed": return .settingsChanged(try data.decode())
            default: return .unknown(type: type, data: data)
            }
        } catch {
            return .unknown(type: type, data: data)
        }
    }
}
