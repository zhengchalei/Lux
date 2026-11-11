package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// client_test.go protects identity, generation, selection, and settings boundaries.
// Fixtures contain no observed player identifiers or client credentials. The
// optional integration check is read-only and requires an explicit environment
// opt-in before discovering the user's live League client.

// selectionFixture creates a local pick action with a separate allied intention.
func selectionFixture() map[string]any {
	return map[string]any{"id": "fixture", "gameId": 1, "queueId": 420, "localPlayerCellId": 1, "myTeam": []any{map[string]any{"cellId": 1, "championPickIntent": 0, "assignedPosition": "middle"}, map[string]any{"cellId": 2, "championPickIntent": 99}}, "theirTeam": []any{map[string]any{"cellId": 6, "puuid": "visible-enemy", "championId": 103, "championPickIntent": 103}}, "actions": []any{[]any{map[string]any{"id": 5, "actorCellId": 1, "type": "pick", "championId": 0, "isInProgress": true, "completed": false}}}}
}

// TestDraftPoolsAndHiddenEnemy reproduces an enemy hover and unavailable metadata.
// It protects completed-pick visibility and prevents unknown pools from producing ranked recommendations.
func TestDraftPoolsAndHiddenEnemy(t *testing.T) {
	session := selectionFixture()
	room := selectionRoom(session, 420, []int{99, 103}, []int{}, false, nil)
	if room.Enemies[0].Champion != 0 || len(room.Available) != 0 {
		t.Fatal("unlocked opposing hover or unknown legal pool leaked")
	}
	session["actions"] = append(array(session["actions"]), []any{map[string]any{"id": 9, "actorCellId": 6, "type": "pick", "championId": 103, "completed": true}})
	room = selectionRoom(session, 420, []int{99, 103}, []int{}, true, nil)
	if room.Enemies[0].Champion != 103 || contains(room.Available, 103) {
		t.Fatal("locked opponent visibility or occupied champion filtering failed")
	}
	person := object(array(session["theirTeam"])[0])
	person["nameVisibilityType"] = "HIDDEN"
	battle := draftBattle(session, room)
	if !battle.Players[2].Hidden || battle.Players[2].PUUID != "" {
		t.Fatal("anonymized identity became queryable")
	}
}

// TestAutomationRespectsOwnershipAndManualChoices reproduces delayed local and opposing actions.
// It verifies automatic fallback and protection without submitting any network operation.
func TestAutomationRespectsOwnershipAndManualChoices(t *testing.T) {
	session := selectionFixture()
	rules := defaultAutomation()
	rules.Picks = []int{99, 103}
	plan, reason := decide(rules, session, []int{103}, nil, []int{}, 0, 0)
	if reason != "" || plan.Champion != 103 || !plan.Complete {
		t.Fatalf("fallback not chosen: %#v %s", plan, reason)
	}
	object(array(session["myTeam"])[0])["championPickIntent"] = 99
	if _, reason = decide(rules, session, []int{99, 103}, nil, []int{}, 0, 0); reason == "" {
		t.Fatal("manual intention was overwritten")
	}
	if _, reason = decide(rules, session, []int{99, 103}, nil, []int{}, 5, 99); reason != "" {
		t.Fatal("runtime-owned preselection could not complete")
	}
	session["queueId"] = 2400
	if _, reason = decide(rules, session, []int{99, 103}, nil, []int{}, 5, 99); reason == "" {
		t.Fatal("random mode produced draft action")
	}
}

// TestMissingCompletionCannotAuthorizeSelection reproduces an incomplete action response.
// Missing completion metadata must remain unknown instead of becoming an unfinished writable action.
func TestMissingCompletionCannotAuthorizeSelection(t *testing.T) {
	session := selectionFixture()
	delete(actionRows(session)[0], "completed")
	rules := defaultAutomation()
	rules.Picks = []int{99}
	rules.Preselect = true
	if _, reason := decide(rules, session, []int{99}, nil, []int{}, 0, 0); reason == "" {
		t.Fatal("unknown completion state authorized a write")
	}
}

// TestBansProtectAlliesAndUseBannablePool reproduces a friendly intended ban candidate.
// Ban selection must skip that hero and must never borrow the pickable pool.
func TestBansProtectAlliesAndUseBannablePool(t *testing.T) {
	session := selectionFixture()
	actionRows(session)[0]["type"] = "ban"
	rules := defaultAutomation()
	rules.AutoBan = true
	rules.Bans = []int{99, 103}
	plan, reason := decide(rules, session, []int{99}, []int{99, 103}, []int{}, 0, 0)
	if reason != "" || plan.Champion != 103 || plan.Kind != "ban" || !plan.Complete {
		t.Fatal("ban did not protect friendly intention")
	}
	if _, reason = decide(rules, session, []int{99, 103}, nil, []int{}, 0, 0); reason == "" {
		t.Fatal("missing bannable metadata fell back to pickable")
	}
}

