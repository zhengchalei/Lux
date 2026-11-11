package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// summoner_search.go resolves bare names through Riot's account alias service.
// Its credentials are discovered separately from League's local endpoint and
// never enter DTOs. Every returned alias is retained, while bounded workers
// enrich current-region profiles without downloading their history before selection.
//
// The owning account and query generations guard all progressive publications.
// Confirmation validates membership while replacing that generation under the
// same mutex, so stale cards cannot query a different search or account. Profile
// icons cross IPC separately as bounded image data rather than bloating snapshots.

// riotAccess holds ephemeral account-service credentials independently from LCU access.
type riotAccess struct {
	port  int    // Local Riot account-service listening port.
	token string // Ephemeral authentication value, never serialized.
}

// findAliases preserves unique service identities in their observed response order.
// The Riot endpoint accepts a missing tag; the legacy LCU name endpoint does not.
// No service-wide completeness claim or guessed numeric tag is introduced here.
func findAliases(ctx context.Context, riot riotAccess, name string) ([]domain.SummonerCandidate, error) {
	var aliases []struct {
		Alias struct {
			Name string `json:"game_name"`
			Tag  string `json:"tag_line"`
		} `json:"alias"`
		PUUID string `json:"puuid"`
	}
	err := get(ctx, history.Access{Port: riot.port, Token: riot.token}, "/player-account/aliases/v1/lookup?gameName="+url.QueryEscape(name), &aliases)
	if err != nil {
		return nil, errors.New("同名搜索暂不可用，请稍后重试或输入名称#编号")
	}
	result := []domain.SummonerCandidate{}
	seen := map[string]bool{}
	for _, alias := range aliases {
		id := domain.Identity(alias.PUUID)
		if id == "" || seen[id] || strings.TrimSpace(alias.Alias.Name) == "" || alias.Alias.Tag == "" {
			continue
		}
		seen[id] = true
		result = append(result, domain.SummonerCandidate{PUUID: id, Name: alias.Alias.Name + "#" + alias.Alias.Tag, Loading: true, Rank: "段位待读取"})
	}
	return result, nil
}

// enrichCandidate reads current-region metadata without requesting any match history.
// A failed rank read preserves an otherwise verified summoner. Missing regional
// profiles remain visible aliases with unavailable metadata and disabled confirmation.
func enrichCandidate(ctx context.Context, access history.Access, candidate domain.SummonerCandidate) domain.SummonerCandidate {
	candidate.Loading = false
	person, err := resolve(ctx, access, "", candidate.PUUID)
	if err != nil || domain.Identity(person["puuid"]) != candidate.PUUID {
		candidate.Rank, candidate.Reason = "资料暂不可用", "当前大区无法读取该账号资料，可能尚未创建角色或服务暂不可用。可输入完整 Riot ID 重试。"
		return candidate
	}
	candidate.Available = true
	candidate.IconID, candidate.Level = number(person["profileIconId"]), number(person["summonerLevel"])
	var rank map[string]any
	_ = get(ctx, access, "/lol-ranked/v1/ranked-stats/"+candidate.PUUID, &rank)
	candidate.Rank = domain.RankLabel(rank)
	return candidate
}

// enrichAliases limits concurrent metadata reads and stops admitting canceled work.
// Each worker owns its candidate value. Publications copy only that small value
// into runtime state, leaving the initial alias slice immutable across workers.
func enrichAliases(ctx context.Context, access history.Access, candidates []domain.SummonerCandidate, publish func(int, domain.SummonerCandidate)) {
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < min(4, len(candidates)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				publish(index, enrichCandidate(ctx, access, candidates[index]))
			}
		}()
	}
enqueue:
	for index := range candidates {
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
}

// searchSummoners progressively publishes aliases under one bounded foreground query.
// Account replacement, reconnection, and later lookups invalidate its publication
// generation. Even a unique result waits for explicit confirmation before history.
func (r *Runtime) searchSummoners(ctx context.Context, access history.Access, name string, epoch, query uint64) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	riot, err := discoverRiot(access)
	var candidates []domain.SummonerCandidate
	if err == nil {
		candidates, err = findAliases(ctx, riot, name)
	}
	if err == nil {
		r.publishIf(epoch, query, 0, func() {
			r.state.SummonerSearch = &domain.SummonerSearch{ID: query, Name: name, Candidates: append([]domain.SummonerCandidate{}, candidates...)}
		})
		enrichAliases(ctx, access, candidates, func(index int, candidate domain.SummonerCandidate) {
			r.publishIf(epoch, query, 0, func() { r.state.SummonerSearch.Candidates[index] = candidate })
		})
	}
	r.publishIf(epoch, query, 0, func() {
		r.state.QueryLoading = false
		if err != nil {
			r.state.QueryError = err.Error()
		} else {
			for index, candidate := range r.state.SummonerSearch.Candidates {
				if candidate.Loading {
					candidate.Loading, candidate.Rank, candidate.Reason = false, "资料暂不可用", "资料读取超时，请重新搜索"
					r.state.SummonerSearch.Candidates[index] = candidate
				}
			}
		}
	})
}

// SelectSummoner confirms one still-current, verified candidate before history collection.
func (r *Runtime) SelectSummoner(searchID uint64, puuid string, queue int) error {
	if searchID == 0 || domain.Identity(puuid) == "" {
		return errors.New("请重新搜索并选择账号")
	}
	return r.beginLookup("", puuid, queue, searchID)
}

// GetSummonerIcon reads only a profile icon observed in the current candidate list.
// Session cancellation stops old authenticated work. Small JPEG or PNG files are
// encoded separately from snapshots; failed reads retain the renderer's neutral fallback.
func (r *Runtime) GetSummonerIcon(ctx context.Context, icon int) (string, error) {
	r.mu.Lock()
	allowed := false
	if r.state.SummonerSearch != nil {
		for _, candidate := range r.state.SummonerSearch.Candidates {
			allowed = allowed || candidate.Available && candidate.IconID == icon
		}
	}
	if !allowed || icon < 0 || r.access == nil || r.session == nil {
		r.mu.Unlock()
		return "", errors.New("头像资料已更新")
	}
	access, session := *r.access, r.session
	r.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d/lol-game-data/assets/v1/profile-icons/%d.jpg", access.Port, icon), nil)
	if err != nil {
		return "", errors.New("头像请求无法创建")
	}
	req.SetBasicAuth("riot", access.Token)
	response, err := localTransport.Do(req)
	if err != nil {
		return "", errors.New("头像暂不可用")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("头像暂不可用")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	mime := http.DetectContentType(data)
	if err != nil || len(data) > 256<<10 || mime != "image/jpeg" && mime != "image/png" {
		return "", errors.New("头像格式不可用")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
