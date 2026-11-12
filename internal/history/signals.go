package history

import (
	"context"
	"fmt"
	"sort"
	"time"

	"lux/internal/domain"
)

// signals.go enriches already visible histories with bounded early-gank evidence.
// At most ten recent Rift timelines are attempted within one optional work budget;
// normal profile availability does not depend on this enrichment succeeding. Raw
// timeline trees are discarded after extracting compact death timestamps by slot.
//
// A per-account and server cache shares successful observations across player
// perspectives and briefly retains unavailable results. Completion channels share
// concurrent reads; cancellation never caches a missing result. All network work
// uses repository admission outside its mutex, while bounded cache entries contain
// no tokens or full frames. Mouse and hover interactions never start these jobs.

// gankEntry retains immutable slot-indexed observations and a short failure cooldown.
type gankEntry struct {
	Players map[int]*domain.EarlyGank // Nil means unavailable timeline evidence.
	At      time.Time                 // Insertion time for failure expiry.
}

// earlyGanks validates one timeline before associating kills with normalized public slots.
// Only Rift matches with ten unique participant slots qualify. Explicit enemy
// jungle positions take precedence over smite; missing identification excludes that
// side instead of counting an unknown enemy's participation as an observed zero.
//
// All kill events through fifteen minutes are considered, including the exact
// boundary. Victim-and-time keys deduplicate repeated source events. Frame coverage
// must reach that boundary, or the final minute for a shorter completed match,
// so a truncated response cannot make a player appear difficult to gank merely
// because their later deaths were absent from the response.
func earlyGanks(m domain.Match, raw map[string]any) map[int]*domain.EarlyGank {
	if !domain.Usable(m, domain.Ranked) || !m.Complete() {
		return nil
	}
	players := map[int]domain.Player{}
	junglers := map[int]map[int]bool{}
	for _, p := range m.Players {
		if p.ParticipantID < 1 || p.ParticipantID > 10 {
			return nil
		}
		if _, exists := players[p.ParticipantID]; exists {
			return nil
		}
		players[p.ParticipantID] = p
	}
	for _, team := range []int{100, 200} {
		ids := map[int]bool{}
		for _, p := range m.Players {
			if p.Team == team && p.Position == "JUNGLE" {
				ids[p.ParticipantID] = true
			}
		}
		if len(ids) == 0 {
			for _, p := range m.Players {
				if p.Team == team && p.IsJungler() {
					ids[p.ParticipantID] = true
				}
			}
		}
		junglers[team] = ids
	}
	frames, ok := raw["frames"].([]any)
	if !ok || len(frames) < 2 || len(frames) > 512 {
		return nil
	}
	last := int64(0)
	for _, v := range frames {
		f := domain.Object(v)
		at := domain.Number(f["timestamp"])
		last = max(last, at)
		if _, ok := f["events"].([]any); !ok {
			return nil
		}
	}
	// A complete minute boundary covers the full fifteen-minute comparison window;
	// shorter games may finish between samples and admit their final partial minute.
	if m.Duration >= 900 && last < 900000 || m.Duration < 900 && last+60000 < int64(m.Duration)*1000 {
		return nil
	}
	result := map[int]*domain.EarlyGank{}
	for _, p := range players {
		if !p.IsJungler() && (p.Position != "" || p.Spell1 > 0 && p.Spell2 > 0) && len(junglers[300-p.Team]) > 0 {
			result[p.ParticipantID] = &domain.EarlyGank{Deaths: []int64{}}
		}
	}
	seen := map[string]bool{}
	for _, v := range frames {
		for _, e := range domain.Array(domain.Object(v)["events"]) {
			event := domain.Object(e)
			if domain.Text(event["type"]) != "CHAMPION_KILL" {
				continue
			}
			at := domain.Number(event["timestamp"])
			if event["timestamp"] == nil || at < 0 {
				return nil
			}
			if at > 900000 {
				continue
			}
			victim := int(domain.Number(event["victimId"]))
			sample := result[victim]
			if sample == nil {
				continue
			}
			key := fmt.Sprintf("%d:%d", victim, at)
			if seen[key] {
				continue
			}
			seen[key] = true
			enemies := junglers[300-players[victim].Team]
			involved := enemies[int(domain.Number(event["killerId"]))]
			for _, a := range domain.Array(event["assistingParticipantIds"]) {
				involved = involved || enemies[int(domain.Number(a))]
			}
			if involved {
				sample.Deaths = append(sample.Deaths, at)
			}
		}
	}
	for _, p := range result {
		sort.Slice(p.Deaths, func(i, j int) bool { return p.Deaths[i] < p.Deaths[j] })
	}
	return result
}

