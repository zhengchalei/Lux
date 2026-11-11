package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"lux/internal/domain"
	"lux/internal/history"
)

// draft_test.go exercises manual selection against a local TLS protocol fixture.
// Tests observe submitted HTTP requests, ensuring stale heroes, unavailable skins,
// and failed swaps cannot become successful or repeated client-changing actions.

// draftFixture holds mutable protocol observations for sequential action tests.
type draftFixture struct {
	runtime    *Runtime       // Account-bound runtime under test.
	session    map[string]any // Current fixture selection response.
	phase      string         // Current fixture gameflow response.
	posts      int            // Number of received swap submissions.
	patches    int            // Number of received skin submissions.
	skin       int            // Last received selected skin identifier.
	fail       bool           // Return a server error after swap admission.
	change     bool           // Replace champion after the carousel is read.
	subset     []int          // Personal preselection endpoint response.
	picks      []int          // Current pickable champion identifiers.
	disabled   []int          // Currently disabled champion identifiers.
	firstPicks int            // Received initial personal pick submissions.
	pickBody   map[string]any // Received initial personal pick action body.
	replace    bool           // Replace session identity during availability reads.
}

// newDraftFixture starts an isolated local protocol server and restores shared transport.
// Tests run sequentially because the production HTTP client is replaced temporarily.
// Cleanup closes the TLS server and cancels this fixture's account context.
func newDraftFixture(t *testing.T) *draftFixture {
	t.Helper()
	f := &draftFixture{phase: "ChampSelect", session: map[string]any{"id": "draft-one", "gameId": 8, "queueId": 450, "localPlayerCellId": 1, "benchEnabled": true, "allowSkinSelection": true, "myTeam": []any{map[string]any{"cellId": 1, "championId": 99, "selectedSkinId": 99000}}, "benchChampions": []any{map[string]any{"championId": 103}}}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch r.URL.Path {
		case "/lol-summoner/v1/current-summoner":
			value = map[string]any{"puuid": "fixture-local"}
		case "/lol-gameflow/v1/gameflow-phase":
			value = f.phase
		case "/lol-champ-select/v1/session":
			value = f.session
		case "/lol-lobby-team-builder/champ-select/v1/subset-champion-list":
			value = f.subset
		case "/lol-champ-select/v1/pickable-champion-ids":
			value = f.picks
			if f.replace {
				f.session["id"] = "draft-replacement"
			}
		case "/lol-champ-select/v1/disabled-champion-ids":
			value = f.disabled
		case "/lol-champ-select/v1/session/actions/7":
			if r.Method != "PATCH" {
				t.Error("initial pick method changed")
			}
			_ = json.NewDecoder(r.Body).Decode(&f.pickBody)
			f.firstPicks++
			if f.fail {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		case "/lol-champ-select/v1/skin-selector-info":
			value = map[string]any{"selectedChampionId": 99, "selectedSkinId": 99000, "showSkinSelector": true, "skinSelectionDisabled": false}
		case "/lol-champ-select/v1/skin-carousel-skins":
			value = []any{map[string]any{"id": 99000, "championId": 99, "name": "Original", "isBase": true, "unlocked": true, "tilePath": "/lol-game-data/assets/ASSETS/base.jpg"}, map[string]any{"id": 99001, "championId": 99, "name": "Granted", "unlocked": true}, map[string]any{"id": 99002, "championId": 99, "name": "Locked", "unlocked": false}, map[string]any{"id": 99003, "championId": 99, "name": "Disabled", "unlocked": true, "disabled": true}}
			if f.change {
				object(array(f.session["myTeam"])[0])["championId"] = 103
			}
		case "/lol-champ-select/v1/pickable-skin-ids":
			value = []int{99001, 99002, 99003}
		case "/lol-champ-select/v1/session/bench/swap/103":
			if r.Method != "POST" {
				t.Error("swap method changed")
			}
			f.posts++
			if f.fail {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		case "/lol-champ-select/v1/session/my-selection":
			if r.Method != "PATCH" {
				t.Error("skin method changed")
			}
			var body map[string]int
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.skin = body["selectedSkinId"]
			f.patches++
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
	f.runtime.access = &history.Access{Port: port, Token: "test", Account: "fixture-local"}
	f.runtime.state.Account = &domain.Account{PUUID: "fixture-local"}
	room := selectionRoom(f.session, 450, nil, nil, false, nil)
	f.runtime.state.Room = &room
	t.Cleanup(func() { cancel(); server.Close(); localTransport = original })
	return f
}

// TestPersonalChoicesRemainDistinctFromBench reproduces three personal cards and a shared bench.
// It protects their separate identities, authoritative first-pick state, and stage-specific availability.
func TestPersonalChoicesRemainDistinctFromBench(t *testing.T) {
	f := newDraftFixture(t)
	f.session["allowSubsetChampionPicks"] = true
	f.session["timer"] = map[string]any{"phase": "BAN_PICK"}
	f.session["actions"] = []any{[]any{map[string]any{"id": 7, "actorCellId": 1, "type": "pick", "completed": false, "isInProgress": true}}}
	member := object(array(f.session["myTeam"])[0])
	member["championId"], member["championPickIntent"] = 0, 99
	room := selectionRoom(f.session, 450, []int{103, 22, 81}, []int{81}, true, []int{103, 22, 81, 103, 0})
	if !reflect.DeepEqual(room.Choices, []int{103, 22, 81}) || !reflect.DeepEqual(room.Bench, []int{103}) || !reflect.DeepEqual(room.Available, []int{103, 22}) || room.Current != 0 || !room.CanChoose || room.CanSwap {
		t.Fatalf("personal normalization: %#v", room)
	}
	unknown := selectionRoom(f.session, 450, nil, nil, false, []int{103, 22})
	if len(unknown.Choices) != 2 || len(unknown.Available) != 0 {
		t.Fatal("unknown availability removed cards or granted selection")
	}
	f.session["timer"] = map[string]any{"phase": "FINALIZATION"}
	member["championId"] = 99
	final := selectionRoom(f.session, 450, nil, nil, false, []int{22, 81})
	if len(final.Choices) != 0 || final.CanChoose || !final.CanSwap || !contains(final.Available, 103) {
		t.Fatalf("finalization retained personal choices or blocked bench: %#v", final)
	}
}

// TestPersonalChoiceCommandsUseCorrectProtocol reproduces first and subsequent personal hero selections.
// It verifies action PATCH versus bench POST and rejects stale, unavailable, or completed selections before writing.
func TestPersonalChoiceCommandsUseCorrectProtocol(t *testing.T) {
	for _, name := range []string{"first", "assigned", "bench-only", "unpickable", "disabled", "no-subset", "completed", "future", "other-player", "spectating", "stale-session", "failed"} {
		t.Run(name, func(t *testing.T) {
			f := newDraftFixture(t)
			f.session["allowSubsetChampionPicks"] = true
			f.session["timer"] = map[string]any{"phase": "BAN_PICK"}
			action := map[string]any{"id": 7, "actorCellId": 1, "type": "pick", "completed": false, "isInProgress": true}
			f.session["actions"] = []any{[]any{action}}
			f.subset, f.picks = []int{103, 22, 81}, []int{103, 22, 81}
			member := object(array(f.session["myTeam"])[0])
			member["championId"] = 0
			current := 0
			switch name {
			case "assigned":
				current = 99
				member["championId"] = current
			case "bench-only", "no-subset":
				f.subset = []int{22, 81}
				if name == "no-subset" {
					f.subset = nil
				}
			case "unpickable":
				f.picks = []int{22, 81}
			case "disabled":
				f.disabled = []int{103}
			case "completed":
				action["completed"] = true
			case "future":
				action["isInProgress"] = false
			case "other-player":
				action["actorCellId"] = 2
			case "spectating":
				f.session["isSpectating"] = true
			case "stale-session":
				f.replace = true
			case "failed":
				f.fail = true
			}
			err := f.runtime.SelectDraftChampion(context.Background(), draftSessionKey(f.session), current, 103)
			if (err == nil) != (name == "first" || name == "assigned") {
				t.Fatalf("unexpected command result: %v", err)
			}
			wantPick, wantSwap := 0, 0
			if name == "first" || name == "failed" {
				wantPick = 1
			}
			if name == "assigned" {
				wantSwap = 1
			}
			if f.firstPicks != wantPick || f.posts != wantSwap {
				t.Fatalf("pick=%d swap=%d, want pick=%d swap=%d", f.firstPicks, f.posts, wantPick, wantSwap)
			}
			if wantPick > 0 && (number(f.pickBody["championId"]) != 103 || !truth(f.pickBody["completed"]) || text(f.pickBody["type"]) != "pick") {
				t.Fatalf("invalid pick action body: %#v", f.pickBody)
			}
		})
	}
}

// TestPersonalChoiceEventTriggersRefresh reproduces the independent personal-card endpoint notification.
// It ensures that card updates wake synchronization even when the shared bench and session have not changed.
func TestPersonalChoiceEventTriggersRefresh(t *testing.T) {
	if !relevantEvent([]byte(`[8,"OnJsonApiEvent",{"uri":"/lol-lobby-team-builder/champ-select/v1/subset-champion-list","eventType":"Update","data":[103,22]}]`)) {
		t.Fatal("personal card notification ignored")
	}
}

// TestManualBenchSwapBoundaries simulates legal, stale, and failed swap submissions.
// It protects exact current-hero ownership and ensures a failed write is submitted once.
func TestManualBenchSwapBoundaries(t *testing.T) {
	for _, name := range []string{"legal", "taken", "stale", "phase", "failed", "overlap"} {
		t.Run(name, func(t *testing.T) {
			f := newDraftFixture(t)
			key := draftSessionKey(f.session)
			switch name {
			case "taken":
				f.session["benchChampions"] = []any{}
			case "stale":
				object(array(f.session["myTeam"])[0])["championId"] = 1
			case "phase":
				f.phase = "InProgress"
			case "failed":
				f.fail = true
			case "overlap":
				f.runtime.draftMu.Lock()
				defer f.runtime.draftMu.Unlock()
			}
			err := f.runtime.SelectDraftChampion(context.Background(), key, 99, 103)
			if (err == nil) != (name == "legal") {
				t.Fatalf("unexpected result %v", err)
			}
			want := 0
			if name == "legal" || name == "failed" {
				want = 1
			}
			if f.posts != want {
				t.Fatalf("submissions %d want %d", f.posts, want)
			}
		})
	}
}

// TestManualSkinAvailabilityAndHeroChange simulates usable grants and unavailable appearances.
// It verifies skin ownership filtering and rejects a champion changed during availability retrieval.
func TestManualSkinAvailabilityAndHeroChange(t *testing.T) {
	for _, id := range []int{99001, 99002, 99003} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			f := newDraftFixture(t)
			key := draftSessionKey(f.session)
			list, err := f.runtime.GetDraftSkins(context.Background(), key, 99)
			if err != nil || len(list.Skins) != 2 {
				t.Fatalf("availability: %#v %v", list, err)
			}
			err = f.runtime.SetDraftSkin(context.Background(), key, 99, id)
			if id == 99001 {
				if err != nil || f.patches != 1 || f.skin != id {
					t.Fatalf("usable grant: %v", err)
				}
			} else if err == nil || f.patches != 0 {
				t.Fatal("unavailable appearance submitted")
			}
		})
	}
	f := newDraftFixture(t)
	f.change = true
	if err := f.runtime.SetDraftSkin(context.Background(), draftSessionKey(f.session), 99, 99001); err == nil || f.patches != 0 {
		t.Fatal("hero change during read admitted stale skin")
	}
}

// TestDraftSessionAndAssetIdentity simulates missing identifiers and unsafe thumbnail paths.
// It prevents unknown sessions from authorizing actions and confines artwork to local assets.
func TestDraftSessionAndAssetIdentity(t *testing.T) {
	if draftSessionKey(map[string]any{}) != "" {
		t.Fatal("unknown session acquired identity")
	}
	for _, path := range []string{"https://other/image.jpg", "/lol-game-data/assets/../secret.jpg", "/lol-game-data/assets/x.jpg?token=x", "//other/x.png"} {
		if safeSkinPath(path) {
			t.Fatalf("unsafe asset path %s", path)
		}
	}
	if !safeSkinPath("/lol-game-data/assets/ASSETS/Characters/Lux/Skins/Base/Lux.png") {
		t.Fatal("valid asset rejected")
	}
}

// TestDraftActivityDismissesDuringGames simulates selection and gameplay phase transitions.
// Explicit manual opening must not allow a companion to remain over an active game.
func TestDraftActivityDismissesDuringGames(t *testing.T) {
	r := New(domain.Database{}, history.New(domain.Database{}))
	r.state.Connected = true
	r.state.Room = &domain.Room{Mode: domain.Aram}
	for _, phase := range []string{"None", "ChampSelect", "GameStart", "InProgress", "Reconnect", "WaitingForStats"} {
		r.state.Phase = phase
		active, playing := r.DraftActivity()
		if (active != "") != (phase == "ChampSelect") || playing != (phase != "None" && phase != "ChampSelect") {
			t.Fatalf("phase %s active %s playing %v", phase, active, playing)
		}
	}
}

// TestDraftMatchingCycles retains one opening through matching, readiness, and personal selection.
// New matching attempts or another selection session reopen the companion, while
// ordinary updates preserve dismissal and ranked queues never become eligible.
func TestDraftMatchingCycles(t *testing.T) {
	r := New(domain.Database{}, history.New(domain.Database{}))
	defer r.Close()
	r.state.Connected = true
	observe := func(phase string, queue int, key string) string {
		var room *domain.Room
		if phase == "ChampSelect" {
			room = &domain.Room{QueueID: queue, Mode: domain.ModeFromQueue(queue, false), SessionKey: key}
		}
		r.mu.Lock()
		r.updateDraftCycleLocked(phase, queue, room)
		r.state.Phase = phase
		r.state.Room = room
		r.mu.Unlock()
		activity, _ := r.DraftActivity()
		return activity
	}
	first := observe("Matchmaking", 450, "")
	if first == "" {
		t.Fatal("ARAM matching did not open")
	}
	for _, phase := range []string{"Matchmaking", "ReadyCheck", "ChampSelect", "ChampSelect"} {
		if got := observe(phase, 450, "one"); got != first {
			t.Fatalf("%s replaced the same matching cycle", phase)
		}
	}
	if next := observe("ChampSelect", 450, "two"); next == "" || next == first {
		t.Fatal("another selection reused its prior opening")
	}
	if got := observe("Lobby", 0, ""); got != "" {
		t.Fatal("idle lobby opened")
	}
	second := observe("Matchmaking", 2400, "")
	if second == "" || second == first {
		t.Fatal("Mayhem matching did not get another opening")
	}
	if got := observe("ReadyCheck", 2400, ""); got != second {
		t.Fatal("Mayhem readiness replaced its cycle")
	}
	if got := observe("ReadyCheck", 0, ""); got != "" {
		t.Fatal("unknown queue opened automatically")
	}
	if got := observe("ReadyCheck", 2400, ""); got != second {
		t.Fatal("recovered queue overrode current-cycle dismissal")
	}
	for _, phase := range []string{"Matchmaking", "ReadyCheck", "ChampSelect"} {
		if got := observe(phase, 420, "ranked"); got != "" {
			t.Fatal("ranked queue opened ARAM companion")
		}
	}
	r.state.Connected = false
	if got := observe("Matchmaking", 450, ""); got != "" {
		t.Fatal("disconnected runtime opened companion")
	}
}
