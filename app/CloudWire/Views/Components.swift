import AppKit
import CloudWireKit
import ServiceManagement
import SwiftUI
import UniformTypeIdentifiers

// MARK: - Cards

extension EnvironmentValues {
    /// Offscreen snapshot rendering: backdrop effects (glass, materials) cannot be captured there.
    @Entry var isSnapshot = false
}

extension View {
    /// Liquid Glass on macOS 26, regular material before.
    func cardBackground(cornerRadius: CGFloat = 14) -> some View {
        modifier(CardBackground(cornerRadius: cornerRadius))
    }
}

private struct CardBackground: ViewModifier {
    let cornerRadius: CGFloat
    @Environment(\.isSnapshot) private var isSnapshot

    func body(content: Content) -> some View {
        if isSnapshot {
            content.background(Color.primary.opacity(0.07),
                               in: RoundedRectangle(cornerRadius: cornerRadius, style: .continuous))
        } else if #available(macOS 26, *) {
            content.glassEffect(.regular, in: .rect(cornerRadius: cornerRadius))
        } else {
            content.background(.regularMaterial, in: RoundedRectangle(cornerRadius: cornerRadius, style: .continuous))
        }
    }
}

struct Card<Content: View>: View {
    var minHeight: CGFloat? = nil
    @ViewBuilder var content: Content

    var body: some View {
        content
            .padding(16)
            .frame(maxWidth: .infinity, minHeight: minHeight, alignment: .topLeading)
            .cardBackground()
    }
}

struct StatusLabel: View {
    let text: String
    let color: Color
    var symbol: String? = nil

    var body: some View {
        HStack(spacing: 5) {
            if let symbol {
                Image(systemName: symbol).foregroundStyle(color)
            } else {
                Circle().fill(color).frame(width: 8, height: 8)
            }
            Text(text).foregroundStyle(.secondary)
        }
        .font(.callout)
    }
}

/// Section content with a title bar, used by every main window section.
struct SectionScaffold<Content: View, Accessory: View>: View {
    let title: String
    var subtitle: String? = nil
    @ViewBuilder var accessory: Accessory
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.title2.weight(.semibold))
                    if let subtitle {
                        Text(subtitle).font(.callout).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                accessory
            }
            .padding(.horizontal, 20)
            .padding(.top, 16)
            .padding(.bottom, 12)
            content
        }
    }
}

/// Shown instead of a section while the Core is not reachable.
struct CoreUnavailableView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        switch model.coreState {
        case .needsApproval:
            ContentUnavailableView {
                Label("Background Service Not Allowed", systemImage: "hand.raised")
            } description: {
                Text("Allow CloudWire in System Settings > General > Login Items so drives and syncing can run in the background.")
            } actions: {
                Button("Allow in Background") { SMAppService.openSystemSettingsLoginItems() }
                Button("Check Again") { model.retryStart() }
            }
        case .failed(let message):
            ContentUnavailableView {
                Label("Background Service Not Reachable", systemImage: "bolt.horizontal.circle")
            } description: {
                Text(message)
            } actions: {
                Button("Try Again") { model.retryStart() }
            }
        default:
            ProgressView("Starting CloudWire…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

// MARK: - Panels

@MainActor
enum Panels {
    /// Lets the user choose a folder; returns its path.
    static func chooseFolder(message: String, startingAt path: String? = nil, canCreate: Bool = true) -> String? {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = canCreate
        panel.allowsMultipleSelection = false
        panel.message = message
        panel.prompt = String(localized: "Choose")
        if let path {
            panel.directoryURL = URL(fileURLWithPath: CorePaths.expandTilde(path))
        }
        return panel.runModal() == .OK ? panel.url?.path : nil
    }

    static func chooseApplications() -> [String] {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.allowsMultipleSelection = true
        panel.allowedContentTypes = [.application]
        panel.directoryURL = URL(fileURLWithPath: "/Applications")
        panel.prompt = String(localized: "Add")
        return panel.runModal() == .OK ? panel.urls.map(\.path) : []
    }

    static func savePanel(name: String, message: String) -> String? {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = name
        panel.message = message
        panel.canCreateDirectories = true
        return panel.runModal() == .OK ? panel.url?.path : nil
    }
}

// MARK: - Fields

/// A path with a "Choose…" button.
struct FolderField: View {
    let title: LocalizedStringKey
    @Binding var path: String
    var placeholder: String = ""
    var message: String = ""

    var body: some View {
        LabeledContent(title) {
            HStack {
                TextField(title, text: $path, prompt: Text(placeholder))
                    .labelsHidden()
                    .textFieldStyle(.roundedBorder)
                Button("Choose…") {
                    if let chosen = Panels.chooseFolder(message: message, startingAt: path.isEmpty ? placeholder : path) {
                        path = chosen
                    }
                }
            }
        }
    }
}

/// Password entry with an optional reveal toggle.
struct PasswordField: View {
    let title: LocalizedStringKey
    @Binding var text: String
    @State private var revealed = false

    var body: some View {
        HStack {
            Group {
                if revealed {
                    TextField(title, text: $text)
                } else {
                    SecureField(title, text: $text)
                }
            }
            .textFieldStyle(.roundedBorder)
            Button {
                revealed.toggle()
            } label: {
                Image(systemName: revealed ? "eye.slash" : "eye")
            }
            .buttonStyle(.borderless)
            .help(revealed ? Text("Hide") : Text("Show"))
        }
    }
}

/// Inline error text.
struct InlineError: View {
    let message: String

    var body: some View {
        Label(message, systemImage: "exclamationmark.triangle.fill")
            .foregroundStyle(.red)
            .font(.callout)
            .fixedSize(horizontal: false, vertical: true)
    }
}

/// Sheet chrome: title, content, and a bottom button row.
struct SheetScaffold<Content: View, Buttons: View>: View {
    let title: String
    var width: CGFloat = 560
    @ViewBuilder var content: Content
    @ViewBuilder var buttons: Buttons

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(title).font(.title3.weight(.semibold))
            content
            HStack {
                Spacer()
                buttons
            }
        }
        .padding(20)
        .frame(width: width)
    }
}

extension View {
    /// Presents `model.alert`.
    func modelAlert(_ model: AppModel) -> some View {
        modifier(ModelAlert(model: model))
    }
}

private struct ModelAlert: ViewModifier {
    @Bindable var model: AppModel

    func body(content: Content) -> some View {
        content.alert(item: $model.alert) { alert in
            Alert(title: Text(alert.title), message: Text(alert.message), dismissButton: .default(Text("OK")))
        }
    }
}

/// Copies text to the general pasteboard.
@MainActor
func copyToPasteboard(_ text: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
}
