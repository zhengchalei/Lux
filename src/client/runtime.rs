//! runtime.rs coordinates independent local sessions and bounded player reads.
//! A two-thread Tokio runtime owns asynchronous HTTP and WebSocket work, while
//! small bridges receive existing synchronous UI commands and public-provider
//! events. Neither player histories nor public requests run inside a live-state
//! snapshot. The initial snapshot and later socket changes publish the public
//! roster, and a timed HTTP refresh remains available when subscriptions fail.
//!
//! Every read carries a session generation, with additional roster or lookup
//! generations where the same connection can host changing views. Replacing an
//! account cancels outstanding work before the new identity is published.
//! Replacing a roster cancels only scouting, preserving independent searches.
//! Admission allows four profile requests, with three available to background
//! scouting and one reserved for an explicit search. Successful
//! profiles are retained briefly under server, identity, depth, and queue keys.

use super::{
    Auth, AutomationHandle, Command, Lcu, Update, start_automation,
    transport::{AsyncLcu, Snapshot, SocketEvent, describe_read_failure, retry_read},
};
use crate::{
    automation::Settings,
    history::HistoryRepository,
    model::{Database, MatchRecord, num},
    scouting::{
        self, AnalysisSettings, Battle, ClientAccount, PreparedProfile, Profile, ProfileTarget,
    },
    sources::{self, Event as SourceEvent, Request as SourceRequest},
};
use anyhow::{Context, Result};
use std::{
    collections::BTreeMap,
    sync::Arc,
    thread,
    time::{Duration, Instant},
};
use tokio::{
    sync::{Semaphore, mpsc},
    task::{AbortHandle, JoinHandle, JoinSet},
    time::Instant as Deadline,
};

// Profile admission and short-lived reuse bound work while independent session snapshots remain responsive to event changes.
const PROFILE_CONCURRENCY: usize = 4;
const BACKGROUND_PROFILE_CONCURRENCY: usize = PROFILE_CONCURRENCY - 1;
const SNAPSHOT_MIN_INTERVAL: Duration = Duration::from_millis(500);
const PROFILE_CACHE_LIMIT: usize = 40;
const PROFILE_CACHE_AGE: Duration = Duration::from_secs(120);

// Profile contexts identify the failed loading stage; their fixed labels can be displayed without copying request URLs or player identifiers.
const PROFILE_RESOLVE_STAGE: ProfileReadStage = ProfileReadStage("玩家资料读取");
const PROFILE_INITIAL_STAGE: ProfileReadStage = ProfileReadStage("首批战绩读取");
const PROFILE_COMPLETE_STAGE: ProfileReadStage = ProfileReadStage("继续加载战绩");

/// ProfileReadStage attaches a fixed public stage label to internal errors.
#[derive(Clone, Copy, Debug)]
struct ProfileReadStage(&'static str);

impl std::fmt::Display for ProfileReadStage {
    /// fmt writes only the fixed stage label into error context.
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(self.0)
    }
}

/// profile_failure reports an observed stage and sanitized transport error category.
/// Internal messages, URLs, queried identities, and response bodies remain inside
/// the error chain. Only the fixed stage label and classified status reach GPUI.
fn profile_failure(error: &anyhow::Error) -> String {
    let stage = error
        .downcast_ref::<ProfileReadStage>()
        .map_or("战绩读取", |stage| stage.0);
    format!(
        "{stage}失败（{}），可单独重试",
        describe_read_failure(error)
    )
}

/// retain_partial_profile records a later-stage failure beside already observed matches.
/// The caller must have successfully obtained and published the first batch.
/// Its actual source, sample counts, capture time, and observations remain
/// unchanged, while the new note names the failed continuation and retained
/// coverage instead of turning a readable player into a failed profile.
///
/// Existing fallback or scan-limit context is retained ahead of the new message.
/// No missing matches, victories, or contribution denominators are invented,
/// and ordinary loading completion can publish this partial result as successful.
fn retain_partial_profile(profile: &mut Profile, error: &anyhow::Error) {
    let retained = format!(
        "保留已读 {} / {} 场；{}",
        profile.coverage.fetched,
        profile.requested,
        profile_failure(error)
    );
    profile.coverage.note = Some(match profile.coverage.note.take() {
        Some(previous) if !previous.is_empty() => format!("{previous}；{retained}"),
        _ => retained,
    });
}

/// ProfileAdmission reserves one total request slot for explicit user searches.
/// Background pipelines acquire their smaller budget first, preventing queued
/// scouting requests from occupying the total budget while waiting for admission.
#[derive(Clone)]
struct ProfileAdmission {
    total: Arc<Semaphore>,      // Maximum concurrent player request pipelines.
    background: Arc<Semaphore>, // Scouts and overview share three background slots.
}

/// ProfilePermit retains both admission budgets until its player pipeline finishes.
struct ProfilePermit {
    _total: tokio::sync::OwnedSemaphorePermit, // Total player pipeline admission slot.
    _background: Option<tokio::sync::OwnedSemaphorePermit>, // Optional background pipeline admission slot.
}

impl ProfileAdmission {
    /// new creates four total slots with three available to background requests.
    fn new() -> Self {
        Self {
            total: Arc::new(Semaphore::new(PROFILE_CONCURRENCY)),
            background: Arc::new(Semaphore::new(BACKGROUND_PROFILE_CONCURRENCY)),
        }
    }

    /// acquire reserves admission for an explicit or background player pipeline.
    /// Background work acquires its three-slot budget before requesting one of
    /// the four total slots. Pending background tasks therefore cannot enter the
    /// total semaphore's FIFO queue ahead of an explicit lookup when three
    /// background pipelines are already running.
    ///
    /// The returned permit owns both acquisitions until completion or cancellation.
    /// Canceling while waiting releases any partially acquired budget, and a
    /// closed semaphore returns None without starting the profile request.
    async fn acquire(&self, background: bool) -> Option<ProfilePermit> {
        let background = if background {
            Some(self.background.clone().acquire_owned().await.ok()?)
        } else {
            None
        };
        let total = self.total.clone().acquire_owned().await.ok()?;
        Some(ProfilePermit {
            _total: total,
            _background: background,
        })
    }
}

/// Input brings independent work back to the sole session state owner.
enum Input {
    Command(Command),    // User request forwarded by its synchronous channel bridge.
    Public(SourceEvent), // Public observations retain their separate source worker.
    Closed,              // All user command senders have been released.
    Discovered(Result<Auth, String>), // Process discovery result without logging credentials.
    Snapshot {
        session: u64,                          // Owning authenticated session generation.
        result: Box<Result<Snapshot, String>>, // Normalized observations or visible failure.
    },
    Socket {
        session: u64,       // Owning authenticated session generation.
        event: SocketEvent, // Local subscription status or endpoint change.
    },
    Completed {
        session: u64,           // Owning authenticated session generation.
        result: Box<Completed>, // Normalized observations or visible failure.
    },
}

/// Completed identifies the view and generation owning a finished asynchronous read.
enum Completed {
    Lookup {
        generation: u64,                         // Owning query or roster generation.
        request: u64,                            // Original native UI request identifier.
        result: Result<PreparedProfile, String>, // Normalized observations or visible failure.
        complete: bool,                          // Final progressive player loading stage.
    },
    Scout {
        generation: u64,                         // Owning query or roster generation.
        revision: u64,                           // Current per-player retry revision.
        battle: String,                          // Owning public roster association key.
        puuid: String,                           // Queried publicly visible player identity.
        result: Result<PreparedProfile, String>, // Normalized observations or visible failure.
        complete: bool,                          // Final progressive player loading stage.
    },
    Overview {
        generation: u64,                 // Owning query or roster generation.
        result: Result<Profile, String>, // Normalized observations or visible failure.
        complete: bool,                  // Final progressive player loading stage.
    },
    Detail {
        request: Option<(u64, u64)>, // Original native UI request identifier.
        puuid: String,               // Queried publicly visible player identity.
        result: Result<MatchRecord, String>, // Normalized observations or visible failure.
    },
    Review {
        generation: u64,                // Owning query or roster generation.
        result: Result<Battle, String>, // Normalized observations or visible failure.
    },
}

