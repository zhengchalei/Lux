//! sources.rs fetches public champion statistics and caches their source context.
//! RESG modules are parsed as JSON exports rather than executed, and their patch
//! identifiers remain separate from OP.GG's ordinary ARAM version. A refresh may
//! update one source while another is unavailable; the old observation survives
//! only with its own visible context, never with a newly invented patch label.
//!
//! The local cache contains public observations only. Matchup queries retain the
//! queried enemy's perspective until their win counts are explicitly inverted
//! for candidate recommendations. Every query includes region, tier, position,
//! and patch in its resulting label. A failed request returns an error to the
//! worker, which keeps its current data and reports that the refresh failed.
//! A dedicated public worker refreshes on startup and on a bounded schedule,
//! keeping provider latency away from client discovery and roster polling.
//! Replies return to the client worker, which retains private account state and
//! accepts matchup results only for the current request generation.
//! Each public dataset is validated before publication or cache replacement.
//! Successful sources retain independent refresh timestamps, so a partial update
//! cannot label an older source as newly downloaded or turn malformed balance
//! rows into a confirmed absence of buffs and debuffs.

use crate::model::{Database, Matchup, num, resg_adjustments};
use anyhow::{Context, Result, bail, ensure};
use reqwest::blocking::Client;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    collections::{BTreeMap, BTreeSet},
    path::PathBuf,
    sync::mpsc::{self, Receiver, Sender},
    thread,
    time::{Duration, Instant},
};

// These public endpoint roots identify the user-requested providers and never receive local client authentication material.
const OPGG: &str = "https://lol-api-champion.op.gg";
const RESG_PAGE: &str = "https://www.bilibilitoy.com/toy/resg/19226257645568-v42593/";

// Successful refreshes check periodically; incomplete or offline attempts retry sooner without requiring a visible update control.
const REFRESH_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);
const RETRY_INTERVAL: Duration = Duration::from_secs(5 * 60);

/// Request carries explicit public lookups to the independently scheduled provider worker.
pub(crate) enum Request {
    Champion(u32), // Champion requiring matching Mayhem balance observations.
    Matchups {
        enemy: u32,       // Publicly selected opposing champion identifier.
        position: String, // Explicitly selected OP.GG lane identifier.
        generation: u64,  // Client-owned request identity for rejecting stale replies.
    },
}

/// Event returns public observations without replacing client-owned account or roster state.
pub(crate) enum Event {
    Public(Box<Database>), // Updated public catalog and source freshness labels.
    Status(String),        // Automatic refresh progress or next retry explanation.
    Message(String),       // Result of an explicit hero detail request.
    Matchups {
        generation: u64, // Original client-owned matchup request identity.
        result: Result<Vec<Matchup>, String>, // Parsed observations or visible lookup failure.
    },
}

/// RefreshReport describes both source outcomes and the next automatic refresh cadence.
pub struct RefreshReport {
    pub message: String, // Source-specific success and retained-cache explanations.
    pub complete: bool,  // All three public datasets refreshed successfully.
}

