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

    func post(_ notification: CoreNotification) {
        let content = UNMutableNotificationContent()
        switch notification.kind {
        case .conflict:
            let name = notification.itemName
            content.title = String(localized: "Conflict in “\(name)”")
            let files = notification.files
            let shown = files.prefix(3).joined(separator: ", ")
            content.body = files.count > 3
                ? String(localized: "Both versions were kept: \(shown) and \(files.count - 3) more.")
                : String(localized: "Both versions were kept: \(shown).")
        case .massDelete:
            content.title = String(localized: "Sync stopped: “\(notification.itemName)”")
            content.body = String(localized: "More than half of the files would be deleted. Confirm the deletion or restore the files.")
            content.categoryIdentifier = Self.massDeleteCategory
        default:
            content.title = notification.title.isEmpty ? String(localized: "CloudWire error") : notification.title
            content.body = notification.message
        }
        content.sound = .default
        content.userInfo = [
            "kind": notification.kind.rawValue,
            "itemId": notification.itemId.isEmpty ? notification.subjectId : notification.itemId,
        ]
        let request = UNNotificationRequest(identifier: "core-\(notification.id)", content: content, trigger: nil)
        center.add(request)
    }

    func postLinkCopied(_ url: String) {
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Link copied")
        content.body = url
        content.userInfo = ["kind": "linkCopied"]
        center.add(UNNotificationRequest(identifier: "link-\(UUID().uuidString)", content: content, trigger: nil))
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
        let kind = response.notification.request.content.userInfo["kind"] as? String ?? ""
        let action = response.actionIdentifier
        await MainActor.run {
            Self.route(kind: kind, action: action)
        }
    }

    private static func route(kind: String, action: String) {
        guard action == UNNotificationDefaultActionIdentifier || action == viewAction else { return }
        switch kind {
        case NotificationKind.massDelete.rawValue, NotificationKind.conflict.rawValue:
            WindowRouter.shared.showMain(section: .offline)
        case NotificationKind.error.rawValue:
            WindowRouter.shared.showMain(section: .activity)
        default:
            break
        }
    }
}
