package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"lux/internal/domain"
)

// repository.go collects same-region histories and shares complete match observations.
// SGP is preferred only when its configured origin and current authentication are
// usable. Successful empty histories remain empty, while an unavailable provider
// falls back to the current LCU connection with its actual source recorded.
// Pagination counts unique inspected records separately from retained exact-queue
// records, and later-page failures preserve the already observed first pages.
//
// Complete matches are cached as compact JSON bytes under region and game ID.
// Each caller decodes its own player perspective outside the cache mutex. Per-game
// in-flight completion channels merge successful concurrent reads, context cancellation
// releases ownership, and count plus byte limits bound retained observations. These
// caches never retain credentials or persist personal histories to disk.

// Repository limits cap inspected pages, concurrent HTTP work, and retained compact complete-game payloads across clients.
const (
	maxGames = 256
	maxBytes = 16 * 1024 * 1024
	maxScan  = 1000
	pageSize = 50
)

// cacheEntry retains perspective-independent source bytes and its recent-use ordering.
type cacheEntry struct {
	Bytes  []byte // Compact perspective-independent JSON payload bytes.
	Source string // Provider selecting the payload adapter.
	Used   uint64 // Most recent monotonic use sequence.
}

// Repository owns public routing, shared admission, and bounded complete-game observations.
type Repository struct {
	// mu coordinates db, directory, cache, bytes, clock, flights, and refreshAt.
	// Cache bytes remain immutable after publication and are decoded only after
	// releasing mu; network operations never run while this state lock is held.
	mu              sync.Mutex               // Protects directory, catalog, cache, and in-flight ownership.
	db              domain.Database          // Public normalization catalog snapshot.
	directory       directory                // Last validated public server directory.
	cache           map[string]cacheEntry    // Region-specific complete raw match bytes.
	bytes           int                      // Retained compact JSON byte count.
	clock           uint64                   // Monotonic cache use ordering.
	flights         map[string]chan struct{} // Active complete-match request completion signals.
	permits         chan struct{}            // Bounded history HTTP admission slots.
	tokenMu         sync.Mutex               // Serializes exact-identity token resolution and cooldown.
	token           *tokenEntry              // Last successful short-lived authentication resolution.
	failureIdentity Access                   // Exact connection with a shared service failure.
	failureAt       time.Time                // Last shared service failure instant.
	refreshAt       time.Time                // Latest public directory refresh attempt.
}

// New constructs a repository using the embedded public routing baseline.
// Network access begins only when Load or Detail is called. An invalid embedded
// directory leaves SGP unavailable while preserving the current-client LCU fallback.
func New(db domain.Database) *Repository {
	// Invalid embedded public data leaves SGP unavailable; an empty directory still permits the current authenticated client's local fallback.
	d, _ := parseDirectory(embeddedDirectory)
	return &Repository{db: db, directory: d, cache: map[string]cacheEntry{}, flights: map[string]chan struct{}{}, permits: make(chan struct{}, 4)}
}

// SetDatabase replaces the public champion catalog used by future normalization operations.
func (r *Repository) SetDatabase(db domain.Database) { r.mu.Lock(); r.db = db; r.mu.Unlock() }

// catalog returns the current immutable public catalog without retaining its mutex.
func (r *Repository) catalog() domain.Database { r.mu.Lock(); defer r.mu.Unlock(); return r.db }

// admitted bounds one history request while allowing context cancellation before admission.
// Live state and automation use their own transports and are therefore never queued
// behind these permits. Returning always releases the slot after the HTTP operation.
func (r *Repository) admitted(ctx context.Context, read func() error) error {
	select {
	case r.permits <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.permits }()
	return read()
}

