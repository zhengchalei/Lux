package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// draft.go implements explicit personal-choice, replacement-bench, and skin commands for current
// champion selection. Each command carries the displayed session and champion,
// then reads the client's phase, identity, and session again before submission.
// A shared manual-action gate rejects overlapping clicks instead of retaining
// commands whose expected champion may change while waiting in a queue.
//
// Account cancellation bounds all authenticated work. Skin availability comes
// from the current carousel and selector rather than ownership guesses, allowing
// client-granted appearances while excluding disabled entries. Thumbnail paths
// are retained only for the last observed session and champion; browser input
// cannot supply an authenticated URL. Mutating requests are never retried because
// a lost reply may follow a successfully processed swap or appearance change.

// draftScope owns one bounded observation of the current authenticated selection.
type draftScope struct {
	access  history.Access     // Private loopback request credentials.
	ctx     context.Context    // Caller and account cancellation context.
	cancel  context.CancelFunc // Releases the bounded request lifetime.
	epoch   uint64             // Account generation captured at admission.
	session map[string]any     // Fresh client selection session.
	room    domain.Room        // Normalized current champion and pool.
}

// draftSessionKey identifies selection ownership without exposing account identifiers.
// Missing session and game identifiers return an empty key, disabling commands.
// The local slot also participates so session transitions cannot reuse another
// participant's displayed selection identity within the same client observation.
func draftSessionKey(session map[string]any) string {
	id := fmt.Sprint(session["id"])
	game := number(session["gameId"])
	if (id == "<nil>" || id == "" || id == "0") && game == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", id, game, number(session["localPlayerCellId"]))))
	return fmt.Sprintf("%x", sum[:12])
}

// DraftSnapshot copies only selection and public catalog data for the companion.
// Historical matches and player analysis never cross this boundary. The returned
// value owns its slices independently from ongoing runtime state publication.
func (r *Runtime) DraftSnapshot() domain.DraftPanelSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clone(domain.DraftPanelSnapshot{Revision: r.state.Revision, Connected: r.state.Connected, Phase: r.state.Phase, QueueID: r.draftQueue, Room: r.state.Room, Database: r.state.Database})
}

// DraftActivity reports a random-mode matching cycle and phases requiring dismissal.
// An empty key means automatic opening is unavailable. The opaque key stays
// stable through matchmaking, readiness, and selection, without exposing account identifiers.
func (r *Runtime) DraftActivity() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	playing := r.state.Phase == "GameStart" || r.state.Phase == "InProgress" || r.state.Phase == "Reconnect" || r.state.Phase == "WaitingForStats"
	selecting := r.state.Phase == "ChampSelect" && r.state.Room != nil && r.state.Room.Mode != domain.Ranked
	matching := (r.state.Phase == "Matchmaking" || r.state.Phase == "ReadyCheck") && domain.ModeFromQueue(r.draftQueue, false) != domain.Ranked
	active := r.state.Connected && (selecting || matching)
	if !active {
		return "", playing
	}
	return fmt.Sprintf("%d:%d", r.epoch, r.draftCycle), playing
}

// updateDraftCycleLocked associates automatic companion opening with verified matchmaking transitions.
// The caller holds mu and invokes this before replacing the prior phase and
// room. Matchmaking, readiness, and their first selection share an opening cycle;
// queue changes, another selection session, and renewed matchmaking create another.
// This lets a user dismiss one cycle without disabling future matches, while
// geometry changes and ordinary snapshot refreshes cannot reopen a dismissed panel.
func (r *Runtime) updateDraftCycleLocked(phase string, queue int, room *domain.Room) {
	if room != nil {
		queue = room.QueueID
	}
	// Missing queue reads suspend automatic visibility without inventing another cycle; recovery within the same matching stage must preserve a user's dismissal.
	newCycle := (queue != 0 && r.draftQueue != 0 && queue != r.draftQueue) || (phase == "Matchmaking" && r.state.Phase != "Matchmaking")
	if phase == "ChampSelect" && room != nil {
		newCycle = newCycle || (r.state.Phase != "Matchmaking" && r.state.Phase != "ReadyCheck" && (r.state.Room == nil || r.state.Room.SessionKey != room.SessionKey))
	}
	if newCycle {
		r.draftCycle++
	}
	r.draftQueue = queue
}

