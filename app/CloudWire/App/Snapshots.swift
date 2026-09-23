#if DEBUG
import AppKit
import CloudWireKit
import SwiftUI

/// `--export-snapshots <dir> [--snapshot-dark]` (DEBUG builds): renders every main window section,
/// sheet, share window state, settings tab, onboarding step and the menu bar panel with demo data to
/// `<dir>/<nn>-<area>-<name>.png`, describes them in `<dir>/INDEX.md`, lists what cannot be rendered
/// offscreen (alerts, dialogs, panels, menus) in `<dir>/dialogs.md`, then quits. The language follows
/// `-AppleLanguages`. Views load their data from `DemoCore`, attached to `AppModel.client`.
@MainActor
enum SnapshotExporter {
    static func run(directory: URL) {
        let dark = CommandLine.arguments.contains("--snapshot-dark")
        NSApp.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        NSApp.setActivationPolicy(.regular)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let model = AppModel.shared
        model.loadDemoData()

        Task { @MainActor in
            await DemoCore.attach(to: model.client)
            let session = SnapshotSession(directory: directory, model: model)
            await session.renderAll()
            session.writeIndex(dark: dark)
            SnapshotDialogs.write(to: directory.appendingPathComponent("dialogs.md"))
            NSApp.terminate(nil)
        }
    }
}

@MainActor
private final class SnapshotSession {
    private struct Entry {
        let file: String
        let summary: String
        let source: String
        let controls: String
    }

    private let directory: URL
    private let model: AppModel
    private var entries: [Entry] = []
    /// `--snapshot-only <text>`: renders only files whose name contains `text` (numbering stays).
    private let only: String? = CommandLine.arguments.firstIndex(of: "--snapshot-only")
        .flatMap { CommandLine.arguments.indices.contains($0 + 1) ? CommandLine.arguments[$0 + 1] : nil }

    init(directory: URL, model: AppModel) {
        self.directory = directory
        self.model = model
    }

    func renderAll() async {
        // Warm-up pass: the first SwiftUI window needs longer before everything is drawn.
        await render(MainWindowContent(), width: 1200, height: 760, to: nil)
        await mainWindow()
        await connectionSheets()
        await mountSheets()
        await offlineSheets()
        await vaultSheets()
        await shareWindow()
        await settings()
        await onboarding()
        await menuBar()
    }

    // MARK: Main window

    private func main(_ name: String, _ section: SidebarSection, _ summary: String, source: String,
                      controls: String, scenario: DemoCore.Scenario = .normal, setup: () -> Void = {}) async
    {
        await shot("main-\(name)", summary, source: "MainWindowContent (Views/MainWindow.swift) → \(source)",
                   controls: controls, width: 1200, height: 760, scenario: scenario,
                   setup: {
                       model.selection = section
                       setup()
                   }, MainWindowContent())
    }

