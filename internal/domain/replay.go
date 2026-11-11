package domain

// replay.go defines the compact client replay observation consumed by match details.
// Download availability and playback permission remain separate because an existing
// replay can be ready while the client is playing, patching, or selecting heroes.

// Replay describes one recorded game's client-managed download and playback state.
type Replay struct {
	GameID uint64 `json:"gameId"` // Selected recorded game identifier.
	State  string `json:"state"`  // Client state or unavailable.
	Label  string `json:"label"`  // Concise localized action status.
	Reason string `json:"reason"` // Context for hover explanations.
	// Progress is present only for downloading observations between zero and one
	// hundred; unrelated client counters are never displayed as download percentages.
	Progress    *int `json:"progress"`
	CanDownload bool `json:"canDownload"` // Fresh metadata permits download.
	CanWatch    bool `json:"canWatch"`    // Ready file and idle client.
}
