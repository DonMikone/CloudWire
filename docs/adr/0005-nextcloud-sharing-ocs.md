# Nextcloud sharing through the OCS API

rclone cannot create links on WebDAV remotes, so the Core talks to Nextcloud's OCS Share API directly for all share kinds. Other providers use rclone's PublicLink; because rclone cannot list existing links, CloudWire keeps a local registry of links it created.
