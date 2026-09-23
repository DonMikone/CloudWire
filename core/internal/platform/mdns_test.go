//go:build darwin

package platform

import (
	"fmt"
	"net"
	"os"
	"slices"
	"testing"
)

func TestRegisterLoopbackHost(t *testing.T) {
	// Spaces are kept: Finder shows the host exactly as registered.
	name := fmt.Sprintf("CloudWire Test %d.local", os.Getpid())
	release, err := RegisterLoopbackHost(name)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	addrs, err := net.DefaultResolver.LookupHost(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(addrs, "127.0.0.1") {
		t.Fatalf("%s resolves to %v, want 127.0.0.1", name, addrs)
	}
}

func TestRegisterLoopbackHostRejectsNonLocal(t *testing.T) {
	if _, err := RegisterLoopbackHost("cloudwire-test.example"); err == nil {
		t.Fatal("registered a name outside .local")
	}
}