/// start fetches public data automatically without blocking local client discovery.
/// The first refresh runs after cached observations are published, including when
/// League is absent. Dropping the request sender ends this worker after the
/// current bounded HTTP operation, and incomplete attempts retry after five minutes.
///
/// All public cache mutations stay on this worker, so periodic refreshes and
/// champion detail writes cannot overwrite each other's cache snapshots. Matchup
/// replies carry the caller's generation; the client worker decides whether that
/// result still belongs to the selected target and observed account.
pub(crate) fn start(mut db: Database) -> (Sender<Request>, Receiver<Event>) {
    let (requests, receiver) = mpsc::channel();
    let (sender, events) = mpsc::channel();
    thread::Builder::new()
        .name("lux-public".into())
        .spawn(move || {
            let mut sources = Sources::new().ok();
            if let Some(source) = &sources {
                source.apply(&mut db);
                let _ = sender.send(Event::Public(Box::new(db.clone())));
            }
            let mut due = Instant::now();
            loop {
                match receiver.recv_timeout(due.saturating_duration_since(Instant::now())) {
                    Err(mpsc::RecvTimeoutError::Disconnected) => break,
                    Err(mpsc::RecvTimeoutError::Timeout) => {
                        let _ = sender.send(Event::Status("强度数据自动更新中…".into()));
                        // Recreate a failed transport only on a scheduled attempt, avoiding a busy loop when initialization remains unavailable.
                        if sources.is_none() {
                            sources = Sources::new().ok();
                        }
                        let refreshed = sources.as_mut().map(|s| s.refresh(&mut db));
                        let complete = match refreshed {
                            Some(Ok(report)) => {
                                let _ = sender.send(Event::Public(Box::new(db.clone())));
                                let suffix = if report.complete {
                                    "自动更新完成"
                                } else {
                                    "部分更新，5分钟后重试"
                                };
                                let _ = sender
                                    .send(Event::Status(format!("{suffix} · {}", report.message)));
                                report.complete
                            }
                            _ => {
                                let _ = sender.send(Event::Status(
                                    "联网更新暂不可用 · 保留缓存 · 5分钟后自动重试".into(),
                                ));
                                false
                            }
                        };
                        // Explicit lookups do not postpone this deadline; a queued request cannot silently disable periodic refresh or offline recovery.
                        due = Instant::now()
                            + if complete {
                                REFRESH_INTERVAL
                            } else {
                                RETRY_INTERVAL
                            };
                    }
                    Ok(Request::Champion(id)) => {
                        match sources.as_mut().map(|s| s.champion_detail(id, &mut db)) {
                            Some(Ok(_)) => {
                                let _ = sender.send(Event::Public(Box::new(db.clone())));
                            }
                            _ => {
                                let _ = sender.send(Event::Message(
                                    "英雄调整暂不可用，保留已标注的缓存".into(),
                                ));
                            }
                        }
                    }
                    Ok(Request::Matchups {
                        enemy,
                        position,
                        generation,
                    }) => {
                        let result = sources
                            .as_ref()
                            .context("公共数据服务暂不可用")
                            .and_then(|s| s.matchups(enemy, &position))
                            .map_err(|_| "该位置对位数据暂不可用".to_owned());
                        let _ = sender.send(Event::Matchups { generation, result });
                    }
                }
            }
        })
        .expect("Unable to start Lux public worker");
    (requests, events)
}

/// SourceCache persists public observations without client or player credentials.
#[derive(Default, Serialize, Deserialize)]
#[serde(default)]
struct SourceCache {
    resg_patch: String,            // Source-provided Mayhem patch identifier.
    resg: Value,                   // RESG public champion list response.
    collected_at: String,          // RESG source collection timestamp string.
    aram_patch: String,            // OP.GG ordinary ARAM patch identifier.
    aram: Value,                   // OP.GG ordinary ARAM statistics response.
    balance: Value,                // Versionless ordinary ARAM adjustment response.
    details: BTreeMap<u32, Value>, // Matching Mayhem champion balance observations.
    fetched_at: String,            // Local UTC public refresh timestamp.
    resg_fetched_at: String,       // Last validated RESG refresh timestamp.
    aram_fetched_at: String,       // Last validated ARAM strength timestamp.
    balance_fetched_at: String,    // Last validated ARAM balance timestamp.
}

/// Sources owns the public HTTP client and versioned cache lifecycle.
pub struct Sources {
    http: Client,       // Public-only certificate-validating HTTP transport.
    cache: SourceCache, // Last parsed public source observations.
    path: PathBuf,      // Per-user public cache file location.
}

