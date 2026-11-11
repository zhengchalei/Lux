//go:build !windows

package client

import (
	"errors"
	"lux/internal/history"
)

// discover_other.go keeps platform-independent client tests buildable off Windows.
// Lux ships only a Windows desktop application, so other platforms receive an
// explicit discovery error instead of attempting unsupported process inspection.

// discover reports the unsupported desktop platform without accessing process metadata.
func discover() (history.Access, error) {
	return history.Access{}, errors.New("Lux 客户端连接仅支持 Windows")
}

// discoverRiot reports unsupported native discovery without returning unrelated credentials.
func discoverRiot(history.Access) (riotAccess, error) {
	return riotAccess{}, errors.New("同名搜索仅支持 Windows 客户端")
}
