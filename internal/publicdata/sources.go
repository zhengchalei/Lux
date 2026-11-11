package publicdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lux/assets"
	"lux/internal/domain"
)

// sources.go loads and refreshes public champion observations independently from
// authenticated client services. Ordinary ARAM and Mayhem retain their own
// versions, sample counts, adjustment lists, and successful fetch timestamps.
// Static RESG JavaScript exports are parsed as JSON rather than evaluated, and
// malformed responses cannot replace a previously validated dataset.
//
// Service serializes public cache mutations, including automatic refreshes and
// champion detail requests. A changed Mayhem patch clears its previous details
// before publication, because identical champion identifiers do not establish
// identical adjustments across patches. Callbacks run after releasing the cache
// mutex and receive a newly constructed database, so client state never shares
// this service's mutable source documents or locks.

// Provider roots identify public statistics endpoints and never receive client authentication headers.
const (
	opgg = "https://lol-api-champion.op.gg"
	resg = "https://www.bilibilitoy.com/toy/resg/19226257645568-v42593/"
)

// document retains source fields until their source-specific validators accept publication.
type document map[string]any

// sourceCache preserves compatibility with the existing public-only Rust cache format.
type sourceCache struct {
	ResgPatch string              `json:"resg_patch"`         // Published Mayhem version label.
	Resg      []document          `json:"resg"`               // Validated public Mayhem catalog.
	Collected string              `json:"collected_at"`       // Publisher collection timestamp label.
	AramPatch string              `json:"aram_patch"`         // Published ordinary ARAM version.
	Aram      document            `json:"aram"`               // Validated ordinary ARAM observations.
	Balance   document            `json:"balance"`            // Versionless ordinary balance observations.
	Details   map[string]document `json:"details"`            // Mayhem adjustments for ResgPatch only.
	Fetched   string              `json:"fetched_at"`         // Last successful public completion.
	ResgAt    string              `json:"resg_fetched_at"`    // Mayhem source successful completion.
	AramAt    string              `json:"aram_fetched_at"`    // Ordinary strength successful completion.
	BalanceAt string              `json:"balance_fetched_at"` // Ordinary balance successful completion.
}

// Service owns public transport, serialized source updates, and its public-only cache.
type Service struct {
	mu      sync.Mutex            // Serializes source and cache mutations.
	http    *http.Client          // Certificate-validating public HTTP transport.
	cache   sourceCache           // Last independently validated public observations.
	path    string                // Go public cache destination path.
	changed func(domain.Database) // Called outside mu after database publication.
}

// New restores validated public observations and prepares an independent public service.
// The Go cache takes precedence; the previous Rust cache is a read-only migration
// fallback. Missing or malformed caches leave the bundled public snapshots available.
func New(changed func(domain.Database)) *Service {
	directory := filepath.Join(os.Getenv("LOCALAPPDATA"), "Lux")
	s := &Service{http: &http.Client{Timeout: 12 * time.Second}, path: filepath.Join(directory, "public-statistics-go.json"), changed: changed}
	// Read old observations without overwriting the Rust cache while both migration executables may exist on the same machine.
	for _, path := range []string{s.path, filepath.Join(directory, "public-statistics.json")} {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &s.cache) == nil {
			break
		}
	}
	return s
}

// Database constructs an isolated public catalog from bundled and validated cached observations.
func (s *Service) Database() domain.Database {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.database()
}