impl Sources {
    /// new initializes public transport and restores a parseable per-user cache.
    /// Missing or invalid cache files select bundled observations through the
    /// caller's Database. No local client authorization is attached to this HTTP
    /// client, including when the public sources use redirects.
    pub fn new() -> Result<Self> {
        let path = data_directory().join("public-statistics.json");
        // A missing or invalid public cache leaves the explicit bundled snapshots available instead of blocking application startup.
        let cache = std::fs::read(&path)
            .ok()
            .and_then(|b| serde_json::from_slice(&b).ok())
            .unwrap_or_default();
        let http = Client::builder()
            .timeout(Duration::from_secs(12))
            .user_agent("Lux/0.1 (Windows desktop)")
            .build()?;
        Ok(Self { http, cache, path })
    }

    /// apply overlays independently validated cached sources onto the public catalog.
    /// Each dataset retains its own patch and successful refresh timestamp, so a
    /// partial network update cannot present an older provider as newly fetched.
    /// Invalid cached rows leave the caller's existing observations unchanged.
    ///
    /// Mayhem champion details are applied only alongside their owning cached
    /// patch. The timestamp is attached to rows actually supplied by that source,
    /// while ordinary ARAM strength and balance remain independently validated.
    pub fn apply(&self, db: &mut Database) {
        if !self.cache.resg_patch.is_empty() && validate_resg(&self.cache.resg).is_ok() {
            db.merge_resg(&self.cache.resg, &self.cache.resg_patch);
            for (id, v) in &self.cache.details {
                if let Some(c) = db.champions.get_mut(id) {
                    c.mayhem.adjustments = resg_adjustments(v);
                }
            }
            if !self.cache.resg_fetched_at.is_empty() {
                for row in self.cache.resg.as_array().into_iter().flatten() {
                    if let Some(champion) = db.champions.get_mut(&(num(&row["id"]) as u32)) {
                        champion
                            .mayhem
                            .source
                            .push_str(&format!(" · 更新 {}", self.cache.resg_fetched_at));
                    }
                }
            }
        }
        if !self.cache.aram_patch.is_empty() && validate_aram(&self.cache.aram).is_ok() {
            db.merge_aram(&self.cache.aram, &self.cache.aram_patch);
            if !self.cache.aram_fetched_at.is_empty() {
                for row in self.cache.aram["data"].as_array().into_iter().flatten() {
                    if let Some(champion) = db.champions.get_mut(&(num(&row["id"]) as u32)) {
                        champion
                            .aram
                            .source
                            .push_str(&format!(" · 更新 {}", self.cache.aram_fetched_at));
                    }
                }
            }
        }
        if validate_balance(&self.cache.balance).is_ok() {
            db.merge_balance(&self.cache.balance);
        }
        if !self.cache.fetched_at.is_empty() {
            db.public_updated = format!(
                "公共缓存 · {} · RESG 采集 {}",
                self.cache.fetched_at, self.cache.collected_at
            );
        }
    }

    /// refresh downloads the current provider versions before updating each dataset.
    /// Independent source failures leave that source's previous payload intact.
    /// Successful results are cached together with their own patch identifiers;
    /// the returned report states partial failures instead of claiming full success.
    /// If every provider fails, the cache timestamp remains unchanged and the
    /// caller receives an error suitable for scheduling an earlier retry.
    pub fn refresh(&mut self, db: &mut Database) -> Result<RefreshReport> {
        let mut report = Vec::new();
        let mut successful = 0;
        match self.refresh_resg() {
            Ok(()) => {
                successful += 1;
                report.push(format!("RESG {}", self.cache.resg_patch));
            }
            Err(_) => report.push("RESG 更新失败，保留快照".into()),
        }
        let aram = (|| -> Result<()> {
            let v: Value = self
                .http
                .get(format!("{OPGG}/api/kr/champions/aram/versions"))
                .send()?
                .error_for_status()?
                .json()?;
            let patch = v["data"][0].as_str().context("Missing ARAM version")?;
            let payload: Value = self
                .http
                .get(format!("{OPGG}/api/kr/champions/aram"))
                .query(&[("tier", "all"), ("version", patch)])
                .send()?
                .error_for_status()?
                .json()?;
            validate_aram(&payload)?;
            self.cache.aram = payload;
            self.cache.aram_patch = patch.into();
            self.cache.aram_fetched_at = refresh_timestamp();
            Ok(())
        })();
        report.push(if aram.is_ok() {
            successful += 1;
            format!("普通大乱斗 {}", self.cache.aram_patch)
        } else {
            "普通大乱斗更新失败，保留快照".into()
        });
        match self
            .http
            .get(format!("{OPGG}/api/contents/aram-balance"))
            .send()
            .and_then(|r| r.error_for_status())
            .and_then(|r| r.json::<Value>())
        {
            Ok(v) if validate_balance(&v).is_ok() => {
                self.cache.balance = v;
                self.cache.balance_fetched_at = refresh_timestamp();
                successful += 1;
            }
            _ => report.push("普通调整更新失败，保留快照".into()),
        }
        if successful == 0 {
            bail!("All public sources unavailable");
        }
        self.cache.fetched_at = refresh_timestamp();
        self.apply(db);
        self.save()?;
        Ok(RefreshReport {
            message: report.join(" · "),
            complete: successful == 3,
        })
    }

