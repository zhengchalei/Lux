package client

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"lux/internal/domain"
)

// selected_review_test.go verifies selected-match ownership and live-roster return.
// Synthetic public records distinguish older matches and other-player perspectives;
// blocked loopback reads exercise supersession without changing the user's client.

// selectedReviewFixture builds a complete older game from another player's perspective.
func selectedReviewFixture(id uint64) domain.Match {
	m := domain.Match{ID: id, QueueID: 450, Queue: "极地大乱斗", Team: 200, Created: 1700000000000, Duration: 1500, GameType: "MATCHED_GAME"}
	for i := 0; i < 10; i++ {
		team := 100
		if i >= 5 {
			team = 200
		}
		m.Players = append(m.Players, domain.Player{PUUID: fmt.Sprintf("player-%d", i), Name: fmt.Sprintf("玩家%d#TEST", i), Team: team, ChampionID: 20 + i, Position: "BOTTOM", Local: i == 7})
	}
	m.Players[9].PUUID = ""
	return m
}

// TestSelectedReviewKeepsExactPerspectiveAndLiveReturn opens an older private explorer match.
// Live gameplay remains available independently, and returning cancels historical jobs without changing the main query or admitting hidden identities.
func TestSelectedReviewKeepsExactPerspectiveAndLiveReturn(t *testing.T) {
	r, _ := participantRuntime(t, nil)
	r.state.Phase = "InProgress"
	live := &domain.Battle{Key: "game:999", Players: []domain.Combatant{{PUUID: "live", Name: "实时玩家"}}}
	r.liveBattle, r.state.Battle = live, live
	m := selectedReviewFixture(84)
	r.participantProfiles = map[string]*domain.Profile{"player-7": {PUUID: "player-7", Matches: []domain.Match{m}}}
	query := r.state.Query
	if err := r.ReviewMatch(context.Background(), "player-7", 84); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	b := r.state.Battle
	if b.Review == nil || b.Review.ID != 84 || b.Review.Owner != "player-7" || b.Review.Created != m.Created || !b.Players[7].Ally || b.Players[0].Ally || b.Players[7].Champion != 27 || len(r.state.Scouts) != 9 || !r.reviewPinned || r.liveBattle != live || r.state.Query != query {
		r.mu.Unlock()
		t.Fatalf("wrong selected perspective: %+v", b)
	}
	// A newer direct client observation replaces only the retained live roster;
	// returning must expose this observation rather than the one seen at admission.
	r.liveBattle = &domain.Battle{Key: "game:1000", Players: []domain.Combatant{{PUUID: "new-live"}}}
	r.mu.Unlock()
	if err := r.ReturnToLive(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reviewPinned || r.state.Battle.Key != "game:1000" {
		t.Fatal("live context was lost")
	}
}

// TestSelectedReviewRejectsUnknownAndForeignRecords protects exact admission before network reads.
// Rejected requests preserve the current board instead of selecting the newest available match as a fallback.
func TestSelectedReviewRejectsUnknownAndForeignRecords(t *testing.T) {
	r, reads := participantRuntime(t, nil)
	before := reads.Load()
	if err := r.ReviewMatch(context.Background(), "local", 999); err == nil {
		t.Fatal("unknown match admitted")
	}
	m := selectedReviewFixture(84)
	m.PlatformID = "FOREIGN"
	r.access.Platform = "CURRENT"
	r.state.Query = &domain.Profile{PUUID: "player-7", Matches: []domain.Match{m}}
	if err := r.ReviewMatch(context.Background(), "player-7", 84); err == nil {
		t.Fatal("foreign game admitted")
	}
	if reads.Load() != before || r.state.Battle != nil {
		t.Fatal("invalid admission changed board or sent requests")
	}
}

// TestSelectedReviewCancelsSlowReads models navigation while an incomplete roster is loading.
// Newer matches, live return, and account changes cancel the old transport and prevent its late response from replacing visible state.
func TestSelectedReviewCancelsSlowReads(t *testing.T) {
	for _, transition := range []string{"new match", "live", "account"} {
		t.Run(transition, func(t *testing.T) {
			entered := make(chan struct{})
			r, _ := participantRuntime(t, func(req *http.Request) {
				if req.URL.Path == "/lol-match-history/v1/games/42" {
					close(entered)
					<-req.Context().Done()
				}
			})
			done := make(chan error, 1)
			go func() { done <- r.ReviewMatch(context.Background(), "local", 42) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("detail read never started")
			}
			switch transition {
			case "new match":
				m := selectedReviewFixture(84)
				r.mu.Lock()
				r.state.Query = &domain.Profile{PUUID: "player-7", Matches: []domain.Match{m}}
				r.mu.Unlock()
				if err := r.ReviewMatch(context.Background(), "player-7", 84); err != nil {
					t.Fatal(err)
				}
			case "live":
				if err := r.ReturnToLive(); err != nil {
					t.Fatal(err)
				}
			case "account":
				r.mu.Lock()
				r.replaceAccountLocked()
				r.mu.Unlock()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled review succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("review did not cancel")
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if transition == "new match" && (r.state.Battle == nil || r.state.Battle.Review.ID != 84) {
				t.Fatal("old response replaced selected match")
			}
			if transition != "new match" && r.state.Battle != nil {
				t.Fatal("revoked review reappeared")
			}
		})
	}
}