// Run refreshes on startup and preserves independent source data during failures.
// Complete refreshes wait six hours; partial failures retry after five minutes.
// Cancellation stops the timer and all in-flight HTTP requests without another update.
func (s *Service) Run(ctx context.Context) {
	for {
		complete := s.refresh(ctx)
		delay := 5 * time.Minute
		if complete {
			delay = 6 * time.Hour
		}
		// A bounded timer keeps unavailable public providers from spinning while client state continues independently in its own runtime.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// request decodes a bounded public response without attaching local account credentials.
// Module responses must contain only a JSON export. HTTP errors and malformed
// payloads return before callers replace any successful source observations.
func (s *Service) request(ctx context.Context, address string, module bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Lux/0.2 Windows MyGo")
	resp, err := s.http.Do(req)
	if err != nil {
		return errors.New("公共数据请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("公共数据 HTTP %d", resp.StatusCode)
	}
	// Reject oversized provider payloads instead of retaining an unbounded response in the public source worker's memory.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return errors.New("公共数据大小或内容无效")
	}
	if module {
		text := strings.TrimSpace(string(data))
		if !strings.HasPrefix(text, "export default ") {
			return errors.New("公共模块格式无效")
		}
		data = []byte(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(text, "export default ")), ";")))
	}
	return json.Unmarshal(data, out)
}

// refresh replaces only independently validated datasets and publishes after unlocking.
// Each source retains its own timestamp on failure. A Mayhem patch change drops
// prior detail observations before merging the new catalog, then a public-only
// snapshot is persisted and sent to the client runtime. Cache write failure is
// visible in status without discarding usable in-memory provider observations.
func (s *Service) refresh(ctx context.Context) bool {
	s.mu.Lock()
	success := 0
	status := []string{}
	var versions []document
	if s.request(ctx, resg+"api/v1/versions.js", true, &versions) == nil && len(versions) > 0 {
		patch := text(versions[0]["version"])
		var payload document
		if patch != "" && s.request(ctx, resg+"api/v1/versions/"+url.PathEscape(patch)+"/champions.js", true, &payload) == nil {
			items := rows(payload["items"])
			if validRows(items, "resg") {
				// Details are patch-specific; keeping old values after a catalog update would attribute the previous patch's adjustments to the new source.
				if s.cache.ResgPatch != patch {
					s.cache.Details = nil
				}
				s.cache.ResgPatch, s.cache.Resg, s.cache.ResgAt = patch, items, stamp()
				s.cache.Collected = text(versions[0]["collectedAt"])
				success++
			}
		}
	}
	if success == 0 {
		status = append(status, "RESG 更新失败，保留原快照")
	}
	var versionData document
	if s.request(ctx, opgg+"/api/kr/champions/aram/versions", false, &versionData) == nil {
		patch := firstVersion(versionData)
		var payload document
		if patch != "" && s.request(ctx, opgg+"/api/kr/champions/aram?tier=all&version="+url.QueryEscape(patch), false, &payload) == nil && validRows(rows(payload["data"]), "aram") {
			s.cache.Aram, s.cache.AramPatch, s.cache.AramAt = payload, patch, stamp()
			success++
		} else {
			status = append(status, "普通强度更新失败，保留原快照")
		}
	} else {
		status = append(status, "普通强度更新失败，保留原快照")
	}
	var balance document
	if s.request(ctx, opgg+"/api/contents/aram-balance", false, &balance) == nil && validRows(rows(balance["data"]), "balance") {
		s.cache.Balance, s.cache.BalanceAt = balance, stamp()
		success++
	} else {
		status = append(status, "普通调整更新失败，保留原快照")
	}
	if success > 0 {
		s.cache.Fetched = stamp()
	}
	db := s.database()
	if success == 3 {
		db.SourceStatus = "强度数据已自动更新"
	} else {
		db.SourceStatus = strings.Join(status, " · ")
	}
	if success > 0 {
		if err := s.save(); err != nil {
			db.SourceStatus += " · 本地缓存保存失败"
		}
	}
	s.mu.Unlock()
	if s.changed != nil && ctx.Err() == nil {
		s.changed(db)
	}
	return success == 3
}