/// Generations rejects results from replaced sessions, rosters, or explicit searches.
#[derive(Default)]
struct Generations {
    session: u64, // Active credential and account observation generation.
    roster: u64,  // Current public roster and analysis preference generation.
    lookup: u64,  // Most recent explicit search and associated detail generation.
}

impl Generations {
    /// accepts reports whether a task still belongs to the current session.
    fn accepts(&self, session: u64) -> bool {
        self.session == session
    }

    /// replace_session invalidates every observation derived from previous account state.
    fn replace_session(&mut self) {
        self.session = self.session.wrapping_add(1);
        self.roster = self.roster.wrapping_add(1);
        self.lookup = self.lookup.wrapping_add(1);
    }
}

/// CacheKey keeps server, requested depth, and exact queue inside profile identity.
type CacheKey = (String, String, usize, Option<u64>);

/// Coordinator owns mutation of session state while requests progress independently.
struct Coordinator {
    db: Database, // Shared public catalog snapshot used when normalizing matches.
    analysis: AnalysisSettings, // Persisted depth and current-queue analysis preference.
    repository: HistoryRepository, // Cloneable history cache and normalized data provider.
    client: Option<AsyncLcu>, // Current ephemeral loopback transport, absent while discovering.
    account: Option<ClientAccount>, // Latest accepted local account and region observation.
    battle: Option<Battle>, // Public roster or explicitly labeled historical review.
    versions: Generations, // Coordinated session, roster, and lookup ownership counters.
    inputs: mpsc::UnboundedReceiver<Input>, // Serialized results from independent asynchronous tasks.
    input: mpsc::UnboundedSender<Input>,    // Sender cloned into tasks and bridge workers.
    updates: async_channel::Sender<Update>, // Awaitable native presentation event channel.
    public: std::sync::mpsc::Sender<SourceRequest>, // Public requests never contain local credentials.
    automation: AutomationHandle, // Separate runtime permission stopped on account changes.
    admission: ProfileAdmission,  // Four concurrent profile pipelines share this request budget.
    jobs: JoinSet<()>,            // Owned asynchronous work aborted when the session is replaced.
    scouts: BTreeMap<String, (u64, AbortHandle)>, // Roster-only tasks canceled without interrupting explicit queries.
    lookup: Option<AbortHandle>, // Superseded explicit query canceled before replacement starts.
    socket: Option<JoinHandle<()>>, // Current local subscription canceled with its credentials.
    cache: BTreeMap<CacheKey, (Instant, Profile)>, // Short-lived successful player snapshots.
    pending_review: bool,        // Explicit startup review waits for its first verified account.
    last_lookup: Option<(u64, ProfileTarget, Option<u64>)>, // Search restarted when configured depth changes.
    discovering: bool, // Prevents repeated process scans while one scan is pending.
    polling: bool,     // Allows one coherent HTTP snapshot at a time.
    dirty: bool,       // A socket event arrived while the current snapshot was running.
    socket_connected: bool, // Chooses the timed recovery refresh cadence.
    last_snapshot: Option<Deadline>, // Latest started snapshot in this authenticated connection.
    next_snapshot: Deadline, // Next HTTP recovery snapshot or merged event refresh.
    next_overview: Deadline, // Local player's independently scheduled history refresh.
    overview_generation: u64, // Latest depth or account context owning automatic history.
    overview: Option<AbortHandle>, // Automatic history task canceled on requested depth changes.
    overview_loading: bool, // Prevents overlapping automatic local history reads.
    fingerprint: String, // Last visible phase, room, and patch observation.
    roster_fingerprint: String, // Last visible full roster observation sent to GPUI.
    matchup_generation: u64, // Current public matchup query and account context.
}

/// start creates independent bridges, selection execution, and the asynchronous runtime.
/// UI callers keep their synchronous command sender and receive an awaitable
/// event channel. Closing that command sender terminates coordination and drops
/// pending network futures, without waiting for a player request to finish.
///
/// The coordinator owns account association and request admission; bridge workers
/// transfer messages without consulting that state. A failed runtime startup
/// reports one visible error while the separately armed selection handle keeps
/// its explicit off-by-default permission rule.
pub(super) fn start(
    db: Database,
    settings: Settings,
    analysis: AnalysisSettings,
) -> (
    std::sync::mpsc::Sender<Command>,
    async_channel::Receiver<Update>,
    AutomationHandle,
) {
    let (commands, command_receiver) = std::sync::mpsc::channel();
    let (updates, receiver) = async_channel::unbounded();
    let automation = start_automation(
        updates.clone(),
        settings,
        db.champions
            .iter()
            .map(|(id, c)| (*id, c.name.clone()))
            .collect(),
    );
    let selection = automation.clone();
    let (input, inputs) = mpsc::unbounded_channel();
    let command_input = input.clone();
    // The bridge blocks only on UI command arrival; forwarding never waits behind history or public-source HTTP requests.
    thread::Builder::new()
        .name("lux-commands".into())
        .spawn(move || {
            while let Ok(command) = command_receiver.recv() {
                if command_input.send(Input::Command(command)).is_err() {
                    return;
                }
            }
            let _ = command_input.send(Input::Closed);
        })
        .expect("Unable to start Lux command bridge");
    let (public, public_events) = sources::start(db.clone());
    let public_input = input.clone();
    // The public worker owns its source cache; this bridge forwards observations without borrowing the local account or authentication state.
    thread::Builder::new()
        .name("lux-source-events".into())
        .spawn(move || {
            while let Ok(event) = public_events.recv() {
                if public_input.send(Input::Public(event)).is_err() {
                    break;
                }
            }
        })
        .expect("Unable to start Lux source bridge");
    thread::Builder::new()
        .name("lux-runtime".into())
        .spawn(move || {
            let runtime = tokio::runtime::Builder::new_multi_thread()
                .worker_threads(2)
                .max_blocking_threads(2)
                .enable_all()
                .build();
            let result = runtime.map_err(anyhow::Error::from).and_then(|runtime| {
                runtime.block_on(async {
                    let repository = HistoryRepository::new()?;
                    let mut coordinator = Coordinator {
                        db,
                        analysis,
                        repository,
                        client: None,
                        account: None,
                        battle: None,
                        versions: Generations::default(),
                        inputs,
                        input,
                        updates: updates.clone(),
                        public,
                        automation: selection,
                        admission: ProfileAdmission::new(),
                        jobs: JoinSet::new(),
                        scouts: BTreeMap::new(),
                        lookup: None,
                        socket: None,
                        cache: BTreeMap::new(),
                        last_lookup: None,
                        pending_review: false,
                        discovering: false,
                        polling: false,
                        dirty: false,
                        socket_connected: false,
                        last_snapshot: None,
                        next_snapshot: Deadline::now(),
                        next_overview: Deadline::now(),
                        overview_loading: false,
                        overview_generation: 0,
                        overview: None,
                        fingerprint: String::new(),
                        roster_fingerprint: String::new(),
                        matchup_generation: 0,
                    };
                    coordinator.run().await;
                    Ok(())
                })
            });
            if result.is_err() {
                let _ = updates.try_send(Update::Message("后台数据服务启动失败".into()));
            }
        })
        .expect("Unable to start Lux async runtime");
    (commands, receiver, automation)
}

impl Coordinator {
    /// emit publishes one sanitized observation to the continuously drained UI channel.
    fn emit(&self, update: Update) {
        let _ = self.updates.try_send(update);
    }

