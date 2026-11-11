//! history.rs loads scoped player history and shares complete match observations.
//! The repository chooses a configured, authenticated SGP server when available
//! and falls back to the current local client after an unavailable SGP path.
//! Every result records its actual source, scanned records, selected queue, and
//! usable coverage. A successful empty response stays empty; failed requests are
//! never converted into invented zero-game histories. Private credentials remain
//! in memory, separate from the public server configuration persisted on disk.
//!
//! Match summaries establish recent results immediately. Complete ten-player
//! observations are enriched separately and cached by region plus game identity,
//! so multiple player cards reuse one match without changing its perspective.
//! Weak per-match gates serialize duplicate fetches while allowing cancellation
//! to release ownership. A bounded HTTP semaphore and byte-limited LRU cache keep
//! large configured samples from creating unrestricted requests or retained data.
//! Cached observations retain compact serialized bytes, so the payload budget
//! measures the actual shared allocation instead of undercounting a parsed JSON
//! tree's objects and strings. Cache hits decode outside the cache mutex, and
//! SGP normalization borrows each page rather than cloning its participant trees.

#[path = "providers/sgp.rs"]
mod sgp;

use crate::{
    model::{Champion, MatchRecord, Player, num, queue_label},
    scouting::{HistoryCoverage, Profile, usable_match},
};
use anyhow::{Context, Result, bail};
use futures_util::{StreamExt, stream};
use reqwest::Client;
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet, HashMap},
    future::Future,
    sync::{Arc, Mutex, Weak},
    time::{Duration, Instant},
};
use tokio::sync::{Mutex as AsyncMutex, Semaphore};

/// Repository limits bound retained matches, compact JSON payload bytes, and concurrent network requests.
const CACHE_MATCHES: usize = 256;
const CACHE_BYTES: usize = 16 * 1024 * 1024;
const CONCURRENCY: usize = 4;
const MAX_SCAN: usize = 1000;
const PAGE_SIZE: usize = 50;
const CONFIG_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);
const TOKEN_INTERVAL: Duration = Duration::from_secs(60);
const FAILURE_INTERVAL: Duration = Duration::from_secs(30);

/// MatchKey keeps a numeric game identity inside its observed server context.
type MatchKey = (String, u64);

/// MatchGates weakly indexes in-flight ownership without retaining canceled request locks.
type MatchGates = HashMap<MatchKey, Weak<AsyncMutex<()>>>;

/// HistoryContext carries one local connection's credentials without formatting or persistence support.
#[derive(Clone)]
pub struct HistoryContext {
    pub port: u16,                // Local client HTTPS listening port.
    pub token: String,            // Ephemeral local client authentication secret.
    pub region: String,           // Observed current client region label.
    pub platform: Option<String>, // Observed current client platform code.
    pub account: String,          // Current logged-in account association identifier.
}

impl HistoryContext {
    /// new associates credential material with the observed local region and platform.
    /// The caller replaces this snapshot after connection or account changes.
    /// Repository requests use it only for the current local server context.
    pub fn new(port: u16, token: String, region: String, platform: Option<String>) -> Self {
        Self {
            port,
            token,
            region,
            platform,
            account: String::new(),
        }
    }

    /// with_account associates authentication caches with the currently logged-in local account.
    /// Callers pass their observed own PUUID rather than the player being queried.
    /// This forces fresh SGP authentication when one running client changes accounts.
    pub fn with_account(mut self, account: &str) -> Self {
        self.account = account.to_owned();
        self
    }

    /// cache_region identifies matches by the explicit region and platform context.
    fn cache_region(&self) -> String {
        format!(
            "{}:{}",
            self.region.to_ascii_uppercase(),
            self.platform.as_deref().unwrap_or("").to_ascii_uppercase()
        )
    }
}

/// HistoryResult associates retained records with the actual provider and sample scope.
#[derive(Clone, Debug)]
pub struct HistoryResult {
    pub matches: Vec<MatchRecord>, // Retained unique records in newest-first order.
    pub coverage: HistoryCoverage, // Provider and independent sample coverage counts.
}

/// CachedGame retains compact source bytes independently from the queried player's perspective.
#[derive(Clone)]
struct CachedGame {
    raw: Arc<[u8]>, // Shared compact serialized source JSON.
    source: String, // LCU or SGP normalization discriminator.
}

impl CachedGame {
    /// encode serializes borrowed source observations without retaining their allocated JSON trees.
    /// The compact byte slice has exactly the length charged to the cache budget.
    /// Clones share that allocation, and source Values may be dropped immediately.
    fn encode(raw: &Value, source: &str) -> Result<Self> {
        Ok(Self {
            raw: Arc::from(serde_json::to_vec(raw)?),
            source: source.into(),
        })
    }

    /// record materializes one perspective while keeping decoded source trees temporary.
    /// The returned MatchRecord contains only the observations used by Lux.
    /// Callers never retain the parsed raw JSON alongside the shared byte cache.
    fn record(&self, puuid: &str, catalog: &BTreeMap<u32, Champion>) -> Result<MatchRecord> {
        let raw: Value = serde_json::from_slice(&self.raw)?;
        normalize(&raw, &self.source, puuid, catalog).context("玩家不在本场比赛中")
    }
}

/// CacheEntry tracks retention cost and recency beside one shared complete match.
struct CacheEntry {
    game: CachedGame, // Shared complete source observation.
    bytes: usize,     // Exactly retained serialized payload bytes.
    stamp: u64,       // Increasing last-access ordering marker.
}

/// MatchCache bounds complete observations across all queried player perspectives.
#[derive(Default)]
struct MatchCache {
    entries: BTreeMap<MatchKey, CacheEntry>, // Region and game identity index.
    bytes: usize,                            // Sum of retained compact payload byte lengths.
    clock: u64,                              // Monotonic access marker for eviction ordering.
}

