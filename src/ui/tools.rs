//! tools.rs renders player search, visible roster scouting, and explicit draft rules.
//! The parent Lux view keeps ordinary match history independent of queried
//! profiles. Each query owns a monotonically increasing request identifier, and
//! both summary and detail results must match it before entering the screen.
//! Scouting rows likewise associate profiles by battle key and account identifier
//! rather than depending on changing row order during champion selection.
//!
//! Selection controls edit draft preferences locally until the user applies them.
//! Starting and stopping automation are explicit actions inside the settings page;
//! the navigation indicator only reports whether that runtime is enabled.
//! Champion priority chips expose their order and removal directly. Settings
//! survive a restart, while the ability to write
//! to the client is restricted to the current explicitly enabled process session.
//! Compact scouting rows show ten recent outcomes independently of the configured
//! analysis depth; aggregate labels always use the actual returned usable sample.
//! Player and hero aggregates arrive prepared by background work, and uniform
//! lists build only visible history rows and hero tiles
//! during interaction-driven redraws.
//! Existing player snapshots survive same-game refresh while status and failure
//! messages describe the new request. Different games and account changes clear
//! old associations before any new participant result becomes visible. Query
//! scope remains independent from roster scope and labels its actual provider,
//! scan depth, and complete-roster coverage beside historical contribution values.

use super::player_analysis::{coverage_label, query_filter_mode, query_scope_label};
use super::*;
use gpui_kit::component::{
    input::{Input, InputEvent, InputState},
    switch::Switch,
    tooltip::Tooltip,
};
use lux::{
    automation::Settings,
    client::AutomationHandle,
    scouting::{
        AnalysisSettings, Battle, Combatant, PreparedProfile, ProfileTarget, RecentSummary,
        ScoutState,
    },
};
use std::collections::VecDeque;

/// Tools owns input widgets and associated query, roster, and automation states.
pub(super) struct Tools {
    query: Entity<InputState>,      // Riot ID input for current-region queries.
    hero_query: Entity<InputState>, // Search text for automation champion priorities.
    request: u64,                   // Latest search request identity accepted by receive_tools.
    searching: bool,                // Current search is awaiting its matching response.
    profile: Option<PreparedProfile>, // Queried history and precomputed combat aggregates.
    profile_revision: u64,          // Accepted snapshot version for local detail work.
    // The target, queue, and champion describe this query independently of battle settings;
    // scope changes reuse that context while advancing the request identity before fetching.
    query_target: Option<ProfileTarget>, // Last explicitly queried player identity.
    query_queue: Option<u64>,            // Exact queue requested for this query.
    query_champion: u32,                 // Selected player champion for sample inspection.
    query_hero: Option<[RecentSummary; 4]>, // Hero aggregates prepared for each visible filter.
    query_error: Option<String>,         // Failure of the current explicit profile query.
    selected_match: usize,               // Selected record within the queried profile.
    query_filter: usize,                 // All, ranked, or ARAM profile record filter.
    battle: Option<Battle>,              // Current visible roster or labeled historical review.
    scouts: BTreeMap<String, ScoutState>, // Prepared profiles keyed by visible identity.
    analysis: AnalysisSettings,          // Persisted depth for subsequent profile requests.
    settings: Settings,                  // Locally edited champion automation rule preferences.
    pub(super) automation: AutomationHandle, // Runtime enable and immediate stop handle.
    pub(super) auto_status: String,      // Latest background automation state explanation.
    actions: VecDeque<String>,           // Recent selection submissions and failures in memory.
    _subscriptions: Vec<Subscription>,   // Input observers owned for this view's lifetime.
}

impl Tools {
    /// clear_account invalidates visible player data when the observed local account changes.
    /// Incrementing the lookup identity rejects outstanding results from the previous
    /// account. Saved analysis preferences and champion rules remain local settings,
    /// while profiles, roster status, and per-session activity are discarded together.
    pub(super) fn clear_account(&mut self) {
        self.request = self.request.wrapping_add(1);
        self.searching = false;
        self.profile = None;
        self.profile_revision = self.profile_revision.wrapping_add(1);
        self.query_target = None;
        self.query_queue = None;
        self.query_champion = 0;
        self.query_hero = None;
        self.query_error = None;
        self.selected_match = 0;
        self.battle = None;
        self.scouts.clear();
        self.actions.clear();
    }

    /// diagnostic_context reports workload sizes without retaining player names or identifiers.
    #[cfg(feature = "diagnostics")]
    pub(super) fn diagnostic_context(&self) -> serde_json::Value {
        serde_json::json!({
            "rosterPlayers": self.battle.as_ref().map_or(0, |b| b.players.len()),
            "loadedProfiles": self.scouts.values().filter(|p| p.profile.is_some()).count(),
            "profileRecords": self.scouts.values().filter_map(|p| p.profile.as_ref().map(|p| p.matches.len())).collect::<Vec<_>>(),
            "queryRecords": self.profile.as_ref().map_or(0, |p| p.matches.len()),
        })
    }

    /// new creates native query inputs without arming saved selection preferences.
    /// Input subscriptions remain owned by Tools, so typing updates champion
    /// results and pressing Enter starts the same lookup as the search button.
    pub(super) fn new(
        settings: Settings,
        automation: AutomationHandle,
        analysis: AnalysisSettings,
        window: &mut Window,
        cx: &mut Context<Lux>,
    ) -> Self {
        let query = cx.new(|cx| InputState::new(window, cx).placeholder("名称#编号，或旧召唤师名"));
        let hero_query =
            cx.new(|cx| InputState::new(window, cx).placeholder("搜索英雄名、称号、英文名或 ID"));
        let subscriptions = vec![
            cx.subscribe_in(&query, window, |this, _, event, _, cx| {
                if matches!(event, InputEvent::PressEnter { .. }) {
                    let query = this.tools.query.read(cx).value().to_string();
                    this.query_player(ProfileTarget::Name(query));
                }
                cx.notify();
            }),
            cx.subscribe_in(&hero_query, window, |_, _, _: &InputEvent, _, cx| {
                cx.notify()
            }),
        ];
        Self {
            query,
            hero_query,
            request: 0,
            searching: false,
            profile: None,
            profile_revision: 0,
            query_target: None,
            query_queue: None,
            query_champion: 0,
            query_hero: None,
            query_error: None,
            selected_match: 0,
            query_filter: 0,
            battle: None,
            scouts: BTreeMap::new(),
            analysis,
            settings,
            automation,
            auto_status: "自动选禁已关闭 · 规则保存后仍需明确开启".into(),
            actions: VecDeque::new(),
            _subscriptions: subscriptions,
        }
    }
}

impl Drop for Tools {
    /// drop disarms client mutations before the associated view state disappears.
    fn drop(&mut self) {
        self.automation.stop();
    }
}

