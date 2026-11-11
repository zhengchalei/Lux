package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"lux/internal/domain"
)

// timeline.go normalizes client samples without inferring continuous movement or intent.
// Match identities establish the viewed participant before timeline slots are read.
// Explicit roles may select one opposing counterpart; ambiguous or shared roles
// require manual selection instead of relying on participant ordering or champion type.
//
// Events retain their original millisecond ordering and stable evidence references.
// Unsupported event types never become invented ward, purchase, or ability events.
// Missing metrics remain independent pointers, and minion totals require both
// lane and jungle observations. Sample boundaries preserve irregular timestamps
// so later comparison cannot silently substitute a presumed exact minute.

// observedInt returns a nonnegative integral measurement or nil when unavailable.
// Numeric strings and fractional values remain unknown; consumers preserve that
// distinction in charts and model facts instead of substituting a measured zero.
func observedInt(value any) *int {
	var n int
	switch v := value.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(string(v))
		if err != nil {
			return nil
		}
		n = parsed
	case float64:
		n = int(v)
		if float64(n) != v {
			return nil
		}
	case int:
		n = v
	default:
		return nil
	}
	if n < 0 {
		return nil
	}
	return &n
}

// addKnown combines measurements only when both source values were actually observed.
// Both lane and jungle counts must exist before their combined minion total can
// support comparisons; an absent component leaves the combined measurement unknown.
func addKnown(a, b *int) *int {
	if a == nil || b == nil {
		return nil
	}
	result := *a + *b
	return &result
}

// role reads explicit modern positions or disambiguates legacy lane and duo-role fields.
// Modern teamPosition and individualPosition values take precedence when recognized.
// Legacy support and carry markers distinguish the two bottom-lane participants;
// solo top, middle, and jungle lanes remain usable without those duo markers.
// Ambiguous bottom assignments return an empty role, requiring the caller to keep
// opponent selection manual instead of inferring the player's position from order.
func role(person map[string]any) string {
	for _, key := range []string{"teamPosition", "individualPosition"} {
		switch v := domain.Text(person[key]); v {
		case "TOP", "JUNGLE", "MIDDLE", "BOTTOM", "UTILITY", "SUPPORT":
			if v == "UTILITY" {
				return "SUPPORT"
			}
			return v
		}
	}
	t := domain.Object(person["timeline"])
	lane, assigned := domain.Text(t["lane"]), domain.Text(t["role"])
	if assigned == "DUO_SUPPORT" {
		return "SUPPORT"
	}
	if assigned == "DUO_CARRY" && lane == "BOTTOM" {
		return "BOTTOM"
	}
	if lane == "TOP" || lane == "JUNGLE" || lane == "MIDDLE" {
		return lane
	}
	return ""
}

