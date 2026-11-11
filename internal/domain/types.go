package domain

import "fmt"

// types.go defines the public state exchanged between Go services and Vue.
// These observations contain display data only; local client credentials belong
// to transport packages and never become fields of a browser-visible snapshot.

// Mode identifies independent gameplay pools and public statistics sources.
type Mode string

// Gameplay constants keep ordinary ARAM and Mayhem statistics independent in every consumer.
const (
	Ranked Mode = "ranked"
	Aram   Mode = "aram"
	Mayhem Mode = "mayhem"
)

// Adjustment describes one source-reported gameplay change and its benefit direction.
type Adjustment struct {
	Label   string `json:"label"`   // Localized affected gameplay property.
	Display string `json:"display"` // Signed source value presentation.
	Helpful bool   `json:"helpful"` // Benefit after incoming-damage reversal.
}

// Statistics groups observations sharing a gameplay mode and source context.
type Statistics struct {
	Tier        string       `json:"tier"`        // Source strength category label.
	WinRate     *float64     `json:"winRate"`     // Percentage; nil means unavailable.
	Samples     int          `json:"samples"`     // Observed source match count.
	Adjustments []Adjustment `json:"adjustments"` // Nil means adjustments unavailable.
	Source      string       `json:"source"`      // Public source version context.
}

// Champion describes one public catalog entry with separate mode observations.
type Champion struct {
	ID     int        `json:"id"`     // Official numeric champion identifier.
	Name   string     `json:"name"`   // Localized champion personal name.
	Title  string     `json:"title"`  // Localized champion display title.
	Alias  string     `json:"alias"`  // Official static asset stem.
	Roles  []string   `json:"roles"`  // Source general gameplay roles.
	Aram   Statistics `json:"aram"`   // Ordinary ARAM public observations.
	Mayhem Statistics `json:"mayhem"` // Mayhem independent public observations.
}

// Database contains the public catalog and its visible update context.
type Database struct {
	Champions    []Champion `json:"champions"`    // Stable public champion catalog.
	Patch        string     `json:"patch"`        // Public source patch label.
	Updated      string     `json:"updated"`      // Successful public observation timestamp.
	SourceStatus string     `json:"sourceStatus"` // Current public refresh explanation.
}

// Champion resolves a catalog entry or a labeled unknown identifier.
// Missing catalog entries retain their numeric identifier so views can show an
// unavailable portrait without silently substituting another champion's name or statistics.
func (d Database) Champion(id int) Champion {
	for _, c := range d.Champions {
		if c.ID == id {
			return c
		}
	}
	// Retain an unknown identifier in its label so missing catalog data cannot masquerade as a different valid champion.
	return Champion{ID: id, Name: fmt.Sprintf("英雄 %d", id)}
}

// Account identifies the client-visible player and current game server.
type Account struct {
	PUUID    string `json:"puuid"`    // Client-visible account persistent identifier.
	Name     string `json:"name"`     // Returned public Riot ID.
	Region   string `json:"region"`   // Client game region code.
	Platform string `json:"platform"` // Client game server code.
	Level    int    `json:"level"`    // Returned summoner account level.
}

// Member retains a friendly intention or publicly locked opposing champion.
type Member struct {
	Cell     int    `json:"cell"`     // Session-local participant slot identifier.
	Champion int    `json:"champion"` // Public or friendly intended champion.
	Position string `json:"position"` // Public assigned gameplay position.
	Locked   bool   `json:"locked"`   // Completed selection action flag.
	Local    bool   `json:"local"`    // Slot belongs to current account.
}

// Room contains public draft state and its actual selectable pool.
type Room struct {
	SessionKey string   `json:"sessionKey"` // Opaque selection session identity.
	CanSwap    bool     `json:"canSwap"`    // Client permits replacement bench swaps.
	CanChoose  bool     `json:"canChoose"`  // Personal preselection accepts manual choices.
	Choices    []int    `json:"choices"`    // Personal preselection champion identifiers.
	Bench      []int    `json:"bench"`      // Shared replacement bench champion identifiers.
	SkinID     int      `json:"skinId"`     // Observed local selected skin identifier.
	Mode       Mode     `json:"mode"`       // Identified current gameplay mode.
	QueueID    int      `json:"queueId"`    // Exact client matchmaking queue.
	Allies     []Member `json:"allies"`     // Friendly public selection observations.
	Enemies    []Member `json:"enemies"`    // Opposing publicly locked observations.
	Available  []int    `json:"available"`  // Client reported selectable champion pool.
	Banned     []int    `json:"banned"`     // Current publicly banned champion identifiers.
	Current    int      `json:"current"`    // Local player's current champion.
}

