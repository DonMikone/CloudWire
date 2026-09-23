#if DEBUG
import AppKit
import CloudWireKit
import SwiftUI

/// `--export-snapshots <dir>`: renders the main window (Übersicht, Offline, Freigaben) and the share
/// window with demo data to PNG files, then quits. DEBUG builds only.
@MainActor
enum SnapshotExporter {
    static func run(directory: URL) {
        let model = AppModel.shared
        model.loadDemoData()
        NSApp.setActivationPolicy(.regular)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        Task { @MainActor in
            let size = NSSize(width: 1200, height: 760)
            let sections: [(SidebarSection, String)] = [(.overview, "overview"), (.offline, "offline"), (.shares, "shares")]
            // Warm-up pass: the first SwiftUI window needs longer before everything is drawn.
            model.selection = .overview
            await render(MainWindowContent().environment(model).environment(\.isSnapshot, true), size: size, to: nil)
            for (section, name) in sections {
                model.selection = section
                let view = MainWindowContent().environment(model).environment(\.isSnapshot, true)
                await render(view, size: size, to: directory.appendingPathComponent("\(name).png"))
            }
            let target = ShareTarget(connectionId: DemoData.connections[0].id, path: "Musik/Projekte/Album 2026",
                                     isDir: true, name: "Album 2026")
            let share = ShareWindow(target: target).environment(model).environment(\.isSnapshot, true)
            await render(share, size: NSSize(width: 560, height: 700), to: directory.appendingPathComponent("share.png"))
            NSApp.terminate(nil)
        }
    }

    private static func render(_ view: some View, size: NSSize, to url: URL?) async {
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: size),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        let hosting = NSHostingView(rootView: view.frame(width: size.width, height: size.height, alignment: .top))
        window.contentView = hosting
        window.setFrameOrigin(NSPoint(x: 40, y: 40))
        window.orderFrontRegardless()
        // Let SwiftUI lay out, run `.task`s and draw.
        try? await Task.sleep(for: .milliseconds(1200))
        hosting.layoutSubtreeIfNeeded()
        hosting.displayIfNeeded()
        let bounds = hosting.bounds
        let png: Data?
        if let layer = hosting.layer {
            png = renderLayer(layer, bounds: bounds, scale: window.backingScaleFactor)
        } else if let rep = hosting.bitmapImageRepForCachingDisplay(in: bounds) {
            hosting.cacheDisplay(in: bounds, to: rep)
            png = rep.representation(using: .png, properties: [:])
        } else {
            png = nil
        }
        if let url { try? png?.write(to: url) }
        window.orderOut(nil)
    }

    /// Renders the SwiftUI layer tree (cacheDisplay misses layer-hosted SwiftUI content).
    private static func renderLayer(_ layer: CALayer, bounds: NSRect, scale: CGFloat) -> Data? {
        let width = Int(bounds.width * scale)
        let height = Int(bounds.height * scale)
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
            let context = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                                    space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return nil }
        // CALayer rendering uses a top-left origin here; flip the bitmap context to match.
        context.translateBy(x: 0, y: CGFloat(height))
        context.scaleBy(x: scale, y: -scale)
        context.setFillColor(NSColor.windowBackgroundColor.cgColor)
        context.fill(CGRect(origin: .zero, size: bounds.size))
        layer.render(in: context)
        guard let image = context.makeImage() else { return nil }
        return NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:])
    }
}