// TestDelayAndAmbiguousAttempts reproduces a candidate change during its delay.
// A changed champion starts a new delay and a submitted candidate remains suppressed.
func TestDelayAndAmbiguousAttempts(t *testing.T) {
	now := time.Now()
	first := plannedAction{ID: 1, Champion: 99, Kind: "pick"}
	second := first
	second.Champion = 103
	state := execution{attempted: map[plannedAction]bool{}}
	if state.ready(first, 2, now) || !state.ready(first, 2, now.Add(2*time.Second)) || state.ready(second, 2, now.Add(2*time.Second)) {
		t.Fatal("stable delay rules failed")
	}
	state.attempted[second] = true
	if state.ready(second, 2, now.Add(time.Hour)) {
		t.Fatal("ambiguous submitted action was retried")
	}
}

// TestStoppingCancelsAdmittedTransport reproduces revocation after a write passes admission.
// The transport context must cancel promptly while permission and its authorization generation change together.
func TestStoppingCancelsAdmittedTransport(t *testing.T) {
	runtime := New(domain.Database{}, history.New(domain.Database{}))
	ctx, cancel := context.WithCancel(context.Background())
	runtime.writeCancel = cancel
	runtime.state.AutomationEnabled = true
	runtime.StopAutomation()
	if ctx.Err() == nil || runtime.state.AutomationEnabled || runtime.automationEpoch == 0 {
		t.Fatal("stopping did not revoke admitted write transport")
	}
}

// TestSnapshotOwnershipAndStalePublication reproduces external mutation and an old query reply.
// Snapshots must detach nested slices and maps while generation mismatches discard results.
func TestSnapshotOwnershipAndStalePublication(t *testing.T) {
	runtime := New(domain.Database{}, history.New(domain.Database{}))
	runtime.state.Automation.Picks = []int{99}
	runtime.state.Scouts["visible"] = domain.Scout{Profile: &domain.Profile{Name: "current"}}
	runtime.queryEpoch = 2
	runtime.SetListener(func() { _ = runtime.CurrentRevision() })
	copy := runtime.Snapshot()
	copy.Automation.Picks[0] = 103
	copy.Scouts["visible"].Profile.Name = "changed"
	if runtime.state.Automation.Picks[0] != 99 || runtime.state.Scouts["visible"].Profile.Name != "current" {
		t.Fatal("external snapshot retained shared mutation")
	}
	runtime.publishIf(0, 1, 0, func() { runtime.state.QueryError = "old" })
	if runtime.state.QueryError != "" {
		t.Fatal("stale query result entered new request")
	}
	runtime.changed(func() { runtime.state.Status = "updated" })
}

// TestAccountReplacementCancelsOwnership reproduces a login or server change during queries.
// Old jobs lose publication permission, saved rules survive, and automation is disarmed before the new account becomes visible.
func TestAccountReplacementCancelsOwnership(t *testing.T) {
	runtime := New(domain.Database{}, history.New(domain.Database{}))
	runtime.root = context.Background()
	runtime.session, runtime.endSession = context.WithCancel(runtime.root)
	previous := runtime.session
	runtime.state.History = &domain.Profile{PUUID: "old-account"}
	runtime.state.Query = &domain.Profile{PUUID: "old-query"}
	runtime.state.Scouts["old-player"] = domain.Scout{Loading: true}
	runtime.state.Automation.Picks = []int{99}
	runtime.state.AutomationEnabled = true
	runtime.mu.Lock()
	runtime.replaceAccountLocked()
	runtime.mu.Unlock()
	defer runtime.endSession()
	if previous.Err() == nil || runtime.state.History != nil || runtime.state.Query != nil || len(runtime.state.Scouts) != 0 || runtime.state.AutomationEnabled || runtime.state.Automation.Picks[0] != 99 {
		t.Fatal("old account work or permission survived replacement")
	}
	runtime.publishIf(0, 0, 0, func() { runtime.state.QueryError = "old" })
	if runtime.state.QueryError != "" {
		t.Fatal("previous account completion was accepted")
	}
}

