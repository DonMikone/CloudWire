package vault

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWrapUnwrapRoundTrip(t *testing.T) {
	f, s, rk, err := New("Geheim", "correct horse battery", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := f.Marshal()
	parsed, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsed.Unlock("correct horse battery")
	if err != nil || got != s {
		t.Fatalf("unlock: %v %+v", err, got)
	}
	if len(parsed.Slots) != 2 || parsed.Slots[0].KDF.N != 65536 || parsed.Slots[0].AEAD != "xchacha20poly1305" {
		t.Fatalf("slot parameters: %+v", parsed.Slots[0])
	}
	if strings.Contains(string(b), s.Password) || strings.Contains(string(b), rk) {
		t.Fatal("secrets or recovery key leaked into vault.json")
	}
	// Recovery slot, with lowercase and spaces as a user might type it.
	typed := strings.ToLower(strings.ReplaceAll(rk, "-", " "))
	rs, err := parsed.UnlockRecovery(typed)
	if err != nil || rs != s {
		t.Fatalf("recovery unlock: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	f, _, rk, _ := New("x", "password-one", time.Now())
	if _, err := f.Unlock("password-two"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("got %v", err)
	}
	if _, err := f.UnlockRecovery(rk + "A"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("recovery with wrong key: %v", err)
	}
	// Tampering with the ciphertext is detected by the AEAD.
	ct := []byte(f.Slots[0].Ciphertext)
	ct[5] ^= 1
	f.Slots[0].Ciphertext = string(ct)
	if _, err := f.Unlock("password-one"); err == nil {
		t.Fatal("tampered slot accepted")
	}
}

func TestPasswordChangeKeepsSecrets(t *testing.T) {
	f, s, rk, _ := New("x", "old-password", time.Now())
	if err := f.SetPassword("new-password", s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Unlock("old-password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatal("old password still works")
	}
	got, err := f.Unlock("new-password")
	if err != nil || got != s {
		t.Fatal("new password must unwrap the same secrets")
	}
	if got, err := f.UnlockRecovery(rk); err != nil || got != s {
		t.Fatal("recovery slot must survive a password change")
	}
}

func TestRecoveryKeyFormat(t *testing.T) {
	re := regexp.MustCompile(`^([A-Z2-7]{4}-){12}[A-Z2-7]{4}$`)
	for range 20 {
		if k := NewRecoveryKey(); !re.MatchString(k) {
			t.Fatalf("bad recovery key %q", k)
		}
	}
}

func TestHostileKDFRejected(t *testing.T) {
	f, _, _, _ := New("x", "password-one", time.Now())
	for _, k := range []KDF{{N: 1 << 20, R: 32, P: 1}, {N: 1 << 30, R: 1, P: 1}, {N: 65536, R: 8, P: 16}} {
		g := f
		g.Slots = append([]Slot{}, f.Slots...)
		k.Alg, k.Salt = "scrypt", f.Slots[0].KDF.Salt
		g.Slots[0].KDF = k
		if _, err := g.Unlock("password-one"); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("expensive kdf %+v accepted: %v", k, err)
		}
	}
	if _, err := Parse([]byte(`{"format":2}`)); !errors.Is(err, ErrInvalidFormat) {
		t.Fatal("unknown format accepted")
	}
}
