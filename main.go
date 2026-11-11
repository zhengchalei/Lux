package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/egoist/mygo"
	"lux/assets"
	"lux/internal/client"
	"lux/internal/domain"
	"lux/internal/history"
	"lux/internal/publicdata"
)

// main.go starts the Windows MyGo application with an embedded Vue frontend.
// WebView2 owns browser composition and scrolling while Go owns client discovery,
// history loading, public providers, and explicitly enabled draft automation.
// Runtime notifications are coalesced before reaching the page, and hover or
// scrolling never crosses the Go bridge merely to repaint a component.
//
// The previous Rust executable remains available for migration comparisons but
// is not a dependency of this application. A separate opt-in loopback development
// mode exposes the same product API for browser tests. Normal desktop execution
// uses MyGo's own trusted-page IPC and opens no application HTTP listening port.

// frontendFiles contains the compiled Vue application built by the packaging script.
//
//go:embed all:frontend/dist
var frontendFiles embed.FS

// stateChanged sends a coalesced revision signal without duplicating player records in events.
var stateChanged = mygo.NewEvent[uint64]("lux:changed")

// productFS combines the compiled frontend and existing embedded public portraits.
type productFS struct{ frontend fs.FS } // Compiled frontend filesystem root.

// Open resolves portraits separately while keeping frontend paths within their embedded filesystem.
func (p productFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrInvalid
	}
	if len(name) > 10 && name[:10] == "portraits/" {
		return assets.Files.Open(name)
	}
	return p.frontend.Open(name)
}

// main constructs independent services and opens the actual WebView2 desktop window.
// CLI startup queries wait for account discovery. The no-activate option shows
// the result without taking focus from an application the user currently operates.
func main() {
	serve := flag.String("serve", "", "explicit loopback browser development address")
	page := flag.String("page", "history", "initial workspace")
	self := flag.Bool("query-self", false, "query local account after discovery")
	review := flag.Bool("review-last", false, "review last competitive game after discovery")
	inactive := flag.Bool("no-activate", false, "show without taking foreground focus")
	flag.Parse()
	var app *App
	// Binding generation runs a short CLI process rather than another desktop, so it must remain available while the user has Lux open.
	if *serve == "" && os.Getenv("MYGO_GENERATE") == "" {
		mygo.App.SetName("Lux")
		// Claim the desktop lifetime before constructing client services, so duplicate launches only activate the existing owner and perform no discovery work.
		if !claimDesktopInstance(func() {
			if app != nil && app.desktop != nil {
				app.desktop.show(false)
			}
		}) {
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runtime *client.Runtime
	public := publicdata.New(func(db domain.Database) {
		if runtime != nil {
			runtime.SetDatabase(db)
		}
	})
	db := public.Database()
	runtime = client.New(db, history.New(db))
	app = &App{runtime: runtime, public: public}
	defer runtime.Close()
	web, err := fs.Sub(frontendFiles, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	files := productFS{frontend: web}
	if *serve != "" {
		runtime.Start(ctx)
		go public.Run(ctx)
		go startupQuery(ctx, runtime, *self, *review)
		if err := serveBrowser(ctx, *serve, files, app); err != nil {
			log.Print(err)
		}
		return
	}
	mygo.SetFrontend(files)
	mygo.Bind(app)
	changes := make(chan struct{}, 1)
	runtime.SetListener(func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	})
	mygo.App.WhenReady(func() {
		app.panel = newDraftPanel(ctx, runtime)
		app.desktop = newDesktopController(runtime, app.panel, *page, cancel)
		app.desktop.show(*inactive)
		runtime.Start(ctx)
		go public.Run(ctx)
		go startupQuery(ctx, runtime, *self, *review)
		go notifyPage(ctx, app.desktop, changes, runtime, app.panel)
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { cancel(); runtime.Close() })
	if err := mygo.App.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// startupQuery waits for client discovery before issuing one requested startup action.
// Cancellation ends the wait. Reconnects do not repeat a consumed startup query,
// and a bounded delay avoids occupying the desktop event loop during discovery.
func startupQuery(ctx context.Context, runtime *client.Runtime, self, review bool) {
	if !self && !review {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if runtime.Snapshot().Account == nil {
				continue
			}
			if review {
				_ = runtime.ReviewLastGame()
			} else {
				_ = runtime.QuerySelf(0)
			}
			return
		}
	}
}

// notifyPage coalesces state bursts into revision notifications for the trusted renderer.
// The one-element channel retains pending work while this task waits. It does
// not send records itself, and idle windows schedule no notification timers.
func notifyPage(ctx context.Context, desktop *desktopController, changes <-chan struct{}, runtime *client.Runtime, panel *draftPanel) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			// Merge a short burst so ten concurrent player replies do not cause ten complete state transfers through the renderer bridge.
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-changes:
			default:
			}
			desktop.Notify(runtime.CurrentRevision())
			panel.Notify(runtime.CurrentRevision())
		}
	}
}