    /// run owns state transitions while snapshots and player requests progress independently.
    /// Its selector receives commands, source observations, and finished reads;
    /// none of those branches waits for a player's network response. Timed
    /// refreshes recover missing subscriptions without delaying incoming commands.
    ///
    /// Completed tasks leave the task set continuously. Shutdown cancels and
    /// drains pending reads before dropping the socket, credentials, and source
    /// handles, so task-owned request permits are released before exit returns.
    async fn run(&mut self) {
        loop {
            if self.updates.is_closed() {
                break;
            }
            let deadline =
                if self.client.is_some() && !self.overview_loading && self.account.is_some() {
                    self.next_snapshot.min(self.next_overview)
                } else {
                    self.next_snapshot
                };
            tokio::select! {
                input = self.inputs.recv() => {
                    match input {
                        Some(Input::Closed) | None => break,
                        Some(input) => self.handle(input),
                    }
                }
                _ = tokio::time::sleep_until(deadline) => self.schedule(),
                Some(_) = self.jobs.join_next(), if !self.jobs.is_empty() => {}
            }
        }
        // Cancel reads before dropping current transports; otherwise an old completion could outlive the session that authorized its association.
        self.jobs.abort_all();
        while self.jobs.join_next().await.is_some() {}
        if let Some(socket) = self.socket.take() {
            socket.abort();
            let _ = socket.await;
        }
        self.automation.stop();
    }

    /// schedule starts due discovery, snapshots, and overview work without awaiting HTTP.
    /// Discovery has one outstanding scan and a five-second retry deadline.
    /// Connected sessions allow one coherent HTTP snapshot while automatic
    /// history uses its own loading flag and sixty-second refresh deadline.
    ///
    /// Each operation captures its session generation before spawning. Neither
    /// the profile admission semaphore nor a slow source request can prevent
    /// starting the next mandatory room snapshot when its deadline arrives.
    fn schedule(&mut self) {
        let now = Deadline::now();
        if self.client.is_none() {
            self.next_snapshot = now + Duration::from_secs(5);
            if !self.discovering {
                self.discovering = true;
                let input = self.input.clone();
                self.jobs.spawn(async move {
                    // Process scanning and the legacy blocking-client construction stay outside asynchronous executor threads and are dropped on that blocking worker.
                    let auth =
                        tokio::task::spawn_blocking(|| Lcu::discover().map(|c| c.auth.clone()))
                            .await
                            .map_err(|_| "客户端发现服务已停止".into())
                            .and_then(|result| result.map_err(|e| e.to_string()));
                    let _ = input.send(Input::Discovered(auth));
                });
            }
            return;
        }
        if now >= self.next_snapshot
            && !self.polling
            && let Some(last) = self.last_snapshot
            && now < last + SNAPSHOT_MIN_INTERVAL
        {
            self.next_snapshot = last + SNAPSHOT_MIN_INTERVAL;
        }
        if now >= self.next_snapshot && !self.polling {
            self.last_snapshot = Some(now);
            self.polling = true;
            self.dirty = false;
            self.next_snapshot = now + Duration::from_secs(60);
            let client = self.client.clone().expect("Connected client above");
            let input = self.input.clone();
            let session = self.versions.session;
            self.jobs.spawn(async move {
                let result = client
                    .snapshot()
                    .await
                    .map_err(|_| "客户端状态暂不可读".into());
                let _ = input.send(Input::Snapshot {
                    session,
                    result: Box::new(result),
                });
            });
        }
        if now >= self.next_overview && !self.overview_loading && self.account.is_some() {
            self.overview_loading = true;
            self.next_overview = now + Duration::from_secs(60);
            self.spawn_profile(
                ProfileTarget::Current,
                None,
                Destination::Overview {
                    generation: self.overview_generation,
                },
            );
        }
    }

    /// handle serializes observations and rejects obsolete tasks before publication.
    /// Discovery installs a transport, snapshots associate its current account,
    /// and socket events request a merged refresh rather than mutating room
    /// fields independently. Completed player work must match the active session
    /// before its finer view checks run.
    ///
    /// An account or connection replacement advances ownership counters first.
    /// Consequently already-queued results cannot restore an older room even
    /// when aborting its task happens after that result was delivered.
    fn handle(&mut self, input: Input) {
        match input {
            Input::Command(command) => self.command(command),
            Input::Public(event) => self.public_event(event),
            Input::Discovered(result) => {
                self.discovering = false;
                match result
                    .and_then(|auth| AsyncLcu::new(auth).map_err(|_| "客户端连接初始化失败".into()))
                {
                    Ok(client) => {
                        self.client = Some(client);
                        self.last_snapshot = None;
                        self.next_snapshot = Deadline::now();
                        self.start_socket();
                    }
                    Err(status) => {
                        if self.fingerprint != status {
                            self.emit(Update::Live {
                                connected: false,
                                status: status.clone(),
                                phase: String::new(),
                                room: None,
                                patch: String::new(),
                            });
                            self.fingerprint = status;
                        }
                    }
                }
            }
            Input::Snapshot { session, result } if self.versions.accepts(session) => {
                self.polling = false;
                match *result {
                    Ok(snapshot) => self.accept_snapshot(snapshot),
                    Err(_) => self.disconnect(),
                }
            }
            Input::Socket { session, event } if self.versions.accepts(session) => match event {
                SocketEvent::Connected(value) => {
                    if self.socket_connected != value {
                        self.fingerprint.clear();
                    }
                    self.socket_connected = value;
                    self.request_snapshot();
                }
                SocketEvent::Changed(uri) => {
                    if uri.starts_with("/lol-match-history/") {
                        self.next_overview = Deadline::now();
                    }
                    self.request_snapshot();
                }
            },
            Input::Completed { session, result } if self.versions.accepts(session) => {
                self.completed(*result)
            }
            _ => {}
        }
    }

    /// request_snapshot merges bursts while preserving changes during an active read.
    /// Idle sessions bring the next snapshot deadline forward by at most one
    /// hundred milliseconds, combining related gameflow and champ-select events.
    /// A running snapshot instead records a dirty flag for its completion path.
    ///
    /// The dirty flag prevents a change received during HTTP reads from being
    /// lost behind their older response. It schedules one follow-up snapshot,
    /// without creating overlapping readers for every countdown event.
    fn request_snapshot(&mut self) {
        if self.polling {
            self.dirty = true;
        } else {
            self.next_snapshot = self.next_snapshot.min(self.event_snapshot_deadline());
        }
    }

    /// event_snapshot_deadline combines event coalescing with the connection throttle.
    /// The first connection snapshot is scheduled separately and remains immediate.
    /// Later event-driven reads cannot start within five hundred milliseconds of
    /// the previous start, including a dirty event received during that read.
    fn event_snapshot_deadline(&self) -> Deadline {
        let merged = Deadline::now() + Duration::from_millis(100);
        self.last_snapshot
            .map_or(merged, |last| merged.max(last + SNAPSHOT_MIN_INTERVAL))
    }

    /// start_socket binds event subscription lifetime to the current session generation.
    /// A previous subscription is canceled before installing its replacement.
    /// Events retain that captured generation and cannot wake a different account.
    fn start_socket(&mut self) {
        if let Some(previous) = self.socket.take() {
            previous.abort();
        }
        let Some(client) = self.client.clone() else {
            return;
        };
        let input = self.input.clone();
        let session = self.versions.session;
        self.socket = Some(tokio::spawn(async move {
            client
                .watch_events(move |event| {
                    let _ = input.send(Input::Socket { session, event });
                })
                .await;
        }));
    }

    /// disconnect invalidates reads and clears live associations before rediscovery.
    /// Advancing generations precedes task cancellation, so queued results also
    /// become obsolete. Transport and account handles are removed together;
    /// a query during recovery cannot combine new credentials with an old server.
    ///
    /// Previously displayed history keeps its captured context, while the live
    /// room and roster are cleared. Selection is disarmed before the next
    /// discovery attempt, preventing recovered credentials from resuming an
    /// action planned for the disconnected session.
    fn disconnect(&mut self) {
        self.versions.replace_session();
        self.jobs.abort_all();
        self.scouts.clear();
        self.lookup = None;
        if let Some(socket) = self.socket.take() {
            socket.abort();
        }
        self.automation.stop();
        self.pending_review = false;
        self.client = None;
        self.account = None;
        self.last_snapshot = None;
        self.polling = false;
        self.overview_loading = false;
        self.socket_connected = false;
        self.battle = None;
        self.cache.clear();
        self.fingerprint.clear();
        self.roster_fingerprint.clear();
        self.next_snapshot = Deadline::now() + Duration::from_secs(5);
        self.emit(Update::Live {
            connected: false,
            status: "客户端连接中断，正在自动重连".into(),
            phase: String::new(),
            room: None,
            patch: String::new(),
        });
        self.emit(Update::Battle(None));
    }