// Inspect fetches balance details for the currently published Mayhem patch only.
// Unknown or absent detail data remains nil. The callback receives the revised
// catalog after the cache lock is released and never changes player state itself.
func (s *Service) Inspect(ctx context.Context, id int) error {
	if id <= 0 {
		return errors.New("请选择英雄")
	}
	s.mu.Lock()
	patch := s.cache.ResgPatch
	if patch == "" {
		patch = "16.22"
	}
	var payload document
	err := s.request(ctx, fmt.Sprintf("%sapi/v1/versions/%s/champions/%d.js", resg, url.PathEscape(patch), id), true, &payload)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if s.cache.ResgPatch == "" {
		s.cache.ResgPatch = patch
	}
	if s.cache.Details == nil {
		s.cache.Details = map[string]document{}
	}
	s.cache.Details[strconv.Itoa(id)] = object(payload["bb"])
	s.cache.Fetched = stamp()
	db := s.database()
	err = s.save()
	s.mu.Unlock()
	if s.changed != nil {
		s.changed(db)
	}
	return err
}

// Matchups returns candidate-owned match victory rates for one public enemy and lane.
// OP.GG reports the enemy's victories, so conversion subtracts those wins from
// observed matches. These are final match outcomes with the stated enemy, region,
// rank, position, and patch, rather than measured laning-phase advantages.
func (s *Service) Matchups(ctx context.Context, enemy int, position string) ([]domain.Matchup, error) {
	if enemy <= 0 || !strings.Contains("|top|jungle|mid|adc|support|", "|"+position+"|") {
		return nil, errors.New("请选择公开英雄与有效分路")
	}
	var versions, payload document
	if err := s.request(ctx, opgg+"/api/kr/champions/ranked/versions", false, &versions); err != nil {
		return nil, err
	}
	patch := firstVersion(versions)
	if patch == "" {
		return nil, errors.New("对位版本暂不可用")
	}
	if err := s.request(ctx, fmt.Sprintf("%s/api/kr/champions/ranked/%d/%s?tier=platinum_plus&version=%s", opgg, enemy, position, url.QueryEscape(patch)), false, &payload); err != nil {
		return nil, err
	}
	result := []domain.Matchup{}
	for _, row := range rows(object(payload["data"])["counters"]) {
		n, w := number(row["play"]), number(row["win"])
		id := int(number(row["champion_id"]))
		if n <= 0 || w < 0 || w > n || id <= 0 {
			continue
		}
		result = append(result, domain.Matchup{ChampionID: id, WinRate: (n - w) / n * 100, Samples: int(n), Source: fmt.Sprintf("OP.GG · 韩服 · 铂金以上 · %s · %s", position, patch)})
	}
	// Shrink small samples toward fifty percent before ranking, while preserving their original observed rates and sample counts for display.
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		return (a.WinRate-50)*float64(a.Samples)/float64(a.Samples+100) > (b.WinRate-50)*float64(b.Samples)/float64(b.Samples+100)
	})
	return result, nil
}

// save atomically replaces the Go public cache after serializing accepted observations.
func (s *Service) save() error {
	data, err := json.Marshal(s.cache)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), "public-go-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	// A failed write or rename leaves the previous cache intact, and this temporary file is removed after closing its handle.
	defer os.Remove(name)
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, s.path)
}

