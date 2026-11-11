package domain

import "testing"

// history_filter_test.go verifies exact history categories alongside competitive
// sample eligibility. Mixed queue fixtures keep ranked variants independent and
// retain inspectable raw rows, while compact summaries avoid copying participant
// rosters into additional recent-result strips used only by other workspaces.

// TestHistoryKinds separates ranked variants and maps every supported custom alias.
// Unrecognized queues remain browsable under other modes rather than acquiring an invented ranked classification.
func TestHistoryKinds(t *testing.T) {
	for kind, queues := range map[string][]int{
		"solo": {420}, "flex": {440}, "normal": {400, 430, 480, 490},
		"aram": {450, 3200, 3210, 3220, 3230}, "mayhem": {2400, 3270},
		"other": {0, 830, 1700, 700, 9999},
	} {
		for _, queue := range queues {
			if got := HistoryKind(queue); got != kind {
				t.Errorf("queue %d: got %s, want %s", queue, got, kind)
			}
		}
	}
}

// TestHistorySummariesKeepEligibilityAndExactTypes mixes competitive, custom, short, and remake records.
// Exact summaries preserve their own averages while raw records and existing broad recent strips remain available for browsing.
func TestHistorySummariesKeepEligibilityAndExactTypes(t *testing.T) {
	p := Profile{Matches: []Match{
		{ID: 1, QueueID: 420, ChampionID: 99, Duration: 1200, Kills: 10, Deaths: 2, Assists: 6, Win: true},
		{ID: 2, QueueID: 420, ChampionID: 99, Duration: 1200, Kills: 2, Deaths: 6, Assists: 6},
		{ID: 3, QueueID: 440, ChampionID: 103, Duration: 1200, Kills: 3, Deaths: 1, Assists: 7, Win: true},
		{ID: 4, QueueID: 430, ChampionID: 22, Duration: 1200},
		{ID: 5, QueueID: 450, ChampionID: 40, Duration: 1200},
		{ID: 6, QueueID: 2400, ChampionID: 37, Duration: 1200, Win: true},
		{ID: 7, QueueID: 1700, ChampionID: 1, Duration: 1200},
		{ID: 8, QueueID: 3200, ChampionID: 99, Duration: 1200, Win: true},
		{ID: 9, QueueID: 420, ChampionID: 99, Duration: 1200, Remake: true, Win: true},
		{ID: 10, QueueID: 430, ChampionID: 22, Duration: 120},
		{ID: 11, QueueID: 830, ChampionID: 22, Duration: 1200},
	}}
	PrepareProfile(&p)
	solo := p.Summaries["history:solo"]
	if solo.Games != 2 || solo.Wins != 1 || solo.Kills != 6 || solo.Deaths != 4 || solo.KDA != 3 || len(solo.Heroes) != 1 || solo.Heroes[0].Games != 2 {
		t.Fatalf("solo summary includes another type or invalid record: %+v", solo)
	}
	for kind, games := range map[string]int{"solo": 2, "flex": 1, "normal": 1, "aram": 1, "mayhem": 1, "other": 1} {
		summary, ok := p.Summaries["history:"+kind]
		if !ok || summary.Games != games || len(summary.Recent) != 0 {
			t.Errorf("wrong compact %s summary: %+v", kind, summary)
		}
	}
	if len(p.Matches) != 11 || p.Summary.Games != 7 || p.Summaries["ranked"].Games != 4 || len(p.Summaries["all"].Recent) != 7 || p.Coverage.Fetched != 11 {
		t.Fatal("history grouping changed raw rows, broad summaries, or coverage")
	}
	empty := Profile{}
	PrepareProfile(&empty)
	if got := empty.Summaries["history:solo"]; got.Games != 0 || got.Heroes == nil || got.Recent == nil {
		t.Fatalf("empty category must remain an explicit zero sample: %+v", got)
	}
}