    /// accept_snapshot publishes account state before its matching room and players.
    /// Account replacement aborts previous jobs, invalidates public matchup
    /// context, and restarts the subscription under a new generation. GPUI sees
    /// the account event first so it can clear old identity associations.
    ///
    /// Roster-only changes preserve independent searches and reuse fresh player
    /// snapshots where their queue and depth match. Labeled historical reviews
    /// survive idle observations, while a live roster replaces them immediately.
    /// A dirty snapshot schedules another read before normal recovery polling.
    fn accept_snapshot(&mut self, snapshot: Snapshot) {
        let changed_account = self
            .account
            .as_ref()
            .is_some_and(|old| !old.same_account(&snapshot.account));
        if changed_account {
            self.versions.replace_session();
            self.jobs.abort_all();
            self.scouts.clear();
            self.lookup = None;
            self.last_lookup = None;
            self.pending_review = false;
            self.cache.clear();
            self.battle = None;
            self.overview_loading = false;
            self.roster_fingerprint.clear();
            self.fingerprint.clear();
            self.matchup_generation = self.matchup_generation.wrapping_add(1);
            self.db.matchups.clear();
            self.automation.stop();
            self.next_overview = Deadline::now();
            self.start_socket();
        }
        if self.account.as_ref() != Some(&snapshot.account) {
            self.emit(Update::Account(snapshot.account.clone()));
            self.account = Some(snapshot.account);
            self.next_overview = Deadline::now();
        }
        let fingerprint = format!("{}|{:?}|{}", snapshot.phase, snapshot.room, snapshot.patch);
        if fingerprint != self.fingerprint {
            self.emit(Update::Live {
                connected: true,
                status: if self.socket_connected {
                    "客户端已连接 · 实时同步"
                } else {
                    "客户端已连接 · 自动恢复订阅"
                }
                .into(),
                phase: snapshot.phase,
                room: snapshot.room,
                patch: snapshot.patch,
            });
            self.fingerprint = fingerprint;
        }
        // A labeled historical review survives an idle snapshot, but live public participants always replace that historical association.
        if snapshot.battle.is_some() || !self.battle.as_ref().is_some_and(|b| b.review) {
            self.publish_battle(snapshot.battle, false);
        }
        // Startup review requests wait for a verified account; consume the intent before spawning so later recovery snapshots cannot submit it twice.
        if std::mem::take(&mut self.pending_review) {
            self.review();
        }
        self.next_snapshot = if self.dirty {
            self.event_snapshot_deadline()
        } else {
            Deadline::now()
                + if self.socket_connected {
                    Duration::from_secs(15)
                } else {
                    Duration::from_secs(5)
                }
        };
    }

    /// publish_battle updates visible champions while reloading changed player scopes.
    /// Its display fingerprint includes the complete public roster, whereas
    /// query ownership includes game identity, exact queue, and visible players.
    /// Hovering another champion therefore updates the screen without refetching
    /// unchanged player histories.
    ///
    /// A changed ownership key advances the roster generation and cancels its
    /// outstanding tasks before new participants are scheduled. Forced refresh
    /// follows the same order but preserves the UI's existing successful rows
    /// until individual replacement profiles arrive.
    fn publish_battle(&mut self, battle: Option<Battle>, force: bool) {
        let fingerprint = format!("{battle:?}");
        let previous_key = self.battle.as_ref().map(roster_key);
        let new_key = battle.as_ref().map(roster_key);
        let reload = force || previous_key != new_key;
        self.battle = battle;
        if force || fingerprint != self.roster_fingerprint {
            self.emit(Update::Battle(self.battle.clone()));
            self.roster_fingerprint = fingerprint;
        }
        if !reload {
            return;
        }
        self.versions.roster = self.versions.roster.wrapping_add(1);
        for (_, (_, task)) in std::mem::take(&mut self.scouts) {
            task.abort();
        }
        let Some(battle) = self.battle.clone() else {
            return;
        };
        for player in &battle.players {
            if let Some(puuid) = &player.puuid
                && !player.bot
            {
                self.scout(&battle, puuid, force);
            }
        }
    }

    /// scout reuses matching snapshots or schedules one publicly visible participant.
    /// Cache identity includes the observed server, requested depth, and exact
    /// queue preference; a fresh entry can therefore populate only an equivalent
    /// analysis scope. Forced retry removes that entry before starting work.
    ///
    /// Each participant also carries a retry revision within the roster.
    /// Canceling its previous task and advancing that revision rejects an older
    /// completion already queued at the coordinator, without clearing other
    /// players or interrupting the separately queried profile.
    fn scout(&mut self, battle: &Battle, puuid: &str, force: bool) {
        let queue = self
            .analysis
            .same_mode
            .then_some(battle.queue_id)
            .filter(|q| *q != 0);
        let key = self.cache_key(puuid, queue);
        if !force
            && let Some((_, profile)) = self
                .cache
                .get(&key)
                .filter(|(at, _)| at.elapsed() < PROFILE_CACHE_AGE)
        {
            self.emit(Update::Scout {
                battle: battle.key.clone(),
                puuid: puuid.into(),
                result: Ok(PreparedProfile::new(profile.clone())),
            });
            return;
        }
        self.cache.remove(&key);
        let revision = self.scouts.get(puuid).map_or(0, |(revision, task)| {
            task.abort();
            revision.wrapping_add(1)
        });
        self.emit(Update::ScoutStarted {
            battle: battle.key.clone(),
            puuid: puuid.into(),
        });
        self.spawn_profile(
            ProfileTarget::Puuid(puuid.into()),
            queue,
            Destination::Scout {
                generation: self.versions.roster,
                revision,
                battle: battle.key.clone(),
                puuid: puuid.into(),
            },
        );
    }

    /// cache_key scopes reusable player data to its observed server and query.
    fn cache_key(&self, puuid: &str, queue: Option<u64>) -> CacheKey {
        (
            self.account
                .as_ref()
                .map(|a| a.region.clone())
                .unwrap_or_default(),
            puuid.into(),
            self.analysis.bounded_count(),
            queue,
        )
    }

