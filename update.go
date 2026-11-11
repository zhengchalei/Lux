package main

import (
	_ "embed"
	"encoding/json"
	"errors"

	"lux/internal/updater"
)

// update.go exposes automatic release observations independently from player data.
// The embedded build configuration supplies the installed version in desktop and
// browser builds alike. Native installation remains restricted to the desktop
// service, and an explicit restart cannot interrupt matching or active gameplay.

// buildConfiguration supplies the release version without depending on the process filename.
//
//go:embed mygo.json
var buildConfiguration []byte

// applicationVersion reads the checked-in semantic version embedded by this build.
func applicationVersion() string {
	var configuration struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(buildConfiguration, &configuration) != nil {
		return "0.0.0"
	}
	return configuration.Version
}

// GetUpdateStatus returns lightweight download progress independent of runtime snapshots.
func (a *App) GetUpdateStatus() updater.Status { return a.updates.Status() }

// SetAutoUpdate persists the automatic update preference and revokes disabled work.
func (a *App) SetAutoUpdate(enabled bool) error { return a.updates.SetEnabled(enabled) }

// CheckForUpdates requests one asynchronous check without waiting on GitHub IPC.
func (a *App) CheckForUpdates() { a.updates.Check() }

// RestartToUpdate installs a ready update and explicitly reopens the main window.
// Matching, selection, and gameplay reject restart before any installer starts.
// A successful worker handshake precedes application quit, preserving the current
// application if the target directory cannot accept its pending replacement.
func (a *App) RestartToUpdate() error {
	if a.desktop == nil {
		return errors.New("更新重启仅在桌面版中可用")
	}
	if a.runtime.UpdateRestartBlocked() {
		return errors.New("正在匹配、选人或对局，请结束后再更新重启")
	}
	if err := a.updates.PrepareExit(true); err != nil {
		return err
	}
	a.desktop.quitForUpdate()
	return nil
}
