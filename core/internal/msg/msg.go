// Package msg is the contract for user-facing Core texts.
//
// Every sentence the App shows travels as a Text: a stable Code naming one
// template below, string Params filling it, and Message, the English
// rendering. The App translates Code+Params (app/CloudWireKit/CoreText.swift,
// strings in app/CloudWire/Localizable.xcstrings) and shows Message for codes
// it does not know and for Activity entries written before codes existed.
// Message stays English: stderr logs, the Activity export and search use it.
//
// Params are strings. Numbers are decimal; names ending in "Bytes" are byte
// counts and names ending in "Ms" are Unix milliseconds, both formatted by
// the renderer. Two names are reserved:
//   - "detail" is raw rclone or OS text. It is never translated: Message
//     appends it after ": ", the App shows it as secondary text.
//   - "cause" is the code of a nested Text whose params are merged in (see
//     Because); both sentences are joined with ": ".
//
// Where a payload already had a text field, code and params sit beside it:
// Activity entries and Pause Rules {message, code, params}; error
// notifications {title, message, code, params}; Mount {error, errorCode,
// errorParams}; Offline Item {reason, reasonCode, reasonParams} (errors only;
// pause reasons stay ids); ConfigStep and the connection.nextcloudLogin event
// {error, errorCode, errorParams}; API errors data {code, message, key,
// params} where code stays the error category for the App's headline and
// logic and key is the Text code of the detail sentence.
package msg

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Params fill a template's {name} placeholders.
type Params map[string]string

// Text is one user-facing sentence.
type Text struct {
	Code    string `json:"code,omitempty"`
	Params  Params `json:"params,omitempty"`
	Message string `json:"message"`
}

// CodeDetail is a text that consists of its raw "detail" only.
const CodeDetail = "detail"

