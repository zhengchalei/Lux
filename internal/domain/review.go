package domain

// review.go defines observed timeline facts and evidence-linked coaching responses.
// Participant slots are stable within one game; timestamps retain client milliseconds.
// Missing measurements remain nil so neither charts nor model prompts can turn
// an unavailable observation into a measured zero or a fabricated advantage.

// TimelinePlayer identifies one recorded participant without exposing authentication or persistent identifiers.
type TimelinePlayer struct {
	ID           int    `json:"id"`           // Recorded participant slot identifier.
	Champion     int    `json:"champion"`     // Official champion numeric identifier.
	ChampionName string `json:"championName"` // Public localized champion catalog name.
	Name         string `json:"name"`         // Observed public display name.
	Team         int    `json:"team"`         // Recorded team numeric identifier.
	Role         string `json:"role"`         // Explicitly recorded role, empty when ambiguous.
}

// TimelineStats preserves one participant's sampled measurements with independent missing values.
type TimelineStats struct {
	ID    int  `json:"id"`    // Sampled participant slot identifier.
	Gold  *int `json:"gold"`  // Cumulative observed earned gold.
	CS    *int `json:"cs"`    // Observed lane and jungle minions.
	XP    *int `json:"xp"`    // Cumulative observed experience points.
	Level *int `json:"level"` // Observed current champion level.
	X     *int `json:"x"`     // Sampled map horizontal coordinate.
	Y     *int `json:"y"`     // Sampled map vertical coordinate.
}

// TimelineFrame associates simultaneous player observations with their actual sampling instant.
type TimelineFrame struct {
	At      int64           `json:"at"`      // Elapsed game time, milliseconds.
	Players []TimelineStats `json:"players"` // Recorded measurements by participant slot.
}

// TimelineEvent describes an observed combat or objective event with stable citation identity.
type TimelineEvent struct {
	ID      string `json:"id"`      // Stable normalized evidence reference.
	At      int64  `json:"at"`      // Elapsed game time, milliseconds.
	Kind    string `json:"kind"`    // Client event type identifier.
	Actor   int    `json:"actor"`   // Recorded killer participant identifier.
	Target  int    `json:"target"`  // Recorded victim participant identifier.
	Assists []int  `json:"assists"` // Explicitly recorded assisting participant slots.
	Team    int    `json:"team"`    // Verified credited team, zero when unknown.
	Label   string `json:"label"`   // Localized observation without tactical inference.
}

// Timeline retains one game's observed samples and public event evidence.
type Timeline struct {
	GameID            uint64           `json:"gameId"`            // Recorded game numeric identifier.
	MapID             int              `json:"mapId"`             // Client map numeric identifier.
	QueueID           int              `json:"queueId"`           // Recorded matchmaking queue identifier.
	OwnID             int              `json:"ownId"`             // Explicitly identified viewed participant slot.
	SuggestedOpponent int              `json:"suggestedOpponent"` // Unique explicit role match, otherwise zero.
	SampleSeconds     int              `json:"sampleSeconds"`     // Median observed positive sample interval.
	Version           string           `json:"version"`           // Recorded game patch label.
	Players           []TimelinePlayer `json:"players"`           // Public roster with explicit roles.
	Frames            []TimelineFrame  `json:"frames"`            // Chronologically ordered observed samples.
	Events            []TimelineEvent  `json:"events"`            // Chronologically ordered supported events.
}

// ReviewWindow compares observed boundaries without extrapolating unavailable player samples.
type ReviewWindow struct {
	ID         string          `json:"id"`         // Stable phase evidence identifier.
	Start      int64           `json:"start"`      // First observed boundary, milliseconds.
	End        int64           `json:"end"`        // Last observed boundary, milliseconds.
	Tone       string          `json:"tone"`       // Economic trend: gain, loss, or neutral.
	OwnStart   TimelineStats   `json:"ownStart"`   // Viewed participant opening measurements.
	OwnEnd     TimelineStats   `json:"ownEnd"`     // Viewed participant closing measurements.
	OtherStart TimelineStats   `json:"otherStart"` // Opposing participant opening measurements.
	OtherEnd   TimelineStats   `json:"otherEnd"`   // Opposing participant closing measurements.
	GoldTrend  *int            `json:"goldTrend"`  // Change in viewed-minus-opponent gold.
	GoldDiff   *int            `json:"goldDiff"`   // Closing viewed-minus-opponent gold difference.
	CSDiff     *int            `json:"csDiff"`     // Closing viewed-minus-opponent minion difference.
	OwnKDA     [3]int          `json:"ownKda"`     // Observed interval kills, deaths, assists.
	OtherKDA   [3]int          `json:"otherKda"`   // Observed interval kills, deaths, assists.
	Events     []TimelineEvent `json:"events"`     // All observed combat and objective events in interval.
}

// TimelineReview combines reusable timeline observations with one explicit opponent comparison.
type TimelineReview struct {
	Timeline Timeline       `json:"timeline"` // Observed client game facts.
	Opponent int            `json:"opponent"` // Selected opposing participant slot.
	Windows  []ReviewWindow `json:"windows"`  // Nonoverlapping observed comparison intervals.
}

// AISettings returns provider configuration status while never returning a saved secret.
type AISettings struct {
	BaseURL string `json:"baseUrl"` // User-configured compatible API base address.
	Model   string `json:"model"`   // User-configured provider model identifier.
	HasKey  bool   `json:"hasKey"`  // Saved credential is currently readable.
	Note    string `json:"note"`    // Local configuration recovery explanation.
}

// AISettingsInput changes provider preferences; a blank key preserves the existing credential.
type AISettingsInput struct {
	BaseURL  string `json:"baseUrl"`  // Compatible API base address input.
	Model    string `json:"model"`    // Provider model identifier input.
	APIKey   string `json:"apiKey"`   // Replacement secret, blank to preserve.
	ClearKey bool   `json:"clearKey"` // Explicitly remove the saved credential.
}

// AIInsight binds one coaching judgment to an observed phase and existing evidence.
type AIInsight struct {
	WindowID    string   `json:"windowId"`    // Existing phase identifier under review.
	Verdict     string   `json:"verdict"`     // good, review, or neutral interpretation.
	Title       string   `json:"title"`       // Concise coach finding label.
	Reason      string   `json:"reason"`      // Evidence-based interpretation of observed play.
	Opponent    string   `json:"opponent"`    // Opponent behavior supported by supplied evidence.
	Advice      string   `json:"advice"`      // Conditional, actionable improvement or reinforcement.
	EvidenceIDs []string `json:"evidenceIds"` // Verified phase or event citation identifiers.
}

// AIReview contains a validated provider response rather than a local heuristic labeled AI.
type AIReview struct {
	Summary  string      `json:"summary"`  // Concise overall retrospective interpretation.
	Insights []AIInsight `json:"insights"` // Evidence-linked phase interpretations.
	Goals    []string    `json:"goals"`    // Bounded next-game practice suggestions.
	Model    string      `json:"model"`    // Provider model used for this response.
	Created  string      `json:"created"`  // Successful response timestamp in UTC.
}
