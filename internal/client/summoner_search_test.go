package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// summoner_search_test.go exercises aliases against separate authenticated TLS services.
// Tests retain duplicate names with distinct identities, unavailable regional data,
// bounded enrichment, and explicit selection generations without using live accounts.

// searchServer creates a private loopback endpoint with synthetic authentication only.
func searchServer(t *testing.T, handler http.HandlerFunc) history.Access {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	parsed, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(parsed.Port())
	return history.Access{Port: port, Token: "search-fixture"}
}

// TestAliasSearchKeepsSameNames verifies encoding, credential isolation, and stable deduplication.
// Only an identical PUUID is deduplicated; distinct tags and service ordering survive.
func TestAliasSearchKeepsSameNames(t *testing.T) {
	access := searchServer(t, func(w http.ResponseWriter, req *http.Request) {
		user, token, _ := req.BasicAuth()
		if user != "riot" || token != "search-fixture" || req.URL.Path != "/player-account/aliases/v1/lookup" || req.URL.Query().Get("gameName") != "同名 & 名字" || req.URL.Query().Has("tagLine") {
			t.Error("incorrect alias request or leaked League credential")
		}
		_, _ = w.Write([]byte(`[{"alias":{"game_name":"同名 & 名字","tag_line":"100"},"puuid":"first-id"},{"alias":{"game_name":"同名 & 名字","tag_line":"200"},"puuid":"second-id"},{"alias":{"game_name":"同名 & 名字","tag_line":"100"},"puuid":"first-id"},{"alias":{"game_name":"invalid","tag_line":"300"},"puuid":"../invalid"}]`))
	})
	candidates, err := findAliases(context.Background(), riotAccess{port: access.Port, token: access.Token}, "同名 & 名字")
	if err != nil || len(candidates) != 2 || candidates[0].Name != "同名 & 名字#100" || candidates[1].PUUID != "second-id" || !candidates[1].Loading {
		t.Fatalf("lost distinct aliases: %#v %v", candidates, err)
	}
}

// TestAliasSearchEmptyAndFailure separates an empty response from sanitized service failure.
func TestAliasSearchEmptyAndFailure(t *testing.T) {
	for _, status := range []int{200, 422} {
		access := searchServer(t, func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`[]`))
		})
		items, err := findAliases(context.Background(), riotAccess{port: access.Port, token: access.Token}, "无结果")
		if status == 200 && (err != nil || len(items) != 0) || status != 200 && (err == nil || strings.Contains(err.Error(), "422")) {
			t.Fatalf("empty/error semantics changed: %v %v", items, err)
		}
	}
}

// TestCandidateEnrichmentBoundsAndUnknownRank checks regional failure and missing rank independently.
// Concurrent reads remain bounded at four, and no metadata request fetches match history.
func TestCandidateEnrichmentBoundsAndUnknownRank(t *testing.T) {
	var active, peak atomic.Int32
	access := searchServer(t, func(w http.ResponseWriter, req *http.Request) {
		now := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); now > previous && !peak.CompareAndSwap(previous, now); previous = peak.Load() {
		}
		time.Sleep(4 * time.Millisecond)
		id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		if id == "missing" {
			w.WriteHeader(404)
			return
		}
		switch {
		case strings.HasPrefix(req.URL.Path, "/lol-summoner/v2/summoners/puuid/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"puuid": id, "profileIconId": 12, "summonerLevel": 87})
		case strings.HasPrefix(req.URL.Path, "/lol-ranked/v1/ranked-stats/"):
			w.WriteHeader(503)
		default:
			t.Error("unexpected history or legacy name request")
			w.WriteHeader(404)
		}
	})
	items := make([]domain.SummonerCandidate, 9)
	for i := range items {
		items[i] = domain.SummonerCandidate{PUUID: "id-" + strconv.Itoa(i), Name: "同名#" + strconv.Itoa(i), Loading: true}
	}
	items[8].PUUID = "missing"
	results := make(chan domain.SummonerCandidate, len(items))
	enrichAliases(context.Background(), access, items, func(_ int, item domain.SummonerCandidate) { results <- item })
	close(results)
	for item := range results {
		if item.Loading || item.PUUID == "missing" && (item.Available || item.Reason == "") || item.PUUID != "missing" && (!item.Available || item.Level != 87 || item.IconID != 12 || item.Rank != "段位暂不可用") {
			t.Fatalf("metadata semantics changed: %#v", item)
		}
	}
	if peak.Load() > 4 || peak.Load() < 2 || !items[0].Loading {
		t.Fatal("worker bound or immutable source ownership failed")
	}
}

