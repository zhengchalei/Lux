# MyGo + Go + Vue 迁移协作契约

目标：Windows WebView2 桌面版，纯 Go 数据服务，Vue 3 + TypeScript 前端。旧 Rust 代码保留供行为对照；新版运行不调用 Rust 程序。公共资产复用现有 assets。无示例玩家数据。

## Go packages

- `internal/domain`: JSON camelCase DTOs, normalization, summary/contribution calculations. This package depends only on the standard library. Go fields use standard initialisms (ID, PUUID, QueueID).
- `internal/history`: LCU + SGP repository, exact queue pages, detail shared cache. Constructor `New(db domain.Database) *Repository`; methods `Load(ctx context.Context, access Access, puuid string, count int, queueID int) (domain.Profile,error)`, `Detail(ctx context.Context, access Access, puuid string, id uint64) (domain.Match,error)`. Access fields `Port int`, `Token, Region, Platform, Account string`. Load returns records and calculated statistics; queueID=0 means all. Expose `Get(ctx, access, path, out)` loopback helper if useful. Authentication never appears in frontend JSON or error text.
- `internal/client`: discover/reconnect, snapshots/events, account generation, async player requests, independent automation. Constructor `New(db domain.Database, repository *history.Repository) *Runtime`. API: `Start(ctx)`, `Close()`, `Snapshot() domain.Snapshot`, `Lookup(name string,queueID int) error`, `QuerySelf(queueID int) error`, `ReviewLastGame() error`, `RefreshBattle() error`, `RetryScout(puuid string) error`, `SetAnalysis(count int,sameMode bool) error`, `SaveAutomation(settings domain.AutomationSettings,enabled bool) error`, `StopAutomation()`, `Detail(ctx,puuid,id) (domain.Match,error)`, `SetDatabase(db domain.Database)`. A `SetListener(func())` callback signals coherent state changes; callback is invoked after unlocking. Runtime snapshot is safe against concurrent callers. Startup automation always off.
- `internal/publicdata`: embedded public catalog, source refresh, mode balance and OP.GG matchups. Owned by root.
- `main.go`, `app.go`: MyGo Bind typed methods, embedded frontend/static assets. Owned by root.

## Shared domain types (authoritative declarations in internal/domain)

- `Mode` string constants `Ranked="ranked"`, `Aram="aram"`, `Mayhem="mayhem"`.
- `Adjustment{Label,Display string; Helpful bool}`; `Statistics{Tier string; WinRate *float64; Samples int; Adjustments []Adjustment; Source string}`. nil adjustments mean unknown.
- `Champion{ID int; Name,Title,Alias string; Roles []string; Aram,Mayhem Statistics}`.
- `Database{Champions []Champion; Patch,Updated,SourceStatus string}`; method `Champion(id int) Champion`.
- `Account{PUUID,Name,Region,Platform string; Level int}`.
- `Member{Cell int; Champion int; Position string; Locked,Local bool}`.
- `Room{Mode Mode; QueueID int; Allies,Enemies []Member; Available,Banned []int; Current int}`.
- `Player{ChampionID int; Name string; Team int; Local bool; Kills,Deaths,Assists,Damage,Gold,Taken,Heal,TowerDamage int}`; Local JSON key `self`.
- `Match{ID uint64; ChampionID int; Name,Queue string; QueueID int; Win bool; Kills,Deaths,Assists,CS,Damage,Gold,Duration int; Created int64; Team int; Players []Player; Source,GameType string; Remake bool; MissingMetrics [5]bool}`. CamelCase including `cs`, `queueId`, `championId`.
- `Coverage{Source string; Requested,Fetched,Scanned,Valid,Complete,QueueID int; Note string}`.
- `Contribution{Games,DamageGames,TakenGames,GoldGames int; DamageShare,TakenShare,GoldShare *float64}`. Fractions 0..1.
- `HeroSummary{ChampionID,Games,Wins int; WinRate,KDA float64; Kills,Deaths,Assists float64}`.
- `Summary{Games,Wins int; WinRate,KDA,Kills,Deaths,Assists float64; Recent []Match; Heroes []HeroSummary; Contribution Contribution}`. WinRate 0..100, kills/deaths/assists averages. KDA sum(K+A)/max(sum D,1).
- `Profile{PUUID,Name,Rank,Captured,HistoryError string; Level,Requested int; Matches []Match; Coverage Coverage; Summary Summary; Summaries map[string]Summary}`; Summaries keys all/ranked/aram/mayhem. Export `PrepareProfile(*Profile)`.
- `Combatant{PUUID,Name,Position string; Champion int; Ally,Hidden,Locked bool}`.
- `Battle{Key,Source string; Mode Mode; QueueID int; Players []Combatant}`.
- `Scout{Profile *Profile; Loading bool; Error string}`.
- `AnalysisSettings{MatchCount int; SameMode bool}`. Existing file snake_case must remain compatible.
- `AutomationSettings{AutoPick,AutoBan,Preselect,LockPick,ProtectAllies,PreserveManual bool; DelaySeconds int; Picks,Bans []int; Position string}`. Frontend camelCase, existing snake_case settings migrated by runtime.
- `Snapshot{Revision uint64; Connected bool; Status,Phase,Patch string; Account *Account; Room *Room; History *Profile; Battle *Battle; Scouts map[string]Scout; Query *Profile; QueryLoading bool; QueryError string; Analysis AnalysisSettings; Automation AutomationSettings; AutomationEnabled bool; AutomationStatus string; Database Database}`.
- `Matchup{ChampionID int; WinRate float64; Samples int; Source string}`.

## Frontend calls and state

Backend bound service `App` public methods: `GetRevision`, `GetSnapshot`, `Lookup(name,queueID)`, `QuerySelf(queueID)`, `ReviewLastGame`, `RefreshBattle`, `RetryScout(puuid)`, `SetAnalysis(count,sameMode)`, `SaveAutomation(settings,enabled)`, `StopAutomation`, `GetMatchDetail(puuid,id)`, `GetMatchups(enemy,position)`, `InspectChampion(id)`.

`frontend/src/api.ts` calls the MyGo typed runtime directly. Go emits a coalesced `lux:changed` revision signal and Vue serializes snapshot reads. Recovery timers first read the scalar `GetRevision`; unchanged revisions do not transfer full history snapshots. Never expose credentials. Frontend components own view selection and update only changed sections. Frame benchmarks state whether actual WebView2 or a browser was measured.