impl Lux {
    /// open_self_query requests the logged-in profile for an explicit startup option.
    pub(super) fn open_self_query(&mut self) {
        self.query_player(ProfileTarget::Current);
    }
    /// receive_tools associates asynchronous query and roster results before replacing UI state.
    /// A late search or detail reply is ignored when another request owns the
    /// profile. Scouting results require both the displayed battle key and a
    /// participant still present in that roster, so an old room cannot populate
    /// an unrelated new row. Automation actions are retained only in a bounded
    /// in-memory activity list and never displayed as successful client locks.
    /// Progressive player batches replace readable content while retaining their
    /// loading flag; only a final Scout result can complete the roster refresh.
    pub(super) fn receive_tools(&mut self, update: Update, cx: &mut Context<Self>) {
        match update {
            Update::LookupProgress { request, profile } if request == self.tools.request => {
                self.notice = format!("已读取 {} 场，正在继续加载…", profile.matches.len());
                self.accept_query_profile(profile);
            }
            Update::Lookup { request, result } if request == self.tools.request => {
                self.tools.searching = false;
                match result {
                    Ok(profile) => {
                        self.notice = profile.history_error.clone().unwrap_or_else(|| {
                            format!(
                                "玩家战绩已更新 · {} · 读取 {} 场",
                                profile.coverage.source, profile.coverage.fetched
                            )
                        });
                        self.accept_query_profile(profile);
                        self.request_query_detail();
                    }
                    Err(error) => {
                        self.notice = error.clone();
                        self.tools.query_error = Some(error);
                    }
                }
            }
            Update::ProfileDetail {
                request,
                puuid,
                record,
            } if request == self.tools.request => {
                self.accept_query_detail(request, puuid, record, cx);
            }
            Update::Battle(battle) => {
                let same = self
                    .tools
                    .battle
                    .as_ref()
                    .zip(battle.as_ref())
                    .is_some_and(|(old, new)| old.key == new.key);
                // A roster edit within the same game preserves surviving identities; a different game must never display the previous game's rows.
                if same {
                    self.tools.scouts.retain(|id, _| {
                        battle
                            .as_ref()
                            .is_some_and(|b| b.players.iter().any(|p| p.puuid.as_ref() == Some(id)))
                    });
                } else {
                    self.tools.scouts.clear();
                }
                self.tools.battle = battle;
            }
            Update::ScoutStarted { battle, puuid } => {
                if self.tools.battle.as_ref().is_some_and(|b| {
                    b.key == battle && b.players.iter().any(|p| p.puuid.as_ref() == Some(&puuid))
                }) {
                    self.tools.scouts.entry(puuid).or_default().begin();
                }
            }
            Update::ScoutProgress {
                battle,
                puuid,
                profile,
            } => {
                if self.tools.battle.as_ref().is_some_and(|b| {
                    b.key == battle && b.players.iter().any(|p| p.puuid.as_ref() == Some(&puuid))
                }) {
                    // First batches are usable observations, but the active request still owns completion and may supply more matches or enriched details.
                    let state = self.tools.scouts.entry(puuid).or_default();
                    state.profile = Some(profile);
                    state.error = None;
                    state.updating = true;
                }
            }
            Update::Scout {
                battle,
                puuid,
                result,
            } => {
                if self.tools.battle.as_ref().is_some_and(|b| {
                    b.key == battle && b.players.iter().any(|p| p.puuid.as_ref() == Some(&puuid))
                }) {
                    self.tools
                        .scouts
                        .entry(puuid)
                        .or_default()
                        .finish_prepared(result);
                    let expected = self
                        .tools
                        .battle
                        .as_ref()
                        .map(|b| {
                            b.players
                                .iter()
                                .filter(|p| p.puuid.is_some() && !p.bot)
                                .count()
                        })
                        .unwrap_or(0);
                    if expected > 0
                        && self.tools.scouts.len() >= expected
                        && self.tools.scouts.values().all(|s| !s.updating)
                    {
                        let readable = self
                            .tools
                            .scouts
                            .values()
                            .filter(|r| {
                                r.error.is_none()
                                    && r.profile
                                        .as_ref()
                                        .is_some_and(|p| p.history_error.is_none())
                            })
                            .count();
                        self.notice = format!(
                            "玩家数据已更新 · {readable}/{expected} 人战绩可读 · 分析上限 {} 场",
                            self.tools.analysis.bounded_count()
                        );
                    }
                }
            }
            Update::Automation { status, action } => {
                self.tools.auto_status = status;
                if let Some(action) = action {
                    self.tools.actions.push_front(format!(
                        "{} {action}",
                        chrono::Local::now().format("%H:%M:%S")
                    ));
                    self.tools.actions.truncate(20);
                }
            }
            _ => {}
        }
    }

    /// query_player starts a player query and invalidates any earlier profile reply.
    /// Querying a player never replaces the user's ordinary history page. Existing
    /// query content is cleared while loading so failures cannot masquerade as
    /// another account's successfully loaded records.
    fn query_player(&mut self, target: ProfileTarget) {
        self.query_player_in(target, None, 0);
    }

    /// query_player_in starts an identity-bound lookup with an independent queue and hero context.
    /// Explicit queries clear previous content before sending work, while progressive
    /// replies retain their request identifier. A roster click can supply its current
    /// champion without making subsequent manual player searches inherit that champion.
    /// The status bar changes at admission and on accepted progress or completion,
    /// so connection-wait text cannot survive a successfully populated query.
    fn query_player_in(&mut self, target: ProfileTarget, queue_id: Option<u64>, champion: u32) {
        self.tools.request = self.tools.request.wrapping_add(1);
        self.tools.searching = true;
        self.tools.profile = None;
        self.tools.query_target = Some(target.clone());
        self.tools.query_queue = queue_id;
        self.tools.query_champion = champion;
        self.tools.query_hero = None;
        self.tools.query_error = None;
        self.tools.selected_match = 0;
        self.tools.query_filter = 0;
        self.page = 2;
        self.notice = "正在读取玩家资料与战绩…".into();
        self.send(Command::Lookup {
            request: self.tools.request,
            target,
            queue_id,
        });
    }

    /// accept_query_profile replaces one query snapshot while retaining its selected match identity.
    /// Progressive pages and final replies use the same path, so a selected match
    /// remains selected when more history arrives. Selection returns to the first
    /// record only when that game disappears. Cached hero statistics are selected
    /// alongside each accepted profile, keeping repeated pointer redraws away
    /// from full-history aggregation and making the displayed detail share the
    /// same source observations as the visible summary.
    fn accept_query_profile(&mut self, profile: PreparedProfile) {
        let selected = self.tools.profile.as_ref().and_then(|old| {
            old.matches
                .get(self.tools.selected_match)
                .map(|game| game.id)
        });
        self.tools.selected_match = selected
            .and_then(|id| {
                profile.matches.iter().position(|game| {
                    game.id == id
                        && lux::scouting::usable_match(
                            game,
                            query_filter_mode(self.tools.query_filter),
                        )
                })
            })
            .or_else(|| {
                profile.matches.iter().position(|game| {
                    lux::scouting::usable_match(game, query_filter_mode(self.tools.query_filter))
                })
            })
            .unwrap_or(usize::MAX);
        if self.tools.query_champion == 0 {
            self.tools.query_champion = profile.matches.first().map_or(0, |game| game.champion_id);
        }
        self.tools.profile_revision = self.tools.profile_revision.wrapping_add(1);
        self.tools.profile = Some(profile);
        self.tools.query_error = None;
        self.prepare_query_hero();
    }

    /// accept_query_detail prepares an enriched profile away from the UI thread.
    /// The captured query and snapshot revision identify the observations being
    /// enriched, and a later progressive reply invalidates that local work. A
    /// completed detail updates the screen only while its game remains selected,
    /// preventing two quick selections from restoring an older detail snapshot.
    /// Recomputed contributions travel with the enriched observations, so header
    /// statistics and match detail never mix separately prepared sample versions.
    fn accept_query_detail(
        &mut self,
        request: u64,
        puuid: String,
        record: MatchRecord,
        cx: &mut Context<Self>,
    ) {
        let Some(profile) = self
            .tools
            .profile
            .as_ref()
            .filter(|profile| profile.puuid == puuid)
        else {
            return;
        };
        let Some(index) = profile.matches.iter().position(|game| game.id == record.id) else {
            return;
        };
        let game_id = record.id;
        let revision = self.tools.profile_revision;
        let mut observations = profile.profile.clone();
        observations.matches[index] = record;
        // A background snapshot owns its observations so preparation cannot mutate the profile still displayed during user interaction.
        cx.spawn(async move |this, cx| {
            let prepared = cx
                .background_executor()
                .spawn(async move {
                    let mut prepared = PreparedProfile::new(observations);
                    prepared.profile.coverage.complete = prepared.summary.contribution.games;
                    prepared
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                if this.tools.request == request
                    && this.tools.profile_revision == revision
                    && this
                        .tools
                        .profile
                        .as_ref()
                        .and_then(|profile| profile.matches.get(this.tools.selected_match))
                        .is_some_and(|game| game.id == game_id)
                {
                    this.accept_query_profile(prepared);
                    cx.notify();
                }
            });
        })
        .detach();
    }

