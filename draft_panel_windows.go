package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"lux/internal/client"
)

// draft_panel_windows.go attaches the compact selection window to LeagueClientUx
// without sending pointer movement or window geometry through the renderer.
// A native worker checks foreground visibility and physical bounds, submitting
// window updates only when placement changes. The client executable identity
// distinguishes its window from game and renderer processes, while monitor work
// areas keep the companion reachable on displays with negative coordinates.
//
// The companion opens automatically for each random-mode matchmaking cycle,
// including its readiness and selection stages. Explicit opening can show its
// waiting state. Closing dismisses only the current cycle. Pinned windows disappear
// when the client minimizes; native ownership keeps unrelated apps above them. Unpinned windows
// remain independently movable. All MyGo window operations are marshaled to its
// UI thread, and application cancellation stops the native observer.

// Native window procedures provide geometry and visibility without client authentication or renderer-side window polling.
var (
	dockUser        = syscall.NewLazyDLL("user32.dll")
	dockKernel      = syscall.NewLazyDLL("kernel32.dll")
	dockEnum        = dockUser.NewProc("EnumWindows")
	dockPID         = dockUser.NewProc("GetWindowThreadProcessId")
	dockVisible     = dockUser.NewProc("IsWindowVisible")
	dockIconic      = dockUser.NewProc("IsIconic")
	dockRect        = dockUser.NewProc("GetWindowRect")
	dockMonitor     = dockUser.NewProc("MonitorFromWindow")
	dockMonitorInfo = dockUser.NewProc("GetMonitorInfoW")
	dockPosition    = dockUser.NewProc("SetWindowPos")
	dockOwner       = dockUser.NewProc("SetWindowLongPtrW")
	dockStyle       = dockUser.NewProc("GetWindowLongPtrW")
	dockHook        = dockUser.NewProc("SetWinEventHook")
	dockUnhook      = dockUser.NewProc("UnhookWinEvent")
	dockDPI         = dockUser.NewProc("GetDpiForWindow")
	dockImage       = dockKernel.NewProc("QueryFullProcessImageNameW")
)

// Enumeration state is serialized because the process-lifetime callback cannot capture a fresh closure on every scan without retaining callback stubs.
var (
	dockEnumerationMu sync.Mutex
	dockChosen        uintptr
	dockLargest       int32
	dockCallback      = syscall.NewCallback(inspectLeagueWindow)
)

// pixelRect stores physical desktop coordinates including negative monitor origins.
type pixelRect struct{ Left, Top, Right, Bottom int32 } // Physical pixel edge coordinates.

// monitorRecord mirrors the native monitor work-area structure used for placement.
type monitorRecord struct {
	Size    uint32    // Native structure byte count.
	Monitor pixelRect // Full physical monitor rectangle.
	Work    pixelRect // Monitor area excluding taskbar.
	Flags   uint32    // Native primary monitor flag.
}

// DraftPanelStatus reports compact window preferences without retaining native handles.
type DraftPanelStatus struct {
	Collapsed bool `json:"collapsed"` // Companion is a narrow edge tab.
	Enabled   bool `json:"enabled"`   // Automatic companion opening is enabled.
	Pinned    bool `json:"pinned"`    // Follow client position and visibility.
	Visible   bool `json:"visible"`   // Current native companion visibility.
	Attached  bool `json:"attached"`  // A usable client window was found.
}

// draftPanel coordinates worker observations with UI-owned companion window lifetime.
type draftPanel struct {
	mu            sync.Mutex       // Protects preferences and native observation state.
	window        *mygo.Window     // Lazy companion protected by mu after creation.
	runtime       *client.Runtime  // Lightweight selection activity observation.
	status        DraftPanelStatus // Current preferences and observed visibility.
	manual        bool             // Explicit opening permits the waiting state.
	activityKey   string           // Last automatically opened matchmaking cycle.
	last          pixelRect        // Last submitted physical placement rectangle.
	lastVisible   bool             // Last submitted native visibility state.
	lastPinned    bool             // Last applied native attachment preference.
	lastCollapsed bool             // Last applied folded edge-tab state.
	target        atomic.Uintptr   // Native client handle observed by location callbacks.
	wake          chan struct{}    // Coalesced native client movement notifications.
	// expandedStyle belongs only to the UI reconciliation callback and restores the normal window frame after folding into a borderless narrow tab.
	expandedStyle uintptr // Expanded companion native style bits.
}