impl MatchCache {
    /// get shares a retained match and updates its relative eviction recency.
    fn get(&mut self, key: &MatchKey) -> Option<CachedGame> {
        self.clock = self.clock.wrapping_add(1);
        self.entries.get_mut(key).map(|entry| {
            entry.stamp = self.clock;
            entry.game.clone()
        })
    }

    /// insert retains one complete observation under both count and byte budgets.
    /// Replacing a match subtracts its former byte length before adding the new one.
    /// The least recently accessed entries leave first, while oversized observations
    /// remain usable by their caller without becoming permanent cache residents.
    fn insert(&mut self, key: MatchKey, game: CachedGame) {
        let bytes = game.raw.len();
        if bytes == 0 || bytes > CACHE_BYTES {
            return;
        }
        if let Some(old) = self.entries.remove(&key) {
            self.bytes -= old.bytes;
        }
        self.clock = self.clock.wrapping_add(1);
        self.bytes += bytes;
        self.entries.insert(
            key,
            CacheEntry {
                game,
                bytes,
                stamp: self.clock,
            },
        );
        // Remove cold observations until both budgets hold, so a deeper query cannot retain an unbounded session history.
        while self.entries.len() > CACHE_MATCHES || self.bytes > CACHE_BYTES {
            let oldest = self
                .entries
                .iter()
                .min_by_key(|(_, entry)| entry.stamp)
                .map(|(key, _)| key.clone());
            if let Some(key) = oldest {
                if let Some(entry) = self.entries.remove(&key) {
                    self.bytes -= entry.bytes;
                }
            } else {
                break;
            }
        }
    }
}

/// SgpAccess keeps one server's bearer credential separate from public configuration.
#[derive(Clone)]
struct SgpAccess {
    server: sgp::Server, // Configured same-region history origin.
    region_path: String, // Configured backend game identifier prefix.
    bearer: String,      // Ephemeral entitlements access token.
}

/// AccessEntry associates short-lived authentication with its exact local credential snapshot.
struct AccessEntry {
    port: u16,           // Local connection port associated with tokens.
    local_token: String, // Local credential identity associated with tokens.
    region: String,      // Exact region and platform routing key.
    account: String,     // Logged-in account associated with token streams.
    at: Instant,         // Successful authentication observation instant.
    access: SgpAccess,   // Same-region authenticated server selection.
}

/// SgpFailure associates a short unavailable interval with one exact authenticated connection.
struct SgpFailure {
    port: u16,       // Failed local connection port.
    token: String,   // Failed connection's in-memory credential identity.
    region: String,  // Failed region and platform routing key.
    account: String, // Logged-in account associated with the unavailable interval.
    at: Instant,     // Most recent provider failure observation.
}

impl SgpFailure {
    /// applies reports whether this unchanged connection remains inside its failure cooldown.
    /// Different credentials or server context permit an immediate new attempt.
    /// The cooldown limits repeated direct-server failures across concurrent player cards.
    fn applies(&self, context: &HistoryContext) -> bool {
        self.port == context.port
            && self.token == context.token
            && self.region == context.cache_region()
            && self.account == context.account
            && self.at.elapsed() < FAILURE_INTERVAL
    }
}

/// Inner owns shared clients, admission limits, public configuration, and bounded caches.
struct Inner {
    local: Client,                      // Loopback-only self-signed client HTTP transport.
    public: Client,                     // Certificate-validating public and SGP HTTP transport.
    permits: Semaphore,                 // Admission slots shared by history and detail reads.
    directory: Mutex<sgp::Directory>,   // Last validated public server directory.
    refresh_at: Mutex<Option<Instant>>, // Last attempted background directory refresh.
    access: AsyncMutex<Option<AccessEntry>>, // Serialized current connection token lookup.
    failure: Mutex<Option<SgpFailure>>, // Short unavailable interval, invalidated by changed connection identity.
    cache: Mutex<MatchCache>,           // Bounded complete-match observation cache.
    gates: Mutex<MatchGates>,           // Weak per-game in-flight gates.
}

/// HistoryRepository shares history transports and cache ownership across background tasks.
#[derive(Clone)]
pub struct HistoryRepository {
    inner: Arc<Inner>, // Shared repository resources and ephemeral authentication.
}

impl HistoryRepository {
    /// new restores validated public configuration and initializes independent HTTP clients.
    /// Authentication is supplied later through HistoryContext. Public certificate
    /// validation remains enabled even though local client HTTPS requires its own
    /// self-signed loopback policy; neither transport follows authenticated redirects.
    pub fn new() -> Result<Self> {
        let mut directory = sgp::Directory::embedded()?;
        // An unreadable or older public cache preserves the packaged directory, allowing history routing before the first successful network update.
        if let Ok(bytes) = std::fs::read(crate::sources::data_directory().join("sgp-servers.json"))
            && let Ok(cached) = sgp::Directory::parse(&bytes)
            && cached.last_update >= directory.last_update
        {
            directory = cached;
        }
        Ok(Self {
            inner: Arc::new(Inner {
                local: Client::builder()
                    .no_proxy()
                    .danger_accept_invalid_certs(true)
                    .redirect(reqwest::redirect::Policy::none())
                    .timeout(Duration::from_secs(5))
                    .build()?,
                public: Client::builder()
                    .user_agent("Lux/0.1.0")
                    .redirect(reqwest::redirect::Policy::none())
                    .timeout(Duration::from_secs(12))
                    .build()?,
                permits: Semaphore::new(CONCURRENCY),
                directory: Mutex::new(directory),
                refresh_at: Mutex::new(None),
                access: AsyncMutex::new(None),
                failure: Mutex::new(None),
                cache: Mutex::new(MatchCache::default()),
                gates: Mutex::new(HashMap::new()),
            }),
        })
    }