// database merges bundled snapshots and independently validated cached provider rows.
// Callers hold mu. Every returned slice and adjustment list is freshly created,
// keeping consumers isolated from future cache replacement or champion detail reads.
func (s *Service) database() domain.Database {
	var bundled []document
	data, _ := assets.Files.ReadFile("champions.json")
	_ = json.Unmarshal(data, &bundled)
	db := domain.Database{Patch: "16.22", Updated: "内置公开快照 · 2026-11-07", SourceStatus: "后台自动更新强度数据"}
	byID := map[int]domain.Champion{}
	applyResg(byID, bundled, "16.22", "内置快照 2026-11-07")
	var aram, balance, details document
	for name, target := range map[string]*document{"aram.json": &aram, "aram-balance.json": &balance, "mayhem-balance.json": &details} {
		raw, _ := assets.Files.ReadFile(name)
		_ = json.Unmarshal(raw, target)
	}
	applyAram(byID, rows(aram["data"]), "16.22", "内置快照")
	applyBalance(byID, rows(balance["data"]), "内置快照 · 版本未标注，以客户端为准")
	for key, row := range object(details["champions"]) {
		id, _ := strconv.Atoi(key)
		c := byID[id]
		c.Mayhem.Adjustments = resgAdjustments(object(object(row)["mayhem"])["balance"])
		byID[id] = c
	}
	if validRows(s.cache.Resg, "resg") {
		applyResg(byID, s.cache.Resg, s.cache.ResgPatch, s.cache.ResgAt)
		db.Patch = s.cache.ResgPatch
	}
	if validRows(rows(s.cache.Aram["data"]), "aram") {
		applyAram(byID, rows(s.cache.Aram["data"]), s.cache.AramPatch, s.cache.AramAt)
	}
	if validRows(rows(s.cache.Balance["data"]), "balance") {
		applyBalance(byID, rows(s.cache.Balance["data"]), s.cache.BalanceAt+" · 版本未标注，以客户端为准")
	}
	for key, row := range s.cache.Details {
		id, _ := strconv.Atoi(key)
		c, ok := byID[id]
		if ok {
			c.Mayhem.Adjustments = resgAdjustments(row)
			byID[id] = c
		}
	}
	if s.cache.Fetched != "" {
		db.Updated = s.cache.Fetched
	}
	for _, c := range byID {
		db.Champions = append(db.Champions, c)
	}
	sort.Slice(db.Champions, func(i, j int) bool { return db.Champions[i].ID < db.Champions[j].ID })
	return db
}

// applyResg replaces Mayhem strength while retaining adjustments only within their source patch.
func applyResg(catalog map[int]domain.Champion, items []document, patch, at string) {
	for _, row := range items {
		id := int(number(row["id"]))
		c := catalog[id]
		if !strings.Contains(c.Mayhem.Source, "· "+patch+" ·") {
			c.Mayhem.Adjustments = nil
		}
		c.ID, c.Name, c.Title, c.Alias = id, text(row["title"]), text(row["name"]), text(row["alias"])
		c.Roles = []string{}
		for _, role := range list(row["roles"]) {
			c.Roles = append(c.Roles, text(role))
		}
		rate := number(row["wr"]) * 100
		c.Mayhem.Tier, c.Mayhem.WinRate, c.Mayhem.Samples = text(row["tier"]), &rate, int(number(row["tm"]))
		c.Mayhem.Source = fmt.Sprintf("RESG · %s · 全样本 · %s", patch, at)
		catalog[id] = c
	}
}

// applyAram updates ordinary strength without modifying independent Mayhem observations.
func applyAram(catalog map[int]domain.Champion, items []document, patch, at string) {
	for _, row := range items {
		id := int(number(row["id"]))
		c, ok := catalog[id]
		if !ok {
			continue
		}
		stats := object(row["average_stats"])
		rate := number(stats["win_rate"]) * 100
		c.Aram.Tier = fmt.Sprintf("T%.0f", number(stats["tier"]))
		c.Aram.WinRate = &rate
		c.Aram.Samples = int(number(stats["play"]))
		c.Aram.Source = fmt.Sprintf("OP.GG · 韩服 · 全段位 · %s · %s", patch, at)
		catalog[id] = c
	}
}

// applyBalance converts ordinary multipliers with incoming damage's reversed benefit direction.
func applyBalance(catalog map[int]domain.Champion, items []document, at string) {
	for _, row := range items {
		id := int(number(row["champion_id"]))
		c, ok := catalog[id]
		if !ok {
			continue
		}
		c.Aram.Adjustments = []domain.Adjustment{}
		for _, f := range []struct {
			key, label string
			base       float64
			inverse    bool
		}{{"damage_dealt", "造成伤害", 100, false}, {"damage_taken", "承受伤害", 100, true}, {"healing", "治疗效果", 100, false}, {"shield_amount", "护盾效果", 100, false}, {"attack_speed", "攻击速度", 100, false}, {"cooldown_reduction", "技能急速", 0, false}, {"tenacity", "韧性", 0, false}, {"energy_regen", "能量回复", 100, false}} {
			delta := number(row[f.key]) - f.base
			if delta == 0 {
				continue
			}
			unit := ""
			if f.base == 100 {
				unit = "%"
			}
			helpful := delta > 0
			if f.inverse {
				helpful = delta < 0
			}
			c.Aram.Adjustments = append(c.Aram.Adjustments, domain.Adjustment{Label: f.label, Display: fmt.Sprintf("%+.0f%s", delta, unit), Helpful: helpful})
		}
		c.Aram.Source += " · 调整 " + at
		catalog[id] = c
	}
}