// newDraftPanel starts a native observer without allocating another WebView initially.
// The caller runs after MyGo readiness. Closing this window dismisses the current
// matchmaking cycle but leaves the main window and client services running normally.
// A process-lifetime native callback is registered on the pumping UI thread and
// wakes the observer only for the discovered client's own location changes.
// Cancellation releases that hook. The periodic observer remains available when
// hook registration fails, so attachment does not depend on event delivery alone.
func newDraftPanel(ctx context.Context, runtime *client.Runtime) *draftPanel {
	p := &draftPanel{runtime: runtime, wake: make(chan struct{}, 1), status: DraftPanelStatus{Enabled: true, Pinned: true}}
	// Register on MyGo's pumping UI thread; movement events wake native placement directly without renderer messages or another polling interval.
	callback := syscall.NewCallback(func(hook, event, hwnd, object, child, thread, stamp uintptr) uintptr {
		if hwnd == p.target.Load() && object == 0 && child == 0 {
			select {
			case p.wake <- struct{}{}:
			default:
			}
		}
		return 0
	})
	hook, _, _ := dockHook.Call(0x800B, 0x800B, 0, callback, 0, 0, 0)
	go func() {
		<-ctx.Done()
		mygo.RunOnMain(func() {
			if hook != 0 {
				dockUnhook.Call(hook)
			}
		})
	}()
	go p.observe(ctx)
	return p
}

// ensureWindow creates the companion lazily when a visible state first requires it.
// The caller runs on the UI thread. Publication under mu lets notifications use
// the same window without allocating an idle WebView at application startup.
func (p *draftPanel) ensureWindow() *mygo.Window {
	p.mu.Lock()
	window := p.window
	p.mu.Unlock()
	if window != nil && !window.IsDestroyed() {
		return window
	}
	window = mygo.NewWindow(mygo.WindowOptions{Title: "Lux · 大乱斗选人", URL: "/?panel=draft", Width: 344, Height: 680, MinWidth: 310, MinHeight: 420, Frameless: true, Hidden: true, SkipTaskbar: true, DisableMaximize: true, BackgroundColor: "#f3f5fa", Page: mygo.PageOptions{DevTools: mygo.DevToolsDisabled}})
	applyWindowIcon(window)
	p.expandedStyle = 0
	window.OnClose(func(event *mygo.CloseEvent) { event.PreventDefault(); p.Configure(false, true) })
	p.mu.Lock()
	p.window = window
	p.mu.Unlock()
	return window
}

// Notify signals only an existing visible companion without copying historical data.
func (p *draftPanel) Notify(revision uint64) {
	p.mu.Lock()
	window, visible := p.window, p.status.Visible
	p.mu.Unlock()
	if window != nil && visible {
		_ = stateChanged.Emit(window, revision)
	}
}

// Status returns the last observed companion preferences under its state lock.
func (p *draftPanel) Status() DraftPanelStatus { p.mu.Lock(); defer p.mu.Unlock(); return p.status }

// Shutdown destroys the companion after application observers have been canceled.
// Call on the UI thread during explicit application quitting. Destroy bypasses
// the companion's ordinary close-to-disable handler so it cannot prevent exit.
func (p *draftPanel) Shutdown() {
	p.mu.Lock()
	p.status.Enabled, p.status.Visible, p.manual = false, false, false
	window := p.window
	p.window = nil
	p.mu.Unlock()
	if window != nil {
		window.Destroy()
	}
}

// Configure applies explicit open, close, pin, or unpin preferences without focus theft.
// Opening permits a waiting view until matching begins. Closing clears that
// exception and dismisses this cycle; later matching can open the panel again.
func (p *draftPanel) Configure(enabled, pinned bool) {
	p.mu.Lock()
	p.status.Enabled = enabled
	p.status.Pinned = pinned
	p.status.Collapsed = false
	p.manual = enabled
	p.last = pixelRect{}
	p.mu.Unlock()
}