// gankData shares one compact game observation across independent profile pipelines.
// Successful entries survive until FIFO capacity eviction; unavailable entries expire
// after one minute. A cancelled reader releases waiters without storing failure.
//
// The account/server key prevents a different local account from inheriting failure
// cooldowns. Network and extraction run outside mu; cache publication and channel
// completion happen together so waiters either reuse immutable observations or
// become the next owner after cancellation. Only 128 compact games are retained,
// independently of the full-detail cache and the explicit review timeline cache.
func (r *Repository) gankData(ctx context.Context, a Access, m domain.Match) map[int]*domain.EarlyGank {
	key := "gank:" + a.Account + ":" + cacheKey(a, m.ID)
	for {
		r.mu.Lock()
		entry, exists := r.ganks[key]
		if exists && (entry.Players != nil || time.Since(entry.At) < time.Minute) {
			r.mu.Unlock()
			return entry.Players
		}
		if wait := r.flights[key]; wait != nil {
			r.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil
			}
		}
		done := make(chan struct{})
		r.flights[key] = done
		r.mu.Unlock()
		raw, err := r.RawTimeline(ctx, a, m.ID)
		var result map[int]*domain.EarlyGank
		if err == nil {
			result = earlyGanks(m, raw)
		}
		r.mu.Lock()
		if ctx.Err() == nil {
			if r.ganks == nil {
				r.ganks = map[string]gankEntry{}
			}
			if len(r.ganks) >= 128 {
				oldest := ""
				var at time.Time
				for k, v := range r.ganks {
					if oldest == "" || v.At.Before(at) {
						oldest, at = k, v.At
					}
				}
				delete(r.ganks, oldest)
			}
			r.ganks[key] = gankEntry{Players: result, At: time.Now()}
		}
		delete(r.flights, key)
		close(done)
		r.mu.Unlock()
		return result
	}
}

// EnrichEarlyGanks supplements a privately owned profile after its first publication.
// Ten qualifying recent games and eight seconds bound this optional phase. Source
// failures preserve normal histories and missing observations remain nil, while two
// consecutive unavailable timelines stop further network attempts for this profile.
func (r *Repository) EnrichEarlyGanks(ctx context.Context, a Access, p *domain.Profile) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	attempts, failures := 0, 0
	for i, m := range p.Matches {
		if attempts >= 10 || failures >= 2 || ctx.Err() != nil {
			break
		}
		if !domain.Usable(m, domain.Ranked) || !m.Complete() {
			continue
		}
		// Numeric game identifiers are server-local, so a foreign-platform record
		// must not request or reuse the connected platform's unrelated timeline.
		if m.PlatformID != "" && a.Platform != "" && m.PlatformID != a.Platform {
			continue
		}
		var own domain.Player
		for _, v := range m.Players {
			if v.Local {
				own = v
				break
			}
		}
		if own.IsJungler() || own.ParticipantID == 0 {
			continue
		}
		attempts++
		request, stop := context.WithTimeout(ctx, 2*time.Second)
		data := r.gankData(request, a, m)
		stop()
		if data == nil {
			failures++
			continue
		}
		failures = 0
		p.Matches[i].EarlyGank = data[own.ParticipantID]
	}
}
