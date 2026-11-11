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

// TestCompanionReopensOnNextMatch preserves dismissal and folding within one matching cycle.
// A later verified cycle re-enables the expanded companion without requiring the
// main window, while gameplay revokes an explicitly opened waiting window.
func TestCompanionReopensOnNextMatch(t *testing.T) {
	p := &draftPanel{status: DraftPanelStatus{Pinned: true}}
	status, _ := p.activity("1:1", false)
	if !status.Enabled || status.Collapsed {
		t.Fatal("new matching cycle did not open expanded")
	}
	p.Configure(false, true)
	if status, _ = p.activity("1:1", false); status.Enabled {
		t.Fatal("same cycle overrode dismissal")
	}
	p.activity("", false)
	if status, _ = p.activity("1:1", false); status.Enabled {
		t.Fatal("transient missing activity overrode dismissal")
	}
	if status, _ = p.activity("1:2", false); !status.Enabled {
		t.Fatal("next cycle retained dismissal")
	}
	p.Collapse(true)
	if status, _ = p.activity("1:2", false); !status.Collapsed {
		t.Fatal("ordinary observation overrode folding")
	}
	if status, _ = p.activity("1:3", false); !status.Enabled || status.Collapsed {
		t.Fatal("next cycle retained folded tab")
	}
	p.Configure(true, false)
	if _, manual := p.activity("", true); manual {
		t.Fatal("gameplay retained waiting override")
	}
}
