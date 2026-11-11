package main

import "testing"

// draft_panel_windows_test.go checks physical companion placement across monitor layouts.
// Cases cover right attachment, left fallback, negative monitor origins, and
// constrained work areas without moving or resizing the user's actual windows.

// TestCompanionPlacementAcrossMonitors reproduces client windows beside physical screen boundaries.
// The panel must stay reachable inside the work area and prefer a free right edge.
func TestCompanionPlacementAcrossMonitors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		client, work pixelRect
		wantX        int32
	}{
		{"right", pixelRect{100, 100, 1380, 820}, pixelRect{0, 0, 1920, 1040}, 1388},
		{"left", pixelRect{500, 100, 1780, 820}, pixelRect{0, 0, 1920, 1040}, 148},
		{"negative", pixelRect{-1800, 80, -520, 800}, pixelRect{-1920, 0, 0, 1040}, -512},
		{"overlap", pixelRect{0, 0, 1280, 720}, pixelRect{0, 0, 1280, 680}, 936},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := companionPlacement(tc.client, tc.work, 344, 680, 8)
			if got.Left != tc.wantX || got.Left < tc.work.Left || got.Right > tc.work.Right || got.Top < tc.work.Top || got.Bottom > tc.work.Bottom {
				t.Fatalf("invalid placement %#v", got)
			}
		})
	}
}
