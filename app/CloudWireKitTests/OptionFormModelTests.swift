import CloudWireKit
import Foundation
import Testing

/// Builds an option from rclone-shaped JSON fields.
private func option(_ fields: [String: Any]) throws -> RcloneOption {
    var object: [String: Any] = ["Help": "Help text", "Type": "string", "Default": "", "DefaultStr": ""]
    object.merge(fields) { _, new in new }
    let data = try JSONSerialization.data(withJSONObject: object)
    return try JSONDecoder().decode(RcloneOption.self, from: data)
}

@Suite("OptionFormModel")
struct OptionFormModelTests {
    @Test("provider conditions follow rclone's MatchProvider", arguments: [
        ("", "AWS", true),
        ("AWS,Ceph", "AWS", true),
        ("AWS,Ceph", "Ceph", true),
        ("AWS,Ceph", "Minio", false),
        ("!AWS,Ceph", "AWS", false),
        ("!AWS,Ceph", "Ceph", false),
        ("!AWS,Ceph", "Minio", true),
        ("AWS", "", true),
    ])
    func providerConditions(condition: String, provider: String, expected: Bool) {
        #expect(OptionFormModel.matchesProvider(condition, provider: provider) == expected)
    }

    @Test("visibility honours the selected provider, Hide bits and the advanced split")
    func visibility() throws {
        let options = [
            try option(["Name": "provider", "Examples": [["Value": "AWS"], ["Value": "Minio"]], "Exclusive": true]),
            try option(["Name": "region", "Provider": "AWS"]),
            try option(["Name": "endpoint", "Provider": "!AWS"]),
            try option(["Name": "chunk_size", "Type": "SizeSuffix", "Advanced": true]),
            try option(["Name": "config_only", "Hide": 1]),
            try option(["Name": "cmdline_only", "Hide": 2]),
        ]
        var form = OptionFormModel(options: options)
        form.setText("Minio", forName: "provider")

        #expect(form.visibleOptions(advanced: false).map(\.name) == ["provider", "endpoint", "config_only"])
        #expect(form.visibleOptions(advanced: true).map(\.name) == ["chunk_size"])

        form.setText("AWS", forName: "provider")
        #expect(form.visibleOptions(advanced: false).map(\.name) == ["provider", "region", "config_only"])

        let flags = OptionFormModel(options: options, hideContext: .commandLine)
        #expect(flags.visibleOptions().map(\.name).contains("cmdline_only"))
        #expect(!flags.visibleOptions().map(\.name).contains("config_only"))
    }

    @Test("examples are filtered by provider")
    func examplesByProvider() throws {
        let acl = try option([
            "Name": "acl",
            "Examples": [["Value": "private", "Provider": ""], ["Value": "bucket-owner-full-control", "Provider": "AWS"]],
        ])
        let provider = try option(["Name": "provider"])
        var form = OptionFormModel(options: [provider, acl])
        form.setText("Minio", forName: "provider")
        #expect(form.examples(for: acl).map(\.value) == ["private"])
        form.setText("AWS", forName: "provider")
        #expect(form.examples(for: acl).map(\.value) == ["private", "bucket-owner-full-control"])
    }

    @Test("required fields are reported until filled; hidden or foreign-provider fields are not")
    func requiredFields() throws {
        let options = [
            try option(["Name": "provider"]),
            try option(["Name": "url", "Required": true]),
            try option(["Name": "region", "Required": true, "Provider": "AWS"]),
            try option(["Name": "with_default", "Required": true, "Default": "x", "DefaultStr": "x"]),
            try option(["Name": "hidden", "Required": true, "Hide": 3]),
        ]
        var form = OptionFormModel(options: options)
        form.setText("Minio", forName: "provider")
        #expect(form.missingRequired().map(\.name) == ["url"])
        #expect(throws: OptionValidationError(optionName: "url", reason: .required)) { try form.parameters() }

        form.setText("AWS", forName: "provider")
        #expect(form.missingRequired().map(\.name) == ["url", "region"])

        form.setText("  https://dav.example.com  ", forName: "url")
        form.setText("eu-west-1", forName: "region")
        #expect(form.missingRequired().isEmpty)
        let parameters = try form.parameters()
        #expect(parameters["url"] == "https://dav.example.com")
        #expect(parameters["region"] == "eu-west-1")
        #expect(parameters["with_default"] == nil, "rclone applies defaults itself")
    }

