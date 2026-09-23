package offline

import "os"

func mkdir(p string) error     { return os.MkdirAll(p, 0o755) }
func removeAll(p string) error { return os.RemoveAll(p) }
