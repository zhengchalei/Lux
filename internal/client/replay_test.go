package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"lux/internal/domain"
	"lux/internal/history"
)

// replay_test.go verifies replay commands against an isolated authenticated client.
// Metadata registration, availability, progress, and launch requests remain observable
// HTTP interactions; no real replay file or game process is used by these tests.

// replayFixture records replay protocol state and received client-changing submissions.
type replayFixture struct {
	runtime    *Runtime       // Account-bound runtime under test.
	state      string         // Reported replay metadata state.
	config     map[string]any // Current replay feature and activity configuration.
	phase      string         // Current client gameflow phase.
	missing    bool           // Metadata endpoint initially returns missing.
	fail       bool           // Command returns an ambiguous server error.
	change     bool           // Account generation changes during metadata retrieval.
	mismatch   bool           // Metadata reports a different game identifier.
	transition bool           // Gameflow enters selection during metadata retrieval.
	creates    int            // Received metadata registration count.
	downloads  int            // Received explicit download count.
	watches    int            // Received explicit watch count.
	body       map[string]any // Last metadata registration body.
}

// newReplayFixture starts a private TLS client with one visible queried game.
// Cleanup closes transport and account lifetime. Only mutating requests use the
// temporarily replaced client transport; metadata GETs use history's loopback client.
func newReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	f := &replayFixture{state: "download", phase: "None", config: map[string]any{"isReplaysEnabled": true, "isReplaysForMatchHistoryEnabled": true, "isLoggedIn": true}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var value any
		switch req.URL.Path {
		case "/lol-summoner/v1/current-summoner":
			value = map[string]any{"puuid": "local-account"}
		case "/lol-gameflow/v1/gameflow-phase":
			value = f.phase
		case "/lol-replays/v1/configuration":
			value = f.config
		case "/lol-replays/v1/metadata/42":
			if f.missing {
				w.WriteHeader(404)
				return
			}
			id := 42
			if f.mismatch {
				id = 43
			}
			value = map[string]any{"gameId": id, "state": f.state, "downloadProgress": 47}
			if f.change {
				f.runtime.mu.Lock()
				f.runtime.epoch++
				f.runtime.mu.Unlock()
			}
			if f.transition {
				f.phase = "ChampSelect"
			}
		case "/lol-replays/v2/metadata/42/create":
			if req.Method != "POST" {
				t.Error("registration method changed")
			}
			_ = json.NewDecoder(req.Body).Decode(&f.body)
			f.creates++
			f.missing = false
			w.WriteHeader(204)
			return
		case "/lol-replays/v1/rofls/42/download", "/lol-replays/v1/rofls/42/watch":
			if req.Method != "POST" {
				t.Error("replay command method changed")
			}
			var body map[string]string
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["componentType"] != "replay-button_match-history" {
				t.Error("missing client replay context")
			}
			if req.URL.Path == "/lol-replays/v1/rofls/42/watch" {
				f.watches++
			} else {
				f.downloads++
			}
			if f.fail {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	address, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(address.Port())
	original := localTransport
	localTransport = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	f.runtime = New(domain.Database{}, history.New(domain.Database{}))
	f.runtime.session = ctx
	f.runtime.access = &history.Access{Port: port, Token: "fixture", Account: "local-account", Platform: "TEST1"}
	f.runtime.state.Connected = true
	f.runtime.state.Query = &domain.Profile{PUUID: "queried-player", Matches: []domain.Match{{ID: 42, GameVersion: "16.22.1", GameType: "MATCHED_GAME", QueueID: 450, GameEnd: 1700001234000, PlatformID: "TEST1"}}}
	t.Cleanup(func() { cancel(); server.Close(); localTransport = original })
	return f
}

// TestReplayRegistrationPreservesRecordedMetadata reproduces a missing replay metadata record.
// It checks original match metadata registration without downloading and avoids recreating an existing record on later reads.
func TestReplayRegistrationPreservesRecordedMetadata(t *testing.T) {
	f := newReplayFixture(t)
	f.missing = true
	for i := 0; i < 2; i++ {
		status, err := f.runtime.GetReplay(context.Background(), "queried-player", 42)
		if err != nil || !status.CanDownload {
			t.Fatalf("status %#v %v", status, err)
		}
	}
	if f.creates != 1 || text(f.body["gameVersion"]) != "16.22.1" || number(f.body["queueId"]) != 450 || number(f.body["gameEnd"]) != 1700001234000 || f.downloads != 0 || f.watches != 0 {
		t.Fatalf("registration lost source metadata: %#v", f.body)
	}
}

// TestReplayCommandBoundaries exercises downloads, launches, stale accounts, and unavailable files.
// It verifies that only fresh matching permissions admit exactly one command and failures never trigger a transport retry.
func TestReplayCommandBoundaries(t *testing.T) {
	for _, name := range []string{"download", "watch", "incompatible", "expired", "unsupported", "playing", "replay-playing", "selecting", "phase-transition", "patching", "disabled", "other-player", "other-game", "other-server", "changed-account", "wrong-metadata", "overlap", "failed"} {
		t.Run(name, func(t *testing.T) {
			f := newReplayFixture(t)
			action := "watch"
			f.state = "watch"
			puuid := "queried-player"
			id := uint64(42)
			switch name {
			case "download":
				action = "download"
				f.state = "download"
			case "incompatible":
				f.state = "incompatible"
			case "expired":
				f.state = "missingOrExpired"
			case "unsupported":
				f.state = "unsupported"
			case "playing":
				f.config["isPlayingGame"] = true
			case "replay-playing":
				f.config["isPlayingReplay"] = true
			case "selecting":
				f.phase = "ChampSelect"
			case "phase-transition":
				f.transition = true
			case "patching":
				f.config["isPatching"] = true
			case "disabled":
				f.config["isReplaysEnabled"] = false
			case "other-player":
				puuid = "unqueried"
			case "other-game":
				id = 999
			case "other-server":
				f.runtime.state.Query.Matches[0].PlatformID = "OTHER1"
			case "changed-account":
				f.change = true
			case "wrong-metadata":
				f.mismatch = true
			case "overlap":
				f.runtime.replayMu.Lock()
				defer f.runtime.replayMu.Unlock()
			case "failed":
				f.fail = true
			}
			err := f.runtime.ReplayAction(context.Background(), puuid, id, action)
			if (err == nil) != (name == "download" || name == "watch") {
				t.Fatalf("unexpected result: %v", err)
			}
			want := 0
			if name == "download" || name == "watch" || name == "failed" {
				want = 1
			}
			if f.downloads+f.watches != want {
				t.Fatalf("commands=%d want=%d", f.downloads+f.watches, want)
			}
		})
	}
}

// TestReplayProgressIgnoresNonPercentageCounters reproduces the client's large checking counters.
// Only an observed downloading percentage becomes progress; ready and checking states retain no misleading percentage value.
func TestReplayProgressIgnoresNonPercentageCounters(t *testing.T) {
	config := map[string]any{"isReplaysEnabled": true, "isReplaysForMatchHistoryEnabled": true, "isLoggedIn": true}
	for _, state := range []string{"checking", "downloading", "watch"} {
		for _, progress := range []int{47, 4000000} {
			status := replayObservation(42, map[string]any{"state": state, "downloadProgress": progress}, config, "None")
			if (status.Progress != nil) != (state == "downloading" && progress == 47) {
				t.Fatalf("invalid percentage: %#v", status)
			}
		}
	}
}

// TestMissingReplayVersionRemainsUnavailable reproduces a queried record without original version metadata.
// The client receives no registration, download, or launch rather than treating the current version as the recorded version.
func TestMissingReplayVersionRemainsUnavailable(t *testing.T) {
	f := newReplayFixture(t)
	f.missing = true
	f.runtime.state.Query.Matches[0].GameVersion = ""
	status, err := f.runtime.GetReplay(context.Background(), "queried-player", 42)
	if err != nil || status.State != "unavailable" || status.CanDownload || status.CanWatch || f.creates != 0 || f.downloads != 0 || f.watches != 0 {
		t.Fatalf("missing metadata authorized replay: %#v %v", status, err)
	}
}
