package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// worker_windows.go replaces Lux only after its original Windows process exits.
// A copied executable runs this worker before ordinary startup and single-instance
// admission. The worker opens the parent process, checks its executable identity,
// and prepares a verified replacement beside that executable before acknowledging
// readiness. It never kills the parent or modifies it while it is running.
//
// Placement uses a temporary file on the target volume. The old executable is
// renamed to a backup before replacement, and a failed rename or requested launch
// restores that backup. A failed handshake leaves the desktop alive. Worker
// results survive in the per-user updater directory for the next launch, while
// ordinary quitting installs without unexpectedly opening another main window.

// Native procedures provide a worker readiness signal and exact parent executable identification without polling command lines.
var (
	workerKernel = syscall.NewLazyDLL("kernel32.dll")
	createEvent  = workerKernel.NewProc("CreateEventW")
	openEvent    = workerKernel.NewProc("OpenEventW")
	setEvent     = workerKernel.NewProc("SetEvent")
	queryImage   = workerKernel.NewProc("QueryFullProcessImageNameW")
)

// workerFlag selects the copied executable's internal replacement entrypoint before any ordinary desktop startup work begins.
const workerFlag = "--lux-update-worker"

// installPlan binds one verified candidate to the process that is actually exiting.
type installPlan struct {
	// Parent and Target identify one exiting process, while Candidate and Digest
	// describe its verified replacement. Ready transfers installation admission only
	// after the worker validates that identity and prepares writable local placement.
	Parent    uint32 `json:"parent"`    // Original desktop process identifier.
	Target    string `json:"target"`    // Original absolute executable path.
	Candidate string `json:"candidate"` // Verified pending executable path.
	Digest    string `json:"digest"`    // Expected public GitHub SHA-256 digest.
	Ready     string `json:"ready"`     // Unique native handshake event name.
	Result    string `json:"result"`    // Persistent installation result path.
	Restart   bool   `json:"restart"`   // Explicit reopen request after replacement.
}

// copyFile creates a new owned file and flushes its complete contents to disk.
// Existing destinations are rejected rather than overwritten. The caller owns
// cleanup of a failed partial copy and may rename a successful result afterwards.
func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	defer output.Close()
	if _, err = io.Copy(output, input); err != nil {
		return err
	}
	if err = output.Sync(); err != nil {
		return err
	}
	return output.Close()
}

// launchWorker copies this running application's known installer and awaits readiness.
// The original executable remains untouched until its process exits. Readiness
// requires successful identity checks and target-volume staging; a timeout kills
// only this newly launched helper and returns without requesting desktop exit.
// The worker is copied from the current binary, so its protocol matches this
// caller even when a future downloaded executable changes its startup behavior.
// Only the parent decides when to quit, and successful preparation never forces
// termination of a still-running application or an active game.
func launchWorker(target string, pending candidate, restart bool, result string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	worker := filepath.Join(pending.Directory, "worker.exe")
	os.Remove(worker)
	if err = copyFile(self, worker); err != nil {
		return err
	}
	plan := installPlan{Parent: uint32(os.Getpid()), Target: target, Candidate: pending.Path, Digest: pending.Digest, Result: result, Restart: restart}
	plan.Ready = fmt.Sprintf("Local\\Lux.Update.%d.%s", os.Getpid(), filepath.Base(pending.Directory))
	name, _ := syscall.UTF16PtrFromString(plan.Ready)
	event, _, callErr := createEvent.Call(0, 0, 0, uintptr(unsafe.Pointer(name)))
	if event == 0 {
		return callErr
	}
	defer syscall.CloseHandle(syscall.Handle(event))
	raw, _ := json.Marshal(plan)
	planPath := filepath.Join(pending.Directory, "plan.json")
	if err = os.WriteFile(planPath, raw, 0600); err != nil {
		return err
	}
	command := exec.Command(worker, workerFlag, planPath)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err = command.Start(); err != nil {
		return err
	}
	response, err := syscall.WaitForSingleObject(syscall.Handle(event), 5000)
	if err != nil || response != syscall.WAIT_OBJECT_0 {
		// This worker has not acknowledged a usable replacement. Stop its
		// admission so the still-running desktop retains sole executable ownership.
		command.Process.Kill()
		command.Wait()
		return errors.New("更新准备失败；请确认程序目录可写，稍后重试")
	}
	return command.Process.Release()
}