    private func mainWindow() async {
        let overview = "OverviewView (Views/OverviewView.swift)"
        let overviewControls = "Links „Manage Mounts“, „Manage Offline Items“; Pause-Splitbutton „Pause“ (Pfeil-Menü: For 1 Hour / Until Tomorrow 08:00 / Until I Resume); „Sync Now“ (nur bei aktiven Pause-Regeln); Link „Show Activity Log“"
        await main("overview", .overview,
                   "Übersicht: Karten Hintergrunddienst, Mounts, Offline, Sync (Studio-Modus pausiert), letzte Probleme",
                   source: overview, controls: overviewControls)
        await main("overview-attention", .overview,
                   "Übersicht mit Mount-Fehler, Mass-Delete-/Sync-Fehlern (Sidebar-Badges), manueller Pause und Update-Karte",
                   source: overview,
                   controls: overviewControls + "; „Resume“ statt Pause-Menü; Update-Karte mit Link „Download“",
                   setup: { self.attention() })

        let connections = "ConnectionsView (Views/ConnectionsView.swift)"
        await main("connections", .connections, "Verbindungen: Nextcloud, Google Drive, SFTP",
                   source: connections,
                   controls: "„Add Connection“ (→ AddConnectionSheet); je Zeile „Test“ (→ Alert), „Edit…“ (→ EditConnectionSheet), Papierkorb (→ Bestätigungsdialog)")
        await main("connections-empty", .connections, "Verbindungen, Leerzustand", source: connections,
                   controls: "„Add Connection“ (Titelleiste und Button)", setup: { model.connections = [] })

        let mounts = "MountsView (Views/MountsView.swift)"
        let mountControls = "„New Mount“ (→ MountEditor); je Zeile Schalter (mounten/auswerfen), Ordner-Button „Show in Finder“, „Edit…“ (→ MountEditor), Papierkorb (→ Bestätigungsdialog); Kontextmenü Show in Finder / Edit… / Delete…"
        await main("mounts", .mounts, "Mounts: gemountet, schreibgeschützt, FUSE-Tresor-Mount nicht gemountet",
                   source: mounts, controls: mountControls)
        await main("mounts-attention", .mounts, "Mounts mit Fehlerzustand (Inline-Fehler) und „Mounting…“",
                   source: mounts, controls: mountControls, setup: { model.mounts = DemoData.attentionMounts })
        await main("mounts-empty", .mounts, "Mounts, Leerzustand", source: mounts, controls: "„New Mount“ (zweimal)",
                   setup: { model.mounts = [] })

        let offline = "OfflineView (Views/OfflineView.swift)"
        let offlineControls = "„Sync All Now“, „Add“ (→ AddOfflineSheet); je Zeile …-Menü und Kontextmenü: Sync Now, Show in Finder, Change Location… (Ordnerauswahl), Excludes & Advanced… (→ ExcludesSheet), Sync History… (→ SyncHistorySheet), Remove Offline… (→ Bestätigungsdialog)"
        await main("offline", .offline,
                   "Offline-Objekte: synchronisiert gerade (Fortschritt), aktuell, pausiert (Studio-Modus), Tresor-Ordner",
                   source: offline, controls: offlineControls)
        await main("offline-attention", .offline,
                   "MassDeleteBanner (Mass-Delete Guard), Objekt im Fehlerzustand, wartendes Objekt",
                   source: offline + ", MassDeleteBanner",
                   controls: "Banner: „Confirm Deletion“ (destruktiv), „Don't Delete – Restore Files“ (primär); sonst " + offlineControls,
                   setup: { model.offlineItems = DemoData.attentionOfflineItems })
        await main("offline-empty", .offline, "Offline, Leerzustand", source: offline,
                   controls: "„Add Offline Item“, „Add“; „Sync All Now“ deaktiviert", setup: { model.offlineItems = [] })

        let shares = "SharesView (Views/SharesView.swift)"
        await main("shares", .shares, "Freigaben der Nextcloud-Verbindung (Link, Benutzer, Gruppe, E-Mail, Datei-Link)",
                   source: shares + ", ShareRow (Views/ShareWindow.swift)",
                   controls: "Verbindungs-Picker (nur Verbindungen, die teilen können), Filterfeld, Neu laden; je Freigabe Link kopieren (nur mit URL), „Edit…“ (→ ShareEditSheet), Papierkorb (→ Bestätigungsdialog)")
        await main("shares-empty", .shares, "Freigaben: keine vorhanden", source: shares,
                   controls: "Verbindungs-Picker, Filterfeld, Neu laden", scenario: .empty)
        await main("shares-no-connections", .shares, "Freigaben ohne Verbindungen", source: shares,
                   controls: "„Add Connection“ (→ Verbindungen + AddConnectionSheet)", setup: { model.connections = [] })

        let vaults = "VaultsView (Views/VaultsView.swift)"
        await main("vaults", .vaults, "Tresore: entsperrt (Schlüsselbund) und gesperrt (Passwort bei Anmeldung)",
                   source: vaults,
                   controls: "„Open Vault…“, „Encrypt Files…“, „New Vault“; je Zeile „Lock“ (mit Bestätigung bei abhängigen Laufwerken/Offline-Elementen) bzw. „Unlock…“, Verschlüsselungsaufträge (Stop, Details…/Delete Original…) und …-Menü (Mount as Drive, Make Available Offline, Encrypt Existing Files Into This Vault…, Change Password…, Reset Password with Recovery Key…, Emergency Export…, Remove…)")
        await main("vaults-empty", .vaults, "Tresore, Leerzustand", source: vaults,
                   controls: "„New Vault“, „Open Existing Vault…“, Titelleisten-Buttons", setup: { model.vaults = [] })

        let activity = "ActivityView (Views/ActivityView.swift)"
        let activityControls = "Filtermenü (Level/Category, Show All), Suchfeld (live), „Export…“ (Sicherungsdialog mit Format-Popup), Tabelle (Auswahl, ⌘C), Detailbereich nur bei Auswahl (VSplitView); ohne Treffer bei Filter „Reset Filters“"
        await main("activity", .activity, "Aktivitätsprotokoll, kein Eintrag ausgewählt", source: activity,
                   controls: activityControls)
        await shot("main-activity-detail", "Aktivität mit ausgewähltem Sync-Eintrag: Details und Dateien des Laufs (nur Detailspalte des Hauptfensters)",
                   source: "ActivityView + RunFilesView (Views/ActivityView.swift, Views/OfflineView.swift)",
                   controls: activityControls + "; „Copy“ im Detail", width: 989, height: 760,
                   ActivityView.snapshot(selecting: 10))
        await main("activity-empty", .activity, "Aktivität ohne Einträge", source: activity,
                   controls: activityControls, scenario: .empty)

        let unavailable = "CoreUnavailableView (Views/Components.swift)"
        await main("core-starting", .overview, "Hintergrunddienst startet", source: unavailable, controls: "keine",
                   setup: { model.coreState = .starting })
        await main("core-needs-approval", .overview, "Hintergrunddienst nicht erlaubt (Anmeldeobjekte)",
                   source: unavailable, controls: "„Allow in Background“ (Systemeinstellungen), „Check Again“",
                   setup: { model.coreState = .needsApproval })
        await main("core-failed", .overview, "Hintergrunddienst nicht erreichbar", source: unavailable,
                   controls: "„Try Again“",
                   setup: { model.coreState = .failed("connect failed: No such file or directory") })
    }

