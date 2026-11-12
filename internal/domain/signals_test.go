package domain

import "testing"

// signals_test.go protects independent sample denominators and Akari score rules.
// Synthetic matches include explicit observed zeros and missing measurements so
// unavailable data cannot create a performance label or change flash evidence.

// signalFixture returns one complete high-performing Rift perspective with every metric.
func signalFixture() Match {
	cs, vision, zero := 300, 100, 0
	m := Match{ID: 1, QueueID: 420, GameType: "MATCHED_GAME", Duration: 1800, Team: 100, Kills: 12, Deaths: 2, Assists: 18, Win: true}
	for i := 1; i <= 10; i++ {
		p := Player{ParticipantID: i, PUUID: string(rune('a' + i)), Team: 100, CS: &zero, Vision: &zero, Spell1: 4, Spell2: 12, Position: "TOP"}
		if i > 5 {
			p.Team = 200
		}
		if i == 1 {
			p.Local = true
			p.CS = &cs
			p.Vision = &vision
			p.Kills = 12
			p.Deaths = 2
			p.Assists = 18
			p.Damage = 100
			p.Taken = 100
			p.Gold = 100
		}
		m.Players = append(m.Players, p)
	}
	return m
}

// TestSignalScoreThresholds reproduces both minimum counts and full component caps.
// Missing vision removes the score sample while preserving independent flash observations.
func TestSignalScoreThresholds(t *testing.T) {
	for _, tc := range []struct {
		count int
		label string
	}{{1, ""}, {4, ""}, {5, "优异"}, {7, "优异"}, {8, "通天代"}} {
		matches := []Match{}
		for i := 0; i < tc.count; i++ {
			matches = append(matches, signalFixture())
		}
		s := SummarizeSignals(matches, Ranked)
		if s.Score == nil || s.Score.Label != tc.label || s.Score.Total != 15 {
			t.Fatalf("%d games: %+v", tc.count, s.Score)
		}
		if s.Score.Parts != [8]float64{1, 1, 3, 2, 2, 2, 2, 2} {
			t.Fatal(s.Score.Parts)
		}
	}
	m := signalFixture()
	m.Players[1].Vision = nil
	s := SummarizeSignals([]Match{m}, Ranked)
	if s.Score != nil || s.FlashD != 1 {
		t.Fatalf("missing vision became a score: %+v", s)
	}
}

// TestSignalCoverageSeparatesModesAndUnknownTimelines keeps a known zero distinct from absence.
// Remakes and other modes cannot inflate the eligible gank or flash denominators.
func TestSignalCoverageSeparatesModesAndUnknownTimelines(t *testing.T) {
	m := signalFixture()
	m.EarlyGank = &EarlyGank{Deaths: []int64{}}
	other := signalFixture()
	other.QueueID = 450
	other.Players[0].Spell1, other.Players[0].Spell2 = 12, 4
	missing := signalFixture()
	remake := signalFixture()
	remake.Remake = true
	s := SummarizeSignals([]Match{m, other, missing, remake}, Ranked)
	if s.GankGames != 1 || s.GankDeaths != 0 || s.FlashD != 2 || s.FlashF != 0 || s.Score.Games != 2 {
		t.Fatalf("mixed coverage: %+v", s)
	}
	s = SummarizeSignals([]Match{m, other, missing}, "")
	if s.FlashD != 2 || s.FlashF != 1 {
		t.Fatal(s)
	}
	for _, v := range []float64{-2, 0, .49, .5} {
		if signalRange(v, .5, 1, 1) != 0 {
			t.Fatal("baseline clamp")
		}
	}
}