// Normalize associates client timeline slots with a verified recorded player identity.
// Missing roster ownership, duplicate slots, malformed frame timestamps, and oversized
// sources fail before coaching. Roles and supported event labels are observations;
// automatic pairing requires one unique matching opposing role on Summoner's Rift.
// Frames are sorted without synthesizing missing timestamps. Missing participant
// samples stay absent, and interval calculations later require both sampled boundaries.
func Normalize(match, raw map[string]any, puuid string, gameID uint64, queue int) (domain.Timeline, error) {
	if wrapped := domain.Object(match["json"]); wrapped != nil {
		match = wrapped
	}
	result := domain.Timeline{GameID: gameID, MapID: int(domain.Number(match["mapId"])), QueueID: queue, Version: domain.Text(match["gameVersion"]), Players: []domain.TimelinePlayer{}, Frames: []domain.TimelineFrame{}, Events: []domain.TimelineEvent{}}
	if uint64(domain.Number(match["gameId"])) != gameID || gameID == 0 || domain.Identity(puuid) == "" {
		return result, errors.New("时间线与所选比赛不一致")
	}
	identities := map[int]map[string]any{}
	for _, value := range domain.Array(match["participantIdentities"]) {
		identity := domain.Object(value)
		identities[int(domain.Number(identity["participantId"]))] = domain.Object(identity["player"])
	}
	players := map[int]domain.TimelinePlayer{}
	for _, value := range domain.Array(match["participants"]) {
		person := domain.Object(value)
		id := int(domain.Number(person["participantId"]))
		if id < 1 || id > 10 || players[id].ID != 0 {
			return result, errors.New("比赛参与者标识不完整")
		}
		identity := identities[id]
		if identity == nil {
			identity = person
		}
		player := domain.TimelinePlayer{ID: id, Champion: int(domain.Number(person["championId"])), Team: int(domain.Number(person["teamId"])), Name: domain.DisplayName(identity), Role: role(person)}
		if player.Team != 100 && player.Team != 200 {
			return result, errors.New("比赛缺少明确队伍信息")
		}
		players[id] = player
		result.Players = append(result.Players, player)
		if domain.Text(identity["puuid"]) == puuid {
			if result.OwnID != 0 {
				return result, errors.New("比赛玩家身份存在重复")
			}
			result.OwnID = id
		}
	}
	if result.OwnID == 0 || len(players) != 10 {
		return result, errors.New("比赛缺少所选玩家的完整阵容")
	}
	own := players[result.OwnID]
	if result.MapID == 11 && own.Role != "" {
		for _, p := range result.Players {
			if p.Team != own.Team && p.Role == own.Role {
				if result.SuggestedOpponent != 0 {
					result.SuggestedOpponent = 0
					break
				}
				result.SuggestedOpponent = p.ID
			}
		}
	}
	frames := domain.Array(raw["frames"])
	// The frame budget admits over eight hours at normal minute sampling without allowing an unexpected payload to grow retained review memory indefinitely.
	if len(frames) < 2 || len(frames) > 512 {
		return result, errors.New("客户端没有提供可用的时间线采样")
	}
	seenFrames := map[int64]bool{}
	for _, value := range frames {
		frame := domain.Object(value)
		at := observedInt(frame["timestamp"])
		if at == nil || seenFrames[int64(*at)] {
			return result, errors.New("时间线采样时间异常")
		}
		seenFrames[int64(*at)] = true
		next := domain.TimelineFrame{At: int64(*at), Players: []domain.TimelineStats{}}
		for id := 1; id <= 10; id++ {
			stats := domain.Object(domain.Object(frame["participantFrames"])[strconv.Itoa(id)])
			if stats == nil || int(domain.Number(stats["participantId"])) != id {
				continue
			}
			position := domain.Object(stats["position"])
			next.Players = append(next.Players, domain.TimelineStats{ID: id, Gold: observedInt(stats["totalGold"]), CS: addKnown(observedInt(stats["minionsKilled"]), observedInt(stats["jungleMinionsKilled"])), XP: observedInt(stats["xp"]), Level: observedInt(stats["level"]), X: observedInt(position["x"]), Y: observedInt(position["y"])})
		}
		result.Frames = append(result.Frames, next)
		for _, value := range domain.Array(frame["events"]) {
			event := domain.Object(value)
			kind := domain.Text(event["type"])
			timestamp := observedInt(event["timestamp"])
			if timestamp == nil {
				continue
			}
			row := domain.TimelineEvent{At: int64(*timestamp), Kind: kind, Actor: int(domain.Number(event["killerId"])), Target: int(domain.Number(event["victimId"])), Assists: []int{}}
			for _, helper := range domain.Array(event["assistingParticipantIds"]) {
				if id := int(domain.Number(helper)); players[id].ID != 0 {
					row.Assists = append(row.Assists, id)
				}
			}
			row.Team = players[row.Actor].Team
			switch kind {
			case "CHAMPION_KILL":
				if players[row.Target].ID == 0 {
					continue
				}
				row.Label = "英雄击杀"
			case "ELITE_MONSTER_KILL":
				labels := map[string]string{"DRAGON": "小龙", "BARON_NASHOR": "纳什男爵", "RIFTHERALD": "峡谷先锋", "HORDE": "虚空巢虫", "ATAKHAN": "厄塔汗"}
				row.Label = labels[domain.Text(event["monsterType"])]
				if row.Label == "" {
					row.Label = "史诗资源"
				}
			case "BUILDING_KILL":
				row.Label = "建筑被摧毁"
				if domain.Text(event["buildingType"]) == "TOWER_BUILDING" {
					row.Label = "防御塔被摧毁"
				}
				if domain.Text(event["buildingType"]) == "INHIBITOR_BUILDING" {
					row.Label = "召唤水晶被摧毁"
				}
			default:
				continue
			}
			result.Events = append(result.Events, row)
		}
	}
	sort.SliceStable(result.Frames, func(i, j int) bool { return result.Frames[i].At < result.Frames[j].At })
	sort.SliceStable(result.Events, func(i, j int) bool { return result.Events[i].At < result.Events[j].At })
	for i := range result.Events {
		result.Events[i].ID = fmt.Sprintf("E%d", i+1)
	}
	intervals := []int{}
	for i := 1; i < len(result.Frames); i++ {
		intervals = append(intervals, int((result.Frames[i].At-result.Frames[i-1].At)/1000))
	}
	sort.Ints(intervals)
	result.SampleSeconds = intervals[len(intervals)/2]
	return result, nil
}