// DraftSkin describes one currently usable skin returned by the client carousel.
type DraftSkin struct {
	ID   int    `json:"id"`   // Official numeric skin identifier.
	Name string `json:"name"` // Client localized skin name.
	Base bool   `json:"base"` // Original champion appearance flag.
}

// DraftSkins binds usable appearances to one current champion and selection session.
type DraftSkins struct {
	SessionKey string      `json:"sessionKey"` // Selection session owning these appearances.
	ChampionID int         `json:"championId"` // Actual current champion identifier.
	Selected   int         `json:"selected"`   // Observed selected skin identifier.
	Enabled    bool        `json:"enabled"`    // Client currently permits skin changes.
	Skins      []DraftSkin `json:"skins"`      // Currently usable client carousel entries.
}

// DraftPanelSnapshot omits historical player records from the compact companion window.
type DraftPanelSnapshot struct {
	Revision  uint64   `json:"revision"`  // Runtime observation publication stamp.
	Connected bool     `json:"connected"` // Local client authentication availability.
	Phase     string   `json:"phase"`     // Observed client gameflow phase.
	QueueID   int      `json:"queueId"`   // Verified matchmaking or selection queue.
	Room      *Room    `json:"room"`      // Current public selection observations.
	Database  Database `json:"database"`  // Mode strength and champion catalog.
}

// Player stores participant combat values in one match's local perspective.
type Player struct {
	PUUID       string `json:"puuid"`       // Public recorded account identifier, empty when unavailable.
	ChampionID  int    `json:"championId"`  // Selected official champion identifier.
	Name        string `json:"name"`        // Recorded public Riot ID or neutral summoner label.
	Team        int    `json:"team"`        // Official match team identifier.
	Local       bool   `json:"self"`        // Participant owns this match perspective.
	Kills       int    `json:"kills"`       // Recorded participant kill count.
	Deaths      int    `json:"deaths"`      // Recorded participant death count.
	Assists     int    `json:"assists"`     // Recorded participant assist count.
	Damage      int    `json:"damage"`      // Recorded champion damage total.
	Gold        int    `json:"gold"`        // Recorded earned gold total.
	Taken       int    `json:"taken"`       // Recorded incoming damage total.
	Heal        int    `json:"heal"`        // Recorded healing done total.
	TowerDamage int    `json:"towerDamage"` // Recorded enemy turret damage.
}

// Match retains raw combat observations and independent missing-statistic flags.
type Match struct {
	ID             uint64   `json:"id"`             // Stable numeric match identifier.
	ChampionID     int      `json:"championId"`     // Perspective player's selected champion.
	Name           string   `json:"name"`           // Localized perspective champion name.
	Queue          string   `json:"queue"`          // Localized gameplay queue label.
	QueueID        int      `json:"queueId"`        // Exact numeric matchmaking queue.
	Win            bool     `json:"win"`            // Explicitly observed player victory.
	Kills          int      `json:"kills"`          // Perspective player's kill count.
	Deaths         int      `json:"deaths"`         // Perspective player's death count.
	Assists        int      `json:"assists"`        // Perspective player's assist count.
	CS             int      `json:"cs"`             // Lane and jungle minions killed.
	Damage         int      `json:"damage"`         // Perspective player's champion damage.
	Gold           int      `json:"gold"`           // Perspective player's earned gold.
	Duration       int      `json:"duration"`       // Match duration in seconds.
	Created        int64    `json:"created"`        // Match creation Unix milliseconds.
	Team           int      `json:"team"`           // Perspective player's team identifier.
	Players        []Player `json:"players"`        // Available participant combat observations.
	Source         string   `json:"source"`         // Actual supplying transport label.
	GameType       string   `json:"gameType"`       // Source explicit gameplay type.
	GameVersion    string   `json:"gameVersion"`    // Recorded game executable version.
	PlatformID     string   `json:"platformId"`     // Recorded game server identifier.
	GameEnd        int64    `json:"gameEnd"`        // Recorded end Unix milliseconds.
	Remake         bool     `json:"remake"`         // Source marks aborted gameplay.
	MissingMetrics [5]bool  `json:"missingMetrics"` // Damage/gold/taken/healing/turret availability.
}