// openDraft captures current account ownership and reads a fresh selection scope.
// The caller must invoke cancel after successful admission. Caller cancellation
// and account replacement both stop requests, and the displayed session and
// champion must still match before the returned scope can authorize a command.
// Champion zero is admitted only for an active personal first-choice action.
// Runtime credentials are copied under the publication lock, then network work
// proceeds without holding that lock. A cancelled account context wins even if
// the renderer remains open and continues waiting for its original request.
func (r *Runtime) openDraft(caller context.Context, key string, champion int) (*draftScope, error) {
	r.mu.Lock()
	if r.closed || r.access == nil || r.session == nil || r.state.Account == nil || key == "" || champion < 0 {
		r.mu.Unlock()
		return nil, errors.New("等待客户端英雄选择")
	}
	access, accountCtx, epoch := *r.access, r.session, r.epoch
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(caller, 8*time.Second)
	// Account replacement cancels even a renderer request whose caller remains alive, preventing old credentials from continuing selection work.
	stop := context.AfterFunc(accountCtx, cancel)
	scope := &draftScope{access: access, ctx: ctx, epoch: epoch, cancel: func() { stop(); cancel() }}
	if err := scope.read(key, champion); err != nil {
		scope.cancel()
		return nil, err
	}
	return scope, nil
}

// read verifies identity, phase, and the displayed current champion before use.
// Missing or changed observations reject the operation. Reads may be repeated
// before submission, but this method itself performs no game-changing request.
// The account identifier comes from the authenticated summoner endpoint rather
// than the room's public slots. Session identity and current champion are checked
// together, since either can change while the other remains numerically equal.
func (s *draftScope) read(key string, champion int) error {
	person, err := resolve(s.ctx, s.access, "", "")
	if err != nil {
		return err
	}
	if domain.Identity(person["puuid"]) != s.access.Account {
		return errors.New("账号已变化，请等待同步")
	}
	var phase string
	if err = get(s.ctx, s.access, "/lol-gameflow/v1/gameflow-phase", &phase); err != nil {
		return err
	}
	if phase != "ChampSelect" {
		return errors.New("当前已不在英雄选择阶段")
	}
	if err = get(s.ctx, s.access, "/lol-champ-select/v1/session", &s.session); err != nil {
		return err
	}
	queue := number(s.session["queueId"])
	var picks, disabled, subset []int
	known := false
	if personalChoiceStage(s.session) {
		subset = readPersonalChoices(s.ctx, s.access, s.session)
		var picksOK, disabledOK bool
		picks, picksOK = readIDs(s.ctx, s.access, "/lol-champ-select/v1/pickable-champion-ids")
		disabled, disabledOK = readIDs(s.ctx, s.access, "/lol-champ-select/v1/disabled-champion-ids")
		known = picksOK && disabledOK
		// Personal availability is fetched separately from the session. Recheck its
		// ownership after those reads so a replacement room cannot supply an action.
		if err = get(s.ctx, s.access, "/lol-champ-select/v1/session", &s.session); err != nil {
			return err
		}
	}
	s.room = selectionRoom(s.session, queue, picks, disabled, known, subset)
	if s.room.SessionKey != key || s.room.Current != champion || truth(s.session["isSpectating"]) || (champion == 0 && !s.room.CanChoose) {
		return errors.New("英雄或房间已变化，请重新选择")
	}
	return nil
}

// current validates runtime ownership immediately before a manual write is sent.
func (r *Runtime) current(s *draftScope) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && s.ctx.Err() == nil && r.epoch == s.epoch && r.access != nil && r.access.Account == s.access.Account
}

