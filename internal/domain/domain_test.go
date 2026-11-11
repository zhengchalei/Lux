package domain

import "testing"

// domain_test.go exercises sample eligibility and player-identity normalization.
// Fixtures contain synthetic combat values only. Tests protect real calculation
// rules rather than snapshotting presentation text or requiring an installed client.

// completeMatch constructs a synthetic ten-player combat observation with one local marker.
func completeMatch() Match {
	m := Match{ID: 1, QueueID: 420, Duration: 1200, GameType: "MATCHED_GAME", ChampionID: 99, Team: 100}
	for i := 0; i < 10; i++ {
		team := 100
		if i >= 5 {
			team = 200
		}
		damage := 100
		if i == 0 {
			damage = 200
		}
		m.Players = append(m.Players, Player{ChampionID: 99, Team: team, Local: i == 0, Damage: damage, Gold: 100, Taken: 100})
	}
	return m
}

// TestRadarUsesMatchMaximum reproduces highest damage with a partial friendly-team share.
// The largest contributor reaches the radar edge while retaining its separate team percentage.
func TestRadarUsesMatchMaximum(t *testing.T) {
	m := completeMatch()
	share, radar, rank, average, ok := m.Contributions(0)
	if !ok || radar != 1 || rank != 1 || share >= 1 || average >= 1 {
		t.Fatalf("unexpected contribution: %v %v %v %v %v", share, radar, rank, average, ok)
	}
	m.MissingMetrics[0] = true
	if _, _, _, _, ok := m.Contributions(0); ok {
		t.Fatal("missing metric became observed zero")
	}
	if _, _, _, _, ok := m.Contributions(1); !ok {
		t.Fatal("missing damage contaminated known economy")
	}
	m.Players[9].Team = 300
	if m.Complete() {
		t.Fatal("malformed teams became complete")
	}
}

// TestSampleScopesAndTotals mixes valid modes, a custom game, and a remake.
// Summaries retain the same result sample and divide total KDA separately from combat averages.
func TestSampleScopesAndTotals(t *testing.T) {
	p := Profile{Matches: []Match{{ID: 1, ChampionID: 99, QueueID: 420, Duration: 1200, Kills: 10, Deaths: 0, Assists: 10, Win: true}, {ID: 2, ChampionID: 99, QueueID: 420, Duration: 1200, Kills: 2, Deaths: 6, Assists: 2}, {ID: 3, ChampionID: 103, QueueID: 450, Duration: 1200, Kills: 1, Deaths: 1, Assists: 1, Win: true}, {ID: 4, QueueID: 3200, Duration: 1200, Win: true}, {ID: 5, QueueID: 420, Duration: 1200, Remake: true, Win: true}}}
	PrepareProfile(&p)
	s := p.Summaries["ranked"]
	if s.Games != 2 || s.Wins != 1 || len(s.Recent) != 2 || s.KDA != 4 || s.Kills != 6 || s.Deaths != 3 || s.Heroes[0].Games != 2 {
		t.Fatalf("wrong ranked sample: %+v", s)
	}
	if p.Summary.Games != 3 || p.Summaries["aram"].Games != 1 {
		t.Fatal("mode samples overlap or include invalid games")
	}
}

// TestIndependentContributionCoverage omits damage in one otherwise complete observed game.
// Economy still has two samples while damage averages only its one known denominator.
func TestIndependentContributionCoverage(t *testing.T) {
	first, second := completeMatch(), completeMatch()
	second.ID = 2
	second.MissingMetrics[0] = true
	s := Summarize([]Match{first, second}, Ranked)
	if s.Contribution.Games != 2 || s.Contribution.DamageGames != 1 || s.Contribution.GoldGames != 2 || s.Contribution.DamageShare == nil {
		t.Fatalf("wrong coverage %+v", s.Contribution)
	}
}

