//go:build !windows

package review

import "errors"

// secret_other.go keeps review logic buildable without a plaintext credential fallback.
// Lux distributes Windows binaries only. Other systems can test keyless providers,
// but saving or restoring a secret requires the Windows user protection service.

// protectSecret rejects secret persistence outside the supported Windows application platform.
func protectSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return "", errors.New("密钥保护仅支持 Windows")
}

// unprotectSecret rejects encrypted credentials outside their supported Windows user context.
func unprotectSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return "", errors.New("密钥保护仅支持 Windows")
}