    /// refresh_directory starts a rate-bounded public update without delaying player history.
    /// The existing validated directory remains available throughout the fetch.
    /// Only a newer validated document replaces it, and persisted bytes contain
    /// public endpoints exclusively rather than either client authentication stream.
    /// Concurrent player loads share one refresh attempt interval; its mirror
    /// requests never receive bearer authentication. Invalid documents and failed
    /// requests leave the previously usable directory intact for ongoing queries.
    fn refresh_directory(&self) {
        {
            let mut at = self.inner.refresh_at.lock().unwrap();
            if at.is_some_and(|at| at.elapsed() < CONFIG_INTERVAL) {
                return;
            }
            *at = Some(Instant::now());
        }
        let inner = self.inner.clone();
        tokio::spawn(async move {
            for url in sgp::CONFIG_URLS {
                let Ok(response) = inner.public.get(url).send().await else {
                    continue;
                };
                let Ok(response) = response.error_for_status() else {
                    continue;
                };
                let Ok(bytes) = response.bytes().await else {
                    continue;
                };
                let Ok(directory) = sgp::Directory::parse(&bytes) else {
                    continue;
                };
                let mut current = inner.directory.lock().unwrap();
                if directory.last_update > current.last_update {
                    *current = directory;
                    // Configuration persistence follows validation and contains only public server data; failure leaves the live directory usable.
                    let path = crate::sources::data_directory().join("sgp-servers.json");
                    if let Some(parent) = path.parent() {
                        let _ = std::fs::create_dir_all(parent);
                    }
                    let _ = std::fs::write(path, &bytes);
                }
                break;
            }
        });
    }

    /// local_get performs an authenticated read against this exact loopback connection.
    /// Paths are constructed internally and cannot select another HTTP origin.
    /// The global history permit bounds remote work without blocking live state
    /// subscriptions or the independent champion-selection execution channel.
    async fn local_get(&self, context: &HistoryContext, path: &str) -> Result<Value> {
        if context.port == 0
            || context.token.is_empty()
            || !path.starts_with('/')
            || path.starts_with("//")
        {
            bail!("Invalid local history context");
        }
        let _permit = self.inner.permits.acquire().await?;
        Ok(self
            .inner
            .local
            .get(format!("https://127.0.0.1:{}{path}", context.port))
            .basic_auth("riot", Some(&context.token))
            .send()
            .await?
            .error_for_status()?
            .json()
            .await?)
    }

    /// access resolves current-region configuration and both client authentication streams.
    /// Successful tokens are reused briefly only for the same local credential
    /// and region. A missing token remains an unavailable SGP path, allowing the
    /// caller to attempt LCU without claiming a successful empty server history.
    /// Both token streams must be ready even though match-history requests use
    /// only entitlements. Account changes invalidate reuse independently from the
    /// local transport credential, which can survive within one running client.
    async fn access(&self, context: &HistoryContext) -> Result<SgpAccess> {
        self.refresh_directory();
        if self
            .inner
            .failure
            .lock()
            .unwrap()
            .as_ref()
            .is_some_and(|failure| failure.applies(context))
        {
            bail!("SGP 暂不可用，冷却期间继续使用客户端数据");
        }
        let region = context.cache_region();
        let mut cache = self.inner.access.lock().await;
        if let Some(entry) = cache.as_ref()
            && entry.port == context.port
            && entry.local_token == context.token
            && entry.region == region
            && entry.account == context.account
            && entry.at.elapsed() < TOKEN_INTERVAL
        {
            return Ok(entry.access.clone());
        }
        let (server, region_path) = self
            .inner
            .directory
            .lock()
            .unwrap()
            .select(&context.region, context.platform.as_deref())?;
        let (entitlements, league_session) = tokio::try_join!(
            self.local_get(context, "/entitlements/v1/token"),
            self.local_get(context, "/lol-league-session/v1/league-session-token"),
        )
        .context("SGP 认证信息未就绪")?;
        if league_session.as_str().is_none_or(str::is_empty) {
            bail!("SGP 会话认证未就绪");
        }
        let bearer = entitlements["accessToken"]
            .as_str()
            .filter(|s| !s.is_empty())
            .context("SGP 战绩认证未就绪")?
            .to_owned();
        let access = SgpAccess {
            server,
            region_path,
            bearer,
        };
        *cache = Some(AccessEntry {
            port: context.port,
            local_token: context.token.clone(),
            region,
            at: Instant::now(),
            account: context.account.clone(),
            access: access.clone(),
        });
        Ok(access)
    }

    /// sgp_get reads one configured history resource with its entitlements credential.
    /// The validated configured origin and numeric or validated identity path
    /// determine the destination. Source authentication is never attached to
    /// public configuration requests or to another region's server selection.
    async fn sgp_get(
        &self,
        access: &SgpAccess,
        path: &str,
        query: &[(String, String)],
    ) -> Result<Value> {
        let _permit = self.inner.permits.acquire().await?;
        Ok(self
            .inner
            .public
            .get(format!(
                "{}{path}",
                access.server.match_history.trim_end_matches('/')
            ))
            .bearer_auth(&access.bearer)
            .query(query)
            .send()
            .await?
            .error_for_status()?
            .json()
            .await?)
    }

