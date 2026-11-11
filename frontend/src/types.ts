// types.ts describes the sanitized observations exposed by Lux's Go services.
// Client credentials never enter these contracts. Match statistics retain their
// coverage and missing-value semantics so views can distinguish an unavailable
// observation from an observed zero, and stable account identities from slots.

/** Mode identifies independent gameplay and public statistic interpretation rules. */
export type Mode = 'ranked' | 'aram' | 'mayhem'
/** Page selects one locally navigated workspace without changing backend state. */
export type Page = 'history' | 'draft' | 'battle' | 'query' | 'automation' | 'settings'
/** Adjustment retains the source value and its interpreted gameplay benefit. */
export interface Adjustment { label: string; display: string; helpful: boolean }
/** Statistics groups observations sharing one gameplay mode and source context. */
export interface Statistics {
  tier: string; winRate: number | null; samples: number
  adjustments: Adjustment[] | null; source: string
}
/** Champion describes a public hero without mixing ordinary and Mayhem observations. */
export interface Champion {
  id: number; name: string; title: string; alias: string; roles: string[]
  aram: Statistics; mayhem: Statistics
}
/** Database contains public champion data independently from account observations. */
export interface Database { champions: Champion[]; patch: string; updated: string; sourceStatus: string }
/** Account identifies the current local client account and its routing context. */
export interface Account { puuid: string; name: string; region: string; platform: string; level: number }
/** Member represents a publicly available or friendly selection session slot. */
export interface Member { cell: number; champion: number; position: string; locked: boolean; local: boolean }
/** Room retains the current session's public lineup and legal champion pool. */
export interface Room {
  sessionKey: string; canSwap: boolean; skinId: number
  canChoose: boolean // Personal cards accept choices.
  choices: number[] // Personal preselection champion identifiers.
  bench: number[] // Shared replacement bench identifiers.
  mode: Mode; queueId: number; allies: Member[]; enemies: Member[]
  available: number[]; banned: number[]; current: number
}
/** Replay describes client-managed availability without exposing local replay file paths. */
export interface Replay {
  gameId: number // Selected recorded game identifier.
  state: string // Observed client replay state.
  label: string // Concise localized status label.
  reason: string // Explanation for hover hints.
  progress: number | null // Download percentage when observed.
  canDownload: boolean // Current download command permission.
  canWatch: boolean // Ready file and idle client.
}
/** Player contains recorded combat statistics for one identified match participant. */
export interface Player {
  championId: number; name: string; team: number; self: boolean
  kills: number; deaths: number; assists: number; damage: number; gold: number
  taken: number; heal: number; towerDamage: number
}
/** Match preserves one player's perspective and any complete observed combat roster. */
export interface Match {
  id: number; championId: number; name: string; queue: string; queueId: number
  win: boolean; kills: number; deaths: number; assists: number; cs: number
  damage: number; gold: number; duration: number; created: number; team: number
  players: Player[]; source: string; gameType: string; remake: boolean; missingMetrics: boolean[]
}
/** Coverage separates requested depth from scanned, returned, and usable observations. */
export interface Coverage {
  source: string; requested: number; fetched: number; scanned: number
  valid: number; complete: number; queueId: number; note: string
}
/** Contribution holds mean team shares with independent known denominator coverage. */
export interface Contribution {
  games: number; damageGames: number; takenGames: number; goldGames: number
  damageShare: number | null; takenShare: number | null; goldShare: number | null
}
/** HeroSummary describes one champion within the current historical sample scope. */
export interface HeroSummary {
  championId: number; games: number; wins: number; winRate: number; kda: number
  kills: number; deaths: number; assists: number
}
/** Summary contains prepared aggregates and the matching recent outcome sample. */
export interface Summary {
  games: number; wins: number; winRate: number; kda: number
  kills: number; deaths: number; assists: number; recent: Match[]
  heroes: HeroSummary[]; contribution: Contribution
}
/** Profile associates retrieved records and prepared summaries with their actual player. */
export interface Profile {
  puuid: string; name: string; rank: string; captured: string; historyError: string
  level: number; requested: number; matches: Match[]; coverage: Coverage
  summary: Summary; summaries: Partial<Record<Mode | 'all', Summary>>
}
/** Combatant represents only a player's publicly observable current battle identity. */
export interface Combatant {
  puuid: string; name: string; position: string; champion: number
  ally: boolean; hidden: boolean; locked: boolean
}
/** Battle attaches a lineup to its live or historical source identity. */
export interface Battle { key: string; source: string; mode: Mode; queueId: number; players: Combatant[] }
/** Scout preserves previously read player data during independent refresh or failure. */
export interface Scout { profile: Profile | null; loading: boolean; error: string }
/** SummonerCandidate retains an alias even when current-region metadata is unavailable. */
export interface SummonerCandidate {
  puuid: string; name: string; iconId: number; level: number; rank: string
  loading: boolean; available: boolean; reason: string
}
/** SummonerSearch binds explicit confirmation to one returned name-only candidate set. */
export interface SummonerSearch { id: number; name: string; candidates: SummonerCandidate[] }
/** TimelinePlayer retains the explicit recorded slot and role for one game. */
export interface TimelinePlayer { id: number; champion: number; championName: string; name: string; team: number; role: string }
/** TimelineStats preserves missing sampled measurements independently from observed zeroes. */
export interface TimelineStats { id: number; gold: number | null; cs: number | null; xp: number | null; level: number | null; x: number | null; y: number | null }
/** TimelineFrame associates simultaneous measurements with elapsed game time in milliseconds. */
export interface TimelineFrame { at: number; players: TimelineStats[] }
/** TimelineEvent retains supported observed events and stable evidence references. */
export interface TimelineEvent { id: string; at: number; kind: string; actor: number; target: number; assists: number[]; team: number; label: string }
/** Timeline contains sampled client facts without continuous movement or intent claims. */
export interface Timeline { gameId: number; mapId: number; queueId: number; ownId: number; suggestedOpponent: number; sampleSeconds: number; version: string; players: TimelinePlayer[]; frames: TimelineFrame[]; events: TimelineEvent[] }
/** ReviewWindow spans actual sample boundaries and nonoverlapping observed events. */
export interface ReviewWindow { id: string; start: number; end: number; tone: 'gain' | 'loss' | 'neutral'; ownStart: TimelineStats; ownEnd: TimelineStats; otherStart: TimelineStats; otherEnd: TimelineStats; goldTrend: number | null; goldDiff: number | null; csDiff: number | null; ownKda: number[]; otherKda: number[]; events: TimelineEvent[] }
/** TimelineReview pairs reusable timeline observations with an explicitly selected opponent. */
export interface TimelineReview { timeline: Timeline; opponent: number; windows: ReviewWindow[] }
/** AISettings exposes provider preferences while never returning the saved credential. */
export interface AISettings { baseUrl: string; model: string; hasKey: boolean; note: string }
/** AISettingsInput preserves a blank key unless removal is explicitly requested. */
export interface AISettingsInput { baseUrl: string; model: string; apiKey: string; clearKey: boolean }
/** AIInsight binds coaching to a verified phase and existing evidence identifiers. */
export interface AIInsight { windowId: string; verdict: 'good' | 'review' | 'neutral'; title: string; reason: string; opponent: string; advice: string; evidenceIds: string[] }
/** AIReview represents a validated provider response, independently from local trend labels. */
export interface AIReview { summary: string; insights: AIInsight[]; goals: string[]; model: string; created: string }
/** AnalysisSettings controls subsequent historical collection depth and current-mode scope. */
export interface AnalysisSettings { matchCount: number; sameMode: boolean }
/** ApplicationSettings retains background lifetime and independent match acceptance preferences. */
export interface ApplicationSettings { closeToTray: boolean; autoAccept: boolean; acceptDelaySeconds: number }
/** DesktopStatus reports native tray recovery availability without exposing window handles. */
export interface DesktopStatus { trayAvailable: boolean }
/** AutomationSettings contains persisted preferences without a persisted runtime permission. */
export interface AutomationSettings {
  autoPick: boolean; autoBan: boolean; preselect: boolean; lockPick: boolean
  protectAllies: boolean; preserveManual: boolean; delaySeconds: number
  picks: number[]; bans: number[]; position: string
}
/** Snapshot is one coherent, credential-free observation published by the Go runtime. */
export interface Snapshot {
  revision: number; connected: boolean; status: string; phase: string; patch: string
  account: Account | null; room: Room | null; history: Profile | null; battle: Battle | null
  scouts: Record<string, Scout>; query: Profile | null; queryLoading: boolean; queryError: string
  summonerSearch: SummonerSearch | null
  application: ApplicationSettings; acceptStatus: string
  analysis: AnalysisSettings; automation: AutomationSettings; automationEnabled: boolean
  automationStatus: string; database: Database
}
/** Matchup describes final match outcomes against an explicitly selected opposing champion. */
export interface Matchup { championId: number; winRate: number; samples: number; source: string }

/** DraftSkin describes a currently usable appearance from the client's carousel. */
export interface DraftSkin { id: number; name: string; base: boolean }
/** DraftSkins ties available appearances to one actual champion and session. */
export interface DraftSkins { sessionKey: string; championId: number; selected: number; enabled: boolean; skins: DraftSkin[] }
/** DraftPanelSnapshot contains compact selection observations without player histories. */
export interface DraftPanelSnapshot { revision: number; connected: boolean; phase: string; queueId: number; room: Room | null; database: Database }
/** DraftPanelStatus describes the companion's native preferences and observed visibility. */
export interface DraftPanelStatus { enabled: boolean; pinned: boolean; visible: boolean; attached: boolean; collapsed: boolean }