// TestBackgroundAdmissionReservesLookupSlot fills all permitted background workers.
// A foreground lookup must still acquire the fourth slot without waiting for background completion.
func TestBackgroundAdmissionReservesLookupSlot(t *testing.T) {
	runtime := New(domain.Database{}, history.New(domain.Database{}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 3 {
		release, ok := runtime.acquire(ctx, true)
		if !ok {
			t.Fatal("background slot unavailable")
		}
		defer release()
	}
	release, ok := runtime.acquire(ctx, false)
	if !ok {
		t.Fatal("background work consumed foreground admission")
	}
	release()
}

// TestCompatibleSettingsAndInactiveStartup restores preferences emitted by the Rust client.
// Malformed preferences retain full defaults and saved rules never persist runtime enablement.
func TestCompatibleSettingsAndInactiveStartup(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(dataDirectory(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory(), "analysis.json"), []byte(`{"match_count":100,"same_mode":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory(), "automation.json"), []byte(`{"auto_pick":false,"delay_seconds":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	automation, analysis := loadSettings()
	if analysis.MatchCount != 100 || analysis.SameMode || !automation.AutoPick {
		t.Fatal("legacy settings or malformed default restoration failed")
	}
	runtime := New(domain.Database{}, history.New(domain.Database{}))
	if runtime.state.AutomationEnabled {
		t.Fatal("restored settings armed automation")
	}
	if err := runtime.SetAnalysis(200, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dataDirectory(), "analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["match_count"] != float64(200) || persisted["same_mode"] != true {
		t.Fatal("saved file lost previous-compatible spelling")
	}
}

// TestEventFilterRejectsUnrelatedPayloads reproduces ordinary client chat and selection events.
// Only endpoint families affecting the visible workspace should wake synchronization.
func TestEventFilterRejectsUnrelatedPayloads(t *testing.T) {
	if !relevantEvent([]byte(`[8,"OnJsonApiEvent",{"uri":"/lol-champ-select/v1/session","data":{"unused":true}}]`)) || relevantEvent([]byte(`[8,"OnJsonApiEvent",{"uri":"/lol-chat/v1/conversations"}]`)) || relevantEvent([]byte(`invalid`)) {
		t.Fatal("event filter admitted noise or dropped selection")
	}
}

// TestLiveClientReadonly exercises actual automatic discovery and a self-history lookup.
// It requires explicit opt-in, never creates rooms or enables automation, and logs
// counts and source context without raw player identities or credentials.
func TestLiveClientReadonly(t *testing.T) {
	if os.Getenv("LUX_LIVE_RUNTIME") != "1" {
		t.Skip("requires explicitly authorized readable live League client")
	}
	runtime := New(domain.Database{}, history.New(domain.Database{}))
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	runtime.Start(ctx)
	defer runtime.Close()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state := runtime.Snapshot()
		if state.Connected && state.Account != nil {
			break
		}
		if !wait(ctx, 200*time.Millisecond) {
			break
		}
	}
	state := runtime.Snapshot()
	if !state.Connected || state.Account == nil {
		t.Fatalf("automatic discovery did not connect: %s", state.Status)
	}
	if state.AutomationEnabled {
		t.Fatal("automatic startup enabled selection actions")
	}
	for _, count := range []int{50, 200} {
		// Change only this isolated runtime's test depth, leaving the user's persisted settings untouched by the read-only integration workload.
		runtime.mu.Lock()
		runtime.state.Analysis.MatchCount = count
		runtime.mu.Unlock()
		if err := runtime.QuerySelf(0); err != nil {
			t.Fatal(err)
		}
		for ctx.Err() == nil {
			state = runtime.Snapshot()
			if !state.QueryLoading {
				break
			}
			if !wait(ctx, 200*time.Millisecond) {
				break
			}
		}
		if state.Query == nil || state.QueryLoading || len(state.Query.Matches) == 0 {
			t.Fatalf("self history did not finish: %s", state.QueryError)
		}
		t.Logf("connected=true source=%s requested=%d fetched=%d valid=%d complete=%d automation=false", state.Query.Coverage.Source, state.Query.Requested, len(state.Query.Matches), state.Query.Coverage.Valid, state.Query.Coverage.Complete)
	}
	detail, err := runtime.Detail(ctx, state.Query.PUUID, state.Query.Matches[0].ID)
	if err != nil || !detail.Complete() {
		t.Fatal("current player match detail unavailable")
	}
	t.Logf("detail=true players=%d source=%s", len(detail.Players), detail.Source)
	runtime.mu.Lock()
	runtime.state.Analysis.MatchCount = 50
	runtime.mu.Unlock()
	if err := runtime.ReviewLastGame(); err != nil {
		t.Log("historical review deferred because live selection/game has priority")
		return
	}
	for ctx.Err() == nil {
		state = runtime.Snapshot()
		ready := state.Battle != nil && strings.HasPrefix(state.Battle.Key, "review:") && len(state.Battle.Players) == 10 && len(state.Scouts) > 0
		for _, scout := range state.Scouts {
			ready = ready && !scout.Loading
		}
		if ready {
			break
		}
		if !wait(ctx, 250*time.Millisecond) {
			break
		}
	}
	if state.Battle == nil || !strings.HasPrefix(state.Battle.Key, "review:") || len(state.Battle.Players) != 10 {
		t.Fatal("historical ten-player roster did not load")
	}
	loaded, failed := 0, 0
	for _, scout := range state.Scouts {
		if scout.Profile != nil && !scout.Loading {
			loaded++
		}
		if scout.Error != "" {
			failed++
		}
	}
	if loaded != len(state.Scouts) {
		t.Fatalf("roster profiles incomplete: loaded=%d visible=%d failed=%d", loaded, len(state.Scouts), failed)
	}
	t.Logf("review=true players=%d loaded=%d failed=%d queue=%d requested=50 automation=false", len(state.Battle.Players), loaded, failed, state.Battle.QueueID)
}