    // MARK: Sheets

    private func connectionSheets() async {
        let add = "AddConnectionSheet (Views/ConnectionsView.swift)"
        await shot("connections-add-choose", "Verbindung hinzufügen: Anbieterauswahl (Nextcloud empfohlen, rclone-Anbieter)",
                   source: add, controls: "Suchfeld, Nextcloud-Zeile, Anbieter-Zeilen (→ Formular), „Cancel“",
                   width: 620, AddConnectionSheet())
        await shot("connections-add-nextcloud", "Nextcloud: Anmeldung im Browser", source: add,
                   controls: "Name, Serveradresse, Radiogruppe „Sign-in“, „Back“, „Cancel“, „Log In with Browser“ (Standard)",
                   width: 620, AddConnectionSheet.snapshot(.nextcloud(manual: false)))
        await shot("connections-add-nextcloud-manual", "Nextcloud: manuell mit App-Passwort", source: add,
                   controls: "Name, Serveradresse, Radiogruppe, Benutzername, App-Passwort, „Back“, „Cancel“, „Connect“",
                   width: 620, AddConnectionSheet.snapshot(.nextcloud(manual: true)))
        let drive = DemoData.providers.first { $0.name == "drive" }!
        await shot("connections-add-provider", "rclone-Anbieter (Google Drive), einfache Optionen", source: add + ", OptionFormView",
                   controls: "Name, Einfach/Erweitert, Optionsfelder (Text, Vorschlagsmenü ⌄), „Back“, „Cancel“, „Create“",
                   width: 620, AddConnectionSheet.snapshot(.provider(drive, advanced: false)))
        await shot("connections-add-provider-advanced", "rclone-Anbieter (Google Drive), erweiterte Optionen",
                   source: add + ", OptionFormView",
                   controls: "wie oben; Schalter, Größen-/Dauerfelder mit Platzhalter",
                   width: 620, AddConnectionSheet.snapshot(.provider(drive, advanced: true)))
        await shot("connections-add-question", "Rückfrage des rclone-Konfigurators (OneDrive-Verbindungstyp)",
                   source: add + ", OptionFieldRow", controls: "Auswahl-Picker, Hilfetext, „Cancel“, „Continue“",
                   width: 620, AddConnectionSheet.snapshot(.question(DemoData.connectionQuestion)))
        await shot("connections-add-waiting", "Warten auf OAuth-Anmeldung im Browser", source: add, controls: "„Cancel“",
                   width: 620, AddConnectionSheet.snapshot(.waiting))
        await shot("connections-add-browser-login", "Warten auf Nextcloud-Login im Browser", source: add,
                   controls: "„Cancel Login“", width: 620, AddConnectionSheet.snapshot(.browserLogin))
        let edit = "EditConnectionSheet (Views/ConnectionsView.swift)"
        let nextcloud = DemoData.connections[0]
        await shot("connections-edit", "Verbindung „Nextcloud“ bearbeiten (WebDAV-Optionen)", source: edit,
                   controls: "Name, Einfach/Erweitert, Optionsfelder, „Cancel“, „Save“",
                   width: 620, EditConnectionSheet(connection: nextcloud))
        await shot("connections-edit-advanced", "Verbindung bearbeiten, erweiterte Optionen", source: edit,
                   controls: "wie oben", width: 620, EditConnectionSheet.snapshotAdvanced(connection: nextcloud))
    }

    private func mountSheets() async {
        let editor = "MountEditor (Views/MountsView.swift)"
        let controls = "Einfach/Erweitert, Name, Connection-Picker, Cloud folder + „Browse…“ (→ Popover), Mount point + „Choose…“ (Ordnerauswahl), Read-only, Cache-Stepper, Mount automatically, Technology (NFS/FUSE), „Cancel“, „Create“/„Save“"
        await shot("mounts-new", "Neuer Mount", source: editor, controls: controls, width: 640,
                   MountEditor(target: .new(connectionId: nil, remotePath: "")))
        await shot("mounts-edit", "Mount „Google Drive – Samples“ bearbeiten", source: editor, controls: controls,
                   width: 640, MountEditor(target: .edit(DemoData.mounts[1])))
        await shot("mounts-edit-advanced", "Mount bearbeiten, erweiterte rclone-Optionen (VFS, Mount, NFS)",
                   source: editor + ", OptionFormView", controls: "Einfach/Erweitert, Optionsfelder mit Namen, „Cancel“, „Save“",
                   width: 640, MountEditor.snapshotAdvanced(target: .edit(DemoData.mounts[1])))
        await shot("mounts-browse-popover", "Popover von „Browse…“ im MountEditor (Inhalt nachgebaut: Popover-Fenster nicht renderbar)",
                   source: "RemoteBrowser (Views/RemoteBrowser.swift) im Popover aus Views/MountsView.swift",
                   controls: "Breadcrumbs, Ordnerzeilen (navigieren), „Use This Folder“", width: 452,
                   VStack(alignment: .trailing) {
                       RemoteBrowser(connectionId: DemoData.nextcloudId, path: .constant("Musik"))
                           .frame(width: 420, height: 320)
                       Button("Use This Folder") {}
                   }
                   .padding())
    }