// templates are the English sentences; the App's CoreText mirrors every code.
var templates = map[string]string{
	CodeDetail:     "",
	"rclone.error": "rclone reported an error",

	"path.createFailed": "Cannot create {path}",
	"path.openFailed":   "Cannot open {path}",
	"path.readFailed":   "Cannot read {path}",
	"path.writeFailed":  "Cannot write {path}",
	"folder.notEmpty":   "The folder {path} is not empty",

	"core.started":        "CloudWire Core {version} started",
	"core.stopped":        "CloudWire Core stopped",
	"core.firstStart":     `First start: conflict label "{label}", {count} Studio Mode app(s) detected`,
	"core.networkOnline":  "Network changed (online)",
	"core.networkOffline": "Network changed (offline)",
	"core.sleeping":       "System will sleep",
	"core.woke":           "System woke up",

	"connection.notFound":          "Connection {id} not found",
	"connection.setupNotFound":     "No connection setup in progress for {id}",
	"connection.pathNotFound":      "{path} is not inside a Mount or Offline Item",
	"connection.nameNotFolder":     `The name "{name}" cannot be used as a folder name`,
	"connection.nameMountFolder":   `The name "{name}" is reserved for the Mount folder`,
	"connection.nameTaken":         `A connection named "{name}" already exists`,
	"connection.namePending":       `A connection named "{name}" is being set up`,
	"connection.invalidServerURL":  `"{url}" is not a valid server address`,
	"connection.noLoginFlow":       "{url} offers no Nextcloud login. Is this the address of a Nextcloud server?",
	"connection.signInFailed":      "Sign-in failed",
	"connection.otherAccount":      `Signed in as {user}, but "{name}" belongs to another account`,
	"connection.signInTimedOut":    "The sign-in timed out",
	"connection.setupCancelled":    "The connection setup was cancelled",
	"connection.loginTimedOut":     "The login timed out",
	"connection.loginCancelled":    "The login was cancelled",
	"connection.removeVault":       "Remove the Vault instead",
	"connection.inUse":             `"{name}" is still used by {count} item(s)`,
	"connection.added":             `Connection "{name}" added ({provider})`,
	"connection.updated":           `Connection "{name}" updated`,
	"connection.removed":           `Connection "{name}" removed`,
	"connection.nextcloudAdded":    `Nextcloud connection "{name}" added ({server} as {user})`,
	"connection.nextcloudSignedIn": `Nextcloud connection "{name}" signed in again as {user}`,

	"mount.notFound":             "Mount {id} not found",
	"mount.pointInUse":           "Another Mount already uses {path}",
	"mount.pointOverlapsOffline": "{path} overlaps the Offline Item stored at {storagePath}",
	"mount.pointIsVolume":        "{path} is already a mounted volume",
	"mount.fuseUnavailable":      "FUSE requires FUSE-T or macFUSE to be installed",
	"mount.inactive":             "Mount is not active",
	"mount.pointBusy":            "{path} is used by another volume",
	"mount.stillAttached":        "The previous mount is still attached",
	"mount.workerExited":         "The mount worker exited",
	"mount.created":              `Mount "{name}" created at {path}`,
	"mount.updated":              `Mount "{name}" updated`,
	"mount.removed":              `Mount "{name}" removed`,
	"mount.unmounted":            `Mount "{name}" unmounted`,
	"mount.unmountFailed":        `Could not unmount "{name}"`,
	"mount.unmountProblem":       `Unmounting "{name}" reported a problem`,
	"mount.mounted":              `Mounted "{name}" at {path}`,
	"mount.reconnected":          `Reconnected "{name}" at {path}`,
	"mount.servedFromLocalhost":  `"{name}" is mounted from localhost`,
	"mount.ejected":              `Mount "{name}" was ejected in Finder`,
	"mount.stoppedUnexpectedly":  `Mount "{name}" stopped unexpectedly`,
	"mount.failedRepeatedly":     `Mount "{name}" failed repeatedly`,
	"mount.notResponding":        `Mount "{name}" is not responding; restarting it`,

	"offline.notFound":             "Offline Item {id} not found",
	"offline.insufficientSpace":    "Not enough free space: {neededBytes} needed, {freeBytes} available",
	"offline.moveFailed":           "Moving the files failed",
	"offline.trashFailed":          "Moving the local copy to the Trash failed",
	"offline.vaultFolder":          `"{folder}" holds encrypted Vault data. Unlock the Vault and make it available offline under Vaults.`,
	"offline.storageTooBroad":      "The storage location {path} is too broad",
	"offline.storageNotAllowed":    "The storage location {path} is not allowed",
	"offline.storageOverlapsMount": "The storage location {path} overlaps the Mount at {mountPoint}",
	"offline.overlapsItem":         `This overlaps the Offline Item "{name}" ({path})`,
	"offline.storageOverlapsItem":  `The storage location overlaps the Offline Item "{name}" ({path})`,
	"offline.syncFailed":           "Syncing failed",
	"offline.cloudFolderMissing":   "The cloud folder was not found. Restore it in the cloud or remove the Offline Item.",
	"offline.pausedByRule":         "Syncing paused",
	"offline.rulesInactive":        "Pause Rules no longer active; syncing resumes",
	"offline.pausedManually":       "Syncing paused until resumed",
	"offline.pausedUntil":          "Syncing paused until {untilMs}",
	"offline.resumed":              "Syncing resumed",
	"offline.added":                `Offline Item "{name}" added at {path}`,
	"offline.filesAdded":           `Added {count} item(s) to Offline Item "{name}"`,
	"offline.selectionChanged":     `Selection of "{name}" changed`,
	"offline.settingsChanged":      `Settings of "{name}" changed`,
	"offline.movedWithLeftovers":   `Moved "{name}", but some files could not be removed from {path}`,
	"offline.moved":                `Moved "{name}" to {path}`,
	"offline.removedTrash":         `Offline Item "{name}" removed; local copy moved to the Trash`,
	"offline.removedKept":          `Offline Item "{name}" removed; local copy kept`,
	"offline.deletionsConfirmed":   `Deletions in "{name}" confirmed`,
	"offline.deletionsRestored":    `Deleted files of "{name}" will be restored`,

	"sync.done":       `Synced "{name}": {transferred} transferred, {deleted} deleted, {conflicts} conflicts`,
	"sync.noChanges":  `Synced "{name}": no changes`,
	"sync.conflicts":  `{count} conflict copies created in "{name}"`,
	"sync.massDelete": `Mass-Delete Guard stopped "{name}"`,
	"sync.failed":     `Sync of "{name}" failed`,

	"pause.studioMode":         "Studio Mode ({app})",
	"pause.battery":            "Battery power",
	"pause.lowPowerMode":       "Low Power Mode",
	"pause.expensiveNetwork":   "Personal hotspot",
	"pause.constrainedNetwork": "Low Data Mode",
	"pause.cpu":                "High CPU load ({percent}%)",

	"share.vaultUnsupported":        "Sharing is not available inside a Vault because its files are encrypted",
	"share.appPasswordUnreadable":   "Cannot read the app password",
	"share.publicLinksOnly":         "This provider only supports public links",
	"share.linkNotEditable":         "Links of this provider cannot be edited; delete and recreate it",
	"share.notFound":                "Share {id} not found",
	"share.peopleUnsupported":       "Sharing with people is only available for Nextcloud and ownCloud",
	"share.internalLinkUnsupported": "Internal links are only available for Nextcloud and ownCloud",
	"share.browserUnsupported":      "Opening in the browser is only available for Nextcloud and ownCloud",
	"share.passwordRequired":        "The server requires a password for public links",
	"share.expiryInPast":            "The expiry date must be in the future",
	"share.createdUser":             `Created a user share for "{path}"`,
	"share.createdGroup":            `Created a group share for "{path}"`,
	"share.createdEmail":            `Created an email share for "{path}"`,
	"share.linkCreated":             `Created public link for "{path}"`,
	"share.updated":                 "Updated share {id}",
	"share.deleted":                 "Deleted share {id}",
	"share.linkRemovedLocally":      `Removed public link for "{path}" from CloudWire; it stays active at the provider`,
	"share.linkDeleted":             `Deleted public link for "{path}"`,

	"vault.notFound":            "Vault {id} not found",
	"vault.migrationNotFound":   "Migration {id} not found",
	"vault.noVaultFile":         "{path} has no vault.json",
	"vault.readFailed":          "Cannot read {path}/vault.json",
	"vault.exists":              "{path} already exists",
	"vault.createFolderFailed":  "Cannot create the Vault folder",
	"vault.writeFailed":         "Cannot write vault.json",
	"vault.alreadyAdded":        "This Vault is already added",
	"vault.configureFailed":     "Cannot configure the Vault",
	"vault.wrongPassword":       "The password is not correct",
	"vault.wrongRecoveryKey":    "The Recovery Key is not correct",
	"vault.passwordRequired":    "The Vault password is required",
	"vault.unlockFirst":         "Unlock the Vault first",
	"vault.inUse":               `The Vault "{name}" is still used by {count} item(s)`,
	"vault.sourceIsOffline":     "Remove the Offline Item {path} first; its local copy would be re-uploaded",
	"vault.deleteFailed":        "Deleting {count} original file(s) failed: {files}",
	"vault.invalidName":         `"{name}" cannot be used as a Vault name`,
	"vault.passwordTooShort":    "The password needs at least {count} characters",
	"vault.cloudRoot":           "The cloud root cannot be encrypted as a whole",
	"vault.sourceOverlapsVault": `The source overlaps the Vault "{name}"`,
	"vault.created":             `Vault "{name}" created at {path}`,
	"vault.keychainFailed":      "Could not store the Vault password in the Keychain",
	"vault.opened":              `Vault "{name}" opened`,
	"vault.unlocked":            `Vault "{name}" unlocked`,
	"vault.locked":              `Vault "{name}" locked`,
	"vault.passwordChanged":     `Password of Vault "{name}" changed`,
	"vault.recovered":           `Vault "{name}" reset with its Recovery Key`,
	"vault.rcloneExported":      `Emergency rclone configuration of "{name}" exported`,
	"vault.removed":             `Vault "{name}" removed from this Mac (cloud data unchanged)`,
	"vault.encryptStarted":      `Encrypting "{path}" into Vault "{name}"`,
	"vault.encryptVerified":     `Encrypted copy in "{name}" verified; waiting for confirmation to delete the original`,
	"vault.encryptMismatch":     `Verification of the encrypted copy in "{name}" found {count} mismatches; the original is kept`,
	"vault.encryptFailed":       `Encrypting into "{name}" failed`,
	"vault.encryptCanceled":     `Encrypting "{path}" into Vault "{name}" canceled`,
	"vault.originalDeleted":     `Original "{path}" deleted after encryption`,
}

