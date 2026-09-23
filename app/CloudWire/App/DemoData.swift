#if DEBUG
import CloudWireKit
import Foundation
import os

/// Static demo content for `--export-snapshots`: typed values for `AppModel.loadDemoData()` and the
/// JSON the demo Core answers with.
enum DemoData {
    static func decode<T: Decodable>(_ json: String) -> T {
        // Demo JSON is a compile-time constant; failing to decode is a programming error.
        try! JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    private static func json(_ object: Any) -> String {
        String(decoding: try! JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]), as: UTF8.self)
    }

    /// Unix ms at launch; relative times ("10 minutes ago") are based on it.
    static let now = Int64(Date().timeIntervalSince1970 * 1000)
    private static let minute: Int64 = 60_000

    static let nextcloudId = "nc0000000001"
    static let driveId = "gd0000000002"
    static let vaultConnectionId = "vc0000000003"
    static let nasId = "sf0000000004"

    // MARK: Model state

    static let coreInfo: CoreInfo = decode(
        #"{"version":"0.1.0","apiVersion":1,"rcloneVersion":"v1.75.0","pid":4242,"startedAt":0,"appSupport":""}"#)

    static let settings: CoreSettings = decode(#"""
        {"pauseRules":{"studioMode":{"enabled":true,"apps":["/Applications/REAPER.app","/Applications/Logic Pro.app",
          "/Applications/Ableton Live 12 Suite.app"]},"battery":{"enabled":true},"meteredNetwork":{"enabled":true},
          "cpu":{"enabled":false,"thresholdPercent":70,"windowSeconds":30}},
         "bandwidth":{"enabled":true,"uploadMiBps":5,"downloadMiBps":0}}
        """#)

    static let fuseStatus: FuseStatus = decode(#"{"fuseT":true,"macFUSE":false}"#)

    static let update: UpdateStatus = decode(#"""
        {"current":"0.1.0","latest":"0.2.0","url":"https://github.com/DonMikone/CloudWire/releases/tag/v0.2.0",
         "available":true,"checkedAt":0}
        """#)

    static let connections: [Connection] = decode(#"""
        [{"id":"nc0000000001","name":"Nextcloud","kind":"remote","provider":"webdav","rcloneRemote":"cw-nc0000000001",
          "vendor":"nextcloud","serverURL":"https://cloud.example.com","user":"mike",
          "parameters":{"url":"https://cloud.example.com/remote.php/dav/files/mike","vendor":"nextcloud","user":"mike"},
          "createdAt":0},
         {"id":"gd0000000002","name":"Google Drive","kind":"remote","provider":"drive","rcloneRemote":"cw-gd0000000002",
          "vendor":"","serverURL":"","user":"","parameters":{"scope":"drive"},"createdAt":0},
         {"id":"sf0000000004","name":"Studio-NAS","kind":"remote","provider":"sftp","rcloneRemote":"cw-sf0000000004",
          "vendor":"","serverURL":"","user":"","parameters":{"host":"nas.studio.lan","user":"mike","port":"22"},
          "createdAt":0},
         {"id":"vc0000000003","name":"Tresor Verträge","kind":"vault","provider":"crypt",
          "rcloneRemote":"cwvault-vt0000000001","vendor":"","serverURL":"","user":"","parameters":{},"createdAt":0},
         {"id":"vc0000000005","name":"Tresor Privat","kind":"vault","provider":"crypt",
          "rcloneRemote":"cwvault-vt0000000002","vendor":"","serverURL":"","user":"","parameters":{},"createdAt":0}]
        """#)

    static let mounts: [Mount] = decode(#"""
        [{"id":"mt0000000001","connectionId":"nc0000000001","remotePath":"","mountPoint":"~/CloudWire/Mounts/Nextcloud",
          "volumeName":"Nextcloud","mountType":"nfsmount","autoMount":true,"readOnly":false,"cacheMaxGB":20,"options":{},
          "state":"mounted","error":"","createdAt":0},
         {"id":"mt0000000002","connectionId":"gd0000000002","remotePath":"Samples","mountPoint":"~/CloudWire/Mounts/Samples",
          "volumeName":"Google Drive – Samples","mountType":"nfsmount","autoMount":true,"readOnly":true,"cacheMaxGB":50,
          "options":{"vfs_read_ahead":"256Mi"},"state":"mounted","error":"","createdAt":0},
         {"id":"mt0000000003","connectionId":"vc0000000003","remotePath":"","mountPoint":"~/CloudWire/Mounts/Verträge",
          "volumeName":"Verträge","mountType":"cmount","autoMount":false,"readOnly":false,"cacheMaxGB":5,"options":{},
          "state":"unmounted","error":"","createdAt":0}]
        """#)

    /// Mounts with an error and one mounting.
    static var attentionMounts: [Mount] {
        var list = mounts
        list[1].state = .error
        list[1].error = "mount helper exited: NFS server did not respond within 10s"
        list[1].errorCode = "detail"
        list[1].errorParams = ["detail": list[1].error]
        list[2].state = .mounting
        return list
    }

    static var offlineItems: [OfflineItem] {
        decode(#"""
        [{"id":"of0000000001","connectionId":"nc0000000001","kind":"folder","remotePath":"Musik/Projekte/Album 2026",
          "files":[],"storagePath":"/Users/mike/CloudWire/Nextcloud/Musik/Projekte/Album 2026","excludes":[],"advanced":{},
          "state":"syncing","reason":"","lastSyncAt":\#(now - 10 * minute),"needsResync":false,"createdAt":0,
          "progress":{"bytes":734003200,"totalBytes":1288490188,"transfers":3,"eta":95},"isVault":false},
         {"id":"of0000000002","connectionId":"nc0000000001","kind":"folder","remotePath":"Musik/Samples/Drums",
          "files":[],"storagePath":"/Volumes/Studio SSD/Samples/Drums","excludes":[".DS_Store","._*","*.reapeaks","Backups/**"],
          "advanced":{"Transfers":8},"state":"idle","reason":"","lastSyncAt":\#(now - 2 * minute),"needsResync":false,
          "createdAt":0,"progress":null,"isVault":false},
         {"id":"of0000000003","connectionId":"gd0000000002","kind":"files","remotePath":"Mixes",
          "files":["Bassline.wav","Master v3.wav"],"storagePath":"/Users/mike/CloudWire/Google Drive/Mixes","excludes":[],
          "advanced":{},"state":"paused","reason":"studioMode","lastSyncAt":\#(now - 60 * minute),"needsResync":false,
          "createdAt":0,"progress":null,"isVault":false},
         {"id":"of0000000004","connectionId":"vc0000000003","kind":"folder","remotePath":"Laufend",
          "files":[],"storagePath":"/Users/mike/CloudWire/Verträge/Laufend","excludes":[],"advanced":{},
          "state":"idle","reason":"","lastSyncAt":\#(now - 180 * minute),"needsResync":false,"createdAt":0,
          "progress":null,"isVault":true}]
        """#)
    }

    /// Offline Items needing attention: Mass-Delete Guard, an error and a waiting item.
    static var attentionOfflineItems: [OfflineItem] {
        offlineItems + decode(#"""
        [{"id":"of0000000005","connectionId":"nc0000000001","kind":"folder","remotePath":"Musik/Stems",
          "files":[],"storagePath":"/Volumes/Studio SSD/Stems","excludes":[],"advanced":{},
          "state":"needsConfirmation","reason":"","lastSyncAt":\#(now - 30 * minute),"needsResync":false,"createdAt":0,
          "progress":null,"isVault":false,"massDelete":{"reason":"tooManyDeletes","side":"cloud","deletes":120,"total":200}},
         {"id":"of0000000006","connectionId":"nc0000000001","kind":"folder","remotePath":"Dokumente/Steuer 2025",
          "files":[],"storagePath":"/Users/mike/CloudWire/Nextcloud/Dokumente/Steuer 2025","excludes":[],"advanced":{},
          "state":"error","reason":"directory not found: Dokumente/Steuer 2025","lastSyncAt":\#(now - 1440 * minute),
          "reasonCode":"offline.cloudFolderMissing","reasonParams":{"detail":"directory not found: Dokumente/Steuer 2025"},
          "needsResync":true,"createdAt":0,"progress":null,"isVault":false},
         {"id":"of0000000007","connectionId":"gd0000000002","kind":"folder","remotePath":"Fotos/2026",
          "files":[],"storagePath":"/Users/mike/CloudWire/Google Drive/Fotos/2026","excludes":[],"advanced":{},
          "state":"pending","reason":"","lastSyncAt":null,"needsResync":false,"createdAt":0,"progress":null,
          "isVault":false}]
        """#)
    }

    static let vaults: [Vault] = decode(#"""
        [{"id":"vt0000000001","connectionId":"nc0000000001","vaultPath":"Dokumente/Verträge.cwvault","name":"Verträge",
          "vaultConnectionId":"vc0000000003","unlockMode":"keychain","unlocked":true,"createdAt":0},
         {"id":"vt0000000002","connectionId":"gd0000000002","vaultPath":"Privat.cwvault","name":"Privat",
          "vaultConnectionId":"vc0000000005","unlockMode":"ask","unlocked":false,"createdAt":0}]
        """#)

    /// Studio Mode holds syncing.
    static let pause: PauseStatus = decode(
        #"""
        {"manualUntil":null,"activeRules":[{"id":"studioMode","detail":"REAPER","code":"pause.studioMode",
         "params":{"app":"REAPER"},"message":"Studio Mode (REAPER)"}],"effective":true}
        """#)

    /// Paused manually for two more hours.
    static var manualPause: PauseStatus {
        decode(#"{"manualUntil":\#(now + 120 * minute),"activeRules":[],"effective":true}"#)
    }

    static let recoveryKey = "7K2Q-9XWD-M4PA-LR8T-3HCZ-QV6N-B1FJ-E5YS"

    static func migration(_ status: String) -> VaultMigrationEvent {
        let mismatches = status == "mismatch" ? #"["Belege/Quittung 0412.pdf","Belege/Quittung 0413.pdf"]"# : "[]"
        return decode(#"""
            {"jobId":"job-demo","vaultId":"vt0000000003","status":"\#(status)","mismatches":\#(mismatches),"error":null}
            """#)
    }

    static let sharees: [Sharee] = decode(#"""
        [{"label":"Anna Berger","shareType":0,"shareWith":"anna"},
         {"label":"Andreas König","shareType":0,"shareWith":"andreas"},
         {"label":"Studio-Team","shareType":1,"shareWith":"studio"}]
        """#)

    static var shares: [Share] { decode(sharesJSON) }

    static var activity: [ActivityEntry] { decode(activityJSON) }

    /// Extra problems for the attention state of the overview.
    static var attentionProblems: [ActivityEntry] {
        decode(#"""
        [{"id":14,"ts":\#(now - minute),"level":"error","category":"sync","subjectId":"of0000000005",
          "message":"Mass-Delete Guard stopped \"Stems\": Safety abort: too many deletes (>50%, 120 of 200) on Path2.",
          "code":"sync.massDelete","params":{"name":"Stems",
          "detail":"Safety abort: too many deletes (>50%, 120 of 200) on Path2."},"details":null},
         {"id":13,"ts":\#(now - 4 * minute),"level":"error","category":"sync","subjectId":"of0000000006",
          "message":"Sync of \"Steuer 2025\" failed: directory not found: Dokumente/Steuer 2025",
          "code":"sync.failed","params":{"name":"Steuer 2025","detail":"directory not found: Dokumente/Steuer 2025"},
          "details":null}]
        """#) + activity.filter { $0.level == .error || $0.level == .warn }
    }

    static var providers: [RcloneProvider] { decode(providersJSON) }

    static var connectionQuestion: ConfigStep {
        decode(#"""
        {"connectionId":"od0000000009","done":false,"pending":false,"state":"choose_type","error":"",
         "option":{"Name":"config_type","Help":"Type of connection","Type":"string","Default":"onedrive",
          "DefaultStr":"onedrive","Required":true,"Exclusive":true,"Examples":[
           {"Value":"onedrive","Help":"OneDrive Personal or Business"},
           {"Value":"sharepoint","Help":"Root Sharepoint site"},
           {"Value":"url","Help":"Sharepoint site name or URL\nE.g. mysite or https://contoso.sharepoint.com/sites/mysite"},
           {"Value":"search","Help":"Search for a Sharepoint site"},
           {"Value":"driveid","Help":"Type in driveID (advanced)"},
           {"Value":"siteid","Help":"Type in SiteID (advanced)"},
           {"Value":"path","Help":"Sharepoint server-relative path (advanced)\nE.g. /teams/hr"}]}}
        """#)
    }

    // MARK: Core JSON

    static var sharesJSON: String {
        #"""
        [{"id":"11","kind":"publicLink","path":"Musik/Projekte/Album 2026","itemType":"folder",
          "url":"https://cloud.example.com/s/Xk3pQ9aLm2","token":"Xk3pQ9aLm2","shareWith":"","shareWithDisplayName":"",
          "permissions":1,"expireDate":"2026-10-23","hasPassword":true,"hideDownload":false,"label":"Mastering",
          "note":"","createdAt":0},
         {"id":"12","kind":"user","path":"Musik/Projekte/Album 2026","itemType":"folder","url":"","token":"",
          "shareWith":"anna","shareWithDisplayName":"Anna Berger","permissions":15,"expireDate":null,"hasPassword":false,
          "hideDownload":false,"label":"","note":"","createdAt":0},
         {"id":"13","kind":"publicLink","path":"Musik/Mixes/Bassline.wav","itemType":"file",
          "url":"https://cloud.example.com/s/Rt7Vw2Nc8e","token":"Rt7Vw2Nc8e","shareWith":"","shareWithDisplayName":"",
          "permissions":1,"expireDate":null,"hasPassword":false,"hideDownload":true,"label":"","note":"","createdAt":0},
         {"id":"14","kind":"group","path":"Musik/Samples/Drums","itemType":"folder","url":"","token":"",
          "shareWith":"studio","shareWithDisplayName":"Studio-Team","permissions":31,"expireDate":null,
          "hasPassword":false,"hideDownload":false,"label":"","note":"","createdAt":0},
         {"id":"15","kind":"email","path":"Musik/Mixes/Master v3.wav","itemType":"file",
          "url":"https://cloud.example.com/s/Pq8Zt4Hw1k","token":"Pq8Zt4Hw1k","shareWith":"label@example.com",
          "shareWithDisplayName":"label@example.com","permissions":1,"expireDate":"2026-09-30","hasPassword":false,
          "hideDownload":false,"label":"","note":"Final master for the release","createdAt":0}]
        """#
    }

    static var activityJSON: String {
        #"""
        [{"id":12,"ts":\#(now - minute),"level":"warn","category":"offline","subjectId":"",
          "message":"Syncing paused: Studio Mode (REAPER)","code":"offline.pausedByRule",
          "params":{"cause":"pause.studioMode","app":"REAPER"},"details":null},
         {"id":11,"ts":\#(now - 3 * minute),"level":"info","category":"offline","subjectId":"of0000000001",
          "message":"Settings of \"Album 2026\" changed","code":"offline.settingsChanged",
          "params":{"name":"Album 2026"},"details":null},
         {"id":10,"ts":\#(now - 10 * minute),"level":"info","category":"sync","subjectId":"of0000000002",
          "message":"Synced \"Drums\": 12 transferred, 0 deleted, 0 conflicts","code":"sync.done",
          "params":{"name":"Drums","transferred":"12","deleted":"0","conflicts":"0"},"details":{"runId":42}},
         {"id":9,"ts":\#(now - 25 * minute),"level":"warn","category":"sync","subjectId":"of0000000002",
          "message":"1 conflict copies created in \"Drums\"","code":"sync.conflicts",
          "params":{"count":"1","name":"Drums"},"details":{"runId":41}},
         {"id":8,"ts":\#(now - 40 * minute),"level":"info","category":"share","subjectId":"nc0000000001",
          "message":"Created public link for \"/Musik/Projekte/Album 2026\"","code":"share.linkCreated",
          "params":{"path":"/Musik/Projekte/Album 2026"},"details":null},
         {"id":7,"ts":\#(now - 55 * minute),"level":"error","category":"mount","subjectId":"mt0000000002",
          "message":"Mount \"Google Drive – Samples\" stopped unexpectedly: NFS server did not respond within 10s",
          "code":"mount.stoppedUnexpectedly",
          "params":{"name":"Google Drive – Samples","detail":"NFS server did not respond within 10s"},
          "details":{"mountPoint":"~/CloudWire/Mounts/Samples","attempt":1}},
         {"id":6,"ts":\#(now - 56 * minute),"level":"info","category":"vault","subjectId":"vt0000000001",
          "message":"Vault \"Verträge\" unlocked","code":"vault.unlocked","params":{"name":"Verträge"},"details":null},
         {"id":5,"ts":\#(now - 57 * minute),"level":"info","category":"mount","subjectId":"mt0000000001",
          "message":"Mounted \"Nextcloud\" at ~/CloudWire/Mounts/Nextcloud","code":"mount.mounted",
          "params":{"name":"Nextcloud","path":"~/CloudWire/Mounts/Nextcloud"},"details":null},
         {"id":4,"ts":\#(now - 58 * minute),"level":"info","category":"connection","subjectId":"sf0000000004",
          "message":"Connection \"Studio-NAS\" added (sftp)","code":"connection.added",
          "params":{"name":"Studio-NAS","provider":"sftp"},"details":null},
         {"id":3,"ts":\#(now - 59 * minute),"level":"info","category":"core","subjectId":"",
          "message":"Network changed (online)","code":"core.networkOnline","details":null},
         {"id":2,"ts":\#(now - 60 * minute),"level":"info","category":"core","subjectId":"",
          "message":"CloudWire Core 0.1.0 started","code":"core.started","params":{"version":"0.1.0"},"details":null},
         {"id":1,"ts":\#(now - 60 * minute),"level":"debug","category":"core","subjectId":"",
          "message":"First start: conflict label \"Konflikt\", 2 Studio Mode app(s) detected","code":"core.firstStart",
          "params":{"label":"Konflikt","count":"2"},"details":["/Applications/REAPER.app","/Applications/Logic Pro.app"]}]
        """#
    }

    static var syncRunsJSON: String {
        #"""
        [{"id":42,"itemId":"of0000000002","kind":"bisync","startedAt":\#(now - 10 * minute),
          "finishedAt":\#(now - 9 * minute),"status":"ok","transferred":12,"deleted":0,"conflicts":0,"bytes":184549376,"error":""},
         {"id":41,"itemId":"of0000000002","kind":"bisync","startedAt":\#(now - 25 * minute),
          "finishedAt":\#(now - 24 * minute),"status":"ok","transferred":3,"deleted":1,"conflicts":1,"bytes":20971520,"error":""},
         {"id":40,"itemId":"of0000000002","kind":"bisync","startedAt":\#(now - 85 * minute),
          "finishedAt":\#(now - 85 * minute),"status":"error","transferred":0,"deleted":0,"conflicts":0,"bytes":0,
          "error":"couldn't list directory: 503 Service Unavailable"},
         {"id":39,"itemId":"of0000000002","kind":"bisync","startedAt":\#(now - 145 * minute),
          "finishedAt":\#(now - 145 * minute),"status":"ok","transferred":0,"deleted":0,"conflicts":0,"bytes":0,"error":""},
         {"id":38,"itemId":"of0000000002","kind":"resync","startedAt":\#(now - 2880 * minute),
          "finishedAt":\#(now - 2860 * minute),"status":"ok","transferred":214,"deleted":0,"conflicts":0,
          "bytes":3221225472,"error":""}]
        """#
    }

    static func runFilesJSON(runId: Int) -> String {
        if runId == 40 { return "[]" }
        return #"""
            [{"action":"transferred","path":"Kicks/Kick 04.wav"},{"action":"transferred","path":"Kicks/Kick 05.wav"},
             {"action":"transferred","path":"Snares/Snare Room 02.wav"},{"action":"transferred","path":"Hats/Hat Open 03.wav"},
             {"action":"deleted","path":"Hats/Hat Closed 01 (alt).wav"},
             {"action":"conflict","path":"Kicks/Kick 04.conflict-2026-09-23.wav"}]
            """#
    }

    static func browseJSON(path: String) -> String {
        func folder(_ name: String) -> [String: Any] {
            ["name": name, "path": path.isEmpty ? name : "\(path)/\(name)", "isDir": true, "size": 0,
             "modTime": now - 3 * 1440 * minute, "mimeType": "inode/directory"]
        }
        func file(_ name: String, _ size: Int64) -> [String: Any] {
            ["name": name, "path": path.isEmpty ? name : "\(path)/\(name)", "isDir": false, "size": size,
             "modTime": now - 1440 * minute, "mimeType": "application/octet-stream"]
        }
        let entries: [[String: Any]]
        switch path {
        case "":
            entries = [folder("Dokumente"), folder("Fotos"), folder("Musik"), folder("Privat.cwvault"),
                       file("Notizen.md", 4_096), file("Rider 2026.pdf", 1_258_291)]
        case "Dokumente":
            entries = [folder("Rechnungen"), folder("Steuer 2025"), folder("Verträge.cwvault"),
                       file("Lebenslauf.pdf", 184_320)]
        case "Musik":
            entries = [folder("Mixes"), folder("Projekte"), folder("Samples"), folder("Stems")]
        case "Musik/Projekte":
            entries = [folder("Album 2026"), folder("EP Sommer"), folder("Remix-Anfragen"), file("Setlist.pdf", 90_112)]
        case "Musik/Projekte/Album 2026":
            entries = [folder("Artwork"), file("01 Intro.wav", 52_428_800), file("02 Nachtfahrt.wav", 63_963_136),
                       file("03 Bassline.wav", 58_720_256), file("Album 2026.RPP", 2_097_152)]
        case "Mixes", "Musik/Mixes":
            entries = [file("Bassline.wav", 58_720_256), file("Master v2.wav", 61_865_984),
                       file("Master v3.wav", 62_914_560)]
        default:
            entries = []
        }
        return json(entries)
    }

    static func preflightJSON(remotePath: String, lowSpace: Bool) -> String {
        json(["remoteBytes": lowSpace ? 412_316_860_416 : 1_288_490_188,
              "freeBytes": lowSpace ? 96_636_764_160 : 412_316_860_416, "storageNonEmpty": false,
              "storagePath": "/Users/mike/CloudWire/Nextcloud/\(remotePath)"])
    }

    // MARK: rclone metadata

    private static func option(_ name: String, _ help: String, type: String = "string", default value: Any = "",
                               defaultString: String? = nil, advanced: Bool = false, required: Bool = false,
                               password: Bool = false, examples: [(String, String)] = [], exclusive: Bool = false,
                               provider: String = "") -> [String: Any]
    {
        let fieldName = name.split(separator: "_").map { $0.prefix(1).uppercased() + $0.dropFirst() }.joined()
        return ["Name": name, "FieldName": fieldName, "Help": help, "Type": type, "Default": value,
                "DefaultStr": defaultString ?? "\(value)", "Advanced": advanced, "Required": required,
                "IsPassword": password, "Examples": examples.map { ["Value": $0.0, "Help": $0.1] },
                "Exclusive": exclusive, "Provider": provider]
    }

    private static var clientId: [String: Any] { option("client_id", "OAuth Client Id.\nLeave blank normally.") }
    private static var clientSecret: [String: Any] {
        option("client_secret", "OAuth Client Secret.\nLeave blank normally.")
    }

    static let providersJSON: String = {
        func provider(_ name: String, _ description: String, hide: Bool = false, _ options: [[String: Any]])
            -> [String: Any]
        {
            ["Name": name, "Description": description, "Prefix": name, "Options": options, "Hide": hide]
        }
        return json([
            provider("b2", "Backblaze B2", [
                option("account", "Account ID or Application Key ID.", required: true),
                option("key", "Application Key.", required: true),
                option("hard_delete", "Permanently delete files on remote removal, otherwise hide files.",
                       type: "bool", default: false),
                option("chunk_size", "Upload chunk size.\nWhen uploading large files, chunk the file into this size.",
                       type: "SizeSuffix", default: 100_663_296, defaultString: "96Mi", advanced: true),
            ]),
            provider("crypt", "Encrypt/Decrypt a remote", [option("remote", "Remote to encrypt/decrypt.", required: true)]),
            provider("drive", "Google Drive", [
                option("client_id", "Google Application Client Id\nSetting your own is recommended.\nSee https://rclone.org/drive/#making-your-own-client-id for how to create your own.\nIf you leave this blank, it will use an internal key which is low performance."),
                clientSecret,
                option("scope", "Comma separated list of scopes that rclone should use when requesting access from drive.",
                       examples: [
                           ("drive", "Full access all files, excluding Application Data Folder."),
                           ("drive.readonly", "Read-only access to file metadata and file contents."),
                           ("drive.file", "Access to files created by rclone only.\nThese are visible in the drive website."),
                           ("drive.appfolder", "Allows read and write access to the Application Data folder."),
                           ("drive.metadata.readonly", "Allows read-only access to file metadata but\ndoes not allow any access to read or download file content."),
                       ]),
                option("service_account_file", "Service Account Credentials JSON file path.\nLeave blank normally.\nNeeded only if you want use SA instead of interactive login."),
                option("root_folder_id", "ID of the root folder.\nLeave blank normally.", advanced: true),
                option("team_drive", "ID of the Shared Drive (Team Drive).", advanced: true),
                option("use_trash", "Send files to the trash instead of deleting permanently.", type: "bool",
                       default: true, advanced: true),
                option("skip_gdocs", "Skip google documents in all listings.", type: "bool", default: false,
                       advanced: true),
                option("export_formats", "Comma separated list of preferred formats for downloading Google docs.",
                       default: "docx,xlsx,pptx,svg", advanced: true),
                option("chunk_size", "Upload chunk size.\nMust a power of 2 >= 256k.", type: "SizeSuffix",
                       default: 8_388_608, defaultString: "8Mi", advanced: true),
                option("pacer_min_sleep", "Minimum time to sleep between API calls.", type: "Duration",
                       default: 100_000_000, defaultString: "100ms", advanced: true),
            ]),
            provider("dropbox", "Dropbox", [
                clientId, clientSecret,
                option("chunk_size", "Upload chunk size (< 150Mi).", type: "SizeSuffix", default: 50_331_648,
                       defaultString: "48Mi", advanced: true),
                option("impersonate", "Impersonate this user when using a business account.", advanced: true),
            ]),
            provider("memory", "In memory object storage system.", hide: true, []),
            provider("onedrive", "Microsoft OneDrive", [
                clientId, clientSecret,
                option("region", "Choose national cloud region for OneDrive.", default: "global", examples: [
                    ("global", "Microsoft Cloud Global"), ("us", "Microsoft Cloud for US Government"),
                    ("de", "Microsoft Cloud Germany (deprecated - try global region first)."),
                    ("cn", "Azure and Office 365 operated by Vnet Group in China"),
                ], exclusive: true),
                option("drive_type", "The type of the drive (personal | business | documentLibrary).", advanced: true),
            ]),
            provider("pcloud", "Pcloud", [
                clientId, clientSecret,
                option("hostname", "Hostname to connect to.", default: "api.pcloud.com", examples: [
                    ("api.pcloud.com", "Original/US region"), ("eapi.pcloud.com", "EU region"),
                ], exclusive: true),
            ]),
            provider("s3", "Amazon S3 Compliant Storage Providers including AWS, Cloudflare, Minio, Wasabi and others", [
                option("provider", "Choose your S3 provider.", examples: [
                    ("AWS", "Amazon Web Services (AWS) S3"), ("Cloudflare", "Cloudflare R2 Storage"),
                    ("IDrive", "IDrive e2"), ("Minio", "Minio Object Storage"), ("Wasabi", "Wasabi Object Storage"),
                    ("Other", "Any other S3 compatible provider"),
                ], exclusive: true),
                option("env_auth", "Get AWS credentials from runtime (environment variables or EC2/ECS meta data if no env vars).\nOnly applies if access_key_id and secret_access_key is blank.",
                       type: "bool", default: false),
                option("access_key_id", "AWS Access Key ID.\nLeave blank for anonymous access or runtime credentials."),
                option("secret_access_key", "AWS Secret Access Key (password).\nLeave blank for anonymous access or runtime credentials."),
                option("region", "Region to connect to.", examples: [
                    ("us-east-1", "The default endpoint - a good choice if you are unsure."),
                    ("eu-central-1", "EU (Frankfurt) Region."),
                ], provider: "AWS"),
                option("endpoint", "Endpoint for S3 API.\nRequired when using an S3 clone.", provider: "!AWS"),
                option("acl", "Canned ACL used when creating buckets and storing or copying objects.", advanced: true),
            ]),
            provider("sftp", "SSH/SFTP", [
                option("host", "SSH host to connect to.\nE.g. \"example.com\".", required: true),
                option("user", "SSH username.", default: "mike"),
                option("port", "SSH port number.", type: "int", default: 22),
                option("pass", "SSH password, leave blank to use ssh-agent.", password: true),
                option("key_file", "Path to PEM-encoded private key file.\nLeave blank or set key-use-agent to use ssh-agent."),
                option("key_use_agent", "When set forces the usage of the ssh-agent.", type: "bool", default: false,
                       advanced: true),
                option("shell_type", "The type of SSH shell on remote server, if any.", advanced: true, examples: [
                    ("none", "No shell access"), ("unix", "Unix shell"), ("powershell", "PowerShell"), ("cmd", "Windows Command Prompt"),
                ]),
            ]),
            provider("webdav", "WebDAV", [
                option("url", "URL of http host to connect to.\nE.g. https://example.com.", required: true),
                option("vendor", "Name of the WebDAV site/service/software you are using.", examples: [
                    ("nextcloud", "Nextcloud"), ("owncloud", "Owncloud 10 PHP based WebDAV server"),
                    ("infinitescale", "ownCloud Infinite Scale"),
                    ("sharepoint", "Sharepoint Online, authenticated by Microsoft account"),
                    ("rclone", "rclone WebDAV server to serve a remote over HTTP via the WebDAV protocol"),
                    ("other", "Other site/service or software"),
                ]),
                option("user", "User name.\nIn case NTLM authentication is used, the username should be in the format 'Domain\\User'."),
                option("pass", "Password.", password: true),
                option("bearer_token", "Bearer token instead of user/pass (e.g. a Macaroon)."),
                option("headers", "Set HTTP headers for all transactions.", type: "CommaSepList", advanced: true),
                option("pacer_min_sleep", "Minimum time to sleep between API calls.", type: "Duration",
                       default: 10_000_000, defaultString: "10ms", advanced: true),
                option("nextcloud_chunk_size", "Nextcloud upload chunk size.\nWe recommend configuring your NextCloud instance to increase the max chunk size to 1 GB for better upload performances.",
                       type: "SizeSuffix", default: 10_485_760, defaultString: "10Mi", advanced: true),
                option("owncloud_exclude_shares", "Exclude ownCloud shares", type: "bool", default: false, advanced: true),
            ]),
        ])
    }()

    static let mountOptionsJSON: String = json([
        "vfs": [
            option("vfs_cache_mode", "Cache mode off|minimal|writes|full", default: "full"),
            option("vfs_cache_max_size", "Max total size of objects in the cache", type: "SizeSuffix", default: -1,
                   defaultString: "off"),
            option("vfs_cache_max_age", "Max time since last access of objects in the cache", type: "Duration",
                   default: 3_600_000_000_000, defaultString: "1h0m0s"),
            option("vfs_cache_poll_interval", "Interval to poll the cache for stale objects", type: "Duration",
                   default: 60_000_000_000, defaultString: "1m0s"),
            option("vfs_read_chunk_size", "Read the source objects in chunks", type: "SizeSuffix",
                   default: 134_217_728, defaultString: "128Mi"),
            option("vfs_read_ahead", "Extra read ahead over --buffer-size when using cache-mode full",
                   type: "SizeSuffix", default: 0, defaultString: "0"),
            option("vfs_write_back", "Time to writeback files after last use when using cache", type: "Duration",
                   default: 5_000_000_000, defaultString: "5s"),
            option("dir_cache_time", "Time to cache directory entries for", type: "Duration",
                   default: 300_000_000_000, defaultString: "5m0s"),
            option("poll_interval", "Time to wait between polling for changes. Must be smaller than dir-cache-time. Only on supported remotes. Set to 0 to disable",
                   type: "Duration", default: 60_000_000_000, defaultString: "1m0s"),
            option("no_modtime", "Don't read/write the modification time (can speed things up)", type: "bool",
                   default: false),
            option("vfs_case_insensitive", "If a file name not found, find a case insensitive match", type: "bool",
                   default: false),
            option("read_only", "Only allow read-only access", type: "bool", default: false),
        ],
        "mount": [
            option("allow_non_empty", "Allow mounting over a non-empty directory", type: "bool", default: false),
            option("attr_timeout", "Time for which file/directory attributes are cached", type: "Duration",
                   default: 1_000_000_000, defaultString: "1s"),
            option("daemon_timeout", "Time limit for rclone to respond to kernel", type: "Duration", default: 0,
                   defaultString: "0s"),
            option("noappledouble", "Ignore Apple Double (._) and .DS_Store files (supported on OSX only)",
                   type: "bool", default: true),
            option("noapplexattr", "Ignore all \"com.apple.*\" extended attributes (supported on OSX only)",
                   type: "bool", default: false),
            option("volname", "Set the volume name"),
        ],
        "nfs": [
            option("nfs_cache_handle_limit", "max file handles cached simultaneously (min 5)", type: "int",
                   default: 1_000_000),
            option("nfs_cache_type", "Type of NFS handle cache to use", default: "memory", examples: [
                ("memory", ""), ("disk", ""), ("symlink", ""),
            ], exclusive: true),
            option("nfs_cache_dir", "The directory the NFS handle cache will use if set"),
        ],
    ])

    static let mainOptionsJSON: String = json([
        "main": [
            option("transfers", "Number of file transfers to run in parallel", type: "int", default: 4),
            option("checkers", "Number of checkers to run in parallel", type: "int", default: 8),
            option("retries", "Retry operations this many times if they fail", type: "int", default: 3),
            option("low_level_retries", "Number of low level retries to do", type: "int", default: 10),
            option("bwlimit", "Bandwidth limit in KiB/s, or use suffix B|K|M|G|T|P or a full timetable",
                   type: "BwTimetable", default: "off"),
            option("buffer_size", "In memory buffer size when reading files for each --transfer",
                   type: "SizeSuffix", default: 16_777_216, defaultString: "16Mi"),
            option("contimeout", "Connect timeout", type: "Duration", default: 60_000_000_000, defaultString: "1m0s"),
            option("timeout", "IO idle timeout", type: "Duration", default: 300_000_000_000, defaultString: "5m0s"),
            option("fast_list", "Use recursive list if available; uses more memory but fewer transactions",
                   type: "bool", default: false),
            option("checksum", "Check for changes with size & checksum (if available, or fallback to size only)",
                   type: "bool", default: false),
            option("ignore_checksum", "Skip post copy check of checksums", type: "bool", default: false),
            option("track_renames", "When synchronizing, track file renames and do a server-side move if possible",
                   type: "bool", default: false),
            option("multi_thread_streams", "Number of streams to use for multi-thread downloads", type: "int",
                   default: 4),
        ],
    ])
}

/// A stand-in for the Core: answers the JSON-RPC calls views make with `DemoData` over a socket pair
/// attached to `AppModel.client`, so every view loads its content the real way.
enum DemoCore {
    enum Scenario: Sendable {
        case normal
        /// Every list is empty.
        case empty
        /// The offline preflight reports too little free space.
        case lowSpace
    }

    static let scenario = OSAllocatedUnfairLock(initialState: Scenario.normal)

    static func attach(to client: CoreClient) async {
        var fds: [Int32] = [0, 0]
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0 else { return }
        await client.attach(fileDescriptor: fds[0])
        let serverFD = fds[1]
        Thread.detachNewThread { serve(serverFD) }
    }

    private static func serve(_ fd: Int32) {
        var buffer = Data()
        var chunk = [UInt8](repeating: 0, count: 65_536)
        while true {
            let count = read(fd, &chunk, chunk.count)
            guard count > 0 else { return }
            buffer.append(contentsOf: chunk[0..<count])
            while let newline = buffer.firstIndex(of: 0x0A) {
                let line = Data(buffer[buffer.startIndex..<newline])
                buffer.removeSubrange(buffer.startIndex...newline)
                guard let reply = reply(to: line) else { continue }
                let bytes = Array((reply + "\n").utf8)
                var offset = 0
                while offset < bytes.count {
                    let written = bytes[offset...].withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
                    guard written > 0 else { return }
                    offset += written
                }
            }
        }
    }

    private static func reply(to line: Data) -> String? {
        guard let request = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
            let id = request["id"] as? Int, let method = request["method"] as? String
        else { return nil }
        let params = request["params"] as? [String: Any] ?? [:]
        if let result = result(method, params, scenario.withLock { $0 }) {
            // The protocol is one JSON value per line; the demo JSON is written across lines.
            return #"{"jsonrpc":"2.0","id":\#(id),"result":\#(result.replacingOccurrences(of: "\n", with: ""))}"#
        }
        return #"{"jsonrpc":"2.0","id":\#(id),"error":{"code":-32601,"message":"Not part of the snapshot demo: \#(method)"}}"#
    }

    private static func result(_ method: String, _ params: [String: Any], _ scenario: Scenario) -> String? {
        let empty = scenario == .empty
        let connectionId = params["connectionId"] as? String ?? ""
        switch method {
        case "providers.list": return #"{"providers":\#(DemoData.providersJSON)}"#
        case "options.mountInfo": return DemoData.mountOptionsJSON
        case "options.mainInfo": return DemoData.mainOptionsJSON
        case "mounts.fuseStatus": return #"{"fuseT":true,"macFUSE":false}"#
        case "connections.browse": return empty ? "[]" : DemoData.browseJSON(path: params["path"] as? String ?? "")
        case "offline.preflight":
            return DemoData.preflightJSON(remotePath: params["remotePath"] as? String ?? "",
                                          lowSpace: scenario == .lowSpace)
        case "offline.runs": return empty ? "[]" : DemoData.syncRunsJSON
        case "offline.runFiles": return empty ? "[]" : DemoData.runFilesJSON(runId: params["runId"] as? Int ?? 0)
        case "activity.query": return empty ? "[]" : DemoData.activityJSON
        case "shares.capabilities":
            switch connectionId {
            case DemoData.nextcloudId:
                return #"{"publicLink":true,"internalLink":true,"userShare":true,"emailShare":true,"webURL":true,"manage":true,"linkExpiry":true}"#
            case DemoData.driveId: return #"{"publicLink":true,"manage":true}"#
            default: return #"{"reason":"unsupported"}"#
            }
        case "shares.policy": return #"{"passwordEnforced":false,"expireDateEnforced":false,"expireDateDays":0}"#
        case "shares.list":
            guard !empty, connectionId == DemoData.nextcloudId,
                let all = try? JSONSerialization.jsonObject(with: Data(DemoData.sharesJSON.utf8)) as? [[String: Any]]
            else { return "[]" }
            let path = params["path"] as? String
            let shares = all.filter { path == nil || $0["path"] as? String == path }
            return String(decoding: try! JSONSerialization.data(withJSONObject: shares), as: UTF8.self)
        case "shares.searchSharees":
            return #"[{"label":"Anna Berger","shareType":0,"shareWith":"anna"},{"label":"Andreas König","shareType":0,"shareWith":"andreas"}]"#
        case "shares.internalLink": return #"{"url":"https://cloud.example.com/f/48213","created":false}"#
        default: return nil
        }
    }
}
#endif
