#if DEBUG
import AppKit
import CloudWireKit
import Foundation

/// `dialogs.md` for `--export-snapshots`: alerts, confirmation dialogs, system panels, notifications
/// and menus cannot be rendered offscreen, so their texts are listed in the exported language. The
/// texts use the same localisation keys as the views; source lines are looked up in the sources.
@MainActor
enum SnapshotDialogs {
    private struct Entry {
        let file: String
        let anchor: String
        let trigger: String
        let title: String
        var message = ""
        var buttons: [String] = []
    }

    /// `app/CloudWire` of the source checkout this build came from.
    private static let sourceRoot = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    /// `file:line` of the first line containing `anchor`, or just `file` without the sources.
    private static func location(_ file: String, _ anchor: String) -> String {
        guard let text = try? String(contentsOf: sourceRoot.appendingPathComponent(file), encoding: .utf8),
            let index = text.components(separatedBy: "\n").firstIndex(where: { $0.contains(anchor) })
        else { return file }
        return "\(file):\(index + 1)"
    }

    private static let cancel = String(localized: "Cancel") + " (Abbrechen-Rolle)"

    static func write(to url: URL) {
        let storage = "~/CloudWire/Nextcloud/Musik/Projekte/Album 2026"
        var text = """
            # Dialoge, Alerts, Panels, Benachrichtigungen und Menüs

            Nicht offscreen renderbar (eigene Fenster/NSMenu/System-UI). Texte in der Exportsprache, Beispielwerte \
            für Platzhalter. Buttons in Anzeigereihenfolge; SwiftUI-Bestätigungsdialoge ohne eigene Abbrechen-Rolle \
            bekommen automatisch einen Abbrechen-Button.

            """

        section(&text, "Bestätigungsdialoge (SwiftUI .confirmationDialog)", [
            Entry(file: "Views/ConnectionsView.swift", anchor: #"Delete the Connection “\(deleting?.name"#,
                  trigger: "Verbindungen: Papierkorb-Button oder Kontextmenü „Delete…“ einer Verbindung",
                  title: String(localized: "Delete the Connection “\("Google Drive")”?"),
                  message: String(localized: "CloudWire forgets this Connection and its credentials. Files in the cloud stay untouched."),
                  buttons: [String(localized: "Delete Connection") + " (destruktiv)", cancelAuto]),
            Entry(file: "Views/MountsView.swift", anchor: #"Delete the Mount “\(deleting"#,
                  trigger: "Mounts: Papierkorb-Button oder Kontextmenü „Delete…“",
                  title: String(localized: "Delete the Mount “\("Google Drive – Samples")”?"),
                  message: String(localized: "The Mount is ejected and removed from CloudWire. Files in the cloud are not touched."),
                  buttons: [String(localized: "Delete Mount") + " (destruktiv)", cancelAuto]),
            Entry(file: "Views/OfflineView.swift", anchor: "” from offline?",
                  trigger: "Offline: Zeilenmenü/Kontextmenü „Remove Offline…“ (auch per cloudwire://-Aktion)",
                  title: String(localized: "Remove “\("Album 2026")” from offline?"),
                  message: String(localized: "Syncing stops for \(storage). The cloud is never touched."),
                  buttons: [String(localized: "Move Local Copy to Trash") + " (Standard ⏎)",
                            String(localized: "Keep Local Copy"), cancel]),
            Entry(file: "Views/OfflineView.swift", anchor: "already exists and is not empty",
                  trigger: "AddOfflineSheet: „Make Available Offline“, wenn der Speicherort nicht leer ist",
                  title: String(localized: "“\("~/CloudWire/Nextcloud/Musik/Projekte/Album 2026")” already exists and is not empty"),
                  message: String(localized: "Merging syncs everything already in this folder with the cloud: files only here are uploaded, and for files present on both sides the newer version wins."),
                  buttons: [String(localized: "Merge (Newer Version Wins)"), String(localized: "Choose Another Folder"),
                            cancel]),
            Entry(file: "Views/SharesView.swift", anchor: "Delete this share?",
                  trigger: "Freigaben: Papierkorb-Button einer Freigabe",
                  title: String(localized: "Delete the share with \("Anna Berg")?") + " | "
                      + String(localized: "Delete the link “\("Mastering")”?") + " | " + String(localized: "Delete this share?"),
                  message: String(localized: "People using this share lose access."),
                  buttons: [String(localized: "Delete Share") + " (destruktiv)", cancelAuto]),
            Entry(file: "Views/ShareWindow.swift", anchor: "Delete this share?",
                  trigger: "Share-Fenster: Papierkorb-Button einer bestehenden Freigabe",
                  title: String(localized: "Delete the share with \("Anna Berg")?") + " | "
                      + String(localized: "Delete the link “\("Mastering")”?") + " | " + String(localized: "Delete this share?"),
                  message: String(localized: "People using this share lose access."),
                  buttons: [String(localized: "Delete Share") + " (destruktiv)", cancelAuto]),
            Entry(file: "Views/VaultsView.swift", anchor: "Remove the Vault “",
                  trigger: "Tresore: …-Menü „Remove…“",
                  title: String(localized: "Remove the Vault “\("Verträge")” from CloudWire?"),
                  message: String(localized: "CloudWire forgets the Vault and its stored password. The encrypted data stays in the cloud and can be opened again with the password."),
                  buttons: [String(localized: "Remove Vault") + " (destruktiv)", cancelAuto]),
            Entry(file: "Views/VaultsView.swift", anchor: "Delete the unencrypted original?",
                  trigger: "EncryptSheet nach Verifikation: „Delete Original…“",
                  title: String(localized: "Delete the unencrypted original?"),
                  message: String(localized: "The encrypted copy was verified. The original “\("Dokumente/Steuer 2025")” is deleted from the cloud."),
                  buttons: [String(localized: "Delete Original") + " (destruktiv)", cancelAuto]),
        ])

        section(&text, "Alerts (SwiftUI .alert)", [
            Entry(file: "Views/ConnectionsView.swift", anchor: ".alert(item: $testResult)",
                  trigger: "Verbindungen: „Test“ erfolgreich (Fehler → Modell-Alert unten)",
                  title: String(localized: "Connection works"),
                  message: String(localized: "CloudWire can reach “\("Nextcloud")”.") + "\n"
                      + String(localized: "\(Format.bytes(268_435_456_000)) of \(Format.bytes(1_099_511_627_776)) used."),
                  buttons: [String(localized: "OK")]),
            Entry(file: "Views/Components.swift", anchor: "content.alert(item: $model.alert)",
                  trigger: "model.alert (Hauptfenster, Einstellungen): jede über model.perform/present gemeldete Fehlermeldung; Titel = Überschrift je Fehlercode (Liste unten), Text = übersetzte Meldung des Core (ErrorText.detail, Liste „Meldungen des Core“)",
                  title: "<ErrorText.headline>", message: "<ErrorText.detail>", buttons: [String(localized: "OK")]),
            Entry(file: "App/AppModel.swift", anchor: "Folder not found",
                  trigger: "„Show in Finder“ für einen nicht vorhandenen Pfad (über model.alert)",
                  title: String(localized: "Folder not found"),
                  message: String(localized: "\("~/CloudWire/Mounts/Samples") does not exist."),
                  buttons: [String(localized: "OK")]),
            Entry(file: "Views/ActivityView.swift", anchor: "Export complete",
                  trigger: "Aktivität/Einstellungen-Log: „Export…“ abgeschlossen (über model.alert)",
                  title: String(localized: "Export complete"),
                  message: String(localized: "\(128) entries saved to \("CloudWire-Activity.csv")."),
                  buttons: [String(localized: "OK")]),
        ])

        section(&text, "App-modale Alerts (NSAlert)", [
            Entry(file: "App/WindowRouter.swift", anchor: "func confirmQuitCompletely",
                  trigger: "Menüleiste „Quit CloudWire Completely…“ und App-Menü (CloudWireApp.swift, CommandGroup)",
                  title: String(localized: "Quit CloudWire completely?"),
                  message: WindowRouter.quitCompletelyMessage(autostart: true) + " | "
                      + WindowRouter.quitCompletelyMessage(autostart: false),
                  buttons: [String(localized: "Quit Completely") + " (destruktiv)", String(localized: "Cancel")]),
        ] + actionConfirmations())

        text += "\n## Überschriften des Modell-Alerts (ErrorText.headline, \(location("App/Presentation.swift", "static func headline")))\n\n"
        for code in headlineCodes() {
            text += "- `\(code)`: \(ErrorText.headline(for: CoreError(code: code, message: "")))\n"
        }
        text += "- `client.*` (nicht erreichbar): \(ErrorText.headline(for: CoreError.notConnected()))\n"
        text += "- sonst: \(ErrorText.headline(for: CoreError(code: "x.unknown", message: "")))\n"
        let inUse = CoreError(code: "connection.inUse", message: "", data: .object(["dependents": .array([
            .object(["kind": .string("mount"), "id": .string("mt0000000001"), "name": .string("Nextcloud")]),
            .object(["kind": .string("offline"), "id": .string("of0000000001"), "name": .string("Album 2026")]),
        ])]))
        text += "- Text bei `connection.inUse`: \(ErrorText.detail(for: inUse))\n"
        text += "- Eingabefehler (\(ErrorText.alert(for: OptionValidationError(optionName: "x", reason: .required)).title)):\n"
        let reasons: [OptionValidationError.Reason] = [.required, .invalidBool, .invalidInteger, .invalidNumber,
                                                       .invalidSize, .invalidDuration, .notAChoice]
        for reason in reasons {
            text += "  - \(ErrorText.message(for: OptionValidationError(optionName: "Transfers", reason: reason)))\n"
        }
        text += "\n## Meldungen des Core (CoreText, CloudWireKit/CoreText.swift)\n\n"
        text += "Beispielwerte; rclone-Rohtext (`detail`) folgt nach „: “ und bleibt unübersetzt.\n\n"
        for code in CoreText.knownCodes.sorted() where code != "detail" {
            var params = sampleParams
            if code == "offline.pausedByRule" { params["cause"] = "pause.battery" }
            text += "- `\(code)`: \(CoreText(code: code, params: params, message: "–").localized())\n"
        }

        section(&text, "System-Panels (NSOpenPanel/NSSavePanel/Druck)", [
            Entry(file: "Views/Components.swift", anchor: "static func chooseFolder",
                  trigger: "Ordnerauswahl, Button „\(String(localized: "Choose"))“; Texte je Aufrufer: Mount point „Choose…“ (MountEditor), „Change Location…“ (Offline-Zeile), „Change…“ (AddOfflineSheet), Basisordner und Laufwerksordner (Einstellungen/Allgemein)",
                  title: String(localized: "Choose"),
                  message: [String(localized: "Choose an empty folder for the Mount."),
                            String(localized: "Choose the new storage location. The local files are moved there, nothing is downloaded again."),
                            String(localized: "Choose the folder in which CloudWire creates the folder for the offline copy, for example on an external SSD."),
                            String(localized: "Choose the base folder for new Offline Items. Existing Offline Items stay where they are."),
                            String(localized: "Choose the folder in which new Mounts appear.")]
                      .joined(separator: " | ")),
            Entry(file: "Views/Components.swift", anchor: "static func chooseApplications",
                  trigger: "Einstellungen/Sync: „+“ unter der Studio-Modus-App-Liste (Programme, Mehrfachauswahl)",
                  title: String(localized: "Add")),
            Entry(file: "Views/Components.swift", anchor: "static func savePanel",
                  trigger: "ExportVaultSheet: „Export…“ (Dateiname „Verträge-rclone.conf“)",
                  title: String(localized: "Save the rclone configuration")),
            Entry(file: "Views/ActivityView.swift", anchor: "func exportActivityLog",
                  trigger: "Aktivität „Export…“ und Einstellungen/Log „Export…“ (Dateiname „CloudWire-Activity.csv“, Zusatzfeld „Format:“ CSV/JSON)",
                  title: String(localized: "Export the activity log")),
            Entry(file: "Views/VaultsView.swift", anchor: "NSPrintOperation(view: view)",
                  trigger: "RecoveryKeySheet „Print…“: Druckdialog mit Text „\(String(localized: "CloudWire Vault Recovery Key"))“ + Schlüssel",
                  title: String(localized: "CloudWire Vault Recovery Key")),
        ])

        section(&text, "Mitteilungen (UNUserNotification)", [
            Entry(file: "App/NotificationManager.swift", anchor: "Conflict in “",
                  trigger: "Core-Benachrichtigung „conflict“",
                  title: String(localized: "Conflict in “\("Drums")”"),
                  message: NotificationManager.conflictBody(files: ["Kick 04.wav", "Kick 05.wav", "Snare 02.wav", "Hat 01.wav", "Tom 03.wav"])
                      + " | " + NotificationManager.conflictBody(files: ["Kick 04.wav"])),
            Entry(file: "App/NotificationManager.swift", anchor: "Sync stopped: “",
                  trigger: "Core-Benachrichtigung „massDelete“, Aktion „\(String(localized: "View"))“",
                  title: String(localized: "Sync stopped: “\("Stems")”"),
                  message: String(localized: "More than half of the files would be deleted. Confirm the deletion or restore the files.")),
            Entry(file: "App/NotificationManager.swift", anchor: "CloudWire error",
                  trigger: "Core-Fehlerbenachrichtigung (Text = Core-Meldung); ohne eigenen Titel „\(String(localized: "CloudWire error"))“",
                  title: String(localized: "Error in “\("Album 2026")”")),
            Entry(file: "App/NotificationManager.swift", anchor: "\"Link copied\"",
                  trigger: "Link erstellt/kopiert (Einstellung „Link copied“), Text = URL",
                  title: String(localized: "Link copied"), message: "https://cloud.example.com/s/Xk3pQ9aLm2"),
        ])

        section(&text, "Menüs (NSMenu, öffnen erst beim Klick)", [
            Entry(file: "Views/OverviewView.swift", anchor: "Button(\"For 1 Hour\")",
                  trigger: "Pfeil des Pause-Buttons (Übersicht, Menüleiste); Klick auf „\(String(localized: "Pause"))“ selbst pausiert bis zur Fortsetzung",
                  title: String(localized: "Pause"),
                  buttons: [String(localized: "For 1 Hour"), PauseControls.tomorrowMorningTitle(),
                            String(localized: "Until I Resume")]),
            Entry(file: "Views/OfflineView.swift", anchor: "private var menuItems",
                  trigger: "Offline-Zeile: …-Button und Kontextmenü",
                  title: "…",
                  buttons: [String(localized: "Sync Now"), String(localized: "Show in Finder"),
                            String(localized: "Change Location…"), String(localized: "Excludes & Advanced…"),
                            String(localized: "Sync History…"), "—", String(localized: "Remove Offline…") + " (destruktiv)"]),
            Entry(file: "Views/MountsView.swift", anchor: ".contextMenu {",
                  trigger: "Mount-Zeile: Kontextmenü",
                  title: "…",
                  buttons: [String(localized: "Show in Finder"), String(localized: "Edit…"), "—",
                            String(localized: "Delete…") + " (destruktiv)"]),
            Entry(file: "Views/VaultsView.swift", anchor: "Button(\"Mount as Drive\")",
                  trigger: "Tresor-Zeile: …-Button (erste drei nur bei entsperrtem Tresor aktiv)",
                  title: "…",
                  buttons: [String(localized: "Mount as Drive"), String(localized: "Make Available Offline"),
                            String(localized: "Encrypt Existing Files Into This Vault…"), "—",
                            String(localized: "Change Password…"), String(localized: "Reset Password with Recovery Key…"),
                            String(localized: "Emergency Export…"), "—", String(localized: "Remove…") + " (destruktiv)"]),
            Entry(file: "Views/ActivityView.swift", anchor: "private var filterMenu",
                  trigger: "Aktivität: Filtermenü „\(String(localized: "All Entries"))“/„\(String(localized: "Filtered"))“",
                  title: String(localized: "All Entries"),
                  buttons: [String(localized: "Level") + ": " + ActivityLevel.allCases.map(\.label).joined(separator: ", "),
                            String(localized: "Category") + ": " + ActivityCategory.allCases.map(\.label).joined(separator: ", "),
                            String(localized: "Show All")]),
            Entry(file: "Views/OptionFormView.swift", anchor: "Menu {",
                  trigger: "rclone-Optionsfeld mit Vorschlägen (nicht exklusiv): Chevron-Menü mit den rclone-Beispielwerten „Wert – Hilfe“",
                  title: "⌄"),
            Entry(file: "App/CloudWireApp.swift", anchor: "CommandGroup(replacing: .appTermination)",
                  trigger: "App-Menü (ersetzt „Beenden“)",
                  title: "CloudWire",
                  buttons: [String(localized: "Close Windows") + " (⌘Q)", String(localized: "Quit CloudWire Completely…")]),
        ])

        try? text.write(to: url, atomically: true, encoding: .utf8)
    }