    private func offlineSheets() async {
        let add = "AddOfflineSheet + RemoteBrowser (Views/OfflineView.swift, Views/RemoteBrowser.swift)"
        let addControls = "Connection-Picker, Browser (Breadcrumbs, „Select This Folder“/„✓ Selected“, Radio je Ordner, Checkbox je Datei), Speicherort + „Change…“ (Ordnerauswahl), „Cancel“, „Make Available Offline“ (→ ggf. Dialog „The chosen folder is not empty“)"
        let preselected = OfflineDraft(connectionId: DemoData.nextcloudId, remotePath: "Musik/Projekte/Album 2026",
                                       kind: .folder, files: [])
        await shot("offline-add", "Offline verfügbar machen: Ordner vorausgewählt (wie aus dem Finder), Platzprüfung ok",
                   source: add, controls: addControls, width: 640, AddOfflineSheet(draft: preselected))
        await shot("offline-add-nothing-selected", "Offline verfügbar machen über „Add“: noch nichts ausgewählt",
                   source: add, controls: addControls, width: 640,
                   AddOfflineSheet(draft: OfflineDraft(connectionId: DemoData.nextcloudId, remotePath: nil, kind: .folder,
                                                       files: [])))
        await shot("offline-add-low-space", "Offline verfügbar machen: zu wenig freier Speicher", source: add,
                   controls: addControls + "; Hauptbutton deaktiviert", width: 640, scenario: .lowSpace,
                   AddOfflineSheet(draft: preselected))
        let drums = DemoData.offlineItems[1]
        await shot("offline-excludes", "Ausschlüsse & erweiterte rclone-Optionen für „Drums“",
                   source: "ExcludesSheet (Views/OfflineView.swift)",
                   controls: "TextEditor Ausschlussmuster, Link „Restore Defaults“, Filterfeld, Optionsformular, „Cancel“, „Save“",
                   width: 640, ExcludesSheet(item: drums))
        await shot("offline-history", "Sync-Verlauf von „Drums“, neuester Lauf ausgewählt",
                   source: "SyncHistorySheet + RunFilesView (Views/OfflineView.swift)",
                   controls: "Liste der Läufe (Auswahl), Dateien des Laufs, „Close“", width: 720,
                   SyncHistorySheet(item: drums))
    }

    private func vaultSheets() async {
        let file = "Views/VaultsView.swift"
        await shot("vaults-new", "Neuer Tresor", source: "NewVaultSheet (\(file))",
                   controls: "Connection-Picker, Name, Ort (Browser), Passwort + Wiederholung, Entsperren (Radio), „Cancel“, „Create Vault“",
                   width: 600, NewVaultSheet { _, _ in })
        await shot("vaults-recovery-key", "Wiederherstellungsschlüssel nach dem Anlegen (einmalig)",
                   source: "RecoveryKeySheet (\(file))",
                   controls: "„Copy“, „Print…“ (Druckdialog), Checkbox Bestätigung, „Done“ (erst nach Checkbox)",
                   width: 560, RecoveryKeySheet(recoveryKey: DemoData.recoveryKey, vault: DemoData.vaults[0]))
        await shot("vaults-open", "Bestehenden Tresor öffnen", source: "OpenVaultSheet (\(file))",
                   controls: "Connection-Picker, Browser (.cwvault-Ordner), Passwort, Entsperren (Radio), „Cancel“, „Open Vault“",
                   width: 600, OpenVaultSheet())
        await shot("vaults-unlock", "Tresor „Privat“ entsperren", source: "UnlockVaultSheet (\(file))",
                   controls: "Passwort, „Cancel“, „Unlock“", width: 420, UnlockVaultSheet(vault: DemoData.vaults[1]))
        await shot("vaults-change-password", "Tresorpasswort ändern", source: "ChangePasswordSheet (\(file))",
                   controls: "Aktuelles Passwort, neues Passwort + Wiederholung, „Cancel“, „Change Password“",
                   width: 460, ChangePasswordSheet(vault: DemoData.vaults[0]))
        await shot("vaults-recover", "Passwort mit Wiederherstellungsschlüssel zurücksetzen",
                   source: "RecoverVaultSheet (\(file))",
                   controls: "Recovery Key, neues Passwort + Wiederholung, „Cancel“, „Set New Password“", width: 520,
                   RecoverVaultSheet(connectionId: DemoData.nextcloudId, vaultPath: DemoData.vaults[0].vaultPath))
        await shot("vaults-export", "Notfall-Export der rclone-Konfiguration", source: "ExportVaultSheet (\(file))",
                   controls: "Tresorpasswort, „Cancel“, „Export…“ (→ Sicherungsdialog)", width: 480,
                   ExportVaultSheet(vault: DemoData.vaults[0]))

        let encrypt = "EncryptSheet (\(file))"
        let fromVaults = EncryptDraft(connectionId: DemoData.nextcloudId, path: "", isDir: true)
        let fromFinder = EncryptDraft(connectionId: DemoData.nextcloudId, path: "Dokumente/Steuer 2025", isDir: true)
        await shot("vaults-encrypt", "Bestehendes verschlüsseln (aus „Encrypt Files…“): Quelle wählen, neuer Tresor",
                   source: encrypt,
                   controls: "Connection-Picker, Browser (ein Ordner oder eine Datei, Radio), Ziel New/Existing Vault (segmentiert), Tresorname, Passwort ×2, Entsperren (Radio), „Ignore Pause Rules (e.g. Studio Mode)“, „Cancel“, „Encrypt“",
                   width: 620, EncryptSheet.snapshot(draft: fromVaults))
        await shot("vaults-encrypt-existing-vault", "Verschlüsseln aus dem Finder: feste Quelle, Ziel bestehender Tresor",
                   source: encrypt, controls: "Ziel-Segmente, Vault-Picker, Unterordner, „Ignore Pause Rules (e.g. Studio Mode)“, „Cancel“, „Encrypt“",
                   width: 620, EncryptSheet.snapshot(draft: fromFinder, existingVault: true))
        await shot("vaults-encrypt-running", "Verschlüsselungsjob läuft", source: encrypt, controls: "„Close“, „Stop Encryption“",
                   width: 620, setup: { model.migrations["job-demo"] = DemoData.migration("running") },
                   EncryptSheet.snapshot(draft: fromFinder, jobId: "job-demo"))
        await shot("vaults-encrypt-verified", "Job verifiziert, mit Wiederherstellungsschlüssel des neuen Tresors",
                   source: encrypt,
                   controls: "„Copy“, Checkbox Bestätigung, „Close“ (erst nach Checkbox), „Delete Original…“ (→ Bestätigungsdialog)",
                   width: 620, setup: { model.migrations["job-demo"] = DemoData.migration("verified") },
                   EncryptSheet.snapshot(draft: fromFinder, jobId: "job-demo", recoveryKey: DemoData.recoveryKey))
        await shot("vaults-encrypt-mismatch", "Job mit Abweichungen bei der Verifikation (Original bleibt)",
                   source: encrypt, controls: "Liste abweichender Dateien, „Close“", width: 620,
                   setup: { model.migrations["job-demo"] = DemoData.migration("mismatch") },
                   EncryptSheet.snapshot(draft: fromFinder, jobId: "job-demo"))
    }

