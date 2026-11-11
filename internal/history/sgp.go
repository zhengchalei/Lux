package history

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"lux/internal/domain"
)

// sgp.go selects authenticated history origins from the public Akari server directory.
// Tencent aliases require an explicit platform identifier and never select another
// server merely because that server is reachable. Public directory validation rejects
// non-HTTPS origins, credentials, queries, and fragments before any bearer token
// can be attached. Loopback authentication and public configuration remain separate.
//
// One short-lived access entry belongs to the exact local credentials, region,
// platform, and account. Shared transport failures temporarily cool down that same
// identity only. Player-specific missing or private histories do not disable SGP
// for the other nine profiles in a roster.

// Embedded configuration is a public routing baseline, never an authentication or personal-data record.
//
//go:embed sgp-servers.json
var embeddedDirectory []byte

// server retains only publisher-verified history routing information from the public directory.
type server struct {
	MatchHistory string `json:"matchHistory"`    // Validated HTTPS history service origin.
	RegionPath   string `json:"regionPathParam"` // Optional published match identifier prefix.
}

// directory groups published server routes under their explicit configuration schema version.
type directory struct {
	Version int               `json:"version"` // Published directory schema version.
	Servers map[string]server `json:"servers"` // Explicit configured game server entries.
}

// sgpAccess retains one resolved same-region origin and ephemeral entitlement credential.
type sgpAccess struct {
	Origin string // Validated same-region HTTPS service origin.
	Path   string // Published match identifier region prefix.
	Bearer string // Ephemeral current entitlement authentication secret.
}

// tokenEntry binds cached entitlement access to one exact local connection identity.
type tokenEntry struct {
	Identity Access    // Exact owning client/account connection identity.
	At       time.Time // Successful authentication observation monotonic timestamp.
	Value    sgpAccess // Short-lived resolved authenticated service access.
}

// parseDirectory validates all candidate origins before replacing the active routing baseline.
// Unsupported or malformed documents return an error so callers retain the earlier
// validated configuration. Empty routes represent deliberately unsupported server entries.
func parseDirectory(bytes []byte) (directory, error) {
	var d directory
	if err := json.Unmarshal(bytes, &d); err != nil {
		return d, errors.New("SGP 配置格式无效")
	}
	if d.Version > 2 || len(d.Servers) == 0 {
		return d, errors.New("SGP 配置版本不可用")
	}
	for _, s := range d.Servers {
		if s.MatchHistory == "" {
			continue
		}
		u, e := url.Parse(s.MatchHistory)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return d, errors.New("SGP 配置地址无效")
		}
	}
	return d, nil
}

// selectServer resolves only the current region's configured origin and match prefix.
// Tencent requires the exact platform, while known non-Tencent aliases use configured
// equivalents. Missing routes report unavailability instead of manufacturing a destination.
func (d directory) selectServer(a Access) (server, string, error) {
	region, platform := strings.ToUpper(a.Region), strings.ToUpper(a.Platform)
	if parts := strings.Split(region, "/"); len(parts) > 1 {
		region = strings.TrimSpace(parts[0])
		if platform == "" {
			platform = strings.TrimSpace(parts[1])
		}
	}
	id := region
	if region == "TENCENT" {
		if platform == "" {
			return server{}, "", errors.New("SGP 大区平台信息未就绪")
		}
		id = "TENCENT_" + platform
	} else {
		aliases := map[string]string{"NA": "NA1", "BR": "BR1", "TR": "TR1", "LAN": "LA1", "LAS": "LA2", "OCE": "OC1", "EUW1": "EUW", "JP1": "JP"}
		if alias := aliases[id]; alias != "" {
			id = alias
		}
	}
	legacy := map[string]string{"NA1": "NA", "BR1": "BR", "TR1": "TR", "LA1": "LAN", "LA2": "LAS", "OC1": "OCE", "EUW": "EUW1", "JP": "JP1"}
	for _, key := range []string{id, region, legacy[id]} {
		if s, ok := d.Servers[key]; ok && s.MatchHistory != "" {
			prefix := s.RegionPath
			if prefix == "" {
				prefix = strings.TrimPrefix(id, "TENCENT_")
			}
			return s, prefix, nil
		}
	}
	return server{}, "", errors.New("当前大区未配置 SGP 战绩接口")
}

// access resolves both client authentication streams and reuses a short-lived exact-identity token.
// Serial token resolution prevents ten simultaneous player requests from fetching the
// same entitlement repeatedly. Account or credential changes invalidate reuse even if
// the underlying League client process continues running. An unavailable token remains
// a failed SGP path, allowing Load to record an explicit LCU fallback instead of
// fabricating a successful empty history observation.
func (r *Repository) access(ctx context.Context, a Access) (sgpAccess, error) {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	// Reuse credentials only for an unchanged identity; the short interval allows entitlement rotation without querying tokens for every player.
	if r.token != nil && r.token.Identity == a && time.Since(r.token.At) < time.Minute {
		return r.token.Value, nil
	}
	if r.failureIdentity == a && time.Since(r.failureAt) < 30*time.Second {
		return sgpAccess{}, errors.New("SGP 暂不可用，继续使用客户端数据")
	}
	r.mu.Lock()
	s, prefix, err := r.directory.selectServer(a)
	r.mu.Unlock()
	if err != nil {
		return sgpAccess{}, err
	}
	var entitlements map[string]any
	var leagueToken string
	if err = Get(ctx, a, "/entitlements/v1/token", &entitlements); err != nil {
		return sgpAccess{}, err
	}
	if err = Get(ctx, a, "/lol-league-session/v1/league-session-token", &leagueToken); err != nil {
		return sgpAccess{}, err
	}
	bearer := domain.Text(entitlements["accessToken"])
	if bearer == "" || leagueToken == "" {
		return sgpAccess{}, errors.New("SGP 认证信息未就绪")
	}
	v := sgpAccess{Origin: s.MatchHistory, Path: prefix, Bearer: bearer}
	r.token = &tokenEntry{Identity: a, At: time.Now(), Value: v}
	return v, nil
}

// markFailure cools down only connection-wide failures for this exact authenticated identity.
// Missing, private, or malformed player payloads remain isolated to that player.
// Cancellation does not poison service availability for subsequent independent requests.
// HTTP 401, throttling, server errors, and connection failures clear the shared
// token entry; HTTP 403 and 404 retain it because their scope may be one player.
// The cooldown key includes account and local credentials, allowing a changed
// session to resolve authentication immediately instead of inheriting an older failure.
func (r *Repository) markFailure(a Access, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	var status HTTPError
	if errors.As(err, &status) && status.Status != 401 && status.Status != 429 && status.Status < 500 {
		return
	}
	if errors.As(err, &status) || err.Error() == "数据接口连接失败或超时" {
		// Clear and cool down the same access identity atomically so concurrent player requests cannot reuse a known failed token entry.
		r.tokenMu.Lock()
		r.failureIdentity = a
		r.failureAt = time.Now()
		r.token = nil
		r.tokenMu.Unlock()
	}
}
