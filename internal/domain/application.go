package domain

// application.go defines desktop lifetime preferences and the independent match
// acceptance switch shared by the renderer and client runtime. These settings
// contain no credentials or champion-selection permission and survive application
// restarts so background operation follows the user's saved choices.

// ApplicationSettings stores desktop lifetime and independent ReadyCheck acceptance preferences.
type ApplicationSettings struct {
	CloseToTray        bool `json:"closeToTray"`        // Release main window on close.
	AutoAccept         bool `json:"autoAccept"`         // Accept newly observed ready checks.
	AcceptDelaySeconds int  `json:"acceptDelaySeconds"` // Acceptance delay in seconds.
}
