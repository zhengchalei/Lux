package domain

// summoner_search.go describes name-only Riot account matches before history selection.
// Each returned identity retains its full alias even when current-region profile
// metadata cannot be read. Search generations bind explicit confirmation to the
// observed candidate list, independently from the last loaded history profile.

// SummonerCandidate contains only observed public identity and current-region metadata.
type SummonerCandidate struct {
	PUUID     string `json:"puuid"`     // Stable returned player identity.
	Name      string `json:"name"`      // Full Riot alias including its tag.
	IconID    int    `json:"iconId"`    // Client profile icon asset identifier.
	Level     int    `json:"level"`     // Observed account level, zero when unavailable.
	Rank      string `json:"rank"`      // Observed rank or explicit unavailable label.
	Loading   bool   `json:"loading"`   // Current-region metadata remains pending.
	Available bool   `json:"available"` // Current-region summoner identity was verified.
	Reason    string `json:"reason"`    // Hover explanation for unavailable metadata.
}

// SummonerSearch preserves every alias returned by one name-only service request.
type SummonerSearch struct {
	ID         uint64              `json:"id"`         // Query generation required for confirmation.
	Name       string              `json:"name"`       // Exact submitted name before tag selection.
	Candidates []SummonerCandidate `json:"candidates"` // Ordered, deduplicated account matches.
}