    /// refresh_resg validates one published version before replacing its champion list.
    /// Both the version document and the complete list must parse before any
    /// existing observation changes. Invalid rows preserve the old patch, its
    /// champion data, and its original successful refresh timestamp.
    ///
    /// A changed patch clears previously cached champion adjustment details,
    /// because those values cannot be attributed to the new version. The new
    /// collection time and local completion time are recorded independently.
    fn refresh_resg(&mut self) -> Result<()> {
        let versions = module_json(
            &self
                .http
                .get(format!("{RESG_PAGE}api/v1/versions.js"))
                .send()?
                .error_for_status()?
                .text()?,
        )?;
        let patch = versions[0]["version"]
            .as_str()
            .context("Missing RESG version")?;
        let payload = module_json(
            &self
                .http
                .get(format!("{RESG_PAGE}api/v1/versions/{patch}/champions.js"))
                .send()?
                .error_for_status()?
                .text()?,
        )?;
        validate_resg(&payload["items"])?;
        // A new source patch cannot reuse the previous patch's balance details, even when champion identifiers remain unchanged.
        if self.cache.resg_patch != patch {
            self.cache.details.clear();
        }
        self.cache.resg = payload["items"].clone();
        self.cache.resg_patch = patch.into();
        self.cache.resg_fetched_at = refresh_timestamp();
        self.cache.collected_at = versions[0]["collectedAt"]
            .as_str()
            .unwrap_or("未标注")
            .into();
        Ok(())
    }

    /// champion_detail loads adjustments matching the current cached RESG patch.
    /// A missing patch first requires a RESG version refresh. Absent balance
    /// fields remain unknown rather than becoming an invented empty adjustment;
    /// the returned boolean reports whether usable adjustment data was present.
    pub fn champion_detail(&mut self, id: u32, db: &mut Database) -> Result<bool> {
        if self.cache.resg_patch.is_empty() {
            self.refresh_resg()?;
        }
        let v = module_json(
            &self
                .http
                .get(format!(
                    "{RESG_PAGE}api/v1/versions/{}/champions/{id}.js",
                    self.cache.resg_patch
                ))
                .send()?
                .error_for_status()?
                .text()?,
        )?;
        let present = resg_adjustments(&v["bb"]).is_some();
        self.cache.details.insert(id, v["bb"].clone());
        self.apply(db);
        self.save()?;
        Ok(present)
    }

