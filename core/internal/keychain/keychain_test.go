//go:build darwin && keychaintest

package keychain

import (
	"errors"
	"testing"
)

// Run with: go test -tags keychaintest ./internal/keychain (touches the login Keychain).
func TestRoundTrip(t *testing.T) {
	const svc, acc = "io.github.donmikone.cloudwire.test", "roundtrip"
	_ = Delete(svc, acc)
	if _, err := Get(svc, acc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := Set(svc, acc, "s3cret\x00with-nul"); err != nil {
		t.Fatal(err)
	}
	if err := Set(svc, acc, "replaced"); err != nil {
		t.Fatal(err)
	}
	got, err := Get(svc, acc)
	if err != nil || got != "replaced" {
		t.Fatalf("got %q %v", got, err)
	}
	if err := Delete(svc, acc); err != nil {
		t.Fatal(err)
	}
	if err := Delete(svc, acc); err != nil {
		t.Fatal("deleting a missing item must succeed")
	}
}
