// Package vault implements Vaults: rclone crypt remotes whose random secrets
// are stored in vault.json, wrapped by the vault password and a Recovery Key
// (docs/adr/0006-vault-format.md). Changing this format breaks portability.
package vault

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// FormatVersion is the vault.json format written by this CloudWire.
const FormatVersion = 1

// AAD binds every wrapped slot to this format.
const AAD = "cloudwire-vault-v1"

// Default scrypt parameters.
const (
	ScryptN = 65536
	ScryptR = 8
	ScryptP = 1
)

// Errors.
var (
	ErrWrongPassword = errors.New("wrong password")
	ErrInvalidFormat = errors.New("invalid vault.json")
)

// File is vault.json.
type File struct {
	Format  int         `json:"format"`
	App     string      `json:"app"`
	Name    string      `json:"name"`
	Created string      `json:"created"`
	Crypt   CryptParams `json:"crypt"`
	Slots   []Slot      `json:"slots"`
}

// CryptParams are the rclone crypt options of the Vault.
type CryptParams struct {
	FilenameEncryption      string `json:"filename_encryption"`
	DirectoryNameEncryption bool   `json:"directory_name_encryption"`
	FilenameEncoding        string `json:"filename_encoding"`
}

// Slot is one wrapped copy of the secrets.
type Slot struct {
	Kind       string `json:"kind"` // password | recovery
	KDF        KDF    `json:"kdf"`
	AEAD       string `json:"aead"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// KDF describes the key derivation of a slot.
type KDF struct {
	Alg  string `json:"alg"`
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt string `json:"salt"`
}

// Secrets are the rclone crypt passwords (plain, not obscured).
type Secrets struct {
	Password  string `json:"password"`
	Password2 string `json:"password2"`
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// NewSecrets returns two random 32-byte secrets, base64url encoded.
func NewSecrets() Secrets {
	return Secrets{
		Password:  base64.RawURLEncoding.EncodeToString(randomBytes(32)),
		Password2: base64.RawURLEncoding.EncodeToString(randomBytes(32)),
	}
}

var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryKey returns 32 random bytes as grouped base32 (13 groups of 4).
func NewRecoveryKey() string {
	s := recoveryEncoding.EncodeToString(randomBytes(32))
	var groups []string
	for i := 0; i < len(s); i += 4 {
		groups = append(groups, s[i:min(i+4, len(s))])
	}
	return strings.Join(groups, "-")
}

// NormalizeRecoveryKey uppercases and removes dashes and whitespace.
func NormalizeRecoveryKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if r == '-' || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

func deriveKey(secret string, k KDF) ([]byte, error) {
	if k.Alg != "scrypt" {
		return nil, fmt.Errorf("%w: unsupported kdf %q", ErrInvalidFormat, k.Alg)
	}
	// Bound the cost so a hostile vault.json cannot exhaust memory or CPU:
	// scrypt needs 128·r·N bytes; allow at most 256 MiB (we write 64 MiB).
	if k.N < 2 || k.N&(k.N-1) != 0 || k.R < 1 || k.P < 1 || k.P > 4 || int64(k.N)*int64(k.R)*128 > 256<<20 {
		return nil, fmt.Errorf("%w: kdf parameters out of range", ErrInvalidFormat)
	}
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil || len(salt) < 16 {
		return nil, fmt.Errorf("%w: bad salt", ErrInvalidFormat)
	}
	return scrypt.Key([]byte(secret), salt, k.N, k.R, k.P, chacha20poly1305.KeySize)
}

func wrap(kind, secret string, s Secrets) (Slot, error) {
	kdf := KDF{Alg: "scrypt", N: ScryptN, R: ScryptR, P: ScryptP, Salt: base64.StdEncoding.EncodeToString(randomBytes(16))}
	key, err := deriveKey(secret, kdf)
	if err != nil {
		return Slot{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Slot{}, err
	}
	plain, err := json.Marshal(s)
	if err != nil {
		return Slot{}, err
	}
	nonce := randomBytes(chacha20poly1305.NonceSizeX)
	ct := aead.Seal(nil, nonce, plain, []byte(AAD))
	return Slot{Kind: kind, KDF: kdf, AEAD: "xchacha20poly1305", Nonce: base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct)}, nil
}

func (sl Slot) unwrap(secret string) (Secrets, error) {
	if sl.AEAD != "xchacha20poly1305" {
		return Secrets{}, fmt.Errorf("%w: unsupported aead %q", ErrInvalidFormat, sl.AEAD)
	}
	key, err := deriveKey(secret, sl.KDF)
	if err != nil {
		return Secrets{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Secrets{}, err
	}
	nonce, err1 := base64.StdEncoding.DecodeString(sl.Nonce)
	ct, err2 := base64.StdEncoding.DecodeString(sl.Ciphertext)
	if err1 != nil || err2 != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return Secrets{}, fmt.Errorf("%w: bad nonce or ciphertext", ErrInvalidFormat)
	}
	plain, err := aead.Open(nil, nonce, ct, []byte(AAD))
	if err != nil {
		return Secrets{}, ErrWrongPassword
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil || s.Password == "" || s.Password2 == "" {
		return Secrets{}, fmt.Errorf("%w: bad secrets", ErrInvalidFormat)
	}
	return s, nil
}

// New creates a vault.json with fresh secrets, returning the Recovery Key.
func New(name, password string, now time.Time) (File, Secrets, string, error) {
	s := NewSecrets()
	rk := NewRecoveryKey()
	f := File{Format: FormatVersion, App: "CloudWire", Name: name, Created: now.UTC().Format(time.RFC3339),
		Crypt: CryptParams{FilenameEncryption: "standard", DirectoryNameEncryption: true, FilenameEncoding: "base32"}}
	ps, err := wrap("password", password, s)
	if err != nil {
		return File{}, Secrets{}, "", err
	}
	rs, err := wrap("recovery", NormalizeRecoveryKey(rk), s)
	if err != nil {
		return File{}, Secrets{}, "", err
	}
	f.Slots = []Slot{ps, rs}
	return f, s, rk, nil
}

// Parse decodes and validates vault.json.
func Parse(b []byte) (File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%w: %v", ErrInvalidFormat, err)
	}
	if f.Format != FormatVersion {
		return f, fmt.Errorf("%w: unsupported format %d", ErrInvalidFormat, f.Format)
	}
	if f.Crypt.FilenameEncryption == "" || f.Crypt.FilenameEncoding == "" {
		return f, fmt.Errorf("%w: missing crypt parameters", ErrInvalidFormat)
	}
	return f, nil
}

// Marshal encodes vault.json (indented for humans).
func (f File) Marshal() ([]byte, error) {
	return json.MarshalIndent(f, "", "  ")
}

func (f File) slot(kind string) (int, bool) {
	for i, s := range f.Slots {
		if s.Kind == kind {
			return i, true
		}
	}
	return -1, false
}

// Unlock unwraps the secrets with the vault password.
func (f File) Unlock(password string) (Secrets, error) {
	i, ok := f.slot("password")
	if !ok {
		return Secrets{}, fmt.Errorf("%w: no password slot", ErrInvalidFormat)
	}
	return f.Slots[i].unwrap(password)
}

// UnlockRecovery unwraps the secrets with the Recovery Key.
func (f File) UnlockRecovery(key string) (Secrets, error) {
	i, ok := f.slot("recovery")
	if !ok {
		return Secrets{}, fmt.Errorf("%w: no recovery slot", ErrInvalidFormat)
	}
	return f.Slots[i].unwrap(NormalizeRecoveryKey(key))
}

// SetPassword re-wraps the password slot; the data keys stay the same.
func (f *File) SetPassword(password string, s Secrets) error {
	sl, err := wrap("password", password, s)
	if err != nil {
		return err
	}
	if i, ok := f.slot("password"); ok {
		f.Slots[i] = sl
	} else {
		f.Slots = append([]Slot{sl}, f.Slots...)
	}
	return nil
}
