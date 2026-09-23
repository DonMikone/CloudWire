import AppKit
import CloudWireKit
import Foundation
import UserNotifications

/// Posts Core notifications and "link copied" through `UNUserNotificationCenter`.
@MainActor
final class NotificationManager: NSObject, UNUserNotificationCenterDelegate {
    static let shared = NotificationManager()

    static let massDeleteCategory = "massDelete"
    static let viewAction = "view"

    private var center: UNUserNotificationCenter { UNUserNotificationCenter.current() }

    func configure() {
        center.delegate = self
        let view = UNNotificationAction(identifier: Self.viewAction, title: String(localized: "View"),
                                        options: [.foreground])
        let massDelete = UNNotificationCategory(identifier: Self.massDeleteCategory, actions: [view],
                                                intentIdentifiers: [])
        center.setNotificationCategories([massDelete])
    }

    @discardableResult
    func requestAuthorization() async -> Bool {
        (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
    }

    func authorizationStatus() async -> UNAuthorizationStatus {
        await center.notificationSettings().authorizationStatus
    }

    /// System Settings > Notifications, where the user can allow CloudWire again after denying it.
    static func openSystemSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") {
            NSWorkspace.shared.open(url)
        }
    }

    func post(_ notification: CoreNotification) {
        let content = UNMutableNotificationContent()
        switch notification.kind {
        case .conflict:
            let name = notification.itemName
            content.title = String(localized: "Conflict in “\(name)”")
            content.body = Self.conflictBody(files: notification.files)
        case .massDelete:
            content.title = String(localized: "Sync stopped: “\(notification.itemName)”")
            content.body = String(localized: "More than half of the files would be deleted. Confirm the deletion or restore the files.")
            content.categoryIdentifier = Self.massDeleteCategory
        case .vaultMigration:
            let path = notification.params["path"]?.stringValue ?? ""
            switch notification.params["status"]?.stringValue {
            case "verified":
                content.title = String(localized: "Encryption of “\(path)” finished and verified")
                content.body = String(localized: "You can now delete the original in Vaults.")
            case "mismatch":
                let count = notification.params["count"]?.int64Value ?? 0
                content.title = String(localized: "Encryption of “\(path)” found differences")
                content.body = String(localized: "Files that differ: \(count). The original is kept.")
            default:
                // rclone's own reason; the title already says what failed.
                content.title = String(localized: "Encryption of “\(path)” failed")
                content.body = notification.message
            }
        default:
            content.title = notification.title.isEmpty
                ? String(localized: "CloudWire error") : String(localized: "Error in “\(notification.title)”")
            content.body = notification.text.localized()
        }
        content.sound = .default
        content.userInfo = [
            "kind": notification.kind.rawValue,
            "itemId": notification.itemId.isEmpty ? notification.subjectId : notification.itemId,
        ]
        let request = UNNotificationRequest(identifier: "core-\(notification.id)", content: content, trigger: nil)
        center.add(request)
    }

    /// The Core lists the Conflict Copies: the cloud versions saved beside the local files.
    static func conflictBody(files: [String]) -> String {
        let shown = files.prefix(3).joined(separator: ", ")
        return switch files.count {
        case ...1: String(localized: "The cloud version was kept as a Conflict Copy next to your file: \(shown).")
        case 2...3: String(localized: "The cloud versions were kept as Conflict Copies next to your files: \(shown).")
        default: String(localized: "The cloud versions were kept as Conflict Copies next to your files: \(shown) and \(files.count - 3) more.")
        }
    }

    func postLinkCopied(_ url: String) {
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Link copied")
        content.body = url
        content.userInfo = ["kind": "linkCopied"]
        center.add(UNNotificationRequest(identifier: "link-\(UUID().uuidString)", content: content, trigger: nil))
    }

    /// A locked Vault that asks for its password at login holds Mounts or Offline Items.
    func postVaultLocked(_ vault: Vault) {
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Vault “\(vault.name)” is locked")
        content.body = String(localized: "Unlock it so the Mounts and Offline Items in it work again.")
        content.userInfo = ["kind": NotificationKind.vaultLocked.rawValue, "itemId": vault.id]
        center.add(UNNotificationRequest(identifier: "vault-locked-\(vault.id)", content: content, trigger: nil))
    }

    // MARK: UNUserNotificationCenterDelegate

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions
    {
        [.banner, .list, .sound]
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            didReceive response: UNNotificationResponse) async
    {
        let userInfo = response.notification.request.content.userInfo
        let kind = userInfo["kind"] as? String ?? ""
        let itemId = userInfo["itemId"] as? String ?? ""
        let action = response.actionIdentifier
        await MainActor.run {
            Self.route(kind: kind, itemId: itemId, action: action)
        }
    }

    private static func route(kind: String, itemId: String, action: String) {
        guard action == UNNotificationDefaultActionIdentifier || action == viewAction else { return }
        switch kind {
        case NotificationKind.massDelete.rawValue, NotificationKind.conflict.rawValue:
            WindowRouter.shared.showMain(section: .offline)
        case NotificationKind.error.rawValue:
            WindowRouter.shared.showMain(section: .activity)
        case NotificationKind.vaultMigration.rawValue:
            WindowRouter.shared.showMain(section: .vaults)
        case NotificationKind.vaultLocked.rawValue:
            let model = AppModel.shared
            if let vault = model.vaults.first(where: { $0.id == itemId }), !vault.unlocked {
                model.vaultUnlockRequest = vault
            }
            WindowRouter.shared.showMain(section: .vaults)
        default:
            break
        }
    }
}

extension NotificationKind {
    /// Core: an Encrypt-Existing job finished (`params`: jobId, vaultId, vaultName, status, path, count, message).
    static let vaultMigration: Self = "vaultMigration"
    /// App only: a locked Vault holds Mounts or Offline Items.
    static let vaultLocked: Self = "vaultLocked"
}