    /// spawn_profile queues progressive history outside live session synchronization.
    /// The semaphore limits whole pipelines, and aborting the returned task drops
    /// its active HTTP futures. Every stage returns through the coordinator so
    /// account and view generations are checked before presentation or caching.
    /// First-batch summaries appear before requested-depth completion, and only
    /// twenty missing complete rosters are enriched automatically; timelines
    /// remain outside the automatic profile loading budget. Later-depth failures
    /// retain a successful first batch and attach a sanitized stage note rather
    /// than discarding the readable player or inventing the missing records.
    fn spawn_profile(
        &mut self,
        target: ProfileTarget,
        queue: Option<u64>,
        destination: Destination,
    ) {
        let (Some(client), Some(account)) = (self.client.clone(), self.account.clone()) else {
            return;
        };
        let repository = self.repository.clone();
        let catalog = self.db.champions.clone();
        let count = self.analysis.bounded_count();
        let session = self.versions.session;
        let input = self.input.clone();
        let admission = self.admission.clone();
        let owner = destination.clone();
        let background = !matches!(destination, Destination::Lookup { .. });
        let task = self.jobs.spawn(async move {
            let Some(_permit) = admission.acquire(background).await else {
                return;
            };
            let context = client.context(&account);
            let result = async {
                let summoner = client
                    .resolve(&target)
                    .await
                    .context(PROFILE_RESOLVE_STAGE)?;
                let puuid =
                    scouting::identity(&summoner["puuid"]).context(PROFILE_RESOLVE_STAGE)?;
                let rank_path = format!("/lol-ranked/v1/ranked-stats/{puuid}");
                let (history, rank) = tokio::join!(
                    retry_read(|| repository.load(
                        &context,
                        &puuid,
                        count.min(20),
                        queue,
                        &catalog
                    )),
                    client.get(&rank_path),
                );
                let history = history.context(PROFILE_INITIAL_STAGE)?;
                let mut profile = Profile {
                    puuid,
                    name: scouting::display_name(&summoner),
                    level: num(&summoner["summonerLevel"]),
                    rank: rank
                        .map(|v| scouting::rank_label(&v))
                        .unwrap_or_else(|error| {
                            format!("段位暂不可用（{}）", describe_read_failure(&error))
                        }),
                    matches: history.matches,
                    history_error: None,
                    captured: chrono::Local::now().format("%Y-%m-%d %H:%M:%S").to_string(),
                    requested: count,
                    coverage: history.coverage,
                };
                profile.coverage.requested = count;
                let _ = input.send(Input::Completed {
                    session,
                    result: Box::new(owner.completed(Ok(profile.clone()), false)),
                });
                if count > 20 {
                    // First-batch rows remain visible while the repository scans toward the configured depth; its cache reuses already observed matches.
                    let history = retry_read(|| {
                        repository.load(&context, &profile.puuid, count, queue, &catalog)
                    })
                    .await
                    .context(PROFILE_COMPLETE_STAGE);
                    match history {
                        Ok(history) => {
                            profile.matches = history.matches;
                            profile.coverage = history.coverage;
                        }
                        Err(error) => {
                            // A continuation failure cannot invalidate observed first-batch games; return their unchanged sample with a stage-specific partial explanation.
                            retain_partial_profile(&mut profile, &error);
                            return Ok::<_, anyhow::Error>(profile);
                        }
                    }
                    let _ = input.send(Input::Completed {
                        session,
                        result: Box::new(owner.completed(Ok(profile.clone()), false)),
                    });
                }
                repository
                    .enrich(&context, &mut profile, &catalog, 20)
                    .await;
                Ok::<_, anyhow::Error>(profile)
            }
            .await
            .map_err(|error| profile_failure(&error));
            let _ = input.send(Input::Completed {
                session,
                result: Box::new(owner.completed(result, true)),
            });
        });
        match destination {
            Destination::Lookup { .. } => self.lookup = Some(task),
            Destination::Scout {
                revision, puuid, ..
            } => {
                self.scouts.insert(puuid, (revision, task));
            }
            Destination::Overview { .. } => self.overview = Some(task),
        }
    }

    /// command applies user intent without awaiting player or public provider requests.
    /// Searches, details, and review reads are spawned with their owner identity;
    /// public hero requests retain the independent provider worker. Refresh and
    /// retry operate on the currently visible roster rather than row indexes.
    ///
    /// Analysis changes persist in arrival order and replace affected player
    /// pipelines. Changing depth also restarts automatic history and the last
    /// explicit search, while their generations prevent a smaller older result
    /// from overwriting the newly requested sample.
    fn command(&mut self, command: Command) {
        match command {
            Command::Lookup {
                request,
                target,
                queue_id,
            } => self.lookup(request, target, queue_id),
            Command::InspectChampion(id) => {
                let _ = self.public.send(SourceRequest::Champion(id));
            }
            Command::Matchups { enemy, position } => {
                self.matchup_generation = self.matchup_generation.wrapping_add(1);
                let _ = self.public.send(SourceRequest::Matchups {
                    enemy,
                    position,
                    generation: self.matchup_generation,
                });
            }
            Command::RefreshBattle => {
                self.cache.clear();
                self.publish_battle(self.battle.clone(), true);
                self.request_snapshot();
            }
            Command::RetryScout { battle, puuid } => {
                if let Some(current) = self.battle.clone().filter(|b| {
                    b.key == battle
                        && b.players
                            .iter()
                            .any(|p| !p.bot && p.puuid.as_deref() == Some(&puuid))
                }) {
                    self.scout(&current, &puuid, true);
                }
            }
            Command::AnalysisScope(value) => {
                self.analysis.same_mode = value;
                self.save_analysis();
                self.publish_battle(self.battle.clone(), true);
            }
            Command::AnalysisCount(count) => {
                self.analysis.match_count = count.clamp(1, 200);
                self.save_analysis();
                self.cache.clear();
                self.publish_battle(self.battle.clone(), true);
                self.overview_generation = self.overview_generation.wrapping_add(1);
                if let Some(previous) = self.overview.take() {
                    previous.abort();
                }
                self.overview_loading = false;
                self.next_overview = Deadline::now();
                if let Some((request, target, queue)) = self.last_lookup.clone() {
                    self.lookup(request, target, queue);
                }
            }
            Command::Detail(id) => self.detail(None, String::new(), id),
            Command::ProfileDetail { request, puuid, id } => self.detail(Some(request), puuid, id),
            Command::ReviewLastGame => self.review(),
        }
    }

    /// lookup replaces the previous search before scheduling its explicit scope.
    /// Its generation changes even when a caller reuses the same visible request
    /// identifier after adjusting analysis depth. The old task is canceled before
    /// the replacement enters the shared profile admission queue.
    ///
    /// A query needs both an established transport and a verified account.
    /// Recovery therefore reports a waiting result rather than mixing partially
    /// rediscovered credentials with previous region metadata. Its target is
    /// retained so later depth changes can restart the same search.
    fn lookup(&mut self, request: u64, target: ProfileTarget, queue: Option<u64>) {
        self.versions.lookup = self.versions.lookup.wrapping_add(1);
        if let Some(previous) = self.lookup.take() {
            previous.abort();
        }
        self.last_lookup = Some((request, target.clone(), queue));
        if self.client.is_none() || self.account.is_none() {
            self.emit(Update::Lookup {
                request,
                result: Err("正在等待客户端自动连接，连接后可查询战绩".into()),
            });
            return;
        }
        self.spawn_profile(
            target,
            queue,
            Destination::Lookup {
                generation: self.versions.lookup,
                request,
            },
        );
    }

    /// save_analysis persists the latest settings in command arrival order.
    /// The small local write runs on the background coordinator, never GPUI.
    /// Serial writes prevent older preference snapshots from overwriting newer ones.
    fn save_analysis(&mut self) {
        if self.analysis.save().is_err() {
            self.emit(Update::Message("统计设置已应用，但保存失败".into()));
        }
    }

    /// detail reads one match with the explicit player perspective and query generation.
    /// Main-history requests use the observed local account; searched-player
    /// requests keep their own PUUID and UI request identifier. The repository
    /// shares the complete match while normalizing each requested perspective.
    ///
    /// A detail task captures the session and lookup generations before starting.
    /// The completion dispatcher verifies both before publishing searched-player
    /// records or errors, so a delayed previous selection cannot replace the
    /// current query after its target or requested depth changes.
    fn detail(&mut self, request: Option<u64>, puuid: String, id: u64) {
        let (Some(client), Some(account)) = (self.client.clone(), self.account.clone()) else {
            return;
        };
        let owner = if puuid.is_empty() {
            account.puuid.clone()
        } else {
            puuid
        };
        let repository = self.repository.clone();
        let context = client.context(&account);
        let catalog = self.db.champions.clone();
        let input = self.input.clone();
        let session = self.versions.session;
        let generation = self.versions.lookup;
        self.jobs.spawn(async move {
            let result = repository
                .detail(&context, id, &owner, &catalog)
                .await
                .map_err(|_| "完整战绩详情暂不可用，保留当前摘要".into());
            // Detail completion includes the lookup generation through its original request; session changes reject every result before it reaches this check.
            let request = request.map(|request| (request, generation));
            let _ = input.send(Input::Completed {
                session,
                result: Box::new(Completed::Detail {
                    request,
                    puuid: owner,
                    result,
                }),
            });
        });
    }

