package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"lux/internal/client"
	"lux/internal/domain"
	"lux/internal/history"
)

// desktop_test.go verifies the native tray lifecycle in an explicitly enabled
// Windows test process. Ordinary package tests create no windows or client
// commands, and native checks show inactive windows without interrupting gameplay.

// TestMain keeps MyGo's native loop on the process main goroutine when opted in.
// Ordinary package tests run through Go's standard runner. The separate native
// process exercises actual window resources without client discovery or game writes.
func TestMain(m *testing.M) {
	if os.Getenv("LUX_NATIVE_LIFECYCLE") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(verifyDesktopLifecycle())
}

// verifyDesktopLifecycle closes and recreates real WebViews around persistent native services.
// Call from the process main goroutine. Its inactive windows preserve foreground
// focus while checking tray registration, zero-window survival, recovery, and shutdown.
func verifyDesktopLifecycle() int {
	settingsDirectory, err := os.MkdirTemp("", "lux-native-lifecycle-")
	if err != nil {
		fmt.Println(err)
		return 1
	}
	previousSettings := os.Getenv("LOCALAPPDATA")
	// Native test preferences belong to a fresh temporary directory, keeping real user settings independent from close-to-exit verification.
	_ = os.Setenv("LOCALAPPDATA", settingsDirectory)
	defer os.Setenv("LOCALAPPDATA", previousSettings)
	defer os.RemoveAll(settingsDirectory)
	failed := false
	fail := func(message string) { fmt.Println("FAIL:", message); failed = true }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := client.New(domain.Database{}, history.New(domain.Database{}))
	defer runtime.Close()
	web, err := fs.Sub(frontendFiles, "frontend/dist")
	if err != nil {
		fmt.Println(err)
		return 1
	}
	mygo.SetFrontend(productFS{frontend: web})
	app := &App{runtime: runtime}
	mygo.Bind(app)
	mygo.App.WhenReady(func() {
		panel := newDraftPanel(ctx, runtime)
		app.panel = panel
		d := newDesktopController(runtime, panel, "settings", cancel)
		app.desktop = d
		if !d.Status().TrayAvailable {
			fail("native tray did not register")
			mygo.App.Quit()
			return
		}
		d.show(true)
		original := d.window
		go func() {
			time.Sleep(1800 * time.Millisecond)
			mygo.RunOnMain(func() { original.Close() })
			time.Sleep(1300 * time.Millisecond)
			mygo.RunOnMain(func() {
				if !original.IsDestroyed() || d.window != nil || len(mygo.Windows()) != 0 || ctx.Err() != nil {
					fail("main close did not preserve background with zero WebViews")
				}
				d.show(true)
				if d.window == nil || d.window == original || d.window.IsDestroyed() {
					fail("tray recovery did not recreate main")
				}
				companion := panel.ensureWindow()
				panel.Shutdown()
				if !companion.IsDestroyed() {
					fail("companion destruction failed")
				}
			})
			time.Sleep(1200 * time.Millisecond)
			mygo.RunOnMain(func() {
				if err := runtime.SaveApplicationSettings(domain.ApplicationSettings{AcceptDelaySeconds: 2}); err != nil {
					fail(err.Error())
				}
				d.window.Close()
			})
		}()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Println(err)
		return 1
	}
	if ctx.Err() == nil {
		fail("explicit exit did not cancel background services")
	}
	if failed {
		return 1
	}
	fmt.Println("PASS: native tray registered; zero-window background survived; main restored; companion released; close-to-exit canceled services")
	return 0
}