// resgAdjustments preserves absent balance data and decodes explicit or absolute percentages.
func resgAdjustments(raw any) []domain.Adjustment {
	values := object(raw)
	if values == nil {
		return nil
	}
	result := []domain.Adjustment{}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := text(values[key])
		n, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
		if err != nil {
			continue
		}
		base := 0.
		if strings.HasSuffix(value, "%") && !strings.HasPrefix(value, "+") && !strings.HasPrefix(value, "-") {
			base = 100
		}
		delta := n - base
		helpful := delta > 0
		if strings.Contains(key, "承受") || strings.Contains(key, "受到") || strings.Contains(key, "所受") {
			helpful = delta < 0
		}
		unit := ""
		if strings.HasSuffix(value, "%") {
			unit = "%"
		}
		result = append(result, domain.Adjustment{Label: key, Display: fmt.Sprintf("%+.1f%s", delta, unit), Helpful: helpful})
	}
	return result
}

// validRows validates every required identity and observation before accepting a dataset.
func validRows(items []document, kind string) bool {
	if len(items) == 0 {
		return false
	}
	seen := map[int]bool{}
	for _, row := range items {
		key := "id"
		if kind == "balance" {
			key = "champion_id"
		}
		id := int(number(row[key]))
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
		switch kind {
		case "resg":
			if text(row["name"]) == "" || text(row["title"]) == "" || text(row["alias"]) == "" || !validNumber(row["wr"], 0, 1) || !validNumber(row["tm"], 0, 1e12) || !validNumber(row["wm"], 0, number(row["tm"])) {
				return false
			}
		case "aram":
			stats := object(row["average_stats"])
			if !validNumber(stats["win_rate"], 0, 1) || !validNumber(stats["play"], 0, 1e12) {
				return false
			}
		case "balance":
			for _, field := range []string{"damage_dealt", "damage_taken", "healing", "shield_amount", "attack_speed", "cooldown_reduction", "tenacity", "energy_regen"} {
				if _, ok := row[field].(float64); !ok {
					return false
				}
			}
		}
	}
	return true
}

// validNumber checks source numeric presence independently from a legitimate zero observation.
func validNumber(v any, min, max float64) bool {
	n, ok := v.(float64)
	return ok && n >= min && n <= max
}

// firstVersion extracts the provider's most recent published version when available.
func firstVersion(v document) string {
	items := list(v["data"])
	if len(items) == 0 {
		return ""
	}
	return text(items[0])
}

// rows converts source arrays into objects without inventing missing values.
func rows(v any) []document {
	out := []document{}
	for _, item := range list(v) {
		out = append(out, object(item))
	}
	return out
}

// object reads either named or decoded source objects consistently.
func object(v any) document {
	if d, ok := v.(document); ok {
		return d
	}
	if d, ok := v.(map[string]any); ok {
		return document(d)
	}
	return nil
}

// list reads source arrays without interpreting scalar values as rows.
func list(v any) []any { items, _ := v.([]any); return items }

// number returns a numeric display value after source validation has established presence.
func number(v any) float64 { n, _ := v.(float64); return n }

// text reads source strings without rendering null as observed data.
func text(v any) string { s, _ := v.(string); return s }

// stamp identifies a completed source observation in explicitly labeled UTC.
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// DecodeModule parses static exported JSON for public-data fixture validation.
func DecodeModule(data []byte, out any) error {
	data = bytes.TrimSpace(data)
	if !bytes.HasPrefix(data, []byte("export default ")) {
		return errors.New("not a JSON export")
	}
	return json.Unmarshal(bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimSpace(bytes.TrimPrefix(data, []byte("export default "))), []byte(";"))), out)
}
