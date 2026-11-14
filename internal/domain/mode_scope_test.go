package domain

import "testing"

// mode_scope_test.go checks contradictory records under the two ARAM rule sets.
// Aggregate counts, outcomes, combat values, champion usage, and recent strips
// must stay within the requested mode. Custom variants remain available as raw
// history without entering either mode's competitive player-performance sample.

// TestAramAndMayhemSummariesNeverShareRecords prepares different results under each ruleset.
// Neither repeated preparation nor custom queues may leak another mode's champion or combat averages.
func TestAramAndMayhemSummariesNeverShareRecords(t *testing.T) {
	p := Profile{Matches: []Match{
		{ID: 1, QueueID: 450, Duration: 1200, ChampionID: 99, Win: false, Kills: 2, Deaths: 8, Assists: 4},
		{ID: 2, QueueID: 2400, Duration: 1200, ChampionID: 103, Win: true, Kills: 20, Deaths: 1, Assists: 30},
		{ID: 3, QueueID: 3270, Duration: 1200, ChampionID: 81, Win: true, Kills: 99},
		{ID: 4, QueueID: 3200, Duration: 1200, ChampionID: 81, Win: true, Kills: 99},
	}}
	for i := 0; i < 2; i++ {
		PrepareProfile(&p)
		aram, mayhem := p.Summaries["aram"], p.Summaries["mayhem"]
		if aram.Games != 1 || aram.Wins != 0 || aram.Kills != 2 || aram.Deaths != 8 || aram.Heroes[0].ChampionID != 99 || aram.Recent[0].ID != 1 {
			t.Fatalf("ordinary scope contaminated: %+v", aram)
		}
		if mayhem.Games != 1 || mayhem.Wins != 1 || mayhem.Kills != 20 || mayhem.Assists != 30 || mayhem.Heroes[0].ChampionID != 103 || mayhem.Recent[0].ID != 2 {
			t.Fatalf("Mayhem scope contaminated: %+v", mayhem)
		}
		if p.Summary.Games != 2 {
			t.Fatal("custom variant entered competitive sample")
		}
	}
	p.Matches = p.Matches[:1]
	PrepareProfile(&p)
	if missing := p.Summaries["mayhem"]; missing.Games != 0 || len(missing.Recent) != 0 || len(missing.Heroes) != 0 {
		t.Fatalf("missing Mayhem scope retained old records: %+v", missing)
	}
}
