package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"lux/internal/domain"
)

// selected_review.go opens any already observed match in the ten-player assistant.
// The selected record supplies identities, champions, teams, and historical time;
// current player histories supply the existing recent-performance cards. Those
// observations have different time scopes, so the board carries explicit review
// metadata rather than deriving its date from a player's latest history window.
//
// A selected review pins only the visible board. Client synchronization retains
// its latest live roster separately, allowing an explicit return without losing
// real-time identity updates. Account replacement and newer review commands revoke
// pending transport and publication together. Match records must already belong
// to a loaded public profile; renderer-supplied rosters never authorize queries.

// ReviewMatch loads one recorded perspective into the historical assistant workspace.
// The caller supplies a loaded history owner and exact game identifier. Incomplete
// records receive a bounded detail read, while complete records reuse observations.
//
// Request ownership is claimed before transport so a newer match or a return to
// the live board supersedes an older read. Publication rechecks account lifetime,
// request generation, and source membership. Failure preserves the previous board;
// success pins this historical roster and restarts independent recent-player jobs.
func (r *Runtime) ReviewMatch(caller context.Context, owner string, id uint64) error {
	r.mu.Lock()
	match, found := r.replayMatchLocked(owner, id)
	if r.closed || r.access == nil || r.session == nil || !r.state.Connected || !found {
		r.mu.Unlock()
		return errors.New("请连接客户端并重新打开该对局")
	}
	access, epoch, session := *r.access, r.epoch, r.session
	if match.PlatformID != "" && access.Platform != "" && match.PlatformID != access.Platform {
		r.mu.Unlock()
		return errors.New("该对局不属于当前客户端大区")
	}
	// The loaded record is detached before optional transport so account refresh
	// cannot mutate this request's participant perspective while its read is pending.
	match = clone(match)
	if r.reviewCancel != nil {
		r.reviewCancel()
	}
	ctx, cancel := context.WithTimeout(caller, 12*time.Second)
	stop := context.AfterFunc(session, cancel)
	r.reviewCancel = cancel
	r.reviewEpoch++
	request := r.reviewEpoch
	r.pendingReview, r.reviewing = false, false
	r.mu.Unlock()
	defer cancel()
	defer stop()
	if !match.Complete() {
		var err error
		match, err = r.repository.Detail(ctx, access, owner, id)
		if err != nil {
			return err
		}
	}
	if match.ID != id {
		return errors.New("返回的比赛与所选对局不一致")
	}
	battle := selectedBattle(match, owner)
	if battle == nil {
		return errors.New("该对局缺少完整十人阵容，暂时无法分析")
	}
	r.mu.Lock()
	_, visible := r.replayMatchLocked(owner, id)
	if r.closed || r.epoch != epoch || r.reviewEpoch != request || ctx.Err() != nil || session.Err() != nil || !visible {
		r.mu.Unlock()
		return errors.New("对局或账号已变化，请重新打开")
	}
	// Commit context and roster together before refreshing players. The pinned
	// selection prevents an unrelated live snapshot from replacing this historical board.
	if r.rosterCancel != nil {
		r.rosterCancel()
	}
	r.rosterEpoch++
	r.state.Battle, r.reviewPinned = battle, true
	r.state.Scouts = map[string]domain.Scout{}
	r.mu.Unlock()
	return r.RefreshBattle()
}

// selectedBattle maps a complete recorded perspective into its exact public lineup.
// The viewed player's team becomes the friendly side even when viewing another
// summoner's history. Missing public identities remain hidden and acquire no jobs.
func selectedBattle(match domain.Match, owner string) *domain.Battle {
	if !match.Complete() {
		return nil
	}
	battle := &domain.Battle{Key: fmt.Sprintf("review:%d", match.ID), Source: "历史复盘 · " + match.Queue, Mode: domain.ModeFromQueue(match.QueueID, false), QueueID: match.QueueID, Review: &domain.BattleReview{ID: match.ID, Owner: owner, Created: match.Created}}
	for _, p := range match.Players {
		battle.Players = append(battle.Players, domain.Combatant{PUUID: p.PUUID, Name: p.Name, Champion: p.ChampionID, Position: p.Position, Ally: p.Team == match.Team, Hidden: domain.Identity(p.PUUID) == "", Locked: true})
		if p.PUUID == owner && p.Team == match.Team {
			battle.Review.Name = p.Name
		}
	}
	if battle.Review.Name == "" {
		return nil
	}
	return battle
}

// ReturnToLive leaves historical inspection and restores the latest observed public roster.
// It revokes pending review responses first. When the client has no active roster,
// the assistant returns to its normal waiting state rather than retaining history.
// Current historical scouting jobs are revoked under the same mutex as the board
// replacement, preventing their late profiles from entering the restored live roster.
// Player loading resumes outside the lock and carries a new roster generation;
// a listener notification also exposes the idle state when no live game exists.
func (r *Runtime) ReturnToLive() error {
	r.mu.Lock()
	if r.reviewCancel != nil {
		r.reviewCancel()
	}
	r.reviewEpoch++
	r.pendingReview, r.reviewing, r.reviewPinned = false, false, false
	r.state.Battle = r.liveBattle
	if r.rosterCancel != nil {
		r.rosterCancel()
	}
	r.rosterEpoch++
	r.state.Scouts = map[string]domain.Scout{}
	r.state.Revision++
	active, listener := r.state.Battle != nil, r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
	if active {
		return r.RefreshBattle()
	}
	return nil
}
