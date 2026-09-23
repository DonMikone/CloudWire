package mounts

import (
	"strings"
	"testing"
)

func TestNFSHost(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, c := range []struct{ mountPoint, want string }{
		{"/Users/mike/CloudWire/Laufwerke/Mikes Nextcloud", "Mikes Nextcloud.local"},
		{"/Users/mike/CloudWire/Laufwerke/Mikes Nextcloud/", "Mikes Nextcloud.local"},
		{"/Volumes/Bücher", "Bücher.local"},
		{"/x/Projekt v1.2", "Projekt v1.2.local"},
		// Not representable exactly: reduced to letters, digits, hyphens.
		{"/x/Team: Bücher", "Team-Buecher.local"},
		{`/x/a\b`, "a-b.local"},
		{"/x/..hidden", "hidden.local"},
		{"/x/" + long, strings.Repeat("a", 63) + ".local"},
		{"/x/" + strings.Repeat("ü", 40), strings.Repeat("ue", 31) + "u.local"},
		{"/x/:::", "CloudWire.local"},
		{"/", "CloudWire.local"},
	} {
		if got := NFSHost(c.mountPoint); got != c.want {
			t.Errorf("NFSHost(%q) = %q, want %q", c.mountPoint, got, c.want)
		}
	}
}
