//! client.rs exposes native commands and sanitized observations for Lux services.
//! Authentication is discovered from process arguments or a client lockfile and
//! remains inside background transports. The dedicated runtime synchronizes room
//! state independently from bounded asynchronous player requests, so a slow
//! history response cannot postpone gameflow changes. Public providers retain
//! their own worker and certificate-validating connection. Update channels wake
//! GPUI directly and never include transport credentials.
//!
//! The selection worker stays separate from history and public statistics. Each
//! write requires an explicitly armed runtime, a current action owned by the
//! local player, and a fresh state validation before submission. Rule revisions
//! invalidate delayed observations, while account changes disarm the worker.
//! Synchronous history helpers below are compiled only for opt-in live protocol
//! probes; production queries use the shared asynchronous history repository.

#[cfg(test)]
use crate::scouting::{self, Profile};
use crate::{
    automation::{self, Decision, Execution, Settings},
    model::{Database, MatchRecord, Room, num},
    scouting::{
        AnalysisSettings, Battle, ClientAccount, HistoryCoverage, PreparedProfile, ProfileTarget,
    },
};
use anyhow::{Context, Result, bail};
use reqwest::{Method, blocking::Client};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc::{self, Sender},
    },
    thread,
    time::{Duration, Instant},
};
use sysinfo::{ProcessRefreshKind, ProcessesToUpdate, System, UpdateKind};

mod runtime;
mod transport;

/// Command identifies data lookups requested from the native user interface.
pub enum Command {
    Detail(u64),
    InspectChampion(u32),
    Matchups {
        enemy: u32,
        position: String,
    },
    Lookup {
        request: u64,
        target: ProfileTarget,
        queue_id: Option<u64>, // Explicit queue filter, absent for all modes.
    },
    ProfileDetail {
        request: u64,
        puuid: String,
        id: u64,
    },
    ReviewLastGame,
    RefreshBattle,
    RetryScout {
        battle: String,
        puuid: String,
    },
    AnalysisScope(bool),  // True uses the current battle's mode in the interface.
    AnalysisCount(usize), // Requested history depth; invalidates cached scouting profiles.
}

/// Update communicates sanitized worker observations back to the GPUI view.
/// ScoutProgress retains a player's loading state while exposing observed games;
/// Scout alone finishes that player's current pipeline or publishes cached data.
pub enum Update {
    Account(ClientAccount),
    ScoutStarted {
        battle: String,
        puuid: String,
    },
    Live {
        connected: bool,
        status: String,
        phase: String,
        room: Option<Room>,
        patch: String,
    },
    History {
        matches: Vec<MatchRecord>,
        captured: String,
        coverage: HistoryCoverage, // Actual source and normalized sample coverage.
    },
    Detail(MatchRecord),
    Public(Database),
    SourceStatus(String), // Automatic public refresh progress and retry explanation.
    Message(String),
    Lookup {
        request: u64,
        result: Result<PreparedProfile, String>,
    },
    LookupProgress {
        request: u64,             // Explicit query owning this progressive observation.
        profile: PreparedProfile, // First usable batch before the requested depth completes.
    },
    ProfileDetail {
        request: u64,
        puuid: String,
        record: MatchRecord,
    },
    Battle(Option<Battle>),
    Scout {
        battle: String,
        puuid: String,
        result: Result<PreparedProfile, String>,
    },
    ScoutProgress {
        battle: String,           // Owning public roster association key.
        puuid: String,            // Queried publicly visible player identity.
        profile: PreparedProfile, // Observed games while continuation remains active.
    },
    Automation {
        status: String,
        action: Option<String>,
    },
}

/// AutomationHandle owns runtime permission separately from persisted selection rules.
#[derive(Clone)]
pub struct AutomationHandle {
    sender: Sender<(u64, Option<Settings>)>, // Versioned changes wake the independent worker.
    armed: Arc<AtomicBool>,                  // False prevents any subsequent request submission.
    revision: Arc<AtomicU64>, // Rule generation invalidates in-flight planning observations.
}

impl AutomationHandle {
    /// apply installs explicit rules and changes this runtime's enable switch.
    /// Disabling takes effect before the worker drains queued rule changes. A
    /// request already submitted to the client cannot be recalled by this switch.
    pub fn apply(&self, settings: Settings, enabled: bool) -> Result<()> {
        let revision = self.revision.fetch_add(1, Ordering::SeqCst) + 1;
        self.armed.store(enabled, Ordering::SeqCst);
        if self.sender.send((revision, Some(settings))).is_err() {
            self.stop();
            bail!("选禁服务已停止");
        }
        Ok(())
    }

