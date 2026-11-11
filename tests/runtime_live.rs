//! runtime_live.rs verifies the production command stream against an authorized client.
//! The opt-in test waits for automatic discovery, requests scoped history through
//! the same service used by GPUI, and records sample coverage without persisting
//! player names, account identifiers, authentication, or raw match responses.
//!
//! Each request uses an explicit identity, and the response must retain its
//! requested queue throughout pagination and fallback. A superseding query is
//! submitted immediately before the retained request, so late messages cannot
//! accidentally become the result under test. The harness only reads game data;
//! it leaves draft automation disabled and never changes the current room.

use anyhow::{Context, Result, bail, ensure};
use lux::{
    automation::Settings,
    client::{self, Command, Update},
    history::{HistoryContext, HistoryRepository},
    model::Database,
    scouting::{AnalysisSettings, ProfileTarget},
};
use serde_json::json;
use std::{
    collections::BTreeSet,
    time::{Duration, Instant},
};

/// runtime_live_scoped_history exercises automatic connection and a current-queue query.
/// The user must have an active readable League client and at least one recent
/// competitive match. The artifact reports observed coverage, including fallback,
/// without assuming the backend always supplies the configured two hundred games.
#[test]
#[ignore = "Requires a user-authorized readable League client and backend network access"]
fn runtime_live_scoped_history() -> Result<()> {
    let settings = AnalysisSettings {
        match_count: 200,
        same_mode: true,
    };
    let (commands, updates, automation) =
        client::start(Database::bundled(), Settings::default(), settings);
    let startup_deadline = Instant::now() + Duration::from_secs(150);
    let mut connected = false;
    let mut connection_status = String::new();
    let mut recent_queue = None;
    let mut startup_count = 0;
    while Instant::now() < startup_deadline {
        match updates.try_recv() {
            Ok(Update::Live {
                connected: value,
                status,
                ..
            }) => {
                connected = value;
                connection_status = status;
            }
            Ok(Update::History { matches, .. }) => {
                startup_count = matches.len();
                recent_queue = matches
                    .iter()
                    .find(|m| m.duration >= 180 && !lux::scouting::is_custom(m.queue_id))
                    .map(|m| m.queue_id);
            }
            _ => std::thread::sleep(Duration::from_millis(20)),
        }
        if connected && recent_queue.is_some() {
            break;
        }
    }
    ensure!(connected, "Automatic production connection failed");
    let queue = recent_queue.context("No usable startup history became available")?;

    // Consecutive request identities replace pending lookup work without allowing the superseded all-queue result to satisfy this scoped query.
    commands.send(Command::Lookup {
        request: 7101,
        target: ProfileTarget::Current,
        queue_id: None,
    })?;
    commands.send(Command::Lookup {
        request: 7102,
        target: ProfileTarget::Current,
        queue_id: Some(queue),
    })?;
    let query_deadline = Instant::now() + Duration::from_secs(180);
    let mut progress_batches = 0;
    let mut result = None;
    while Instant::now() < query_deadline {
        match updates.try_recv() {
            Ok(Update::LookupProgress {
                request: 7102,
                profile,
            }) => {
                ensure!(
                    profile.matches.iter().all(|m| m.queue_id == queue),
                    "Progress scope changed"
                );
                progress_batches += 1;
            }
            Ok(Update::Lookup {
                request: 7102,
                result: value,
            }) => {
                result = Some(value);
                break;
            }
            Ok(Update::Live { status, .. }) => connection_status = status,
            _ => std::thread::sleep(Duration::from_millis(20)),
        }
    }
    let profile = match result.context("Scoped production lookup timed out")? {
        Ok(profile) => profile,
        Err(error) => bail!("Scoped production lookup failed: {error}"),
    };
    ensure!(
        !profile.matches.is_empty(),
        "The observed queue returned no records"
    );
    ensure!(
        profile.matches.iter().all(|m| m.queue_id == queue),
        "Result contains another queue"
    );
    let unique: BTreeSet<_> = profile.matches.iter().map(|m| m.id).collect();
    ensure!(
        unique.len() == profile.matches.len(),
        "Pagination retained duplicate matches"
    );
    ensure!(
        profile.coverage.requested == 200,
        "Configured depth was lost"
    );
    ensure!(
        profile.coverage.queue_id == Some(queue),
        "Coverage lost its requested queue"
    );
    ensure!(
        profile.coverage.valid <= profile.coverage.fetched,
        "Invalid effective sample count"
    );
    ensure!(
        profile.coverage.complete <= profile.coverage.valid,
        "Invalid complete sample count"
    );
    ensure!(
        !automation.enabled(),
        "Read-only query enabled draft automation"
    );
    std::fs::create_dir_all("artifacts")?;
    std::fs::write(
        "artifacts/runtime-live-history.json",
        serde_json::to_vec_pretty(&json!({
            "connected": connected,
            "connectionStatus": connection_status,
            "startupRecords": startup_count,
            "requested": profile.coverage.requested,
            "source": profile.coverage.source,
            "fetched": profile.coverage.fetched,
            "scanned": profile.coverage.scanned,
            "valid": profile.coverage.valid,
            "complete": profile.coverage.complete,
            "queue": queue,
            "note": profile.coverage.note,
            "progressBatches": progress_batches,
            "uniqueMatches": unique.len(),
            "automationEnabled": automation.enabled(),
            "identitiesStored": false
        }))?,
    )?;
    drop(commands);
    Ok(())
}