// TestAdaptersRequirePlayerAndVictory protects identity mapping against missing source semantics.
// Neither provider may substitute the first participant or infer a loss from an absent result.
func TestAdaptersRequirePlayerAndVictory(t *testing.T) {
	game := map[string]any{"gameId": 1, "queueId": 420, "gameDuration": 1200, "participantIdentities": []any{map[string]any{"participantId": 1, "player": map[string]any{"puuid": "player-a"}}}, "participants": []any{map[string]any{"participantId": 1, "championId": 99, "teamId": 100, "stats": map[string]any{"win": true, "kills": 7}}}}
	m, ok := NormalizeLCU(game, "player-a", Database{})
	if !ok || m.Kills != 7 || !m.Win || !m.MissingMetrics[0] {
		t.Fatal("LCU observation lost source semantics")
	}
	if _, ok := NormalizeLCU(game, "player-b", Database{}); ok {
		t.Fatal("other player substituted")
	}
	delete(Object(Array(game["participants"])[0])["stats"].(map[string]any), "win")
	if _, ok := NormalizeLCU(game, "player-a", Database{}); ok {
		t.Fatal("absent victory inferred")
	}
	sgp := map[string]any{"json": map[string]any{"gameId": 2, "participants": []any{map[string]any{"puuid": "player-a", "win": true, "championId": 99}}}}
	if _, ok := NormalizeSGP(sgp, "player-b", Database{}); ok {
		t.Fatal("SGP player substituted")
	}
	if _, ok := NormalizeSGP(sgp, "player-a", Database{}); !ok {
		t.Fatal("SGP explicit observation rejected")
	}
}

// TestIdentityAndModeMetadata rejects placeholder identifiers and keeps unknown ranks distinct.
// Client-published custom Mayhem remains Mayhem even before a bench flag becomes available.
func TestIdentityAndModeMetadata(t *testing.T) {
	for _, id := range []string{"", "0000-0000", "bad/id", "a?b"} {
		if Identity(id) != "" {
			t.Fatalf("invalid identity accepted: %q", id)
		}
	}
	if Identity("real-id_4") == "" || ModeFromQueue(3270, false) != Mayhem || ModeFromQueue(3200, false) != Aram || RankLabel(nil) != "段位暂不可用" {
		t.Fatal("metadata interpretation changed")
	}
}

// TestReplayMetadataRetainsVersionAndEndUnits compares recorded and derived game-end timestamps.
// Both providers preserve the original version and platform, and incomplete timing never substitutes the current clock.
func TestReplayMetadataRetainsVersionAndEndUnits(t *testing.T) {
	game := map[string]any{"gameId": 42, "gameVersion": "16.21.5", "platformId": "TEST1", "gameCreation": int64(1700000000000), "gameDuration": 1200, "gameType": "MATCHED_GAME"}
	for _, explicit := range []int64{0, 1700001234000} {
		game["gameEndTimestamp"] = explicit
		m := matchBase(game, map[string]any{}, map[string]any{}, true, Database{})
		want := int64(1700001200000)
		if explicit > 0 {
			want = explicit
		}
		if m.GameVersion != "16.21.5" || m.PlatformID != "TEST1" || m.GameEnd != want {
			t.Fatalf("source metadata lost: %#v", m)
		}
	}
	delete(game, "gameCreation")
	delete(game, "gameEndTimestamp")
	if m := matchBase(game, nil, nil, true, Database{}); m.GameEnd != 0 {
		t.Fatal("missing timing invented an end timestamp")
	}
}

// TestRankLabelOmitsUnavailableDivision keeps apex ranks free of protocol placeholders.
func TestRankLabelOmitsUnavailableDivision(t *testing.T) {
	for _, division := range []string{"", "NA"} {
		rank := map[string]any{"queueMap": map[string]any{"RANKED_SOLO_5x5": map[string]any{"tier": "MASTER", "division": division, "leaguePoints": 178}}}
		if label := RankLabel(rank); label != "大师 · 178 LP" {
			t.Fatalf("protocol placeholder in rank: %s", label)
		}
	}
}