    /// stop prevents new writes immediately and wakes the selection worker.
    pub fn stop(&self) {
        self.armed.store(false, Ordering::SeqCst);
        let revision = self.revision.fetch_add(1, Ordering::SeqCst) + 1;
        let _ = self.sender.send((revision, None));
    }

    /// enabled reports the runtime switch displayed by the automation controls.
    pub fn enabled(&self) -> bool {
        self.armed.load(Ordering::SeqCst)
    }
}

/// Auth stores local connection material without implementing debug output.
#[derive(Clone)]
struct Auth {
    port: u16,                // Local HTTPS client listening port.
    token: String,            // Ephemeral local client authentication secret.
    platform: Option<String>, // Optional client-supplied game server code.
}

impl Auth {
    /// from_arguments extracts exact client flags without matching Riot service ports.
    /// Arguments may include surrounding quotes. Invalid or missing port/token
    /// pairs return None, allowing discovery to report a permission problem.
    fn from_arguments(args: &[String]) -> Option<Self> {
        let port = args.iter().find_map(|a| {
            a.trim_matches('"')
                .strip_prefix("--app-port=")
                .and_then(|p| p.parse().ok())
        })?;
        let token = args.iter().find_map(|a| {
            a.trim_matches('"')
                .strip_prefix("--remoting-auth-token=")
                .map(str::to_owned)
        })?;
        if token.is_empty() || port == 0 {
            return None;
        }
        let platform = args.iter().find_map(|a| {
            let arg = a.trim_matches('"');
            arg.strip_prefix("--rso-platform-id=")
                .or_else(|| arg.strip_prefix("--rso_platform_id="))
                .filter(|v| !v.is_empty())
                .map(str::to_owned)
        });
        Some(Self {
            port,
            token,
            platform,
        })
    }

    /// from_lockfile parses the documented five-part local client connection record.
    /// Empty lockfiles are common during transitions and return None. Only the
    /// HTTPS record type is accepted for the local authenticated connection.
    fn from_lockfile(text: &str) -> Option<Self> {
        let p: Vec<&str> = text.trim().split(':').collect();
        if p.len() != 5 || p[4] != "https" || p[3].is_empty() {
            return None;
        }
        let port = p[2].parse().ok()?;
        if port == 0 {
            return None;
        }
        Some(Self {
            port,
            token: p[3].into(),
            platform: None,
        })
    }
}

/// Lcu holds local transport and account context for queries and restricted actions.
struct Lcu {
    auth: Auth,   // Ephemeral endpoint and authentication material.
    http: Client, // Proxy-free loopback-only HTTPS client.
    #[cfg(test)]
    puuid: String, // Local identity used only for history association.
    #[cfg(test)]
    account: Option<ClientAccount>, // Last locally observed account and region.
    #[cfg(test)]
    history_at: Option<Instant>, // Last successful history refresh instant.
}