// RunWorker handles the private installer entrypoint before any application service starts.
// Return true whenever the argument selects worker mode, including failures, so
// malformed internal arguments cannot accidentally open an ordinary desktop window.
func RunWorker(args []string) bool {
	if len(args) == 0 || args[0] != workerFlag {
		return false
	}
	if len(args) != 2 {
		return true
	}
	raw, err := os.ReadFile(args[1])
	var plan installPlan
	if err != nil || len(raw) > 64*1024 || json.Unmarshal(raw, &plan) != nil {
		return true
	}
	err = applyPlan(plan)
	message := ""
	if err != nil {
		message = err.Error()
	}
	result, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{message})
	_ = os.WriteFile(plan.Result, result, 0600)
	return true
}

// applyPlan verifies parent ownership, stages locally, then waits for an actual exit.
// This function runs only inside the copied worker. It signals readiness after
// all pre-exit validation succeeds and waits at most thirty seconds for the
// parent to finish. The old file survives every preparation failure. Replacement
// begins only after that process handle signals termination, avoiding PID reuse
// and an unrelated process being mistaken for the application being updated.
func applyPlan(plan installPlan) error {
	if !filepath.IsAbs(plan.Target) || !filepath.IsAbs(plan.Candidate) || strings.EqualFold(plan.Target, plan.Candidate) {
		return errors.New("更新文件路径无效")
	}
	// Query rights verify the exact executable; synchronization waits on this
	// process object rather than a numeric identifier that Windows can reuse.
	parent, err := syscall.OpenProcess(0x00100000|0x1000, false, plan.Parent)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(parent)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if result, _, err := queryImage.Call(uintptr(parent), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size))); result == 0 {
		return err
	}
	if !strings.EqualFold(filepath.Clean(syscall.UTF16ToString(buffer[:size])), filepath.Clean(plan.Target)) {
		return errors.New("更新目标与原程序不一致")
	}
	if err = verifyDigest(plan.Candidate, plan.Digest); err != nil {
		return err
	}
	if err = verifyExecutable(plan.Candidate); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(plan.Target), ".lux-update-*.exe")
	if err != nil {
		return err
	}
	prepared := file.Name()
	file.Close()
	os.Remove(prepared)
	defer os.Remove(prepared)
	if err = copyFile(plan.Candidate, prepared); err != nil {
		return err
	}
	if err = verifyDigest(prepared, plan.Digest); err != nil {
		return err
	}
	name, _ := syscall.UTF16PtrFromString(plan.Ready)
	event, _, callErr := openEvent.Call(0x0002, 0, uintptr(unsafe.Pointer(name)))
	if event == 0 {
		return callErr
	}
	defer syscall.CloseHandle(syscall.Handle(event))
	if result, _, err := setEvent.Call(event); result == 0 {
		return err
	}
	response, err := syscall.WaitForSingleObject(parent, 30000)
	if err != nil || response != syscall.WAIT_OBJECT_0 {
		return errors.New("原程序未退出，保留当前版本")
	}
	if err = replaceExecutable(plan.Target, prepared, plan.Restart); err != nil {
		return err
	}
	// Only successful installation consumes the pending download; the running
	// worker itself is removed by a later application's cache cleanup.
	os.Remove(plan.Candidate)
	return nil
}

// verifyDigest checks a local staged file again before executable replacement begins.
func verifyDigest(path, digest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if digest != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
		return errors.New("更新文件 SHA-256 校验失败")
	}
	return nil
}

// replaceExecutable swaps a same-volume candidate and rolls back failed replacement or restart.
// The original process must have exited. A unique backup keeps the old binary
// available until the replacement and requested launch both succeed. Temporary
// filesystem locks receive a short bounded retry rather than deleting either
// executable. Rollback errors identify the retained backup so the old program
// remains recoverable even when the filesystem refuses a restoring rename.
func replaceExecutable(target, prepared string, restart bool) error {
	backup := prepared + ".previous"
	var err error
	// Antivirus scans may briefly retain the exiting image. Retry for only
	// three seconds so an unrelated lock cannot leave a helper waiting indefinitely.
	for attempt := 0; attempt < 30; attempt++ {
		err = os.Rename(target, backup)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%v；旧版本保留在 %s", cause, backup)
		}
		if err := os.Rename(backup, target); err != nil {
			return fmt.Errorf("%v；旧版本保留在 %s", cause, backup)
		}
		return cause
	}
	if err = os.Rename(prepared, target); err != nil {
		return rollback(err)
	}
	if restart {
		command := exec.Command(target)
		command.Dir = filepath.Dir(target)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err = command.Start(); err != nil {
			return rollback(err)
		}
		_ = command.Process.Release()
	}
	return os.Remove(backup)
}
