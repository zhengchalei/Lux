package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lux/internal/domain"
)

// repository_test.go verifies history collection against bounded in-process HTTPS fixtures.
// Tests reproduce exact-queue filtering, provider fallback, shared detail perspective,
// and cancellation without accessing real player data or a running League client.

// fixtureSGP builds one complete synthetic provider match with distinct player identities.
func fixtureSGP(id, queue int) map[string]any {
	parts := []any{}
	for i := 0; i < 10; i++ {
		team := 100
		if i >= 5 {
			team = 200
		}
		parts = append(parts, map[string]any{"puuid": fmt.Sprintf("player-%d", i), "teamId": team, "championId": 99, "win": i < 5, "kills": i, "deaths": 1, "assists": 2, "totalDamageDealtToChampions": 100 + i, "goldEarned": 100, "totalDamageTaken": 100, "totalHeal": 0, "damageDealtToTurrets": 0})
	}
	return map[string]any{"json": map[string]any{"gameId": id, "queueId": queue, "gameDuration": 1200, "gameType": "MATCHED_GAME", "participants": parts}}
}

// fixtureRepository supplies one trusted local test origin and ready entitlement endpoints.
// Callers own the returned server through test cleanup. Remote client replacement is
// restored before another sequential package test can observe the fixture transport.
func fixtureRepository(t *testing.T, handler http.HandlerFunc) (*Repository, Access) {
	t.Helper()
	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entitlements/v1/token":
			json.NewEncoder(w).Encode(map[string]any{"accessToken": "fixture-secret"})
		case "/lol-league-session/v1/league-session-token":
			json.NewEncoder(w).Encode("fixture-league")
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(testServer.Close)
	previous := remoteHTTP
	remoteHTTP = testServer.Client()
	t.Cleanup(func() { remoteHTTP = previous })
	parsed, _ := url.Parse(testServer.URL)
	port, _ := strconv.Atoi(parsed.Port())
	repo := New(domain.Database{})
	repo.directory = directory{Version: 2, Servers: map[string]server{"NA1": {MatchHistory: testServer.URL, RegionPath: "NA1"}}}
	repo.refreshAt = time.Now()
	return repo, Access{Port: port, Token: "fixture-local", Region: "NA", Account: "player-0"}
}

// TestExactQueueDedupAndPartial retains matching records while preserving a later page failure.
// Returned scan counts include off-queue records and duplicates cannot inflate either sample.
func TestExactQueueDedupAndPartial(t *testing.T) {
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/match-history-query/") {
			http.Error(w, "unexpected", 404)
			return
		}
		if r.URL.Query().Get("tag") != "q_420" {
			t.Error("missing exact queue tag")
		}
		switch r.URL.Query().Get("startIndex") {
		case "0":
			json.NewEncoder(w).Encode(map[string]any{"games": []any{fixtureSGP(1, 450), fixtureSGP(2, 420)}})
		case "2":
			json.NewEncoder(w).Encode(map[string]any{"games": []any{fixtureSGP(2, 420), fixtureSGP(3, 420)}})
		default:
			http.Error(w, "later unavailable", 503)
		}
	})
	p, err := repo.Load(context.Background(), access, "player-0", 20, 420)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Matches) != 2 || p.Coverage.Scanned != 3 || p.Coverage.Valid != 2 || p.Coverage.Source != "SGP" || p.Coverage.Note == "" {
		t.Fatalf("incorrect partial queue coverage %+v", p.Coverage)
	}
}

// TestSuccessfulEmptyDoesNotFallback distinguishes observed zero history from failed requests.
// A valid empty SGP response must complete without touching the client's history endpoint.
func TestSuccessfulEmptyDoesNotFallback(t *testing.T) {
	var local atomic.Int32
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/match-history-query/") {
			json.NewEncoder(w).Encode(map[string]any{"games": []any{}})
			return
		}
		local.Add(1)
		http.Error(w, "should not read", 500)
	})
	p, err := repo.Load(context.Background(), access, "player-0", 50, 0)
	if err != nil || p.Coverage.Source != "SGP" || p.Coverage.Fetched != 0 || local.Load() != 0 {
		t.Fatalf("empty success converted to fallback: %+v %v", p.Coverage, err)
	}
}

// TestPrivatePlayerDoesNotPoisonOthers reproduces one private player's provider failure.
// The next player's history must still use SGP with the same authentication context.
func TestPrivatePlayerDoesNotPoisonOthers(t *testing.T) {
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/player/private-player/") {
			http.Error(w, "private", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/lol-match-history/") {
			json.NewEncoder(w).Encode(map[string]any{"games": map[string]any{"games": []any{}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"games": []any{fixtureSGP(1, 420)}})
	})
	first, err := repo.Load(context.Background(), access, "private-player", 1, 0)
	if err != nil || first.Coverage.Source != "LCU" {
		t.Fatalf("private fallback failed %+v %v", first.Coverage, err)
	}
	second, err := repo.Load(context.Background(), access, "player-0", 1, 0)
	if err != nil || second.Coverage.Source != "SGP" {
		t.Fatalf("private player contaminated provider %+v %v", second.Coverage, err)
	}
}

