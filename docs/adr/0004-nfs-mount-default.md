# NFS mounts by default, FUSE optional

Mounts use `rclone nfsmount` (macOS's built-in NFS client) by default because it needs no third-party install or kernel extension. FUSE (`cmount` via FUSE-T or macFUSE) is an opt-in per Mount; libfuse headers are vendored for compilation and the library is loaded at runtime only if installed.
