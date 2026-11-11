package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"lux/internal/client"
	"lux/internal/domain"
	"lux/internal/history"
	"lux/internal/updater"
)

// desktop_test.go verifies the native tray lifecycle in an explicitly enabled
// Windows test process. Ordinary package tests create no windows or client
// commands. Native recovery checks temporarily activate their isolated test window.

// TestMain keeps MyGo's native loop on the process main goroutine when opted in.
// Ordinary package tests run through Go's standard runner. The separate native
// process exercises actual window resources without client discovery or game writes.
func TestMain(m *testing.M) {
	if os.Getenv("LUX_SINGLE_INSTANCE_PROBE") == "1" {
		mygo.App.SetName(os.Getenv("LUX_TEST_INSTANCE_NAME"))
		if claimDesktopInstance(func() {}) {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Getenv("LUX_NATIVE_LIFECYCLE") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(verifyDesktopLifecycle())
}

// verifyDesktopLifecycle closes and recreates real WebViews around persistent native services.
// Call from the process main goroutine. The isolated native process checks tray
// registration, duplicate launch recovery, zero-window survival, and application shutdown.
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
	updates := updater.New(ctx, applicationVersion(), filepath.Join(settingsDirectory, "updates"), "")
	defer updates.Close()
	app := &App{runtime: runtime, updates: updates}
	mygo.Bind(app)
	instanceName := fmt.Sprintf("Lux.lifecycle.%d", os.Getpid())
	mygo.App.SetName(instanceName)
	var activations atomic.Int32
	if !claimDesktopInstance(func() { activations.Add(1); app.desktop.show(false) }) {
		fmt.Println("FAIL: native test did not claim its isolated instance")
		return 1
	}
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
			mygo.RunOnMain(func() {
				if activations.Load() != 1 {
					fail("startup activation was lost before UI readiness")
				}
				original.Minimize()
			})
			if err := launchSecondInstance(instanceName); err != nil {
				mygo.RunOnMain(func() { fail(err.Error()); mygo.App.Quit() })
				return
			}
			time.Sleep(500 * time.Millisecond)
			mygo.RunOnMain(func() {
				if activations.Load() != 2 || d.window != original || d.window.IsMinimized() {
					fail("second launch did not restore the same minimized main")
				}
			})
			mygo.RunOnMain(func() { original.Close() })
			time.Sleep(1300 * time.Millisecond)
			mygo.RunOnMain(func() {
				if !original.IsDestroyed() || d.window != nil || len(mygo.Windows()) != 0 || ctx.Err() != nil {
					fail("main close did not preserve background with zero WebViews")
				}
			})
			if err := launchSecondInstance(instanceName); err != nil {
				mygo.RunOnMain(func() { fail(err.Error()); mygo.App.Quit() })
				return
			}
			time.Sleep(500 * time.Millisecond)
			mygo.RunOnMain(func() {
				if activations.Load() != 3 {
					fail("background second launch was not delivered")
				}
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
	if err := launchSecondInstance(instanceName); err != nil {
		fmt.Println("FAIL: startup forwarding:", err)
		return 1
	}
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
	fmt.Println("PASS: native tray registered; duplicate launches exited before UI readiness and restored minimized/background main; zero-window background survived; companion released; close-to-exit canceled services")
	return 0
}

// launchSecondInstance starts a real duplicate process using the test's isolated identity.
// The helper must forward activation and exit successfully within five seconds.
// Hidden process creation avoids a console window during native desktop verification.
func launchSecondInstance(name string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable)
	command.Env = append(os.Environ(), "LUX_SINGLE_INSTANCE_PROBE=1", "LUX_TEST_INSTANCE_NAME="+name)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command.Run()
}