    /// matchups converts enemy-owned victory counts into candidate-owned recommendations.
    /// Region and tier are explicit product defaults: KR and platinum-plus.
    /// The position comes from the user's chosen lane, while the enemy must be
    /// a publicly visible room champion selected through the interface.
    pub fn matchups(&self, enemy: u32, position: &str) -> Result<Vec<Matchup>> {
        if !["top", "jungle", "mid", "adc", "support"].contains(&position) {
            bail!("Unsupported position");
        }
        let versions: Value = self
            .http
            .get(format!("{OPGG}/api/kr/champions/ranked/versions"))
            .send()?
            .error_for_status()?
            .json()?;
        let patch = versions["data"][0]
            .as_str()
            .context("Missing ranked version")?;
        let value: Value = self
            .http
            .get(format!("{OPGG}/api/kr/champions/ranked/{enemy}/{position}"))
            .query(&[("tier", "platinum_plus"), ("version", patch)])
            .send()?
            .error_for_status()?
            .json()?;
        let rows = value["data"]["counters"]
            .as_array()
            .context("No matchup observations")?;
        Ok(rows
            .iter()
            .filter_map(|v| {
                let samples = num(&v["play"]);
                let enemy_wins = num(&v["win"]);
                if samples == 0 || enemy_wins > samples {
                    return None;
                }
                Some(Matchup {
                    champion: num(&v["champion_id"]) as u32,
                    enemy,
                    position: position.into(),
                    wins: samples - enemy_wins,
                    samples,
                    source: format!("OP.GG · 韩服 · 铂金以上 · {position} · {patch}"),
                })
            })
            .collect())
    }

    /// save persists public data after complete source parsing succeeds.
    fn save(&self) -> Result<()> {
        std::fs::create_dir_all(data_directory())?;
        std::fs::write(&self.path, serde_json::to_vec(&self.cache)?)?;
        Ok(())
    }
}

/// refresh_timestamp identifies the local completion time of one validated source.
fn refresh_timestamp() -> String {
    chrono::Utc::now().format("%Y-%m-%d %H:%M UTC").to_string()
}

/// checked_rows requires a nonempty dataset with distinct positive champion identifiers.
/// The returned rows retain their original values for provider-specific validation.
/// Duplicate or missing identities reject the entire update before existing observations change.
fn checked_rows<'a>(value: &'a Value, id_key: &str) -> Result<&'a [Value]> {
    let rows = value.as_array().context("Missing champion rows")?;
    ensure!(!rows.is_empty(), "Empty champion dataset");
    let mut identities = BTreeSet::new();
    for row in rows {
        let id = row[id_key].as_u64().context("Missing champion identity")?;
        ensure!(id > 0 && identities.insert(id), "Invalid champion identity");
    }
    Ok(rows)
}

/// validate_resg checks the catalog and observed win counts before replacing Mayhem data.
/// A malformed row rejects the whole source, preserving the previous patch and adjustments.
/// Published numeric rates use fractions between zero and one.
fn validate_resg(value: &Value) -> Result<()> {
    for row in checked_rows(value, "id")? {
        for field in ["name", "title", "alias"] {
            ensure!(
                row[field].as_str().is_some_and(|s| !s.trim().is_empty()),
                "Missing RESG catalog field"
            );
        }
        let played = row["tm"].as_u64().context("Missing RESG sample count")?;
        let wins = row["wm"].as_u64().context("Missing RESG victory count")?;
        ensure!(wins <= played, "Invalid RESG victory count");
        ensure!(valid_rate(&row["wr"]), "Invalid RESG win rate");
    }
    Ok(())
}

/// validate_aram checks source identities and strength observations before publication.
/// Sample counts and fractional victory rates must be present in every row.
/// Invalid responses leave the last readable ordinary ARAM dataset available.
fn validate_aram(value: &Value) -> Result<()> {
    for row in checked_rows(&value["data"], "id")? {
        let stats = &row["average_stats"];
        ensure!(stats["play"].as_u64().is_some(), "Missing ARAM samples");
        ensure!(valid_rate(&stats["win_rate"]), "Invalid ARAM win rate");
    }
    Ok(())
}

