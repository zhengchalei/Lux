//! transport.rs reads local client snapshots and watches authenticated LCU events.
//! HTTP and WebSocket connections are restricted to the discovered loopback
//! endpoint. Their self-signed certificate exception belongs to those transports
//! only; public and SGP requests use separate certificate-validating clients.
//! Current account identity and gameflow are mandatory snapshot observations,
//! while unavailable patch and region metadata retain explicit unknown labels.
//!
//! The event stream subscribes to OnJsonApiEvent because individual champ-select
//! subscriptions can fail during phase transitions. Relevant events wake the
//! session coordinator without carrying raw payloads or credentials into UI
//! state. Reconnecting restores the subscription and requests a fresh HTTP
//! snapshot, while the independent coordinator keeps a recovery poll active.
//! Region and patch observations are cached by account, so high-frequency
//! selection timers do not repeat optional metadata requests on every snapshot.
//! Read-only operations retry confirmed temporary transport failures once;
//! permanent HTTP responses remain available for sanitized stage diagnostics.

use super::{Auth, ProfileTarget};
use crate::{
    history::HistoryContext,
    model::{Mode, Room, num},
    scouting::{self, Battle, ClientAccount},
};
use anyhow::{Context, Result, bail};
use base64::Engine;
use futures_util::{SinkExt, StreamExt};
use serde_json::{Value, json};
use std::{
    future::Future,
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::sync::Mutex;
use tokio_tungstenite::{
    Connector,
    tungstenite::{Message, client::IntoClientRequest},
};

// Read-only retries are limited to one short pause, allowing transient client load to recover without replaying permanent failures.
const READ_RETRY_DELAY: Duration = Duration::from_millis(250);

/// temporary_read_failure identifies request failures eligible for one bounded retry.
/// Permission, missing-resource, validation, and JSON contract errors are permanent
/// for this request. Their callers retain the original response without guessing data.
pub(super) fn temporary_read_failure(error: &anyhow::Error) -> bool {
    error.chain().any(|cause| {
        cause.downcast_ref::<reqwest::Error>().is_some_and(|error| {
            error.is_timeout()
                || error.is_connect()
                || error.is_body()
                || error.status().is_some_and(|status| {
                    status.as_u16() == 408 || status.as_u16() == 429 || status.is_server_error()
                })
        })
    })
}

/// describe_read_failure returns a transport category without copying request identities.
/// URLs, raw error messages, request bodies, and authentication are never included.
/// The classification follows the actual nested reqwest error when one exists.
pub(super) fn describe_read_failure(error: &anyhow::Error) -> String {
    for cause in error.chain() {
        if let Some(error) = cause.downcast_ref::<reqwest::Error>() {
            return if let Some(status) = error.status() {
                format!("HTTP {}", status.as_u16())
            } else if error.is_timeout() {
                "请求超时".into()
            } else if error.is_connect() {
                "连接失败".into()
            } else if error.is_body() {
                "响应连接中断".into()
            } else if error.is_decode() {
                "响应无法解析".into()
            } else {
                "请求失败".into()
            };
        }
        if cause.is::<serde_json::Error>() {
            return "响应 JSON 无法解析".into();
        }
    }
    "响应缺少所需数据".into()
}

/// retry_read repeats one read-only operation after a transient transport failure.
/// The operation must not mutate game state and must recreate its request for
/// each attempt. Explicit HTTP 403 and 404 responses, malformed data, and caller
/// validation errors return immediately instead of entering the retry path.
///
/// Cancellation drops either the active request or the short retry timer.
/// A second failure propagates unchanged, preserving its HTTP status and nested
/// cause for sanitized stage reporting by the profile coordinator.
pub(super) async fn retry_read<T, F, Fut>(mut read: F) -> Result<T>
where
    F: FnMut() -> Fut,
    Fut: Future<Output = Result<T>>,
{
    match read().await {
        Err(error) if temporary_read_failure(&error) => {
            // Retry only a confirmed transport or temporary HTTP failure; permanent permission and missing-player responses retain their original result immediately.
            tokio::time::sleep(READ_RETRY_DELAY).await;
            read().await
        }
        result => result,
    }
}

/// SocketEvent identifies connection transitions and relevant local data changes.
pub(super) enum SocketEvent {
    Connected(bool), // Local event subscription currently receives client events.
    Changed(String), // Endpoint changed and needs a fresh coherent snapshot.
}

/// Snapshot associates visible room observations with one verified local account.
pub(super) struct Snapshot {
    pub account: ClientAccount, // Current local identity and observed server.
    pub phase: String,          // Current gameflow phase supplied by LCU.
    pub room: Option<Room>,     // Public selection data during champion selection.
    pub patch: String,          // Client patch or explicit unknown label.
    pub battle: Option<Battle>, // Public roster associated with this snapshot.
}

// Missing metadata retries at a bounded cadence; valid account metadata is reused until the connection or account changes.
const METADATA_RETRY_INTERVAL: Duration = Duration::from_secs(60);

/// CachedMetadata retains optional display metadata for one observed account only.
#[derive(Default)]
struct CachedMetadata {
    account: String,            // Local account owning these observations.
    region: Option<String>,     // Region code supplied by the client.
    patch: Option<String>,      // Game version supplied by the client.
    attempted: Option<Instant>, // Latest completed optional metadata refresh attempt.
}

/// MetadataCache serializes account replacement and optional metadata publication.
#[derive(Default)]
struct MetadataCache {
    current: Mutex<CachedMetadata>, // One account's optional client metadata observations.
}

impl MetadataCache {
    /// read_or_load returns cached metadata or evaluates a supplied refresh future.
    /// Account replacement removes old region and patch observations before the
    /// new future is polled. The lock covers that replacement and its refresh,
    /// preventing a canceled older reader from publishing metadata for a newer
    /// account through a cloned transport.
    ///
    /// Complete metadata avoids polling the supplied future entirely. Incomplete
    /// observations retain successful fields and retry after sixty seconds,
    /// keeping unavailable optional endpoints away from frequent countdown updates.
    async fn read_or_load(
        &self,
        account: &str,
        load: impl Future<Output = (Option<String>, Option<String>)>,
    ) -> (String, String) {
        let mut current = self.current.lock().await;
        if current.account != account {
            // Clear both fields before awaiting another account's endpoints so cancellation cannot leave the previous account's metadata available under its successor.
            *current = CachedMetadata {
                account: account.into(),
                ..CachedMetadata::default()
            };
        }
        let complete = current.region.is_some() && current.patch.is_some();
        let cooling_down = current
            .attempted
            .is_some_and(|at| at.elapsed() < METADATA_RETRY_INTERVAL);
        if !complete && !cooling_down {
            let (region, patch) = load.await;
            current.region = region.or(current.region.take());
            current.patch = patch.or(current.patch.take());
            current.attempted = Some(Instant::now());
        }
        (
            current.region.clone().unwrap_or_else(|| "大区未知".into()),
            current.patch.clone().unwrap_or_else(|| "未知版本".into()),
        )
    }
}

/// AsyncLcu owns cloneable loopback-only transport and ephemeral client credentials.
#[derive(Clone)]
pub(super) struct AsyncLcu {
    auth: Auth,            // Credentials never enter published snapshots or diagnostics.
    http: reqwest::Client, // Pooled asynchronous local HTTP connection manager.
    metadata: Arc<MetadataCache>, // Optional metadata cache shared by transport clones.
}

impl AsyncLcu {
    /// new constructs loopback transport from a discovered authenticated client endpoint.
    /// The client cannot follow redirects, so local basic authentication cannot
    /// move to another server through a client response.
    pub fn new(auth: Auth) -> Result<Self> {
        let http = reqwest::Client::builder()
            .no_proxy()
            .danger_accept_invalid_certs(true)
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(4))
            .build()?;
        Ok(Self {
            auth,
            http,
            metadata: Arc::new(MetadataCache::default()),
        })
    }

    /// get reads an internal absolute endpoint from the authenticated local client.
    /// Absolute remote URLs and protocol-relative paths are rejected before
    /// basic credentials are attached to the loopback request.
    pub async fn get(&self, path: &str) -> Result<Value> {
        if !path.starts_with('/') || path.starts_with("//") {
            bail!("Invalid local endpoint");
        }
        retry_read(|| async {
            Ok(self
                .http
                .get(format!("https://127.0.0.1:{}{path}", self.auth.port))
                .basic_auth("riot", Some(&self.auth.token))
                .send()
                .await?
                .error_for_status()?
                .json()
                .await?)
        })
        .await
    }

    /// context creates memory-only history access for the observed local server.
    /// Its account identifier scopes cached authentication to the logged-in user,
    /// while the separately queried player remains a history method argument.
    pub fn context(&self, account: &ClientAccount) -> HistoryContext {
        HistoryContext::new(
            self.auth.port,
            self.auth.token.clone(),
            account.region.split(" / ").next().unwrap_or("").into(),
            self.auth.platform.clone(),
        )
        .with_account(&account.puuid)
    }

    /// resolve reads a public player identity from a current account or query.
    /// Riot IDs use the client's alias lookup; legacy names remain encoded query
    /// parameters. An alias request searches accounts and never changes the user.
    ///
    /// A supplied PUUID must pass the shared public-identity validator before it
    /// enters an endpoint path. Name inputs retain their tagline components and
    /// use JSON or URL encoding rather than concatenating user text into paths.
    /// Empty results remain explicit lookup failures instead of fabricated accounts.
    pub async fn resolve(&self, target: &ProfileTarget) -> Result<Value> {
        match target {
            ProfileTarget::Current => self.get("/lol-summoner/v1/current-summoner").await,
            ProfileTarget::Puuid(id) => {
                if scouting::identity(&json!(id)).is_none() {
                    bail!("无效的玩家标识");
                }
                self.get(&format!("/lol-summoner/v2/summoners/puuid/{id}"))
                    .await
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
                    let result: Value = retry_read(|| async {
                        Ok(self
                            .http
                            .post(format!(
                                "https://127.0.0.1:{}/lol-summoner/v1/summoners/aliases",
                                self.auth.port
                            ))
                            .basic_auth("riot", Some(&self.auth.token))
                            .json(&json!([{ "gameName":name.trim(),"tagLine":tag.trim() }]))
                            .send()
                            .await?
                            .error_for_status()?
                            .json()
                            .await?)
                    })
                    .await?;
                    result
                        .as_array()
                        .and_then(|rows| rows.first())
                        .cloned()
                        .context("当前大区未找到该 Riot ID")
                } else {
                    retry_read(|| async {
                        Ok(self
                            .http
                            .get(format!(
                                "https://127.0.0.1:{}/lol-summoner/v1/summoners",
                                self.auth.port
                            ))
                            .query(&[("name", query)])
                            .basic_auth("riot", Some(&self.auth.token))
                            .send()
                            .await?
                            .error_for_status()?
                            .json()
                            .await?)
                    })
                    .await
                }
            }
        }
    }

    /// snapshot reads mandatory identity and phase before deriving the visible roster.
    /// Optional metadata failures remain unknown rather than disconnecting usable
    /// sessions. A final identity read rejects mixed snapshots when the account
    /// changes while independent endpoint requests are still in progress.
    ///
    /// Champion selection combines its session with gameflow queue metadata and
    /// verified availability lists. In-game rosters instead use public gameflow
    /// participants. A phase transition that invalidates mandatory reads fails
    /// this snapshot and lets the coordinator obtain a new consistent state.
    pub async fn snapshot(&self) -> Result<Snapshot> {
        let (summoner, phase) = tokio::join!(
            self.resolve(&ProfileTarget::Current),
            self.get("/lol-gameflow/v1/gameflow-phase"),
        );
        let summoner = summoner?;
        let puuid = scouting::identity(&summoner["puuid"]).context("Missing local identity")?;
        let phase = phase?
            .as_str()
            .context("Missing gameflow phase")?
            .to_owned();
        let (mut region, patch) = self
            .metadata
            .read_or_load(&puuid, async {
                let (region, patch) = tokio::join!(
                    self.get("/riotclient/region-locale"),
                    self.get("/lol-patch/v1/game-version"),
                );
                (
                    region.ok().and_then(|v| {
                        v["region"]
                            .as_str()
                            .filter(|value| !value.is_empty())
                            .map(str::to_owned)
                    }),
                    patch.ok().and_then(|v| {
                        v.as_str()
                            .filter(|value| !value.is_empty())
                            .map(str::to_owned)
                    }),
                )
            })
            .await;
        if let Some(platform) = &self.auth.platform {
            region = format!("{region} / {platform}");
        }
        let account = ClientAccount {
            puuid: puuid.clone(),
            name: scouting::display_name(&summoner),
            region,
        };
        let (room, battle) = if phase == "ChampSelect" {
            let (session, flow, picks, disabled) = tokio::join!(
                self.get("/lol-champ-select/v1/session"),
                self.get("/lol-gameflow/v1/session"),
                self.get("/lol-champ-select/v1/pickable-champion-ids"),
                self.get("/lol-champ-select/v1/disabled-champion-ids"),
            );
            let session = session?;
            let flow = flow?;
            let queue = if num(&session["queueId"]) > 0 {
                num(&session["queueId"])
            } else {
                num(&flow["gameData"]["queue"]["id"])
            };
            let mode = Mode::from_queue(queue, session["benchEnabled"] == true);
            let picks = picks
                .ok()
                .and_then(|v| serde_json::from_value::<Vec<u32>>(v).ok());
            let disabled = disabled
                .ok()
                .and_then(|v| serde_json::from_value::<Vec<u32>>(v).ok());
            // Ranked suggestions require both availability endpoints; uncertain metadata must never turn a disabled champion into a legal recommendation.
            let room = Room::from_lcu(
                &session,
                queue,
                if mode == Mode::Ranked && disabled.is_some() {
                    picks.as_deref()
                } else {
                    None
                },
                disabled.as_deref().unwrap_or(&[]),
            );
            let battle = Battle::draft(&session, &room);
            (Some(room), Some(battle))
        } else if ["GameStart", "InProgress", "Reconnect"].contains(&phase.as_str()) {
            let flow = self.get("/lol-gameflow/v1/session").await?;
            (None, Battle::gameflow(&flow, &puuid))
        } else {
            (None, None)
        };
        // Discard an interleaved account transition before publication; the next recovery snapshot obtains one consistent replacement account and roster.
        if scouting::identity(&self.resolve(&ProfileTarget::Current).await?["puuid"]).as_deref()
            != Some(&puuid)
        {
            bail!("客户端账号正在切换");
        }
        Ok(Snapshot {
            account,
            phase,
            room,
            patch,
            battle,
        })
    }

    /// watch_events reconnects the local JSON event subscription until canceled.
    /// The caller owns the task and aborts it when credentials change. Only
    /// relevant endpoint names leave this transport; raw event data stays private.
    ///
    /// Every reconnect emits its connection status and requests a fresh HTTP
    /// snapshot through the coordinator. Failed negotiations wait five seconds
    /// before another attempt, while clean socket closure retries sooner.
    /// Canceling this owned task drops the active socket or retry timer, so
    /// previous credentials cannot establish a replacement subscription.
    pub async fn watch_events(&self, publish: impl Fn(SocketEvent) + Send) {
        loop {
            let result = self.receive_events(&publish).await;
            publish(SocketEvent::Connected(false));
            // A closed subscription retries independently from HTTP polling, allowing local state to recover even when WebSocket negotiation remains unavailable.
            if result.is_err() {
                tokio::time::sleep(Duration::from_secs(5)).await;
            } else {
                tokio::time::sleep(Duration::from_secs(1)).await;
            }
        }
    }

    /// receive_events authenticates one loopback subscription and forwards relevant changes.
    /// The native certificate exception applies only to this explicit loopback
    /// connector. Closing or canceling the task drops its socket and subscription.
    async fn receive_events(&self, publish: &impl Fn(SocketEvent)) -> Result<()> {
        let mut request = format!("wss://127.0.0.1:{}/", self.auth.port).into_client_request()?;
        let basic =
            base64::engine::general_purpose::STANDARD.encode(format!("riot:{}", self.auth.token));
        request
            .headers_mut()
            .insert("Authorization", format!("Basic {basic}").parse()?);
        let tls = native_tls::TlsConnector::builder()
            .danger_accept_invalid_certs(true)
            .build()?;
        let (mut socket, _) = tokio::time::timeout(
            Duration::from_secs(4),
            tokio_tungstenite::connect_async_tls_with_config(
                request,
                None,
                false,
                Some(Connector::NativeTls(tls)),
            ),
        )
        .await??;
        socket
            .send(Message::Text("[5,\"OnJsonApiEvent\"]".into()))
            .await?;
        publish(SocketEvent::Connected(true));
        while let Some(message) = socket.next().await {
            let message = message?;
            if message.is_close() {
                break;
            }
            if let Message::Text(text) = message
                && let Some(uri) = relevant_event(&text)
            {
                publish(SocketEvent::Changed(uri));
            }
        }
        Ok(())
    }
}

