package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"lux/internal/domain"
	"lux/internal/review"
)

// timeline.go connects visible recorded matches to sampled review and explicit AI calls.
// The shared match scope verifies account ownership before timeline reads. Parsed
// observations retain bounded in-memory ownership under account generation, game,
// and viewed participant; changing the query prevents an old response from being
// returned as the newly selected match's analysis.
//
// Timeline and model work use independent locks rather than the snapshot mutex.
// No review payload enters periodic state publication, and moving the mouse never
// starts model requests. One admitted model operation owns a cancel function that
// the renderer can revoke when its selected match or opponent changes. Account
// replacement also cancels that operation through the authenticated session context.

// timelineData reads or reuses immutable observations for one verified match scope.
// Eight retained game perspectives bound memory across navigation. Cache keys
// include the account generation so reconnected clients cannot reuse stale ownership.
// Network reads happen outside both cache and snapshot locks. Successful sources
// are immutable after insertion; the public method clones returned DTOs so external
// mutation cannot change future opponent comparisons. Concurrent cache misses may
// perform independent reads, while insertion keeps only one retained entry for
// each verified game perspective and evicts the oldest retained key at capacity.
func (r *Runtime) timelineData(s *replayScope) (domain.Timeline, error) {
	if s.match.PlatformID != "" && s.access.Platform != "" && !strings.EqualFold(s.match.PlatformID, s.access.Platform) {
		return domain.Timeline{}, errors.New("本局来自其他区服，请连接对应客户端后复盘")
	}
	key := fmt.Sprintf("%d:%d:%s", s.epoch, s.match.ID, s.puuid)
	r.timelineMu.Lock()
	cached, ok := r.timelineCache[key]
	r.timelineMu.Unlock()
	if ok {
		return cached, nil
	}
	game, err := r.repository.RawDetail(s.ctx, s.access, s.match.ID)
	if err != nil {
		return domain.Timeline{}, errors.New("客户端暂未提供本局完整阵容，请稍后重试")
	}
	raw, err := r.repository.RawTimeline(s.ctx, s.access, s.match.ID)
	if err != nil {
		return domain.Timeline{}, errors.New("客户端暂未提供本局时间线；这不影响普通战绩和回放")
	}
	timeline, err := review.Normalize(game, raw, s.puuid, s.match.ID, s.match.QueueID)
	if err != nil {
		return timeline, err
	}
	r.mu.Lock()
	catalog := r.state.Database
	r.mu.Unlock()
	for index := range timeline.Players {
		timeline.Players[index].ChampionName = catalog.Champion(timeline.Players[index].Champion).Name
	}
	if !r.replayCurrent(s) {
		return domain.Timeline{}, errors.New("账号或所选对局已变化")
	}
	r.timelineMu.Lock()
	if _, exists := r.timelineCache[key]; !exists {
		// Retaining eight complete perspectives avoids keeping every historical timeline in memory while still making opponent switches immediate.
		if len(r.timelineKeys) >= 8 {
			delete(r.timelineCache, r.timelineKeys[0])
			r.timelineKeys = r.timelineKeys[1:]
		}
		r.timelineKeys = append(r.timelineKeys, key)
		r.timelineCache[key] = timeline
	}
	r.timelineMu.Unlock()
	return timeline, nil
}

// GetTimeline returns observed phases for a loaded match and selected enemy slot.
// It lazily reads client frames and never invokes a model. A zero opponent uses
// a unique explicit role match, while ambiguous matchups await manual selection.
func (r *Runtime) GetTimeline(ctx context.Context, puuid string, id uint64, opponent int) (domain.TimelineReview, error) {
	// A twenty-second scope bounds read-only frame retrieval without coupling it to the longer explicit model request timeout.
	s, err := r.openMatchScope(ctx, puuid, id, 20*time.Second)
	if err != nil {
		return domain.TimelineReview{}, err
	}
	defer s.cancel()
	timeline, err := r.timelineData(s)
	if err != nil {
		return domain.TimelineReview{}, err
	}
	result, err := review.Compare(timeline, opponent)
	if err != nil {
		return result, err
	}
	if !r.replayCurrent(s) {
		return domain.TimelineReview{}, errors.New("账号或所选对局已变化")
	}
	return clone(result), nil
}

// GetAISettings reads only provider status and never returns a saved API key.
func (r *Runtime) GetAISettings() domain.AISettings { return r.ai.Settings() }

// SaveAISettings replaces user-entered provider preferences without contacting that provider.
func (r *Runtime) SaveAISettings(input domain.AISettingsInput) (domain.AISettings, error) {
	return r.ai.Save(input)
}

// CancelAIAnalysis revokes the current explicit analysis when browsing ownership changes.
// Only the matching renderer request identifier can cancel that operation; empty
// or stale identifiers leave a later analysis running instead of interrupting it.
func (r *Runtime) CancelAIAnalysis(requestID string) {
	r.mu.Lock()
	cancel := r.aiCancel
	if requestID == "" || requestID != r.aiCancelKey {
		cancel = nil
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// AnalyzeTimeline sends verified selected-player evidence after explicit user invocation.
// One admitted request owns cancellation until completion. Account, match visibility,
// and context are checked again after the provider responds so old coaching never
// becomes a successful response under a replaced query. Model traffic runs outside
// snapshot synchronization, keeping client updates and the renderer responsive.
// CancelAIAnalysis revokes this lifetime without modifying cached client observations
// or the locally persisted configuration used for a later independent request.
func (r *Runtime) AnalyzeTimeline(ctx context.Context, puuid string, id uint64, opponent int, requestID string) (domain.AIReview, error) {
	if requestID == "" || len(requestID) > 100 {
		return domain.AIReview{}, errors.New("无效的 AI 分析请求")
	}
	if !r.aiMu.TryLock() {
		return domain.AIReview{}, errors.New("已有 AI 分析正在进行，请稍候或先取消")
	}
	defer r.aiMu.Unlock()
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	// Register cancellation before account verification can block on client I/O; a late cancellation from a replaced panel only owns its exact request key.
	r.mu.Lock()
	r.aiCancel, r.aiCancelKey = cancel, requestID
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.aiCancel, r.aiCancelKey = nil, ""; r.mu.Unlock() }()
	s, err := r.openMatchScope(owned, puuid, id, 110*time.Second)
	if err != nil {
		return domain.AIReview{}, err
	}
	defer s.cancel()
	timeline, err := r.timelineData(s)
	if err != nil {
		return domain.AIReview{}, err
	}
	view, err := review.Compare(timeline, opponent)
	if err != nil {
		return domain.AIReview{}, err
	}
	result, err := r.ai.Analyze(s.ctx, view)
	if err != nil {
		return domain.AIReview{}, err
	}
	if !r.replayCurrent(s) {
		return domain.AIReview{}, errors.New("账号或所选对局已变化，已丢弃旧分析")
	}
	return result, nil
}