    /// load selects a real provider and collects the requested exact-queue sample.
    /// SGP failure permits only a same-client LCU fallback, with that change
    /// recorded in coverage. Successful empty SGP pages remain valid empty results.
    /// LCU pagination scans a bounded number of records because its endpoint does
    /// not implement queue tags; inspected records and retained matches therefore
    /// have independent counts. Dropping this future cancels its active HTTP read.
    pub async fn load(
        &self,
        context: &HistoryContext,
        puuid: &str,
        count: usize,
        queue_id: Option<u64>,
        catalog: &BTreeMap<u32, Champion>,
    ) -> Result<HistoryResult> {
        if crate::scouting::identity(&json!(puuid)).is_none() {
            bail!("无效的玩家标识");
        }
        let count = count.clamp(1, 200);
        let queue_id = queue_id.filter(|queue| *queue > 0);
        let primary = async {
            let access = self.access(context).await?;
            self.collect(context, puuid, count, queue_id, catalog, Some(&access))
                .await
        }
        .await;
        if primary.as_ref().is_err_and(global_sgp_failure) {
            // Only shared transport, authentication, or service failures invalidate server access; one unavailable player's history must not redirect other cards.
            *self.inner.access.lock().await = None;
            {
                let mut failure = self.inner.failure.lock().unwrap();
                if !failure
                    .as_ref()
                    .is_some_and(|failure| failure.applies(context))
                {
                    *failure = Some(SgpFailure {
                        port: context.port,
                        token: context.token.clone(),
                        region: context.cache_region(),
                        account: context.account.clone(),
                        at: Instant::now(),
                    });
                }
            }
        }
        with_lcu_fallback(async move { primary }, || async {
            self.collect(context, puuid, count, queue_id, catalog, None)
                .await
        })
        .await
    }

    /// collect paginates one provider while retaining exact-queue, identity-matched observations.
    /// A repeated page terminates scanning instead of inflating the sample.
    /// Partial pages advance by their actual length, allowing clients that cap a
    /// page below fifty records to continue into older history. Later page failure
    /// preserves already acquired records with an explicit partial-read explanation;
    /// first-page failure propagates so load can choose its documented fallback.
    async fn collect(
        &self,
        context: &HistoryContext,
        puuid: &str,
        count: usize,
        queue_id: Option<u64>,
        catalog: &BTreeMap<u32, Champion>,
        access: Option<&SgpAccess>,
    ) -> Result<HistoryResult> {
        let source = if access.is_some() { "SGP" } else { "LCU" };
        let scan_limit = if queue_id.is_some() {
            (count * 10).clamp(count, MAX_SCAN)
        } else {
            count
        };
        let mut accumulator = Collector::new(count, queue_id, scan_limit);
        let mut note = None;
        while !accumulator.done() {
            let start = accumulator.offset;
            // SGP already applies exact queue tags, so the first twenty-record batch need not download fifty full ten-player summaries.
            let size = PAGE_SIZE.min(scan_limit - start).min(if access.is_some() {
                count - accumulator.matches.len()
            } else {
                PAGE_SIZE
            });
            let response = if let Some(access) = access {
                let mut query = vec![
                    ("startIndex".into(), start.to_string()),
                    ("count".into(), size.to_string()),
                ];
                if let Some(queue) = queue_id {
                    query.push(("tag".into(), format!("q_{queue}")));
                }
                self.sgp_get(
                    access,
                    &format!("/match-history-query/v1/products/lol/player/{puuid}/SUMMARY"),
                    &query,
                )
                .await
            } else {
                self.local_get(context, &format!("/lol-match-history/v1/products/lol/{puuid}/matches?begIndex={start}&endIndex={}", start + size - 1)).await
            };
            let response = match response {
                Ok(response) => response,
                Err(error) if accumulator.offset == 0 => return Err(error),
                Err(_) => {
                    note = Some("后续分页暂不可读，已保留取得的样本".into());
                    break;
                }
            };
            let rows = if access.is_some() {
                response["games"].as_array()
            } else {
                response["games"]["games"].as_array()
            };
            let rows = match rows {
                Some(rows) => rows,
                None if accumulator.offset == 0 => bail!("战绩响应缺少比赛列表"),
                None => {
                    note = Some("后续分页格式暂不可读，已保留取得的样本".into());
                    break;
                }
            };
            let before = accumulator.matches.len();
            let progressed = accumulator.push(rows, puuid, catalog, source);
            if start == 0 && !rows.is_empty() && accumulator.parsed == 0 {
                bail!("战绩响应没有可解析的玩家观察");
            }
            for record in &accumulator.matches[before..] {
                if !record.complete() {
                    continue;
                }
                // Shared matches from another player's page already own compact bytes; reuse those allocations rather than serializing the same tree again.
                let key = (context.cache_region(), record.id);
                let cached = self.inner.cache.lock().unwrap().get(&key).is_some();
                if cached {
                    continue;
                }
                if let Some(raw) = rows.iter().find(|raw| game_id(raw, source) == record.id)
                    && let Ok(game) = CachedGame::encode(raw, source)
                {
                    self.inner.cache.lock().unwrap().insert(key, game);
                }
            }
            if !progressed {
                break;
            }
        }
        if accumulator.offset >= scan_limit
            && accumulator.matches.len() < count
            && queue_id.is_some()
        {
            note = Some("已达扫描上限，当前队列样本不足目标场数".into());
        }
        let matches = accumulator.matches;
        let coverage = coverage(
            source,
            count,
            accumulator.seen.len(),
            queue_id,
            &matches,
            note,
        );
        Ok(HistoryResult { matches, coverage })
    }