    /// prepare_query_hero caches hero aggregates before any profile-dependent view is rendered.
    /// A missing profile clears these values. The four entries follow the query
    /// filter order, and use the same source sample as its player summary.
    fn prepare_query_hero(&mut self) {
        self.tools.query_hero = self.tools.profile.as_ref().map(|profile| {
            [
                None,
                Some(Mode::Ranked),
                Some(Mode::Aram),
                Some(Mode::Mayhem),
            ]
            .map(|mode| profile.hero_summary(self.tools.query_champion, mode))
        });
    }

    /// reload_query_scope fetches the last player under a newly selected exact queue.
    /// The query keeps its hero context and advances its own request identity;
    /// changing battle settings alone never silently changes this query scope.
    fn reload_query_scope(&mut self, queue_id: Option<u64>) {
        if let Some(target) = self.tools.query_target.clone() {
            self.query_player_in(target, queue_id, self.tools.query_champion);
        }
    }

    /// request_query_detail loads a partial record with its queried account perspective.
    /// Results carry the query identifier and player identity. A full ten-player
    /// record needs no additional network request, preserving the existing detail.
    fn request_query_detail(&mut self) {
        if let Some(profile) = &self.tools.profile
            && let Some(record) = profile.matches.get(self.tools.selected_match)
            && record.players.len() < 10
        {
            self.send(Command::ProfileDetail {
                request: self.tools.request,
                puuid: profile.puuid.clone(),
                id: record.id,
            });
        }
    }