/// relevant_event extracts state-changing endpoint names from JSON API messages.
/// Malformed frames and unrelated client traffic do not trigger HTTP refreshes.
/// Authentication payloads are intentionally not returned to the session coordinator.
fn relevant_event(text: &str) -> Option<String> {
    let frame: Value = serde_json::from_str(text).ok()?;
    if frame[0] != 8 || frame[1] != "OnJsonApiEvent" {
        return None;
    }
    let uri = frame[2]["uri"].as_str()?;
    [
        "/lol-gameflow/v1/",
        "/lol-champ-select/v1/",
        "/lol-lobby/v2/lobby",
        "/lol-summoner/v1/current-summoner",
        "/entitlements/v1/token",
        "/lol-league-session/v1/league-session-token",
        "/lol-match-history/v1/",
    ]
    .iter()
    .any(|prefix| uri.starts_with(prefix))
    .then(|| uri.into())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// event_subscription_filters_noise_without_exposing_payloads reproduces useful and unrelated frames.
    /// Only endpoint names that invalidate public session state are accepted, and raw authentication or unrelated chat data never leaves the parser.
    #[test]
    fn event_subscription_filters_noise_without_exposing_payloads() {
        let text = r#"[8,"OnJsonApiEvent",{"uri":"/lol-champ-select/v1/session","data":{"secret":"unused"}}]"#;
        assert_eq!(
            relevant_event(text).as_deref(),
            Some("/lol-champ-select/v1/session")
        );
        assert!(
            relevant_event(r#"[8,"OnJsonApiEvent",{"uri":"/lol-chat/v1/conversations"}]"#)
                .is_none()
        );
        assert!(
            relevant_event(r#"[5,"OnJsonApiEvent",{"uri":"/lol-gameflow/v1/session"}]"#).is_none()
        );
        assert!(relevant_event("invalid").is_none());
    }
    /// metadata_cache_skips_countdown_refreshes_and_replaces_changed_accounts reproduces repeated snapshots.
    /// Cached optional endpoints are not polled again until a different local account needs its own observations.
    #[tokio::test]
    async fn metadata_cache_skips_countdown_refreshes_and_replaces_changed_accounts() {
        let cache = MetadataCache::default();
        let loads = std::sync::atomic::AtomicUsize::new(0);
        let first = cache
            .read_or_load("first", async {
                loads.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                (Some("region-a".into()), Some("patch-a".into()))
            })
            .await;
        assert_eq!(first, ("region-a".into(), "patch-a".into()));
        for _ in 0..100 {
            let cached = cache
                .read_or_load("first", async {
                    loads.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                    (Some("unexpected".into()), Some("unexpected".into()))
                })
                .await;
            assert_eq!(cached, first);
        }
        assert_eq!(loads.load(std::sync::atomic::Ordering::SeqCst), 1);
        let second = cache
            .read_or_load("second", async {
                loads.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                (Some("region-b".into()), Some("patch-b".into()))
            })
            .await;
        assert_eq!(second, ("region-b".into(), "patch-b".into()));
        assert_eq!(loads.load(std::sync::atomic::Ordering::SeqCst), 2);
    }

    /// missing_optional_metadata_keeps_known_fields_without_repeated_endpoint_reads reproduces a partial response.
    /// Frequent snapshots reuse the known region and explicit unknown patch during the bounded retry interval.
    #[tokio::test]
    async fn missing_optional_metadata_keeps_known_fields_without_repeated_endpoint_reads() {
        let cache = MetadataCache::default();
        let first = cache
            .read_or_load("first", async { (Some("region-a".into()), None) })
            .await;
        assert_eq!(first, ("region-a".into(), "未知版本".into()));
        let polled = std::sync::atomic::AtomicBool::new(false);
        let cached = cache
            .read_or_load("first", async {
                polled.store(true, std::sync::atomic::Ordering::SeqCst);
                (None, Some("unexpected".into()))
            })
            .await;
        assert_eq!(cached, first);
        assert!(!polled.load(std::sync::atomic::Ordering::SeqCst));
        let other = cache.read_or_load("second", async { (None, None) }).await;
        assert_eq!(other, ("大区未知".into(), "未知版本".into()));
    }
    /// status_server returns controlled loopback HTTP responses for read retry checks.
    /// Its thread accepts exactly the supplied response sequence and never reads
    /// credentials or contacts the game client; callers join it after the request.
    fn status_server(statuses: Vec<u16>) -> (String, std::thread::JoinHandle<()>) {
        use std::io::{Read, Write};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = std::thread::spawn(move || {
            for status in statuses {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                let mut request = [0_u8; 2048];
                let _ = stream.read(&mut request).unwrap();
                let response = format!(
                    "HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n[]"
                );
                stream.write_all(response.as_bytes()).unwrap();
            }
        });
        (endpoint, server)
    }

    /// read_retry_recovers_from_throttling_but_preserves_permanent_statuses uses real loopback responses.
    /// A 429 receives one delayed retry, whereas 403 and 404 propagate their actual status without retrying.
    #[tokio::test]
    async fn read_retry_recovers_from_throttling_but_preserves_permanent_statuses() {
        let client = reqwest::Client::builder()
            .no_proxy()
            .timeout(Duration::from_secs(2))
            .build()
            .unwrap();
        let (endpoint, server) = status_server(vec![429, 200]);
        let started = Instant::now();
        let result = retry_read(|| async {
            Ok(client
                .get(&endpoint)
                .send()
                .await?
                .error_for_status()?
                .json::<Value>()
                .await?)
        })
        .await
        .unwrap();
        assert_eq!(result, json!([]));
        assert!(started.elapsed() >= READ_RETRY_DELAY);
        server.join().unwrap();
        for status in [403_u16, 404] {
            let (endpoint, server) = status_server(vec![status]);
            let error = retry_read(|| async {
                Ok(client
                    .get(&endpoint)
                    .send()
                    .await?
                    .error_for_status()?
                    .json::<Value>()
                    .await?)
            })
            .await
            .unwrap_err();
            assert_eq!(describe_read_failure(&error), format!("HTTP {status}"));
            assert!(!temporary_read_failure(&error));
            assert!(!describe_read_failure(&error).contains(&endpoint));
            server.join().unwrap();
        }
    }
}