    /// review derives a labeled roster from the latest playable finished match.
    /// It queries the observed local account and requires real participant
    /// identities from complete LCU match data before orienting the two teams.
    /// Missing history or a missing roster remains an explicit failure.
    ///
    /// The task carries its original session and roster generations. A live
    /// game appearing while the history request is pending wins that transition;
    /// a delayed historical review cannot replace current public participants.
    /// Successful idle reviews retain their separate label and association key.
    fn review(&mut self) {
        let (Some(client), Some(account)) = (self.client.clone(), self.account.clone()) else {
            self.pending_review = true;
            self.emit(Update::Message(
                "正在等待客户端自动连接，就绪后自动加载最近一局".into(),
            ));
            return;
        };
        let repository = self.repository.clone();
        let context = client.context(&account);
        let db = self.db.clone();
        let input = self.input.clone();
        let session = self.versions.session;
        let generation = self.versions.roster;
        self.jobs.spawn(async move {
            let result = async {
                let history = repository
                    .load(&context, &account.puuid, 20, None, &db.champions)
                    .await?;
                let record = history
                    .matches
                    .iter()
                    .find(|m| scouting::usable_match(m, None))
                    .context("没有可复盘的近期对局")?;
                let raw = client
                    .get(&format!("/lol-match-history/v1/games/{}", record.id))
                    .await?;
                Battle::finished(&raw, &account.puuid, &db).context("本场完整玩家列表暂不可用")
            }
            .await
            .map_err(|_| "最近一局复盘暂不可用".into());
            let _ = input.send(Input::Completed {
                session,
                result: Box::new(Completed::Review { generation, result }),
            });
        });
    }

    /// completed accepts matching view generations and publishes progressive results.
    /// Explicit searches require their lookup generation; scouting also requires
    /// the roster key, visible participant, and current per-player retry revision.
    /// Only final successful profiles enter the short-lived cache, whose oldest
    /// entry is evicted before its fixed capacity is exceeded.
    ///
    /// Progressive scouting preserves a readable row through ScoutProgress while
    /// leaving its continuation loading. Only Scout represents a final stage,
    /// so first batches cannot increment the interface's completed-player count.
    /// Overview stages retain their own depth generation,
    /// while detail and review completions follow their original view ownership.
    /// These checks apply before either successful data or failure messages
    /// can alter the current screen.
    fn completed(&mut self, result: Completed) {
        match result {
            Completed::Lookup {
                generation,
                request,
                result,
                complete,
            } if generation == self.versions.lookup => {
                if complete {
                    self.emit(Update::Lookup { request, result });
                } else if let Ok(profile) = result {
                    self.emit(Update::LookupProgress { request, profile });
                }
            }
            Completed::Scout {
                generation,
                revision,
                battle,
                puuid,
                result,
                complete,
            } if generation == self.versions.roster
                && self
                    .scouts
                    .get(&puuid)
                    .is_some_and(|(current, _)| *current == revision)
                && self.battle.as_ref().is_some_and(|b| {
                    b.key == battle && b.players.iter().any(|p| p.puuid.as_deref() == Some(&puuid))
                }) =>
            {
                if complete
                    && let Ok(profile) = &result
                    && profile.history_error.is_none()
                {
                    if self.cache.len() >= PROFILE_CACHE_LIMIT
                        && let Some(key) = self
                            .cache
                            .iter()
                            .min_by_key(|(_, (at, _))| *at)
                            .map(|(key, _)| key.clone())
                    {
                        self.cache.remove(&key);
                    }
                    let queue = self
                        .analysis
                        .same_mode
                        .then_some(self.battle.as_ref().unwrap().queue_id)
                        .filter(|q| *q != 0);
                    self.cache.insert(
                        self.cache_key(&puuid, queue),
                        (Instant::now(), profile.profile.clone()),
                    );
                }
                if complete {
                    self.emit(Update::Scout {
                        battle,
                        puuid,
                        result,
                    });
                } else if let Ok(profile) = result {
                    // Keep progressive observations distinct from final outcomes so visible first batches never finish the participant's loading counter.
                    self.emit(Update::ScoutProgress {
                        battle,
                        puuid,
                        profile,
                    });
                }
            }
            Completed::Overview {
                generation,
                result,
                complete,
            } if generation == self.overview_generation => {
                if complete {
                    self.overview_loading = false;
                }
                if let Ok(profile) = result {
                    self.emit(Update::History {
                        matches: profile.matches,
                        captured: profile.captured,
                        coverage: profile.coverage,
                    });
                } else {
                    self.emit(Update::Message(
                        "客户端已连接，近期战绩暂不可读，将自动重试".into(),
                    ));
                }
            }
            Completed::Detail {
                request,
                puuid,
                result,
            } if request.is_none_or(|(_, generation)| generation == self.versions.lookup) => {
                match result {
                    Ok(record) => match request {
                        Some((request, generation)) if generation == self.versions.lookup => self
                            .emit(Update::ProfileDetail {
                                request,
                                puuid,
                                record,
                            }),
                        Some(_) => {}
                        None => self.emit(Update::Detail(record)),
                    },
                    Err(message) => self.emit(Update::Message(message)),
                }
            }
            Completed::Review { generation, result }
                if generation == self.versions.roster
                    && !self.battle.as_ref().is_some_and(|b| !b.review) =>
            {
                match result {
                    Ok(battle) => self.publish_battle(Some(battle), true),
                    Err(message) => self.emit(Update::Message(message)),
                }
            }
            _ => {}
        }
    }

    /// public_event applies public observations without replacing private client associations.
    /// Periodic catalogs replace champion observations and freshness labels while
    /// retaining the separately requested matchup rows. Public failures report
    /// their existing source message without changing account, room, or history.
    ///
    /// Matchup replies must retain the latest query generation, which also
    /// changes when the account changes. A late response for another enemy,
    /// position, or account context therefore cannot restore recommendations
    /// cleared by a newer user selection.
    fn public_event(&mut self, event: SourceEvent) {
        match event {
            SourceEvent::Public(public) => {
                self.db.champions = public.champions;
                self.db.public_updated = public.public_updated;
                self.emit(Update::Public(self.db.clone()));
            }
            SourceEvent::Status(status) => self.emit(Update::SourceStatus(status)),
            SourceEvent::Message(message) => self.emit(Update::Message(message)),
            SourceEvent::Matchups { generation, result }
                if generation == self.matchup_generation =>
            {
                match result {
                    Ok(rows) => {
                        let count = rows.len();
                        self.db.matchups = rows;
                        self.emit(Update::Public(self.db.clone()));
                        self.emit(Update::Message(format!(
                            "OP.GG 对位已更新 · {count} 组 · 韩服 / 铂金以上"
                        )));
                    }
                    Err(message) => self.emit(Update::Message(message)),
                }
            }
            SourceEvent::Matchups { .. } => {}
        }
    }
}

/// Destination retains ownership of all progressive stages of a player pipeline.
#[derive(Clone)]
enum Destination {
    Lookup {
        generation: u64, // Owning query or roster generation.
        request: u64,    // Original native UI request identifier.
    }, // Current explicit query and its UI request.
    Scout {
        generation: u64, // Owning query or roster generation.
        revision: u64,   // Current per-player retry revision.
        battle: String,  // Owning public roster association key.
        puuid: String,   // Queried publicly visible player identity.
    }, // Current publicly visible roster participant.
    Overview {
        generation: u64, // Owning query or roster generation.
    }, // Automatic history for the current logged-in account.
}

impl Destination {
    /// completed wraps one stage with immutable query or roster ownership.
    /// Player aggregates are prepared on the background task before entering the
    /// update channel, so GPUI receives summaries without scanning combat records.
    fn completed(&self, result: Result<Profile, String>, complete: bool) -> Completed {
        match self {
            Self::Lookup {
                generation,
                request,
            } => Completed::Lookup {
                generation: *generation,
                request: *request,
                result: result.map(PreparedProfile::new),
                complete,
            },
            Self::Scout {
                generation,
                revision,
                battle,
                puuid,
            } => Completed::Scout {
                generation: *generation,
                revision: *revision,
                battle: battle.clone(),
                puuid: puuid.clone(),
                result: result.map(PreparedProfile::new),
                complete,
            },
            Self::Overview { generation } => Completed::Overview {
                generation: *generation,
                result,
                complete,
            },
        }
    }
}

