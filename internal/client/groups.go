package client

import (
	"context"
	"strconv"
	"strings"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// groups.go supplements a public live roster with direct client grouping evidence.
// LCU observations have priority; GSM can fill missing groups for already visible
// players but never supplies hidden identities or expands the current roster.
// Group identifiers are meaningful only within one account and live game session.
//
// Optional GSM reads run outside snapshot locks and are limited to one concurrent
// request per game, with a one-minute retry interval. Account and game association
// are rechecked before publication. A missing response preserves ordinary rosters
// and historical inference, while subsequent snapshots reuse the latest matching
// immutable membership without introducing polling or pointer-driven game requests.

// liveGroups retains one game-scoped optional grouping request and its observed membership.
type liveGroups struct {
	// Key binds At, Loading, and Members to one account-generation-owned request
	// record; callbacks publish only while the runtime retains this same record.
	Key     string            // Exact current live roster association.
	At      time.Time         // Last optional request admission instant.
	Loading bool              // One current optional request admitted.
	Members map[string]string // Public identifiers to session-local group.
}

// participantGroup accepts only a positive client-published group identifier.
func participantGroup(p map[string]any) string {
	id := domain.Number(p["teamParticipantId"])
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// mergeLiveGroups fills missing groups only for explicitly public current roster members.
// Callers own the roster or hold the runtime mutex. Existing LCU groups retain
// priority, and additional supplemental identities never expand the current roster.
func mergeLiveGroups(b *domain.Battle, members map[string]string) {
	for i := range b.Players {
		p := &b.Players[i]
		if !p.Hidden && p.PUUID != "" && p.Group == "" && members[p.PUUID] != "" {
			p.Group, p.GroupSource = members[p.PUUID], "SGP GSM"
		}
	}
}

// applyLiveGroups reuses a matching immutable observation before fingerprinting a live roster.
// The captured account generation and exact game key must match. Existing direct
// LCU grouping remains authoritative over optional supplemental GSM observations.
func (r *Runtime) applyLiveGroups(b *domain.Battle, epoch uint64) {
	if b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.epoch == epoch && r.groups != nil && r.groups.Key == b.Key {
		mergeLiveGroups(b, r.groups.Members)
	}
}

// ensureLiveGroups schedules optional read-only supplementation for the current live game.
// A game key owns its admission state and account cancellation context. Repeated
// snapshots reuse that state, with no retry before one minute and no admission
// during champion select or historical review. A response is discarded when its
// account, group record pointer, or live game has changed while transport progressed.
// Successful public memberships fill missing LCU groups before one revision is
// published; failures do not overwrite existing public roster metadata or profiles.
func (r *Runtime) ensureLiveGroups() {
	r.mu.Lock()
	b := r.state.Battle
	if r.closed || r.access == nil || b == nil || !strings.HasPrefix(b.Key, "game:") {
		r.mu.Unlock()
		return
	}
	if r.groups == nil || r.groups.Key != b.Key {
		// A different game gets independent admission state so its reply cannot
		// reuse or publish the previous game's membership through this record.
		r.groups = &liveGroups{Key: b.Key}
	}
	job := r.groups
	if job.Loading || time.Since(job.At) < time.Minute {
		r.mu.Unlock()
		return
	}
	job.Loading, job.At = true, time.Now()
	access, epoch, ctx := *r.access, r.epoch, r.session
	r.mu.Unlock()
	r.spawn(func() {
		// Optional transport outlives this snapshot read, but the captured session
		// cancellation and publication checks keep ownership tied to the live game.
		request, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		members, err := r.repository.LiveGroups(request, access, history.GameID(job.Key))
		r.mu.Lock()
		// Account replacement, review navigation, or a newer game invalidates the
		// captured record before any returned memberships can change visible state.
		if r.closed || r.epoch != epoch || r.groups != job || r.state.Battle == nil || r.state.Battle.Key != job.Key {
			r.mu.Unlock()
			return
		}
		job.Loading = false
		if err != nil || request.Err() != nil {
			r.mu.Unlock()
			return
		}
		job.Members = members
		mergeLiveGroups(r.state.Battle, members)
		r.state.Revision++
		listener := r.listener
		r.mu.Unlock()
		if listener != nil {
			listener()
		}
	})
}
