package domain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// normalize.go adapts LCU and SGP combat observations without losing player identity.
// LCU separates participant identities from nested combat statistics, whereas SGP
// stores both in a flat participant. Both adapters first identify the requested
// player and require an explicit victory boolean, because another player's record
// or a missing result must never become the requested player's observed loss.
//
// Complete-roster coverage remains independent from individual statistic availability.
// Missing damage, economy, incoming damage, healing, and turret fields are tracked
// independently so later summaries can report known metrics without manufacturing
// zero-valued observations for statistics omitted by either service.

// Object returns a JSON object or nil for absent observations.
// Missing and non-object values retain nil, whose reads are safe; callers must
// allocate an object themselves before writing fields into an absent observation.
func Object(v any) map[string]any { m, _ := v.(map[string]any); return m }

// Array returns a JSON list or nil when the observation is absent.
// Non-list values remain unavailable rather than becoming a fabricated item.
// Callers may iterate the returned slice but must own observations they mutate.
func Array(v any) []any { s, _ := v.([]any); return s }

// Text returns a source string without inventing labels for absent values.
func Text(v any) string { s, _ := v.(string); return s }

// Number extracts a source integer while accepting decoder-preserved JSON numeric values.
// Absent or unparsable values return zero for display fields; metric availability
// is checked separately before any zero can contribute to a statistical denominator.
func Number(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case uint64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// hasNumber reports whether the source explicitly provides a numeric combat observation.
func hasNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, int, int64, uint64:
		return true
	}
	return false
}

// Identity accepts public identifiers and rejects empty or anonymized placeholder values.
// Callers still determine whether an otherwise valid identifier is publicly visible.
// This helper never reads obfuscated identity fields or reconstructs hidden identities.
func Identity(v any) string {
	s := Text(v)
	if s == "" || len(s) > 128 {
		return ""
	}
	real := false
	for _, c := range s {
		if c != '0' && c != '-' {
			real = true
		}
		if c > unicode.MaxASCII || !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_') {
			return ""
		}
	}
	if !real {
		return ""
	}
	return s
}

// DisplayName formats a returned public Riot ID without inventing a missing tagline.
func DisplayName(v map[string]any) string {
	name := Text(v["gameName"])
	if name == "" {
		name = Text(v["displayName"])
	}
	if name == "" {
		name = Text(v["summonerName"])
	}
	if name == "" {
		name = "召唤师"
	}
	if tag := Text(v["tagLine"]); tag != "" {
		return name + "#" + tag
	}
	return name
}

// RankLabel translates solo queue metadata while distinguishing unavailable and unranked results.
func RankLabel(v map[string]any) string {
	q := Object(Object(v["queueMap"])["RANKED_SOLO_5x5"])
	tier, ok := q["tier"].(string)
	if !ok {
		return "段位暂不可用"
	}
	if tier == "" || tier == "NONE" {
		return "单排未定级"
	}
	labels := map[string]string{"IRON": "黑铁", "BRONZE": "青铜", "SILVER": "白银", "GOLD": "黄金", "PLATINUM": "铂金", "EMERALD": "翡翠", "DIAMOND": "钻石", "MASTER": "大师", "GRANDMASTER": "宗师", "CHALLENGER": "王者"}
	if label := labels[tier]; label != "" {
		tier = label
	}
	division := Text(q["division"])
	if division != "" && division != "NA" {
		tier += " " + division
	}
	return fmt.Sprintf("%s · %d LP", tier, Number(q["leaguePoints"]))
}

// ModeFromQueue recognizes client-published matchmaking and custom ARAM mode identifiers.
func ModeFromQueue(queue int, bench bool) Mode {
	switch queue {
	case 2400, 3270:
		return Mayhem
	case 450, 3200, 3210, 3220, 3230:
		return Aram
	}
	if bench {
		return Aram
	}
	return Ranked
}

// QueueLabel returns a localized display label for known matchmaking queues.
func QueueLabel(queue int) string {
	switch queue {
	case 420:
		return "单双排位"
	case 440:
		return "灵活排位"
	case 450:
		return "极地大乱斗"
	case 2400:
		return "海克斯大乱斗"
	case 3100:
		return "峡谷自定义"
	case 3200, 3210, 3220, 3230:
		return "大乱斗自定义"
	case 3270:
		return "海克斯自定义"
	case 430:
		return "匹配模式"
	case 400:
		return "征召模式"
	}
	return "其他模式"
}

// NormalizeLCU returns only the requested player's explicitly observed match perspective.
// Identity mapping must identify one participant before combat fields are copied.
// Missing victory, participant, or game identifiers reject the observation entirely.
// Missing individual metrics survive separately from the zero-valued display fields,
// allowing complete matches to provide some contribution axes while leaving others
// unknown. The returned player list retains a single local marker rather than
// assuming the first participant or first team represents the requested account.
func NormalizeLCU(game map[string]any, puuid string, db Database) (Match, bool) {
	if Identity(puuid) == "" {
		return Match{}, false
	}
	own := -1
	for _, raw := range Array(game["participantIdentities"]) {
		p := Object(raw)
		if Text(Object(p["player"])["puuid"]) == puuid {
			own = int(Number(p["participantId"]))
			break
		}
	}
	if own < 0 {
		return Match{}, false
	}
	parts := Array(game["participants"])
	var me map[string]any
	for _, raw := range parts {
		p := Object(raw)
		if int(Number(p["participantId"])) == own {
			me = p
			break
		}
	}
	if me == nil {
		return Match{}, false
	}
	s := Object(me["stats"])
	win, ok := s["win"].(bool)
	if !ok {
		return Match{}, false
	}
	m := matchBase(game, me, s, win, db)
	m.Source = "LCU"
	// LCU separates identities from combat rows; participant IDs, rather than array
	// ordering, associate each displayed Riot ID with its recorded combat statistics.
	identities := map[int64]map[string]any{}
	for _, raw := range Array(game["participantIdentities"]) {
		row := Object(raw)
		id := Number(row["participantId"])
		if _, exists := identities[id]; exists {
			identities[id] = nil
		} else if id > 0 {
			identities[id] = Object(row["player"])
		}
	}
	for _, raw := range parts {
		p := Object(raw)
		stats := Object(p["stats"])
		m.Players = append(m.Players, player(p, stats, identities[Number(p["participantId"])], int(Number(p["participantId"])) == own))
		markMissing(&m, stats)
	}
	return m, m.ID != 0
}

