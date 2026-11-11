//go:build windows

package client

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"lux/internal/history"
)

// discover_windows.go locates the active League client's local HTTPS credentials.
// Native process enumeration reads only LeagueClientUx command lines and executable
// locations; it does not launch a shell or expose authentication in logs.
// Known installation lockfiles provide recovery when process command-line access
// is restricted by the client's integrity level. Those files use the documented
// five-field HTTPS record and are read without modification.
//
// A successful discovery returns process-local history.Access values. Transport
// code binds these credentials to loopback only, while callers report sanitized
// connection explanations when the current process cannot read the client.

// Native discovery procedures remain local to this package and are used only for querying LeagueClientUx process metadata.
var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	ntdll                     = syscall.NewLazyDLL("ntdll.dll")
	createToolhelp32Snapshot  = kernel32.NewProc("CreateToolhelp32Snapshot")
	process32First            = kernel32.NewProc("Process32FirstW")
	process32Next             = kernel32.NewProc("Process32NextW")
	queryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
	ntQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
	credentialFlag            = regexp.MustCompile(`--(app-port|remoting-auth-token|rso-platform-id|rso_platform_id)=([^\s"]+)`)
	riotCredentialFlag        = regexp.MustCompile(`--(riotclient-app-port|riotclient-auth-token)=([^\s"]+)`)
)

// processEntry mirrors the native PROCESSENTRY32W layout for read-only enumeration.
type processEntry struct {
	Size            uint32      // Native structure byte count.
	Usage           uint32      // Native unused reference count.
	ProcessID       uint32      // Enumerated process numeric identifier.
	DefaultHeapID   uintptr     // Native reserved heap identifier.
	ModuleID        uint32      // Native reserved module identifier.
	Threads         uint32      // Native process thread count.
	ParentProcessID uint32      // Enumerated parent process identifier.
	Priority        int32       // Native process base priority.
	Flags           uint32      // Native reserved process flags.
	ExeFile         [260]uint16 // Executable filename UTF-16 buffer.
}

// parseArguments extracts exact League flags without confusing Riot service credentials.
// A usable access pair requires an explicit positive port and nonempty token.
// Optional platform flags remain separate from region until client metadata arrives.
func parseArguments(command string) (history.Access, bool) {
	var access history.Access
	for _, match := range credentialFlag.FindAllStringSubmatch(command, -1) {
		switch match[1] {
		case "app-port":
			access.Port, _ = strconv.Atoi(match[2])
		case "remoting-auth-token":
			access.Token = match[2]
		case "rso-platform-id", "rso_platform_id":
			access.Platform = match[2]
		}
	}
	return access, access.Port > 0 && access.Port <= 65535 && access.Token != ""
}

// parseRiotArguments keeps Riot account-service credentials separate from LCU access.
func parseRiotArguments(command string) (riotAccess, bool) {
	var access riotAccess
	for _, match := range riotCredentialFlag.FindAllStringSubmatch(command, -1) {
		if match[1] == "riotclient-app-port" {
			access.port, _ = strconv.Atoi(match[2])
		} else {
			access.token = match[2]
		}
	}
	return access, access.port > 0 && access.port <= 65535 && access.token != ""
}

// discoverRiot reads account-service flags from the process owning this LCU pair.
// Matching both credentials prevents an unrelated or restarted client process from
// supplying alias identities to the currently authenticated account generation.
func discoverRiot(league history.Access) (riotAccess, error) {
	snapshot, _, _ := createToolhelp32Snapshot.Call(2, 0)
	if snapshot != ^uintptr(0) {
		defer syscall.CloseHandle(syscall.Handle(snapshot))
		entry := processEntry{Size: uint32(unsafe.Sizeof(processEntry{}))}
		more, _, _ := process32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		for more != 0 {
			if strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), "LeagueClientUx.exe") {
				_, command := processMetadata(entry.ProcessID)
				if current, ok := parseArguments(command); ok && current.Port == league.Port && current.Token == league.Token {
					if riot, ok := parseRiotArguments(command); ok {
						return riot, nil
					}
				}
			}
			more, _, _ = process32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		}
	}
	return riotAccess{}, errors.New("同名搜索服务暂不可读，请使用名称#编号，或以客户端相同权限运行 Lux")
}