    /// detail returns one player's perspective from a shared complete match observation.
    /// Weak per-game gates merge concurrent successful reads without retaining
    /// canceled owners. Cache lookup is repeated after admission, because another
    /// player may have completed the same game while this request was waiting.
    /// A canceled HTTP future releases its gate, allowing the next waiter to load
    /// the game. The region key prevents equal numeric IDs from crossing servers.
    pub async fn detail(
        &self,
        context: &HistoryContext,
        game_id: u64,
        puuid: &str,
        catalog: &BTreeMap<u32, Champion>,
    ) -> Result<MatchRecord> {
        let key = (context.cache_region(), game_id);
        // Release cache ownership before decoding source bytes, so a large match cannot serialize unrelated players' cache reads behind JSON parsing.
        let cached = self.inner.cache.lock().unwrap().get(&key);
        if let Some(game) = cached {
            return game.record(puuid, catalog);
        }
        let gate = {
            let mut gates = self.inner.gates.lock().unwrap();
            gates.retain(|_, gate| gate.strong_count() > 0);
            let gate = gates
                .get(&key)
                .and_then(Weak::upgrade)
                .unwrap_or_else(|| Arc::new(AsyncMutex::new(())));
            gates.insert(key.clone(), Arc::downgrade(&gate));
            gate
        };
        let _guard = gate.lock().await;
        // Another caller may have populated this game while its gate was awaited; decode only after releasing the shared cache mutex.
        let cached = self.inner.cache.lock().unwrap().get(&key);
        if let Some(game) = cached {
            return game.record(puuid, catalog);
        }
        let sgp = if let Ok(access) = self.access(context).await {
            self.sgp_get(
                &access,
                &format!(
                    "/match-history-query/v1/products/lol/{}_{game_id}/SUMMARY",
                    access.region_path
                ),
                &[],
            )
            .await
            .ok()
        } else {
            None
        };
        let game = if let Some(raw) = sgp
            && normalize(&raw, "SGP", puuid, catalog).is_some_and(|record| record.complete())
        {
            CachedGame::encode(&raw, "SGP")?
        } else {
            // A malformed or partial SGP game cannot satisfy complete-match enrichment; keep the current local client as the same-region fallback.
            CachedGame::encode(
                &self
                    .local_get(context, &format!("/lol-match-history/v1/games/{game_id}"))
                    .await?,
                "LCU",
            )?
        };
        let record = game.record(puuid, catalog)?;
        if record.complete() {
            self.inner.cache.lock().unwrap().insert(key, game);
        }
        Ok(record)
    }

    /// enrich fills a bounded recent subset while preserving every readable summary.
    /// Failures leave original records intact and update the completeness count.
    /// Callers publish the summary profile before awaiting this method, then reject
    /// its result if the query or roster generation changed during enrichment.
    /// Complete summaries provide the team denominators directly, so this operation
    /// does not download timelines. Results merge back by their original record
    /// indices only while this exclusive mutable profile remains owned by its caller.
    pub async fn enrich(
        &self,
        context: &HistoryContext,
        profile: &mut Profile,
        catalog: &BTreeMap<u32, Champion>,
        limit: usize,
    ) {
        let pending: Vec<_> = profile
            .matches
            .iter()
            .enumerate()
            .filter(|(_, record)| usable_match(record, None) && !record.complete())
            .take(limit.min(200))
            .map(|(index, record)| (index, record.id))
            .collect();
        let puuid = profile.puuid.clone();
        let results: Vec<_> = stream::iter(pending)
            .map(|(index, id)| {
                let puuid = &puuid;
                async move { (index, self.detail(context, id, puuid, catalog).await) }
            })
            .buffer_unordered(CONCURRENCY)
            .collect()
            .await;
        for (index, result) in results {
            if let Ok(record) = result
                && record.complete()
            {
                profile.matches[index] = record;
            }
        }
        profile.coverage.complete = profile
            .matches
            .iter()
            .filter(|record| usable_match(record, None) && record.complete())
            .count();
    }
}

/// global_sgp_failure reports failures whose scope extends beyond one queried player.
/// Missing player histories and malformed individual payloads permit local fallback
/// without disabling SGP for other identities. Connectivity failures, expired
/// authentication, rate limiting, and server errors describe shared service access
/// and therefore justify a short connection-wide cooldown. In particular, a 404
/// or privacy-related 403 must not erase another player's valid token stream or
/// interrupt that player's progression from the first twenty to fifty records.
fn global_sgp_failure(error: &anyhow::Error) -> bool {
    error
        .chain()
        .filter_map(|cause| cause.downcast_ref::<reqwest::Error>())
        .any(|error| {
            error.is_connect()
                || error.is_timeout()
                || error.status().is_some_and(|status| {
                    status == reqwest::StatusCode::UNAUTHORIZED
                        || status == reqwest::StatusCode::TOO_MANY_REQUESTS
                        || status.is_server_error()
                })
        })
}

/// with_lcu_fallback preserves successful empty histories and exposes actual fallback failures.
/// The secondary operation runs only when the SGP operation fails, which includes
/// unavailable configuration or authentication. Its successful result receives a
/// visible fallback explanation while retaining any existing partial-page note.
/// Failure of both sources propagates an error instead of becoming a fabricated
/// empty sample, and canceling the caller drops whichever provider future is
/// currently active rather than leaving an independent history request running.
async fn with_lcu_fallback<P, F, S>(primary: P, fallback: F) -> Result<HistoryResult>
where
    P: Future<Output = Result<HistoryResult>>,
    F: FnOnce() -> S,
    S: Future<Output = Result<HistoryResult>>,
{
    if let Ok(result) = primary.await {
        return Ok(result);
    }
    let mut result = fallback()
        .await
        .context("SGP 与当前客户端均未提供可读取的战绩")?;
    let note = "SGP 暂不可用，已使用当前客户端 LCU";
    result.coverage.note = Some(match result.coverage.note.take() {
        Some(existing) => format!("{note}；{existing}"),
        None => note.into(),
    });
    Ok(result)
}

/// Collector tracks pagination progress independently from the retained exact-queue sample.
struct Collector {
    target: usize,             // Maximum retained matching game count.
    queue: Option<u64>,        // Exact queue scope when requested.
    limit: usize,              // Maximum source rows inspected per query.
    offset: usize,             // Next provider pagination row offset.
    seen: BTreeSet<u64>,       // Unique observed source game identifiers.
    parsed: usize,             // Observations containing the requested participant.
    matches: Vec<MatchRecord>, // Matching normalized player observations.
}

