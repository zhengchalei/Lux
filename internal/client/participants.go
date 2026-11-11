package client

import (
	"context"
	"errors"
	"time"

	"lux/internal/domain"
)

// participants.go loads profiles reached through an observed match's public roster.
// A lookup carries the parent perspective, recorded game, and target PUUID; names
// never authorize or route the query. One explorer retains a bounded set of
// profiles outside periodic snapshots, allowing its match details, replay checks,
// and subsequent participant links to use the same exact recorded identities.
//
// The runtime mutex coordinates explorer lifetime and account generations. A new
// request cancels the preceding read, and only the latest generation can retain
// a result. Closing clears the private profiles and revokes pending reads before
// returning; a delayed close from an old explorer cannot clear a newer explorer.
// Account replacement uses the same cleanup, preventing cross-account reuse.

// participantLimit bounds retained profiles and mirrors the renderer's back-history depth.
const participantLimit = 6

// clearParticipantsLocked revokes explorer transport and releases all private player observations.
// The caller holds mu. Incrementing the request generation also invalidates reads
// that finish despite cancellation, before any new explorer can reuse its keys.
func (r *Runtime) clearParticipantsLocked() {
	if r.participantCancel != nil {
		r.participantCancel()
	}
	r.participantCancel = nil
	r.participantEpoch++
	r.participantKey = ""
	r.participantProfiles = nil
}

// CloseParticipantExplorer releases only the explicitly named explorer and its in-flight read.
// Calls for a superseded explorer do nothing. The calling renderer invokes this
// when dismissing its dialog, independently from its global history query state.
func (r *Runtime) CloseParticipantExplorer(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key != "" && key == r.participantKey {
		r.clearParticipantsLocked()
	}
}

// participantMatchLocked finds the exact parent observation authorized for this explorer.
// The caller holds mu. A new explorer can start from global history or scouting
// only; private profiles authorize navigation solely within their current explorer.
func (r *Runtime) participantMatchLocked(key, owner string, id uint64) (domain.Match, bool) {
	profiles := []*domain.Profile{r.state.History, r.state.Query, r.state.Scouts[owner].Profile}
	if key == r.participantKey {
		profiles = append(profiles, r.participantProfiles[owner])
	}
	for _, profile := range profiles {
		if profile == nil || profile.PUUID != owner {
			continue
		}
		for _, match := range profile.Matches {
			if match.ID == id {
				return match, true
			}
		}
	}
	return domain.Match{}, false
}

// GetParticipantProfile reads a public match participant without replacing the main query.
// The parent match must already belong to a loaded visible profile, and its
// recorded roster must explicitly contain the target PUUID. Unknown aliases are
// never resolved by name, preventing same-name accounts from being substituted.
//
// Admission captures the account and explorer generation before network work.
// A new request, explorer dismissal, parent replacement, or account transition
// revokes publication. Successful profiles remain private to this explorer, so
// periodic snapshots do not grow while the user browses several player pages.
// The retained list names at most five previously validated back-trail profiles;
// unused branches are dropped. The returned value is detached and may be mutated
// by the caller without changing later match membership checks.
func (r *Runtime) GetParticipantProfile(caller context.Context, key, owner string, id uint64, target string, retained []string) (domain.Profile, error) {
	if domain.Identity(key) == "" || domain.Identity(owner) == "" || domain.Identity(target) == "" || id == 0 {
		return domain.Profile{}, errors.New("玩家或对局标识不可用")
	}
	if len(retained) >= participantLimit {
		return domain.Profile{}, errors.New("玩家浏览路径过长，请返回后重试")
	}
	r.mu.Lock()
	match, found := r.participantMatchLocked(key, owner, id)
	member := false
	for _, player := range match.Players {
		member = member || player.PUUID == target
	}
	if r.closed || r.access == nil || r.session == nil || !r.state.Connected || !found || !member {
		r.mu.Unlock()
		return domain.Profile{}, errors.New("该玩家不在当前可查看的对局中，请重新打开详情")
	}
	if r.participantKey != key {
		r.clearParticipantsLocked()
		r.participantKey = key
		r.participantProfiles = map[string]*domain.Profile{}
	}
	if r.participantCancel != nil {
		r.participantCancel()
	}
	// Explicit profile navigation receives the foreground admission slot. A bounded
	// lifetime and account cancellation prevent a dismissed page from retaining transport indefinitely.
	ctx, cancel := context.WithTimeout(caller, 45*time.Second)
	stop := context.AfterFunc(r.session, cancel)
	r.participantCancel = cancel
	r.participantEpoch++
	request, epoch, access, count := r.participantEpoch, r.epoch, *r.access, r.state.Analysis.MatchCount
	r.mu.Unlock()
	defer cancel()
	defer stop()
	profile, err := r.loadProfile(ctx, access, "", target, count, 0, false, func(domain.Profile) {})
	if err != nil {
		return domain.Profile{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil || r.closed || epoch != r.epoch || key != r.participantKey || request != r.participantEpoch {
		return domain.Profile{}, errors.New("玩家页面已关闭或查询对象已变化")
	}
	current, found := r.participantMatchLocked(key, owner, id)
	member = false
	for _, player := range current.Players {
		member = member || player.PUUID == target
	}
	if !found || !member {
		return domain.Profile{}, errors.New("来源对局已变化，请重新打开详情")
	}
	// The renderer retains an actual back trail rather than every previously opened
	// branch. Keep only already validated profiles named by that trail and this result.
	keep := map[string]bool{target: true}
	for _, puuid := range retained {
		keep[puuid] = true
	}
	for puuid := range r.participantProfiles {
		if !keep[puuid] {
			delete(r.participantProfiles, puuid)
		}
	}
	r.participantProfiles[target] = &profile
	return clone(profile), nil
}
