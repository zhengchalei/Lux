package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
	"lux/internal/review"
)

// runtime.go coordinates client discovery, independent history jobs, and browser state.
// One mutex publishes coherent snapshots, while network requests run outside that
// mutex and carry account, query, or roster generations. Account changes cancel
// previous work before new results can enter the visible state. Explicit queries
// have a reserved admission opportunity because background scouting can occupy
// only three of the four concurrent profile pipelines.
//
// The listener receives no payload and runs after the state lock is released.
// Browser calls receive deep snapshots, preventing a caller from mutating shared
// slices while another goroutine publishes a player. Every application start
// leaves selection automation disabled; its independent worker checks both the
// current rules generation and current account before a write is submitted.

// Runtime owns the local client lifetime and independently progressing user requests.
type Runtime struct {
	mu            sync.Mutex // Protects all fields except worker-local observations and immutable channel capacities.
	preferencesMu sync.Mutex // Serializes each runtime preference update with its matching file replacement.
	draftMu       sync.Mutex // Rejects overlapping manual hero and skin submissions.
	replayMu      sync.Mutex // Serializes replay registration and commands.
	// timelineMu protects timelineCache and timelineKeys together. Entries remain
	// immutable after insertion; account-generation keys prevent reuse after reconnects,
	// while FIFO eviction limits retained parsed game perspectives independently from snapshots.
	timelineMu    sync.Mutex                 // Protects immutable bounded timeline observations.
	timelineCache map[string]domain.Timeline // Account-generation-specific parsed game timelines.
	timelineKeys  []string                   // Oldest-first timeline cache eviction keys.
	// aiMu admits one model operation, whose aiCancel is read under mu by explicit
	// cancellation. The private ai service retains provider credentials outside public
	// snapshots, while its own settings lock orders concurrent configuration changes.
	aiMu            sync.Mutex          // Admits one explicit model analysis.
	aiCancel        context.CancelFunc  // Cancels the currently admitted model request.
	aiCancelKey     string              // Renderer request owning that cancellation permission.
	ai              *review.AIService   // Private model provider and encrypted preferences.
	draftAssetKey   string              // Session and champion owning cached thumbnail paths.
	draftAssets     map[int]string      // Current usable skin identifiers to verified asset paths.
	state           domain.Snapshot     // Coherent current browser observation.
	repository      *history.Repository // Shared bounded history and match cache.
	listener        func()              // Invoked outside mu after a committed state change.
	root            context.Context     // Application-owned cancellation context.
	cancel          context.CancelFunc  // Cancels all workers on close.
	session         context.Context     // Current credential/account generation context.
	endSession      context.CancelFunc  // Cancels old authenticated network jobs.
	access          *history.Access     // Ephemeral loopback credentials, never serialized.
	queryCancel     context.CancelFunc  // Ends only the preceding explicit query.
	rosterCancel    context.CancelFunc  // Ends only preceding roster jobs.
	roster          context.Context     // Current public roster cancellation context.
	epoch           uint64              // Credential/account publication generation.
	queryEpoch      uint64              // Explicit lookup publication generation.
	lookupName      string              // Last explicit query name for depth refresh.
	lookupPUUID     string              // Last explicit query identity when already visible.
	lookupQueue     int                 // Last explicit exact queue collection scope.
	hasLookup       bool                // Current account has an explicit query to refresh.
	rosterEpoch     uint64              // Current roster publication generation.
	automationEpoch uint64              // Rules/account authorization generation.
	// acceptEpoch, acceptAttempt, readyCycle, and acceptCancel are protected by mu.
	// Settings replacement revokes transport; consumed cycles survive toggle changes until
	// a phase transition or account generation produces another invitation identity.
	acceptEpoch   uint64             // Acceptance settings authorization generation.
	acceptAttempt string             // Last consumed account-cycle stamp.
	readyCycle    uint64             // Observed gameflow transition counter.
	acceptCancel  context.CancelFunc // Current acceptance transport cancellation.
	// writeCancel interrupts admitted selection transport when a later stop or rule replacement revokes its permission; already processed server actions cannot be undone.
	writeCancel     context.CancelFunc // Current admitted selection transport cancellation.
	wake            chan struct{}      // Coalesced state-refresh demand.
	profileSlots    chan struct{}      // Four total independently progressing profile jobs.
	backgroundSlots chan struct{}      // Three background jobs reserve one slot for lookup.
	wg              sync.WaitGroup     // Tracks application-owned goroutines until close.
	started         bool               // Start runs once for this runtime.
	closed          bool               // Blocks new jobs after cancellation begins.
	overviewLoading bool               // One personal overview read at a time.
	overviewEpoch   uint64             // Analysis revision owning personal overview results.
	overviewCancel  context.CancelFunc // Cancels an outdated personal overview request.
	lastOverview    time.Time          // Last personal overview scheduling instant.
	pendingReview   bool               // Defers startup review until authentication is available.
	reviewing       bool               // Suppresses duplicate review metadata jobs.
	fingerprint     string             // Latest normalized live observation fingerprint.
	metadataAccount string             // Account associated with optional cached region and patch.
	metadataAt      time.Time          // Latest optional metadata attempt instant.
	metadataRegion  string             // Last successful account region observation.
	metadataPatch   string             // Last successful client patch observation.
}

