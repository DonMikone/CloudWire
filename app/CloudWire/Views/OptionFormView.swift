import CloudWireKit
import SwiftUI

/// Renders rclone options as form fields bound to an `OptionFormModel`.
struct OptionFormView: View {
    @Binding var form: OptionFormModel
    let options: [RcloneOption]
    var showsNames = false

    var body: some View {
        ForEach(options) { option in
            OptionFieldRow(form: $form, option: option, showsName: showsNames)
        }
    }
}

struct OptionFieldRow: View {
    @Binding var form: OptionFormModel
    let option: RcloneOption
    var showsName = false

    private var text: Binding<String> {
        Binding(get: { form.text(for: option) }, set: { form.setText($0, for: option) })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            field
            if !option.details.isEmpty || showsName {
                Text(showsName ? "\(option.name) · \(option.details)" : option.details)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
                    .textSelection(.enabled)
            }
            if let error = form.validationError(for: option), !(error.reason == .required && text.wrappedValue.isEmpty) {
                InlineError(message: ErrorText.message(for: error))
            }
        }
        .padding(.vertical, 2)
    }

    private var label: some View {
        HStack(spacing: 2) {
            Text(option.title)
            if option.required { Text("*").foregroundStyle(.red) }
        }
    }

    @ViewBuilder
    private var field: some View {
        switch OptionFormModel.kind(for: option) {
        case .bool:
            Toggle(isOn: Binding(get: { text.wrappedValue.lowercased() == "true" },
                                 set: { text.wrappedValue = $0 ? "true" : "false" })) { label }
        case .tristate:
            Picker(selection: Binding(get: {
                let value = text.wrappedValue.lowercased()
                return value == "true" || value == "false" ? value : ""
            }, set: { text.wrappedValue = $0 })) {
                Text("Default").tag("")
                Text("On").tag("true")
                Text("Off").tag("false")
            } label: { label }
        case .choice(let exclusive):
            let examples = form.examples(for: option)
            if exclusive {
                Picker(selection: text) {
                    if !examples.contains(where: { $0.value == text.wrappedValue }) {
                        Text(text.wrappedValue.isEmpty ? String(localized: "Not set") : text.wrappedValue)
                            .tag(text.wrappedValue)
                    }
                    ForEach(examples) { example in
                        Text(choiceTitle(example)).tag(example.value)
                    }
                } label: { label }
            } else {
                LabeledContent {
                    HStack(spacing: 4) {
                        TextField("", text: text).textFieldStyle(.roundedBorder)
                        Menu {
                            ForEach(examples) { example in
                                Button(choiceTitle(example)) { text.wrappedValue = example.value }
                            }
                        } label: {
                            Image(systemName: "chevron.down")
                        }
                        .menuStyle(.borderlessButton)
                        .fixedSize()
                    }
                } label: { label }
            }
        case .password:
            LabeledContent {
                SecureField("", text: text).textFieldStyle(.roundedBorder)
            } label: { label }
        default:
            LabeledContent {
                TextField("", text: text, prompt: Text(placeholder))
                    .textFieldStyle(.roundedBorder)
            } label: { label }
        }
    }

    private var placeholder: String {
        switch OptionFormModel.kind(for: option) {
        case .sizeSuffix: return "100M, 20G, off"
        case .duration: return "30s, 5m, 1h30m"
        case .commaList: return "a,b,c"
        default: return option.defaultString
        }
    }

    private func choiceTitle(_ example: RcloneExample) -> String {
        let help = example.help.split(separator: "\n").first.map(String.init) ?? ""
        if help.isEmpty || help == example.value { return example.value.isEmpty ? String(localized: "Not set") : example.value }
        return example.value.isEmpty ? help : "\(example.value) – \(help)"
    }
}

/// "Einfach / Erweitert" switch.
enum FormDetail: String, CaseIterable, Identifiable {
    case simple, advanced
    var id: String { rawValue }
    var title: String {
        switch self {
        case .simple: return String(localized: "Simple")
        case .advanced: return String(localized: "Advanced")
        }
    }
}

struct FormDetailPicker: View {
    @Binding var selection: FormDetail

    var body: some View {
        Picker("", selection: $selection) {
            ForEach(FormDetail.allCases) { Text($0.title).tag($0) }
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        .fixedSize()
    }
}
