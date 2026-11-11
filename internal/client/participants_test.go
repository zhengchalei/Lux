package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// participants_test.go verifies contextual player navigation against an isolated
// loopback client. Tests protect exact roster admission, same-name account routing,
// private profile ownership, branch cleanup, and cancellation on dismissal or
// account replacement without querying the user's client or sending game commands.

// participantRuntime creates a connected fixture with one recorded public opponent.
// The handler observes read requests only. Caller cleanup cancels background public
// routing refreshes and any profile transport remaining after an assertion fails.
func participantRuntime(t *testing.T, before func(*http.Request)) (*Runtime, *atomic.Int32) {
	t.Helper()
	reads := &atomic.Int32{}
	access := searchServer(t, func(w http.ResponseWriter, req *http.Request) {
		reads.Add(1)
		if req.Method != http.MethodGet {
			t.Error("participant navigation submitted a write")
		}
		if before != nil {
			before(req)
		}
		if req.Context().Err() != nil {
			return
		}
		var response any
		switch {
		case strings.HasPrefix(req.URL.Path, "/lol-summoner/v2/summoners/puuid/"):
			id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
			response = map[string]any{"puuid": id, "gameName": "同名", "tagLine": id, "summonerLevel": 87, "profileIconId": 12}
		case strings.HasPrefix(req.URL.Path, "/lol-ranked/v1/ranked-stats/"):
			response = map[string]any{}
		case strings.HasPrefix(req.URL.Path, "/lol-match-history/v1/products/lol/"):
			response = map[string]any{"games": map[string]any{"games": []any{}}}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	r := New(domain.Database{}, history.New(domain.Database{}))
	r.root, r.cancel = context.WithCancel(context.Background())
	r.session, r.endSession = context.WithCancel(r.root)
	r.access = &access
	r.state.Connected = true
	r.state.Account = &domain.Account{PUUID: "local"}
	r.state.Analysis.MatchCount = 20
	r.state.History = &domain.Profile{PUUID: "local", Matches: []domain.Match{{ID: 42, Players: []domain.Player{{PUUID: "opponent", Name: "同名#opponent"}}}}}
	t.Cleanup(func() { r.cancel(); r.endSession() })
	return r, reads
}

// TestParticipantLookupContextAndOwnership rejects forged identities before transport and retains exact results privately.
// Closing releases authorization, stale closes preserve newer explorers, and main query records remain untouched throughout the profile journey.
func TestParticipantLookupContextAndOwnership(t *testing.T) {
	r, reads := participantRuntime(t, nil)
	for _, input := range []struct {
		owner, target string
		game          uint64
	}{
		{"local", "not-in-roster", 42}, {"other-owner", "opponent", 42}, {"local", "opponent", 43}, {"local", "../invalid", 42},
	} {
		if _, err := r.GetParticipantProfile(context.Background(), "view-one", input.owner, input.game, input.target, nil); err == nil {
			t.Fatal("unobserved participant admitted")
		}
	}
	if reads.Load() != 0 {
		t.Fatal("invalid identity reached transport")
	}
	query := &domain.Profile{PUUID: "separate-query", Name: "unchanged"}
	r.state.Query = query
	revision := r.CurrentRevision()
	result, err := r.GetParticipantProfile(context.Background(), "view-one", "local", 42, "opponent", nil)
	if err != nil || result.PUUID != "opponent" || result.Name != "同名#opponent" || result.Level != 87 || result.IconID != 12 {
		t.Fatalf("wrong participant metadata: %+v %v", result, err)
	}
	result.Name = "caller-mutated"
	if r.participantProfiles["opponent"].Name == result.Name || r.state.Query != query || r.CurrentRevision() != revision {
		t.Fatal("player navigation mutated query, snapshot revision, or retained ownership")
	}
	r.participantProfiles["opponent"].Matches = []domain.Match{{ID: 84, Players: []domain.Player{{PUUID: "next", Name: "同名#next"}}}}
	result, err = r.GetParticipantProfile(context.Background(), "view-one", "opponent", 84, "next", []string{"opponent"})
	if err != nil || result.PUUID != "next" || len(r.participantProfiles) != 2 {
		t.Fatalf("chained public participant failed: %v", err)
	}
	r.participantProfiles["abandoned-branch"] = &domain.Profile{}
	if _, err = r.GetParticipantProfile(context.Background(), "view-one", "local", 42, "opponent", nil); err != nil || len(r.participantProfiles) != 1 {
		t.Fatal("unused navigation branches retained")
	}
	if _, err = r.GetParticipantProfile(context.Background(), "view-two", "local", 42, "opponent", nil); err != nil {
		t.Fatal(err)
	}
	r.CloseParticipantExplorer("view-one")
	if r.participantProfiles["opponent"] == nil {
		t.Fatal("stale close cleared newer explorer")
	}
	r.CloseParticipantExplorer("view-two")
	if len(r.participantProfiles) != 0 || r.visibleIdentityLocked("opponent") {
		t.Fatal("closed player retained read authorization")
	}
}

// TestParticipantDismissalAndAccountChangeCancelReads interrupts a participant metadata read before history begins.
// Cancellation must finish promptly and cannot retain a late result under a closed page or replacement account generation.
func TestParticipantDismissalAndAccountChangeCancelReads(t *testing.T) {
	for _, transition := range []string{"close", "account"} {
		t.Run(transition, func(t *testing.T) {
			entered := make(chan struct{})
			r, _ := participantRuntime(t, func(req *http.Request) {
				if strings.HasPrefix(req.URL.Path, "/lol-summoner/v2/summoners/puuid/") {
					close(entered)
					<-req.Context().Done()
				}
			})
			done := make(chan error, 1)
			go func() {
				_, err := r.GetParticipantProfile(context.Background(), "closing-view", "local", 42, "opponent", nil)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("profile request did not start")
			}
			if transition == "close" {
				r.CloseParticipantExplorer("closing-view")
			} else {
				r.mu.Lock()
				r.replaceAccountLocked()
				r.mu.Unlock()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled profile returned success")
				}
			case <-time.After(time.Second):
				t.Fatal("dismissal did not cancel profile transport")
			}
			if len(r.participantProfiles) != 0 {
				t.Fatal("cancelled result survived navigation")
			}
		})
	}
}

// TestParticipantSupersessionAndRosterReplacement exercises changes during an admitted read.
// A newer target cancels the old transport, and a replaced source roster cannot publish a newly unauthorized profile.
func TestParticipantSupersessionAndRosterReplacement(t *testing.T) {
	t.Run("newer target", func(t *testing.T) {
		entered := make(chan struct{})
		r, _ := participantRuntime(t, func(req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/opponent") {
				close(entered)
				<-req.Context().Done()
			}
		})
		r.state.History.Matches[0].Players = append(r.state.History.Matches[0].Players, domain.Player{PUUID: "next"})
		done := make(chan error, 1)
		go func() {
			_, err := r.GetParticipantProfile(context.Background(), "same-view", "local", 42, "opponent", nil)
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first read did not start")
		}
		result, err := r.GetParticipantProfile(context.Background(), "same-view", "local", 42, "next", nil)
		if err != nil || result.PUUID != "next" {
			t.Fatalf("newer player failed: %v", err)
		}
		select {
		case err = <-done:
			if err == nil {
				t.Fatal("superseded request published")
			}
		case <-time.After(time.Second):
			t.Fatal("superseded transport remained active")
		}
		if len(r.participantProfiles) != 1 || r.participantProfiles["next"] == nil {
			t.Fatal("superseded player retained")
		}
	})
	t.Run("source replacement", func(t *testing.T) {
		entered, proceed := make(chan struct{}), make(chan struct{})
		r, _ := participantRuntime(t, func(req *http.Request) {
			if strings.HasPrefix(req.URL.Path, "/lol-summoner/v2/summoners/puuid/") {
				close(entered)
				select {
				case <-proceed:
				case <-req.Context().Done():
				}
			}
		})
		done := make(chan error, 1)
		go func() {
			_, err := r.GetParticipantProfile(context.Background(), "same-view", "local", 42, "opponent", nil)
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("profile read did not start")
		}
		r.mu.Lock()
		r.state.History.Matches[0].Players = nil
		r.mu.Unlock()
		close(proceed)
		select {
		case err := <-done:
			if err == nil || len(r.participantProfiles) != 0 {
				t.Fatal("replaced source published its former participant")
			}
		case <-time.After(time.Second):
			t.Fatal("profile read did not complete")
		}
	})
}
