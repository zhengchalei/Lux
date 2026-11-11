package main

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/egoist/mygo"
	"lux/assets"
	"lux/internal/client"
)

// desktop.go owns the main window independently from Lux's background services.
// Closing a tray-enabled main window destroys its WebView instead of retaining
// a hidden history renderer. The Go runtime and the independently attached draft
// companion continue, and the tray menu creates a fresh main window on demand.
// Native windows and the tray share the embedded Lux champion portrait.
// A failed tray registration restores ordinary close-to-exit behavior.
//
// Window and tray pointers belong to MyGo's UI thread. Background notifications
// enter that thread before reading the current main window, preventing stale
// captured-window references after a restore. Explicit application quitting first
// cancels observers and destroys the companion, whose ordinary close handler
// otherwise preserves background selection. The zero-window listener keeps the
// process alive only while a usable tray remains registered.

// DesktopStatus exposes native tray availability without sharing native window handles.
type DesktopStatus struct {
	TrayAvailable bool `json:"trayAvailable"` // Native tray recovery is registered.
}

// desktopController owns UI-thread resources and the application cancellation boundary.
type desktopController struct {
	runtime   *client.Runtime    // Persistent client service owner.
	panel     *draftPanel        // Independent draft companion owner.
	window    *mygo.Window       // UI-thread current main window.
	tray      *mygo.Tray         // UI-thread recovery menu owner.
	available atomic.Bool        // Cross-thread tray availability observation.
	quitting  bool               // UI-thread shutdown has begun.
	page      string             // Initial restored workspace URL.
	cancel    context.CancelFunc // Application worker cancellation boundary.
}

// newDesktopController registers tray recovery before opening the initial main window.
// Call after MyGo readiness on its UI thread. Tray failure leaves the main
// window accessible and uses ordinary application exit when that window closes.
// The last-window listener preserves background workers only with registered tray
// recovery. A before-quit handler cancels those workers and destroys windows before
// their ordinary close interception can keep the application alive. The menu's
// recovery action always resolves this controller's current window ownership.
func newDesktopController(runtime *client.Runtime, panel *draftPanel, page string, cancel context.CancelFunc) *desktopController {
	d := &desktopController{runtime: runtime, panel: panel, page: page, cancel: cancel}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "打开 Lux", Click: func(*mygo.MenuItem, *mygo.Window) { d.show(false) }},
		{Label: "大乱斗选人窗口", Click: func(*mygo.MenuItem, *mygo.Window) { panel.Configure(true, true) }},
		mygo.Separator(),
		{Label: "退出 Lux", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "dev.lux.lolassistant", Icon: assets.AppIcon, ToolTip: "Lux · 英雄联盟助手", Menu: menu})
	if err != nil {
		log.Print("系统托盘不可用，关闭主窗口将退出：", err)
	} else {
		d.tray = tray
		d.available.Store(true)
		// Windows delivers left activation to OnClick and right activation to its attached native menu, keeping opening and quitting independently reachable.
		tray.OnClick(func() { d.show(false) })
	}
	mygo.App.OnWindowAllClosed(func() {
		if !d.quitting && d.tray == nil {
			mygo.App.Quit()
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { d.shutdown() })
	return d
}

// show restores the existing main window or allocates a fresh embedded renderer.
// The UI thread calls this method. Inactive initial display preserves foreground
// focus; explicit tray recovery restores and focuses the user's requested window.
// Closing with tray permission removes the owned window reference before native
// destruction, allowing queued state notifications to skip it. Closing without
// that permission delegates to application quit, which owns companion cleanup and
// service cancellation instead of tying either lifecycle to this renderer.
func (d *desktopController) show(inactive bool) {
	if d.quitting {
		return
	}
	if d.window == nil || d.window.IsDestroyed() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "Lux · 英雄联盟战绩助手", URL: "/?page=" + d.page, Width: 1280, Height: 860, MinWidth: 1060, MinHeight: 700, UseContentSize: true, Hidden: true, BackgroundColor: "#f3f5fa", Page: mygo.PageOptions{DevTools: mygo.DevToolsDisabled}})
		applyWindowIcon(window)
		d.window = window
		window.OnClose(func(event *mygo.CloseEvent) {
			if d.quitting {
				return
			}
			event.PreventDefault()
			if d.tray != nil && d.runtime.ApplicationSettings().CloseToTray {
				// Remove the UI reference before destroying its WebView so subsequent state events cannot target the released renderer.
				d.window = nil
				window.Destroy()
			} else {
				mygo.App.Quit()
			}
		})
	}
	if inactive {
		d.window.ShowInactive()
	} else {
		d.window.Restore()
		d.window.Show()
		d.window.Focus()
	}
}

// Status reads tray capability without requiring a UI-thread round trip.
func (d *desktopController) Status() DesktopStatus {
	return DesktopStatus{TrayAvailable: d.available.Load()}
}

// Notify emits only into the currently owned visible main renderer.
// Callers may run outside the UI thread. The queued callback resolves the
// window at execution time, so tray restoration cannot inherit a stale target.
func (d *desktopController) Notify(revision uint64) {
	mygo.RunOnMain(func() {
		if !d.quitting && d.window != nil && !d.window.IsDestroyed() && d.window.IsVisible() && !d.window.IsMinimized() {
			_ = stateChanged.Emit(d.window, revision)
		}
	})
}

// shutdown releases all native resources before MyGo performs ordinary application quitting.
// The UI thread calls this method once or repeatedly. Observer cancellation runs
// before window destruction, and the quitting flag suppresses queued main recovery.
// The companion is destroyed directly because its regular close handler disables
// automatic opening while retaining services. Tray removal happens after window
// release, and capability publication then tells surviving callers recovery is no
// longer available. Repeated quit events cannot recreate any destroyed resource.
func (d *desktopController) shutdown() {
	if d.quitting {
		return
	}
	d.quitting = true
	d.cancel()
	d.panel.Shutdown()
	if d.window != nil {
		d.window.Destroy()
		d.window = nil
	}
	if d.tray != nil {
		d.tray.Destroy()
		d.tray = nil
	}
	d.available.Store(false)
}

// applyWindowIcon gives a newly created native window the shared Lux portrait.
// Call on MyGo's UI thread before showing the window. An icon failure is logged
// while the functional window remains available to the user.
func applyWindowIcon(window *mygo.Window) {
	if err := window.SetIcon(assets.AppIcon); err != nil {
		log.Print("无法设置 Lux 窗口图标：", err)
	}
}