// New builds the Text of code from name/value pairs (values are formatted
// with fmt.Sprint; errors contribute their message). An empty "detail" is
// dropped. Unknown codes and unfilled placeholders panic under go test.
func New(code string, kv ...any) Text {
	p := Params{}
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		v := value(kv[i+1])
		if k == "detail" && v == "" {
			continue
		}
		p[k] = v
	}
	if len(kv)%2 != 0 {
		fail("msg.New(%q): odd number of arguments", code)
	}
	tmpl, ok := templates[code]
	if !ok {
		fail("msg.New: unknown code %q", code)
		tmpl = code
	}
	t := Text{Code: code, Message: join(render(code, tmpl, p), p["detail"])}
	if len(p) > 0 {
		t.Params = p
	}
	return t
}

// Detail wraps raw rclone or OS text that has no sentence of its own.
func Detail(detail string) Text {
	return New(CodeDetail, "detail", detail)
}

// Because returns t with cause as its reason. A raw cause becomes t's
// "detail"; any other cause is named by "cause" and its params are merged
// in (t's own params win on a name clash, so causes must use other names).
func (t Text) Because(cause Text) Text {
	if cause.Code == "" && cause.Message == "" {
		return t
	}
	p := Params{}
	maps.Copy(p, t.Params)
	if cause.Code == CodeDetail || cause.Code == "" {
		if cause.Message == "" {
			return t
		}
		p["detail"] = cause.Message
	} else {
		for k, v := range cause.Params {
			if _, taken := p[k]; !taken {
				p[k] = v
			}
		}
		p["cause"] = cause.Code
	}
	return Text{Code: t.Code, Params: p, Message: join(t.Message, cause.Message)}
}