// Load collects one bounded exact-queue sample and prepares its combat summaries.
// A successful empty SGP response never invokes fallback. Later pagination failures
// preserve acquired records and a visible coverage note. LCU summaries receive at
// most five recent complete-game enrichments; older detail stays available on demand.
// Cancellation propagates instead of changing provider. Source metadata records both
// scan coverage and eligibility, so a requested two hundred games cannot become
// an assertion that two hundred valid games were returned.
func (r *Repository) Load(ctx context.Context, a Access, puuid string, count, queueID int) (domain.Profile, error) {
	if domain.Identity(puuid) == "" {
		return domain.Profile{}, errors.New("玩家标识不可用")
	}
	count = max(1, min(count, 200))
	queueID = max(queueID, 0)
	r.refreshDirectory(ctx)
	var primaryErr error
	var profile domain.Profile
	access, err := r.access(ctx, a)
	if err == nil {
		profile, err = r.collect(ctx, a, &access, puuid, count, queueID)
	}
	primaryErr = err
	if primaryErr != nil {
		if ctx.Err() != nil {
			return domain.Profile{}, ctx.Err()
		}
		// Only an unavailable SGP path invokes fallback; a successful empty collection has already established an observed zero-record result.
		r.markFailure(a, primaryErr)
		profile, err = r.collect(ctx, a, nil, puuid, count, queueID)
		if err != nil {
			if ctx.Err() != nil {
				return domain.Profile{}, ctx.Err()
			}
			return domain.Profile{}, errors.New("SGP 与当前客户端均未提供可读取的战绩")
		}
		profile.Coverage.Note = strings.TrimSuffix("SGP 暂不可用，已使用当前客户端 LCU；"+profile.Coverage.Note, "；")
	}
	if profile.Coverage.Source == "LCU" {
		// Bound automatic detail work to five recent usable records so initial history availability does not require enriching two hundred matches.
		enriched := 0
		for i, m := range profile.Matches {
			if !domain.Usable(m, "") || m.Complete() {
				continue
			}
			if enriched >= 5 {
				break
			}
			enriched++
			if full, e := r.Detail(ctx, a, puuid, m.ID); e == nil && full.Complete() {
				profile.Matches[i] = full
			}
			if ctx.Err() != nil {
				return domain.Profile{}, ctx.Err()
			}
		}
	}
	domain.PrepareProfile(&profile)
	return profile, nil
}

// collect advances one provider by its actual row count and rejects repeated pages.
// Retained count is independent from unique scanned count when queues are filtered.
// A malformed first page fails visibly; a later failure retains earlier observations.
// Normalization enforces the requested player's identity before counting any record,
// and the first nonempty page must contain at least one parseable observation.
// Each page advances by returned rows instead of assuming the service honored
// the requested size. A repeated page terminates without inflating either count.
func (r *Repository) collect(ctx context.Context, a Access, access *sgpAccess, puuid string, count, queue int) (domain.Profile, error) {
	source := "LCU"
	if access != nil {
		source = "SGP"
	}
	limit := count
	if queue > 0 {
		// Scoped LCU reads may inspect ten times the target, but never more than one thousand source rows per query.
		limit = min(count*10, maxScan)
	}
	seen := map[uint64]bool{}
	offset := 0
	db := r.catalog()
	p := domain.Profile{PUUID: puuid, Name: "召唤师", Rank: "段位暂不可用", Captured: time.Now().UTC().Format(time.RFC3339Nano), Requested: count, Matches: []domain.Match{}, Coverage: domain.Coverage{Source: source, Requested: count, QueueID: queue}}
	for offset < limit && len(p.Matches) < count {
		size := min(pageSize, limit-offset)
		if access != nil {
			size = min(size, count-len(p.Matches))
		}
		var response map[string]any
		path := fmt.Sprintf("/lol-match-history/v1/products/lol/%s/matches?begIndex=%d&endIndex=%d", puuid, offset, offset+size-1)
		if access != nil {
			query := url.Values{"startIndex": {strconv.Itoa(offset)}, "count": {strconv.Itoa(size)}}
			if queue > 0 {
				query.Set("tag", fmt.Sprintf("q_%d", queue))
			}
			path = "/match-history-query/v1/products/lol/player/" + puuid + "/SUMMARY?" + query.Encode()
		}
		err := r.admitted(ctx, func() error {
			if access != nil {
				return remoteGet(ctx, *access, path, &response)
			}
			return Get(ctx, a, path, &response)
		})
		if err != nil {
			if ctx.Err() != nil {
				return p, ctx.Err()
			}
			if offset == 0 {
				return p, err
			}
			p.Coverage.Note = "后续分页暂不可读，已保留取得的样本"
			break
		}
		rows, ok := response["games"].([]any)
		if access == nil {
			rows, ok = domain.Object(response["games"])["games"].([]any)
		}
		if !ok {
			if offset == 0 {
				return p, errors.New("战绩响应缺少比赛列表")
			}
			p.Coverage.Note = "后续分页格式暂不可读，已保留取得的样本"
			break
		}
		before := len(seen)
		parsed := 0
		for _, v := range rows[:min(len(rows), limit-offset)] {
			raw := domain.Object(v)
			id := rawID(raw, source)
			if id == 0 || seen[id] {
				continue
			}
			seen[id] = true
			m, valid := normalize(raw, source, puuid, db)
			if !valid {
				continue
			}
			parsed++
			if queue != 0 && m.QueueID != queue {
				continue
			}
			if len(p.Matches) < count {
				p.Matches = append(p.Matches, m)
				if m.Complete() {
					r.cachePut(cacheKey(a, id), raw, source)
				}
			}
		}
		if offset == 0 && len(rows) > 0 && parsed == 0 {
			return p, errors.New("战绩响应没有可解析的玩家观察")
		}
		offset += min(len(rows), limit-offset)
		if len(seen) == before {
			break
		}
	}
	if offset >= limit && queue > 0 && len(p.Matches) < count {
		p.Coverage.Note = "已达扫描上限，当前队列样本不足目标场数"
	}
	p.Coverage.Scanned = len(seen)
	domain.PrepareProfile(&p)
	return p, nil
}