// Collapse folds the attached companion into a narrow reachable edge tab.
// Detached windows stay expanded so their draggable controls remain usable.
// The observer applies dimensions on its next native reconciliation pass.
func (p *draftPanel) Collapse(collapsed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status.Collapsed = collapsed && p.status.Pinned
}

// activity applies automatic opening once for each verified random-mode cycle.
// The native observer calls this before placement reconciliation. Closing remains
// effective throughout the current cycle, and a later cycle restores the expanded
// panel. Minimizing the client changes native visibility only, so restoring it
// cannot override either the user's close action or chosen collapsed state.
func (p *draftPanel) activity(key string, playing bool) (DraftPanelStatus, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if key != "" && key != p.activityKey {
		p.activityKey = key
		p.status.Enabled = true
		p.status.Collapsed = false
	}
	if key != "" || playing {
		p.manual = false
	}
	return p.status, p.manual
}

// companionPlacement prefers the client's right edge within the monitor work area.
// If that side cannot fit, a free left side is preferred. Otherwise the window
// remains inside the work area, overlapping the client's edge rather than
// becoming unreachable beyond a screen or behind the taskbar. All sizes and
// offsets use physical pixels, so callers convert logical dimensions before
// invoking the placement calculation with their current target display scale.
func companionPlacement(client, work pixelRect, width, height, gap int32) pixelRect {
	width = min(width, work.Right-work.Left)
	height = min(height, work.Bottom-work.Top)
	x := client.Right + gap
	// When the right edge exceeds the work area, available left-side space takes precedence over a reachable but overlapping placement.
	if x+width > work.Right {
		if client.Left-gap-width >= work.Left {
			x = client.Left - gap - width
		} else {
			x = work.Right - width
		}
	}
	x = max(work.Left, min(x, work.Right-width))
	y := max(work.Top, min(client.Top, work.Bottom-height))
	return pixelRect{x, y, x + width, y + height}
}

// leagueWindow locates the largest visible LeagueClientUx top-level native window.
// Processes that cannot be queried are skipped. Every opened process handle is
// closed during enumeration; neither executable command lines nor credentials
// are inspected by this window-placement subsystem.
func leagueWindow() uintptr {
	dockEnumerationMu.Lock()
	defer dockEnumerationMu.Unlock()
	dockChosen, dockLargest = 0, 0
	dockEnum.Call(dockCallback, 0)
	return dockChosen
}

// inspectLeagueWindow retains the largest matching process window during native enumeration.
func inspectLeagueWindow(hwnd, unused uintptr) uintptr {
	visible, _, _ := dockVisible.Call(hwnd)
	if visible == 0 {
		return 1
	}
	var pid uint32
	dockPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	handle, err := syscall.OpenProcess(0x1000, false, pid)
	if err != nil {
		return 1
	}
	buffer := make([]uint16, 1024)
	size := uint32(len(buffer))
	ok, _, _ := dockImage.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	syscall.CloseHandle(handle)
	if ok == 0 || !strings.EqualFold(filepath.Base(syscall.UTF16ToString(buffer[:size])), "LeagueClientUx.exe") {
		return 1
	}
	var rect pixelRect
	dockRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	area := (rect.Right - rect.Left) * (rect.Bottom - rect.Top)
	if area > dockLargest {
		dockChosen = hwnd
		dockLargest = area
	}
	return 1
}