// Detail returns the raw detail param ("" when there is none).
func (t Text) Detail() string { return t.Params["detail"] }

func value(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case error:
		return v.Error()
	default:
		return fmt.Sprint(v)
	}
}

// render fills {name} placeholders; *Bytes and *Ms params are formatted.
func render(code, tmpl string, p Params) string {
	var b strings.Builder
	for {
		start := strings.IndexByte(tmpl, '{')
		if start < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		end := strings.IndexByte(tmpl[start:], '}')
		if end < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		name := tmpl[start+1 : start+end]
		b.WriteString(tmpl[:start])
		if v, ok := p[name]; ok {
			b.WriteString(format(name, v))
		} else {
			fail("msg.New(%q): param %q missing", code, name)
			b.WriteString(tmpl[start : start+end+1])
		}
		tmpl = tmpl[start+end+1:]
	}
}

func format(name, v string) string {
	switch {
	case strings.HasSuffix(name, "Bytes"):
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return humanBytes(n)
		}
	case strings.HasSuffix(name, "Ms"):
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.UnixMilli(n).Format("2006-01-02 15:04")
		}
	}
	return v
}

// join appends b to sentence a: after ": ", or after a space when a already
// ends a sentence.
func join(a, b string) string {
	switch {
	case b == "":
		return a
	case a == "":
		return b
	case strings.HasSuffix(a, "."):
		return a + " " + b
	}
	return a + ": " + b
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// fail reports a programming error: fatal in tests, ignored in production so
// a user never loses an operation over a wording mistake.
func fail(format string, args ...any) {
	if testing.Testing() {
		panic(fmt.Sprintf(format, args...))
	}
}
