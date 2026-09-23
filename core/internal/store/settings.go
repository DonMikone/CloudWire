package store

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Settings mirrors plan Appendix E. Stored one row per top-level key.
type Settings struct {
	Autostart             bool                 `json:"autostart"`
	MenuBarIcon           bool                 `json:"menuBarIcon"`
	BaseFolder            string               `json:"baseFolder"`
	MountFolder           string               `json:"mountFolder"`
	QuietPeriodSeconds    int                  `json:"quietPeriodSeconds"`
	PollIntervalSeconds   int                  `json:"pollIntervalSeconds"`
	NextcloudEtagSeconds  int                  `json:"nextcloudEtagSeconds"`
	GenericCheckSeconds   int                  `json:"genericCheckSeconds"`
	SafetyFullSyncMinutes int                  `json:"safetyFullSyncMinutes"`
	PauseRules            PauseRulesSettings   `json:"pauseRules"`
	Bandwidth             BandwidthSettings    `json:"bandwidth"`
	DefaultCacheMaxGB     int                  `json:"defaultCacheMaxGB"`
	DefaultMountType      string               `json:"defaultMountType"`
	DefaultExcludes       []string             `json:"defaultExcludes"`
	Notifications         NotificationSettings `json:"notifications"`
	Log                   LogSettings          `json:"log"`
	Updates               UpdateSettings       `json:"updates"`
	ConflictLabel         string               `json:"conflictLabel"`
}

// PauseRulesSettings configures the Pause Rules.
type PauseRulesSettings struct {
	StudioMode     StudioModeSettings `json:"studioMode"`
	Battery        ToggleSettings     `json:"battery"`
	MeteredNetwork ToggleSettings     `json:"meteredNetwork"`
	CPU            CPUSettings        `json:"cpu"`
}

// StudioModeSettings lists the app bundles that pause syncing.
type StudioModeSettings struct {
	Enabled bool     `json:"enabled"`
	Apps    []string `json:"apps"`
}

// ToggleSettings is a rule that is only on or off.
type ToggleSettings struct {
	Enabled bool `json:"enabled"`
}

// CPUSettings configures the CPU Pause Rule.
type CPUSettings struct {
	Enabled          bool `json:"enabled"`
	ThresholdPercent int  `json:"thresholdPercent"`
	WindowSeconds    int  `json:"windowSeconds"`
}

// BandwidthSettings configures the sync bandwidth limit (0 = unlimited).
type BandwidthSettings struct {
	Enabled       bool `json:"enabled"`
	UploadMiBps   int  `json:"uploadMiBps"`
	DownloadMiBps int  `json:"downloadMiBps"`
}

// NotificationSettings toggles each notification kind.
type NotificationSettings struct {
	Errors     bool `json:"errors"`
	Conflicts  bool `json:"conflicts"`
	MassDelete bool `json:"massDelete"`
	LinkCopied bool `json:"linkCopied"`
}

// LogSettings configures the Activity Log.
type LogSettings struct {
	Level         string `json:"level"`
	RetentionDays int    `json:"retentionDays"`
	MaxMB         int    `json:"maxMB"`
}

// UpdateSettings configures the update hint.
type UpdateSettings struct {
	Check          bool   `json:"check"`
	SkippedVersion string `json:"skippedVersion"`
}

// DefaultSettings returns plan Appendix E.
func DefaultSettings() Settings {
	return Settings{
		Autostart:             true,
		MenuBarIcon:           true,
		BaseFolder:            "~/CloudWire",
		MountFolder:           "~/CloudWire/Mounts",
		QuietPeriodSeconds:    60,
		PollIntervalSeconds:   60,
		NextcloudEtagSeconds:  60,
		GenericCheckSeconds:   300,
		SafetyFullSyncMinutes: 60,
		PauseRules: PauseRulesSettings{
			StudioMode:     StudioModeSettings{Enabled: true, Apps: []string{}},
			Battery:        ToggleSettings{Enabled: true},
			MeteredNetwork: ToggleSettings{Enabled: true},
			CPU:            CPUSettings{Enabled: true, ThresholdPercent: 70, WindowSeconds: 30},
		},
		Bandwidth:         BandwidthSettings{Enabled: true, UploadMiBps: 5, DownloadMiBps: 20},
		DefaultCacheMaxGB: 20,
		DefaultMountType:  "nfsmount",
		DefaultExcludes: []string{".DS_Store", "._*", ".Spotlight-V100/**", ".Trashes/**", ".fseventsd/**",
			".TemporaryItems/**", ".DocumentRevisions-V100/**"},
		Notifications: NotificationSettings{Errors: true, Conflicts: true, MassDelete: true, LinkCopied: true},
		Log:           LogSettings{Level: "info", RetentionDays: 30, MaxMB: 50},
		Updates:       UpdateSettings{Check: true},
	}
}

