package rcl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cw-rcl")
	if err != nil {
		panic(err)
	}
	if err := Init(filepath.Join(dir, "rclone.conf"), "test-config-pass", Options{CacheDir: filepath.Join(dir, "cache")}); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestRCMethodsRegistered(t *testing.T) {
	out, err := Call("rc/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, c := range out["commands"].([]any) {
		have = append(have, c.(map[string]any)["Path"].(string))
	}
	for _, want := range []string{"sync/bisync", "mount/mount", "mount/unmount", "operations/publiclink", "config/create",
		"options/info", "core/bwlimit", "vfs/stats", "vfs/forget", "job/stop", "job/status", "operations/copyfile",
		"operations/size", "operations/check", "sync/copy", "operations/purge", "serve/start", "serve/stop"} {
		if !slices.Contains(have, want) {
			t.Errorf("rc method %s not registered", want)
		}
	}
}

func TestMountTypes(t *testing.T) {
	out, err := Call("mount/types", nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, v := range out["mountTypes"].([]any) {
		types = append(types, v.(string))
	}
	for _, want := range []string{"nfsmount", "cmount"} {
		if !slices.Contains(types, want) {
			t.Errorf("mount type %s missing from %v (build with -tags cmount)", want, types)
		}
	}
}

func TestConfigFileIsEncrypted(t *testing.T) {
	if _, err := Call("config/create", map[string]any{"name": "enc-check", "type": "local", "parameters": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	out, _ := Call("config/paths", nil)
	b, err := os.ReadFile(out["config"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "# Encrypted rclone configuration File") && !strings.Contains(string(b), "RCLONE_ENCRYPT_V0:") {
		t.Fatalf("config not encrypted: %q", b)
	}
	if strings.Contains(string(b), "enc-check") {
		t.Fatal("remote name visible in plaintext")
	}
}

func TestErrorMapping(t *testing.T) {
	_, err := Call("operations/list", map[string]any{"fs": "no-such-remote:", "remote": ""})
	var e *Error
	if err == nil || !errorsAs(err, &e) || e.Status == 200 || e.Message == "" {
		t.Fatalf("expected rc error with message, got %#v", err)
	}
	if _, err := Call("no/such/method", nil); !IsNotFound(err) {
		t.Fatalf("unknown method must map to 404, got %v", err)
	}
}

func errorsAs(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