// SelectDraftChampion selects one personal card or exchanges a verified bench champion.
// The expected current champion prevents rapid stale clicks from exchanging the
// wrong assignment. Overlapping manual writes fail immediately; a transport error
// refreshes observations without retrying a potentially completed swap.
// Availability belongs to the fresh client observations and cannot be borrowed
// from public strength recommendations. An unassigned personal choice completes
// the local active pick action; an assigned player's choice uses bench exchange.
// Shared bench heroes cannot authorize a write during the personal choice stage.
func (r *Runtime) SelectDraftChampion(ctx context.Context, key string, current, target int) error {
	if !r.draftMu.TryLock() {
		return errors.New("正在切换，请稍候")
	}
	defer r.draftMu.Unlock()
	s, err := r.openDraft(ctx, key, current)
	if err != nil {
		return err
	}
	defer s.cancel()
	personal := s.room.CanChoose && contains(s.room.Choices, target)
	if (!personal && !s.room.CanSwap) || target == current || !contains(s.room.Available, target) {
		return errors.New("该英雄当前不可选择，请等待客户端同步")
	}
	if !r.current(s) {
		return errors.New("客户端已重新连接，请重试")
	}
	if personal && current == 0 {
		action := personalPickAction(s.session)
		if action == nil {
			return errors.New("当前预选阶段已结束")
		}
		err = request(s.ctx, s.access, http.MethodPatch, fmt.Sprintf("/lol-champ-select/v1/session/actions/%d", number(action["id"])), map[string]any{"championId": target, "completed": true, "type": "pick"}, nil)
	} else {
		err = request(s.ctx, s.access, http.MethodPost, fmt.Sprintf("/lol-champ-select/v1/session/bench/swap/%d", target), nil, nil)
	}
	// Refresh after every submission, including ambiguous transport failures, so subsequent clicks use observed server state instead of optimistic assignments.
	r.wakeSync()
	return err
}

// readSkins intersects the client carousel with its current selection permission.
// Unlocked entries include temporary client grants; disabled entries remain
// unavailable. A successful pickable list further restricts entries, while an
// unavailable optional endpoint leaves the explicit carousel availability intact.
// Every skin remains associated with the selected champion, including child
// appearances exposed by the carousel. A separate path index contains only
// admitted local image paths and is never accepted directly from browser input.
func (s *draftScope) readSkins() (domain.DraftSkins, map[int]string, error) {
	result := domain.DraftSkins{SessionKey: s.room.SessionKey, ChampionID: s.room.Current, Skins: []domain.DraftSkin{}}
	if s.room.Current <= 0 {
		return result, nil, errors.New("请先选择英雄")
	}
	var selector map[string]any
	if err := get(s.ctx, s.access, "/lol-champ-select/v1/skin-selector-info", &selector); err != nil {
		return result, nil, err
	}
	if number(selector["selectedChampionId"]) != s.room.Current {
		return result, nil, errors.New("皮肤列表正在切换")
	}
	result.Selected = number(selector["selectedSkinId"])
	result.Enabled = truth(selector["showSkinSelector"]) && !truth(selector["skinSelectionDisabled"]) && truth(s.session["allowSkinSelection"])
	var rows []any
	if err := get(s.ctx, s.access, "/lol-champ-select/v1/skin-carousel-skins", &rows); err != nil {
		return result, nil, err
	}
	ids, known := readIDs(s.ctx, s.access, "/lol-champ-select/v1/pickable-skin-ids")
	paths := map[int]string{}
	seen := map[int]bool{}
	for _, raw := range rows {
		row := object(raw)
		entries := append([]any{row}, array(row["childSkins"])...)
		for _, value := range entries {
			entry := object(value)
			id := number(entry["id"])
			// The live client omits original appearances from pickable-skin-ids even when the carousel explicitly grants them; preserve that verified base-skin exception.
			if id <= 0 || seen[id] || number(entry["championId"]) != s.room.Current || !truth(entry["unlocked"]) || truth(entry["disabled"]) || (known && !truth(entry["isBase"]) && !contains(ids, id)) {
				continue
			}
			seen[id] = true
			result.Skins = append(result.Skins, domain.DraftSkin{ID: id, Name: text(entry["name"]), Base: truth(entry["isBase"])})
			path := text(entry["tilePath"])
			if safeSkinPath(path) {
				paths[id] = path
			}
		}
	}
	return result, paths, nil
}