    @Test("values are normalised and validated per type")
    func serialisationPerType() throws {
        let options = [
            try option(["Name": "flag", "Type": "bool", "Default": false, "DefaultStr": "false"]),
            try option(["Name": "on_by_default", "Type": "bool", "Default": true, "DefaultStr": "true"]),
            try option(["Name": "count", "Type": "int", "Default": 4, "DefaultStr": "4"]),
            try option(["Name": "size", "Type": "SizeSuffix", "Default": -1, "DefaultStr": "off"]),
            try option(["Name": "age", "Type": "Duration", "Default": 0, "DefaultStr": "0s"]),
            try option(["Name": "list", "Type": "CommaSepList"]),
            try option(["Name": "words", "Type": "SpaceSepList"]),
            try option(["Name": "tri", "Type": "Tristate", "DefaultStr": "unset"]),
            try option(["Name": "mode", "Type": "CacheMode", "Default": "off", "DefaultStr": "off",
                        "Examples": [["Value": "off"], ["Value": "writes"], ["Value": "full"]], "Exclusive": true]),
            try option(["Name": "secret", "IsPassword": true]),
        ]
        var form = OptionFormModel(options: options)
        #expect(try form.parameters().isEmpty)

        form.setText("yes", forName: "flag")
        form.setText("true", forName: "on_by_default")
        form.setText(" 8 ", forName: "count")
        form.setText("10 G", forName: "size")
        form.setText("1h30m", forName: "age")
        form.setText(" a, b ,,c ", forName: "list")
        form.setText("x   y\tz", forName: "words")
        form.setText("unset", forName: "tri")
        form.setText("full", forName: "mode")
        form.setText("p@ss word ", forName: "secret")

        let parameters = try form.parameters()
        #expect(parameters == [
            "flag": "true", "count": "8", "size": "10G", "age": "1h30m", "list": "a,b,c", "words": "x y z",
            "mode": "full", "secret": "p@ss word ",
        ])

        form.setText("false", forName: "tri")
        #expect(try form.parameters()["tri"] == "false")
    }

    @Test("invalid values fail with the matching reason", arguments: [
        ("int", "4.5", OptionValidationError.Reason.invalidInteger),
        ("uint32", "-1", .invalidInteger),
        ("bool", "maybe", .invalidBool),
        ("SizeSuffix", "ten gigs", .invalidSize),
        ("Duration", "5 minutes", .invalidDuration),
        ("float64", "1,5", .invalidNumber),
    ])
    func invalidValues(type: String, text: String, reason: OptionValidationError.Reason) throws {
        var form = OptionFormModel(options: [try option(["Name": "value", "Type": type])])
        form.setText(text, forName: "value")
        #expect(throws: OptionValidationError(optionName: "value", reason: reason)) { try form.parameters() }
    }

    @Test("exclusive choices reject other values; open choices accept them")
    func choices() throws {
        let exclusive = try option(["Name": "mode", "Examples": [["Value": "a"], ["Value": "b"]], "Exclusive": true])
        let open = try option(["Name": "hint", "Examples": [["Value": "a"]], "Exclusive": false])
        var form = OptionFormModel(options: [exclusive, open])
        form.setText("c", forName: "hint")
        #expect(try form.parameters() == ["hint": "c"])
        form.setText("c", forName: "mode")
        #expect(throws: OptionValidationError(optionName: "mode", reason: .notAChoice)) { try form.parameters() }
    }

    @Test("passwords are never prefilled and typed values use FieldName keys")
    func typedValues() throws {
        let options = [
            try option(["Name": "secret", "IsPassword": true, "Default": "old", "DefaultStr": "old"]),
            try option(["Name": "transfers", "FieldName": "Transfers", "Type": "int", "Default": 4, "DefaultStr": "4"]),
            try option(["Name": "size_only", "FieldName": "SizeOnly", "Type": "bool", "Default": false,
                        "DefaultStr": "false"]),
            try option(["Name": "max_age", "FieldName": "MaxAge", "Type": "Duration", "DefaultStr": "off"]),
        ]
        var form = OptionFormModel(options: options, hideContext: .commandLine,
                                   initialValues: ["transfers": "2"])
        #expect(form.text(forName: "secret") == "")
        #expect(form.text(forName: "transfers") == "2")
        form.setText("true", forName: "size_only")
        form.setText("7d", forName: "max_age")

        let typed = try form.typedValues(keyedBy: .fieldName)
        #expect(typed == ["Transfers": .int(2), "SizeOnly": .bool(true), "MaxAge": .string("7d")])

        let restored = OptionFormModel.textValues(fromTyped: typed, options: options)
        #expect(restored == ["transfers": "2", "size_only": "true", "max_age": "7d"])
    }

    @Test("rclone size spellings are accepted", arguments: ["128Mi", "16Mi", "100Ki", "10MiB", "1.5G", "20G", "1P", "0", "off"])
    func sizeSpellings(text: String) throws {
        let size = try option(["Name": "buffer_size", "Type": "SizeSuffix", "DefaultStr": "16Mi"])
        #expect(try OptionFormModel.normalize(text, for: size) == text)
    }

    @Test("an untouched flag form sends nothing, even when the Core reports non-default current values")
    func untouchedFlagForm() throws {
        let options = [
            // As returned by options/info: Value is the Core process's current global setting.
            try option(["Name": "cache_dir", "FieldName": "CacheDir", "Default": "/Users/x/Library/Caches/rclone",
                        "DefaultStr": "/Users/x/Library/Caches/rclone", "Value": "/Users/x/Library/Caches/CloudWire",
                        "ValueStr": "/Users/x/Library/Caches/CloudWire"]),
            try option(["Name": "vfs_read_chunk_size", "Type": "SizeSuffix", "Default": 134_217_728,
                        "DefaultStr": "128Mi"]),
        ]
        let form = OptionFormModel(options: options, hideContext: .commandLine)
        #expect(form.text(forName: "cache_dir") == "/Users/x/Library/Caches/rclone")
        #expect(try form.parameters().isEmpty)
        #expect(try form.typedValues().isEmpty)
    }

    @Test("editing a saved config sends changes and resets to the default, and keeps unentered secrets")
    func changedParametersFromStored() throws {
        let options = [
            try option(["Name": "region"]),
            try option(["Name": "chunk_size", "Type": "SizeSuffix", "Default": 5_242_880, "DefaultStr": "5Mi"]),
            try option(["Name": "use_accel", "Type": "bool", "Default": false, "DefaultStr": "false"]),
            try option(["Name": "endpoint"]),
            try option(["Name": "acl", "DefaultStr": "private", "Default": "private"]),
            try option(["Name": "pass", "IsPassword": true]),
        ]
        let stored = ["region": "eu-west-1", "chunk_size": "64Mi", "use_accel": "true", "endpoint": "https://s3.example",
                      "acl": "private"]
        var form = OptionFormModel(options: options, initialValues: stored)
        #expect(try form.changedParameters(from: stored).isEmpty)

        form.setText("", forName: "region")
        form.reset(options[1])
        form.setText("false", forName: "use_accel")
        form.setText("https://s3.other", forName: "endpoint")
        #expect(try form.changedParameters(from: stored)
            == ["region": "", "chunk_size": "5Mi", "use_accel": "false", "endpoint": "https://s3.other"])

        form.setText("s3cret", forName: "pass")
        #expect(try form.changedParameters(from: stored)["pass"] == "s3cret")
    }

    @Test("field labels drop the help line's full stop but keep ellipses", arguments: [
        ("User name.\nLeave blank to use the default.", "User name"),
        ("OAuth Client Id", "OAuth Client Id"),
        ("Wait for more...", "Wait for more..."),
        ("", "user"),
    ])
    func optionTitle(help: String, expected: String) throws {
        #expect(try option(["Name": "user", "Help": help]).title == expected)
    }

    @Test("provider short names drop remarks and fall back to the type name for long descriptions", arguments: [
        ("drive", "Google Drive", "Google Drive"),
        ("gcs", "Google Cloud Storage (this is not Google Drive)", "Google Cloud Storage"),
        ("s3", "Amazon S3 Compliant Storage Providers including AWS, Alibaba, Ceph", "S3"),
        ("sftp", "", "Sftp"),
    ])
    func providerShortName(name: String, description: String, expected: String) throws {
        let data = try JSONSerialization.data(withJSONObject: ["Name": name, "Description": description])
        #expect(try JSONDecoder().decode(RcloneProvider.self, from: data).shortName == expected)
    }
}