impl Collector {
    /// new initializes bounded collection for one requested queue and player history.
    fn new(target: usize, queue: Option<u64>, limit: usize) -> Self {
        Self {
            target,
            queue,
            limit,
            offset: 0,
            seen: BTreeSet::new(),
            parsed: 0,
            matches: vec![],
        }
    }

    /// done reports whether retained count or scan budget terminates pagination.
    fn done(&self) -> bool {
        self.matches.len() >= self.target || self.offset >= self.limit
    }

    /// push advances by actual response rows and rejects duplicate or wrong-queue records.
    /// Normalization requires the queried participant's identity. A repeated page
    /// reports no progress so callers cannot loop indefinitely on capped endpoints.
    /// Queue filtering happens before the retained target is counted, preserving
    /// the difference between fifty recent games and fifty games of one queue.
    fn push(
        &mut self,
        rows: &[Value],
        puuid: &str,
        catalog: &BTreeMap<u32, Champion>,
        source: &str,
    ) -> bool {
        let previous = self.seen.len();
        let inspected = rows.len().min(self.limit.saturating_sub(self.offset));
        self.offset += inspected;
        for raw in rows.iter().take(inspected) {
            let id = game_id(raw, source);
            if id == 0 || !self.seen.insert(id) {
                continue;
            }
            if let Some(record) = normalize(raw, source, puuid, catalog) {
                self.parsed += 1;
                if self.queue.is_none_or(|queue| record.queue_id == queue)
                    && self.matches.len() < self.target
                {
                    self.matches.push(record);
                }
            }
        }
        self.seen.len() > previous
    }
}

/// game_id reads an identifier using the selected provider's documented summary envelope.
fn game_id(raw: &Value, source: &str) -> u64 {
    num(if source == "SGP" {
        &raw["json"]["gameId"]
    } else {
        &raw["gameId"]
    })
}

/// normalize preserves queried identity while adapting flat SGP participants directly into records.
/// SGP summary participants contain combat fields directly, whereas LCU places
/// them beneath stats and participant identities. The adapter borrows the source
/// tree and copies only the fields consumed by Lux, avoiding a second full JSON
/// tree during every normalization. Incomplete identities produce no record, and absent team
/// metrics remain marked missing so later contribution analysis cannot treat them
/// as zeros or manufacture a complete team from a personal summary.
fn normalize(
    raw: &Value,
    source: &str,
    puuid: &str,
    catalog: &BTreeMap<u32, Champion>,
) -> Option<MatchRecord> {
    if source != "SGP" {
        return MatchRecord::from_lcu(raw, puuid, catalog);
    }
    let data = &raw["json"];
    let participants = data["participants"].as_array()?;
    let own = participants
        .iter()
        .find(|participant| participant["puuid"].as_str() == Some(puuid))?;
    let champion_id = num(&own["championId"]) as u32;
    let queue_id = num(&data["queueId"]);
    let name = |id| {
        catalog
            .get(&id)
            .map(|champion| champion.name.clone())
            .unwrap_or_else(|| format!("英雄 {id}"))
    };
    Some(MatchRecord {
        id: num(&data["gameId"]),
        champion_id,
        name: name(champion_id),
        queue: queue_label(queue_id).into(),
        queue_id,
        win: own["win"].as_bool()?,
        kills: num(&own["kills"]),
        deaths: num(&own["deaths"]),
        assists: num(&own["assists"]),
        cs: num(&own["totalMinionsKilled"]) + num(&own["neutralMinionsKilled"]),
        damage: num(&own["totalDamageDealtToChampions"]),
        gold: num(&own["goldEarned"]),
        duration: num(&data["gameDuration"]),
        created: data["gameCreation"].as_i64().unwrap_or(0),
        team: num(&own["teamId"]),
        source: "SGP".into(),
        game_type: data["gameType"].as_str().unwrap_or("").into(),
        remake: own["gameEndedInEarlySurrender"] == true
            || data["endOfGameResult"]
                .as_str()
                .is_some_and(|result| result.starts_with("Abort_")),
        missing_metrics: [
            "totalDamageDealtToChampions",
            "goldEarned",
            "totalDamageTaken",
            "totalHeal",
            "damageDealtToTurrets",
        ]
        .map(|field| {
            participants
                .iter()
                .any(|participant| !participant[field].is_number())
        }),
        players: participants
            .iter()
            .map(|participant| {
                let id = num(&participant["championId"]) as u32;
                Player {
                    champion_id: id,
                    name: name(id),
                    team: num(&participant["teamId"]),
                    local: participant["puuid"].as_str() == Some(puuid),
                    kills: num(&participant["kills"]),
                    deaths: num(&participant["deaths"]),
                    assists: num(&participant["assists"]),
                    damage: num(&participant["totalDamageDealtToChampions"]),
                    gold: num(&participant["goldEarned"]),
                    taken: num(&participant["totalDamageTaken"]),
                    heal: num(&participant["totalHeal"]),
                    tower_damage: num(&participant["damageDealtToTurrets"]),
                }
            })
            .collect(),
    })
}

