package history

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"lux/internal/domain"
)

// signals_test.go verifies timeline event attribution, coverage, and request sharing.
// HTTPS fixtures expose only read operations and distinguish explicit jungle roles
// from smite fallbacks, missing frames, and repeated public game perspectives.

// gankFixture builds ten public slots and a full fifteen-minute event stream.
func gankFixture() (domain.Match, map[string]any) {
	m := domain.Match{ID: 42, QueueID: 420, Duration: 1200, Team: 100, GameType: "MATCHED_GAME"}
	for i := 1; i <= 10; i++ {
		p := domain.Player{ParticipantID: i, PUUID: fmt.Sprintf("p%d", i), Team: 100, Position: "TOP", Spell1: 4, Spell2: 12, Local: i == 1}
		if i > 5 {
			p.Team = 200
		}
		if i == 2 || i == 7 {
			p.Position = "JUNGLE"
			p.Spell2 = 11
		}
		m.Players = append(m.Players, p)
	}
	event := func(at, killer int, assists []any) any {
		return map[string]any{"type": "CHAMPION_KILL", "timestamp": at, "victimId": 1, "killerId": killer, "assistingParticipantIds": assists}
	}
	return m, map[string]any{"frames": []any{map[string]any{"timestamp": 0, "events": []any{}}, map[string]any{"timestamp": 900000, "events": []any{event(120000, 7, nil), event(200000, 6, []any{7}), event(200000, 6, []any{7}), event(300000, 6, nil), event(900000, 7, nil), event(900001, 7, nil)}}}}
}

// TestEarlyGankAttribution reproduces killer, assist, duplicate, and exact-boundary cases.
// Missing coverage and an unidentified opposing jungler remain unavailable rather than zero.
func TestEarlyGankAttribution(t *testing.T) {
	m, raw := gankFixture()
	data := earlyGanks(m, raw)
	if data[1] == nil || len(data[1].Deaths) != 3 || data[1].Deaths[2] != 900000 || data[2] != nil {
		t.Fatalf("incorrect attribution: %+v", data)
	}
	if data[3] == nil || len(data[3].Deaths) != 0 {
		t.Fatal("observed zero missing")
	}
	m.Players[6].Position = ""
	if earlyGanks(m, raw)[1] == nil {
		t.Fatal("smite fallback lost")
	}
	m.Players[6].Spell2 = 12
	if earlyGanks(m, raw)[1] != nil {
		t.Fatal("unknown jungler became zero")
	}
	m, raw = gankFixture()
	domain.Object(domain.Array(raw["frames"])[1])["timestamp"] = 840000
	if earlyGanks(m, raw) != nil {
		t.Fatal("truncated timeline admitted")
	}
	m, raw = gankFixture()
	m.Players[1].ParticipantID = 1
	if earlyGanks(m, raw) != nil {
		t.Fatal("duplicate slots admitted")
	}
}

// TestGankConcurrentPerspectivesShareRead ensures repeated profiles reuse one compact observation.
// Unavailable game responses are briefly cached independently without poisoning another game.
func TestGankConcurrentPerspectivesShareRead(t *testing.T) {
	m, raw := gankFixture()
	var reads atomic.Int32
	repo, a := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path == "/lol-match-history/v1/game-timelines/42" {
			json.NewEncoder(w).Encode(raw)
		} else {
			http.Error(w, "missing", 404)
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if data := repo.gankData(context.Background(), a, m); data[1] == nil {
				t.Error("no shared sample")
			}
		}()
	}
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("%d reads", reads.Load())
	}
	m.ID = 43
	repo.gankData(context.Background(), a, m)
	repo.gankData(context.Background(), a, m)
	if reads.Load() != 2 {
		t.Fatal("failure cooldown not reused")
	}
}

// TestLiveGroupsUsesLeagueTokenAndExactGame verifies routing and public identity boundaries.
// A mismatched game response cannot publish current grouping or expose hidden members.
func TestLiveGroupsUsesLeagueTokenAndExactGame(t *testing.T) {
	repo, a := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-league" {
			t.Error("wrong GSM token")
		}
		json.NewEncoder(w).Encode(map[string]any{"game": map[string]any{"gameId": 42, "teamOne": []any{map[string]any{"puuid": "p1", "teamParticipantId": 9}, map[string]any{"puuid": "p2", "teamParticipantId": 0}, map[string]any{"puuid": "hidden", "teamParticipantId": 9, "nameVisibilityType": "HIDDEN"}}}})
	})
	s := repo.directory.Servers["NA1"]
	s.Common = s.MatchHistory
	repo.directory.Servers["NA1"] = s
	groups, err := repo.LiveGroups(context.Background(), a, 42)
	if err != nil || groups["p1"] != "9" || len(groups) != 1 {
		t.Fatalf("groups %+v: %v", groups, err)
	}
	if _, err = repo.LiveGroups(context.Background(), a, 43); err == nil {
		t.Fatal("wrong game accepted")
	}
}

// TestGankEnrichmentRejectsForeignPlatforms reproduces equal numeric IDs across servers.
// A foreign record must remain unknown without sending a timeline request to the connected server.
func TestGankEnrichmentRejectsForeignPlatforms(t *testing.T) {
	m, _ := gankFixture()
	var reads atomic.Int32
	repo, a := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		http.Error(w, "unexpected read", 500)
	})
	a.Platform = "NA1"
	m.PlatformID = "EUW1"
	p := domain.Profile{Matches: []domain.Match{m}}
	repo.EnrichEarlyGanks(context.Background(), a, &p)
	if reads.Load() != 0 || p.Matches[0].EarlyGank != nil {
		t.Fatal("foreign timeline was requested or assumed observed")
	}
}