impl Lcu {
    /// discover locates readable client credentials without requesting elevation.
    /// Process access can fail when League runs at a higher integrity level.
    /// Known installation lockfiles and the optional LUX_LCU_LOCKFILE path are
    /// tried before a descriptive connection state is returned to the interface.
    fn discover() -> Result<Self> {
        let mut system = System::new();
        system.refresh_processes_specifics(
            ProcessesToUpdate::All,
            true,
            ProcessRefreshKind::nothing()
                .with_cmd(UpdateKind::Always)
                .with_exe(UpdateKind::Always),
        );
        let clients: Vec<_> = system
            .processes()
            .values()
            .filter(|p| {
                p.name()
                    .to_string_lossy()
                    .eq_ignore_ascii_case("LeagueClientUx.exe")
            })
            .collect();
        let mut candidates: Vec<PathBuf> = std::env::var_os("LUX_LCU_LOCKFILE")
            .map(PathBuf::from)
            .into_iter()
            .collect();
        let mut auth = None;
        for process in &clients {
            let args: Vec<String> = process
                .cmd()
                .iter()
                .map(|s| s.to_string_lossy().into_owned())
                .collect();
            if let Some(value) = Auth::from_arguments(&args) {
                auth = Some(value);
                break;
            }
            if let Some(parent) = process.exe().and_then(|p| p.parent()) {
                candidates.push(parent.join("lockfile"));
            }
        }
        // Higher-integrity clients may hide their executable path; the user's known installation remains a useful read-only lockfile fallback.
        candidates.extend([
            PathBuf::from("C:/WeGameApps/英雄联盟/LeagueClient/lockfile"),
            PathBuf::from("C:/Riot Games/League of Legends/lockfile"),
        ]);
        if auth.is_none() {
            for path in candidates {
                if let Ok(text) = std::fs::read_to_string(path)
                    && let Some(value) = Auth::from_lockfile(&text)
                {
                    auth = Some(value);
                    break;
                }
            }
        }
        let auth = auth.context(if clients.is_empty() {
            "未检测到可读取的客户端，请启动英雄联盟"
        } else {
            "检测到客户端，但连接参数不可读；若客户端以管理员身份运行，请以相同权限启动 Lux"
        })?;
        let http = Client::builder()
            .no_proxy()
            .danger_accept_invalid_certs(true)
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(4))
            .build()?;
        Ok(Self {
            auth,
            http,
            #[cfg(test)]
            puuid: String::new(),
            #[cfg(test)]
            account: None,
            #[cfg(test)]
            history_at: None,
        })
    }

    /// get performs a local read-only request with per-request basic authentication.
    /// Endpoint paths are internal constants or numeric identifiers. Redirects
    /// are disabled so authentication cannot follow a response to another host.
    fn get(&self, path: &str) -> Result<Value> {
        if !path.starts_with('/') || path.starts_with("//") {
            bail!("Invalid local endpoint");
        }
        let result = self
            .http
            .get(format!("https://127.0.0.1:{}{path}", self.auth.port))
            .basic_auth("riot", Some(&self.auth.token))
            .send()?
            .error_for_status()?
            .json()?;
        Ok(result)
    }

    /// write submits only alias lookups or numeric champion-selection action updates.
    /// Alias POST is a profile search, while PATCH changes a draft action.
    /// Other mutations are rejected here, and redirects remain disabled by Lcu.
    fn write(&self, method: Method, path: &str, body: &Value) -> Result<Value> {
        let action = path.strip_prefix("/lol-champ-select/v1/session/actions/");
        if !((method == Method::POST && path == "/lol-summoner/v1/summoners/aliases")
            || (method == Method::PATCH
                && action
                    .is_some_and(|id| !id.is_empty() && id.chars().all(|c| c.is_ascii_digit()))))
        {
            bail!("Unsupported client write");
        }
        let bytes = self
            .http
            .request(
                method,
                format!("https://127.0.0.1:{}{path}", self.auth.port),
            )
            .basic_auth("riot", Some(&self.auth.token))
            .json(body)
            .send()?
            .error_for_status()?
            .bytes()?;
        if bytes.is_empty() {
            Ok(Value::Null)
        } else {
            Ok(serde_json::from_slice(&bytes)?)
        }
    }

    /// profile queries a returned identity and retains independent rank/history failures.
    /// A Riot ID with a tagline uses the client's alias lookup. Bare legacy names
    /// use a properly encoded query parameter; neither search changes the current
    /// account. Match normalization always receives the resolved target's PUUID.
    /// Count bounds the raw recent records; fewer returned records remain valid
    /// and are labeled separately from the configured request on the interface.
    #[cfg(test)]
    fn profile(&self, target: &ProfileTarget, db: &Database, count: usize) -> Result<Profile> {
        let summoner = match target {
            ProfileTarget::Current => self.get("/lol-summoner/v1/current-summoner")?,
            ProfileTarget::Puuid(id) => {
                if scouting::identity(&json!(id)).is_none() {
                    bail!("无效的玩家标识");
                }
                self.get(&format!("/lol-summoner/v2/summoners/puuid/{id}"))?
            }
            ProfileTarget::Name(query) => {
                let query = query.trim();
                if query.is_empty() || query.chars().count() > 100 {
                    bail!("请输入召唤师名称或名称#编号");
                }
                if let Some((name, tag)) = query.rsplit_once('#') {
                    if name.trim().is_empty() || tag.trim().is_empty() {
                        bail!("Riot ID 需要完整的名称#编号");
                    }
                    let result = self.write(
                        Method::POST,
                        "/lol-summoner/v1/summoners/aliases",
                        &json!([{ "gameName":name.trim(),"tagLine":tag.trim() }]),
                    )?;
                    result
                        .as_array()
                        .and_then(|a| a.first())
                        .cloned()
                        .context("当前大区未找到该 Riot ID")?
                } else {
                    self.http
                        .get(format!(
                            "https://127.0.0.1:{}/lol-summoner/v1/summoners",
                            self.auth.port
                        ))
                        .query(&[("name", query)])
                        .basic_auth("riot", Some(&self.auth.token))
                        .send()?
                        .error_for_status()?
                        .json()?
                }
            }
        };
        let puuid = scouting::identity(&summoner["puuid"]).context("当前大区未找到该玩家")?;
        let history = self.history_for(&puuid, db, count);
        let (matches, history_error) = match history {
            Ok(records) => (records, None),
            // Lookup success does not prove history access; keep that failure distinct from an account with zero games.
            Err(_) => (vec![], Some("客户端暂未提供该玩家战绩，可稍后重试".into())),
        };
        // Rank is optional metadata and cannot invalidate an otherwise resolved player or readable match collection.
        let rank = self
            .get(&format!("/lol-ranked/v1/ranked-stats/{puuid}"))
            .map(|v| scouting::rank_label(&v))
            .unwrap_or_else(|_| "段位暂不可用".into());
        Ok(Profile {
            puuid,
            name: scouting::display_name(&summoner),
            level: num(&summoner["summonerLevel"]),
            rank,
            matches,
            history_error,
            captured: chrono::Local::now().format("%Y-%m-%d %H:%M:%S").to_string(),
            requested: count.clamp(1, 200),
            coverage: Default::default(),
        })
    }

    /// history_for normalizes recent client matches from exactly the requested account.
    /// The inclusive endIndex follows the LCU contract used by LeagueAkari.
    /// Requests are bounded to two hundred records, and actual availability is
    /// determined by the client's response rather than the configured count.
    #[cfg(test)]
    fn history_for(&self, puuid: &str, db: &Database, count: usize) -> Result<Vec<MatchRecord>> {
        let count = count.clamp(1, 200);
        // LCU uses an inclusive index, so fifty requested matches end at forty-nine rather than requesting an extra record.
        let history = self.get(&format!(
            "/lol-match-history/v1/products/lol/{puuid}/matches?begIndex=0&endIndex={}",
            count - 1
        ))?;
        let games = history["games"]["games"]
            .as_array()
            .context("Missing match history games")?;
        Ok(games
            .iter()
            .take(count)
            .filter_map(|g| MatchRecord::from_lcu(g, puuid, &db.champions))
            .collect())
    }

    /// review_last loads the latest non-custom match as explicitly historical scouting.
    /// It resolves the logged-in account before selecting a match. A missing
    /// complete roster returns an error rather than fabricating ten participants.
    #[cfg(test)]
    fn review_last(&mut self, db: &Database) -> Result<Battle> {
        let matches = self.history(db)?;
        let record = matches
            .iter()
            .find(|m| !scouting::is_custom(m.queue_id) && m.duration >= 180)
            .context("没有可复盘的近期对局")?;
        let game = self.get(&format!("/lol-match-history/v1/games/{}", record.id))?;
        Battle::finished(&game, &self.puuid, db).context("本场完整玩家列表暂不可用")
    }

    /// observe_account reads current identity and refreshes region after account changes.
    /// Region metadata is optional and remains visibly unknown if unavailable.
    /// Changing identity invalidates history timing before the next request, so a
    /// newly observed account does not inherit the previous account's refresh delay.
    #[cfg(test)]
    fn observe_account(&mut self) -> Result<ClientAccount> {
        let summoner = self.get("/lol-summoner/v1/current-summoner")?;
        let puuid = scouting::identity(&summoner["puuid"]).context("Missing local identity")?;
        if self.account.as_ref().is_none_or(|old| old.puuid != puuid) {
            self.puuid = puuid.clone();
            self.history_at = None;
            self.account = None;
        }
        // Region is connection metadata rather than a guessed location; unavailable metadata stays unknown until another connection is discovered.
        let region = self
            .account
            .as_ref()
            .map(|a| a.region.clone())
            .unwrap_or_else(|| {
                self.get("/riotclient/region-locale")
                    .ok()
                    .and_then(|v| v["region"].as_str().map(str::to_owned))
                    .unwrap_or_else(|| "大区未知".into())
            });
        let region = if let Some(platform) = &self.auth.platform {
            // Tencent's region value identifies the provider; the client-supplied platform code distinguishes the actual server without guessing its name.
            if region.ends_with(&format!(" / {platform}")) {
                region
            } else {
                format!("{region} / {platform}")
            }
        } else {
            region
        };
        let account = ClientAccount {
            puuid,
            name: scouting::display_name(&summoner),
            region,
        };
        self.account = Some(account.clone());
        Ok(account)
    }

    /// history refreshes the local identity before associating returned combat records.
    /// Each response is filtered through participant identities. A failed refresh
    /// leaves the last published history visible under its earlier timestamp.
    #[cfg(test)]
    fn history(&mut self, db: &Database) -> Result<Vec<MatchRecord>> {
        let summoner = self.get("/lol-summoner/v1/current-summoner")?;
        self.puuid = summoner["puuid"]
            .as_str()
            .filter(|s| !s.is_empty())
            .context("Missing local identity")?
            .into();
        let matches = self.history_for(&self.puuid, db, 20)?;
        self.history_at = Some(Instant::now());
        Ok(matches)
    }
}