// NormalizeSGP returns the requested player's perspective directly from flat summary participants.
// The optional json envelope is unwrapped without cloning the source tree. Every
// copied participant preserves its own identity marker and metric availability.
// A missing queried player or missing victory rejects this record rather than
// inferring a loss. This direct mapping copies only fields Lux consumes, keeping
// the original large provider object eligible for collection after normalization.
func NormalizeSGP(raw map[string]any, puuid string, db Database) (Match, bool) {
	game := Object(raw["json"])
	if game == nil {
		game = raw
	}
	parts := Array(game["participants"])
	var me map[string]any
	if Identity(puuid) == "" {
		return Match{}, false
	}
	for _, raw := range parts {
		p := Object(raw)
		if Text(p["puuid"]) == puuid {
			me = p
			break
		}
	}
	if me == nil {
		return Match{}, false
	}
	win, ok := me["win"].(bool)
	if !ok {
		return Match{}, false
	}
	m := matchBase(game, me, me, win, db)
	m.Source = "SGP"
	for _, raw := range parts {
		p := Object(raw)
		m.Players = append(m.Players, player(p, p, p, Text(p["puuid"]) == puuid))
		markMissing(&m, p)
	}
	return m, m.ID != 0
}

// matchBase copies shared match metadata and perspective combat fields from one provider.
// Replay metadata preserves the recorded version and server. A missing end timestamp
// is derived only from an observed positive creation time and duration, using the
// source's millisecond creation timestamp and second-based duration together.
func matchBase(game, me, s map[string]any, win bool, db Database) Match {
	id := int(Number(me["championId"]))
	queue := int(Number(game["queueId"]))
	end := Number(game["gameEndTimestamp"])
	if end <= 0 && Number(game["gameCreation"]) > 0 && Number(game["gameDuration"]) > 0 {
		// LCU may omit the explicit end timestamp; its recorded creation milliseconds
		// plus duration seconds identify the end without substituting the current time.
		end = Number(game["gameCreation"]) + Number(game["gameDuration"])*1000
	}
	return Match{ID: uint64(Number(game["gameId"])), ChampionID: id, Name: db.Champion(id).Name, Queue: QueueLabel(queue), QueueID: queue, Win: win, Kills: int(Number(s["kills"])), Deaths: int(Number(s["deaths"])), Assists: int(Number(s["assists"])), CS: int(Number(s["totalMinionsKilled"]) + Number(s["neutralMinionsKilled"])), Damage: int(Number(s["totalDamageDealtToChampions"])), Gold: int(Number(s["goldEarned"])), Duration: int(Number(game["gameDuration"])), Created: Number(game["gameCreation"]), Team: int(Number(me["teamId"])), Players: []Player{}, GameType: Text(game["gameType"]), GameVersion: Text(game["gameVersion"]), PlatformID: Text(game["platformId"]), GameEnd: end, Remake: s["gameEndedInEarlySurrender"] == true || strings.HasPrefix(Text(game["endOfGameResult"]), "Abort_")}
}

// player copies one participant's public account identity and raw combat observations.
// The caller supplies the identity matched to this combat slot. Explicitly hidden
// metadata removes the link; absent names stay neutral instead of borrowing a
// champion name or another participant's alias. Riot's flat SGP naming fields
// are translated only when the regular display-name fields are unavailable.
// The caller joins split LCU identities by participant ID before invoking this
// adapter; SGP supplies its flat participant directly. An unavailable identity
// leaves combat statistics inspectable while removing account navigation.
func player(p, s, person map[string]any, local bool) Player {
	id := int(Number(p["championId"]))
	name, puuid := DisplayName(person), Identity(person["puuid"])
	if name == "召唤师" && Text(person["riotIdGameName"]) != "" {
		name = Text(person["riotIdGameName"])
		if tag := Text(person["riotIdTagline"]); tag != "" {
			name += "#" + tag
		}
	}
	if Text(person["nameVisibilityType"]) == "HIDDEN" {
		name, puuid = "身份未公开", ""
	}
	return Player{PUUID: puuid, ChampionID: id, Name: name, Team: int(Number(p["teamId"])), Local: local, Kills: int(Number(s["kills"])), Deaths: int(Number(s["deaths"])), Assists: int(Number(s["assists"])), Damage: int(Number(s["totalDamageDealtToChampions"])), Gold: int(Number(s["goldEarned"])), Taken: int(Number(s["totalDamageTaken"])), Heal: int(Number(s["totalHeal"])), TowerDamage: int(Number(s["damageDealtToTurrets"]))}
}

// markMissing retains each absent metric across every participant in one observation.
func markMissing(m *Match, s map[string]any) {
	for i, key := range []string{"totalDamageDealtToChampions", "goldEarned", "totalDamageTaken", "totalHeal", "damageDealtToTurrets"} {
		m.MissingMetrics[i] = m.MissingMetrics[i] || !hasNumber(s[key])
	}
}