// New creates inactive services from public data and compatible persisted preferences.
// No network request or automation action starts until Start is called. Missing
// preferences retain protected defaults and an empty set of champion priorities.
func New(db domain.Database, repository *history.Repository) *Runtime {
	automation, analysis := loadSettings()
	application := loadApplicationSettings()
	return &Runtime{repository: repository, ai: review.NewAI(dataDirectory()), timelineCache: map[string]domain.Timeline{}, state: domain.Snapshot{Revision: 1, Status: "正在查找英雄联盟客户端…", Scouts: map[string]domain.Scout{}, Analysis: analysis, Application: application, AcceptStatus: acceptIdleStatus(application.AutoAccept), Automation: automation, AutomationStatus: "自动选禁已关闭", Database: clone(db)}, wake: make(chan struct{}, 1), profileSlots: make(chan struct{}, 4), backgroundSlots: make(chan struct{}, 3)}
}

// clone detaches serialized state values before crossing a concurrent ownership boundary.
// Domain DTOs contain JSON-compatible values only. An impossible encoding failure
// is a programmer error rather than a reason to return a shared mutable object.
func clone[T any](value T) T {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("non-serializable client state")
	}
	var detached T
	if err = json.Unmarshal(encoded, &detached); err != nil {
		panic("invalid serialized client state")
	}
	return detached
}

// SetListener installs a change signal invoked after coherent state commits.
func (r *Runtime) SetListener(listener func()) {
	r.mu.Lock()
	r.listener = listener
	r.mu.Unlock()
}

// Snapshot returns independent DTO ownership for concurrent browser serialization.
func (r *Runtime) Snapshot() domain.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clone(r.state)
}

// CurrentRevision reads the scalar change stamp without copying player histories.
func (r *Runtime) CurrentRevision() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Revision
}

// changed commits one public mutation and emits its listener after unlocking.
// Internal generations must be updated in the supplied closure when they govern
// publication. Callers must never start network operations inside that closure.
func (r *Runtime) changed(change func()) {
	r.mu.Lock()
	change()
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
}

