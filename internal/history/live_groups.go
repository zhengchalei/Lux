package history

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"lux/internal/domain"
)

// live_groups.go reads current-game grouping from the configured same-region GSM
// service. The league-session token is used only with the validated common origin,
// independently from match-history entitlement authentication. Returned membership
// is accepted only for the exact current numeric game requested by the caller.
//
// The caller owns account and runtime generation checks because transport can
// finish after a live game changes. This adapter limits its result to positive
// group identifiers attached to visible public accounts, leaving hidden slots
// unavailable. Runtime merging additionally restricts those memberships to its
// existing public roster and retains LCU values where both sources are present.
// Neither this response nor a local alphabetic squad label identifies a captain.

// LiveGroups returns public current-game participant group identifiers for exact game ownership.
// Missing routes, game mismatches, and unavailable GSM responses return errors.
// Callers retain their existing LCU roster and merge only already visible players.
func (r *Repository) LiveGroups(ctx context.Context, a Access, id uint64) (map[string]string, error) {
	access, err := r.access(ctx, a)
	if err != nil {
		return nil, err
	}
	if access.Common == "" || domain.Identity(a.Account) == "" {
		return nil, errors.New("当前大区没有可用的对局分组接口")
	}
	access.Origin, access.Bearer = access.Common, access.LeagueBearer
	var raw map[string]any
	err = r.admitted(ctx, func() error {
		return remoteGet(ctx, access, "/gsm/v1/ledge/region/"+url.PathEscape(access.Path)+"/puuid/"+url.PathEscape(a.Account), &raw)
	})
	if err != nil {
		return nil, err
	}
	game := domain.Object(raw["game"])
	if uint64(domain.Number(game["gameId"])) != id {
		return nil, errors.New("对局分组与当前比赛不一致")
	}
	result := map[string]string{}
	for _, side := range []string{"teamOne", "teamTwo"} {
		for _, value := range domain.Array(game[side]) {
			p := domain.Object(value)
			who := domain.Identity(p["puuid"])
			group := domain.Number(p["teamParticipantId"])
			if who != "" && domain.Text(p["nameVisibilityType"]) != "HIDDEN" && group > 0 {
				result[who] = strconv.FormatInt(group, 10)
			}
		}
	}
	return result, nil
}

// GameID extracts the numeric live game from the runtime's explicit association key.
func GameID(key string) uint64 { var id uint64; _, _ = fmt.Sscanf(key, "game:%d", &id); return id }
