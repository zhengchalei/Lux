package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lux/internal/domain"
)

// settings.go restores and persists Lux preferences across the Rust and Go clients.
// Persisted rules never contain runtime permission to perform a selection action.
// Reads accept the previous snake_case field names while writes preserve that
// format, so switching between application builds does not discard priorities.
// Missing files are ordinary first-run settings; malformed files leave the
// corresponding defaults visible rather than preventing client discovery.
//
// The settings mutex serializes replacement within this process. Temporary files
// are closed before rename, and callers update runtime state independently from
// a save failure so an enabled automation switch can always be stopped promptly.

// settingsMu serializes the two preference files without coupling their readers to network or rendering work in another goroutine.
var settingsMu sync.Mutex

// dataDirectory locates the existing per-user Lux preference directory on Windows.
// LOCALAPPDATA takes precedence, preserving the previous application's storage
// location. Other hosts use Go's user configuration directory for isolated tests.
func dataDirectory() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	return filepath.Join(base, "Lux")
}

// defaultAutomation supplies protected empty rules with no runtime execution permission.
func defaultAutomation() domain.AutomationSettings {
	return domain.AutomationSettings{AutoPick: true, LockPick: true, ProtectAllies: true, PreserveManual: true, DelaySeconds: 2, Picks: []int{}, Bans: []int{}}
}

// loadSettings restores compatible rules and bounds the retained analysis depth.
// Malformed files retain the defaults for that file only. Runtime enablement is
// intentionally absent from both serialized objects and always starts false.
func loadSettings() (domain.AutomationSettings, domain.AnalysisSettings) {
	automation := defaultAutomation()
	analysis := domain.AnalysisSettings{MatchCount: 50, SameMode: true}
	// Restore each file separately so a damaged rules file cannot discard the user's independently configured history sample depth.
	if raw, err := os.ReadFile(filepath.Join(dataDirectory(), "automation.json")); err == nil {
		candidate := automation
		if decodeCompatible(raw, &candidate) == nil {
			automation = candidate
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dataDirectory(), "analysis.json")); err == nil {
		candidate := analysis
		if decodeCompatible(raw, &candidate) == nil {
			analysis = candidate
		}
	}
	analysis.MatchCount = boundedCount(analysis.MatchCount)
	return automation, analysis
}

// decodeCompatible removes underscore spelling differences before decoding preference fields.
// It decodes into a temporary copy at the caller, so malformed fields cannot
// partially replace unrelated defaults when the JSON document itself is invalid.
func decodeCompatible(raw []byte, out any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	normalized := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		parts := strings.Split(key, "_")
		for index := 1; index < len(parts); index++ {
			if parts[index] != "" {
				parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
			}
		}
		normalized[strings.Join(parts, "")] = value
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

// boundedCount restricts history depth to the supported user-facing sample choices.
// Any unsupported or malformed persisted count selects fifty records. It never
// interprets an arbitrary input as permission for an unbounded history scan.
func boundedCount(count int) int {
	switch count {
	case 20, 50, 100, 200:
		return count
	default:
		return 50
	}
}

// savePreference replaces one compatible settings file after its temporary contents close.
// The caller supplies only persisted preferences. Runtime permission, accounts,
// tokens, and player histories must never be passed to this function.
func savePreference(name string, value any) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	compatible := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		var snake strings.Builder
		for _, character := range key {
			if character >= 'A' && character <= 'Z' {
				snake.WriteByte('_')
				snake.WriteRune(character + ('a' - 'A'))
			} else {
				snake.WriteRune(character)
			}
		}
		compatible[snake.String()] = value
	}
	raw, err = json.MarshalIndent(compatible, "", "  ")
	if err != nil {
		return err
	}
	directory := dataDirectory()
	if err = os.MkdirAll(directory, 0700); err != nil {
		return errors.New("无法创建设置目录")
	}
	temporary, err := os.CreateTemp(directory, ".preferences-*.json")
	if err != nil {
		return errors.New("无法创建设置文件")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_, writeErr := temporary.Write(raw)
	closeErr := temporary.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("无法保存设置")
	}
	// Rename publishes only closed complete settings, preserving the prior file when the temporary write fails before this replacement point.
	if err = os.Rename(temporaryPath, filepath.Join(directory, name)); err != nil {
		return errors.New("无法替换设置文件")
	}
	return nil
}