    // MARK: Share window

    private func shareWindow() async {
        let source = "ShareWindow (Views/ShareWindow.swift)"
        let folder = ShareTarget(connectionId: DemoData.nextcloudId, path: "Musik/Projekte/Album 2026", isDir: true,
                                 name: "Album 2026")
        let existing = "; bestehende Freigaben: Link kopieren, „Edit…“ (→ ShareEditSheet), Papierkorb (→ Bestätigungsdialog)"
        let modes = "Modus-Segmente Public Link / People & Groups / Email / Internal Link"
        await shot("share-public-link", "Teilen-Fenster, Ordner: öffentlicher Link", source: source,
                   controls: modes + "; „Protect with password“, „Expires“, „Permissions“, „Hide download“, Label, Notiz, „Create Link“" + existing,
                   width: 560, ShareWindow.snapshot(target: folder, mode: .publicLink))
        await shot("share-public-link-created", "Öffentlicher Link erstellt (Passwort, 7 Tage, Label)", source: source,
                   controls: "Passwortfeld mit Anzeigen/„Generate“, LinkRow („Copy“/„Copied“, Safari-Button)" + existing,
                   width: 560,
                   ShareWindow.snapshot(target: folder, mode: .publicLink,
                                        createdURL: "https://cloud.example.com/s/Hn4Rx7Vb8L"))
        await shot("share-people", "Mit Personen und Gruppen teilen (Suchtreffer, erster ausgewählt)", source: source,
                   controls: modes + "; Suchfeld, Trefferliste, Checkboxen Edit/Create/Delete/Reshare, „Share with …“" + existing,
                   width: 560, ShareWindow.snapshot(target: folder, mode: .people, sharees: DemoData.sharees))
        await shot("share-email", "Link per E-Mail senden", source: source,
                   controls: modes + "; E-Mail-Adresse, „Expires“, Notiz, „Send Link“" + existing,
                   width: 560, ShareWindow.snapshot(target: folder, mode: .email))
        await shot("share-internal-link", "Interner Link", source: source, controls: modes + "; LinkRow" + existing,
                   width: 560, ShareWindow.snapshot(target: folder, mode: .internalLink))
        let file = ShareTarget(connectionId: DemoData.nextcloudId, path: "Musik/Mixes/Bassline.wav", isDir: false,
                               name: "Bassline.wav")
        await shot("share-file", "Teilen-Fenster, Datei (ohne „File drop“), bestehender Link mit verborgenem Download",
                   source: source, controls: modes + existing, width: 560,
                   ShareWindow.snapshot(target: file, mode: .publicLink))
        var manage = folder
        manage.manage = true
        await shot("share-manage", "„Freigaben verwalten…“: bestehende Freigaben zuerst, Erstellformular eingeklappt", source: source,
                   controls: existing + "; „Create New Share“ (DisclosureGroup, zu)", width: 560,
                   ShareWindow.snapshot(target: manage, mode: .publicLink))
        let drive = ShareTarget(connectionId: DemoData.driveId, path: "Mixes/Master v3.wav", isDir: false,
                                name: "Master v3.wav")
        await shot("share-rclone-link", "rclone-Anbieter (Google Drive): nur öffentlicher Link, keine Segmente",
                   source: source, controls: "Hinweis „Links of this provider don't expire.“, „Create Link“", width: 560,
                   ShareWindow.snapshot(target: drive, mode: .publicLink))
        let nas = ShareTarget(connectionId: DemoData.nasId, path: "Projekte/Session 12", isDir: true, name: "Session 12")
        await shot("share-unsupported", "Verbindung ohne Freigabefunktionen (SFTP): Hinweis „This connection does not support sharing.“",
                   source: source, controls: "keine", width: 560, ShareWindow.snapshot(target: nas, mode: .publicLink))
        let vault = ShareTarget(connectionId: DemoData.vaultConnectionId, path: "Laufend/Mietvertrag.pdf", isDir: false,
                                name: "Mietvertrag.pdf")
        await shot("share-vault", "Objekt in einem Tresor: Hinweis statt Freigabe", source: source, controls: "keine",
                   width: 560, ShareWindow.snapshot(target: vault, mode: .publicLink))
        let edit = "ShareEditSheet (Views/ShareWindow.swift)"
        await shot("share-edit-link", "Öffentlichen Link bearbeiten", source: edit,
                   controls: "„Password Protection“ (Checkbox), Neues Passwort (leer = behalten, Anzeigen-Button, „Generate“), Permissions, Hide download, Label, Expires (+ Datum), Note, „Cancel“, „Save“",
                   width: 480,
                   ShareEditSheet(share: DemoData.shares[0], connectionId: DemoData.nextcloudId, isDir: true,
                                  policy: SharePolicy()) { _ in })
        await shot("share-edit-user", "Benutzerfreigabe bearbeiten", source: edit,
                   controls: "Checkboxen Edit/Create/Delete/Reshare, Expires, Note, „Cancel“, „Save“", width: 480,
                   ShareEditSheet(share: DemoData.shares[1], connectionId: DemoData.nextcloudId, isDir: true,
                                  policy: SharePolicy()) { _ in })
    }

