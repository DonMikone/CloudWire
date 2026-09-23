# Offline Items are real files synced with rclone bisync

Offline Items are plain local APFS files reconciled with `rclone bisync`, not pinned entries in a VFS cache. Only real local files give SSD speed to DAWs and survive cache eviction. The same cloud path stays visible in Mounts; conflicts keep both versions and the Mass-Delete Guard stops runs that would delete more than half.
