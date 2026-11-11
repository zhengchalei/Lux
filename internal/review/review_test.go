package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"lux/internal/domain"
)

// review_test.go verifies observed timeline attribution and compatible model boundaries.
// Synthetic client and model fixtures exercise identity, missing measurements,
// event intervals, citation rejection, cancellation, and encrypted settings without
// sending any real account data or credentials to an external model service.

// reviewFixture creates explicit teams and irregularly timed source samples for comparison.
func reviewFixture() (map[string]any, map[string]any) {
	game := map[string]any{"gameId": 42, "mapId": 11, "gameVersion": "test-version"}
	participants := []any{}
	identities := []any{}
	roles := []string{"TOP", "JUNGLE", "MIDDLE", "BOTTOM", "UTILITY"}
	for id := 1; id <= 10; id++ {
		team := 100
		if id > 5 {
			team = 200
		}
		participants = append(participants, map[string]any{"participantId": id, "championId": id, "teamId": team, "teamPosition": roles[(id-1)%5]})
		identities = append(identities, map[string]any{"participantId": id, "player": map[string]any{"puuid": "identity-" + strconv.Itoa(id), "gameName": "private-player-name", "tagLine": "123"}})
	}
	game["participants"], game["participantIdentities"] = participants, identities
	frames := []any{}
	for index, at := range []int{0, 60123, 180456, 240000, 360900} {
		players := map[string]any{}
		for id := 1; id <= 10; id++ {
			gold := 500 + index*400
			if id == 3 && index == 2 {
				gold = 2500
			}
			players[strconv.Itoa(id)] = map[string]any{"participantId": id, "totalGold": gold, "minionsKilled": index * 10, "jungleMinionsKilled": 0, "xp": index * 100, "level": 1 + index, "position": map[string]any{"x": 1000 + index*100, "y": 2000 + index*100}}
		}
		events := []any{}
		if index == 2 {
			events = append(events, map[string]any{"type": "CHAMPION_KILL", "timestamp": 180456, "killerId": 3, "victimId": 8, "assistingParticipantIds": []any{1}}, map[string]any{"type": "WARD_PLACED", "timestamp": 180000})
		}
		if index == 3 {
			events = append(events, map[string]any{"type": "CHAMPION_KILL", "timestamp": 180457, "killerId": 8, "victimId": 3, "assistingParticipantIds": []any{7}}, map[string]any{"type": "ELITE_MONSTER_KILL", "timestamp": 200000, "killerId": 7, "monsterType": "DRAGON"})
		}
		frames = append(frames, map[string]any{"timestamp": at, "participantFrames": players, "events": events})
	}
	return game, map[string]any{"frames": frames}
}

