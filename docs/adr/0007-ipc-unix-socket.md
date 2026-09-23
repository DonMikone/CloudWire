# App–Core IPC over a JSON-RPC unix socket

The App and the Finder extension talk to the Core using newline-delimited JSON-RPC 2.0 on a 0600 unix socket in Application Support, instead of XPC. Go has no native XPC support. The sandboxed extension reaches the socket through a home-relative temporary-exception entitlement. UI actions from Finder travel via the `cloudwire://` URL scheme; the URL carries a Core-issued token, and the App asks for confirmation when the token is missing.