// parseLockfile validates the documented HTTPS credential record before using its port.
// Invalid field counts, empty credentials, unsupported protocols, and invalid
// TCP ports return false without copying their contents to any diagnostic output.
func parseLockfile(raw string) (history.Access, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 5 || parts[4] != "https" || parts[3] == "" {
		return history.Access{}, false
	}
	port, err := strconv.Atoi(parts[2])
	return history.Access{Port: port, Token: parts[3]}, err == nil && port > 0 && port <= 65535
}

// processMetadata reads one process's image path and command line without mutation.
// Access failure returns empty observations so discovery can try its lockfile
// alternatives. Every successfully opened native handle is closed before return.
func processMetadata(id uint32) (string, string) {
	handle, err := syscall.OpenProcess(0x1000, false, id)
	if err != nil {
		return "", ""
	}
	defer syscall.CloseHandle(handle)
	pathBuffer := make([]uint16, 32768)
	pathLength := uint32(len(pathBuffer))
	result, _, _ := queryFullProcessImageName.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&pathBuffer[0])), uintptr(unsafe.Pointer(&pathLength)))
	path := ""
	if result != 0 {
		path = syscall.UTF16ToString(pathBuffer[:pathLength])
	}
	var needed uint32
	// ProcessCommandLineInformation reports a UNICODE_STRING followed by its text, avoiding PEB traversal and cross-process pointer dereferences.
	_, _, _ = ntQueryInformationProcess.Call(uintptr(handle), 60, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed < 16 || needed > 1<<20 {
		return path, ""
	}
	buffer := make([]byte, needed)
	status, _, _ := ntQueryInformationProcess.Call(uintptr(handle), 60, uintptr(unsafe.Pointer(&buffer[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)))
	if status != 0 {
		return path, ""
	}
	length := int(binary.LittleEndian.Uint16(buffer[:2]))
	base := uintptr(unsafe.Pointer(&buffer[0]))
	pointer := uintptr(binary.LittleEndian.Uint64(buffer[8:16]))
	if pointer < base || pointer-base > uintptr(len(buffer)) || uintptr(length) > uintptr(len(buffer))-(pointer-base) {
		return path, ""
	}
	text := buffer[pointer-base : pointer-base+uintptr(length)]
	utf16 := make([]uint16, len(text)/2)
	for index := range utf16 {
		utf16[index] = binary.LittleEndian.Uint16(text[index*2:])
	}
	return path, syscall.UTF16ToString(utf16)
}

// discover locates readable client authentication through native metadata and lockfiles.
// No elevation is requested here. A higher-integrity client may require the user
// to launch Lux with matching permissions before its command line becomes readable.
func discover() (history.Access, error) {
	candidates := []string{os.Getenv("LUX_LCU_LOCKFILE")}
	found := false
	snapshot, _, _ := createToolhelp32Snapshot.Call(2, 0)
	if snapshot != ^uintptr(0) {
		defer syscall.CloseHandle(syscall.Handle(snapshot))
		entry := processEntry{Size: uint32(unsafe.Sizeof(processEntry{}))}
		first, _, _ := process32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		for first != 0 {
			if strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), "LeagueClientUx.exe") {
				found = true
				path, command := processMetadata(entry.ProcessID)
				if access, ok := parseArguments(command); ok {
					return access, nil
				}
				if path != "" {
					candidates = append(candidates, filepath.Join(filepath.Dir(path), "lockfile"))
				}
			}
			first, _, _ = process32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		}
	}
	// Installation fallbacks cover the user's configured client and Riot defaults when elevated process paths are unavailable to this process.
	candidates = append(candidates, `C:\WeGameApps\英雄联盟\LeagueClient\lockfile`, `C:\Riot Games\League of Legends\lockfile`)
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if raw, err := os.ReadFile(path); err == nil {
			if access, ok := parseLockfile(string(raw)); ok {
				return access, nil
			}
		}
	}
	if found {
		return history.Access{}, errors.New("检测到客户端但连接参数不可读；请以与客户端相同权限运行 Lux")
	}
	return history.Access{}, errors.New("等待英雄联盟客户端，启动后将自动连接")
}