// Coverage separates requested depth, inspected records, and usable complete observations.
type Coverage struct {
	Source    string `json:"source"`    // Actual supplying history provider.
	Requested int    `json:"requested"` // Requested retained history depth.
	Fetched   int    `json:"fetched"`   // Retained unique record count.
	Scanned   int    `json:"scanned"`   // Inspected unique record count.
	Valid     int    `json:"valid"`     // Usable competitive record count.
	Complete  int    `json:"complete"`  // Valid records with both teams.
	QueueID   int    `json:"queueId"`   // Exact queue; zero includes all.
	Note      string `json:"note"`      // Partial coverage or fallback explanation.
}

// Contribution averages team shares while preserving independent metric coverage counts.
type Contribution struct {
	Games       int      `json:"games"`       // Valid complete-roster match count.
	DamageGames int      `json:"damageGames"` // Matches with known damage denominator.
	TakenGames  int      `json:"takenGames"`  // Matches with known incoming denominator.
	GoldGames   int      `json:"goldGames"`   // Matches with known economy denominator.
	DamageShare *float64 `json:"damageShare"` // Mean fraction; nil means unknown.
	TakenShare  *float64 `json:"takenShare"`  // Mean fraction; nil means unknown.
	GoldShare   *float64 `json:"goldShare"`   // Mean fraction; nil means unknown.
}

// HeroSummary describes one champion's contribution to an observed recent sample.
type HeroSummary struct {
	ChampionID int     `json:"championId"` // Observed official champion identifier.
	Games      int     `json:"games"`      // Usable champion observation count.
	Wins       int     `json:"wins"`       // Recorded champion victory count.
	WinRate    float64 `json:"winRate"`    // Observed victories as percentage.
	KDA        float64 `json:"kda"`        // Total kills/assists divided by deaths.
	Kills      float64 `json:"kills"`      // Average kills per usable match.
	Deaths     float64 `json:"deaths"`     // Average deaths per usable match.
	Assists    float64 `json:"assists"`    // Average assists per usable match.
}

// Summary precomputes combat, champion usage, and contribution for one sample scope.
type Summary struct {
	Games        int           `json:"games"`        // Usable observed match count.
	Wins         int           `json:"wins"`         // Recorded sample victory count.
	WinRate      float64       `json:"winRate"`      // Victories divided by games percentage.
	KDA          float64       `json:"kda"`          // Total kills/assists divided by deaths.
	Kills        float64       `json:"kills"`        // Average kills per usable match.
	Deaths       float64       `json:"deaths"`       // Average deaths per usable match.
	Assists      float64       `json:"assists"`      // Average assists per usable match.
	Recent       []Match       `json:"recent"`       // Latest ten usable match observations.
	Heroes       []HeroSummary `json:"heroes"`       // All observed heroes sorted by usage.
	Contribution Contribution  `json:"contribution"` // Independent known team-share observations.
}

// Profile binds normalized history and prepared summaries to one player identity.
type Profile struct {
	PUUID        string             `json:"puuid"`        // Queried client-visible account identifier.
	Name         string             `json:"name"`         // Returned public display name.
	Rank         string             `json:"rank"`         // Solo ranked display context.
	Captured     string             `json:"captured"`     // Successful read UTC timestamp.
	HistoryError string             `json:"historyError"` // Explicit history retrieval failure.
	Level        int                `json:"level"`        // Source summoner account level.
	IconID       int                `json:"iconId"`       // Source profile icon identifier, negative when unavailable.
	Requested    int                `json:"requested"`    // Requested bounded history depth.
	Matches      []Match            `json:"matches"`      // Identity-normalized raw match observations.
	Coverage     Coverage           `json:"coverage"`     // Actual source and sample coverage.
	Summary      Summary            `json:"summary"`      // All-mode prepared sample summary.
	Summaries    map[string]Summary `json:"summaries"`    // Broad modes and compact history-type aggregates.
}

// Combatant identifies a public roster member without inferring hidden information.
type Combatant struct {
	PUUID    string `json:"puuid"`    // Public player identifier, empty when hidden.
	Name     string `json:"name"`     // Public display name or unknown label.
	Position string `json:"position"` // Publicly assigned gameplay position.
	Champion int    `json:"champion"` // Publicly visible champion selection.
	Ally     bool   `json:"ally"`     // Member belongs to friendly side.
	Hidden   bool   `json:"hidden"`   // Client has not exposed identity.
	Locked   bool   `json:"locked"`   // Public completed selection state.
}