var settingsMu sync.Mutex

// Settings returns the stored settings over the defaults.
func (s *Store) Settings() (Settings, error) {
	m, err := s.settingsMap()
	if err != nil {
		return Settings{}, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	if err := json.Unmarshal(b, &out); err != nil {
		return Settings{}, err
	}
	return out, nil
}

func defaultSettingsMap() (map[string]any, error) {
	b, err := json.Marshal(DefaultSettings())
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) settingsMap() (map[string]any, error) {
	m, err := defaultSettingsMap()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if _, known := m[k]; !known {
			continue
		}
		var val any
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			continue
		}
		// Merge over the defaults so keys added in later versions keep defaults.
		m[k] = MergePatch(m[k], val)
	}
	return m, rows.Err()
}

// UpdateSettings applies an RFC 7396 JSON merge patch and returns the result.
func (s *Store) UpdateSettings(patch map[string]any) (Settings, error) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	cur, err := s.settingsMap()
	if err != nil {
		return Settings{}, err
	}
	// null in the patch removes a key; re-applying the defaults resets it.
	defaults, err := defaultSettingsMap()
	if err != nil {
		return Settings{}, err
	}
	merged, _ := MergePatch(defaults, MergePatch(cur, patch)).(map[string]any)
	b, err := json.Marshal(merged)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	if err := json.Unmarshal(b, &out); err != nil {
		return Settings{}, fmt.Errorf("invalid settings: %w", err)
	}
	if err := out.Validate(); err != nil {
		return Settings{}, err
	}
	// Re-marshal the typed value so only known keys with valid types persist.
	b, _ = json.Marshal(out)
	var clean map[string]json.RawMessage
	_ = json.Unmarshal(b, &clean)
	tx, err := s.db.Begin()
	if err != nil {
		return Settings{}, err
	}
	for k := range patch {
		v, ok := clean[k]
		if !ok {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, string(v)); err != nil {
			_ = tx.Rollback()
			return Settings{}, err
		}
	}
	return out, tx.Commit()
}

// Validate rejects values that would break the Core.
func (st Settings) Validate() error {
	switch {
	case st.QuietPeriodSeconds < 1, st.PollIntervalSeconds < 10, st.NextcloudEtagSeconds < 10,
		st.GenericCheckSeconds < 30, st.SafetyFullSyncMinutes < 1:
		return fmt.Errorf("intervals out of range")
	case st.PauseRules.CPU.ThresholdPercent < 1 || st.PauseRules.CPU.ThresholdPercent > 100:
		return fmt.Errorf("cpu threshold must be 1-100")
	case st.Bandwidth.UploadMiBps < 0 || st.Bandwidth.DownloadMiBps < 0:
		return fmt.Errorf("bandwidth must not be negative")
	case st.DefaultCacheMaxGB < 1:
		return fmt.Errorf("cache size must be at least 1 GB")
	case st.DefaultMountType != "nfsmount" && st.DefaultMountType != "cmount":
		return fmt.Errorf("unknown mount type %q", st.DefaultMountType)
	case st.Log.RetentionDays < 1 || st.Log.MaxMB < 1:
		return fmt.Errorf("log retention out of range")
	case st.BaseFolder == "" || st.MountFolder == "":
		return fmt.Errorf("folders must not be empty")
	}
	switch st.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unknown log level %q", st.Log.Level)
	}
	return nil
}

// MergePatch applies an RFC 7396 merge patch to target and returns the result.
func MergePatch(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	} else {
		cp := make(map[string]any, len(t))
		for k, v := range t {
			cp[k] = v
		}
		t = cp
	}
	for k, v := range p {
		if v == nil {
			delete(t, k)
			continue
		}
		t[k] = MergePatch(t[k], v)
	}
	return t
}
