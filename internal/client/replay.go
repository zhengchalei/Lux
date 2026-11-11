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

// replay.go registers observed matches with the client's replay service and
// submits explicit download or playback requests. The server owns files, version
// compatibility, download progress, and launching the replay viewer. Lux passes
// recorded match metadata rather than file paths or command lines from Vue.
// Only a game present in the currently visible player's loaded history qualifies.
//
// Each operation captures the account generation and shares its cancellation
// lifetime. A separate gate serializes registration and commands without blocking
// client discovery or history loading. Commands read fresh metadata and playback
// configuration before writing; failures never repeat a download or viewer launch.
// Status checks return a compact observation directly and do not republish full
// player histories. Closing or changing a match stops its renderer polling.

// replayScope carries account ownership for one bounded replay service operation.
type replayScope struct {
	access history.Access     // Private authenticated loopback credentials.
	ctx    context.Context    // Caller and account cancellation lifetime.
	cancel context.CancelFunc // Releases operation cancellation resources.
	epoch  uint64             // Captured runtime account generation.
	puuid  string             // Visible player owning the match.
	match  domain.Match       // Recorded metadata copied under runtime lock.
}

// replayMatchLocked returns a match belonging to the currently loaded visible player.
// The caller holds Runtime.mu. No arbitrary numeric game identifier or hidden
// roster identity becomes replay authorization through this lookup.
func (r *Runtime) replayMatchLocked(puuid string, id uint64) (domain.Match, bool) {
	if puuid == "" || id == 0 {
		return domain.Match{}, false
	}
	profiles := []*domain.Profile{r.state.Query, r.state.History, r.participantProfiles[puuid]}
	if scout, ok := r.state.Scouts[puuid]; ok {
		profiles = append(profiles, scout.Profile)
	}
	for _, profile := range profiles {
		if profile == nil || profile.PUUID != puuid {
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

// openReplay captures a visible match and binds transport to the current account.
// Callers release the returned scope after use. The actual client summoner must
// still match the captured account before any replay registration is attempted.
// Match metadata and credentials are captured together under the state lock,
// then network reads proceed without retaining that lock. Account cancellation
// interrupts pending transport, while later generation checks reject replies
// that complete during reconnection or replacement of the queried profile.
func (r *Runtime) openReplay(caller context.Context, puuid string, id uint64) (*replayScope, error) {
	return r.openMatchScope(caller, puuid, id, 10*time.Second)
}

// openMatchScope binds a loaded visible match to current account ownership.
// The caller supplies a bounded operation duration and releases the returned scope.
// Both replay commands and timeline reviews verify the actual client identity
// before authenticated reads; account replacement cancels their shared session.
func (r *Runtime) openMatchScope(caller context.Context, puuid string, id uint64, timeout time.Duration) (*replayScope, error) {
	r.mu.Lock()
	match, visible := r.replayMatchLocked(puuid, id)
	if r.closed || r.access == nil || r.session == nil || !r.state.Connected || !visible {
		r.mu.Unlock()
		return nil, errors.New("请连接客户端并重新查询该对局")
	}
	access, epoch, accountCtx := *r.access, r.epoch, r.session
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(caller, timeout)
	// Reconnection cancels outstanding replay transport even when the renderer's
	// request remains alive, so old credentials cannot launch work for another account.
	stop := context.AfterFunc(accountCtx, cancel)
	s := &replayScope{access: access, epoch: epoch, ctx: ctx, cancel: func() { stop(); cancel() }, puuid: puuid, match: match}
	person, err := resolve(ctx, access, "", "")
	if err != nil || domain.Identity(person["puuid"]) != access.Account {
		s.cancel()
		return nil, errors.New("客户端账号已变化，请等待重连")
	}
	return s, nil
}

// replayCurrent verifies account and visible-match ownership before publication or writes.
func (r *Runtime) replayCurrent(s *replayScope) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, visible := r.replayMatchLocked(s.puuid, s.match.ID)
	return visible && !r.closed && r.state.Connected && s.ctx.Err() == nil && r.epoch == s.epoch && r.access != nil && r.access.Account == s.access.Account
}

// replayObservation maps server metadata to actionable controls and concise explanations.
// Only downloading values within the percentage range become progress. Unknown
// states stay unavailable; configuration can revoke actions without pretending
// that a downloaded file has disappeared or changed compatibility.
// In particular, a ready replay retains its watch label while a game or another
// replay is running, but its disabled action explains that activity. Download
// permissions remain independent, allowing the client to manage files without
// treating download completion as permission to launch a viewer automatically.
func replayObservation(id uint64, metadata, config map[string]any, phase string) domain.Replay {
	state := text(metadata["state"])
	result := domain.Replay{GameID: id, State: state, Label: "回放暂不可用", Reason: "客户端尚未提供可用回放状态。"}
	switch state {
	case "checking", "found":
		result.Label, result.Reason = "检查回放中", "正在由客户端检查回放文件及版本。"
	case "download", "retryDownload":
		result.Label, result.Reason, result.CanDownload = "下载回放", "下载到英雄联盟客户端设置的回放目录；完成后可点击观看。", true
	case "downloading":
		result.Label, result.Reason = "正在下载", "客户端正在下载回放；完成后会显示观看入口。"
		if raw, ok := metadata["downloadProgress"]; ok && number(raw) >= 0 && number(raw) <= 100 {
			progress := number(raw)
			result.Progress = &progress
		}
	case "watch":
		result.Label, result.Reason, result.CanWatch = "观看回放", "通过英雄联盟客户端打开本局回放。", true
	case "incompatible":
		result.Label, result.Reason = "版本不兼容", "本局回放与当前游戏版本不兼容，客户端无法播放。"
	case "missingOrExpired", "lost":
		result.Label, result.Reason = "回放已失效", "客户端未找到本局回放，或回放已过期。"
	case "unsupported":
		result.Label, result.Reason = "不支持回放", "客户端不支持此模式或此对局的回放。"
	case "maxLimitReached":
		result.Label, result.Reason = "下载队列已满", "请等待客户端现有回放下载完成后重试。"
	case "error":
		result.Label, result.Reason = "回放读取失败", "客户端返回回放错误，可点击刷新重新检查。"
	}
	if !truth(config["isReplaysEnabled"]) || !truth(config["isReplaysForMatchHistoryEnabled"]) || !truth(config["isLoggedIn"]) || truth(config["isPatching"]) {
		result.CanWatch, result.CanDownload = false, false
		result.Reason = "客户端未启用战绩回放、未登录或正在更新，请稍后刷新。"
	} else if truth(config["isPlayingGame"]) || truth(config["isPlayingReplay"]) || (phase != "None" && phase != "Lobby" && phase != "EndOfGame") {
		result.CanWatch = false
		if state == "watch" {
			result.Reason = "请结束当前游戏、回放或选人 / 匹配后再观看。"
		}
	}
	return result
}

// readReplay reads fresh configuration and registers only missing replay metadata.
// Registration uses recorded version, queue, type, and end time. A missing recorded
// version remains unavailable rather than labeling an older match with today's
// client version. Existing metadata can still expose a previously downloaded file.
// Configuration and phase are observed after metadata work so a game started
// during registration can revoke playback. A response for a different game or
// an account generation replaced during these reads never becomes an actionable
// control, even if an earlier response advertised a ready local replay file.
func (r *Runtime) readReplay(s *replayScope) (domain.Replay, error) {
	id := s.match.ID
	if s.match.PlatformID != "" && s.access.Platform != "" && !strings.EqualFold(s.match.PlatformID, s.access.Platform) {
		return domain.Replay{GameID: id, State: "unavailable", Label: "区服不匹配", Reason: "本局来自其他区服，请切换到对应客户端后重试。"}, nil
	}
	var config, metadata map[string]any
	path := fmt.Sprintf("/lol-replays/v1/metadata/%d", id)
	err := history.Get(s.ctx, s.access, path, &metadata)
	if err != nil {
		var status history.HTTPError
		if !errors.As(err, &status) || status.Status != http.StatusNotFound {
			return domain.Replay{}, err
		}
		if s.match.GameVersion == "" || s.match.GameType == "" || s.match.GameEnd <= 0 {
			return domain.Replay{GameID: id, State: "unavailable", Label: "回放资料不完整", Reason: "对局缺少原始版本或结束时间，请重新查询后重试。"}, nil
		}
		if err = get(s.ctx, s.access, "/lol-replays/v1/configuration", &config); err != nil {
			return domain.Replay{}, err
		}
		if !truth(config["isReplaysEnabled"]) || !truth(config["isLoggedIn"]) {
			return domain.Replay{GameID: id, State: "unavailable", Label: "回放暂不可用", Reason: "请登录客户端并确认回放功能已启用。"}, nil
		}
		if !r.replayCurrent(s) {
			return domain.Replay{}, errors.New("账号或查询对象已变化")
		}
		body := map[string]any{"gameVersion": s.match.GameVersion, "gameType": s.match.GameType, "queueId": s.match.QueueID, "gameEnd": s.match.GameEnd}
		// Only a confirmed missing metadata record triggers registration. Failed reads
		// never overwrite existing metadata or retry a potentially completed mutation.
		if err = request(s.ctx, s.access, http.MethodPost, fmt.Sprintf("/lol-replays/v2/metadata/%d/create", id), body, nil); err != nil {
			return domain.Replay{}, err
		}
		if err = get(s.ctx, s.access, path, &metadata); err != nil {
			return domain.Replay{}, err
		}
	}
	if uint64(number(metadata["gameId"])) != id {
		return domain.Replay{}, errors.New("客户端回放与当前对局不一致")
	}
	// Metadata registration can outlast a phase transition. Read configuration and
	// gameflow afterward so a newly started game revokes playback before submission.
	if err = get(s.ctx, s.access, "/lol-replays/v1/configuration", &config); err != nil {
		return domain.Replay{}, err
	}
	var phase string
	if err = get(s.ctx, s.access, "/lol-gameflow/v1/gameflow-phase", &phase); err != nil {
		return domain.Replay{}, err
	}
	if !r.replayCurrent(s) {
		return domain.Replay{}, errors.New("账号或查询对象已变化")
	}
	return replayObservation(id, metadata, config, phase), nil
}

// GetReplay returns current download or playback availability for one visible match.
// Checks serialize with manual replay commands. The caller may poll while details
// remain visible; this operation never downloads files or launches the viewer.
func (r *Runtime) GetReplay(ctx context.Context, puuid string, id uint64) (domain.Replay, error) {
	if !r.replayMu.TryLock() {
		return domain.Replay{}, errors.New("正在处理回放，请稍候")
	}
	defer r.replayMu.Unlock()
	s, err := r.openReplay(ctx, puuid, id)
	if err != nil {
		return domain.Replay{}, err
	}
	defer s.cancel()
	return r.readReplay(s)
}

// ReplayAction submits one explicit download or watch command after fresh permission checks.
// The action must match current server metadata, and account replacement revokes
// admission. No failure retries a command whose response might have been lost.
// The shared replay gate rejects concurrent commands and status registration,
// while the runtime publication lock remains available to discovery and histories.
// Download and watch use distinct permissions from the same fresh observation;
// neither a displayed card nor a previous download authorizes a later launch.
func (r *Runtime) ReplayAction(ctx context.Context, puuid string, id uint64, action string) error {
	if action != "download" && action != "watch" {
		return errors.New("无效的回放操作")
	}
	if !r.replayMu.TryLock() {
		return errors.New("正在处理回放，请稍候")
	}
	defer r.replayMu.Unlock()
	s, err := r.openReplay(ctx, puuid, id)
	if err != nil {
		return err
	}
	defer s.cancel()
	observation, err := r.readReplay(s)
	if err != nil {
		return err
	}
	if (action == "download" && !observation.CanDownload) || (action == "watch" && !observation.CanWatch) {
		return errors.New(observation.Reason)
	}
	if !r.replayCurrent(s) {
		return errors.New("账号或查询对象已变化")
	}
	return request(s.ctx, s.access, http.MethodPost, fmt.Sprintf("/lol-replays/v1/rofls/%d/%s", id, action), map[string]string{"componentType": "replay-button_match-history"}, nil)
}