// Battle associates ten-player observations with one room or historical match.
type Battle struct {
	Key     string      `json:"key"`     // Stable roster association key.
	Source  string      `json:"source"`  // Live or historical provenance label.
	Mode    Mode        `json:"mode"`    // Identified gameplay statistics mode.
	QueueID int         `json:"queueId"` // Exact observed matchmaking queue.
	Players []Combatant `json:"players"` // Publicly exposed roster participants.
}

// Scout preserves a prior successful profile during independent player refreshes.
type Scout struct {
	Profile *Profile `json:"profile"` // Last successful observed player profile.
	Loading bool     `json:"loading"` // Current player pipeline remains active.
	Error   string   `json:"error"`   // Current sanitized retrieval failure explanation.
}

// AnalysisSettings configures requested history depth and current-mode summary selection.
type AnalysisSettings struct {
	MatchCount int  `json:"matchCount"` // Requested bounded analysis record count.
	SameMode   bool `json:"sameMode"`   // Restrict summaries to current battle mode.
}

// AutomationSettings stores draft rules independently from runtime permission to execute.
type AutomationSettings struct {
	AutoPick       bool   `json:"autoPick"`       // Choose permitted pick priority candidates.
	AutoBan        bool   `json:"autoBan"`        // Ban permitted ban priority candidates.
	Preselect      bool   `json:"preselect"`      // Hover during a pending local pick.
	LockPick       bool   `json:"lockPick"`       // Complete currently active local picks.
	ProtectAllies  bool   `json:"protectAllies"`  // Preserve friendly intended ban candidates.
	PreserveManual bool   `json:"preserveManual"` // Retain manually selected champion choices.
	DelaySeconds   int    `json:"delaySeconds"`   // Stable decision delay in seconds.
	Picks          []int  `json:"picks"`          // Ordered configured pick champion candidates.
	Bans           []int  `json:"bans"`           // Ordered configured ban champion candidates.
	Position       string `json:"position"`       // Empty permits every assigned position.
}

// Snapshot exposes one coherent browser-visible state revision without transport secrets.
type Snapshot struct {
	Revision          uint64              `json:"revision"`          // Monotonic public state revision number.
	Connected         bool                `json:"connected"`         // Local client transport health flag.
	Status            string              `json:"status"`            // Connection or permission state explanation.
	Phase             string              `json:"phase"`             // Last observed client gameflow phase.
	Patch             string              `json:"patch"`             // Current client game patch label.
	Account           *Account            `json:"account"`           // Last observed current account identity.
	Room              *Room               `json:"room"`              // Current real selection session only.
	History           *Profile            `json:"history"`           // Current account history observations.
	Battle            *Battle             `json:"battle"`            // Current public or historical roster.
	Scouts            map[string]Scout    `json:"scouts"`            // Public profiles keyed by player identity.
	Query             *Profile            `json:"query"`             // Latest explicit query player observations.
	QueryLoading      bool                `json:"queryLoading"`      // Current explicit query remains active.
	QueryError        string              `json:"queryError"`        // Latest sanitized explicit query failure.
	SummonerSearch    *SummonerSearch     `json:"summonerSearch"`    // Current name-only candidates awaiting confirmation.
	Analysis          AnalysisSettings    `json:"analysis"`          // Current persisted analysis preferences.
	Application       ApplicationSettings `json:"application"`       // Desktop and match acceptance preferences.
	AcceptStatus      string              `json:"acceptStatus"`      // Independent match acceptance worker feedback.
	Automation        AutomationSettings  `json:"automation"`        // Locally configured draft selection rules.
	AutomationEnabled bool                `json:"automationEnabled"` // Explicit runtime permission for draft writes.
	AutomationStatus  string              `json:"automationStatus"`  // Current selection worker state explanation.
	Database          Database            `json:"database"`          // Public catalog and source observations.
}

// Matchup retains candidate perspective and the public sample's source context.
type Matchup struct {
	ChampionID int     `json:"championId"` // Candidate official champion numeric identifier.
	WinRate    float64 `json:"winRate"`    // Candidate observed match victory percentage.
	Samples    int     `json:"samples"`    // Observed candidate versus target count.
	Source     string  `json:"source"`     // Version, region, and rank context.
}