// GetDraftSkins reads usable appearances for the displayed actual current champion.
// Session changes reject stale requests. The small path index replaces its
// predecessor only while the captured account generation still owns publication.
// The result retains the session and champion expected by the calling picker,
// allowing the renderer to reject replies for a superseded assignment. Replacing
// the index bounds its lifetime to the current appearance list rather than
// accumulating asset paths across accounts or successive selection rooms.
func (r *Runtime) GetDraftSkins(ctx context.Context, key string, champion int) (domain.DraftSkins, error) {
	s, err := r.openDraft(ctx, key, champion)
	if err != nil {
		return domain.DraftSkins{}, err
	}
	defer s.cancel()
	result, paths, err := s.readSkins()
	if err != nil {
		return result, err
	}
	r.mu.Lock()
	if r.epoch == s.epoch && s.ctx.Err() == nil {
		r.draftAssetKey = fmt.Sprintf("%s:%d", key, champion)
		r.draftAssets = paths
	}
	r.mu.Unlock()
	return result, nil
}

// SetDraftSkin applies one freshly verified usable appearance to the current hero.
// Skin and hero writes share admission, so a skin click cannot race a manual
// swap. The session is re-read after availability retrieval before the single
// PATCH request, rejecting a hero changed by the client during that read.
// Availability and phase can progress independently, so reading a usable skin
// alone cannot authorize the final write. Runtime generation and cancellation are
// checked immediately before transport begins; a failed PATCH is never repeated.
func (r *Runtime) SetDraftSkin(ctx context.Context, key string, champion, skin int) error {
	if !r.draftMu.TryLock() {
		return errors.New("正在切换，请稍候")
	}
	defer r.draftMu.Unlock()
	s, err := r.openDraft(ctx, key, champion)
	if err != nil {
		return err
	}
	defer s.cancel()
	list, _, err := s.readSkins()
	if err != nil {
		return err
	}
	allowed := false
	for _, entry := range list.Skins {
		allowed = allowed || entry.ID == skin
	}
	if !list.Enabled || !allowed {
		return errors.New("该皮肤当前不可使用")
	}
	if err = s.read(key, champion); err != nil {
		return err
	}
	if !r.current(s) {
		return errors.New("客户端已重新连接，请重试")
	}
	err = request(s.ctx, s.access, http.MethodPatch, "/lol-champ-select/v1/session/my-selection", map[string]int{"selectedSkinId": skin}, nil)
	r.wakeSync()
	return err
}

// safeSkinPath confines thumbnail reads to image files in local game assets.
func safeSkinPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(lower, "/lol-game-data/assets/") && !strings.ContainsAny(path, "?#\\") && !strings.Contains(path, "..") && (strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpeg"))
}

// GetSkinPreview returns one bounded local thumbnail without exposing client credentials.
// Only paths registered by GetDraftSkins for the same session and champion are
// admitted. Missing images fail once in the renderer, which retains its portrait
// fallback instead of repeatedly fetching full-size artwork or arbitrary URLs.
// The current account session cancels authenticated image work after reconnects,
// and redirects retain the transport's loopback boundary. Only PNG and JPEG
// bytes pass content inspection before their encoded representation crosses IPC.
func (r *Runtime) GetSkinPreview(ctx context.Context, key string, champion, skin int) (string, error) {
	r.mu.Lock()
	if r.access == nil || r.session == nil || r.state.Room == nil || r.state.Room.SessionKey != key || r.state.Room.Current != champion || r.draftAssetKey != fmt.Sprintf("%s:%d", key, champion) {
		r.mu.Unlock()
		return "", errors.New("皮肤预览已过期")
	}
	path := r.draftAssets[skin]
	access, session := *r.access, r.session
	r.mu.Unlock()
	if !safeSkinPath(path) {
		return "", errors.New("暂无皮肤预览")
	}
	bounded, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d%s", access.Port, path), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth("riot", access.Token)
	res, err := localTransport.Do(req)
	if err != nil {
		return "", errors.New("皮肤预览读取失败")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("暂无皮肤预览")
	}
	// One megabyte bounds retained preview bytes; tile artwork above this size is rejected rather than copied through IPC.
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return "", errors.New("皮肤预览过大")
	}
	mime := http.DetectContentType(data)
	if mime != "image/jpeg" && mime != "image/png" {
		return "", errors.New("皮肤预览格式不可用")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
