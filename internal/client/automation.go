package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// automation.go plans and executes explicit local draft rules independently from history.
// Saved champion preferences never arm the worker. Each candidate belongs to
// the local player's unfinished action, a verified legal pool, one session, and
// one rules/account generation. The pending candidate must remain unchanged for
// its configured delay before a fresh session can authorize submission.
//
// Attempts are recorded before sending because a timeout can hide a successful
// client write. Ambiguous writes therefore remain suppressed until rules or the
// session change. The worker remembers its own successful preselection so manual
// protection permits completing that hover without overwriting another choice.
// Stopping changes authorization immediately; a previously submitted HTTP request
// may finish but cannot authorize another action after the stop.

// plannedAction identifies one complete candidate write for delay and duplicate tracking.
type plannedAction struct {
	ID       int    // Current session action identifier.
	Champion int    // Verified legal configured champion.
	Complete bool   // Completes an active local action.
	Kind     string // Protocol pick or ban action.
}

// execution retains delay and attempt ownership within one observed selection session.
type execution struct {
	session     string                 // Current selection association key.
	revision    uint64                 // Rules/account generation being evaluated.
	pending     plannedAction          // Latest stable candidate awaiting delay.
	pendingAt   time.Time              // First observation of pending candidate.
	attempted   map[plannedAction]bool // Submitted candidates, including ambiguous results.
	ownID       int                    // Last successful automatic pick action.
	ownChampion int                    // Last successful automatic pick champion.
}

// decide chooses a legal action from the current local player's unfinished draft.
// Nil pools mean unreadable metadata; observed empty pools remain empty. Friendly
// intentions protect bans, while completed choices remove already used candidates.
// Random modes, manual changes, and another player's turn cannot produce writes.
func decide(settings domain.AutomationSettings, session map[string]any, picks, bans, disabled []int, ownID, ownChampion int) (plannedAction, string) {
	if domain.ModeFromQueue(number(session["queueId"]), truth(session["benchEnabled"])) != domain.Ranked {
		return plannedAction{}, "随机选人模式不执行自动选禁"
	}
	if _, exists := session["localPlayerCellId"]; !exists || number(session["localPlayerCellId"]) < 0 {
		return plannedAction{}, "等待本人的选人位置"
	}
	local := number(session["localPlayerCellId"])
	actions := actionRows(session)
	var chosen map[string]any
	for _, action := range actions {
		kind := text(action["type"])
		completed, known := action["completed"].(bool)
		_, actorKnown := action["actorCellId"]
		if actorKnown && number(action["actorCellId"]) == local && known && !completed && (kind == "pick" || kind == "ban") && truth(action["isInProgress"]) {
			chosen = action
			break
		}
	}
	if chosen == nil && settings.Preselect {
		for _, action := range actions {
			completed, known := action["completed"].(bool)
			_, actorKnown := action["actorCellId"]
			if actorKnown && number(action["actorCellId"]) == local && known && !completed && text(action["type"]) == "pick" {
				chosen = action
				break
			}
		}
	}
	if chosen == nil {
		return plannedAction{}, "等待本人的选禁回合"
	}
	kind, active := text(chosen["type"]), truth(chosen["isInProgress"])
	if kind == "ban" && (!settings.AutoBan || !active) {
		return plannedAction{}, "自动禁用未开启"
	}
	if kind == "pick" && ((!settings.AutoPick || !active) && !settings.Preselect) {
		return plannedAction{}, "自动选用未开启"
	}
	if _, exists := chosen["id"]; !exists {
		return plannedAction{}, "选禁动作尚不可用"
	}
	id, manual := number(chosen["id"]), number(chosen["championId"])
	positionMatches := settings.Position == ""
	for _, raw := range array(session["myTeam"]) {
		member := object(raw)
		if number(member["cellId"]) == local {
			positionMatches = positionMatches || text(member["assignedPosition"]) == settings.Position
			if manual == 0 {
				manual = number(member["championPickIntent"])
			}
		}
	}
	if !positionMatches {
		return plannedAction{}, "当前分路与规则不符"
	}
	if kind == "pick" && settings.PreserveManual && manual > 0 && (ownID != id || ownChampion != manual) {
		return plannedAction{}, "已保留手动预选，请手动锁定或关闭保护"
	}
	if disabled == nil {
		return plannedAction{}, "等待客户端禁用英雄列表"
	}
	priorities, pool := settings.Picks, picks
	if kind == "ban" {
		priorities, pool = settings.Bans, bans
	}
	if pool == nil {
		return plannedAction{}, "等待客户端可选／可禁列表"
	}
	for _, candidate := range priorities {
		if candidate <= 0 || !contains(pool, candidate) || contains(disabled, candidate) {
			continue
		}
		blocked := false
		for _, action := range actions {
			blocked = blocked || truth(action["completed"]) && number(action["championId"]) == candidate
		}
		if kind == "ban" && settings.ProtectAllies {
			blocked = blocked || contains(settings.Picks, candidate)
			for _, raw := range array(session["myTeam"]) {
				member := object(raw)
				blocked = blocked || number(member["championId"]) == candidate || number(member["championPickIntent"]) == candidate
			}
		}
		if blocked {
			continue
		}
		complete := active && (kind == "ban" || settings.AutoPick && settings.LockPick)
		if !complete && number(chosen["championId"]) == candidate {
			return plannedAction{}, "已预选，等待本人回合或手动锁定"
		}
		return plannedAction{ID: id, Champion: candidate, Complete: complete, Kind: kind}, ""
	}
	return plannedAction{}, "优先列表暂无可用英雄，等待手动操作"
}

