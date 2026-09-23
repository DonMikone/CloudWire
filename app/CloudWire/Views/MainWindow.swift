import CloudWireKit
import SwiftUI

struct MainWindow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings
    @State private var suppressed = false

    var body: some View {
        MainWindowContent()
            .frame(minWidth: 900, minHeight: 600)
            .background(WindowAccessor { window in
                suppressed = !WindowRouter.shared.mainWindowAttached(window)
                if !suppressed {
                    WindowRouter.shared.windowAppeared(WindowRouter.mainID)
                }
            })
            .onAppear {
                WindowRouter.shared.capture(openWindow: openWindow, openSettings: openSettings)
            }
            .onDisappear {
                if suppressed {
                    suppressed = false
                } else {
                    WindowRouter.shared.windowDisappeared(WindowRouter.mainID)
                }
            }
    }
}

/// The split view itself (also rendered offscreen for snapshots).
struct MainWindowContent: View {
    @Environment(AppModel.self) private var model
    @Environment(\.isSnapshot) private var isSnapshot

    var body: some View {
        @Bindable var model = model
        Group {
            if isSnapshot {
                // The split view's sidebar lives in a backdrop layer that offscreen rendering cannot
                // capture; snapshots draw the same content side by side.
                HStack(spacing: 0) {
                    snapshotSidebar.frame(width: 210)
                    Divider()
                    detailColumn
                }
            } else {
                NavigationSplitView {
                    List(SidebarSection.allCases, selection: Binding($model.selection)) { section in
                        Label(section.title, systemImage: section.symbol)
                            .badge(badge(for: section))
                            .tag(section)
                    }
                    .navigationSplitViewColumnWidth(min: 190, ideal: 210, max: 260)
                } detail: {
                    detailColumn
                }
            }
        }
        .modelAlert(model)
    }

    private var detailColumn: some View {
        Group {
            if model.isConnected {
                detail(for: model.selection)
            } else {
                CoreUnavailableView()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    @ViewBuilder
    private func detail(for section: SidebarSection) -> some View {
        switch section {
        case .overview: OverviewView()
        case .connections: ConnectionsView()
        case .mounts: MountsView()
        case .offline: OfflineView()
        case .shares: SharesView()
        case .vaults: VaultsView()
        case .activity: ActivityView()
        }
    }

    /// The sidebar list and its material cannot be captured offscreen; snapshots draw a static copy.
    private var snapshotSidebar: some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(SidebarSection.allCases) { section in
                Label(section.title, systemImage: section.symbol)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(section == model.selection ? Color.accentColor.opacity(0.25) : .clear,
                                in: RoundedRectangle(cornerRadius: 6))
            }
            Spacer()
        }
        .padding(.horizontal, 10)
        .padding(.top, 20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Color(nsColor: .underPageBackgroundColor))
    }

    private func badge(for section: SidebarSection) -> Int {
        switch section {
        case .offline:
            return model.offlineItems.filter { $0.state == .error || $0.state == .needsConfirmation }.count
        case .mounts:
            return model.mounts.filter { $0.state == .error }.count
        default:
            return 0
        }
    }
}

extension Binding where Value == SidebarSection? {
    /// Adapts a non-optional selection to `List(selection:)`.
    init(_ source: Binding<SidebarSection>) {
        self.init(get: { source.wrappedValue }, set: { if let value = $0 { source.wrappedValue = value } })
    }
}