// observe reconciles physical placement and visibility without renderer traffic.
// Client discovery runs every two seconds, while active geometry checks run at
// eighty milliseconds. Only changed bounds enter the UI queue. Native foreground
// ownership keeps the companion above League without globally covering unrelated
// applications. Minimization and phase transitions control hiding independently
// from foreground focus, preventing focus changes from producing panel flicker.
func (p *draftPanel) observe(ctx context.Context) {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	var clientHandle uintptr
	var discovered time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
		if time.Since(discovered) > 2*time.Second {
			clientHandle = leagueWindow()
			p.target.Store(clientHandle)
			discovered = time.Now()
		}
		activityKey, playing := p.runtime.DraftActivity()
		status, manual := p.activity(activityKey, playing)
		visible := status.Enabled && !playing && (activityKey != "" || manual)
		attached := clientHandle != 0
		var placement pixelRect
		if status.Pinned {
			iconic, _, _ := dockIconic.Call(clientHandle)
			shown, _, _ := dockVisible.Call(clientHandle)
			visible = visible && attached && iconic == 0 && shown != 0
			if attached {
				var rect pixelRect
				dockRect.Call(clientHandle, uintptr(unsafe.Pointer(&rect)))
				monitor, _, _ := dockMonitor.Call(clientHandle, 2)
				info := monitorRecord{Size: uint32(unsafe.Sizeof(monitorRecord{}))}
				dockMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info)))
				dpi, _, _ := dockDPI.Call(clientHandle)
				if dpi == 0 {
					dpi = 96
				}
				// Native rectangles use physical pixels; convert the companion's logical size using the target client's current monitor DPI.
				width, height := int32(344*dpi/96), rect.Bottom-rect.Top
				if status.Collapsed {
					width, height = int32(36*dpi/96), int32(144*dpi/96)
					rect.Top += (rect.Bottom - rect.Top - height) / 2
				}
				placement = companionPlacement(rect, info.Work, width, height, 0)
			}
		}
		p.mu.Lock()
		changed := visible != p.lastVisible || status.Pinned != p.lastPinned || status.Collapsed != p.lastCollapsed || (visible && status.Pinned && placement != p.last)
		p.status.Attached = attached
		p.status.Visible = visible
		p.lastVisible = visible
		p.last = placement
		p.lastPinned = status.Pinned
		p.lastCollapsed = status.Collapsed
		p.mu.Unlock()
		if changed {
			// Marshal the complete visibility-placement decision together so MyGo's window methods never race its UI-owned window destruction.
			mygo.RunOnMain(func() {
				if ctx.Err() != nil {
					return
				}
				p.mu.Lock()
				window := p.window
				p.mu.Unlock()
				if !visible {
					if window != nil {
						// An inactive selection panel has no useful renderer work; release its WebView while retaining native preferences for the next draft.
						p.mu.Lock()
						p.window = nil
						p.mu.Unlock()
						window.Destroy()
					}
					return
				}
				window = p.ensureWindow()
				if window.IsDestroyed() {
					return
				}
				// MyGo's nonmovable flag also suppresses programmatic WM_WINDOWPOSCHANGING moves, so attachment temporarily permits native positioning before restoring drag protection.
				window.SetMovable(true)
				// Windows retains caption minimum widths even for a frameless MyGo window; folding removes those native style bits and expansion restores them before normal sizing.
				if status.Collapsed {
					if p.expandedStyle == 0 {
						p.expandedStyle, _, _ = dockStyle.Call(window.NativeHandle(), ^uintptr(15))
					}
					dockOwner.Call(window.NativeHandle(), ^uintptr(15), p.expandedStyle&^uintptr(0x00C00000|0x00040000|0x00080000|0x00020000|0x00010000))
				} else if p.expandedStyle != 0 {
					dockOwner.Call(window.NativeHandle(), ^uintptr(15), p.expandedStyle)
					p.expandedStyle = 0
				}
				if status.Collapsed {
					window.SetMinimumSize(36, 100)
				} else {
					window.SetMinimumSize(310, 420)
				}
				// Native ownership keeps this companion above League while other applications retain normal z-order; global topmost and foreground-dependent hiding cause distracting focus transitions.
				window.SetAlwaysOnTop(false)
				owner := uintptr(0)
				if status.Pinned {
					owner = clientHandle
				}
				dockOwner.Call(window.NativeHandle(), ^uintptr(7), owner)
				window.ShowInactive()
				if status.Pinned {
					dockPosition.Call(window.NativeHandle(), 0, uintptr(placement.Left), uintptr(placement.Top), uintptr(placement.Right-placement.Left), uintptr(placement.Bottom-placement.Top), 0x0010|0x0040|0x0004|0x0020)
				}
				window.SetMovable(!status.Pinned)
			})
		}
	}
}
