package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"lux/internal/domain"
	"lux/internal/history"
)

// transport.go reads coherent local client observations and authenticated events.
// The self-signed certificate exception is confined to the explicit loopback
// client; redirects and proxy routing cannot move credentials to another host.
// Public summoner identities are validated before entering path segments, and
// hidden enemy intentions never become visible counterpick targets.
//
// Snapshot reads verify the current identity again after collecting the room.
// A mixed-account observation is rejected instead of publishing an old roster
// under a new account. Socket events carry only relevant endpoint names and
// wake a bounded HTTP refresh; incoming payloads do not enter browser state.

// localTransport holds one reusable loopback client independently from public HTTPS and SGP clients owned by the history repository.
var localTransport = &http.Client{
	Timeout:       4 * time.Second,
	Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil}, // Loopback LCU uses its own self-signed certificate.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// request executes one internal loopback endpoint and returns sanitized failures.
// Only callers implementing explicit draft actions may use PATCH. Retry policy
// belongs to read-only callers; this function never repeats a mutating request.
func request(ctx context.Context, access history.Access, method, path string, body, out any) error {
	if access.Port < 1 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("无效的本机客户端路径")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errors.New("请求参数无法编码")
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("https://127.0.0.1:%d%s", access.Port, path), reader)
	if err != nil {
		return errors.New("本机请求无法创建")
	}
	req.SetBasicAuth("riot", access.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := localTransport.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("客户端连接失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("客户端 HTTP %d", response.StatusCode)
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	// Limit each decoded response so corrupt local endpoints cannot retain unbounded payloads; ordinary match details remain well below this budget.
	decoder := json.NewDecoder(io.LimitReader(response.Body, 32<<20))
	decoder.UseNumber()
	if err = decoder.Decode(out); err != nil {
		return errors.New("客户端响应无法解析")
	}
	return nil
}

// get reads one JSON endpoint using the authenticated loopback transport only.
func get(ctx context.Context, access history.Access, path string, out any) error {
	return request(ctx, access, http.MethodGet, path, nil, out)
}

// object returns a decoded object map or nil for other JSON types.
// Callers use nil map reads to treat absent nested fields as unknown
// observations, rather than creating synthetic protocol objects.
func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

// array returns the source's decoded array, or nil for other JSON types.
// Iterating a nil result makes absent roster collections contribute no
// synthetic members to the published client state.
func array(value any) []any {
	result, _ := value.([]any)
	return result
}

// number decodes integral protocol fields from JSON numbers and typed test inputs.
// Missing values and unsupported types return zero, which callers must not treat
// as proof that required metadata was observed. Ownership-sensitive planners
// separately require the corresponding field to exist.
func number(value any) int {
	switch typed := value.(type) {
	case json.Number:
		result, _ := strconv.Atoi(string(typed))
		return result
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case uint64:
		return int(typed)
	default:
		return 0
	}
}

// text reads a source string without formatting absent fields as placeholders.
// Non-string values return an empty string, allowing identity and metadata
// validators to retain their unknown-state behavior instead of inventing labels.
func text(value any) string {
	result, _ := value.(string)
	return result
}

// truth reads an explicit protocol boolean without coercing missing values.
// Missing or differently typed fields return false. Writable-action planning
// separately checks that completion is an observed boolean before permitting
// the result to authorize a request.
func truth(value any) bool {
	result, _ := value.(bool)
	return result
}

// readIDs preserves endpoint failure separately from an observed empty legal pool.
// Failure returns nil and false; a successful empty JSON response becomes an
// allocated empty slice and true, allowing planners to distinguish both cases.
func readIDs(ctx context.Context, access history.Access, path string) ([]int, bool) {
	var ids []int
	if err := get(ctx, access, path, &ids); err != nil {
		return nil, false
	}
	if ids == nil {
		ids = []int{}
	}
	return ids, true
}

// contains reports whether a champion occurs in a verified protocol list.
func contains(ids []int, id int) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// actionRows flattens selection phases while retaining original action objects.
// Missing action arrays produce no entries. The returned maps are read-only
// observations shared with the current decoded session and are not persisted.
func actionRows(session map[string]any) []map[string]any {
	var result []map[string]any
	for _, phase := range array(session["actions"]) {
		for _, raw := range array(phase) {
			if row := object(raw); row != nil {
				result = append(result, row)
			}
		}
	}
	return result
}

// selectionRoom exposes friendly intentions and only publicly completed opposing picks.
// Ranked recommendations require both availability endpoints. ARAM recommendations
// use the local assignment, personal choices, and replacement bench independently
// from public strength. Personal cards stay visible even when the client has not
// confirmed their availability, while Available admits only verified personal
// choices during BAN_PICK. The shared bench becomes actionable after that stage,
// preventing its visible cards from authorizing a premature exchange.
func selectionRoom(session map[string]any, queue int, picks, disabled []int, poolsKnown bool, subset []int) domain.Room {
	mode := domain.ModeFromQueue(queue, truth(session["benchEnabled"]))
	room := domain.Room{Mode: mode, QueueID: queue, Available: []int{}, Banned: []int{}, Choices: []int{}, Bench: []int{}}
	room.SessionKey = draftSessionKey(session)
	room.CanSwap = mode != domain.Ranked && truth(session["benchEnabled"]) && !truth(session["isSpectating"])
	local := number(session["localPlayerCellId"])
	actions := actionRows(session)
	for _, action := range actions {
		if text(action["type"]) == "ban" && truth(action["completed"]) {
			room.Banned = append(room.Banned, number(action["championId"]))
		}
	}
	for _, side := range []struct {
		key  string
		ally bool
	}{{"myTeam", true}, {"theirTeam", false}} {
		for _, raw := range array(session[side.key]) {
			member := object(raw)
			cell := number(member["cellId"])
			locked := false
			for _, action := range actions {
				locked = locked || number(action["actorCellId"]) == cell && text(action["type"]) == "pick" && truth(action["completed"])
			}
			champion := number(member["championId"])
			if side.ally && champion == 0 {
				champion = number(member["championPickIntent"])
			}
			if !side.ally && !locked {
				champion = 0
			}
			entry := domain.Member{Cell: cell, Champion: champion, Position: text(member["assignedPosition"]), Locked: locked, Local: side.ally && cell == local}
			if side.ally {
				room.Allies = append(room.Allies, entry)
				if entry.Local {
					room.Current = champion
					room.SkinID = number(member["selectedSkinId"])
				}
			} else {
				room.Enemies = append(room.Enemies, entry)
			}
		}
	}
	if mode != domain.Ranked {
		for _, raw := range array(session["benchChampions"]) {
			id := number(object(raw)["championId"])
			if id > 0 && !contains(room.Bench, id) {
				room.Bench = append(room.Bench, id)
			}
		}
		if personalChoiceStage(session) {
			applyPersonalChoices(&room, session, subset, picks, disabled, poolsKnown)
			return room
		}
		picks = []int{room.Current}
		picks = append(picks, room.Bench...)
		poolsKnown = true
	}
	if poolsKnown {
		for _, id := range picks {
			occupied := false
			for _, member := range append(append([]domain.Member{}, room.Allies...), room.Enemies...) {
				occupied = occupied || member.Locked && member.Champion == id
			}
			if id > 0 && !contains(disabled, id) && !contains(room.Banned, id) && (mode != domain.Ranked || !occupied || truth(session["allowDuplicatePicks"])) && !contains(room.Available, id) {
				room.Available = append(room.Available, id)
			}
		}
	}
	return room
}

// draftBattle associates visible selection members with their sanitized hero slots.
// Hidden and anonymized identities remain empty. No obfuscated identifier field
// is interpreted as a queryable account, even if present in the response.
func draftBattle(session map[string]any, room domain.Room) domain.Battle {
	battle := domain.Battle{Key: fmt.Sprintf("draft:%v:%v", session["id"], session["gameId"]), Source: "实时选人 · 客户端公开阵容", Mode: room.Mode, QueueID: room.QueueID, Players: []domain.Combatant{}}
	for _, side := range []struct {
		key     string
		ally    bool
		members []domain.Member
	}{{"myTeam", true, room.Allies}, {"theirTeam", false, room.Enemies}} {
		for _, raw := range array(session[side.key]) {
			person := object(raw)
			visible := text(person["nameVisibilityType"]) != "HIDDEN" && !truth(person["isAnonymized"]) && !truth(person["isBot"])
			puuid := ""
			if visible {
				puuid = domain.Identity(person["puuid"])
			}
			entry := domain.Combatant{PUUID: puuid, Name: domain.DisplayName(person), Position: text(person["assignedPosition"]), Ally: side.ally, Hidden: puuid == ""}
			if !entry.Hidden {
				entry.Group = participantGroup(person)
				if entry.Group != "" {
					entry.GroupSource = "LCU"
				}
			}
			if entry.Hidden {
				entry.Name = "身份未公开"
			}
			for _, member := range side.members {
				if member.Cell == number(person["cellId"]) {
					entry.Champion, entry.Locked = member.Champion, member.Locked
				}
			}
			battle.Players = append(battle.Players, entry)
		}
	}
	return battle
}

// gameBattle derives friendly orientation only from an observed local player identity.
// Missing local orientation returns nil instead of assigning arbitrary teams.
// In-game champions come from public team data or published player selections.
func gameBattle(flow map[string]any, own string) *domain.Battle {
	data := object(flow["gameData"])
	ownTeam := ""
	for _, key := range []string{"teamOne", "teamTwo"} {
		for _, raw := range array(data[key]) {
			if text(object(raw)["puuid"]) == own {
				ownTeam = key
			}
		}
	}
	if ownTeam == "" {
		return nil
	}
	queue := number(object(data["queue"])["id"])
	battle := &domain.Battle{Key: fmt.Sprintf("game:%v", data["gameId"]), Source: "当前对局 · 客户端公开阵容", Mode: domain.ModeFromQueue(queue, false), QueueID: queue, Players: []domain.Combatant{}}
	for _, key := range []string{"teamOne", "teamTwo"} {
		for _, raw := range array(data[key]) {
			person := object(raw)
			puuid := domain.Identity(person["puuid"])
			if truth(person["isBot"]) {
				puuid = ""
			}
			champion := number(person["championId"])
			if champion == 0 {
				for _, selection := range array(data["playerChampionSelections"]) {
					if text(object(selection)["puuid"]) == puuid {
						champion = number(object(selection)["championId"])
					}
				}
			}
			entry := domain.Combatant{PUUID: puuid, Name: domain.DisplayName(person), Champion: champion, Position: text(person["selectedPosition"]), Ally: key == ownTeam, Hidden: puuid == "", Locked: true}
			if !entry.Hidden {
				entry.Group = participantGroup(person)
				if entry.Group != "" {
					entry.GroupSource = "LCU"
				}
			}
			battle.Players = append(battle.Players, entry)
		}
	}
	return battle
}

// resolve identifies the requested summoner through the client's public lookup APIs.
// Complete Riot ID aliases use JSON input; bare names require candidate selection.
// Empty names and invalid public identifiers return user-facing validation failures.
func resolve(ctx context.Context, access history.Access, query, puuid string) (map[string]any, error) {
	var result map[string]any
	if puuid != "" {
		if domain.Identity(puuid) == "" {
			return nil, errors.New("无效玩家标识")
		}
		err := get(ctx, access, "/lol-summoner/v2/summoners/puuid/"+puuid, &result)
		return result, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		err := get(ctx, access, "/lol-summoner/v1/current-summoner", &result)
		return result, err
	}
	if len([]rune(query)) > 100 {
		return nil, errors.New("召唤师名称过长")
	}
	if index := strings.LastIndex(query, "#"); index >= 0 {
		name, tag := strings.TrimSpace(query[:index]), strings.TrimSpace(query[index+1:])
		if name == "" || tag == "" {
			return nil, errors.New("请输入完整名称#编号")
		}
		var aliases []map[string]any
		err := request(ctx, access, http.MethodPost, "/lol-summoner/v1/summoners/aliases", []any{map[string]string{"gameName": name, "tagLine": tag}}, &aliases)
		if err != nil {
			return nil, err
		}
		if len(aliases) == 0 {
			return nil, errors.New("当前大区未找到该 Riot ID")
		}
		return aliases[0], nil
	}
	return nil, errors.New("请先搜索同名账号并确认，或输入完整名称#编号")
}

// watchSocket restores the local event subscription until its connection generation ends.
// Socket closure and cancellation drop the old credentials before retrying. Only
// relevant URI changes wake HTTP synchronization; raw payloads are never published.
func watchSocket(ctx context.Context, access history.Access, wake func()) {
	for ctx.Err() == nil {
		dialer := websocket.Dialer{HandshakeTimeout: 4 * time.Second, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil} // LCU loopback certificate only.
		headers := http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("riot:"+access.Token))}}
		connection, _, err := dialer.DialContext(ctx, fmt.Sprintf("wss://127.0.0.1:%d/", access.Port), headers)
		if err == nil {
			connection.SetReadLimit(4 << 20)
			stopClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
			err = connection.WriteMessage(websocket.TextMessage, []byte(`[5,"OnJsonApiEvent"]`))
			if err == nil {
				wake()
			}
			for err == nil {
				var payload []byte
				_, payload, err = connection.ReadMessage()
				if err == nil && relevantEvent(payload) {
					wake()
				}
			}
			stopClose()
			_ = connection.Close()
		}
		// Retry independently from HTTP recovery so an unavailable subscription cannot block account or phase synchronization through polling.
		if !wait(ctx, 3*time.Second) {
			return
		}
	}
}

// relevantEvent admits only endpoint families that change visible client state.
func relevantEvent(payload []byte) bool {
	var frame []json.RawMessage
	if json.Unmarshal(payload, &frame) != nil || len(frame) != 3 || string(frame[0]) != "8" || string(frame[1]) != `"OnJsonApiEvent"` {
		return false
	}
	var event struct {
		URI string `json:"uri"`
	}
	if json.Unmarshal(frame[2], &event) != nil {
		return false
	}
	for _, prefix := range []string{"/lol-matchmaking/v1/ready-check", "/lol-gameflow/v1/", "/lol-champ-select/v1/", "/lol-lobby-team-builder/champ-select/v1/subset-champion-list", "/lol-lobby/v2/lobby", "/lol-summoner/v1/current-summoner", "/entitlements/v1/token", "/lol-league-session/v1/league-session-token", "/lol-match-history/v1/"} {
		if strings.HasPrefix(event.URI, prefix) {
			return true
		}
	}
	return false
}

// wait pauses one owned worker until its deadline or cancellation arrives.
// Cancellation returns false and stops the timer; reaching the deadline returns
// true so the caller may continue its owned polling or recovery loop.
func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