/// roster_key distinguishes query ownership without refetching histories for champion changes.
/// Sorting visible identities makes slot-order changes equivalent while preserving
/// the game and exact queue; hidden identities never become query participants.
fn roster_key(battle: &Battle) -> String {
    let mut players: Vec<_> = battle
        .players
        .iter()
        .filter(|p| !p.bot)
        .filter_map(|p| p.puuid.as_deref())
        .collect();
    players.sort_unstable();
    format!("{}:{}:{}", battle.key, battle.queue_id, players.join(","))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicBool, AtomicU64};

    /// coordinator builds production coordination with disconnected, in-memory test channels.
    /// No process discovery or network task starts until its scheduler is run;
    /// callers can dispatch completed observations without contacting a real client.
    fn coordinator() -> (Coordinator, async_channel::Receiver<Update>) {
        let (updates, receiver) = async_channel::unbounded();
        let (input, inputs) = mpsc::unbounded_channel();
        let (public, _) = std::sync::mpsc::channel();
        let (selection, _) = std::sync::mpsc::channel();
        let coordinator = Coordinator {
            db: Database::default(),
            analysis: AnalysisSettings::default(),
            repository: HistoryRepository::new().unwrap(),
            client: None,
            account: None,
            battle: None,
            versions: Generations::default(),
            inputs,
            input,
            updates,
            public,
            automation: AutomationHandle {
                sender: selection,
                armed: Arc::new(AtomicBool::new(false)),
                revision: Arc::new(AtomicU64::new(0)),
            },
            admission: ProfileAdmission::new(),
            jobs: JoinSet::new(),
            scouts: BTreeMap::new(),
            lookup: None,
            socket: None,
            cache: BTreeMap::new(),
            last_lookup: None,
            pending_review: false,
            discovering: false,
            polling: false,
            dirty: false,
            socket_connected: false,
            last_snapshot: None,
            next_snapshot: Deadline::now() + Duration::from_secs(3600),
            next_overview: Deadline::now() + Duration::from_secs(3600),
            overview_generation: 0,
            overview: None,
            overview_loading: false,
            fingerprint: String::new(),
            roster_fingerprint: String::new(),
            matchup_generation: 0,
        };
        (coordinator, receiver)
    }

    /// scouting_progress_does_not_publish_completion reproduces successive usable batches for one player.
    /// Progress stays distinct from final and cached results, preventing early completion counts while preserving the current roster association.
    #[tokio::test]
    async fn scouting_progress_does_not_publish_completion() {
        let (mut coordinator, updates) = coordinator();
        let battle = Battle {
            key: "review-1".into(),
            label: "Historical review".into(),
            mode: crate::model::Mode::Aram,
            queue_id: 450,
            review: true,
            players: vec![scouting::Combatant {
                puuid: Some("visible-player".into()),
                ..scouting::Combatant::default()
            }],
        };
        coordinator.battle = Some(battle.clone());
        let task = coordinator.jobs.spawn(std::future::pending::<()>());
        coordinator
            .scouts
            .insert("visible-player".into(), (0, task));
        let destination = Destination::Scout {
            generation: 0,
            revision: 0,
            battle: battle.key.clone(),
            puuid: "visible-player".into(),
        };
        let profile = Profile {
            puuid: "visible-player".into(),
            ..Profile::default()
        };
        for _ in 0..2 {
            coordinator.completed(destination.completed(Ok(profile.clone()), false));
            assert!(matches!(
                updates.try_recv(),
                Ok(Update::ScoutProgress { battle, puuid, .. })
                    if battle == "review-1" && puuid == "visible-player"
            ));
            assert!(updates.try_recv().is_err());
            assert!(coordinator.cache.is_empty());
        }
        coordinator.completed(destination.completed(Ok(profile), true));
        assert!(matches!(
            updates.try_recv(),
            Ok(Update::Scout { result: Ok(_), .. })
        ));
        assert_eq!(coordinator.cache.len(), 1);
        coordinator.scout(&battle, "visible-player", false);
        assert!(matches!(
            updates.try_recv(),
            Ok(Update::Scout { result: Ok(_), .. })
        ));
        assert!(updates.try_recv().is_err());
        coordinator.jobs.abort_all();
        while coordinator.jobs.join_next().await.is_some() {}
    }

    /// delayed_superseded_lookup_cannot_replace_newer_player reproduces reversed network completions.
    /// The production result dispatcher accepts the newest request and discards the older delayed result after both tasks have actually completed.
    #[tokio::test]
    async fn delayed_superseded_lookup_cannot_replace_newer_player() {
        let (mut coordinator, updates) = coordinator();
        coordinator.versions.lookup = 2;
        for (generation, delay) in [(1, 30), (2, 5)] {
            let input = coordinator.input.clone();
            coordinator.jobs.spawn(async move {
                tokio::time::sleep(Duration::from_millis(delay)).await;
                let profile = PreparedProfile::new(Profile {
                    puuid: format!("player-{generation}"),
                    ..Profile::default()
                });
                let _ = input.send(Input::Completed {
                    session: 0,
                    result: Box::new(Completed::Lookup {
                        generation,
                        request: generation,
                        result: Ok(profile),
                        complete: true,
                    }),
                });
            });
        }
        for _ in 0..2 {
            let input = tokio::time::timeout(Duration::from_secs(1), coordinator.inputs.recv())
                .await
                .unwrap()
                .unwrap();
            coordinator.handle(input);
        }
        assert!(matches!(
            updates.try_recv(),
            Ok(Update::Lookup { request: 2, .. })
        ));
        assert!(updates.try_recv().is_err());
    }

    /// disconnect_aborts_pending_work_before_new_credentials_are_discovered reproduces an interrupted read.
    /// The coordinator clears the live roster and cancels the real delayed task without publishing its obsolete lookup.
    #[tokio::test]
    async fn disconnect_aborts_pending_work_before_new_credentials_are_discovered() {
        let (mut coordinator, updates) = coordinator();
        let input = coordinator.input.clone();
        let (started, ready) = tokio::sync::oneshot::channel();
        coordinator.jobs.spawn(async move {
            let _ = started.send(());
            tokio::time::sleep(Duration::from_secs(10)).await;
            let _ = input.send(Input::Completed {
                session: 0,
                result: Box::new(Completed::Lookup {
                    generation: 0,
                    request: 1,
                    result: Ok(PreparedProfile::new(Profile::default())),
                    complete: true,
                }),
            });
        });
        ready.await.unwrap();
        coordinator.disconnect();
        let result = tokio::time::timeout(Duration::from_secs(1), coordinator.jobs.join_next())
            .await
            .unwrap()
            .unwrap();
        assert!(result.unwrap_err().is_cancelled());
        assert!(coordinator.client.is_none());
        assert!(coordinator.inputs.try_recv().is_err());
        assert!(matches!(
            updates.try_recv(),
            Ok(Update::Live {
                connected: false,
                ..
            })
        ));
        assert!(matches!(updates.try_recv(), Ok(Update::Battle(None))));
        assert!(updates.try_recv().is_err());
    }

    /// saturated_player_requests_leave_live_state_responsive reproduces four slow admitted pipelines.
    /// A room snapshot reaches the real coordinator while every history slot remains occupied, then shutdown cancels those tasks.
    #[tokio::test]
    async fn saturated_player_requests_leave_live_state_responsive() {
        let (mut coordinator, updates) = coordinator();
        let admission = coordinator.admission.clone();
        let (ready, mut started) = mpsc::unbounded_channel();
        for index in 0..PROFILE_CONCURRENCY {
            let admission = admission.clone();
            let ready = ready.clone();
            coordinator.jobs.spawn(async move {
                let _permit = admission
                    .acquire(index < BACKGROUND_PROFILE_CONCURRENCY)
                    .await
                    .unwrap();
                let _ = ready.send(());
                tokio::time::sleep(Duration::from_secs(10)).await;
            });
        }
        for _ in 0..PROFILE_CONCURRENCY {
            started.recv().await.unwrap();
        }
        assert_eq!(admission.total.available_permits(), 0);
        let input = coordinator.input.clone();
        let task = tokio::spawn(async move {
            coordinator.run().await;
        });
        input
            .send(Input::Snapshot {
                session: 0,
                result: Box::new(Ok(Snapshot {
                    account: ClientAccount {
                        puuid: "local".into(),
                        name: "Local".into(),
                        region: "test".into(),
                    },
                    phase: "Lobby".into(),
                    room: None,
                    patch: "test".into(),
                    battle: None,
                })),
            })
            .unwrap();
        assert!(matches!(
            tokio::time::timeout(Duration::from_secs(1), updates.recv())
                .await
                .unwrap(),
            Ok(Update::Account(_))
        ));
        assert!(
            matches!(tokio::time::timeout(Duration::from_secs(1), updates.recv()).await.unwrap(), Ok(Update::Live { connected: true, phase, .. }) if phase == "Lobby")
        );
        assert_eq!(admission.total.available_permits(), 0);
        input.send(Input::Closed).unwrap();
        task.await.unwrap();
        assert_eq!(admission.total.available_permits(), PROFILE_CONCURRENCY);
    }
    /// queued_background_profiles_leave_the_fourth_slot_for_lookup reproduces a full scouting queue.
    /// An explicit search starts before the fourth background task, and canceling requests returns both admission budgets.
    #[tokio::test]
    async fn queued_background_profiles_leave_the_fourth_slot_for_lookup() {
        let admission = ProfileAdmission::new();
        let (events, mut ready) = mpsc::unbounded_channel();
        let mut jobs = JoinSet::new();
        let mut releases = vec![];
        for _ in 0..BACKGROUND_PROFILE_CONCURRENCY {
            let admission = admission.clone();
            let events = events.clone();
            let (release, wait) = tokio::sync::oneshot::channel();
            releases.push(release);
            jobs.spawn(async move {
                let _permit = admission.acquire(true).await.unwrap();
                let _ = events.send("background");
                let _ = wait.await;
            });
        }
        for _ in 0..BACKGROUND_PROFILE_CONCURRENCY {
            assert_eq!(ready.recv().await, Some("background"));
        }
        assert_eq!(admission.total.available_permits(), 1);
        let queued_admission = admission.clone();
        let queued_events = events.clone();
        jobs.spawn(async move {
            let _permit = queued_admission.acquire(true).await.unwrap();
            let _ = queued_events.send("queued-background");
            std::future::pending::<()>().await;
        });
        tokio::task::yield_now().await;
        let lookup_admission = admission.clone();
        let lookup_events = events.clone();
        let (release_lookup, wait_lookup) = tokio::sync::oneshot::channel();
        jobs.spawn(async move {
            let _permit = lookup_admission.acquire(false).await.unwrap();
            let _ = lookup_events.send("lookup");
            let _ = wait_lookup.await;
        });
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(1), ready.recv())
                .await
                .unwrap(),
            Some("lookup")
        );
        assert_eq!(admission.total.available_permits(), 0);
        release_lookup.send(()).unwrap();
        assert!(
            tokio::time::timeout(Duration::from_millis(30), ready.recv())
                .await
                .is_err()
        );
        releases.pop().unwrap().send(()).unwrap();
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(1), ready.recv())
                .await
                .unwrap(),
            Some("queued-background")
        );
        jobs.abort_all();
        while jobs.join_next().await.is_some() {}
        assert_eq!(admission.total.available_permits(), PROFILE_CONCURRENCY);
        assert_eq!(
            admission.background.available_permits(),
            BACKGROUND_PROFILE_CONCURRENCY
        );
    }

    /// countdown_bursts_and_dirty_reads_preserve_snapshot_spacing reproduces repeated session events.
    /// The real request merger and scheduler refuse a second HTTP read during the five-hundred-millisecond interval.
    #[tokio::test]
    async fn countdown_bursts_and_dirty_reads_preserve_snapshot_spacing() {
        let (mut coordinator, _) = coordinator();
        coordinator.client = Some(
            AsyncLcu::new(Auth {
                port: 1,
                token: "test".into(),
                platform: None,
            })
            .unwrap(),
        );
        let first = Deadline::now();
        coordinator.last_snapshot = Some(first);
        coordinator.next_snapshot = first + Duration::from_secs(15);
        for _ in 0..100 {
            coordinator.request_snapshot();
        }
        assert!(coordinator.next_snapshot >= first + SNAPSHOT_MIN_INTERVAL);
        coordinator.next_snapshot = Deadline::now();
        coordinator.schedule();
        assert!(!coordinator.polling);
        assert!(coordinator.jobs.is_empty());
        assert!(coordinator.next_snapshot >= first + SNAPSHOT_MIN_INTERVAL);
        coordinator.polling = true;
        coordinator.request_snapshot();
        assert!(coordinator.dirty);
        coordinator.polling = false;
        coordinator.accept_snapshot(Snapshot {
            account: ClientAccount {
                puuid: "local".into(),
                name: "Local".into(),
                region: "test".into(),
            },
            phase: "Lobby".into(),
            room: None,
            patch: "test".into(),
            battle: None,
        });
        assert!(coordinator.next_snapshot >= first + SNAPSHOT_MIN_INTERVAL);
        assert!(coordinator.jobs.is_empty());
    }

    /// startup_review_waits_for_account_and_runs_once protects the explicit launch journey.
    /// A disconnected request is queued, one verified account consumes it, and disconnect clears any new waiting intent.
    #[tokio::test]
    async fn startup_review_waits_for_account_and_runs_once() {
        let (mut coordinator, _) = coordinator();
        coordinator.command(Command::ReviewLastGame);
        assert!(coordinator.pending_review);
        coordinator.client = Some(
            AsyncLcu::new(Auth {
                port: 1,
                token: "test".into(),
                platform: None,
            })
            .unwrap(),
        );
        let snapshot = || Snapshot {
            account: ClientAccount {
                puuid: "local".into(),
                name: "Local".into(),
                region: "test".into(),
            },
            phase: "Lobby".into(),
            room: None,
            patch: "test".into(),
            battle: None,
        };
        coordinator.accept_snapshot(snapshot());
        assert!(!coordinator.pending_review);
        assert_eq!(coordinator.jobs.len(), 1);
        coordinator.accept_snapshot(snapshot());
        assert_eq!(coordinator.jobs.len(), 1);
        coordinator.jobs.abort_all();
        while coordinator.jobs.join_next().await.is_some() {}
        coordinator.disconnect();
        coordinator.command(Command::ReviewLastGame);
        assert!(coordinator.pending_review);
        coordinator.disconnect();
        assert!(!coordinator.pending_review);
    }
    /// continuation_failure_retains_observed_games_and_sanitizes_its_stage protects progressive loading.
    /// A later failure keeps the first batch, its provider and counts, and reports partial coverage without leaking request identity.
    #[test]
    fn continuation_failure_retains_observed_games_and_sanitizes_its_stage() {
        let mut profile = Profile {
            matches: vec![MatchRecord {
                id: 11,
                queue_id: 450,
                duration: 1200,
                win: true,
                ..MatchRecord::default()
            }],
            requested: 50,
            coverage: crate::scouting::HistoryCoverage {
                source: "SGP".into(),
                requested: 50,
                fetched: 1,
                scanned: 1,
                valid: 1,
                note: Some("已有来源说明".into()),
                ..crate::scouting::HistoryCoverage::default()
            },
            ..Profile::default()
        };
        let error = anyhow::anyhow!("https://private.invalid/player-secret bearer-token")
            .context(PROFILE_COMPLETE_STAGE);
        retain_partial_profile(&mut profile, &error);
        assert_eq!(profile.matches.len(), 1);
        assert_eq!(profile.matches[0].id, 11);
        assert_eq!(profile.coverage.source, "SGP");
        assert_eq!(profile.coverage.fetched, 1);
        assert_eq!(profile.coverage.valid, 1);
        assert!(profile.history_error.is_none());
        let note = profile.coverage.note.unwrap();
        assert!(note.contains("保留已读 1 / 50 场"));
        assert!(note.contains("已有来源说明"));
        assert!(note.contains("继续加载战绩失败"));
        assert!(!note.contains("private.invalid"));
        assert!(!note.contains("player-secret"));
        assert!(!note.contains("bearer-token"));
        for stage in [
            PROFILE_RESOLVE_STAGE,
            PROFILE_INITIAL_STAGE,
            PROFILE_COMPLETE_STAGE,
        ] {
            let error = anyhow::anyhow!("private request context").context(stage);
            assert!(profile_failure(&error).starts_with(stage.0));
        }
    }
}