// Start begins independent synchronization and selection workers for one application lifetime.
// Calling Start again has no effect. The parent context and Close both cancel
// network work, wake waits, and prevent new request jobs from being admitted.
func (r *Runtime) Start(parent context.Context) {
	r.mu.Lock()
	if r.started || r.closed {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.root, r.cancel = context.WithCancel(parent)
	r.mu.Unlock()
	r.spawn(r.run)
	r.spawn(r.automationLoop)
	r.spawn(r.acceptLoop)
}

// spawn registers owned work before launch while excluding jobs after close starts.
func (r *Runtime) spawn(job func()) bool {
	r.mu.Lock()
	if r.closed || r.root == nil || r.root.Err() != nil {
		r.mu.Unlock()
		return false
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		job()
	}()
	return true
}

// Close disables subsequent writes and waits for owned canceled workers to finish.
// An HTTP write already submitted cannot be recalled; the permission epoch stops
// every later submission. Repeated Close calls remain harmless.
func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.automationEpoch++
	r.state.AutomationEnabled = false
	if r.cancel != nil {
		r.cancel()
	}
	if r.endSession != nil {
		r.endSession()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// wakeSync coalesces repeated event notifications into a single outstanding snapshot demand.
func (r *Runtime) wakeSync() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// run recovers authentication and serializes live snapshots independently from profile reads.
// HTTP polling remains available when the event stream fails. A short minimum
// interval merges countdown bursts without leaving pending events unserved.
func (r *Runtime) run() {
	var lastSync time.Time
	for r.root.Err() == nil {
		r.mu.Lock()
		access := r.access
		r.mu.Unlock()
		if access == nil {
			found, err := discover()
			if err != nil {
				r.connectionError(err.Error())
				if !wait(r.root, 2*time.Second) {
					return
				}
				continue
			}
			r.installConnection(found)
		}
		if since := time.Since(lastSync); since < 300*time.Millisecond {
			if !wait(r.root, 300*time.Millisecond-since) {
				return
			}
		}
		lastSync = time.Now()
		if err := r.readSnapshot(); err != nil && r.root.Err() == nil {
			// A failed mandatory snapshot ends old authenticated jobs before rediscovery; last personal data retains its account rather than becoming fabricated empty history.
			r.disconnect("客户端暂不可读，正在自动重连")
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-r.root.Done():
			timer.Stop()
			return
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// connectionError publishes changed discovery explanations without idle revision churn.
func (r *Runtime) connectionError(message string) {
	r.mu.Lock()
	same := r.state.Status == message && !r.state.Connected
	r.mu.Unlock()
	if same {
		return
	}
	r.changed(func() { r.state.Status = message; r.state.Connected = false })
}

// installConnection starts a credential generation and its independently reconnecting socket.
func (r *Runtime) installConnection(access history.Access) {
	r.mu.Lock()
	if r.endSession != nil {
		r.endSession()
	}
	r.session, r.endSession = context.WithCancel(r.root)
	r.access = &access
	r.epoch++
	ctx := r.session
	r.mu.Unlock()
	r.spawn(func() { watchSocket(ctx, access, r.wakeSync) })
}

// disconnect invalidates old authenticated jobs before exposing automatic reconnection state.
func (r *Runtime) disconnect(message string) {
	r.changed(func() {
		if r.endSession != nil {
			r.endSession()
		}
		r.epoch++
		r.queryEpoch++
		r.rosterEpoch++
		r.automationEpoch++
		r.access = nil
		r.state.Connected, r.state.AutomationEnabled = false, false
		r.state.Status, r.state.AutomationStatus = message, "连接中断，自动选禁已停止"
		r.state.AcceptStatus = acceptIdleStatus(r.state.Application.AutoAccept)
		r.state.Room = nil
		if r.state.Battle != nil && !strings.HasPrefix(r.state.Battle.Key, "review:") {
			r.state.Battle = nil
		}
		for id, scout := range r.state.Scouts {
			scout.Loading = false
			r.state.Scouts[id] = scout
		}
		r.state.QueryLoading = false
		r.state.SummonerSearch = nil
		r.overviewLoading, r.reviewing = false, false
		r.fingerprint = ""
	})
}

// readSnapshot validates account ownership around public room and phase observations.
// Optional metadata is cached for the observed account. Identity is checked
// before and after reading gameflow so a mid-request account switch cannot
// publish a roster associated with the preceding account.
//
// A same-room refresh preserves player profiles, while a changed public roster
// starts fresh jobs. Leaving a live roster cancels those jobs and advances their
// publication generation under the state lock. An explicitly requested historical
// review remains visible during idle phases until another public roster replaces it.
func (r *Runtime) readSnapshot() error {
	r.mu.Lock()
	if r.access == nil {
		r.mu.Unlock()
		return errors.New("客户端尚未连接")
	}
	access, ctx, epoch := *r.access, r.session, r.epoch
	r.mu.Unlock()
	person, err := resolve(ctx, access, "", "")
	if err != nil {
		return err
	}
	puuid := domain.Identity(person["puuid"])
	if puuid == "" {
		return errors.New("等待账号身份")
	}
	var phase string
	if err = get(ctx, access, "/lol-gameflow/v1/gameflow-phase", &phase); err != nil {
		return err
	}
	region, patch := r.readMetadata(ctx, access, puuid)
	account := domain.Account{PUUID: puuid, Name: domain.DisplayName(person), Region: region, Platform: access.Platform, Level: number(person["summonerLevel"])}
	var room *domain.Room
	var battle *domain.Battle
	if phase == "ChampSelect" {
		var session, flow map[string]any
		if err = get(ctx, access, "/lol-champ-select/v1/session", &session); err != nil {
			return err
		}
		_ = get(ctx, access, "/lol-gameflow/v1/session", &flow)
		queue := number(session["queueId"])
		if queue == 0 {
			queue = number(object(object(flow["gameData"])["queue"])["id"])
		}
		picks, picksOK := readIDs(ctx, access, "/lol-champ-select/v1/pickable-champion-ids")
		disabled, disabledOK := readIDs(ctx, access, "/lol-champ-select/v1/disabled-champion-ids")
		subset := readPersonalChoices(ctx, access, session)
		observed := selectionRoom(session, queue, picks, disabled, picksOK && disabledOK, subset)
		room = &observed
		roster := draftBattle(session, observed)
		battle = &roster
	} else if phase == "GameStart" || phase == "InProgress" || phase == "Reconnect" {
		var flow map[string]any
		if err = get(ctx, access, "/lol-gameflow/v1/session", &flow); err != nil {
			return err
		}
		battle = gameBattle(flow, puuid)
	}
	verify, err := resolve(ctx, access, "", "")
	if err != nil || domain.Identity(verify["puuid"]) != puuid {
		return errors.New("客户端账号正在切换")
	}
	encoded, _ := json.Marshal([]any{account, phase, patch, room, battle})
	fingerprint := string(encoded)
	r.mu.Lock()
	if r.epoch != epoch || ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	accountChanged := r.state.Account == nil || r.state.Account.PUUID != puuid || r.state.Account.Region != region || r.state.Account.Platform != access.Platform
	oldBattle := r.state.Battle
	if accountChanged {
		// Cancel all previous account-owned jobs before replacing visible identity, preventing late completion from publishing under the next logged-in account.
		r.replaceAccountLocked()
		oldBattle = nil
	}
	r.access.Account, r.access.Region = puuid, region
	access = *r.access
	ctx = r.session
	changed := fingerprint != r.fingerprint || !r.state.Connected
	r.fingerprint = fingerprint
	if changed {
		if r.state.Phase != phase {
			r.readyCycle++
			if phase != "ReadyCheck" {
				r.state.AcceptStatus = acceptIdleStatus(r.state.Application.AutoAccept)
			}
		}
		r.state.Account = &account
		r.state.Connected, r.state.Status, r.state.Phase, r.state.Patch = true, "已连接 · 自动同步", phase, patch
		r.state.Room = room
		if battle != nil || oldBattle == nil || !strings.HasPrefix(oldBattle.Key, "review:") {
			r.state.Battle = battle
		}
		r.state.Revision++
	}
	currentBattle := r.state.Battle
	rosterChanged := battleSignature(oldBattle) != battleSignature(currentBattle)
	if rosterChanged && currentBattle == nil {
		// Leaving the live room revokes old roster publication before releasing the lock, including replies already completing during cancellation.
		if r.rosterCancel != nil {
			r.rosterCancel()
		}
		r.roster, r.rosterCancel = nil, nil
		r.rosterEpoch++
		r.state.Scouts = map[string]domain.Scout{}
	}
	pendingReview := r.pendingReview
	listener := r.listener
	r.mu.Unlock()
	if accountChanged {
		r.spawn(func() { watchSocket(ctx, access, r.wakeSync) })
	}
	if changed && listener != nil {
		listener()
	}
	r.ensureOverview()
	if rosterChanged {
		_ = r.RefreshBattle()
	}
	if pendingReview {
		_ = r.ReviewLastGame()
	}
	return nil
}

// replaceAccountLocked cancels previous ownership before clearing account-dependent observations.
// The caller holds mu and installs the new account before unlocking. Public
// champion data and persisted preferences survive this identity transition.
func (r *Runtime) replaceAccountLocked() {
	if r.endSession != nil {
		r.endSession()
	}
	r.session, r.endSession = context.WithCancel(r.root)
	r.epoch++
	r.queryEpoch++
	r.rosterEpoch++
	r.overviewEpoch++
	r.automationEpoch++
	r.state.History, r.state.Query, r.state.Battle = nil, nil, nil
	r.state.SummonerSearch = nil
	r.state.Scouts = map[string]domain.Scout{}
	r.state.QueryLoading, r.state.AutomationEnabled = false, false
	r.hasLookup = false
	r.state.QueryError, r.state.AutomationStatus = "", "账号已更新，自动选禁已关闭"
	r.overviewLoading, r.reviewing = false, false
	r.lastOverview = time.Time{}
}

// readMetadata retries incomplete optional metadata at most once per minute per account.
func (r *Runtime) readMetadata(ctx context.Context, access history.Access, account string) (string, string) {
	r.mu.Lock()
	if r.metadataAccount != account {
		r.metadataAccount, r.metadataRegion, r.metadataPatch, r.metadataAt = account, "", "", time.Time{}
	}
	region, patch, at := r.metadataRegion, r.metadataPatch, r.metadataAt
	r.mu.Unlock()
	if (region == "" || patch == "") && time.Since(at) >= time.Minute {
		var locale map[string]any
		var version string
		_ = get(ctx, access, "/riotclient/region-locale", &locale)
		_ = get(ctx, access, "/lol-patch/v1/game-version", &version)
		if observed := text(locale["region"]); observed != "" {
			region = observed
		}
		if version != "" {
			patch = version
		}
		r.mu.Lock()
		if r.metadataAccount == account {
			r.metadataRegion, r.metadataPatch, r.metadataAt = region, patch, time.Now()
		}
		r.mu.Unlock()
	}
	if region == "" {
		region = "未知大区"
	}
	if patch == "" {
		patch = "未知版本"
	}
	return region, patch
}

// battleSignature tracks membership, visible identities, and queue without champion hover churn.
func battleSignature(battle *domain.Battle) string {
	if battle == nil {
		return ""
	}
	var key strings.Builder
	fmt.Fprintf(&key, "%s/%d", battle.Key, battle.QueueID)
	for _, player := range battle.Players {
		fmt.Fprintf(&key, "/%t:%s", player.Ally, player.PUUID)
	}
	return key.String()
}

// acquire limits profile concurrency while preserving one user-query admission slot.
func (r *Runtime) acquire(ctx context.Context, background bool) (func(), bool) {
	if background {
		select {
		case r.backgroundSlots <- struct{}{}:
		case <-ctx.Done():
			return nil, false
		}
	}
	select {
	case r.profileSlots <- struct{}{}:
		return func() {
			<-r.profileSlots
			if background {
				<-r.backgroundSlots
			}
		}, true
	case <-ctx.Done():
		if background {
			<-r.backgroundSlots
		}
		return nil, false
	}
}

// loadProfile resolves metadata and publishes first observations before target-depth completion.
// Background callers supply a known public identity. The second read retains its
// first usable batch on failure, with explicit coverage and history error state.
func (r *Runtime) loadProfile(ctx context.Context, access history.Access, query, puuid string, count, queue int, background bool, progress func(domain.Profile)) (domain.Profile, error) {
	release, ok := r.acquire(ctx, background)
	if !ok {
		return domain.Profile{}, ctx.Err()
	}
	defer release()
	person, err := resolve(ctx, access, query, puuid)
	if err != nil {
		return domain.Profile{}, err
	}
	resolved := domain.Identity(person["puuid"])
	if resolved == "" || puuid != "" && resolved != puuid {
		return domain.Profile{}, errors.New("玩家身份尚不可读")
	}
	var rank map[string]any
	_ = get(ctx, access, "/lol-ranked/v1/ranked-stats/"+resolved, &rank)
	decorate := func(profile *domain.Profile) {
		profile.PUUID, profile.Name, profile.Level, profile.Rank = resolved, domain.DisplayName(person), number(person["summonerLevel"]), domain.RankLabel(rank)
		profile.Requested, profile.Coverage.Requested = count, count
	}
	firstCount := min(count, 20)
	profile, err := r.repository.Load(ctx, access, resolved, firstCount, queue)
	if err != nil {
		return profile, err
	}
	decorate(&profile)
	if count > firstCount {
		progress(profile)
		continued, nextErr := r.repository.Load(ctx, access, resolved, count, queue)
		if nextErr != nil {
			if ctx.Err() != nil {
				return domain.Profile{}, ctx.Err()
			}
			profile.HistoryError = "继续加载失败：" + nextErr.Error()
			profile.Coverage.Note = "保留已读取的首批战绩；" + profile.Coverage.Note
			return profile, nil
		}
		profile = continued
		decorate(&profile)
	}
	return profile, nil
}

// ensureOverview schedules at most one current-account overview in each thirty-second interval.
func (r *Runtime) ensureOverview() {
	r.mu.Lock()
	if r.access == nil || r.state.Account == nil || r.overviewLoading || time.Since(r.lastOverview) < 30*time.Second {
		r.mu.Unlock()
		return
	}
	access, epoch, count := *r.access, r.epoch, r.state.Analysis.MatchCount
	ctx, cancel := context.WithCancel(r.session)
	r.overviewCancel = cancel
	r.overviewEpoch++
	overview := r.overviewEpoch
	r.overviewLoading, r.lastOverview = true, time.Now()
	r.mu.Unlock()
	r.spawn(func() {
		defer cancel()
		publish := func(profile domain.Profile) {
			r.publishIf(epoch, 0, 0, func() {
				if r.overviewEpoch == overview {
					r.state.History = &profile
				}
			})
		}
		profile, err := r.loadProfile(ctx, access, "", access.Account, count, 0, true, publish)
		r.publishIf(epoch, 0, 0, func() {
			if r.overviewEpoch != overview {
				return
			}
			r.overviewLoading = false
			if err == nil {
				r.state.History = &profile
			} else if r.state.History == nil {
				r.state.Status = "已连接，个人战绩暂不可读：" + err.Error()
			}
		})
	})
}

// publishIf commits only observations still owned by their authenticated request generation.
func (r *Runtime) publishIf(epoch, query, roster uint64, change func()) {
	r.mu.Lock()
	if r.closed || r.epoch != epoch || query != 0 && r.queryEpoch != query || roster != 0 && r.rosterEpoch != roster {
		r.mu.Unlock()
		return
	}
	change()
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
}

// Lookup starts an asynchronous name, Riot ID, or visible PUUID history query.
// A new query cancels the prior query before returning. Results are associated
// with both the account generation and this exact query generation.
func (r *Runtime) Lookup(name string, queueID int) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("请输入召唤师名称或名称#编号")
	}
	if len([]rune(name)) > 100 {
		return errors.New("召唤师名称过长")
	}
	r.mu.Lock()
	visible := r.visibleIdentityLocked(name)
	r.mu.Unlock()
	if visible {
		return r.startLookup("", name, queueID)
	}
	return r.startLookup(name, "", queueID)
}

// QuerySelf starts the same asynchronous query path for the current account.
func (r *Runtime) QuerySelf(queueID int) error {
	return r.startLookup("", "", queueID)
}

// startLookup establishes query ownership before admitting its profile pipeline.
func (r *Runtime) startLookup(name, puuid string, queue int) error {
	return r.beginLookup(name, puuid, queue, 0)
}

// beginLookup atomically validates candidate confirmation and replaces query ownership.
// A candidate must belong to the still-current search and have a verified local
// summoner. Cancellation and generation replacement precede every asynchronous read.
func (r *Runtime) beginLookup(name, puuid string, queue int, searchID uint64) error {
	r.mu.Lock()
	if r.access == nil || r.state.Account == nil {
		r.mu.Unlock()
		return errors.New("客户端尚未连接")
	}
	if strings.HasPrefix(name, "puuid:") {
		puuid, name = strings.TrimPrefix(name, "puuid:"), ""
		if !r.visibleIdentityLocked(puuid) {
			r.mu.Unlock()
			return errors.New("只能查询当前公开的玩家身份")
		}
	}
	confirmed := false
	if searchID != 0 {
		if search := r.state.SummonerSearch; search != nil && search.ID == searchID {
			for _, candidate := range search.Candidates {
				confirmed = confirmed || candidate.PUUID == puuid && candidate.Available && !candidate.Loading
			}
		}
		if !confirmed {
			r.mu.Unlock()
			return errors.New("候选账号已更新，请重新搜索并选择")
		}
	}
	if puuid != "" && !confirmed && !r.visibleIdentityLocked(puuid) {
		r.mu.Unlock()
		return errors.New("只能查询当前公开的玩家身份")
	}
	if r.queryCancel != nil {
		r.queryCancel()
	}
	ctx, cancel := context.WithCancel(r.session)
	r.queryCancel = cancel
	r.queryEpoch++
	r.lookupName, r.lookupPUUID, r.lookupQueue, r.hasLookup = name, puuid, queue, true
	searching := name != "" && !strings.Contains(name, "#") && puuid == ""
	if searching {
		r.hasLookup = false
	}
	access, epoch, query, count := *r.access, r.epoch, r.queryEpoch, r.state.Analysis.MatchCount
	r.state.Query, r.state.QueryLoading, r.state.QueryError = nil, true, ""
	r.state.SummonerSearch = nil
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
	r.spawn(func() {
		defer cancel()
		if searching {
			r.searchSummoners(ctx, access, name, epoch, query)
			return
		}
		publish := func(profile domain.Profile) { r.publishIf(epoch, query, 0, func() { r.state.Query = &profile }) }
		profile, err := r.loadProfile(ctx, access, name, puuid, count, queue, false, publish)
		r.publishIf(epoch, query, 0, func() {
			r.state.QueryLoading = false
			if err == nil {
				r.state.Query = &profile
				r.state.QueryError = profile.HistoryError
			} else {
				r.state.QueryError = "查询失败：" + err.Error()
			}
		})
	})
	return nil
}

// visibleIdentityLocked verifies explicit public identity membership while holding mu.
func (r *Runtime) visibleIdentityLocked(puuid string) bool {
	if puuid == "" {
		return false
	}
	if r.state.Account != nil && r.state.Account.PUUID == puuid {
		return true
	}
	if r.state.Query != nil && r.state.Query.PUUID == puuid {
		return true
	}
	if r.hasLookup && r.lookupPUUID == puuid {
		return true
	}
	if r.state.Battle != nil {
		for _, player := range r.state.Battle.Players {
			if !player.Hidden && player.PUUID == puuid {
				return true
			}
		}
	}
	return false
}

// SetDatabase publishes an independently owned public catalog without touching account state.
func (r *Runtime) SetDatabase(db domain.Database) {
	owned := clone(db)
	r.repository.SetDatabase(db)
	r.changed(func() { r.state.Database = owned })
}

// SetAnalysis saves bounded depth and refreshes affected history pipelines on count changes.
// Scope changes refresh exact-queue collection as well as prepared mode summaries.
// Persistence errors return after in-memory application, leaving the interface responsive.
func (r *Runtime) SetAnalysis(count int, sameMode bool) error {
	r.preferencesMu.Lock()
	defer r.preferencesMu.Unlock()
	count = boundedCount(count)
	r.mu.Lock()
	countChanged := count != r.state.Analysis.MatchCount
	scopeChanged := sameMode != r.state.Analysis.SameMode
	lookupName, lookupPUUID, lookupQueue, refreshLookup := r.lookupName, r.lookupPUUID, r.lookupQueue, r.hasLookup && countChanged
	r.mu.Unlock()
	settings := domain.AnalysisSettings{MatchCount: count, SameMode: sameMode}
	r.changed(func() {
		r.state.Analysis = settings
		if countChanged {
			if r.overviewCancel != nil {
				r.overviewCancel()
			}
			r.overviewEpoch++
			r.overviewLoading = false
			r.lastOverview = time.Time{}
		}
	})
	if countChanged {
		r.ensureOverview()
		if refreshLookup {
			_ = r.startLookup(lookupName, lookupPUUID, lookupQueue)
		}
	}
	if countChanged || scopeChanged {
		_ = r.RefreshBattle()
	}
	return savePreference("analysis.json", settings)
}