    // MARK: Settings, onboarding, menu bar

    private func settings() async {
        let tabs: [(SettingsTab, String, String, String)] = [
            (.general, "general", "Allgemein",
             "Schalter Autostart, Menüleisten-Icon, Update-Prüfung; Ordner für Offline/Mounts mit „Choose…“ (Ordnerauswahl)"),
            (.sync, "sync", "Sync: Zeiten, Pause-Regeln, Bandbreite, Standard-Ausschlüsse",
             "Zahlenfelder mit Steppern; Studio-Modus + App-Liste (+/−, App-Auswahl), Akku, getaktetes Netz, CPU + Schwelle; Bandbreite mit „Unlimited“-Checkboxen; TextEditor, „Restore Defaults“, „Apply“"),
            (.mounts, "mounts", "Mount-Standards und FUSE-Status",
             "Standard-Cachegröße (Stepper), Standard-Technologie (Picker), Link „Get FUSE-T“ falls nicht installiert"),
            (.notifications, "notifications", "Mitteilungen",
             "Schalter Errors, Conflicts, Mass-Delete Guard, Link copied; Link „Open Notification Settings…“"),
            (.log, "log", "Protokoll",
             "Log-Level-Picker, Aufbewahrung, Maximalgröße, „Export…“ (Sicherungsdialog), „Show in Finder“"),
            (.about, "about", "Über CloudWire", "Links „Project on GitHub“, „Releases“"),
        ]
        for (tab, name, summary, controls) in tabs {
            await shot("settings-\(name)", "Einstellungen, Tab \(summary)",
                       source: "SettingsView → SettingsTab.content (Views/SettingsView.swift); Toolbar-Tabs als statische Kopie",
                       controls: "Toolbar-Tabs General/Sync/Mounts/Notifications/Log/About; " + controls, width: 600,
                       SettingsView.snapshot(tab: tab))
        }
        await shot("settings-no-core", "Einstellungen ohne Verbindung zum Hintergrunddienst",
                   source: "SettingsView → CoreUnavailableView (Views/SettingsView.swift, Views/Components.swift)",
                   controls: "keine", width: 600,
                   setup: {
                       model.hasSettings = false
                       model.coreState = .starting
                   }, SettingsView())
    }