// TestSharedDetailPreservesPlayerPerspective loads one match through concurrent player requests.
// Exactly one provider fetch supplies each player's own local participant marker and combat values.
func TestSharedDetailPreservesPlayerPerspective(t *testing.T) {
	var reads atomic.Int32
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		time.Sleep(10 * time.Millisecond)
		json.NewEncoder(w).Encode(fixtureSGP(9, 420))
	})
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			m, err := repo.Detail(context.Background(), access, fmt.Sprintf("player-%d", index), 9)
			if err != nil || m.Kills != index || !m.Complete() {
				t.Errorf("perspective %d failed %+v %v", index, m, err)
			}
		}(i)
	}
	group.Wait()
	if reads.Load() != 1 {
		t.Fatalf("shared detail fetched %d times", reads.Load())
	}
	if _, _, ok := repo.cacheGet(cacheKey(Access{Region: "OTHER"}, 9)); ok {
		t.Fatal("cache crossed region identity")
	}
}

// TestCancellationDoesNotBecomePartialSuccess cancels a collection after its first valid page.
// The caller receives cancellation rather than a successful partial profile from abandoned work.
func TestCancellationDoesNotBecomePartialSuccess(t *testing.T) {
	started := make(chan struct{})
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("startIndex") == "0" {
			json.NewEncoder(w).Encode(map[string]any{"games": []any{fixtureSGP(1, 420)}})
			return
		}
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := repo.Load(ctx, access, "player-0", 20, 420); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

// TestDirectoryAndCacheLimits rejects unsafe origins and evicts beyond retained count budgets.
// An invalid directory cannot receive bearer credentials, and equal IDs remain region-specific.
func TestDirectoryAndCacheLimits(t *testing.T) {
	if _, err := parseDirectory([]byte(`{"version":2,"servers":{"NA":{"matchHistory":"http://bad.example"}}}`)); err == nil {
		t.Fatal("plaintext origin accepted")
	}
	repo := New(domain.Database{})
	for i := 0; i < maxGames+2; i++ {
		repo.cachePut(fmt.Sprint(i), fixtureSGP(i+1, 420), "SGP")
	}
	if len(repo.cache) != maxGames || repo.bytes > maxBytes {
		t.Fatalf("cache limits failed: %d %d", len(repo.cache), repo.bytes)
	}
	if _, _, ok := repo.cacheGet("0"); ok {
		t.Fatal("coldest observation retained")
	}
	large := map[string]any{"payload": strings.Repeat("x", maxBytes/2+10)}
	repo.cachePut("large-one", large, "LCU")
	repo.cachePut("large-two", large, "LCU")
	if repo.bytes > maxBytes {
		t.Fatal("actual serialized payload exceeded byte budget")
	}
	if _, _, ok := repo.cacheGet("large-one"); ok {
		t.Fatal("byte budget failed to evict an older large payload")
	}
}

// TestRequestedDepthPaginatesWithoutInflation covers each supported analysis depth against real page boundaries.
// The repository retains exactly the requested unique records while advancing through the provider's fifty-row page boundaries.
func TestRequestedDepthPaginatesWithoutInflation(t *testing.T) {
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		rows := []any{}
		for i := start; i < start+count; i++ {
			rows = append(rows, fixtureSGP(i+1, 420))
		}
		json.NewEncoder(w).Encode(map[string]any{"games": rows})
	})
	for _, count := range []int{20, 50, 100, 200} {
		p, err := repo.Load(context.Background(), access, "player-0", count, 420)
		if err != nil || len(p.Matches) != count || p.Coverage.Requested != count || p.Coverage.Scanned != count {
			t.Fatalf("depth %d: %+v %v", count, p.Coverage, err)
		}
	}
}

// fixtureLCU converts a complete synthetic observation into the client participant-identity envelope.
func fixtureLCU(id, queue int) map[string]any {
	flat := domain.Object(fixtureSGP(id, queue)["json"])
	identities, parts := []any{}, []any{}
	for i, v := range domain.Array(flat["participants"]) {
		p := domain.Object(v)
		identities = append(identities, map[string]any{"participantId": i + 1, "player": map[string]any{"puuid": p["puuid"]}})
		parts = append(parts, map[string]any{"participantId": i + 1, "championId": p["championId"], "teamId": p["teamId"], "stats": p})
	}
	return map[string]any{"gameId": id, "queueId": queue, "gameDuration": 1200, "gameType": "MATCHED_GAME", "participantIdentities": identities, "participants": parts}
}

// TestLCURepeatedPageRetainsActualQueueCoverage exercises fallback's nested client history envelope.
// A capped endpoint that repeats its page cannot inflate matching or scanned counts into the requested target.
func TestLCURepeatedPageRetainsActualQueueCoverage(t *testing.T) {
	repo, access := fixtureRepository(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"games": map[string]any{"games": []any{fixtureLCU(1, 450), fixtureLCU(2, 420)}}})
	})
	repo.directory = directory{}
	p, err := repo.Load(context.Background(), access, "player-0", 20, 420)
	if err != nil || p.Coverage.Source != "LCU" || p.Coverage.Requested != 20 || p.Coverage.Fetched != 1 || p.Coverage.Scanned != 2 || p.Coverage.Note == "" {
		t.Fatalf("wrong fallback coverage %+v %v", p.Coverage, err)
	}
}
