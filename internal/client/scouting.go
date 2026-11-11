package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// scouting.go associates independent player histories with public roster generations.
// Public account identifiers determine query ownership; card position never does.
// Replacing a roster cancels previous jobs before starting the new set, while
// refreshing the same roster preserves successful observations during loading.
// A hidden identity has no query job and cannot be passed through a retry path.
//
// Historical review has its own explicit source label and account generation.
// It obtains player identities from the complete historical observation, never
// from champion names or team guesses. The active gameflow roster takes priority
// over a pending review, so a late history reply cannot overwrite a current game.

// RefreshBattle restarts current public-player pipelines while retaining same-roster observations.
// No roster is synthesized when the client has not exposed one. Requested depth
// and exact queue scope are captured before any request is admitted.
func (r *Runtime) RefreshBattle() error {
	r.mu.Lock()
	if r.access == nil || r.state.Battle == nil {
		r.mu.Unlock()
		return errors.New("等待客户端公开阵容")
	}
	if r.rosterCancel != nil {
		r.rosterCancel()
	}
	ctx, cancel := context.WithCancel(r.session)
	r.roster = ctx
	r.rosterCancel = cancel
	r.rosterEpoch++
	access, epoch, roster, count := *r.access, r.epoch, r.rosterEpoch, r.state.Analysis.MatchCount
	battle := clone(*r.state.Battle)
	queue := 0
	if r.state.Analysis.SameMode {
		queue = battle.QueueID
	}
	previous := r.state.Scouts
	r.state.Scouts = map[string]domain.Scout{}
	identities := map[string]bool{}
	for _, player := range battle.Players {
		if player.Hidden || domain.Identity(player.PUUID) == "" || identities[player.PUUID] {
			continue
		}
		identities[player.PUUID] = true
		scout := previous[player.PUUID]
		if scout.Profile != nil && (scout.Profile.Requested != count || scout.Profile.Coverage.QueueID != queue) {
			scout.Profile = nil
		}
		scout.Loading, scout.Error = true, ""
		r.state.Scouts[player.PUUID] = scout
	}
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
	for puuid := range identities {
		r.startScout(ctx, access, epoch, roster, puuid, count, queue)
	}
	return nil
}

// startScout publishes progressive player results only to their captured roster generation.
func (r *Runtime) startScout(ctx context.Context, access history.Access, epoch, roster uint64, puuid string, count, queue int) {
	r.spawn(func() {
		publish := func(profile domain.Profile) {
			r.publishIf(epoch, 0, roster, func() {
				scout := r.state.Scouts[puuid]
				scout.Profile, scout.Loading, scout.Error = &profile, true, ""
				r.state.Scouts[puuid] = scout
			})
		}
		profile, err := r.loadProfile(ctx, access, "", puuid, count, queue, true, publish)
		r.publishIf(epoch, 0, roster, func() {
			scout := r.state.Scouts[puuid]
			scout.Loading = false
			if err == nil {
				scout.Profile, scout.Error = &profile, profile.HistoryError
			} else {
				scout.Error = "读取失败：" + err.Error()
			}
			r.state.Scouts[puuid] = scout
		})
	})
}

// RetryScout restarts one visible current-room player without clearing other players.
// Hidden identities and stale room IDs are rejected. An in-progress player keeps
// its existing pipeline rather than occupying another profile admission slot.
func (r *Runtime) RetryScout(puuid string) error {
	r.mu.Lock()
	if r.access == nil || r.state.Battle == nil {
		r.mu.Unlock()
		return errors.New("当前没有可重试的阵容")
	}
	visible := false
	for _, player := range r.state.Battle.Players {
		visible = visible || !player.Hidden && player.PUUID == puuid
	}
	if !visible || domain.Identity(puuid) == "" {
		r.mu.Unlock()
		return errors.New("该玩家身份未公开或已离开当前阵容")
	}
	scout := r.state.Scouts[puuid]
	if scout.Loading {
		r.mu.Unlock()
		return nil
	}
	scout.Loading, scout.Error = true, ""
	r.state.Scouts[puuid] = scout
	access, ctx, epoch, roster, count := *r.access, r.roster, r.epoch, r.rosterEpoch, r.state.Analysis.MatchCount
	if ctx == nil {
		ctx = r.session
	}
	queue := 0
	if r.state.Analysis.SameMode {
		queue = r.state.Battle.QueueID
	}
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
	r.startScout(ctx, access, epoch, roster, puuid, count, queue)
	return nil
}