// normalize dispatches to the selected provider's explicit player-identity adapter.
func normalize(raw map[string]any, source, puuid string, db domain.Database) (domain.Match, bool) {
	if source == "SGP" {
		return domain.NormalizeSGP(raw, puuid, db)
	}
	return domain.NormalizeLCU(raw, puuid, db)
}

// rawID reads the documented provider envelope's stable numeric match identifier.
func rawID(raw map[string]any, source string) uint64 {
	if source == "SGP" {
		raw = domain.Object(raw["json"])
	}
	return uint64(domain.Number(raw["gameId"]))
}

// cacheKey isolates equal numeric match identifiers by current region and platform.
func cacheKey(a Access, id uint64) string {
	return strings.ToUpper(a.Region) + "/" + strings.ToUpper(a.Platform) + ":" + strconv.FormatUint(id, 10)
}

// cacheGet returns one immutable compact observation after releasing the shared cache lock.
// Decoding happens outside the lock, so a large match cannot block unrelated cache
// users. Successful hits refresh least-recently-used ordering before returning shared bytes.
func (r *Repository) cacheGet(key string) (map[string]any, string, bool) {
	r.mu.Lock()
	entry, ok := r.cache[key]
	if ok {
		r.clock++
		entry.Used = r.clock
		r.cache[key] = entry
	}
	r.mu.Unlock()
	if !ok {
		return nil, "", false
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(entry.Bytes))
	decoder.UseNumber()
	if decoder.Decode(&raw) != nil {
		return nil, "", false
	}
	return raw, entry.Source, true
}