    /// query_page renders current-region player lookup beside full combat detail.
    pub(super) fn query_page(
        &self,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        let profile = self.tools.profile.as_ref();
        let filter_mode = query_filter_mode(self.tools.query_filter);
        let filtered: Vec<usize> = profile
            .into_iter()
            .flat_map(|p| p.matches.iter().enumerate())
            .filter(|(_, m)| lux::scouting::usable_match(m, filter_mode))
            .map(|(index, _)| index)
            .collect();
        div()
            .flex()
            .flex_col()
            .flex_1()
            .min_h_0()
            .child(
                div()
                    .p_5()
                    .border_b_1()
                    .border_color(p.line)
                    .child(section_title(
                        "查找召唤师",
                        &format!(
                            "当前客户端大区 · 最多 {} 场",
                            profile
                                .map(|p| p.requested)
                                .unwrap_or(self.tools.analysis.bounded_count())
                        ),
                        p,
                    ))
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_2()
                            .child(
                                div().flex_1().max_w(px(470.)).child(
                                    Input::new(&self.tools.query).aria_label("召唤师 Riot ID"),
                                ),
                            )
                            .child(
                                Button::new("search-player")
                                    .label("查询")
                                    .disabled(self.tools.searching)
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        let query = this.tools.query.read(cx).value().to_string();
                                        this.query_player(ProfileTarget::Name(query));
                                        cx.notify();
                                    })),
                            )
                            .child(Button::new("search-self").label("查询自己").on_click(
                                cx.listener(|this, _, _, cx| {
                                    this.query_player(ProfileTarget::Current);
                                    cx.notify();
                                }),
                            )),
                    )
                    .child(
                        div()
                            .mt_2()
                            .text_xs()
                            .text_color(p.muted)
                            .child("优先输入完整 Riot ID；点击对战助手里的玩家也能打开战绩。"),
                    )
                    .child(
                        div().mt_3().flex().items_center().gap_2().flex_wrap()
                            .child(div().text_xs().text_color(p.muted).child("查询范围"))
                            .child(Button::new("query-scope-all").label("全部模式").small()
                                .selected(self.tools.query_queue.is_none())
                                .disabled(self.tools.query_target.is_none())
                                .on_click(cx.listener(|this, _, _, cx| {
                                    this.reload_query_scope(None);
                                    cx.notify();
                                })))
                            .when_some(self.tools.battle.as_ref().map(|battle| battle.queue_id).filter(|queue| *queue > 0), |el, queue| {
                                el.child(Button::new("query-scope-current").label(format!("当前队列 · {}", lux::model::queue_label(queue))).small()
                                    .selected(self.tools.query_queue == Some(queue))
                                    .disabled(self.tools.query_target.is_none())
                                    .on_click(cx.listener(move |this, _, _, cx| {
                                        this.reload_query_scope(Some(queue));
                                        cx.notify();
                                    })))
                            })
                            .child(div().text_xs().text_color(p.muted).ml_2().child("目标场数"))
                            .children([20_usize,50,100,200].into_iter().map(|count| {
                                Button::new(("query-depth",count)).label(count.to_string()).small()
                                    .selected(self.tools.analysis.bounded_count() == count)
                                    .on_click(cx.listener(move |this,_,_,cx| {
                                        let queue = this.tools.query_queue;
                                        this.set_analysis_count(count);
                                        this.reload_query_scope(queue);
                                        cx.notify();
                                    }))
                            })),
                    ),
            )
            .when(self.tools.searching, |el| {
                el.child(div().px_5().py_2().text_sm().text_color(p.muted).child(
                    if self.tools.profile.is_some() { "首批战绩已就绪，正在补齐目标场数…" } else { "正在查询玩家与战绩…" }
                ))
            })
            .when_some(self.tools.query_error.clone(), |el, error| {
                el.child(div().p_5().text_color(p.bad).child(error))
            })
            .when(
                profile.is_none() && !self.tools.searching && self.tools.query_error.is_none(),
                |el| {
                    el.child(
                        div()
                            .p_6()
                            .text_color(p.muted)
                            .child("输入名称开始查询，或直接查看自己的实时战绩。"),
                    )
                },
            )
            .when_some(profile, |el, profile| {
                let summary = profile.summary_for(filter_mode);
                let scope_label = query_scope_label(profile.coverage.queue_id, filter_mode);
                el.child(
                    div()
                        .px_5()
                        .py_4()
                        .bg(p.panel)
                        .child(
                            div()
                                .flex()
                                .justify_between()
                                .child(
                                    div()
                                        .text_lg()
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .child(profile.name.clone()),
                                )
                                .child(format!("{} 级 · {}", profile.level, profile.rank)),
                        )
                        .child(
                            div()
                                .mt_2()
                                .text_color(p.muted)
                                .child(if summary.games > 0 {
                                    format!(
                                        "{} · {} 场有效 · {} 胜 {} 负 · 历史胜率 {:.1}% · KDA {:.2}",
                                        scope_label,
                                        summary.games,
                                        summary.wins,
                                        summary.games - summary.wins,
                                        summary.wins as f64 * 100. / summary.games as f64,
                                        summary.kda
                                    )
                                } else {
                                    "暂无有效样本".into()
                                }),
                        )
                        .child(div().mt_2().text_xs().text_color(p.muted).child(format!(
                            "{} · 查询于 {}",
                            coverage_label(profile), profile.captured
                        )))
                        .when_some(profile.history_error.clone(), |el, error| {
                            el.child(div().mt_2().text_color(p.bad).child(error))
                        }),
                )
                .child(
                    div()
                        .flex()
                        .flex_1()
                        .min_h_0()
                        .child(
                            div()
                                .w(px(280.))
                                .flex_shrink_0()
                                .flex()
                                .flex_col()
                                .border_r_1()
                                .border_color(p.line)
                                .child(div().p_2().flex().gap_1().flex_wrap().children(
                                    ["全部", "峡谷", "普通大乱斗", "海克斯"].into_iter().enumerate().map(
                                        |(i, label)| {
                                            Button::new(("query-filter", i))
                                                .label(label)
                                                .small()
                                                .ghost()
                                                .selected(self.tools.query_filter == i)
                                                .on_click(cx.listener(move |this, _, _, cx| {
                                                    this.tools.query_filter = i;
                                                    // Clearing a filter with no matches also clears detail selection so an unrelated previous game cannot remain visible.
                                                    this.tools.selected_match = this.tools.profile.as_ref()
                                                        .and_then(|profile| profile.matches.iter().position(|m| lux::scouting::usable_match(m, query_filter_mode(i))))
                                                        .unwrap_or(usize::MAX);
                                                    this.request_query_detail();
                                                    cx.notify();
                                                }))
                                        },
                                    ),
                                ))
                                .child(
                                    uniform_list(
                                        "query-matches",
                                        filtered.len(),
                                        cx.processor(
                                            move |this, range: std::ops::Range<usize>, _, cx| {
                                                let Some(profile) = &this.tools.profile else {
                                                    return Vec::new();
                                                };
                                                range
                                                    .map(|slot| {
                                                        let index = filtered[slot];
                                                        let m = &profile.matches[index];
                                                        div().h(px(120.)).pb_2().child(
                                                            div()
                                                                .h_full()
                                                                .id(("query-match", index))
                                                                .role(Role::Button)
                                                                .aria_label(format!(
                                                                    "查询战绩 {} {}",
                                                                    m.name,
                                                                    match_date(m.created)
                                                                ))
                                                                .cursor_pointer()
                                                                .p_3()
                                                                .rounded_md()
                                                                .border_1()
                                                                .border_color(
                                                                    if this.tools.selected_match
                                                                        == index
                                                                    {
                                                                        p.blue
                                                                    } else {
                                                                        p.line
                                                                    },
                                                                )
                                                                .bg(
                                                                    if this.tools.selected_match
                                                                        == index
                                                                    {
                                                                        p.selected
                                                                    } else {
                                                                        p.bg
                                                                    },
                                                                )
                                                                .on_click(cx.listener(
                                                                    move |this, _, _, cx| {
                                                                        this.tools.selected_match =
                                                                            index;
                                                                        this.request_query_detail();
                                                                        cx.notify();
                                                                    },
                                                                ))
                                                                .child(
                                                                    div()
                                                                        .flex()
                                                                        .gap_2()
                                                                        .items_center()
                                                                        .child(this.portrait(
                                                                            m.champion_id,
                                                                            32.,
                                                                            p,
                                                                        ))
                                                                        .child(
                                                                            div().flex_1().child(
                                                                                m.name.clone(),
                                                                            ),
                                                                        )
                                                                        .child(badge(
                                                                            if m.win {
                                                                                "胜利"
                                                                            } else {
                                                                                "失败"
                                                                            },
                                                                            if m.win {
                                                                                p.good
                                                                            } else {
                                                                                p.bad
                                                                            },
                                                                            p.panel,
                                                                        )),
                                                                )
                                                                .child(div().mt_2().child(format!(
                                                                    "{} / {} / {}",
                                                                    m.kills, m.deaths, m.assists
                                                                )))
                                                                .child(
                                                                    div()
                                                                        .text_xs()
                                                                        .text_color(p.muted)
                                                                        .mt_1()
                                                                        .child(format!(
                                                                            "{} · {}",
                                                                            m.queue,
                                                                            match_date(m.created)
                                                                        )),
                                                                ),
                                                        )
                                                    })
                                                    .collect::<Vec<_>>()
                                            },
                                        ),
                                    )
                                    .flex_1()
                                    .min_h_0()
                                    .p_2(),
                                ),
                        )
                        .child(
                            div()
                                .id("query-detail")
                                .overflow_y_scroll()
                                .flex_1()
                                .min_w_0()
                                .p_5()
                                .child(
                                    div().flex().items_center().gap_1().flex_wrap().mb_2()
                                        .child(div().text_xs().text_color(p.muted).mr_1().child("英雄样本"))
                                        .children(summary.heroes.iter().take(5).map(|hero| {
                                            let champion = hero.champion;
                                            Button::new(("query-hero", champion as usize))
                                                .label(self.db.champion(champion).name)
                                                .small().ghost()
                                                .selected(self.tools.query_champion == champion)
                                                .on_click(cx.listener(move |this, _, _, cx| {
                                                    this.tools.query_champion = champion;
                                                    this.prepare_query_hero();
                                                    cx.notify();
                                                }))
                                        })),
                                )
                                .child(self.profile_analysis(
                                    profile,
                                    summary,
                                    self.tools.query_champion,
                                    self.tools.query_hero.as_ref().map(|modes| &modes[self.tools.query_filter.min(3)]),
                                    p,
                                ))
                                .when_some(
                                    profile.matches.get(self.tools.selected_match),
                                    |el, m| el.child(self.match_detail(m, p, cx)),
                                ),
                        ),
                )
            })
    }

    /// mark_battle_refresh retains current records while marking visible human profiles pending.
    fn mark_battle_refresh(&mut self) {
        if let Some(battle) = &self.tools.battle {
            for id in battle
                .players
                .iter()
                .filter(|p| !p.bot)
                .filter_map(|p| p.puuid.as_ref())
            {
                self.tools.scouts.entry(id.clone()).or_default().begin();
            }
        }
    }

    /// retry_scout requests one failed player without disturbing other visible participants.
    /// Unknown, hidden, disconnected, and already-pending participants are ignored.
    /// Both the game key and account identifier accompany the command so a delayed
    /// click cannot enqueue a player from a replaced roster.
    fn retry_scout(&mut self, puuid: String) {
        if !self.connected {
            return;
        }
        let Some(battle) = &self.tools.battle else {
            return;
        };
        if !battle
            .players
            .iter()
            .any(|p| !p.bot && p.puuid.as_ref() == Some(&puuid))
            || self.tools.scouts.get(&puuid).is_some_and(|s| s.updating)
        {
            return;
        }
        let key = battle.key.clone();
        self.tools.scouts.entry(puuid.clone()).or_default().begin();
        self.send(Command::RetryScout { battle: key, puuid });
    }

    /// set_analysis_scope changes the roster's queue scope and requests matching observations.
    /// The data worker refreshes visible players under that scope while preserving
    /// successful snapshots until replacements arrive. Independent query-page scope
    /// remains attached to its own explicit request context.
    fn set_analysis_scope(&mut self, same_mode: bool) {
        self.tools.analysis.same_mode = same_mode;
        self.mark_battle_refresh();
        self.send(Command::AnalysisScope(same_mode));
    }

    /// set_analysis_count applies a new sample depth and reloads the visible roster.
    /// The worker persists the preference and invalidates its old profile cache.
    /// Existing query results retain their original requested count until queried again.
    fn set_analysis_count(&mut self, count: usize) {
        self.tools.analysis.match_count = count.clamp(1, 200);
        self.mark_battle_refresh();
        self.send(Command::AnalysisCount(self.tools.analysis.bounded_count()));
        self.notice = format!(
            "分析最多 {} 场 · 正在刷新玩家数据…",
            self.tools.analysis.bounded_count()
        );
    }

    /// battle_page fits five compact player rows beneath one shared analysis toolbar.
    /// The requested history depth affects aggregates, while each card displays
    /// only ten newest usable results. Source labels distinguish historical review
    /// from live observations without repeating explanatory panels above the teams.
    /// Team rates combine each player's wins and usable sample count, so players
    /// with fewer available games carry less weight. Their historical label and
    /// tooltip keep that aggregate distinct from predicting the current game's result.
    pub(super) fn battle_page(
        &self,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        let battle = self.tools.battle.as_ref();
        let count = self.tools.analysis.bounded_count();
        let expected = battle.map_or(0, |b| {
            b.players
                .iter()
                .filter(|p| p.puuid.is_some() && !p.bot)
                .count()
        });
        let updating = self.tools.scouts.values().filter(|s| s.updating).count();
        let failed = self
            .tools
            .scouts
            .values()
            .filter(|s| !s.updating && s.error.is_some())
            .count();
        let ready = self
            .tools
            .scouts
            .values()
            .filter(|s| !s.updating && s.error.is_none() && s.profile.is_some())
            .count();
        let scope_label = if self.tools.analysis.same_mode {
            battle.map_or_else(
                || "当前队列".into(),
                |b| query_scope_label(Some(b.queue_id), None),
            )
        } else {
            "全部模式".into()
        };
        div()
            .id("battle-workspace")
            .overflow_y_scroll()
            .flex_1()
            .min_h_0()
            .p_4()
            .child(
                div()
                    .flex()
                    .justify_between()
                    .items_center()
                    .gap_3()
                    .mb_2()
                    .child(
                        div()
                            .min_w_0()
                            .child(
                                div()
                                    .text_lg()
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .child("阵容与状态"),
                            )
                            .child(
                                div().text_xs().text_color(p.muted).child(
                                    battle
                                        .map(|b| b.label.clone())
                                        .unwrap_or_else(|| "等待客户端公开阵容".into()),
                                ),
                            ),
                    )
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_1()
                            .flex_wrap()
                            .justify_end()
                            .children([(true, "当前队列"), (false, "全部模式")].into_iter().map(|(same_mode, label)| Button::new(if same_mode { "scope-current" } else { "scope-all" }).label(label).small().selected(self.tools.analysis.same_mode == same_mode).on_click(cx.listener(move |this, _, _, cx| { this.set_analysis_scope(same_mode); cx.notify(); }))))
                            .child(div().id("analysis-depth-help").text_xs().text_color(p.muted).mr_1().child("分析场数")
                                .tooltip(|window,cx|Tooltip::new("按所选队列分页读取，实际样本以卡片为准。先展示首批，再补齐目标；统计排除自定义、重开和非匹配对局。").build(window,cx)))
                            .children([20_usize, 50, 100, 200].into_iter().map(|value| {
                                Button::new(("analysis-count", value))
                                    .label(value.to_string())
                                    .small()
                                    .selected(count == value)
                                    .on_click(cx.listener(move |this, _, _, cx| {
                                        this.set_analysis_count(value);
                                        cx.notify();
                                    }))
                            }))
                            .child(
                                Button::new("battle-refresh")
                                    .label("刷新")
                                    .small()
                                    .disabled(battle.is_none() || !self.connected || updating > 0)
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.mark_battle_refresh();
                                        this.send(Command::RefreshBattle);
                                        this.notice = "正在逐人读取近期战绩…".into();
                                        cx.notify();
                                    })),
                            )
                            .child(
                                Button::new("battle-review")
                                    .label("复盘最近一局")
                                    .small()
                                    .disabled(
                                        self.room.is_some()
                                            || ["GameStart", "InProgress", "Reconnect"]
                                                .contains(&self.phase.as_str()),
                                    )
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.send(Command::ReviewLastGame);
                                        this.notice = "正在读取最近一局的真实玩家列表…".into();
                                        cx.notify();
                                    })),
                            ),
                    ),
            )
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .flex_wrap()
                    .mb_3()
                    .text_xs()
                    .text_color(p.muted)
                    .child(div().text_color(p.good).child("浅绿 胜"))
                    .child(div().text_color(p.bad).child("浅红 负"))
                    .child(format!("{} · 最近10场左新右旧 · 击杀/死亡/助攻", scope_label))
                    .when(battle.is_some(), |el| el.child(format!("已完成 {ready}/{expected} · 更新中 {updating} · 失败 {failed}")))
                    .when_some(battle, |el, battle| {
                        let room = Room {
                            mode: battle.mode,
                            allies: battle
                                .players
                                .iter()
                                .filter(|p| p.ally)
                                .map(|p| Member {
                                    champion: p.champion,
                                    ..Member::default()
                                })
                                .collect(),
                            ..Room::default()
                        };
                        let composition = self.composition(&room);
                        el.child(
                            div()
                                .id("battle-composition")
                                .ml_auto()
                                .text_color(p.blue)
                                .child("阵容观察")
                                .tooltip(move |window, cx| {
                                    Tooltip::new(composition.clone()).build(window, cx)
                                }),
                        )
                    }),
            )
            .when(battle.is_none(), |el| {
                el.child(
                    empty_state(
                        "等待这一局的阵容",
                        "进入英雄选择后自动同步双方公开信息，也可以载入最近一局复盘。",
                        assets::IconName::Users, p,
                    ),
                )
            })
            .when_some(battle, |el, battle| {
                el.child(div().flex().gap_3().items_start().children(
                    [true, false].into_iter().map(|ally| {
                        let side: Vec<_> =
                            battle.players.iter().filter(|p| p.ally == ally).collect();
                        let (games, wins) = side.iter()
                            .filter_map(|player| player.puuid.as_ref())
                            .filter_map(|id| self.tools.scouts.get(id)?.profile.as_ref())
                            .map(|profile| profile.summary_for(self.tools.analysis.same_mode.then_some(battle.mode)))
                            .fold((0_usize, 0_usize), |(games, wins), summary| (games + summary.games, wins + summary.wins));
                        let team_history_tip = format!("按队员有效历史样本加权合并，共 {games} 场；不是本局获胜概率。");
                        div()
                            .flex_1()
                            .min_w_0()
                            .child(
                                div()
                                    .flex()
                                    .justify_between()
                                    .items_center()
                                    .mb_1()
                                    .text_xs()
                                    .child(div().font_weight(FontWeight::SEMIBOLD).child(if ally {
                                        "己方阵容"
                                    } else {
                                        "敌方阵容"
                                    }))
                                    .child(div()
                                        .id(if ally { "friendly-history-rate" } else { "enemy-history-rate" })
                                        .text_color(p.muted)
                                        .child(if games > 0 {
                                            format!("{} 人 · 历史胜率 {:.0}%", side.len(), wins as f64 * 100. / games as f64)
                                        } else {
                                            format!("{} 人 · {}", side.len(), battle.mode.label())
                                        })
                                        .tooltip(move |window, cx| Tooltip::new(team_history_tip.clone()).build(window, cx))),
                            )
                            .children(
                                side.into_iter().enumerate().take(5).map(|(index, player)| {
                                    if self.use_render_cache { self.regions.scouts[usize::from(!ally)][index].clone().cached(
                                        StyleRefinement::default().w_full().h(px(108.)).mb_2(),
                                    ).into_any_element() } else { div().h(px(108.)).mb_2().child(
                                        self.scout_card(player, battle.mode, p, cx)
                                    ).into_any_element() }
                                }),
                            )
                    }),
                ))
            })
    }

    /// scout_region resolves a current participant for one independently cached card.
    /// A stable side and index determine its location only. The card's commands
    /// always use the current player's explicit identity after owner invalidation.
    pub(super) fn scout_region(
        &self,
        ally: bool,
        index: usize,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        self.tools
            .battle
            .as_ref()
            .and_then(|battle| {
                battle
                    .players
                    .iter()
                    .filter(|player| player.ally == ally)
                    .nth(index)
                    .map(|player| {
                        self.scout_card(player, battle.mode, p, cx)
                            .into_any_element()
                    })
            })
            .unwrap_or_else(|| div().into_any_element())
    }

    /// scout_card prioritizes rank, per-game outcomes, and explicit kills/deaths/assists.
    /// Player identity stays attached to its profile, and unavailable histories keep
    /// their error state rather than displaying a fabricated zero-percent record.
    /// Champion names and mode adjustments remain available on the portrait tooltip.
    fn scout_card(
        &self,
        player: &Combatant,
        mode: Mode,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        #[cfg(feature = "diagnostics")]
        super::regions::record_build(2);
        let state = player
            .puuid
            .as_ref()
            .and_then(|id| self.tools.scouts.get(id));
        let profile = state.and_then(|s| s.profile.as_ref());
        let scope = self.tools.analysis.same_mode.then_some(mode);
        let summary = profile.map(|profile| profile.summary_for(scope));
        let updating = state.is_some_and(|s| s.updating);
        let failed = state.is_some_and(|s| s.error.is_some());
        let retry_id = player.puuid.clone();
        let name = profile
            .map(|p| p.name.clone())
            .unwrap_or_else(|| player.name.clone());
        let id = player.puuid.clone();
        let inspected_champion = player.champion;
        let query_queue = self
            .tools
            .analysis
            .same_mode
            .then(|| self.tools.battle.as_ref().map(|battle| battle.queue_id))
            .flatten()
            .filter(|queue| *queue > 0);
        let champion = self.db.champion(player.champion);
        let stats = champion.stats(mode);
        let mut champion_tip = format!("{} · {}", champion.name, lane_label(&player.position));
        if mode != Mode::Ranked && player.champion > 0 {
            champion_tip.push_str(&format!(
                " · 模式强度 {} · {}",
                stats.tier.clone().unwrap_or_else(|| "待补充".into()),
                stats
                    .win_rate
                    .map(|v| format!("{:.1}%", v * 100.))
                    .unwrap_or_else(|| "胜率缺失".into())
            ));
            if let Some(adjustments) = &stats.adjustments {
                for adjustment in adjustments {
                    champion_tip
                        .push_str(&format!(" · {} {}", adjustment.label, adjustment.display));
                }
            } else {
                champion_tip.push_str(" · 调整数据缺失");
            }
        }
        let rank = profile.map(|p| p.rank.clone()).unwrap_or_else(|| {
            if player.puuid.is_none() {
                "身份未公开".into()
            } else if failed || player.bot {
                "段位暂不可用".into()
            } else {
                "段位读取中…".into()
            }
        });
        let status = if player.bot {
            "电脑玩家".into()
        } else if player.puuid.is_none() {
            "身份未公开，等待客户端提供".into()
        } else if updating {
            "正在读取近期战绩…".into()
        } else if let Some(error) = state.and_then(|s| s.error.clone()) {
            error
        } else if profile.is_some_and(|p| p.matches.is_empty()) {
            "数据源返回0场战绩".into()
        } else if scope.is_some() {
            "暂无当前队列有效战绩，可切换全部模式".into()
        } else {
            "暂无有效近期对局".into()
        };
        let freshness = if updating {
            "更新中".into()
        } else if failed && profile.is_some_and(|p| p.history_error.is_none()) {
            "刷新失败·保留上次资料".into()
        } else if failed {
            "读取失败".into()
        } else {
            profile
                .map(|p| {
                    format!(
                        "{}读取",
                        p.captured.rsplit(' ').next().unwrap_or(&p.captured)
                    )
                })
                .unwrap_or_default()
        };
        let rank = if profile.is_some() && !rank.starts_with("单排") {
            format!("单排 {rank}")
        } else {
            rank
        };
        let rank_line = format!(
            "{rank}{}{}",
            if freshness.is_empty() { "" } else { " · " },
            freshness
        );
        let status_tip = format!(
            "{}{}",
            profile
                .map(|p| format!("资料时间 {} · {}", p.captured, coverage_label(p)))
                .unwrap_or_else(|| "尚未读取玩家资料".into()),
            state
                .and_then(|s| s.error.as_ref())
                .map(|e| format!(" · 本次更新失败：{e}"))
                .unwrap_or_default()
        );
        let recent_tip = if self.use_render_cache {
            profile
                .map(|profile| {
                    profile
                        .recent_in(scope)
                        .take(10)
                        .map(|record| {
                            format!(
                                "{} · {} · {} · {} / {} / {}",
                                match_date(record.created),
                                record.name,
                                if record.win { "胜" } else { "负" },
                                record.kills,
                                record.deaths,
                                record.assists
                            )
                        })
                        .collect::<Vec<_>>()
                        .join("\n")
                })
                .unwrap_or_default()
        } else {
            String::new()
        };
        let common_tip = summary
            .map(|summary| {
                summary
                    .heroes
                    .iter()
                    .take(3)
                    .map(|hero| {
                        format!(
                            "{} · {}场 · {}胜",
                            self.db
                                .champions
                                .get(&hero.champion)
                                .map(|hero| hero.name.as_str())
                                .unwrap_or("未知英雄"),
                            hero.games,
                            hero.wins
                        )
                    })
                    .collect::<Vec<_>>()
                    .join("\n")
            })
            .unwrap_or_default();
        // Five compact rows fit the default window while retaining readable per-game outcome strips and aggregate combat values.
        div()
            .id(SharedString::from(format!("scout-{}", player.key)))
            .role(Role::Button)
            .aria_label(format!("查看玩家 {name} 战绩"))
            .h_full()
            .p_2()
            .rounded_lg()
            .border_1()
            .border_color(if player.local { p.blue } else { p.line })
            .bg(p.bg)
            .when(id.is_some() && !player.bot, |el| {
                el.cursor_pointer()
                    .hover(|style| style.bg(p.panel).border_color(p.blue))
                    .on_click(cx.listener(move |this, _, _, cx| {
                        if let Some(id) = &id {
                            this.query_player_in(
                                ProfileTarget::Puuid(id.clone()),
                                query_queue,
                                inspected_champion,
                            );
                            cx.notify();
                        }
                    }))
            })
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(
                        div()
                            .id(SharedString::from(format!("scout-portrait-{}", player.key)))
                            .flex_shrink_0()
                            .child(self.portrait(player.champion, 32., p))
                            .tooltip(move |window, cx| {
                                Tooltip::new(champion_tip.clone()).build(window, cx)
                            }),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .child(
                                div()
                                    .flex()
                                    .items_center()
                                    .gap_1()
                                    .child(
                                        div()
                                            .min_w_0()
                                            .truncate()
                                            .text_sm()
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child(name),
                                    )
                                    .when(player.local, |el| {
                                        el.child(div().text_xs().text_color(p.blue).child("我"))
                                    }),
                            )
                            .child(
                                div()
                                    .id(SharedString::from(format!(
                                        "scout-freshness-{}",
                                        player.key
                                    )))
                                    .text_xs()
                                    .text_color(if failed { p.bad } else { p.muted })
                                    .truncate()
                                    .child(rank_line)
                                    .tooltip(move |window, cx| {
                                        Tooltip::new(status_tip.clone()).build(window, cx)
                                    }),
                            ),
                    )
                    .when(
                        failed && !updating && retry_id.is_some() && !player.bot,
                        |el| {
                            el.child(
                                Button::new(SharedString::from(format!(
                                    "retry-scout-{}",
                                    player.key
                                )))
                                .label("重试")
                                .small()
                                .disabled(!self.connected)
                                .on_click(cx.listener(
                                    move |this, _, _, cx| {
                                        cx.stop_propagation();
                                        if let Some(id) = &retry_id {
                                            this.retry_scout(id.clone());
                                            cx.notify();
                                        }
                                    },
                                )),
                            )
                        },
                    )
                    .child(
                        div()
                            .flex_shrink_0()
                            .text_right()
                            .child(
                                div()
                                    .text_size(px(10.))
                                    .text_color(p.muted)
                                    .child("场均 击杀 / 死亡 / 助攻"),
                            )
                            .child(
                                div().text_sm().font_weight(FontWeight::SEMIBOLD).child(
                                    summary
                                        .as_ref()
                                        .filter(|s| s.games > 0)
                                        .map(|s| {
                                            format!(
                                                "{:.1} / {:.1} / {:.1}",
                                                s.average_kills,
                                                s.average_deaths,
                                                s.average_assists
                                            )
                                        })
                                        .unwrap_or_else(|| "— / — / —".into()),
                                ),
                            ),
                    ),
            )
            .child(
                div()
                    .id(SharedString::from(format!("scout-recent-{}", player.key)))
                    .flex()
                    .gap_1()
                    .mt_1()
                    .h(px(30.))
                    // One strip tooltip preserves all ten game descriptions without registering a separate mouse listener for every small result cell.
                    .when(self.use_render_cache && !recent_tip.is_empty(), |el| {
                        el.tooltip(move |window, cx| {
                            Tooltip::new(recent_tip.clone()).build(window, cx)
                        })
                    })
                    .when_some(profile, |el, profile| {
                        el.children(
                            profile
                                .recent_in(scope)
                                .take(10)
                                .enumerate()
                                .map(|(index, record)| self.scout_result(record, index, p, cx)),
                        )
                    })
                    .when(
                        profile.is_none_or(|p| p.recent_in(scope).next().is_none()),
                        |el| {
                            el.child(
                                div()
                                    .text_xs()
                                    .text_color(if failed { p.bad } else { p.muted })
                                    .child(status),
                            )
                        },
                    ),
            )
            .child(
                div()
                    .flex()
                    .items_center()
                    .justify_between()
                    .gap_2()
                    .mt_1()
                    .text_size(px(10.))
                    .text_color(p.muted)
                    .child(
                        div()
                            .id(SharedString::from(format!(
                                "scout-common-strip-{}",
                                player.key
                            )))
                            .flex()
                            .items_center()
                            .gap_1()
                            .min_w_0()
                            .child("常用")
                            .when(self.use_render_cache && !common_tip.is_empty(), |el| {
                                el.tooltip(move |window, cx| {
                                    Tooltip::new(common_tip.clone()).build(window, cx)
                                })
                            })
                            .when_some(summary.as_ref(), |el, summary| {
                                el.children(summary.heroes.iter().take(3).map(|hero| {
                                    let tip = format!(
                                        "{} · {}场 · {}胜",
                                        self.db
                                            .champions
                                            .get(&hero.champion)
                                            .map(|hero| hero.name.as_str())
                                            .unwrap_or("未知英雄"),
                                        hero.games,
                                        hero.wins
                                    );
                                    div()
                                        .id(SharedString::from(format!(
                                            "scout-common-{}-{}",
                                            player.key, hero.champion
                                        )))
                                        .flex()
                                        .items_center()
                                        .gap_1()
                                        .child(self.portrait(hero.champion, 14., p))
                                        .child(format!("{}", hero.games))
                                        .when(!self.use_render_cache, |el| {
                                            el.tooltip(move |window, cx| {
                                                Tooltip::new(tip.clone()).build(window, cx)
                                            })
                                        })
                                }))
                            }),
                    )
                    .child(
                        div()
                            .flex_shrink_0()
                            .child(match (profile, summary.as_ref()) {
                                (Some(profile), Some(summary)) if summary.games > 0 => format!(
                                    "{} · {}场有效 · {:.0}%胜率 · KDA {:.2}",
                                    if profile.coverage.source.is_empty() {
                                        "来源未知"
                                    } else {
                                        &profile.coverage.source
                                    },
                                    summary.games,
                                    summary.wins as f64 * 100. / summary.games as f64,
                                    summary.kda
                                ),
                                (Some(profile), _) => {
                                    format!("0场有效 / {}场读取", profile.matches.len())
                                }
                                _ => "样本待读取".into(),
                            }),
                    ),
            )
    }

    /// scout_result renders one game with readable outcome tint and individual K/D/A.
    /// Both themes use muted backgrounds and retain a textual win/loss cue.
    /// Shipping builds group descriptions on the containing strip, leaving its
    /// individual cells free of tooltip mouse handlers; diagnostics can compare
    /// the previous independent-cell tooltip behavior against the same data.
    fn scout_result(
        &self,
        record: &MatchRecord,
        index: usize,
        p: Palette,
        cx: &App,
    ) -> impl IntoElement + use<> {
        let dark = cx.theme().is_dark();
        let background = match (record.win, dark) {
            (true, false) => rgb(0xe5f2eb),
            (false, false) => rgb(0xf8e8eb),
            (true, true) => rgb(0x263c35),
            (false, true) => rgb(0x432f36),
        };
        div()
            .id(("scout-result", index))
            .flex_1()
            .min_w_0()
            .h_full()
            .rounded_sm()
            .bg(background)
            .text_color(p.ink)
            .flex()
            .flex_col()
            .items_center()
            .justify_center()
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_1()
                    .child(self.portrait(record.champion_id, 12., p))
                    .child(
                        div()
                            .text_size(px(9.))
                            .text_color(if record.win { p.good } else { p.bad })
                            .child(if record.win { "胜" } else { "负" }),
                    ),
            )
            .child(div().text_size(px(10.)).child(format!(
                "{}/{}/{}",
                record.kills, record.deaths, record.assists
            )))
            .when(!self.use_render_cache, |el| {
                let tip = format!(
                    "{} · {} · {} · {} / {} / {}",
                    match_date(record.created),
                    record.queue,
                    if record.win { "胜利" } else { "失败" },
                    record.kills,
                    record.deaths,
                    record.assists
                );
                el.tooltip(move |window, cx| Tooltip::new(tip.clone()).build(window, cx))
            })
    }

    /// apply_automation validates configured priorities before applying or arming draft rules.
    /// Saving preferences does not turn the runtime on. Enabling requires at
    /// least one active feature with its own configured champion priority list.
    fn apply_automation(&mut self, enable: bool) {
        let settings = &self.tools.settings;
        if enable
            && (!(settings.auto_pick || settings.auto_ban || settings.preselect)
                || (settings.auto_pick || settings.preselect) && settings.picks.is_empty()
                || settings.auto_ban && settings.bans.is_empty())
        {
            self.notice = "请先为已开启的选用／禁用功能添加英雄优先级".into();
            return;
        }
        match self.tools.automation.apply(settings.clone(), enable) {
            Ok(()) => {
                self.tools.auto_status = if enable {
                    "规则已应用 · 自动选禁已开启"
                } else {
                    "规则已保存 · 自动选禁已关闭"
                }
                .into();
                self.notice = self.tools.auto_status.clone();
            }
            Err(e) => self.notice = e.to_string(),
        }
    }

    /// automation_page renders draft rules and a viewport-limited champion library.
    /// The native window width determines fixed tile columns. A bounded list builds
    /// only visible grid rows, while settings and search remain owned by Lux so
    /// scrolling never changes rule order or enables client operations.
    pub(super) fn automation_page(
        &self,
        p: Palette,
        window: &Window,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        let config = &self.tools.settings;
        let query = self.tools.hero_query.read(cx).value().to_lowercase();
        let heroes: Vec<u32> = self
            .db
            .champions
            .values()
            .filter(|c| {
                query.is_empty()
                    || c.name.to_lowercase().contains(&query)
                    || c.title.to_lowercase().contains(&query)
                    || c.alias.to_lowercase().contains(&query)
                    || c.id.to_string() == query
            })
            .map(|hero| hero.id)
            .collect();
        // Reserve the navigation rail, page padding, and library inset before allocating 222-pixel tiles with eight-pixel gaps, preventing offscreen columns.
        let columns = ((f32::from(window.viewport_size().width) - 232.) / 230.)
            .floor()
            .max(1.) as usize;
        let rows = heroes.len().div_ceil(columns);
        div().id("automation-workspace").overflow_y_scroll().flex_1().min_h_0().p_6()
            .child(
                div().flex().justify_between().items_center().mb_5()
                    .child(div()
                        .child(div().text_base().font_weight(FontWeight::SEMIBOLD).child("运行与规则"))
                        .child(div().mt_1().text_xs().text_color(p.muted).child("仅操作本人选禁 · 英雄不可用时按优先级顺延")))
                    .child(Button::new("save-automation").label("保存并应用规则").primary()
                        .on_click(cx.listener(|this,_,_,cx| {
                            this.apply_automation(this.tools.automation.enabled());
                            cx.notify();
                        })))
            )
            .child(
                div().p_5().rounded_xl().bg(p.bg).border_1()
                    .border_color(if self.tools.automation.enabled(){p.blue}else{p.line})
                    .flex().items_center().justify_between().gap_4()
                    .child(div().flex().items_center().gap_4()
                        .child(div().size(px(40.)).rounded_lg().bg(p.selected).text_color(p.blue)
                            .flex().items_center().justify_center().child(assets::IconName::ShieldCheck))
                        .child(div()
                            .child(div().font_weight(FontWeight::SEMIBOLD).child(if self.tools.automation.enabled(){"自动选禁已开启"}else{"自动选禁已关闭"}))
                            .child(div().mt_1().text_xs().text_color(p.muted).child(self.tools.auto_status.clone()))
                            .child(div().mt_1().text_xs().text_color(p.muted).child("每次启动默认关闭，开启后按已应用规则执行"))))
                    .child(Switch::new("toggle-automation").checked(self.tools.automation.enabled())
                        .accessibility_label("自动选禁总开关").color(p.blue)
                        .on_click(cx.listener(|this,enabled,_,cx| {
                            if *enabled { this.apply_automation(true); }
                            else {
                                this.tools.automation.stop();
                                this.tools.auto_status="自动选禁已关闭".into();
                                this.notice="自动选禁已关闭".into();
                            }
                            cx.notify();
                        })))
            )
            .child(
                div().mt_5().p_5().rounded_xl().bg(p.bg).border_1().border_color(p.line)
                    .child(section_title("选禁行为","修改后保存并应用",p))
                    .children([
                        vec![(0,"自动选用","按候选顺序选择英雄",config.auto_pick),(1,"自动禁用","按候选顺序禁用英雄",config.auto_ban),(2,"提前预选","在本人回合前表达意向",config.preselect)],
                        vec![(3,"轮到时锁定","符合条件时完成锁定",config.lock_pick),(4,"保护队友预选","跳过队友预选的禁用目标",config.protect_allies),(5,"保留手动选择","不覆盖已手动选择的英雄",config.preserve_manual)],
                    ].into_iter().map(|row| {
                        div().flex().gap_4().mt_3().children(row.into_iter().map(|(index,label,description,enabled)| {
                            div().flex_1().min_w_0().p_3().rounded_lg().bg(p.panel)
                                .child(div().flex().justify_between().items_center().gap_2()
                                    .child(div().text_sm().font_weight(FontWeight::MEDIUM).child(label))
                                    .child(Switch::new(("auto-option",index as usize)).checked(enabled).small()
                                        .accessibility_label(label).color(p.blue)
                                        .on_click(cx.listener(move|this,value,_,cx| {
                                            let config=&mut this.tools.settings;
                                            let flag=match index {
                                                0=>&mut config.auto_pick, 1=>&mut config.auto_ban,
                                                2=>&mut config.preselect, 3=>&mut config.lock_pick,
                                                4=>&mut config.protect_allies, _=>&mut config.preserve_manual,
                                            };
                                            *flag = *value;
                                            this.notice="规则已修改，保存并应用后生效".into();
                                            cx.notify();
                                        }))))
                                .child(div().mt_2().text_xs().text_color(p.muted).child(description))
                        }))
                    }))
            )
            .child(
                div().mt_5().p_4().rounded_xl().bg(p.bg).border_1().border_color(p.line)
                    .child(div().flex().items_center().flex_wrap().gap_2()
                        .child(div().text_xs().text_color(p.muted).mr_2().child("操作延时"))
                        .children([0,2,5,10].into_iter().map(|delay| {
                            Button::new(("auto-delay",delay as usize)).label(format!("{delay} 秒")).small()
                                .selected(config.delay_seconds==delay)
                                .on_click(cx.listener(move|this,_,_,cx| {this.tools.settings.delay_seconds=delay;cx.notify();}))
                        })))
                    .child(div().flex().items_center().flex_wrap().gap_2().mt_3()
                        .child(div().text_xs().text_color(p.muted).mr_2().child("分路规则"))
                        .children([("","全部"),("top","上路"),("jungle","打野"),("middle","中路"),("bottom","下路"),("utility","辅助")].into_iter().enumerate().map(|(i,(position,label))| {
                            Button::new(("auto-position",i)).label(label).small().selected(config.position==position)
                                .on_click(cx.listener(move|this,_,_,cx| {this.tools.settings.position=position.into();cx.notify();}))
                        })))
            )
            .child(div().flex().gap_4().mt_5().child(self.priority_list(false,p,cx)).child(self.priority_list(true,p,cx)))
            .child(div().mt_5().p_4().rounded_xl().bg(p.bg).border_1().border_color(p.line).child(section_title("添加英雄","数字为优先顺序 · 点击移除 · 使用 ↑ 调整顺序",p))
                .child(div().max_w(px(500.)).mb_3().child(Input::new(&self.tools.hero_query).aria_label("搜索自动选禁英雄")))
                .child(div().h(px(336.)).overflow_hidden().child(
                    uniform_list("automation-heroes", rows, cx.processor(move |this, range: std::ops::Range<usize>, _, cx| {
                        range.map(|row| {
                            div().h(px(56.)).flex().gap_2().children(
                                heroes.iter().skip(row * columns).take(columns).map(|id| {
                                    let id = *id;
                                    let config = &this.tools.settings;
                                    div().w(px(222.)).h(px(48.)).flex_shrink_0().p_2().bg(p.panel).border_1().border_color(p.line).rounded_md().flex().items_center().gap_2()
                                        .child(this.portrait(id,28.,p)).child(div().flex_1().min_w_0().truncate().text_xs().child(this.db.champions.get(&id).map(|hero| hero.name.clone()).unwrap_or_else(|| format!("英雄 {id}"))))
                                        .child(Button::new(("add-pick",id as usize)).label("选").small().disabled(config.picks.contains(&id)).on_click(cx.listener(move|this,_,_,cx|{if !this.tools.settings.picks.contains(&id){this.tools.settings.picks.push(id);}cx.notify();})))
                                        .child(Button::new(("add-ban",id as usize)).label("禁").small().disabled(config.bans.contains(&id)).on_click(cx.listener(move|this,_,_,cx|{if !this.tools.settings.bans.contains(&id){this.tools.settings.bans.push(id);}cx.notify();})))
                                })
                            )
                        }).collect::<Vec<_>>()
                    })).size_full()
                )))
            .child(div().mt_5().text_xs().text_color(p.muted).child("预选不等于锁定；随机选人模式不执行自动选禁。选择已被占用、禁用或无法验证时保持等待。停止后不会提交新请求，已发出的请求无法撤回。"))
            .child(div().mt_5().child(section_title("本次运行记录","仅保留最近 20 条",p)).children(self.tools.actions.iter().map(|line|div().py_1().text_xs().text_color(p.muted).child(line.clone()))))
    }

    /// priority_list renders editable pick or ban ordering without changing the live client.
    fn priority_list(
        &self,
        ban: bool,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        let list = if ban {
            &self.tools.settings.bans
        } else {
            &self.tools.settings.picks
        };
        div()
            .flex_1()
            .min_w_0()
            .p_4()
            .rounded_xl()
            .bg(p.bg)
            .border_1()
            .border_color(p.line)
            .child(section_title(
                if ban {
                    "禁用优先级"
                } else {
                    "选用优先级"
                },
                "不可用时依次顺延",
                p,
            ))
            .when(list.is_empty(), |el| {
                el.child(div().text_color(p.muted).child("从下方英雄库添加"))
            })
            .children(list.iter().copied().enumerate().map(|(index, id)| {
                div()
                    .flex()
                    .gap_2()
                    .items_center()
                    .mb_2()
                    .child(badge(&(index + 1).to_string(), p.blue, p.panel))
                    .child(self.portrait(id, 26., p))
                    .child(div().flex_1().child(self.db.champion(id).name))
                    .child(
                        Button::new((if ban { "ban-up" } else { "pick-up" }, index))
                            .label("↑")
                            .small()
                            .disabled(index == 0)
                            .on_click(cx.listener(move |this, _, _, cx| {
                                let list = if ban {
                                    &mut this.tools.settings.bans
                                } else {
                                    &mut this.tools.settings.picks
                                };
                                if index > 0 && index < list.len() {
                                    list.swap(index, index - 1);
                                }
                                cx.notify();
                            })),
                    )
                    .child(
                        Button::new((if ban { "ban-remove" } else { "pick-remove" }, index))
                            .label("移除")
                            .small()
                            .ghost()
                            .on_click(cx.listener(move |this, _, _, cx| {
                                let list = if ban {
                                    &mut this.tools.settings.bans
                                } else {
                                    &mut this.tools.settings.picks
                                };
                                list.retain(|hero| *hero != id);
                                cx.notify();
                            })),
                    )
            }))
    }
}

/// lane_label translates public role assignments and preserves unknown positions explicitly.
fn lane_label(position: &str) -> &str {
    match position.to_ascii_lowercase().as_str() {
        "top" => "上路",
        "jungle" => "打野",
        "middle" | "mid" => "中路",
        "bottom" | "bot" => "下路",
        "utility" | "support" => "辅助",
        _ => "分路待确认",
    }
}