/// start launches independent session, history, public-source, and selection services.
/// The returned receiver wakes GPUI when an observation arrives. Dropping the
/// command sender closes the background runtime and cancels outstanding reads;
/// automatic selection remains governed by its separately controlled handle.
pub fn start(
    db: Database,
    settings: Settings,
    analysis: AnalysisSettings,
) -> (
    Sender<Command>,
    async_channel::Receiver<Update>,
    AutomationHandle,
) {
    runtime::start(db, settings, analysis)
}

/// start_automation starts a worker that never waits for public or player statistics.
/// The returned handle begins disarmed even when persisted pick rules exist.
/// Dropping its sender terminates the worker after the current local request;
/// applying rules resets execution attempts and saves only those rule preferences.
///
/// Rule revisions are checked before a local action is submitted. Runtime stop
/// takes effect immediately through the atomic switch even if queued preference
/// messages remain unread; an already submitted HTTP request cannot be recalled.
fn start_automation(
    sender: async_channel::Sender<Update>,
    mut settings: Settings,
    names: BTreeMap<u32, String>,
) -> AutomationHandle {
    let (commands, receiver) = mpsc::channel::<(u64, Option<Settings>)>();
    let armed = Arc::new(AtomicBool::new(false));
    let switch = armed.clone();
    let revision = Arc::new(AtomicU64::new(0));
    let generation = revision.clone();
    thread::Builder::new()
        .name("lux-selection".into())
        .spawn(move || {
            let mut client = None;
            let mut state = Execution::default();
            let mut last = String::new();
            let mut applied_revision = 0;
            loop {
                let timeout = if switch.load(Ordering::SeqCst) {
                    Duration::from_millis(800)
                } else {
                    Duration::from_secs(60)
                };
                match receiver.recv_timeout(timeout) {
                    Err(mpsc::RecvTimeoutError::Disconnected) => break,
                    Ok((version, Some(next))) => {
                        applied_revision = version;
                        settings = next;
                        state = Execution::default();
                        if settings.save().is_err() {
                            let _ = sender.send_blocking(Update::Message(
                                "选禁规则已应用，但保存失败；重启后可能丢失".into(),
                            ));
                        }
                    }
                    Ok((version, None)) => {
                        applied_revision = version;
                        state = Execution::default();
                    }
                    Err(mpsc::RecvTimeoutError::Timeout) => {}
                }
                let (status, action) = if !switch.load(Ordering::SeqCst) {
                    ("自动选禁已关闭".into(), None)
                } else if generation.load(Ordering::SeqCst) != applied_revision {
                    ("正在应用新规则".into(), None)
                } else {
                    if client.is_none() {
                        client = Lcu::discover().ok();
                    }
                    match &client {
                        Some(lcu) => {
                            match automation_tick(
                                lcu,
                                &settings,
                                &switch,
                                (&generation, applied_revision),
                                &mut state,
                                &names,
                            ) {
                                Ok(result) => result,
                                Err(_) => {
                                    client = None;
                                    ("自动选禁等待客户端连接".into(), None)
                                }
                            }
                        }
                        None => ("自动选禁等待同权限客户端".into(), None),
                    }
                };
                if status != last || action.is_some() {
                    let _ = sender.send_blocking(Update::Automation {
                        status: status.clone(),
                        action,
                    });
                    last = status;
                }
            }
        })
        .expect("Unable to start selection worker");
    AutomationHandle {
        sender: commands,
        armed,
        revision,
    }
}

