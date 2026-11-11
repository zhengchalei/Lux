package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// timeline_test.go verifies visible-match admission and account-bound cached review data.
// An isolated client supplies a synthetic ten-player match. Tests observe actual
// endpoint reads, immutable response ownership, and rejection of replaced accounts
// without involving the user's client, provider settings, or model services.

// newTimelineRuntime creates a current account and one loaded synthetic match.
// The returned counters distinguish cache reuse from additional authenticated reads.
func newTimelineRuntime(t *testing.T) (*Runtime, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	reads := &atomic.Int32{}
	change := &atomic.Bool{}
	var r *Runtime
	access := searchServer(t, func(w http.ResponseWriter, req *http.Request) {
		var value any
		switch req.URL.Path {
		case "/lol-summoner/v1/current-summoner":
			value = map[string]any{"puuid": "local"}
		case "/lol-match-history/v1/games/42":
			reads.Add(1)
			people := []any{}
			identities := []any{}
			for id := 1; id <= 10; id++ {
				team := 100
				if id > 5 {
					team = 200
				}
				people = append(people, map[string]any{"participantId": id, "teamId": team, "championId": id})
				identities = append(identities, map[string]any{"participantId": id, "player": map[string]any{"puuid": "player-" + strconv.Itoa(id)}})
			}
			value = map[string]any{"gameId": 42, "mapId": 11, "participants": people, "participantIdentities": identities}
		case "/lol-match-history/v1/game-timelines/42":
			reads.Add(1)
			frames := []any{}
			for _, at := range []int{0, 180000} {
				samples := map[string]any{}
				for id := 1; id <= 10; id++ {
					samples[strconv.Itoa(id)] = map[string]any{"participantId": id, "totalGold": 500 + at/100, "minionsKilled": at / 10000, "jungleMinionsKilled": 0, "level": 1}
				}
				frames = append(frames, map[string]any{"timestamp": at, "participantFrames": samples, "events": []any{}})
			}
			value = map[string]any{"frames": frames}
			if change.Load() {
				r.mu.Lock()
				r.epoch++
				r.mu.Unlock()
			}
		default:
			t.Error("unexpected timeline endpoint")
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	})
	r = New(domain.Database{}, history.New(domain.Database{}))
	access.Account = "local"
	access.Platform = "TEST1"
	r.access = &access
	r.session = context.Background()
	r.state.Connected = true
	r.state.Account = &domain.Account{PUUID: "local"}
	r.state.Query = &domain.Profile{PUUID: "player-1", Matches: []domain.Match{{ID: 42, QueueID: 420, PlatformID: "TEST1"}}}
	return r, reads, change
}

// TestTimelineVisibleMatchAndCacheOwnership rejects arbitrary identities and reuses observed frames.
// Returned DTO mutations cannot alter the cache, and friendly opponent selection remains invalid.
func TestTimelineVisibleMatchAndCacheOwnership(t *testing.T) {
	r, reads, _ := newTimelineRuntime(t)
	for _, input := range []struct {
		player string
		id     uint64
	}{{"unknown", 42}, {"player-1", 43}} {
		if _, err := r.GetTimeline(context.Background(), input.player, input.id, 6); err == nil {
			t.Fatal("unknown match admitted")
		}
	}
	if reads.Load() != 0 {
		t.Fatal("unknown match reached transport")
	}
	view, err := r.GetTimeline(context.Background(), "player-1", 42, 6)
	if err != nil {
		t.Fatal(err)
	}
	*view.Timeline.Frames[0].Players[0].Gold = 999999
	again, err := r.GetTimeline(context.Background(), "player-1", 42, 7)
	if err != nil || reads.Load() != 2 || *again.Timeline.Frames[0].Players[0].Gold != 500 {
		t.Fatal("cache read or immutable ownership failed")
	}
	if _, err = r.GetTimeline(context.Background(), "player-1", 42, 2); err == nil {
		t.Fatal("friendly comparison accepted")
	}
}

// TestTimelineAccountAndPlatformReplacement discards results completed under a replaced account generation.
// Recorded cross-region games cannot borrow the current client's numeric timeline endpoint.
func TestTimelineAccountAndPlatformReplacement(t *testing.T) {
	r, reads, change := newTimelineRuntime(t)
	r.state.Query.Matches[0].PlatformID = "OTHER"
	if _, err := r.GetTimeline(context.Background(), "player-1", 42, 6); err == nil || reads.Load() != 0 {
		t.Fatal("cross-region game reached timeline transport")
	}
	r.state.Query.Matches[0].PlatformID = "TEST1"
	change.Store(true)
	if _, err := r.GetTimeline(context.Background(), "player-1", 42, 6); err == nil || len(r.timelineCache) != 0 {
		t.Fatal("old account result entered cache")
	}
}

// TestAnalysisCancellationOwnsExactRequest protects later analyses from stale panel cancellation.
// Empty or mismatched request identifiers cannot invoke the current operation's cancel function.
func TestAnalysisCancellationOwnsExactRequest(t *testing.T) {
	r := New(domain.Database{}, history.New(domain.Database{}))
	var canceled atomic.Int32
	r.aiCancelKey = "current"
	r.aiCancel = func() { canceled.Add(1) }
	r.CancelAIAnalysis("")
	r.CancelAIAnalysis("previous")
	if canceled.Load() != 0 {
		t.Fatal("stale panel canceled current analysis")
	}
	r.CancelAIAnalysis("current")
	if canceled.Load() != 1 {
		t.Fatal("owning panel could not cancel analysis")
	}
}

// TestAnalysisCanCancelDuringAccountVerification interrupts the first authenticated client read.
// Cancellation is registered before that read, so immediate cancellation cannot leave a hidden model request running afterward.
func TestAnalysisCanCancelDuringAccountVerification(t *testing.T) {
	started := make(chan struct{})
	access := searchServer(t, func(w http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-request.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	access.Account = "local"
	r := New(domain.Database{}, history.New(domain.Database{}))
	r.access = &access
	r.session = context.Background()
	r.state.Connected = true
	r.state.Query = &domain.Profile{PUUID: "viewed", Matches: []domain.Match{{ID: 42}}}
	done := make(chan error, 1)
	go func() {
		_, err := r.AnalyzeTimeline(context.Background(), "viewed", 42, 6, "early-cancel")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("verification read did not start")
	}
	r.CancelAIAnalysis("early-cancel")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled verification admitted model work")
		}
	case <-time.After(time.Second):
		t.Fatal("early cancellation did not release verification")
	}
}
