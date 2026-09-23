# NFS Mounts use a local mDNS host name instead of localhost

Finder lists network volumes under their server's host name, so NFS Mounts attached as `localhost:/` all appeared as "localhost". Each NFS Mount is now attached via a host named after its mount point folder (`<Folder>.local`), which the worker registers with mDNSResponder as a local-only A record pointing to 127.0.0.1. Mounts whose folders have the same name share one record and one Finder entry.

Considered: an `/etc/hosts` entry would allow names without `.local`, but needs an admin prompt for every new Mount and edits a system file; mDNSResponder rejects local-only records outside `.local`. The kernel stores the resolved address at mount time; the record is still held for the whole life of every worker, including one that takes over an existing mount. If registration fails, the Mount falls back to `localhost`, and `localhost:` mounts stay recognized as CloudWire's own.
