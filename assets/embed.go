package assets

import "embed"

// embed.go embeds Lux's existing public catalog and champion portraits for Go.
// Player records and local client authentication are not assets. Both the desktop
// frontend and public-data service consume these bundled files without requiring
// a network connection during application startup.

// Files supplies the public snapshots and portraits shared by the Go services and webview.
//
//go:embed champions.json aram.json aram-balance.json mayhem-balance.json portraits/*.jpg
var Files embed.FS
