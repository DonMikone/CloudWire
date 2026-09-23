// Package buildinfo carries version information set at link time.
package buildinfo

// Version is the CloudWire version, set with -ldflags "-X …/buildinfo.Version=…".
var Version = "0.0.0-dev"

// APIVersion is the version of the App–Core JSON-RPC contract.
const APIVersion = 1