// ReviewLastGame asynchronously reads an explicitly labeled historical ten-player roster.
// Before authentication this request remains pending once. Active selection and
// in-game states reject review rather than replacing the current public roster.
func (r *Runtime) ReviewLastGame() error {
	r.mu.Lock()
	if r.access == nil || r.state.Account == nil {
		r.pendingReview = true
		r.mu.Unlock()
		return nil
	}
	if r.state.Phase == "ChampSelect" || r.state.Phase == "InProgress" || r.state.Phase == "GameStart" || r.state.Phase == "Reconnect" {
		r.pendingReview = false
		r.mu.Unlock()
		return errors.New("当前处于选人或对局，请查看实时阵容")
	}
	if r.reviewing {
		r.mu.Unlock()
		return nil
	}
	r.pendingReview, r.reviewing = false, true
	access, ctx, epoch := *r.access, r.session, r.epoch
	var matches []domain.Match
	if r.state.History != nil {
		matches = append(matches, r.state.History.Matches...)
	}
	r.mu.Unlock()
	r.spawn(func() {
		if len(matches) == 0 {
			profile, err := r.repository.Load(ctx, access, access.Account, 20, 0)
			if err != nil {
				r.finishReview(epoch, nil, "复盘读取失败："+err.Error())
				return
			}
			matches = profile.Matches
		}
		for _, match := range matches {
			if match.Remake || match.Duration < 180 || strings.Contains(match.GameType, "CUSTOM") {
				continue
			}
			raw, err := r.repository.RawDetail(ctx, access, match.ID)
			if err != nil {
				continue
			}
			battle := finishedBattle(raw, access.Account, match)
			if battle != nil {
				r.finishReview(epoch, battle, "")
				return
			}
		}
		r.finishReview(epoch, nil, "近期没有可读取双方十人的匹配对局")
	})
	return nil
}

// finishReview rejects late historical replies after live selection or gameplay begins.
func (r *Runtime) finishReview(epoch uint64, battle *domain.Battle, message string) {
	accepted := false
	r.publishIf(epoch, 0, 0, func() {
		r.reviewing = false
		if r.state.Phase == "ChampSelect" || r.state.Phase == "InProgress" || r.state.Phase == "GameStart" || r.state.Phase == "Reconnect" {
			return
		}
		if battle != nil {
			r.state.Battle = battle
			accepted = true
		} else {
			r.state.Status = message
		}
	})
	if accepted {
		_ = r.RefreshBattle()
	}
}

// finishedBattle associates historical public identities with the recorded perspective's team.
// It accepts either LCU identity arrays or SGP participant PUUID fields. Every
// participant must provide enough identity/side data to retain ten-player integrity.
func finishedBattle(raw map[string]any, own string, match domain.Match) *domain.Battle {
	if wrapped := object(raw["json"]); wrapped != nil {
		raw = wrapped
	}
	participants := array(raw["participants"])
	if len(participants) != 10 {
		return nil
	}
	identities := array(raw["participantIdentities"])
	battle := &domain.Battle{Key: fmt.Sprintf("review:%d", match.ID), Source: "历史复盘 · " + match.Queue, Mode: domain.ModeFromQueue(match.QueueID, false), QueueID: match.QueueID, Players: []domain.Combatant{}}
	for _, source := range participants {
		participant := object(source)
		person := participant
		for _, identity := range identities {
			row := object(identity)
			if number(row["participantId"]) == number(participant["participantId"]) {
				person = object(row["player"])
				break
			}
		}
		puuid := domain.Identity(person["puuid"])
		name := domain.DisplayName(person)
		if name == "召唤师" && text(person["riotIdGameName"]) != "" {
			name = text(person["riotIdGameName"])
			if tag := text(person["riotIdTagline"]); tag != "" {
				name += "#" + tag
			}
		}
		battle.Players = append(battle.Players, domain.Combatant{PUUID: puuid, Name: name, Position: text(object(participant["timeline"])["lane"]), Champion: number(participant["championId"]), Ally: number(participant["teamId"]) == match.Team, Hidden: puuid == "", Locked: true})
	}
	visibleOwn := false
	for _, player := range battle.Players {
		visibleOwn = visibleOwn || player.PUUID == own && player.Ally
	}
	if !visibleOwn {
		return nil
	}
	return battle
}

// Detail reads one current-visible player's match and updates corresponding prepared history.
// The returned record is rejected when account or query ownership changes during
// loading. Details cannot be requested for arbitrary hidden roster identities.
func (r *Runtime) Detail(ctx context.Context, puuid string, id uint64) (domain.Match, error) {
	r.mu.Lock()
	if r.access == nil || !r.visibleIdentityLocked(puuid) {
		r.mu.Unlock()
		return domain.Match{}, errors.New("该玩家不是当前可见账号")
	}
	access, epoch := *r.access, r.epoch
	session := r.session
	r.mu.Unlock()
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	record, err := r.repository.Detail(owned, access, puuid, id)
	if err != nil {
		return record, err
	}
	r.mu.Lock()
	if epoch != r.epoch || session.Err() != nil || !r.visibleIdentityLocked(puuid) {
		r.mu.Unlock()
		return domain.Match{}, errors.New("账号或查询对象已变化")
	}
	update := func(profile *domain.Profile) {
		if profile == nil || profile.PUUID != puuid {
			return
		}
		for index := range profile.Matches {
			if profile.Matches[index].ID == id {
				profile.Matches[index] = record
				profile.Captured = time.Now().UTC().Format(time.RFC3339Nano)
				domain.PrepareProfile(profile)
				return
			}
		}
	}
	update(r.state.History)
	update(r.state.Query)
	update(r.participantProfiles[puuid])
	if scout, ok := r.state.Scouts[puuid]; ok && scout.Profile != nil {
		update(scout.Profile)
		r.state.Scouts[puuid] = scout
	}
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
	return clone(record), nil
}