/// automation_tick validates timing, availability, ownership, and runtime permission before writing.
/// Delay and attempted operations survive polling within selection, while leaving
/// selection resets that state. After a candidate's delay expires, another fresh
/// session must produce the same plan and the runtime switch must still be on.
/// The attempt is recorded before network submission because a timeout can hide
/// a successful server-side action; automatic retries would then overwrite state.
fn automation_tick(
    client: &Lcu,
    settings: &Settings,
    armed: &AtomicBool,
    revision: (&AtomicU64, u64),
    state: &mut Execution,
    names: &BTreeMap<u32, String>,
) -> Result<(String, Option<String>)> {
    if client.get("/lol-gameflow/v1/gameflow-phase")? != "ChampSelect" {
        *state = Execution::default();
        return Ok(("自动选禁已开启 · 等待选人".into(), None));
    }
    let mut session = client.get("/lol-champ-select/v1/session")?;
    // Some sessions omit queueId; gameflow supplies its actual queue so random-selection custom modes cannot fall through as drafts.
    if num(&session["queueId"]) == 0 {
        session["queueId"] =
            client.get("/lol-gameflow/v1/session")?["gameData"]["queue"]["id"].clone();
    }
    let pool = |path: &str| {
        client
            .get(path)
            .ok()
            .and_then(|v| serde_json::from_value::<Vec<u32>>(v).ok())
    };
    let picks = pool("/lol-champ-select/v1/pickable-champion-ids");
    let bans = if settings.auto_ban {
        pool("/lol-champ-select/v1/bannable-champion-ids")
    } else {
        None
    };
    let disabled = pool("/lol-champ-select/v1/disabled-champion-ids");
    let decision = automation::decide(
        settings,
        &session,
        picks.as_deref(),
        bans.as_deref(),
        disabled.as_deref(),
        state.own_pick,
    );
    let plan = match decision {
        Decision::Wait(reason) => return Ok((reason.into(), None)),
        Decision::Act(plan) => plan,
    };
    let name = names
        .get(&plan.champion)
        .cloned()
        .unwrap_or_else(|| format!("英雄 {}", plan.champion));
    if !state.ready(&plan, settings.delay_seconds, Instant::now()) {
        return Ok((format!("等待规则延时／客户端回执 · {name}"), None));
    }
    let mut fresh = client.get("/lol-champ-select/v1/session")?;
    if num(&fresh["queueId"]) == 0 {
        fresh["queueId"] = session["queueId"].clone();
    }
    if automation::decide(
        settings,
        &fresh,
        picks.as_deref(),
        bans.as_deref(),
        disabled.as_deref(),
        state.own_pick,
    ) != Decision::Act(plan.clone())
        || client.get("/lol-gameflow/v1/gameflow-phase")? != "ChampSelect"
        || !armed.load(Ordering::SeqCst)
        || revision.0.load(Ordering::SeqCst) != revision.1
    {
        return Ok(("选禁状态已变化，等待重新判断".into(), None));
    }
    state.attempted(plan.clone());
    let result = client.write(
        Method::PATCH,
        &format!("/lol-champ-select/v1/session/actions/{}", plan.id),
        &json!({"championId":plan.champion,"completed":plan.complete,"type":plan.kind.protocol()}),
    );
    let message = if result.is_ok() {
        state.succeeded(&plan);
        format!(
            "已提交{}：{name}",
            if plan.kind == automation::ActionKind::Ban {
                "禁用"
            } else if plan.complete {
                "锁定"
            } else {
                "预选"
            }
        )
    } else {
        format!("{name} 请求未确认，已停止重复提交；可手动操作或重新应用规则")
    };
    Ok((message.clone(), Some(message)))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// live_automatic_startup verifies account and public data arrive without manual commands.
    /// This opt-in check leaves automation disabled and does not create rooms or
    /// change game state. Its report stores counts and booleans without identities.
    #[test]
    #[ignore = "Requires an authorized live client and public provider network access"]
    fn live_automatic_startup() -> Result<()> {
        let (commands, updates, automation) = start(
            Database::bundled(),
            Settings::default(),
            AnalysisSettings::default(),
        );
        assert!(!automation.enabled());
        let deadline = Instant::now() + Duration::from_secs(100);
        let mut connected = false;
        let mut history_count = 0;
        let mut public_refreshed = false;
        let mut heroes = 0;
        while Instant::now() < deadline {
            match updates.try_recv() {
                Ok(Update::Live {
                    connected: value, ..
                }) => connected = value,
                Ok(Update::History { matches, .. }) => history_count = matches.len(),
                Ok(Update::Public(db)) => heroes = db.champions.len(),
                Ok(Update::SourceStatus(status)) => {
                    public_refreshed = status.starts_with("自动更新完成")
                }
                _ => std::thread::sleep(Duration::from_millis(50)),
            }
            if connected && history_count > 0 && public_refreshed {
                break;
            }
        }
        // No command is submitted: successful observations must come from startup discovery and the independent automatic provider schedule.
        drop(commands);
        assert!(!automation.enabled());
        std::fs::create_dir_all("artifacts")?;
        std::fs::write(
            "artifacts/live-automatic-startup.json",
            serde_json::to_vec_pretty(
                &json!({"connected":connected,"historyRecords":history_count,"publicRefreshed":public_refreshed,"catalogHeroes":heroes,"manualCommands":0,"automationEnabled":false,"identitiesStored":false}),
            )?,
        )?;
        assert!(connected, "Automatic client connection failed");
        assert!(history_count > 0, "Automatic history loading failed");
        assert!(
            public_refreshed,
            "Automatic provider refresh did not complete"
        );
        assert!(heroes > 100);
        Ok(())
    }

    /// live_analysis_counts verifies that configured history depths reach the real client.
    /// The same account must retain its newest match across each requested range.
    /// Only sample counts and booleans are written into the verification artifact.
    #[test]
    #[ignore = "Requires a user-authorized live League client"]
    fn live_analysis_counts() -> Result<()> {
        let mut client = Lcu::discover()?;
        let db = Database::bundled();
        let battle = client.review_last(&db)?;
        let other = battle
            .players
            .iter()
            .find(|p| !p.local && p.puuid.is_some())
            .context("No public other player")?;
        let mut rows = vec![];
        for (label, target) in [
            ("current", ProfileTarget::Current),
            ("other", ProfileTarget::Puuid(other.puuid.clone().unwrap())),
        ] {
            let mut newest = None;
            let mut sizes = vec![];
            for count in [20, 50, 100, 200] {
                let profile = client.profile(&target, &db, count)?;
                assert!(profile.history_error.is_none());
                assert!(!profile.matches.is_empty());
                assert!(profile.matches.len() <= count);
                assert_eq!(profile.requested, count);
                let id = profile.matches.first().map(|m| m.id);
                if let Some(previous) = newest {
                    assert_eq!(id, Some(previous));
                }
                newest = id;
                sizes.push(profile.matches.len());
                rows.push(json!({"account":label,"requested":count,"returned":profile.matches.len(),"usable":profile.summary().games}));
            }
            if label == "other" {
                assert!(
                    sizes[1] > sizes[0],
                    "Other player's configured depth did not expand history"
                );
            }
        }
        std::fs::create_dir_all("artifacts")?;
        std::fs::write(
            "artifacts/live-analysis-counts.json",
            serde_json::to_vec_pretty(
                &json!({"success":true,"ranges":rows,"identitiesStored":false}),
            )?,
        )?;
        Ok(())
    }

    /// retry_targets_one_current_visible_player_without_clearing_other_work protects independent retries.
    /// Hidden identities, bot participants, and stale game requests enqueue nothing;
    /// a valid retry invalidates only its own cache and deduplicates queued work.
    /// live_scoped_scouting checks current account metadata and mode-scoped real histories.
    /// This opt-in check reads an existing client without changing rooms or game
    /// state. Its artifact contains only scope counts and metadata availability.
    #[test]
    #[ignore = "Requires a user-authorized live League client"]
    fn live_scoped_scouting() -> Result<()> {
        let mut client = Lcu::discover()?;
        let account = client.observe_account()?;
        assert_eq!(account.puuid, client.puuid);
        assert!(!account.name.is_empty());
        let db = Database::bundled();
        let battle = client.review_last(&db)?;
        let mut rows = Vec::new();
        for player in battle
            .players
            .iter()
            .filter(|p| p.puuid.is_some() && !p.bot)
            .take(2)
        {
            let id = player.puuid.as_ref().unwrap();
            let profile = client.profile(&ProfileTarget::Puuid(id.clone()), &db, 50)?;
            let prepared = crate::scouting::PreparedProfile::new(profile);
            let scope = prepared.summary_for(Some(battle.mode));
            assert_eq!(scope.games, prepared.recent_in(Some(battle.mode)).count());
            assert!(scope.games <= prepared.summary.games);
            assert_eq!(
                scope.heroes.iter().map(|h| h.games).sum::<usize>(),
                scope.games
            );
            rows.push(json!({"returned":prepared.matches.len(),"allUsable":prepared.summary.games,"sameModeUsable":scope.games}));
        }
        assert_eq!(rows.len(), 2);
        std::fs::create_dir_all("artifacts")?;
        std::fs::write(
            "artifacts/live-scoped-scouting.json",
            serde_json::to_vec_pretty(
                &json!({"success":true,"accountResolved":true,"regionAvailable":account.region!="大区未知","profiles":rows,"identitiesStored":false}),
            )?,
        )?;
        Ok(())
    }

    /// live_profiles_and_review verifies real account lookup and ten-player scouting reads.
    /// This opt-in check requires an already running same-permission League client.
    /// Its report stores counts and booleans instead of account names or identifiers.
    #[test]
    #[ignore = "Requires a user-authorized live League client"]
    fn live_profiles_and_review() -> Result<()> {
        let mut client = Lcu::discover()?;
        let db = Database::bundled();
        let own = client.profile(&ProfileTarget::Current, &db, 20)?;
        let alias = client.profile(&ProfileTarget::Name(own.name.clone()), &db, 20)?;
        assert_eq!(alias.puuid, own.puuid);
        assert!(!own.matches.is_empty());
        let battle = client.review_last(&db)?;
        assert_eq!(battle.players.len(), 10);
        assert!(battle.review);
        let mut readable = 0;
        let mut complete = 0;
        for player in &battle.players {
            if let Some(id) = &player.puuid {
                let profile = client.profile(&ProfileTarget::Puuid(id.clone()), &db, 20)?;
                assert_eq!(profile.puuid, *id);
                if profile.history_error.is_none() {
                    readable += 1;
                }
                if let Some(record) = profile.matches.first() {
                    let raw = client.get(&format!("/lol-match-history/v1/games/{}", record.id))?;
                    let detail = MatchRecord::from_lcu(&raw, id, &db.champions)
                        .context("Queried player missing from detail")?;
                    if detail.players.len() == 10 {
                        complete += 1;
                    }
                }
            }
        }
        assert!(readable >= 2);
        assert!(complete >= 2);
        let report = json!({"success":true,"currentGames":own.matches.len(),"riotIdLookupMatches":true,"reviewPlayers":battle.players.len(),"readableProfiles":readable,"tenPlayerDetails":complete,"identitiesStored":false});
        std::fs::create_dir_all("artifacts")?;
        std::fs::write(
            "artifacts/live-feature-queries.json",
            serde_json::to_vec_pretty(&report)?,
        )?;
        Ok(())
    }

    /// live_custom_auto_hover verifies production planning and PATCH transport in an isolated room.
    /// This explicit check requires the user-authorized LuxTest custom selection
    /// containing only the logged-in account. It preselects without completing
    /// the action, leaving room cleanup to the surrounding integration harness.
    #[test]
    #[ignore = "Requires an isolated user-authorized LuxTest custom selection"]
    fn live_custom_auto_hover() -> Result<()> {
        let client = Lcu::discover()?;
        let lobby = client.get("/lol-lobby/v2/lobby")?;
        assert_eq!(lobby["gameConfig"]["customLobbyName"], "LuxTest");
        assert_eq!(lobby["gameConfig"]["isCustom"], true);
        assert_eq!(
            lobby["members"]
                .as_array()
                .context("No lobby members")?
                .len(),
            1
        );
        assert_eq!(lobby["members"][0]["puuid"], lobby["localMember"]["puuid"]);
        let settings = Settings {
            picks: vec![99, 103],
            lock_pick: false,
            delay_seconds: 0,
            ..Settings::default()
        };
        let armed = AtomicBool::new(true);
        let revision = AtomicU64::new(1);
        let mut state = Execution::default();
        let (status, action) = automation_tick(
            &client,
            &settings,
            &armed,
            (&revision, 1),
            &mut state,
            &BTreeMap::new(),
        )?;
        assert!(action.is_some(), "{status}");
        let session = client.get("/lol-champ-select/v1/session")?;
        let local = session["localPlayerCellId"].as_i64().unwrap_or(-1);
        let action = session["actions"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(Value::as_array)
            .flatten()
            .find(|a| a["actorCellId"].as_i64() == Some(local) && a["type"] == "pick")
            .context("No local pick")?;
        assert_eq!(action["completed"], false);
        assert!(
            settings
                .picks
                .contains(&(num(&action["championId"]) as u32))
        );
        std::fs::write(
            "artifacts/live-feature-auto-hover.json",
            serde_json::to_vec_pretty(
                &json!({"success":true,"champion":action["championId"],"completed":false,"productionPlannerAndTransport":true}),
            )?,
        )?;
        Ok(())
    }

    /// authentication_parsers_keep_riot_and_league_ports_separate protects loopback discovery.
    /// Only exact League flags identify its endpoint; empty transition lockfiles
    /// must fail instead of producing an invalid default connection.
    #[test]
    fn authentication_parsers_keep_riot_and_league_ports_separate() {
        for flag in ["--rso_platform_id=HN1", "--rso-platform-id=HN1"] {
            let auth = Auth::from_arguments(&[
                "--app-port=2222".into(),
                "--remoting-auth-token=test".into(),
                flag.into(),
            ])
            .unwrap();
            assert_eq!(auth.platform.as_deref(), Some("HN1"));
        }

        let args = vec![
            "--riotclient-app-port=1111".into(),
            "--app-port=2222".into(),
            "--remoting-auth-token=example-test-token".into(),
        ];
        assert_eq!(Auth::from_arguments(&args).unwrap().port, 2222);
        assert!(Auth::from_lockfile("").is_none());
        assert!(Auth::from_lockfile("LeagueClient:1:2:test:http").is_none());
        assert_eq!(
            Auth::from_lockfile("LeagueClient:1:2222:test:https")
                .unwrap()
                .port,
            2222
        );
    }
}