/// history_live_lcu_fallback verifies the real local fallback without relying on SGP failure.
/// An intentionally unconfigured region prevents remote routing, while the authorized
/// current client's process or lockfile supplies process-local authentication. Exact-queue
/// filtering and fallback provenance must survive the client's smaller history range.
#[test]
#[ignore = "Requires a user-authorized readable local League client"]
fn history_live_lcu_fallback() -> Result<()> {
    let lock_path = std::env::var_os("LUX_LCU_LOCKFILE")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|| "C:/WeGameApps/英雄联盟/LeagueClient/lockfile".into());
    let mut credentials = std::fs::read_to_string(lock_path)
        .ok()
        .and_then(|contents| {
            let fields: Vec<_> = contents.trim().split(':').collect();
            (fields.len() == 5 && fields[4] == "https" && !fields[3].is_empty())
                .then(|| {
                    fields[2]
                        .parse::<u16>()
                        .ok()
                        .map(|port| (port, fields[3].to_owned()))
                })
                .flatten()
        });
    if credentials.is_none() {
        // Some running clients expose usable credentials only through their command line; inspect them inside this elevated test without logging or exporting process arguments.
        use sysinfo::{ProcessRefreshKind, ProcessesToUpdate, System, UpdateKind};
        let mut system = System::new();
        system.refresh_processes_specifics(
            ProcessesToUpdate::All,
            true,
            ProcessRefreshKind::nothing().with_cmd(UpdateKind::Always),
        );
        for process in system.processes().values().filter(|p| {
            p.name()
                .to_string_lossy()
                .eq_ignore_ascii_case("LeagueClientUx.exe")
        }) {
            let args: Vec<_> = process.cmd().iter().map(|v| v.to_string_lossy()).collect();
            let port = args.iter().find_map(|arg| {
                arg.trim_matches('"')
                    .strip_prefix("--app-port=")
                    .and_then(|v| v.parse::<u16>().ok())
            });
            let token = args.iter().find_map(|arg| {
                arg.trim_matches('"')
                    .strip_prefix("--remoting-auth-token=")
                    .filter(|v| !v.is_empty())
                    .map(str::to_owned)
            });
            if let (Some(port), Some(token)) = (port, token) {
                credentials = Some((port, token));
                break;
            }
        }
    }
    let (port, token) = credentials.context("Current client credentials are unreadable")?;
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()?;
    runtime.block_on(async move {
        let local = reqwest::Client::builder()
            .no_proxy()
            .danger_accept_invalid_certs(true)
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(8))
            .build()?;
        let own: serde_json::Value = local
            .get(format!(
                "https://127.0.0.1:{port}/lol-summoner/v1/current-summoner"
            ))
            .basic_auth("riot", Some(&token))
            .send()
            .await?
            .error_for_status()?
            .json()
            .await?;
        let puuid = own["puuid"]
            .as_str()
            .context("Current client identity is unavailable")?;
        // The deliberate unsupported directory key forces the repository's production LCU path without modifying its endpoint configuration or transmitting credentials elsewhere.
        let context =
            HistoryContext::new(port, token, "TEST_UNCONFIGURED".into(), None).with_account(puuid);
        let repository = HistoryRepository::new()?;
        let db = Database::bundled();
        let result = repository
            .load(&context, puuid, 50, Some(2400), &db.champions)
            .await?;
        ensure!(
            result.coverage.source == "LCU",
            "Fallback source was not preserved"
        );
        ensure!(
            !result.matches.is_empty(),
            "Local fallback returned no observed queue records"
        );
        ensure!(
            result.matches.iter().all(|m| m.queue_id == 2400),
            "Local fallback mixed queues"
        );
        ensure!(
            result
                .coverage
                .note
                .as_deref()
                .is_some_and(|note| note.contains("LCU")),
            "Fallback explanation was lost"
        );
        std::fs::create_dir_all("artifacts")?;
        std::fs::write(
            "artifacts/runtime-live-fallback.json",
            serde_json::to_vec_pretty(&json!({
                "source": result.coverage.source,
                "requested": result.coverage.requested,
                "scanned": result.coverage.scanned,
                "fetched": result.coverage.fetched,
                "valid": result.coverage.valid,
                "queue": result.coverage.queue_id,
                "note": result.coverage.note,
                "identitiesStored": false
            }))?,
        )?;
        Ok(())
    })
}

