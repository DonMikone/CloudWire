# Vendored libfuse headers

These header files are copied unmodified from the `include/` directory of
[macos-fuse-t/libfuse](https://github.com/macos-fuse-t/libfuse) at commit
`4ddd212781edad1620bb4f46760abc324aea71f4`.

They are licensed under the GNU Lesser General Public License, version 2.1
(see `COPYING.LIB`). CloudWire only uses them to compile rclone's `cmount`
backend (through cgofuse). No libfuse code is linked into CloudWire: the FUSE
library (FUSE-T or macFUSE) is loaded at runtime with `dlopen` and only if the
user has installed it.