    private func onboarding() async {
        let source = "OnboardingView (Views/OnboardingView.swift)"
        let dots = "Seitenpunkte; "
        await shot("onboarding-1-welcome", "Onboarding 1/4: Willkommen", source: source, controls: dots + "„Continue“",
                   width: 560, height: 420, OnboardingView.snapshot(step: .welcome))
        let background = dots + "Schalter Autostart, „Back“, „Continue“ (nur bei laufendem Dienst aktiv)"
        await shot("onboarding-2-background", "Onboarding 2/4: Hintergrunddienst läuft", source: source,
                   controls: background, width: 560, height: 420, OnboardingView.snapshot(step: .background))
        await shot("onboarding-2-background-starting", "Onboarding 2/4: Dienst startet", source: source,
                   controls: background, width: 560, height: 420, setup: { model.coreState = .starting },
                   OnboardingView.snapshot(step: .background))
        await shot("onboarding-2-background-needs-approval", "Onboarding 2/4: Hintergrundausführung nicht erlaubt",
                   source: source, controls: background + "; „Allow in Background“, „Check Again“",
                   width: 560, height: 420, setup: { model.coreState = .needsApproval },
                   OnboardingView.snapshot(step: .background))
        await shot("onboarding-2-background-failed", "Onboarding 2/4: Dienst nicht erreichbar", source: source,
                   controls: background + "; „Try Again“", width: 560, height: 420,
                   setup: { model.coreState = .failed("connect failed: No such file or directory") },
                   OnboardingView.snapshot(step: .background))
        await shot("onboarding-3-finder", "Onboarding 3/4: Finder-Erweiterung und Mitteilungen", source: source,
                   controls: dots + "„Enable Finder Extension…“ (Systemeinstellungen), „Allow Notifications“ (Systemabfrage), „Back“, „Continue“",
                   width: 560, height: 420, OnboardingView.snapshot(step: .finder))
        await shot("onboarding-4-connection", "Onboarding 4/4: erste Verbindung", source: source,
                   controls: dots + "„Add Connection…“ (→ Hauptfenster + AddConnectionSheet), „Back“, „Done“",
                   width: 560, height: 420, OnboardingView.snapshot(step: .connection))
    }

    private func menuBar() async {
        let source = "MenuBarView (Views/MenuBarView.swift), Fensterstil-Menüleistenpanel"
        let controls = "Mount-Schalter, Mount-/Offline-Zeilen (öffnen den Bereich im Hauptfenster, ab 280 pt scrollbar), „Sync Now“, Pause-Splitbutton/„Sync Now“ bzw. „Resume“; Einträge Open CloudWire, Settings…, Quit CloudWire Completely… (→ NSAlert)"
        await shot("menubar", "Menüleistenpanel: Status, Mounts, Offline (Fortschritt), Sync-Steuerung", source: source,
                   controls: controls, width: 320, MenuBarView())
        await shot("menubar-attention", "Menüleistenpanel mit Fehlern, manueller Pause und Update-Hinweis",
                   source: source, controls: controls + "; Update-Link", width: 320, setup: { self.attention() },
                   MenuBarView())
        await shot("menubar-not-allowed", "Menüleistenpanel ohne Hintergrunddienst (nicht erlaubt)", source: source,
                   controls: "Open CloudWire, Settings…, Quit CloudWire Completely…", width: 320,
                   setup: { model.coreState = .needsApproval }, MenuBarView())
        let states: [(AggregateStatus, String)] = [(.idle, "idle"), (.syncing, "syncing"), (.paused, "paused"),
                                                   (.error, "error")]
        await shot("menubar-icons", "Menüleisten-Icon je Gesamtstatus (Template-Bild, 2,5-fach)",
                   source: "MenuBarLabel/MenuBarIconRenderer (Views/MenuBarView.swift)", controls: "keine (Icon öffnet das Panel)",
                   width: 360,
                   HStack(spacing: 28) {
                       ForEach(states, id: \.1) { state in
                           VStack(spacing: 6) {
                               Image(nsImage: MenuBarIconRenderer.image(for: state.0))
                                   .renderingMode(.template)
                                   .resizable()
                                   .interpolation(.high)
                                   .frame(width: 55, height: 45)
                               Text(verbatim: state.1).font(.caption).foregroundStyle(.secondary)
                           }
                       }
                   }
                   .padding(20))
    }

    /// Errors, a Mass-Delete Guard stop, a manual pause and an available update.
    private func attention() {
        model.mounts = DemoData.attentionMounts
        model.offlineItems = DemoData.attentionOfflineItems
        model.pause = DemoData.manualPause
        model.updateStatus = DemoData.update
        model.recentProblems = DemoData.attentionProblems
    }

    // MARK: Rendering

    /// Restores the demo state, applies `setup` and renders `view` to the next numbered file (fitting
    /// height when `height` is nil).
    private func shot(_ name: String, _ summary: String, source: String, controls: String, width: CGFloat,
                      height: CGFloat? = nil, scenario: DemoCore.Scenario = .normal, setup: () -> Void = {},
                      _ view: some View) async
    {
        model.loadDemoData()
        model.selection = .overview
        DemoCore.scenario.withLock { $0 = scenario }
        setup()
        let file = String(format: "%02d-%@.png", entries.count + 1, name)
        entries.append(Entry(file: file, summary: summary, source: source, controls: controls))
        if let only, !name.contains(only) { return }
        await render(view, width: width, height: height, to: directory.appendingPathComponent(file))
    }

