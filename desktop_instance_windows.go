package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
)

// desktop_instance_windows.go reserves one Lux desktop owner with a named Windows
// event. Creating the event is atomic, and another launch signals its existing
// owner before exiting without constructing client services or another WebView.
// MyGo's Unix-domain socket transport cannot connect on the tested Windows host.
//
// The event identity includes the current Windows account and application name,
// keeping native tests independent from the installed application. Its access
// descriptor admits that account at medium integrity, allowing an ordinary launch
// to activate an elevated owner. A pending signal survives early startup until
// the UI is ready. Quitting wakes the receiver and releases its native handle.

// Native event procedures share startup ownership without creating another window or application network listener.
var (
	instanceKernel          = syscall.NewLazyDLL("kernel32.dll")
	instanceCreateEvent     = instanceKernel.NewProc("CreateEventW")
	instanceSetEvent        = instanceKernel.NewProc("SetEvent")
	instanceLocalFree       = instanceKernel.NewProc("LocalFree")
	instanceSecurity        = syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	instanceAllowForeground = syscall.NewLazyDLL("user32.dll").NewProc("AllowSetForegroundWindow")
)

// claimDesktopInstance forwards duplicate launches to the existing native desktop owner.
// Set the application name and call once before constructing services. Return
// immediately when false; the existing owner receives activation after UI readiness.
// A kernel event atomically arbitrates simultaneous launches and retains a signal
// during startup. Shutdown interrupts the receiver before its handle is released.
func claimDesktopInstance(show func()) bool {
	handle, existing, err := openDesktopEvent(mygo.App.Name())
	if err != nil {
		log.Print("无法建立 Lux 单实例：", err)
		return false
	}
	if existing {
		defer syscall.CloseHandle(handle)
		instanceAllowForeground.Call(0xFFFFFFFF)
		if result, _, err := instanceSetEvent.Call(uintptr(handle)); result == 0 {
			log.Print("无法唤起 Lux 窗口：", err)
		}
		return false
	}
	var stopping atomic.Bool
	mygo.App.OnQuit(func() { stopping.Store(true); instanceSetEvent.Call(uintptr(handle)) })
	mygo.App.WhenReady(func() {
		go func() {
			defer syscall.CloseHandle(handle)
			for {
				result, err := syscall.WaitForSingleObject(handle, syscall.INFINITE)
				if err != nil || result != syscall.WAIT_OBJECT_0 || stopping.Load() {
					return
				}
				mygo.App.WhenReady(show)
			}
		}()
	})
	return true
}

// openDesktopEvent creates or opens the current account's auto-reset activation event.
// The caller owns the returned handle. Security attributes authorize the same
// account across ordinary and elevated launches without accepting other users.
func openDesktopEvent(name string) (syscall.Handle, bool, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return 0, false, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return 0, false, err
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return 0, false, err
	}
	identity := sha256.Sum256([]byte(sid + ":" + name))
	eventName, _ := syscall.UTF16PtrFromString(fmt.Sprintf("Local\\Lux.%x", identity[:12]))
	descriptor, _ := syscall.UTF16PtrFromString("D:(A;;GA;;;SY)(A;;GA;;;" + sid + ")S:(ML;;NW;;;ME)")
	var security uintptr
	if result, _, err := instanceSecurity.Call(uintptr(unsafe.Pointer(descriptor)), 1, uintptr(unsafe.Pointer(&security)), 0); result == 0 {
		return 0, false, err
	}
	defer instanceLocalFree.Call(security)
	attributes := syscall.SecurityAttributes{SecurityDescriptor: security}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	handle, _, result := instanceCreateEvent.Call(uintptr(unsafe.Pointer(&attributes)), 0, 0, uintptr(unsafe.Pointer(eventName)))
	if handle == 0 {
		return 0, false, result
	}
	return syscall.Handle(handle), result == syscall.ERROR_ALREADY_EXISTS, nil
}