    private static let cancelAuto = String(localized: "Cancel") + " (automatisch)"

    /// Confirmations for `cloudwire://` actions requested by other apps.
    private static func actionConfirmations() -> [Entry] {
        let names = ["~/CloudWire/Mounts/Nextcloud/Musik/Projekte/Album 2026"]
        let actions: [ActionURL.Name] = [.copyPublicLink, .makeOffline, .removeOffline, .encrypt]
        return actions.map { action in
            Entry(file: "App/ActionHandler.swift", anchor: "Allow this action?",
                  trigger: "cloudwire://-URL einer anderen App (Warn-Alert)",
                  title: String(localized: "Allow this action?"),
                  message: ActionHandler.confirmationMessage(action, paths: names),
                  buttons: [String(localized: "Continue"), String(localized: "Cancel")])
        }
    }

    /// Values for every param a Core text uses (see `CoreText`).
    private static let sampleParams: [String: String] = [
        "name": "Album 2026", "path": "~/CloudWire/Mounts/Nextcloud", "storagePath": "~/CloudWire/Nextcloud/Musik",
        "mountPoint": "~/CloudWire/Mounts/Nextcloud", "id": "mt0000000001", "version": "0.1.0", "label": "Konflikt",
        "count": "2", "provider": "webdav", "server": "https://cloud.example.com", "user": "mike",
        "url": "cloud.example", "folder": "Privat.cwvault", "app": "REAPER", "percent": "73", "method": "mounts.list",
        "files": "Belege/Quittung 0412.pdf, Belege/Quittung 0413.pdf", "neededBytes": "3221225472",
        "freeBytes": "536870912", "untilMs": "1790613000000", "transferred": "12", "deleted": "1", "conflicts": "0",
    ]

    /// Error codes with their own headline, read from `ErrorText.headline` in the sources.
    private static func headlineCodes() -> [String] {
        guard let source = try? String(contentsOf: sourceRoot.appendingPathComponent("App/Presentation.swift"),
                                       encoding: .utf8)
        else { return [] }
        return source.matches(of: /case "([a-z]+\.[A-Za-z]+)": return String\(localized:/).map { String($0.output.1) }
    }

    private static func section(_ text: inout String, _ heading: String, _ entries: [Entry]) {
        text += "\n## \(heading)\n"
        for entry in entries {
            text += "\n### \(location(entry.file, entry.anchor))\n\n"
            text += "- Auslöser: \(entry.trigger)\n"
            text += "- Titel: „\(entry.title)“\n"
            if !entry.message.isEmpty {
                text += "- Text: „\(entry.message.replacingOccurrences(of: "\n", with: "⏎"))“\n"
            }
            if !entry.buttons.isEmpty {
                text += "- Buttons/Einträge: " + entry.buttons.map { "„\($0)“" }.joined(separator: " · ") + "\n"
            }
        }
    }
}
#endif