// TestCandidateConfirmationGeneration rejects stale, unavailable, and unselected identities.
// Successful admission clears the candidate list and cancels preceding search work
// atomically; later publications and account transitions cannot restore that list.
func TestCandidateConfirmationGeneration(t *testing.T) {
	r := New(domain.Database{}, history.New(domain.Database{}))
	r.session = context.Background()
	r.access = &history.Access{Port: 1, Token: "fixture"}
	r.state.Account = &domain.Account{PUUID: "self"}
	r.queryEpoch = 7
	searchCtx, cancel := context.WithCancel(r.session)
	t.Cleanup(cancel)
	r.queryCancel = cancel
	r.state.SummonerSearch = &domain.SummonerSearch{ID: 7, Name: "同名", Candidates: []domain.SummonerCandidate{{PUUID: "first", Available: true}, {PUUID: "missing"}, {PUUID: "pending", Available: true, Loading: true}}}
	for _, input := range []struct {
		id    uint64
		puuid string
	}{{6, "first"}, {7, "unknown"}, {7, "missing"}, {7, "pending"}, {0, "first"}} {
		if err := r.SelectSummoner(input.id, input.puuid, 0); err == nil {
			t.Fatal("unconfirmed identity admitted")
		}
	}
	if r.visibleIdentityLocked("first") {
		t.Fatal("search result bypassed confirmation")
	}
	if err := r.SelectSummoner(7, "first", 450); err != nil {
		t.Fatal(err)
	}
	if r.state.SummonerSearch != nil || r.lookupPUUID != "first" || r.lookupQueue != 450 || searchCtx.Err() == nil {
		t.Fatal("selection did not replace search ownership")
	}
	r.publishIf(0, 7, 0, func() { t.Fatal("old search published after confirmation") })
	if err := r.SelectSummoner(7, "first", 0); err == nil {
		t.Fatal("same card admitted twice")
	}
	r.root = context.Background()
	r.state.SummonerSearch = &domain.SummonerSearch{ID: 9}
	r.replaceAccountLocked()
	if r.state.SummonerSearch != nil || r.visibleIdentityLocked("first") {
		t.Fatal("account replacement retained candidate authorization")
	}
	r.endSession()
}

// TestSummonerIconObservationBoundary verifies permitted assets and neutral failure behavior.
// Unobserved identifiers never reach transport, and HTML cannot cross the image API.
func TestSummonerIconObservationBoundary(t *testing.T) {
	var reads atomic.Int32
	access := searchServer(t, func(w http.ResponseWriter, req *http.Request) {
		reads.Add(1)
		if req.URL.Path == "/lol-game-data/assets/v1/profile-icons/12.jpg" {
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0})
			return
		}
		_, _ = w.Write([]byte("<html>not an image</html>"))
	})
	r := New(domain.Database{}, history.New(domain.Database{}))
	r.access, r.session = &access, context.Background()
	r.state.SummonerSearch = &domain.SummonerSearch{Candidates: []domain.SummonerCandidate{{IconID: 12, Available: true}, {IconID: 13, Available: true}}}
	if _, err := r.GetSummonerIcon(context.Background(), 99); err == nil || reads.Load() != 0 {
		t.Fatal("unobserved asset reached transport")
	}
	if image, err := r.GetSummonerIcon(context.Background(), 12); err != nil || !strings.HasPrefix(image, "data:image/jpeg;base64,") {
		t.Fatal("observed profile icon unavailable")
	}
	if _, err := r.GetSummonerIcon(context.Background(), 13); err == nil {
		t.Fatal("non-image content crossed image API")
	}
}