/// runtime_live_roster_profiles checks progressive and final loading for a real roster.
/// Review is requested before discovery to exercise startup ordering. Every visible
/// non-bot participant must receive a final result, while reports retain only
/// coverage and sanitized errors rather than player identities or match bodies.
#[test]
#[ignore = "Requires an authorized client with a recent ten-player competitive match"]
fn runtime_live_roster_profiles() -> Result<()> {
    let (commands, updates, automation) = client::start(
        Database::bundled(),
        Settings::default(),
        AnalysisSettings {
            match_count: 50,
            same_mode: true,
        },
    );
    commands.send(Command::ReviewLastGame)?;
    let started = Instant::now();
    let deadline = started + Duration::from_secs(180);
    let mut battle_key = String::new();
    let mut visible = BTreeSet::new();
    let mut completed = std::collections::BTreeMap::new();
    let mut progress_batches = 0;
    let mut queue = 0;
    while Instant::now() < deadline {
        match updates.try_recv() {
            Ok(Update::Battle(Some(battle))) if battle.review => {
                if battle.key != battle_key {
                    // A replacement roster owns a distinct result set; late updates from the previous review cannot satisfy this test's completion requirements.
                    battle_key = battle.key;
                    visible = battle
                        .players
                        .into_iter()
                        .filter(|player| !player.bot)
                        .filter_map(|player| player.puuid)
                        .collect();
                    completed.clear();
                    queue = battle.queue_id;
                }
            }
            Ok(Update::ScoutProgress { battle, puuid, .. })
                if battle == battle_key && visible.contains(&puuid) =>
            {
                progress_batches += 1;
            }
            Ok(Update::Scout {
                battle,
                puuid,
                result,
            }) if battle == battle_key && visible.contains(&puuid) => {
                let report = match result {
                    Ok(profile) => json!({
                        "success": true,
                        "source": profile.coverage.source,
                        "requested": profile.coverage.requested,
                        "fetched": profile.coverage.fetched,
                        "valid": profile.coverage.valid,
                        "complete": profile.coverage.complete,
                        "note": profile.coverage.note,
                        "error": profile.history_error,
                        "scopeMatches": profile.matches.iter().all(|game| game.queue_id == queue),
                    }),
                    Err(error) => json!({"success":false,"error":error}),
                };
                completed.insert(puuid, report);
            }
            _ => std::thread::sleep(Duration::from_millis(20)),
        }
        if !visible.is_empty() && completed.len() == visible.len() {
            break;
        }
    }
    let successful = completed
        .values()
        .filter(|report| report["success"] == true)
        .count();
    let partial = completed
        .values()
        .filter(|report| report["success"] == true && !report["error"].is_null())
        .count();
    std::fs::create_dir_all("artifacts")?;
    std::fs::write(
        "artifacts/runtime-live-roster.json",
        serde_json::to_vec_pretty(&json!({
            "requested":50,
            "queue":queue,
            "rosterPlayers":visible.len(),
            "completed":completed.len(),
            "successful":successful,
            "partial":partial,
            "progressBatches":progress_batches,
            "elapsedSeconds":started.elapsed().as_secs_f64(),
            "players":completed.values().collect::<Vec<_>>(),
            "automationEnabled":automation.enabled(),
            "identitiesStored":false,
        }))?,
    )?;
    ensure!(visible.len() == 10, "A ten-player review was not available");
    ensure!(completed.len() == 10, "Some roster jobs did not finish");
    ensure!(
        successful == 10,
        "Some roster profiles were unreadable; see sanitized report"
    );
    ensure!(
        completed
            .values()
            .all(|report| report["scopeMatches"] == true),
        "A roster profile mixed queue scopes"
    );
    ensure!(
        !automation.enabled(),
        "Read-only review enabled draft automation"
    );
    drop(commands);
    Ok(())
}
