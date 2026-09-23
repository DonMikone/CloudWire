// Package rcl is the bridge to the embedded rclone library. Every rclone
// operation goes through its in-process rc API.
package rcl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/librclone/librclone"

	// Register every backend and the rc calls CloudWire uses.
	_ "github.com/rclone/rclone/backend/all"
	_ "github.com/rclone/rclone/cmd/bisync"
	_ "github.com/rclone/rclone/cmd/cmount"
	_ "github.com/rclone/rclone/cmd/mountlib"
	_ "github.com/rclone/rclone/cmd/nfsmount"
	_ "github.com/rclone/rclone/fs/operations"
	_ "github.com/rclone/rclone/fs/sync"
)

// Error is an rc call that returned a non-200 status.
type Error struct {
	Method  string
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Method, e.Message)
}

// IsNotFound reports whether err is an rc error with status 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

var initOnce sync.Once

// Options tune how rclone is initialised.
type Options struct {
	// JSONLog makes rclone log JSON lines (workers parse them).
	JSONLog bool
	// CacheDir is rclone's cache directory (VFS cache, bisync default workdir).
	CacheDir string
}

// Init loads the encrypted rclone config at configPath. If the file does not
// exist yet it is created, encrypted with configPass.
func Init(configPath, configPass string, opt Options) (err error) {
	initOnce.Do(func() {
		err = initialise(configPath, configPass, opt)
	})
	return err
}

func initialise(configPath, configPass string, opt Options) error {
	if err := config.SetConfigPath(configPath); err != nil {
		return fmt.Errorf("set config path: %w", err)
	}
	// Setting the key directly (instead of RCLONE_CONFIG_PASS) keeps the
	// secret out of the environment of every child process rclone spawns.
	if err := config.SetConfigPassword(configPass); err != nil {
		return fmt.Errorf("set config password: %w", err)
	}
	if opt.CacheDir != "" {
		if err := config.SetCacheDir(opt.CacheDir); err != nil {
			return fmt.Errorf("set cache dir: %w", err)
		}
	}
	ci := fs.GetConfig(context.Background())
	ci.UseJSONLog = opt.JSONLog
	librclone.Initialize()
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		config.SaveConfig()
		if _, err := os.Stat(configPath); err != nil {
			return fmt.Errorf("create rclone config: %w", err)
		}
	}
	return os.Chmod(configPath, 0o600)
}

// Call runs an rc method with in marshalled as its JSON parameters.
func Call(method string, in any) (map[string]any, error) {
	var out map[string]any
	err := CallInto(method, in, &out)
	return out, err
}

// CallInto runs an rc method and decodes the JSON result into out.
func CallInto(method string, in any, out any) error {
	input := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: marshal params: %w", method, err)
		}
		input = string(b)
	}
	output, status := librclone.RPC(method, input)
	if status != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(output), &e)
		if e.Error == "" {
			e.Error = output
		}
		return &Error{Method: method, Status: status, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(output), out); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// ForgetRemote drops cached Fs instances of a remote after its config changed
// or was deleted, so later calls see the new configuration.
func ForgetRemote(name string) {
	cache.ClearConfig(name)
}