/// validate_balance preserves unknown adjustments when a provider omits required multipliers.
/// Every recognized balance field must be numeric, including baseline values.
/// This prevents a damaged response from becoming a confirmed empty buff list.
fn validate_balance(value: &Value) -> Result<()> {
    for row in checked_rows(&value["data"], "champion_id")? {
        for field in [
            "damage_dealt",
            "damage_taken",
            "healing",
            "shield_amount",
            "attack_speed",
            "cooldown_reduction",
            "tenacity",
            "energy_regen",
        ] {
            ensure!(row[field].as_f64().is_some(), "Missing balance field");
        }
    }
    Ok(())
}

/// valid_rate recognizes finite source probabilities represented as fractional numbers.
fn valid_rate(value: &Value) -> bool {
    value
        .as_f64()
        .is_some_and(|rate| (0.0..=1.0).contains(&rate))
}

/// module_json accepts only a JSON value exported by a static module.
/// It never evaluates JavaScript. Modules containing executable statements fail
/// parsing and therefore cannot replace the last usable public observations.
pub fn module_json(text: &str) -> Result<Value> {
    let content = text
        .trim()
        .strip_prefix("export default ")
        .context("Not a JSON export")?
        .trim_end_matches(';')
        .trim();
    Ok(serde_json::from_str(content)?)
}

/// data_directory selects the current user's local application cache directory.
pub fn data_directory() -> PathBuf {
    std::env::var_os("LOCALAPPDATA")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir)
        .join("Lux")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// exported_data_is_parsed_without_executing_javascript protects static provider ingestion.
    /// JSON exports are accepted, while executable modules must fail rather than
    /// being evaluated during a champion statistic update.
    #[test]
    fn exported_data_is_parsed_without_executing_javascript() {
        assert_eq!(
            module_json("export default {\"value\":2};").unwrap(),
            json!({"value":2})
        );
        assert!(module_json("export default fetch('https://example.com')").is_err());
    }

    /// bundled_provider_contracts_validate_public_source_shapes protects existing public observations.
    /// All bundled datasets must pass the same validation applied to downloaded replacements,
    /// keeping the offline startup path consistent with online ingestion.
    #[test]
    fn bundled_provider_contracts_validate_public_source_shapes() {
        validate_resg(
            &serde_json::from_str::<Value>(include_str!("../assets/champions.json")).unwrap(),
        )
        .unwrap();
        validate_aram(&serde_json::from_str::<Value>(include_str!("../assets/aram.json")).unwrap())
            .unwrap();
        validate_balance(
            &serde_json::from_str::<Value>(include_str!("../assets/aram-balance.json")).unwrap(),
        )
        .unwrap();
    }

    /// malformed_updates_preserve_cached_source_context prevents mixed freshness and invented balance.
    /// A damaged source cannot overwrite its readable bundled observations, while a valid
    /// independent source can still publish its own patch and refresh timestamp.
    #[test]
    fn malformed_updates_preserve_cached_source_context() {
        let mut db = Database::bundled();
        let original = db.champion(103).aram.win_rate;
        let cache = SourceCache {
            aram_patch: "invalid-new-patch".into(),
            aram: json!({"data":[{"id":103,"average_stats":{"play":10,"win_rate":57}}]}),
            balance: json!({"data":[{"champion_id":103}]}),
            resg_patch: "test-patch".into(),
            resg: json!([{"id":103,"name":"九尾妖狐","title":"阿狸","alias":"Ahri","tm":10,"wm":6,"wr":0.6}]),
            resg_fetched_at: "2026-11-10 12:00 UTC".into(),
            ..SourceCache::default()
        };
        let sources = Sources {
            http: Client::new(),
            cache,
            path: PathBuf::new(),
        };
        sources.apply(&mut db);
        assert_eq!(db.champion(103).aram.win_rate, original);
        assert_eq!(db.champion(103).mayhem.win_rate, Some(0.6));
        assert!(
            db.champion(103)
                .mayhem
                .source
                .contains("2026-11-10 12:00 UTC")
        );
        assert!(validate_resg(&json!([])).is_err());
        assert!(checked_rows(&json!([{"id":103},{"id":103}]), "id").is_err());
    }
}