/// coverage derives source counts from retained records and shared usability rules.
/// The requested target describes the query rather than a claim about returned
/// availability. Scanned rows can exceed retained rows after queue filtering, and
/// valid rows can be fewer after excluding remakes or non-matched games. Complete
/// coverage is a subset of valid observations and never includes a personal-only
/// summary. Source names identify the provider that actually produced this sample.
fn coverage(
    source: &str,
    requested: usize,
    scanned: usize,
    queue_id: Option<u64>,
    matches: &[MatchRecord],
    note: Option<String>,
) -> HistoryCoverage {
    HistoryCoverage {
        source: source.into(),
        requested,
        fetched: matches.len(),
        scanned,
        valid: matches
            .iter()
            .filter(|record| usable_match(record, None))
            .count(),
        complete: matches
            .iter()
            .filter(|record| usable_match(record, None) && record.complete())
            .count(),
        queue_id,
        note,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};

    /// compact_cache_preserves_perspectives_and_exact_payload_budget protects low-memory shared match storage.
    /// Two perspectives decode independently from one shared byte allocation, and
    /// replacement subtracts the prior retained payload rather than double counting it.
    #[test]
    fn compact_cache_preserves_perspectives_and_exact_payload_budget() {
        let raw = json!({"json":{"gameId":7,"queueId":450,"gameDuration":1200,
            "participants":[{"puuid":"one","championId":99,"teamId":100,"win":true,"kills":3},
                {"puuid":"two","championId":103,"teamId":200,"win":false,"kills":8}]}});
        let encoded = CachedGame::encode(&raw, "SGP").unwrap();
        assert_eq!(encoded.raw.len(), serde_json::to_vec(&raw).unwrap().len());
        assert!(Arc::ptr_eq(&encoded.raw, &encoded.clone().raw));
        let one = encoded.record("one", &BTreeMap::new()).unwrap();
        let two = encoded.record("two", &BTreeMap::new()).unwrap();
        assert_eq!((one.kills, one.win), (3, true));
        assert_eq!((two.kills, two.win), (8, false));
        assert_eq!(one.players.iter().filter(|player| player.local).count(), 1);
        assert_eq!(two.players.iter().filter(|player| player.local).count(), 1);
        let mut cache = MatchCache::default();
        cache.insert(("region".into(), 7), encoded);
        let smaller = CachedGame::encode(&json!({"gameId":7}), "LCU").unwrap();
        let size = smaller.raw.len();
        cache.insert(("region".into(), 7), smaller);
        assert_eq!(cache.bytes, size);
        assert_eq!(cache.entries.len(), 1);
    }

    /// sgp_direct_normalization_preserves_missing_and_remake_semantics protects allocation-reduced field mapping.
    /// Complete blue and red teams retain identity association after decoding compact
    /// bytes, while an absent metric or duplicated player cannot manufacture valid contribution coverage.
    #[test]
    fn sgp_direct_normalization_preserves_missing_and_remake_semantics() {
        let participants: Vec<_> = (0..10)
            .map(|index| {
                json!({"puuid":format!("player-{index}"),
            "teamId":if index < 5 {100} else {200}, "championId":99,"win":index < 5,
            "kills":index,"totalDamageDealtToChampions":10,"goldEarned":100,
            "totalDamageTaken":20,"totalHeal":0,"damageDealtToTurrets":0})
            })
            .collect();
        let mut raw = json!({"json":{"gameId":8,"queueId":450,"gameType":"MATCHED_GAME",
            "gameDuration":1200,"participants":participants}});
        let record = normalize(&raw, "SGP", "player-0", &BTreeMap::new()).unwrap();
        assert!(record.complete());
        assert_eq!(record.contribution(0).unwrap().0, 0.2);
        raw["json"]["participants"][2]
            .as_object_mut()
            .unwrap()
            .remove("totalDamageTaken");
        raw["json"]["participants"][0]["gameEndedInEarlySurrender"] = json!(true);
        let record = normalize(&raw, "SGP", "player-0", &BTreeMap::new()).unwrap();
        assert!(record.remake);
        assert_eq!(record.game_type, "MATCHED_GAME");
        assert!(record.contribution(2).is_none());
        assert!(record.contribution(0).is_some());
        raw["json"]["participants"][1]["puuid"] = json!("player-0");
        assert!(
            !normalize(&raw, "SGP", "player-0", &BTreeMap::new())
                .unwrap()
                .complete()
        );
    }

    /// player_specific_http_failures_do_not_disable_other_histories protects concurrent multi-player progression.
    /// Loopback HTTP responses reproduce actual reqwest status errors. A missing
    /// or private player must not trigger the service-wide cooldown, while shared
    /// authentication, throttling, and backend failures retain their bounded retry policy.
    #[tokio::test]
    async fn player_specific_http_failures_do_not_disable_other_histories() {
        for (status, shared) in [
            (404, false),
            (403, false),
            (401, true),
            (429, true),
            (503, true),
        ] {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let port = listener.local_addr().unwrap().port();
            // Each responder owns one ephemeral loopback socket and is joined before the next status, preventing unrelated request or port reuse.
            let server = std::thread::spawn(move || {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                let mut request = [0; 2048];
                assert!(stream.read(&mut request).unwrap() > 0);
                write!(
                    stream,
                    "HTTP/1.1 {status} Test\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                )
                .unwrap();
            });
            let error = Client::new()
                .get(format!("http://127.0.0.1:{port}/history"))
                .send()
                .await
                .unwrap()
                .error_for_status()
                .unwrap_err();
            assert_eq!(
                global_sgp_failure(&anyhow::Error::new(error).context("SGP history read")),
                shared
            );
            server.join().unwrap();
        }
        assert!(!global_sgp_failure(&anyhow::anyhow!(
            "战绩响应没有可解析的玩家观察"
        )));
    }

    /// failure_cooldown_expires_and_does_not_cross_accounts protects retry eligibility after connection changes.
    /// Repeated player requests reuse a brief unavailable interval, but a changed
    /// account, credential, or expired interval must permit a fresh SGP attempt.
    #[test]
    fn failure_cooldown_expires_and_does_not_cross_accounts() {
        let context = HistoryContext::new(
            1234,
            "test-local-token".into(),
            "TENCENT".into(),
            Some("HN1".into()),
        )
        .with_account("own-a");
        let mut failure = SgpFailure {
            port: context.port,
            token: context.token.clone(),
            region: context.cache_region(),
            account: context.account.clone(),
            at: Instant::now(),
        };
        assert!(failure.applies(&context));
        assert!(!failure.applies(&context.clone().with_account("own-b")));
        let mut changed = context.clone();
        changed.token = "rotated-test-token".into();
        assert!(!failure.applies(&changed));
        failure.at = Instant::now() - FAILURE_INTERVAL;
        assert!(!failure.applies(&context));
    }

    /// fallback_preserves_empty_success_and_does_not_hide_dual_failure protects provider selection semantics.
    /// Successful empty SGP responses must not query LCU, while unavailable SGP
    /// paths expose their actual replacement source and propagate a second failure.
    #[tokio::test]
    async fn fallback_preserves_empty_success_and_does_not_hide_dual_failure() {
        let empty = || HistoryResult {
            matches: vec![],
            coverage: HistoryCoverage {
                source: "SGP".into(),
                requested: 50,
                ..HistoryCoverage::default()
            },
        };
        let success = with_lcu_fallback(async { Ok(empty()) }, || async {
            panic!("LCU must not replace a successful empty SGP sample")
        })
        .await
        .unwrap();
        assert_eq!(success.coverage.source, "SGP");
        assert!(success.coverage.note.is_none());
        let fallback = with_lcu_fallback(async { bail!("SGP unavailable") }, || async {
            Ok(HistoryResult {
                coverage: HistoryCoverage {
                    source: "LCU".into(),
                    note: Some("partial page".into()),
                    ..HistoryCoverage::default()
                },
                matches: vec![],
            })
        })
        .await
        .unwrap();
        assert_eq!(fallback.coverage.source, "LCU");
        assert!(fallback.coverage.note.unwrap().contains("partial page"));
        assert!(
            with_lcu_fallback(async { bail!("SGP unavailable") }, || async {
                bail!("LCU unavailable")
            })
            .await
            .is_err()
        );
    }

    /// lcu_game creates one actual-participant response for pagination behavior checks.
    fn lcu_game(id: u64, queue: u64, puuid: &str) -> Value {
        json!({"gameId":id,"queueId":queue,"gameDuration":1200,"gameType":"MATCHED_GAME",
            "participantIdentities":[{"participantId":1,"player":{"puuid":puuid}}],
            "participants":[{"participantId":1,"teamId":100,"championId":99,
                "stats":{"kills":2,"deaths":1,"assists":3,"win":true}}]})
    }

    /// exact_queue_collection_counts_scanned_and_retained_separately protects configured analysis scope.
    /// Wrong-queue observations and repeated pages must not fill the target count,
    /// while short provider pages advance using their actual returned row count.
    #[test]
    fn exact_queue_collection_counts_scanned_and_retained_separately() {
        let mut collector = Collector::new(2, Some(450), 100);
        let catalog = BTreeMap::new();
        let first = vec![lcu_game(1, 420, "own"), lcu_game(2, 450, "own")];
        assert!(collector.push(&first, "own", &catalog, "LCU"));
        assert_eq!(collector.offset, 2);
        assert_eq!(collector.matches.len(), 1);
        assert!(!collector.push(&first, "own", &catalog, "LCU"));
        assert_eq!(collector.seen.len(), 2);
        assert!(collector.push(&[lcu_game(3, 450, "own")], "own", &catalog, "LCU"));
        assert!(collector.done());
        let counts = coverage(
            "LCU",
            2,
            collector.seen.len(),
            Some(450),
            &collector.matches,
            None,
        );
        assert_eq!(
            (
                counts.scanned,
                counts.fetched,
                counts.valid,
                counts.complete
            ),
            (3, 2, 2, 0)
        );
    }

    /// sgp_adapter_retains_identity_and_missing_contribution_fields protects mixed-source normalization.
    /// Flat SGP combat fields must map into the same KDA observations as LCU,
    /// while an absent player or an incomplete roster cannot acquire contribution shares.
    #[test]
    fn sgp_adapter_retains_identity_and_missing_contribution_fields() {
        let raw = json!({"json":{"gameId":11,"queueId":450,"gameDuration":1200,
            "gameType":"MATCHED_GAME","participants":[{"participantId":1,"puuid":"own",
            "championId":99,"teamId":100,"kills":4,"deaths":2,"assists":8,"win":true}]}});
        let record = normalize(&raw, "SGP", "own", &BTreeMap::new()).unwrap();
        assert_eq!((record.kills, record.deaths, record.assists), (4, 2, 8));
        assert_eq!(record.source, "SGP");
        assert!(record.contribution(0).is_none());
        assert!(normalize(&raw, "SGP", "other", &BTreeMap::new()).is_none());
    }

    /// cached_matches_keep_regions_separate_and_evict_cold_entries protects bounded cross-player reuse.
    /// Equal numeric game identifiers on different servers must retain separate observations,
    /// and exceeding the count budget evicts the oldest retained match instead of growing indefinitely.
    #[test]
    fn cached_matches_keep_regions_separate_and_evict_cold_entries() {
        let mut cache = MatchCache::default();
        let game = CachedGame::encode(&json!({"gameId":1}), "LCU").unwrap();
        cache.insert(("NA1".into(), 1), game.clone());
        cache.insert(("KR".into(), 1), game.clone());
        assert_eq!(cache.entries.len(), 2);
        for id in 2..=(CACHE_MATCHES as u64 + 1) {
            cache.insert(("NA1".into(), id), game.clone());
        }
        assert_eq!(cache.entries.len(), CACHE_MATCHES);
        assert!(cache.get(&("NA1".into(), 1)).is_none());
        assert!(cache.bytes <= CACHE_BYTES);
    }
}