// cachePut retains compact perspective-independent bytes within both count and payload budgets.
// Encoding happens before taking the cache mutex. Replacing or evicting an entry
// updates the exact retained payload count, and oversized observations are not cached.
// Eviction removes the least recently used entries until both constraints hold,
// including when replacing an existing entry increases its payload size. The
// serialized allocation is the measured budget; temporary decoder trees are not
// retained in the shared cache and therefore cannot silently expand its payload.
func (r *Repository) cachePut(key string, raw map[string]any, source string) {
	data, err := json.Marshal(raw)
	if err != nil || len(data) > maxBytes {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Replace accounting and evictions under one lock so another caller cannot observe a cache whose entries exceed its measured payload.
	if old, ok := r.cache[key]; ok {
		r.bytes -= len(old.Bytes)
	}
	r.clock++
	r.cache[key] = cacheEntry{Bytes: data, Source: source, Used: r.clock}
	r.bytes += len(data)
	for len(r.cache) > maxGames || r.bytes > maxBytes {
		oldest := ""
		used := ^uint64(0)
		for k, v := range r.cache {
			if v.Used < used {
				oldest = k
				used = v.Used
			}
		}
		r.bytes -= len(r.cache[oldest].Bytes)
		delete(r.cache, oldest)
	}
}

// Detail returns one player's perspective from a shared complete match observation.
// Concurrent requests for the same region and match share in-flight ownership.
// Waiters recheck the cache when an owner completes, and a canceled owner closes
// its completion signal so another waiter can retry. SGP detail must contain the
// requested player's complete roster before replacing LCU fallback; successful partial
// local detail remains inspectable but is never cached as complete coverage.
func (r *Repository) Detail(ctx context.Context, a Access, puuid string, id uint64) (domain.Match, error) {
	if domain.Identity(puuid) == "" || id == 0 {
		return domain.Match{}, errors.New("比赛身份不可用")
	}
	key := cacheKey(a, id)
	db := r.catalog()
	for {
		if raw, source, ok := r.cacheGet(key); ok {
			if m, valid := normalize(raw, source, puuid, db); valid {
				return m, nil
			}
		}
		r.mu.Lock()
		flight, waiting := r.flights[key]
		if !waiting {
			// Publish one completion signal before network work; releasing ownership closes it even on cancellation so waiting callers cannot remain stranded.
			flight = make(chan struct{})
			r.flights[key] = flight
		}
		r.mu.Unlock()
		if !waiting {
			defer func() { r.mu.Lock(); delete(r.flights, key); close(flight); r.mu.Unlock() }()
			break
		}
		select {
		case <-ctx.Done():
			return domain.Match{}, ctx.Err()
		case <-flight:
		}
	}
	// An earlier owner may have populated the cache between our initial lookup and ownership admission; recheck before starting another authenticated read.
	if raw, source, ok := r.cacheGet(key); ok {
		if m, valid := normalize(raw, source, puuid, db); valid {
			return m, nil
		}
	}
	if access, err := r.access(ctx, a); err == nil {
		var raw map[string]any
		path := fmt.Sprintf("/match-history-query/v1/products/lol/%s_%d/SUMMARY", url.PathEscape(access.Path), id)
		err = r.admitted(ctx, func() error { return remoteGet(ctx, access, path, &raw) })
		if err == nil {
			if m, ok := normalize(raw, "SGP", puuid, db); ok && m.Complete() {
				r.cachePut(key, raw, "SGP")
				return m, nil
			}
		}
	}
	var raw map[string]any
	// Incomplete or unreadable SGP detail cannot establish contribution coverage; the current-region local endpoint remains the only fallback for this perspective.
	err := r.admitted(ctx, func() error { return Get(ctx, a, fmt.Sprintf("/lol-match-history/v1/games/%d", id), &raw) })
	if err != nil {
		return domain.Match{}, err
	}
	m, ok := normalize(raw, "LCU", puuid, db)
	if !ok {
		return domain.Match{}, errors.New("比赛缺少该玩家的明确记录")
	}
	if m.Complete() {
		r.cachePut(key, raw, "LCU")
	}
	return m, nil
}

// RawDetail returns current-client match identities for explicitly labeled historical roster review.
// This operation reads LCU because its participant identities include public account
// names. It does not persist account observations or infer missing roster identities.
func (r *Repository) RawDetail(ctx context.Context, a Access, id uint64) (map[string]any, error) {
	var raw map[string]any
	err := r.admitted(ctx, func() error { return Get(ctx, a, fmt.Sprintf("/lol-match-history/v1/games/%d", id), &raw) })
	return raw, err
}

// RawTimeline reads observed client frames without synthesizing unsupported event details.
// Callers verify visible match ownership before requesting this numeric game identifier.
func (r *Repository) RawTimeline(ctx context.Context, a Access, id uint64) (map[string]any, error) {
	var raw map[string]any
	err := r.admitted(ctx, func() error { return Get(ctx, a, fmt.Sprintf("/lol-match-history/v1/game-timelines/%d", id), &raw) })
	return raw, err
}

// refreshDirectory attempts public configuration refresh independently from authenticated history work.
// The embedded validated baseline stays available immediately, and malformed or failed
// remote responses cannot replace it. Attempts are spaced six hours per repository.
// The originating request context bounds the background attempt, and its timeout
// closes any remaining transport work. Active routing changes only after the
// entire replacement document passes validation, while failed mirrors leave all
// existing callers on the previously published baseline rather than clearing routes.
func (r *Repository) refreshDirectory(ctx context.Context) {
	r.mu.Lock()
	if time.Since(r.refreshAt) < 6*time.Hour {
		r.mu.Unlock()
		return
	}
	r.refreshAt = time.Now()
	r.mu.Unlock()
	// Publish the attempt time before spawning so simultaneous player pipelines share one refresh instead of issuing ten independent configuration downloads.
	go func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		for _, origin := range []string{"https://raw.githubusercontent.com/LeagueAkari/LeagueAkari-Config/refs/heads/main/config/sgp/league-servers.json", "https://gitee.com/LeagueAkari/LeagueAkari-Config/raw/main/config/sgp/league-servers.json"} {
			req, err := http.NewRequestWithContext(refreshCtx, http.MethodGet, origin, nil)
			if err != nil {
				continue
			}
			var raw json.RawMessage
			if decode(remoteHTTP, req, &raw) != nil {
				continue
			}
			d, err := parseDirectory(raw)
			if err != nil {
				continue
			}
			r.mu.Lock()
			r.directory = d
			r.mu.Unlock()
			return
		}
	}()
}