// ready applies a bounded stable delay and rejects already submitted candidates.
func (state *execution) ready(plan plannedAction, delay int, now time.Time) bool {
	if state.attempted[plan] {
		return false
	}
	if state.pendingAt.IsZero() || state.pending != plan {
		state.pending, state.pendingAt = plan, now
	}
	return now.Sub(state.pendingAt) >= time.Duration(min(max(delay, 0), 10))*time.Second
}

// SaveAutomation installs explicit rules and runtime permission independently from persistence.
// Enabling requires at least one usable action and configured candidates. A save
// failure leaves the visible runtime state applied and returns a settings error.
// Replacing rules cancels any admitted selection transport before its new generation.
func (r *Runtime) SaveAutomation(settings domain.AutomationSettings, enabled bool) error {
	r.preferencesMu.Lock()
	defer r.preferencesMu.Unlock()
	settings = clone(settings)
	settings.DelaySeconds = min(max(settings.DelaySeconds, 0), 10)
	settings.Position = strings.ToLower(strings.TrimSpace(settings.Position))
	if enabled && (!(settings.AutoPick || settings.AutoBan || settings.Preselect) || (settings.AutoPick || settings.Preselect) && len(settings.Picks) == 0 || settings.AutoBan && len(settings.Bans) == 0) {
		return errors.New("请先配置需要执行的功能和英雄优先列表")
	}
	r.changed(func() {
		if r.writeCancel != nil {
			r.writeCancel()
			r.writeCancel = nil
		}
		r.automationEpoch++
		r.state.Automation, r.state.AutomationEnabled = settings, enabled
		if enabled {
			r.state.AutomationStatus = "自动选禁已开启 · 等待选人"
		} else {
			r.state.AutomationStatus = "自动选禁已关闭"
		}
	})
	return savePreference("automation.json", settings)
}

// StopAutomation revokes later submissions immediately without waiting for network workers.
// It also cancels admitted transport, while requests already processed by the
// client cannot be undone by application cancellation.
func (r *Runtime) StopAutomation() {
	r.changed(func() {
		if r.writeCancel != nil {
			r.writeCancel()
			r.writeCancel = nil
		}
		r.automationEpoch++
		r.state.AutomationEnabled = false
		r.state.AutomationStatus = "自动选禁已关闭"
	})
}

// automationLoop polls explicit rules independently from public data and profile pipelines.
func (r *Runtime) automationLoop() {
	state := execution{}
	for r.root.Err() == nil {
		r.mu.Lock()
		enabled := r.state.AutomationEnabled && r.access != nil && r.state.Account != nil
		var access history.Access
		var ctx context.Context
		if enabled {
			access, ctx = *r.access, r.session
		}
		settings, revision := clone(r.state.Automation), r.automationEpoch
		r.mu.Unlock()
		if !enabled {
			state = execution{}
		} else {
			if state.revision != revision {
				state = execution{revision: revision, attempted: map[plannedAction]bool{}}
			}
			message := r.automationTick(ctx, access, settings, revision, &state)
			r.mu.Lock()
			changed := r.automationEpoch == revision && r.state.AutomationEnabled && r.state.AutomationStatus != message
			r.mu.Unlock()
			if changed {
				r.changed(func() {
					if r.automationEpoch == revision && r.state.AutomationEnabled {
						r.state.AutomationStatus = message
					}
				})
			}
		}
		if !wait(r.root, 800*time.Millisecond) {
			return
		}
	}
}

// automationSession fills omitted queue metadata before any draft planner can run.
func automationSession(ctx context.Context, access history.Access) (map[string]any, error) {
	var session map[string]any
	if err := get(ctx, access, "/lol-champ-select/v1/session", &session); err != nil {
		return nil, err
	}
	if number(session["queueId"]) == 0 {
		var flow map[string]any
		if err := get(ctx, access, "/lol-gameflow/v1/session", &flow); err != nil {
			return nil, err
		}
		session["queueId"] = object(object(flow["gameData"])["queue"])["id"]
	}
	if number(session["queueId"]) == 0 {
		return nil, errors.New("等待真实队列信息")
	}
	return session, nil
}

