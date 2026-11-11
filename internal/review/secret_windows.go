//go:build windows

package review

import (
	"encoding/base64"
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

// secret_windows.go encrypts saved provider credentials with Windows user protection.
// DPAPI binds ciphertext to the current Windows account. The settings file stores
// only that ciphertext, while plaintext remains confined to provider configuration
// and authenticated request construction rather than browser-visible application snapshots.

// Native secret procedures encrypt local user data and release Windows-owned output buffers.
var (
	secretCrypt   = syscall.NewLazyDLL("crypt32.dll")
	protectData   = secretCrypt.NewProc("CryptProtectData")
	unprotectData = secretCrypt.NewProc("CryptUnprotectData")
	secretFree    = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
)

// secretBlob mirrors the Windows DATA_BLOB byte count and native buffer pointer.
type secretBlob struct {
	Size uint32 // Native payload byte count.
	Data *byte  // Native payload buffer address.
}

// transformSecret copies a DPAPI result before freeing its Windows-owned buffer.
// The input remains alive throughout the native call. Interactive recovery prompts
// are disabled so background application startup cannot open a credential dialog.
func transformSecret(raw []byte, procedure *syscall.LazyProc) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	input := secretBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var output secretBlob
	ok, _, _ := procedure.Call(uintptr(unsafe.Pointer(&input)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&output)))
	runtime.KeepAlive(raw)
	if ok == 0 {
		return nil, errors.New("Windows 无法读取或保护 AI 密钥，请重新填写")
	}
	defer secretFree.Call(uintptr(unsafe.Pointer(output.Data)))
	return append([]byte{}, unsafe.Slice(output.Data, output.Size)...), nil
}

// protectSecret encodes Windows-protected ciphertext for the local preference file.
func protectSecret(value string) (string, error) {
	raw, err := transformSecret([]byte(value), protectData)
	return base64.StdEncoding.EncodeToString(raw), err
}

// unprotectSecret decrypts this Windows user's saved provider credential in memory.
func unprotectSecret(value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", errors.New("保存的 AI 密钥不可读，请重新填写")
	}
	decoded, err := transformSecret(raw, unprotectData)
	return string(decoded), err
}