// fixtureView normalizes the synthetic perspective and prepares its explicitly paired phases.
func fixtureView(t *testing.T) domain.TimelineReview {
	t.Helper()
	game, raw := reviewFixture()
	timeline, err := Normalize(game, raw, "identity-3", 42, 420)
	if err != nil {
		t.Fatal(err)
	}
	view, err := Compare(timeline, 0)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

// TestTimelineIdentityAndBoundaryAttribution uses irregular sample times and adjacent combat events.
// Each event belongs to exactly one phase, and explicit role matching identifies the correct enemy slot.
func TestTimelineIdentityAndBoundaryAttribution(t *testing.T) {
	view := fixtureView(t)
	if view.Timeline.OwnID != 3 || view.Opponent != 8 || len(view.Windows) != 2 || len(view.Timeline.Events) != 3 {
		t.Fatalf("identity or event filtering failed: %#v", view)
	}
	first, second := view.Windows[0], view.Windows[1]
	if first.End != 180456 || first.OwnKDA != [3]int{1, 0, 0} || second.OwnKDA != [3]int{0, 1, 0} || second.OtherKDA != [3]int{1, 0, 0} || first.Tone != "gain" || second.Tone != "loss" {
		t.Fatalf("incorrect phase accounting: %#v %#v", first, second)
	}
	if _, err := Compare(view.Timeline, 2); err == nil {
		t.Fatal("friendly opponent accepted")
	}
	if _, err := Compare(view.Timeline, 99); err == nil {
		t.Fatal("unknown opponent accepted")
	}
}

// TestTimelineUnknownSamplesAndAmbiguousRoles preserves missing metrics and manual pairing requirements.
// Unobserved minion components and duplicate opposing roles cannot become guessed performance data.
func TestTimelineUnknownSamplesAndAmbiguousRoles(t *testing.T) {
	game, raw := reviewFixture()
	parts := domain.Array(game["participants"])
	domain.Object(parts[5])["teamPosition"] = "MIDDLE"
	frames := domain.Array(raw["frames"])
	stats := domain.Object(domain.Object(domain.Object(frames[2])["participantFrames"])["3"])
	delete(stats, "jungleMinionsKilled")
	delete(stats, "totalGold")
	timeline, err := Normalize(game, raw, "identity-3", 42, 420)
	if err != nil {
		t.Fatal(err)
	}
	if timeline.SuggestedOpponent != 0 {
		t.Fatal("ambiguous role auto-paired")
	}
	view, err := Compare(timeline, 8)
	if err != nil {
		t.Fatal(err)
	}
	if view.Windows[0].CSDiff != nil || view.Windows[0].GoldTrend != nil || view.Windows[1].GoldTrend != nil {
		t.Fatal("missing boundary became an observed zero")
	}
	game["mapId"] = 12
	domain.Object(parts[5])["teamPosition"] = "TOP"
	timeline, err = Normalize(game, raw, "identity-3", 42, 450)
	if err != nil || timeline.SuggestedOpponent != 0 {
		t.Fatal("ARAM received an automatic lane opponent")
	}
}

// TestTimelineRejectsMisassociatedSources supplies invalid identity, game, frame, and roster metadata.
// Every malformed source fails before producing a phase or model-ready evidence collection.
func TestTimelineRejectsMisassociatedSources(t *testing.T) {
	for _, kind := range []string{"identity", "game", "duplicate-frame", "missing-team"} {
		t.Run(kind, func(t *testing.T) {
			game, raw := reviewFixture()
			own := "identity-3"
			switch kind {
			case "identity":
				own = "other"
			case "game":
				game["gameId"] = 43
			case "duplicate-frame":
				domain.Object(domain.Array(raw["frames"])[1])["timestamp"] = 0
			case "missing-team":
				delete(domain.Object(domain.Array(game["participants"])[0]), "teamId")
			}
			if _, err := Normalize(game, raw, own, 42, 420); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

// validModelReply constructs a short coaching fixture with actual phase and event citations.
func validModelReply() domain.AIReview {
	return domain.AIReview{Summary: "fixture coaching response", Insights: []domain.AIInsight{{WindowID: "W1", Verdict: "good", Title: "observed trade", Reason: "recorded kill and gold change", Opponent: "recorded opposing death", Advice: "review the trade if lane conditions permit", EvidenceIDs: []string{"W1", "E1"}}}, Goals: []string{"compare the next recorded trade"}}
}

// TestModelProtocolAndIdentityRedaction runs the compatible HTTP adapter against a local provider fixture.
// Requests contain selected samples and citations without names, persistent identities, or unrelated player samples.
func TestModelProtocolAndIdentityRedaction(t *testing.T) {
	view := fixtureView(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-provider-key" {
			t.Error("incorrect compatible request")
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "fixture-model" || len(body.Messages) != 2 {
			t.Error("invalid model request")
		}
		facts := body.Messages[1].Content
		if strings.Contains(facts, "private-player-name") || strings.Contains(facts, "identity-3") || strings.Contains(facts, "puuid") {
			t.Error("private display identity reached provider")
		}
		var input map[string]any
		_ = json.Unmarshal([]byte(facts), &input)
		if len(domain.Array(input["players"])) != 2 {
			t.Error("unselected player samples included")
		}
		content, _ := json.Marshal(validModelReply())
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer server.Close()
	s := NewAI(t.TempDir())
	s.config = providerConfig{BaseURL: server.URL + "/v1", Model: "fixture-model"}
	s.key = "synthetic-provider-key"
	result, err := s.Analyze(context.Background(), view)
	if err != nil || result.Model != "fixture-model" || result.Created == "" {
		t.Fatalf("compatible response failed: %#v %v", result, err)
	}
}

// TestModelRejectsUnrelatedCitations ensures existing event identifiers cannot support another phase.
// Missing citations, fabricated phases, and unsupported verdicts fail instead of becoming successful coaching.
func TestModelRejectsUnrelatedCitations(t *testing.T) {
	view := fixtureView(t)
	for _, kind := range []string{"wrong-phase", "wrong-event", "no-evidence", "wrong-verdict"} {
		result := validModelReply()
		switch kind {
		case "wrong-phase":
			result.Insights[0].WindowID = "W999"
		case "wrong-event":
			result.Insights[0].EvidenceIDs = []string{"E2"}
		case "no-evidence":
			result.Insights[0].EvidenceIDs = nil
		case "wrong-verdict":
			result.Insights[0].Verdict = "made-up"
		}
		if validateAnalysis(result, view) == nil {
			t.Fatal("invalid citation accepted:", kind)
		}
	}
}

// TestModelCancellationAndSanitizedFailure interrupts a pending local provider and rejects credential failures.
// Request cancellation finishes promptly, while provider error bodies cannot reveal secrets through UI errors.
func TestModelCancellationAndSanitizedFailure(t *testing.T) {
	for _, kind := range []string{"cancel", "failure"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "failure" {
					w.WriteHeader(401)
					_, _ = w.Write([]byte("synthetic-provider-key"))
					return
				}
				close(started)
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
			}))
			defer server.Close()
			s := NewAI(t.TempDir())
			s.config = providerConfig{BaseURL: server.URL, Model: "fixture"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.Analyze(ctx, fixtureView(t)); done <- err }()
			if kind == "cancel" {
				<-started
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || strings.Contains(err.Error(), "synthetic-provider-key") {
					t.Fatal("failure missing or exposed provider body")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("provider cancellation did not finish")
			}
		})
	}
}

// TestProviderSettingsPreserveAndClearSecret verifies encrypted persistence and public-key redaction.
// Blank replacement input preserves a credential, explicit removal clears it, and invalid URLs retain the previous provider.
func TestProviderSettingsPreserveAndClearSecret(t *testing.T) {
	dir := t.TempDir()
	s := NewAI(dir)
	input := domain.AISettingsInput{BaseURL: "https://example.test/v1/chat/completions", Model: "model", APIKey: "synthetic-secret-value"}
	settings, err := s.Save(input)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.HasKey || settings.BaseURL != "https://example.test/v1" {
		t.Fatal("provider settings not normalized")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ai-provider.json"))
	if err != nil || strings.Contains(string(raw), input.APIKey) {
		t.Fatal("plaintext credential persisted")
	}
	restored := NewAI(dir)
	if !restored.Settings().HasKey || restored.key != input.APIKey {
		t.Fatal("credential did not restore")
	}
	input.APIKey = ""
	if _, err = restored.Save(input); err != nil || !restored.Settings().HasKey {
		t.Fatal("blank replacement erased secret")
	}
	input.ClearKey = true
	if _, err = restored.Save(input); err != nil || restored.Settings().HasKey {
		t.Fatal("explicit removal retained secret")
	}
	for _, address := range []string{"http://remote.test/v1", "https://user:password@example.test/v1", "https://example.test/v1?key=secret"} {
		input.BaseURL = address
		if _, err = restored.Save(input); err == nil {
			t.Fatal("invalid provider URL accepted")
		}
	}
}