// automationTick revalidates candidate, session, phase, and authorization immediately before submission.
// It records an attempt before network I/O, suppressing ambiguous retries. All
// history queries use separate goroutines and cannot postpone this worker's poll.
func (r *Runtime) automationTick(ctx context.Context, access history.Access, settings domain.AutomationSettings, revision uint64, state *execution) string {
	var phase string
	if get(ctx, access, "/lol-gameflow/v1/gameflow-phase", &phase) != nil {
		return "客户端暂不可读，等待恢复"
	}
	if phase != "ChampSelect" {
		*state = execution{revision: revision, attempted: map[plannedAction]bool{}}
		return "自动选禁已开启 · 等待选人"
	}
	session, err := automationSession(ctx, access)
	if err != nil {
		return "等待选人会话和真实队列"
	}
	key := fmt.Sprintf("%v:%v:%v", session["id"], session["gameId"], session["localPlayerCellId"])
	if state.session != key {
		*state = execution{session: key, revision: revision, attempted: map[plannedAction]bool{}}
	}
	picks, _ := readIDs(ctx, access, "/lol-champ-select/v1/pickable-champion-ids")
	var bans []int
	if settings.AutoBan {
		bans, _ = readIDs(ctx, access, "/lol-champ-select/v1/bannable-champion-ids")
	}
	disabled, _ := readIDs(ctx, access, "/lol-champ-select/v1/disabled-champion-ids")
	plan, reason := decide(settings, session, picks, bans, disabled, state.ownID, state.ownChampion)
	if reason != "" {
		state.pendingAt = time.Time{}
		return reason
	}
	if !state.ready(plan, settings.DelaySeconds, time.Now()) {
		return "等待规则延时／客户端回执"
	}
	fresh, err := automationSession(ctx, access)
	if err != nil {
		return "选禁状态正在变化"
	}
	// Availability can change during the delay; obtain new legal pools for the second planner rather than reusing the earlier observation at submission.
	picks, _ = readIDs(ctx, access, "/lol-champ-select/v1/pickable-champion-ids")
	if settings.AutoBan {
		bans, _ = readIDs(ctx, access, "/lol-champ-select/v1/bannable-champion-ids")
	}
	disabled, _ = readIDs(ctx, access, "/lol-champ-select/v1/disabled-champion-ids")
	freshPlan, freshReason := decide(settings, fresh, picks, bans, disabled, state.ownID, state.ownChampion)
	if freshReason != "" || freshPlan != plan || fmt.Sprintf("%v:%v:%v", fresh["id"], fresh["gameId"], fresh["localPlayerCellId"]) != key {
		return "选禁状态已变化，等待重新判断"
	}
	if get(ctx, access, "/lol-gameflow/v1/gameflow-phase", &phase) != nil || phase != "ChampSelect" {
		return "选人阶段已变化"
	}
	r.mu.Lock()
	allowed := !r.closed && r.state.AutomationEnabled && r.automationEpoch == revision && r.access != nil && r.access.Port == access.Port && r.access.Token == access.Token && r.access.Account == access.Account && ctx.Err() == nil
	var submission context.Context
	var cancelSubmission context.CancelFunc
	if allowed {
		state.attempted[plan] = true
		submission, cancelSubmission = context.WithCancel(ctx)
		r.writeCancel = cancelSubmission
	}
	r.mu.Unlock()
	if !allowed {
		return "自动选禁已停止或账号已变化"
	}
	// Submission is authorized above and its attempt is retained even on timeout; replaying an uncertain PATCH could override a later manual choice.
	err = request(submission, access, http.MethodPatch, fmt.Sprintf("/lol-champ-select/v1/session/actions/%d", plan.ID), map[string]any{"championId": plan.Champion, "completed": plan.Complete, "type": plan.Kind}, nil)
	cancelSubmission()
	r.mu.Lock()
	r.writeCancel = nil
	r.mu.Unlock()
	if err != nil {
		return "请求结果未确认，已停止重复提交；可手动操作或重新应用规则"
	}
	if plan.Kind == "pick" {
		state.ownID, state.ownChampion = plan.ID, plan.Champion
	}
	if plan.Kind == "ban" {
		return fmt.Sprintf("已提交禁用 · 英雄 %d", plan.Champion)
	}
	if plan.Complete {
		return fmt.Sprintf("已提交锁定 · 英雄 %d", plan.Champion)
	}
	return fmt.Sprintf("已提交预选 · 英雄 %d", plan.Champion)
}