/// Static demo content for snapshots.
enum DemoData {
    private static func decode<T: Decodable>(_ json: String) -> T {
        // Demo JSON is a compile-time constant; failing to decode is a programming error.
        try! JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    private static let now = Int64(Date().timeIntervalSince1970 * 1000)

    static let coreInfo: CoreInfo = decode(
        #"{"version":"0.1.0","apiVersion":1,"rcloneVersion":"v1.75.0","pid":4242,"startedAt":0,"appSupport":""}"#)

    static let connections: [Connection] = decode(#"""
        [{"id":"nc0000000001","name":"Nextcloud","kind":"remote","provider":"webdav","rcloneRemote":"cw-nc0000000001",
          "vendor":"nextcloud","serverURL":"https://cloud.example.com","user":"mike","parameters":{},"createdAt":0},
         {"id":"gd0000000002","name":"Google Drive","kind":"remote","provider":"drive","rcloneRemote":"cw-gd0000000002",
          "vendor":"","serverURL":"","user":"","parameters":{},"createdAt":0},
         {"id":"vc0000000003","name":"Tresor Verträge","kind":"vault","provider":"crypt","rcloneRemote":"cwvault-vt0000000001",
          "vendor":"","serverURL":"","user":"","parameters":{},"createdAt":0}]
        """#)

    static let mounts: [Mount] = decode(#"""
        [{"id":"mt0000000001","connectionId":"nc0000000001","remotePath":"","mountPoint":"~/CloudWire/Laufwerke/Nextcloud",
          "volumeName":"Nextcloud","mountType":"nfsmount","autoMount":true,"readOnly":false,"cacheMaxGB":20,"options":{},
          "state":"mounted","error":"","createdAt":0},
         {"id":"mt0000000002","connectionId":"gd0000000002","remotePath":"Samples","mountPoint":"~/CloudWire/Laufwerke/Samples",
          "volumeName":"Google Drive – Samples","mountType":"nfsmount","autoMount":true,"readOnly":true,"cacheMaxGB":50,
          "options":{},"state":"mounted","error":"","createdAt":0}]
        """#)

    static var offlineItems: [OfflineItem] {
        decode(#"""
        [{"id":"of0000000001","connectionId":"nc0000000001","kind":"folder","remotePath":"Musik/Projekte/Album 2026",
          "files":[],"storagePath":"/Users/mike/CloudWire/Nextcloud/Musik/Projekte/Album 2026","excludes":[],"advanced":{},
          "state":"syncing","reason":"","lastSyncAt":\#(now - 600000),"needsResync":false,"createdAt":0,
          "progress":{"bytes":734003200,"totalBytes":1288490188,"transfers":3,"eta":95},"isVault":false},
         {"id":"of0000000002","connectionId":"nc0000000001","kind":"folder","remotePath":"Musik/Samples/Drums",
          "files":[],"storagePath":"/Volumes/Studio SSD/Samples/Drums","excludes":[],"advanced":{},
          "state":"idle","reason":"","lastSyncAt":\#(now - 120000),"needsResync":false,"createdAt":0,"progress":null,"isVault":false},
         {"id":"of0000000003","connectionId":"gd0000000002","kind":"files","remotePath":"Mixes",
          "files":["Bassline.wav","Master v3.wav"],"storagePath":"/Users/mike/CloudWire/Google Drive/Mixes","excludes":[],
          "advanced":{},"state":"paused","reason":"studioMode","lastSyncAt":\#(now - 3600000),"needsResync":false,
          "createdAt":0,"progress":null,"isVault":false}]
        """#)
    }

    static let vaults: [Vault] = decode(#"""
        [{"id":"vt0000000001","connectionId":"nc0000000001","vaultPath":"Dokumente/Verträge.cwvault","name":"Verträge",
          "vaultConnectionId":"vc0000000003","unlockMode":"keychain","unlocked":true,"createdAt":0}]
        """#)

    static let pause: PauseStatus = decode(
        #"{"manualUntil":null,"activeRules":[{"id":"studioMode","detail":"REAPER"}],"effective":true}"#)

    static let capabilities: ShareCapabilities = decode(
        #"{"publicLink":true,"internalLink":true,"userShare":true,"emailShare":true,"webURL":true,"manage":true}"#)

    static let shares: [Share] = decode(#"""
        [{"id":"11","kind":"publicLink","path":"Musik/Projekte/Album 2026","itemType":"folder",
          "url":"https://cloud.example.com/s/Xk3pQ9aLm2","token":"Xk3pQ9aLm2","shareWith":"","shareWithDisplayName":"",
          "permissions":1,"expireDate":"2026-10-23","hasPassword":true,"hideDownload":false,"label":"Mastering",
          "note":"","createdAt":0},
         {"id":"12","kind":"user","path":"Musik/Projekte/Album 2026","itemType":"folder","url":"","token":"",
          "shareWith":"anna","shareWithDisplayName":"Anna Berger","permissions":15,"expireDate":null,"hasPassword":false,
          "hideDownload":false,"label":"","note":"","createdAt":0},
         {"id":"13","kind":"publicLink","path":"Musik/Mixes/Bassline.wav","itemType":"file",
          "url":"https://cloud.example.com/s/Rt7Vw2Nc8e","token":"Rt7Vw2Nc8e","shareWith":"","shareWithDisplayName":"",
          "permissions":1,"expireDate":null,"hasPassword":false,"hideDownload":true,"label":"","note":"","createdAt":0}]
        """#)

    static var activity: [ActivityEntry] {
        decode(#"""
        [{"id":3,"ts":\#(now - 60000),"level":"warn","category":"sync","subjectId":"of0000000003",
          "message":"Sync paused: Studio Mode (REAPER is running)","details":null},
         {"id":2,"ts":\#(now - 600000),"level":"info","category":"sync","subjectId":"of0000000002",
          "message":"Synced Drums: 12 transferred, 0 deleted, 0 conflicts","details":null},
         {"id":1,"ts":\#(now - 900000),"level":"info","category":"mount","subjectId":"mt0000000001",
          "message":"Mounted Nextcloud","details":null}]
        """#)
    }
}
#endif