    private func render(_ view: some View, width: CGFloat, height: CGFloat?, to url: URL?) async {
        let content = view.environment(model).environment(\.isSnapshot, true)
        let root = if let height {
            AnyView(content.frame(width: width, height: height, alignment: .top))
        } else {
            AnyView(content.frame(width: width))
        }
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: width, height: height ?? 400),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        let hosting = NSHostingView(rootView: root)
        window.contentView = hosting
        window.setFrameOrigin(NSPoint(x: 40, y: 40))
        window.orderFrontRegardless()
        // Let SwiftUI lay out, run `.task`s (the demo Core answers at once) and draw.
        try? await Task.sleep(for: .milliseconds(1200))
        if height == nil {
            window.setContentSize(NSSize(width: width, height: ceil(hosting.fittingSize.height)))
            try? await Task.sleep(for: .milliseconds(300))
        }
        hosting.layoutSubtreeIfNeeded()
        hosting.displayIfNeeded()
        if let url, let png = Self.png(of: hosting, in: window) {
            try? png.write(to: url)
        }
        window.orderOut(nil)
    }

    /// Renders the SwiftUI layer tree (cacheDisplay misses layer-hosted SwiftUI content) on the window
    /// background of the current appearance.
    private static func png(of view: NSView, in window: NSWindow) -> Data? {
        let bounds = view.bounds
        guard let layer = view.layer else {
            guard let rep = view.bitmapImageRepForCachingDisplay(in: bounds) else { return nil }
            view.cacheDisplay(in: bounds, to: rep)
            return rep.representation(using: .png, properties: [:])
        }
        let scale = window.backingScaleFactor
        let width = Int(bounds.width * scale)
        let height = Int(bounds.height * scale)
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
            let context = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                                    space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return nil }
        // CALayer rendering uses a top-left origin here; flip the bitmap context to match.
        context.translateBy(x: 0, y: CGFloat(height))
        context.scaleBy(x: scale, y: -scale)
        var background = NSColor.windowBackgroundColor.cgColor
        window.effectiveAppearance.performAsCurrentDrawingAppearance {
            background = NSColor.windowBackgroundColor.cgColor
        }
        context.setFillColor(background)
        context.fill(CGRect(origin: .zero, size: bounds.size))
        layer.render(in: context)
        guard let image = context.makeImage() else { return nil }
        return NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:])
    }

    // MARK: Index

    func writeIndex(dark: Bool) {
        let language = Bundle.main.preferredLocalizations.first ?? "?"
        var text = """
            # CloudWire UI-Snapshots (Sprache \(language), \(dark ? "dunkel" : "hell"))

            Erzeugt mit `CloudWire --export-snapshots <dir>` (DEBUG, App/Snapshots.swift). Demo-Daten: App/DemoData.swift; \
            die Views laden ihre Inhalte wie im Betrieb über den Core-Client, beantwortet von einem Demo-Core. \
            Hauptfenster 1200 × 760 pt, Sheets in ihrer eigenen Breite, Höhe passend; PNGs in Retina-Auflösung. \
            Bedienelemente mit ihren englischen Quelltexten (Lokalisierungsschlüsseln).

            | Datei | Zeigt | Quelle | Bedienelemente |
            |---|---|---|---|

            """
        for entry in entries {
            text += "| \(entry.file) | \(entry.summary) | \(entry.source) | \(entry.controls) |\n"
        }
        text += """

            ## Nicht gerendert

            - Alerts, Bestätigungsdialoge, NSAlerts, System-Panels (Öffnen/Sichern/Drucken), Mitteilungen und Menüs \
            (Pause-Menü, Zeilen-/Kontextmenüs, Aktivitätsfilter, Optionsvorschläge, App-Menü): eigene Fenster bzw. \
            NSMenu, offscreen nicht renderbar. Texte, Buttons und Quelle (Datei:Zeile) stehen in `dialogs.md`.
            - Popover „Browse…“ im MountEditor: Popover-Fenster nicht renderbar; der Inhalt ist in \
            `mounts-browse-popover` nachgebaut.
            - Fensterrahmen, Titelleisten, Sheet-Rahmen, die Toolbar-Tabs des Einstellungsfensters (hier eine \
            statische Kopie über dem Tab-Inhalt), Materialien/Liquid Glass (Karten mit flacher Füllung) und der \
            Hintergrund des Menüleistenpanels: Offscreen-Rendering erfasst keine Fenster-Chrome und Backdrop-Effekte.
            - Aktiver Fensterzustand: die App wird beim Export nicht aktiviert (kein Fokus-Diebstahl), daher zeigen \
            eingeschaltete Schalter, Standard-Buttons, Fortschrittsbalken, Auswahlen und Segmente die graue \
            Inaktiv-Darstellung statt der Akzentfarbe.
            - Seitenleiste des Hauptfensters: echte NavigationSplitView-Liste nicht erfassbar; statische Kopie mit \
            denselben Titeln, Symbolen und Fehler-Badges.
            - Kurzlebige Lade-/Busy-Zustände (Spinner beim Laden von Anbietern, Freigaben, Browser, laufende \
            Aktionen) und Inline-Fehler nach fehlgeschlagenen Aktionen.
            - Finder-Erweiterung (Kontextmenü, Sync-Badges): eigenes Target CloudWireFinder, keine SwiftUI-View der App.

            """
        try? text.write(to: directory.appendingPathComponent("INDEX.md"), atomically: true, encoding: .utf8)
    }
}
#endif
