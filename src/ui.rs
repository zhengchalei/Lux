//! ui.rs renders Lux's native match history and champion selection workspace.
//! The view owns presentation choices while client and provider work runs through
//! a background channel. Rooms exist only when observed in the local client,
//! and leaving selection clears the displayed live room. Personal history starts
//! empty and is populated exclusively by client replies; previously read records
//! retain their capture label when the connection is lost.
//!
//! Champion inspection is separate from explicitly armed selection automation.
//! The tools module owns that runtime switch, user queries, and visible player
//! scouting, while the original views retain history and champion observations.
//! Recommended heroes are intersected with the current available pool, and a
//! selected public enemy is associated with a manually chosen lane before a
//! source lookup. Match contribution bars and radar share raw measurements while
//! retaining separate all-match normalization and friendly-team percentage labels.
//! Uniform history rows defer construction until they intersect the viewport,
//! keeping offscreen records out of pointer and scrolling redraw work. Observed
//! account changes clear personal views and invalidate pending query identities
//! before new-account player updates are accepted by the presentation state.
//! A persistent navigation rail separates workspace changes from the account
//! header. Connection discovery and provider refresh run automatically, leaving
//! only contextual page actions and an explicit automation switch in settings.

use gpui_kit::{
    component::{
        ActiveTheme, Disableable, Selectable, Sizable, Theme, ThemeMode,
        button::{Button, ButtonVariants},
    },
    prelude::FluentBuilder as _,
    *,
};
use lux::{
    client::{self, Command, Update},
    model::{Database, MatchRecord, Member, Mode, Room},
};
use std::{
    collections::{BTreeMap, BTreeSet},
    sync::{Arc, mpsc::Sender},
};

mod tools;
use tools::Tools;
#[cfg(feature = "diagnostics")]
mod diagnostics;
mod player_analysis;
mod regions;
use regions::{RegionKind, Regions};

// Portrait bytes are generated from the bundled public champion assets during compilation.
include!(concat!(env!("OUT_DIR"), "/portraits.rs"));

/// Palette captures theme-aware product colors for one native render pass.
#[derive(Clone, Copy)]
struct Palette {
    bg: Hsla,       // Raised card and content surface color.
    canvas: Hsla,   // Main workspace background behind cards.
    panel: Hsla,    // Quiet secondary surface color.
    ink: Hsla,      // Main readable foreground color.
    muted: Hsla,    // Secondary annotation foreground color.
    line: Hsla,     // Structural divider and border color.
    blue: Hsla,     // Stable selection and chart color.
    selected: Hsla, // Subtle active selection surface.
    good: Hsla,     // Favorable outcome foreground color.
    bad: Hsla,      // Unfavorable outcome foreground color.
}

impl Palette {
    /// new resolves semantic toolkit colors and product accents for the active theme.
    fn new(cx: &App) -> Self {
        let dark = cx.theme().is_dark();
        Self {
            bg: if dark {
                rgb(0x1c2638).into()
            } else {
                rgb(0xffffff).into()
            },
            canvas: if dark {
                rgb(0x131c2b).into()
            } else {
                rgb(0xf3f5fa).into()
            },
            panel: if dark {
                rgb(0x222e43).into()
            } else {
                rgb(0xf7f9fd).into()
            },
            ink: if dark {
                rgb(0xe4ebf8).into()
            } else {
                rgb(0x202e48).into()
            },
            muted: if dark {
                rgb(0xa1aec5).into()
            } else {
                rgb(0x69788f).into()
            },
            line: if dark {
                rgb(0x34415a).into()
            } else {
                rgb(0xe1e7f1).into()
            },
            blue: if dark {
                rgb(0x9dbbfa).into()
            } else {
                rgb(0x4b68d4).into()
            },
            selected: if dark {
                rgb(0x283449).into()
            } else {
                rgb(0xedf1ff).into()
            },
            good: if dark {
                rgb(0x93d1b4).into()
            } else {
                rgb(0x28705b).into()
            },
            bad: if dark {
                rgb(0xe3a1ad).into()
            } else {
                rgb(0xa24755).into()
            },
        }
    }
}

/// Lux owns native presentation state and the sanitized data snapshots it displays.
pub struct Lux {
    db: Database,              // Public catalog and labeled source snapshots.
    history: Vec<MatchRecord>, // Current visible combat record collection.
    history_label: String,     // Source and capture context for history.
    history_live: bool,        // History belongs to the current live connection.
    account: Option<lux::scouting::ClientAccount>, // Last observed account and region.
    // The startup flag survives discovery until an account event confirms readiness;
    // consuming it prevents subsequent reconnects from repeating the initial query.
    startup_self_query: bool, // Pending one-shot current-account startup lookup.
    connected: bool,          // Local client transport is currently healthy.
    connection: String,       // Visible connection or permission explanation.
    phase: String,            // Most recently observed client gameflow phase.
    client_patch: String,     // Client patch metadata when readable.
    room: Option<Room>,       // Current sanitized live selection session.
    page: usize,              // Active application workspace navigation index.
    selected_match: usize,    // Selected combat record list index.
    history_filter: usize,    // All, ranked, or ARAM list filter.
    detail_tab: usize,        // Overview or contribution inspection tab.
    metric: usize,            // Currently compared raw combat measurement.
    selected_hero: u32,       // Champion inspected without changing client selection.
    enemy: u32,               // Public opposing champion chosen as target.
    position: usize,          // User-selected matchup lane index.
    notice: String,           // Most recent visible background operation result.
    source_status: String,    // Background public refresh progress and retry state.
    commands: Sender<Command>, // Background data lookup command channel.
    tools: Tools,             // Queries, scouting, and explicit automation controls.
    portraits: BTreeMap<u32, Arc<Image>>, // Stable decoded-source portrait identities.
    requested_details: BTreeSet<(u32, String)>, // Hero and source contexts already requested.
    _updates: Task<()>,       // Lifetime of the native update receiver task.
    regions: Regions,         // Persistent native surfaces with independent hover invalidation.
    // Ordinary builds always use cached regions; diagnostic builds can compare
    // the same data against the previous whole-workspace invalidation behavior.
    use_render_cache: bool, // Internal diagnostic comparison switch.
    #[cfg(feature = "diagnostics")]
    frame_probe: Option<diagnostics::FrameProbe>, // Opt-in bounded frame benchmark state.
}

impl Lux {
    /// new loads public champion observations and starts native background synchronization.
    /// Personal history and rooms start empty until the client supplies them.
    /// Public balance snapshots provide hero metadata without inventing player data.
    /// Worker updates are drained together before repainting to avoid unnecessary
    /// intermediate frames. Waiting on the asynchronous receiver lets incoming
    /// state wake this view without a periodic timer, and the receiver task ends
    /// when its view disappears or the background runtime closes its channel.
    /// The optional startup self-query waits for the first account observation
    /// instead of issuing a command before client discovery has completed.
    pub fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let db = Database::bundled();
        let history = Vec::new();
        let history_label = "等待从客户端读取战绩".into();
        // A malformed settings file restores empty rules with the runtime switch off rather than preventing history access.
        let settings = lux::automation::Settings::load().unwrap_or_default();
        // Malformed analysis preferences fall back to fifty records, leaving account lookup available without changing draft automation permissions.
        let analysis = lux::scouting::AnalysisSettings::load().unwrap_or_default();
        let (commands, receiver, automation) =
            client::start(db.clone(), settings.clone(), analysis.clone());
        let tools = Tools::new(settings, automation, analysis, window, cx);
        let updates = cx.spawn(async move |this, cx| {
            while let Ok(first) = receiver.recv().await {
                let mut messages = vec![first];
                // Bound each burst so a busy network cannot monopolize the UI task.
                while messages.len() < 128 {
                    match receiver.try_recv() {
                        Ok(message) => messages.push(message),
                        Err(_) => break,
                    }
                }
                let full_burst = messages.len() == 128;
                // Apply a received burst before notifying so related player updates do not each request an intermediate frame.
                if this
                    .update(cx, |this, cx| {
                        for message in messages {
                            this.receive(message, cx);
                        }
                        cx.notify();
                    })
                    .is_err()
                {
                    break;
                }
                if full_burst {
                    // Yield only after a full burst so queued updates cannot keep an immediately ready receiver ahead of user input indefinitely.
                    cx.background_executor()
                        .timer(std::time::Duration::from_millis(1))
                        .await;
                }
            }
        });
        let portraits = PORTRAITS
            .iter()
            .map(|(id, bytes)| {
                (
                    *id,
                    Arc::new(Image::from_bytes(ImageFormat::Jpeg, bytes.to_vec())),
                )
            })
            .collect();
        let startup: Vec<_> = std::env::args().collect();
        let mut view = Self {
            db,
            history,
            history_label,
            history_live: false,
            account: None,
            startup_self_query: startup.iter().any(|arg| arg == "--query-self"),
            connected: false,
            connection: "正在查找英雄联盟客户端…".into(),
            phase: String::new(),
            client_patch: String::new(),
            room: None,
            page: 0,
            selected_match: 0,
            history_filter: 0,
            detail_tab: 0,
            metric: 0,
            selected_hero: 0,
            enemy: 0,
            position: 2,
            notice: String::new(),
            source_status: "强度数据等待自动更新".into(),
            commands,
            tools,
            portraits,
            requested_details: BTreeSet::new(),
            _updates: updates,
            regions: Regions::new(&cx.entity(), cx),
            use_render_cache: {
                #[cfg(feature = "diagnostics")]
                {
                    !startup.iter().any(|arg| arg == "--uncached-ui")
                }
                #[cfg(not(feature = "diagnostics"))]
                {
                    true
                }
            },
            #[cfg(feature = "diagnostics")]
            frame_probe: diagnostics::FrameProbe::from_args(&startup),
        };
        if let Some(index) = startup.iter().position(|arg| arg == "--page") {
            view.page = match startup.get(index + 1).map(String::as_str) {
                Some("query") => 2,
                Some("battle") => 3,
                Some("automation") => 4,
                Some("draft") => 1,
                _ => 0,
            };
        }
        if view.startup_self_query {
            view.page = 2;
            view.notice = "连接客户端后将自动查询当前账号…".into();
        }
        if startup.iter().any(|arg| arg == "--review-last") {
            view.page = 3;
            view.send(Command::ReviewLastGame);
        }
        view
    }

    /// receive applies client observations while retaining explicit history capture context.
    /// A disconnect invalidates the live room and history freshness label. Public
    /// updates replace source observations only; they cannot replace live history.
    /// The selected record is retained by match identifier when refreshed history
    /// still contains it, preventing an unrelated row from silently becoming selected.
    /// Entering a new live selection opens the assistant, and a selected live
    /// history summary requests its complete roster before contribution analysis.
    /// Inspection follows local hero changes while it still refers to the previous
    /// local hero; explicitly inspecting a different hero preserves that choice.
    /// The first account event consumes any startup self-query after establishing
    /// its account context, and later account events keep ordinary query behavior.
    fn receive(&mut self, update: Update, cx: &mut Context<Self>) {
        match update {
            Update::Account(account) => {
                if self
                    .account
                    .as_ref()
                    .is_some_and(|old| !old.same_account(&account))
                {
                    // New accounts start with empty personal data; previously issued query results are rejected by Tools' advanced request identity.
                    self.history.clear();
                    self.history_live = false;
                    self.history_label = "账号已切换，等待读取战绩".into();
                    self.selected_match = 0;
                    self.room = None;
                    self.enemy = 0;
                    self.db.matchups.clear();
                    self.selected_hero = 0;
                    self.tools.clear_account();
                    self.notice = "已切换账号，正在重新读取资料 · 自动选禁已关闭".into();
                }
                self.account = Some(account);
                // Consume readiness-dependent startup work before enqueueing so repeated account events cannot duplicate the same initial lookup.
                if std::mem::take(&mut self.startup_self_query) {
                    self.open_self_query();
                }
            }
            Update::Live {
                connected,
                status,
                phase,
                room,
                patch,
            } => {
                if self.connected && !connected && self.history_live {
                    self.history_live = false;
                    self.history_label = format!("已断开 · 上次读取 {}", self.history_label);
                }
                self.connected = connected;
                self.connection = status;
                if ["GameStart", "InProgress"].contains(&phase.as_str())
                    && self.phase != phase
                    && self.page == 1
                {
                    self.page = 3;
                }
                self.phase = phase;
                self.client_patch = patch;
                if let Some(room) = &room {
                    if self.room.is_none() {
                        self.page = 1;
                    }
                    if self.selected_hero == 0
                        || self.room.is_none()
                        || self.room.as_ref().is_some_and(|old| {
                            old.allies
                                .iter()
                                .any(|m| m.local && m.champion == self.selected_hero)
                        })
                    {
                        self.selected_hero = room
                            .allies
                            .iter()
                            .find(|m| m.local)
                            .map(|m| m.champion)
                            .unwrap_or(0);
                    }
                }
                if !room
                    .as_ref()
                    .is_some_and(|r| r.enemies.iter().any(|m| m.champion == self.enemy))
                {
                    self.enemy = 0;
                }
                self.room = room;
                self.request_room_details();
            }
            Update::History {
                matches,
                captured,
                coverage,
            } => {
                let selected = self.history.get(self.selected_match).map(|m| m.id);
                self.history = matches;
                self.selected_match = selected
                    .and_then(|id| self.history.iter().position(|m| m.id == id))
                    .unwrap_or(0);
                self.history_label = format!(
                    "{} · {} 场读取 / {} 场有效 / {} 场完整 · {captured}",
                    coverage.source, coverage.fetched, coverage.valid, coverage.complete
                );
                self.history_live = true;
                if let Some(record) = self.history.get(self.selected_match)
                    && record.players.len() < 10
                {
                    self.send(Command::Detail(record.id));
                }
            }
            Update::Detail(record) => {
                if let Some(old) = self.history.iter_mut().find(|m| m.id == record.id) {
                    *old = record;
                }
            }
            Update::Public(db) => {
                self.db = db;
                self.request_room_details();
            }
            Update::Message(message) => self.notice = message,
            Update::SourceStatus(status) => self.source_status = status,
            other => self.receive_tools(other, cx),
        }
    }

    /// send queues data lookup work and reports a stopped worker visibly.
    fn send(&mut self, command: Command) {
        if self.commands.send(command).is_err() {
            self.notice = "数据服务已停止，请重新启动 Lux".into();
        }
    }

    /// portrait renders an embedded champion image or a neutral identifier fallback.
    fn portrait(&self, id: u32, size: f32, p: Palette) -> AnyElement {
        if let Some(image) = self.portraits.get(&id) {
            img(image.clone())
                .size(px(size))
                .rounded(px(6.))
                .flex_shrink_0()
                .into_any_element()
        } else {
            div()
                .size(px(size))
                .rounded(px(6.))
                .bg(p.panel)
                .flex()
                .items_center()
                .justify_center()
                .text_xs()
                .text_color(p.muted)
                .child(if id == 0 { "?".into() } else { id.to_string() })
                .into_any_element()
        }
    }

    /// inspect updates local hero detail and requests matching Mayhem adjustments.
    /// It never chooses or locks a champion inside League. Already known balance
    /// observations remain readable while an optional public refresh is pending.
    fn inspect(&mut self, id: u32, cx: &mut Context<Self>) {
        self.selected_hero = id;
        self.request_room_details();
        cx.notify();
    }

    /// request_room_details queues missing Mayhem adjustments for the visible roster.
    /// Each hero and source context is requested once automatically, including
    /// the inspected hero and current legal pool. A failed request remains
    /// manually retryable through the inspector, without flooding the worker.
    fn request_room_details(&mut self) {
        let Some(room) = self.room.as_ref().filter(|r| r.mode == Mode::Mayhem) else {
            return;
        };
        let ids: BTreeSet<_> = room
            .allies
            .iter()
            .map(|m| m.champion)
            .chain(room.available.iter().copied())
            .chain([self.selected_hero])
            .filter(|id| *id > 0)
            .collect();
        for id in ids {
            let champion = self.db.champion(id);
            if champion.mayhem.adjustments.is_none()
                && self.requested_details.insert((id, champion.mayhem.source))
            {
                // Record the request before enqueueing: subsequent source snapshots may arrive before this hero's network response does.
                self.send(Command::InspectChampion(id));
            }
        }
    }

    /// sidebar renders persistent navigation and the theme control outside workspace actions.
    /// Navigation preserves loaded records and local rule edits. The runtime
    /// automation indicator opens its settings page; enabling and stopping remain
    /// explicit actions inside that page rather than global header controls.
    fn sidebar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let muted: Hsla = rgb(0x9daac4).into();
        div()
            .w(px(160.))
            .h_full()
            .flex_shrink_0()
            .flex()
            .flex_col()
            .bg(rgb(0x19243b))
            .text_color(rgb(0xe9eefb))
            .child(
                div()
                    .px_5()
                    .pt_6()
                    .pb_8()
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_3()
                            .child(
                                div()
                                    .size(px(32.))
                                    .rounded_lg()
                                    .bg(rgb(0x637deb))
                                    .flex()
                                    .items_center()
                                    .justify_center()
                                    .font_weight(FontWeight::BOLD)
                                    .text_lg()
                                    .child("L"),
                            )
                            .child(div().text_xl().font_weight(FontWeight::BOLD).child("LUX")),
                    )
                    .child(
                        div()
                            .mt_3()
                            .text_xs()
                            .text_color(muted)
                            .child("英雄联盟 · 对局助手"),
                    ),
            )
            .child(
                div()
                    .px_5()
                    .mb_3()
                    .text_xs()
                    .text_color(muted)
                    .child("工作台"),
            )
            .child(
                div().px_3().flex().flex_col().gap_2().children(
                    [
                        (0, "战绩分析", assets::IconName::ChartBar),
                        (1, "选人助手", assets::IconName::Swords),
                        (3, "对战助手", assets::IconName::Users),
                        (2, "战绩查询", assets::IconName::Search),
                        (4, "自动选禁", assets::IconName::Settings),
                    ]
                    .into_iter()
                    .map(|(page, label, icon)| {
                        let selected = self.page == page;
                        div()
                            .id(("workspace-nav", page))
                            .role(Role::Button)
                            .aria_label(label)
                            .h(px(44.))
                            .px_3()
                            .rounded_lg()
                            .flex()
                            .items_center()
                            .gap_3()
                            .cursor_pointer()
                            .bg(if selected {
                                rgb(0x344977)
                            } else {
                                rgb(0x19243b)
                            })
                            .text_color(if selected {
                                rgb(0xffffff)
                            } else {
                                rgb(0xb4c0d7)
                            })
                            .hover(|style| style.bg(rgb(0x2b3b5e)))
                            .child(div().size(px(18.)).child(icon))
                            .child(
                                div()
                                    .font_weight(if selected {
                                        FontWeight::SEMIBOLD
                                    } else {
                                        FontWeight::NORMAL
                                    })
                                    .child(label),
                            )
                            .when(page == 4 && self.tools.automation.enabled(), |el| {
                                el.child(
                                    div()
                                        .ml_auto()
                                        .size(px(6.))
                                        .rounded_full()
                                        .bg(rgb(0x8bd3b2)),
                                )
                            })
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.page = page;
                                cx.notify();
                            }))
                    }),
                ),
            )
            .child(
                div()
                    .mt_auto()
                    .px_4()
                    .pb_4()
                    .child(
                        div()
                            .id("theme-control")
                            .role(Role::Button)
                            .aria_label("切换浅色或深色主题")
                            .flex()
                            .items_center()
                            .gap_3()
                            .p_3()
                            .rounded_lg()
                            .cursor_pointer()
                            .text_color(muted)
                            .hover(|style| style.bg(rgb(0x2b3b5e)))
                            .child(if cx.theme().is_dark() {
                                assets::IconName::Sun
                            } else {
                                assets::IconName::Moon
                            })
                            .child(if cx.theme().is_dark() {
                                "切换浅色"
                            } else {
                                "切换深色"
                            })
                            .on_click(|_, window, cx| {
                                let mode = if cx.theme().is_dark() {
                                    ThemeMode::Light
                                } else {
                                    ThemeMode::Dark
                                };
                                Theme::change(mode, Some(window), cx);
                            }),
                    )
                    .child(
                        div()
                            .mt_3()
                            .px_3()
                            .text_xs()
                            .text_color(rgb(0xaec0f9))
                            .child("高帧模式 · 自适应"),
                    )
                    .child(
                        div()
                            .mt_2()
                            .px_3()
                            .text_size(px(10.))
                            .text_color(muted)
                            .child("LUX 0.1 · Windows"),
                    ),
            )
    }

    /// header presents the account and automatic connection state without manual controls.
    /// The status tooltip retains permission and discovery explanations. Connection
    /// loss leaves the last account visibly labeled while the worker discovers
    /// new authentication automatically, so cached records keep their identity.
    fn header(&self, p: Palette) -> impl IntoElement {
        let (title, subtitle) = match self.page {
            0 => ("战绩分析", "从每一局，找到进步的方向"),
            1 => ("选人助手", "看清阵容，选好这一局"),
            2 => ("战绩查询", "了解一位召唤师的近期表现"),
            3 => ("对战助手", "双方阵容与近期状态，一眼看清"),
            _ => ("自动选禁", "按你的顺序，完成英雄选择"),
        };
        let connection_tip = self.connection.clone();
        div()
            .h(px(76.))
            .flex_shrink_0()
            .px_6()
            .flex()
            .items_center()
            .justify_between()
            .gap_4()
            .bg(p.bg)
            .border_b_1()
            .border_color(p.line)
            .child(
                div()
                    .child(
                        div()
                            .text_lg()
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(title),
                    )
                    .child(div().mt_1().text_xs().text_color(p.muted).child(subtitle)),
            )
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_4()
                    .when_some(self.account.as_ref(), |el, account| {
                        el.child(
                            div()
                                .text_right()
                                .child(
                                    div()
                                        .text_sm()
                                        .font_weight(FontWeight::MEDIUM)
                                        .child(account.name.clone()),
                                )
                                .child(div().mt_1().text_xs().text_color(p.muted).child(format!(
                                    "{}{}",
                                    account.region,
                                    if self.connected {
                                        ""
                                    } else {
                                        " · 上次连接"
                                    }
                                ))),
                        )
                    })
                    .child(
                        div()
                            .id("automatic-connection-status")
                            .px_3()
                            .py_2()
                            .rounded_lg()
                            .bg(p.panel)
                            .flex()
                            .items_center()
                            .gap_2()
                            .text_xs()
                            .text_color(if self.connected { p.good } else { p.muted })
                            .child(div().size(px(6.)).rounded_full().bg(if self.connected {
                                p.good
                            } else {
                                p.muted
                            }))
                            .child(if self.connected {
                                phase_label(&self.phase)
                            } else {
                                "自动连接中"
                            })
                            .tooltip(move |window, cx| {
                                gpui_kit::component::tooltip::Tooltip::new(connection_tip.clone())
                                    .build(window, cx)
                            }),
                    ),
            )
    }

    /// history_page renders visible history rows beside the currently selected detail.
    /// Uniform row heights permit viewport-only construction. Filtered positions map
    /// back to original history indexes so clicking after scrolling or filtering
    /// requests the correct record and preserves the existing detail selection.
    fn history_page(&self, p: Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let filtered: Vec<usize> = self
            .history
            .iter()
            .enumerate()
            .filter(|(_, m)| match self.history_filter {
                1 => [420, 440].contains(&m.queue_id),
                2 => [450, 2400].contains(&m.queue_id),
                _ => true,
            })
            .map(|(i, _)| i)
            .collect();
        let wins = self.history.iter().filter(|m| m.win).count();
        let list = div()
            .w(px(270.))
            .flex_shrink_0()
            .h_full()
            .border_r_1()
            .border_color(p.line)
            .flex()
            .flex_col()
            .child(
                div()
                    .px_4()
                    .pt_4()
                    .pb_3()
                    .child(
                        div()
                            .text_base()
                            .font_weight(FontWeight::SEMIBOLD)
                            .child("最近战绩"),
                    )
                    .child(div().text_xs().mt_1().text_color(p.muted).child(
                        if self.history_live || !self.history.is_empty() {
                            format!(
                                "{} 场 · {} 胜 {} 负",
                                self.history.len(),
                                wins,
                                self.history.len() - wins
                            )
                        } else {
                            "等待客户端战绩".into()
                        },
                    ))
                    .child(
                        div().flex().gap_1().mt_3().children(
                            ["全部", "排位", "大乱斗"]
                                .iter()
                                .enumerate()
                                .map(|(i, label)| {
                                    Button::new(("filter", i))
                                        .label(*label)
                                        .small()
                                        .ghost()
                                        .selected(self.history_filter == i)
                                        .on_click(cx.listener(move |this, _, _, cx| {
                                            this.history_filter = i;
                                            if let Some(index) =
                                                this.history.iter().position(|m| match i {
                                                    1 => [420, 440].contains(&m.queue_id),
                                                    2 => [450, 2400].contains(&m.queue_id),
                                                    _ => true,
                                                })
                                            {
                                                this.selected_match = index;
                                            }
                                            cx.notify();
                                        }))
                                }),
                        ),
                    ),
            )
            .child(
                // Fixed row bounds let GPUI skip constructing histories outside the visible list while retaining their original record indexes.
                uniform_list(
                    "match-list",
                    filtered.len(),
                    cx.processor(move |this, range: std::ops::Range<usize>, _, cx| {
                        range
                            .map(|slot| {
                                let index = filtered[slot];
                                let record = &this.history[index];
                                let selected = index == this.selected_match;
                                div().h(px(112.)).pb_1().child(
                                    div()
                                        .h_full()
                                        .id(("match", index))
                                        .role(Role::Button)
                                        .aria_label(format!(
                                            "查看{}战绩，{}，{}",
                                            record.name,
                                            record.queue,
                                            if record.win { "胜利" } else { "失败" }
                                        ))
                                        .cursor_pointer()
                                        .mx_1()
                                        .px_3()
                                        .py_3()
                                        .rounded_md()
                                        .bg(if selected { p.selected } else { p.bg })
                                        .border_1()
                                        .border_color(if selected { p.blue } else { p.line })
                                        .on_click(cx.listener(move |this, _, _, cx| {
                                            this.selected_match = index;
                                            let m = &this.history[index];
                                            if this.history_live && m.players.len() < 10 {
                                                this.send(Command::Detail(m.id));
                                            }
                                            cx.notify();
                                        }))
                                        .child(
                                            div()
                                                .flex()
                                                .gap_3()
                                                .items_center()
                                                .child(this.portrait(record.champion_id, 36., p))
                                                .child(
                                                    div()
                                                        .flex_1()
                                                        .min_w_0()
                                                        .child(
                                                            div()
                                                                .flex()
                                                                .justify_between()
                                                                .child(record.name.clone())
                                                                .child(
                                                                    div()
                                                                        .text_xs()
                                                                        .text_color(if record.win {
                                                                            p.good
                                                                        } else {
                                                                            p.bad
                                                                        })
                                                                        .child(if record.win {
                                                                            "胜利"
                                                                        } else {
                                                                            "失败"
                                                                        }),
                                                                ),
                                                        )
                                                        .child(
                                                            div()
                                                                .mt_1()
                                                                .flex()
                                                                .justify_between()
                                                                .child(format!(
                                                                    "{} / {} / {}",
                                                                    record.kills,
                                                                    record.deaths,
                                                                    record.assists
                                                                ))
                                                                .child(
                                                                    div()
                                                                        .text_xs()
                                                                        .text_color(p.muted)
                                                                        .child(format!(
                                                                            "{}:{:02}",
                                                                            record.duration / 60,
                                                                            record.duration % 60
                                                                        )),
                                                                ),
                                                        ),
                                                ),
                                        )
                                        .child(div().mt_2().text_xs().text_color(p.muted).child(
                                            format!(
                                                "{} · {}",
                                                record.queue,
                                                match_date(record.created)
                                            ),
                                        )),
                                )
                            })
                            .collect::<Vec<_>>()
                    }),
                )
                .flex_1()
                .min_h_0()
                .px_2(),
            );
        div().flex().flex_1().min_h_0().child(list).child(
            div()
                .id("match-detail")
                .overflow_y_scroll()
                .flex_1()
                .min_w_0()
                .p_6()
                .when(self.history.is_empty(), |el| {
                    el.child(empty_state(
                        if self.connected {
                            "正在读取近期战绩"
                        } else {
                            "等待英雄联盟客户端"
                        },
                        if self.connected {
                            "正在读取当前账号战绩，暂时不可用时会自动重试。"
                        } else {
                            "启动英雄联盟后会自动连接，战绩将从当前登录账号读取。"
                        },
                        assets::IconName::ChartBar,
                        p,
                    ))
                })
                .when_some(self.history.get(self.selected_match), |el, m| {
                    el.child(self.match_detail(m, p, cx))
                }),
        )
    }

    /// match_detail renders the selected match's overview and contribution navigation.
    fn match_detail(
        &self,
        m: &MatchRecord,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        let kda = (m.kills + m.assists) as f64 / m.deaths.max(1) as f64;
        let csm = m.cs as f64 / (m.duration.max(1) as f64 / 60.);
        div()
            .flex()
            .flex_col()
            .gap_5()
            .child(
                div()
                    .flex()
                    .justify_between()
                    .items_start()
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_3()
                            .child(self.portrait(m.champion_id, 52., p))
                            .child(
                                div()
                                    .child(
                                        div()
                                            .text_xl()
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child(m.name.clone()),
                                    )
                                    .child(div().text_xs().text_color(p.muted).mt_1().child(
                                        format!(
                                            "{} · {} · {} 分 {} 秒",
                                            m.queue,
                                            match_date(m.created),
                                            m.duration / 60,
                                            m.duration % 60
                                        ),
                                    )),
                            ),
                    )
                    .child(badge(
                        if m.win { "胜利" } else { "失败" },
                        if m.win { p.good } else { p.bad },
                        p.panel,
                    )),
            )
            .child(
                div()
                    .flex()
                    .gap_8()
                    .pb_5()
                    .border_b_1()
                    .border_color(p.line)
                    .child(stat(
                        "K / D / A",
                        format!("{} / {} / {}", m.kills, m.deaths, m.assists),
                        format!("KDA {kda:.2}"),
                        p,
                    ))
                    .child(stat("对英雄伤害", compact(m.damage), "总伤害".into(), p))
                    .child(stat(
                        "补刀 / 分钟",
                        format!("{csm:.1}"),
                        format!("补刀 {} · 金币 {}", m.cs, compact(m.gold)),
                        p,
                    )),
            )
            .child(
                div()
                    .flex()
                    .gap_2()
                    .children(
                        ["对局概览", "贡献分析"]
                            .iter()
                            .enumerate()
                            .map(|(i, label)| {
                                Button::new(("detail-tab", i))
                                    .label(*label)
                                    .ghost()
                                    .selected(self.detail_tab == i)
                                    .on_click(cx.listener(move |this, _, _, cx| {
                                        this.detail_tab = i;
                                        cx.notify();
                                    }))
                            }),
                    ),
            )
            .when(self.detail_tab == 0, |el| {
                el.child(self.scoreboard(m, p, cx))
            })
            .when(self.detail_tab == 1, |el| {
                el.child(self.contributions(m, p, cx))
            })
            .child(
                div()
                    .text_xs()
                    .text_color(p.muted)
                    .pt_2()
                    .child(self.history_label.clone()),
            )
    }

    /// scoreboard compares visible participants on a single selected raw metric.
    fn scoreboard(&self, m: &MatchRecord, p: Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let max = m
            .players
            .iter()
            .map(|player| player.metric(self.metric))
            .max()
            .unwrap_or(1)
            .max(1) as f32;
        div()
            .flex()
            .flex_col()
            .gap_2()
            .child(self.metric_controls(cx))
            .when(m.players.len() < 10, |el| {
                el.child(
                    div()
                        .p_4()
                        .bg(p.panel)
                        .text_color(p.muted)
                        .child("完整十人数据尚未读取，暂不展示全场排名与占比。"),
                )
            })
            .children([100, 200].into_iter().map(|team| {
                div()
                    .mt_3()
                    .child(
                        div()
                            .flex()
                            .justify_between()
                            .mb_2()
                            .text_xs()
                            .text_color(p.muted)
                            .child(if team == m.team {
                                "己方阵容"
                            } else {
                                "敌方阵容"
                            })
                            .child("K / D / A                  数值"),
                    )
                    .children(
                        m.players
                            .iter()
                            .filter(move |player| player.team == team)
                            .map(|player| {
                                let value = player.metric(self.metric);
                                div()
                                    .flex()
                                    .items_center()
                                    .gap_3()
                                    .py_2()
                                    .px_2()
                                    .rounded_md()
                                    .bg(if player.local { p.selected } else { p.bg })
                                    .child(self.portrait(player.champion_id, 30., p))
                                    .child(div().w(px(95.)).child(player.name.clone()))
                                    .child(div().w(px(100.)).text_xs().child(format!(
                                        "{} / {} / {}",
                                        player.kills, player.deaths, player.assists
                                    )))
                                    .child(
                                        div().flex_1().h(px(5.)).rounded_full().bg(p.panel).child(
                                            div()
                                                .h_full()
                                                .w(relative(value as f32 / max))
                                                .bg(if team == m.team { p.blue } else { p.muted })
                                                .rounded_full(),
                                        ),
                                    )
                                    .child(
                                        div()
                                            .w(px(70.))
                                            .text_right()
                                            .text_xs()
                                            .child(compact(value)),
                                    )
                            }),
                    )
            }))
    }

    /// metric_controls selects the raw measurement shared by combat comparisons.
    fn metric_controls(&self, cx: &mut Context<Self>) -> impl IntoElement {
        div().flex().flex_wrap().gap_1().children(
            ["伤害", "金币", "承伤", "治疗", "推塔"]
                .iter()
                .enumerate()
                .map(|(i, label)| {
                    Button::new(("metric", i))
                        .label(*label)
                        .small()
                        .ghost()
                        .selected(self.metric == i)
                        .on_click(cx.listener(move |this, _, _, cx| {
                            this.metric = i;
                            cx.notify();
                        }))
                }),
        )
    }

    /// contributions displays genuine team shares beside the all-match comparison radar.
    /// Complete combat detail is required; partial rosters keep a loading message.
    /// Metric selection updates the explanatory list while radar axes retain all
    /// five measures and use the maximum raw participant value on each axis.
    fn contributions(
        &self,
        m: &MatchRecord,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement {
        div().flex().flex_col().gap_4().child(self.metric_controls(cx)).when(m.players.len()<10,|el|el.child("等待完整十人战绩详情后计算占比和雷达。"))
            .when(m.players.len()>=10,|el|el.child(div().flex().gap_6().items_center().child(div().flex_1().children(["对英雄伤害","金币收入","承受伤害","治疗量","推塔伤害"].iter().enumerate().map(|(i,label)|{
                let value=m.players.iter().find(|p|p.local).map(|p|p.metric(i)).unwrap_or(0);let stats=m.contribution(i);
                div().mb_4().child(div().flex().justify_between().mb_1().child(*label).child(div().text_color(p.blue).child(stats.map(|(share,_,_,_)|format!("己方 {:.1}%",share*100.)).unwrap_or_else(||"暂无有效总量".into())))).child(div().h(px(5.)).rounded_full().bg(p.panel).child(div().h_full().w(relative(stats.map(|(share,_,_,_)|share as f32).unwrap_or(0.))).rounded_full().bg(p.blue))).child(div().mt_1().text_xs().text_color(p.muted).child(stats.map(|(_,_,rank,_)|format!("{} · 全场第 {}",compact(value),rank)).unwrap_or_else(||compact(value))))
            }))).child(self.radar(m,p))))
            .child(div().p_3().bg(p.panel).rounded_md().text_xs().text_color(p.muted).child("雷达外圈＝本局全场该项最高值；半径按原始数值归一化。灰色轮廓＝全场人均，左侧保留真实己方占比。"))
    }

    /// radar draws five independent match-maximum-normalized combat dimensions.
    /// Zero-total dimensions collapse to the center and show an unavailable rank.
    /// The maximum is a comparison ceiling rather than a performance target;
    /// different dimensions retain their own units and cannot be added together.
    fn radar(&self, m: &MatchRecord, p: Palette) -> impl IntoElement {
        let observations: Vec<_> = (0..5).map(|i| m.contribution(i)).collect();
        let owned = observations.clone();
        let chart = canvas(
            |_, _, _| (),
            move |bounds, _, window, _| {
                let center = point(
                    bounds.origin.x + bounds.size.width / 2.,
                    bounds.origin.y + bounds.size.height / 2.,
                );
                let radius = px(82.);
                for fraction in [0.25, 0.5, 0.75, 1.] {
                    let points: Vec<_> = (0..5)
                        .map(|i| radar_point(center, radius, fraction, i))
                        .collect();
                    let mut builder = PathBuilder::stroke(px(1.));
                    builder.add_polygon(&points, true);
                    if let Ok(path) = builder.build() {
                        window.paint_path(path, p.line);
                    }
                }
                for i in 0..5 {
                    let mut line = PathBuilder::stroke(px(1.));
                    line.move_to(center);
                    line.line_to(radar_point(center, radius, 1., i));
                    if let Ok(path) = line.build() {
                        window.paint_path(path, p.line);
                    }
                }
                let average: Vec<_> = (0..5)
                    .map(|i| {
                        radar_point(
                            center,
                            radius,
                            owned[i].map(|s| s.3 as f32).unwrap_or(0.),
                            i,
                        )
                    })
                    .collect();
                let mut builder = PathBuilder::stroke(px(1.5));
                builder.add_polygon(&average, true);
                if let Ok(path) = builder.build() {
                    window.paint_path(path, p.muted);
                }
                let points: Vec<_> = (0..5)
                    .map(|i| {
                        radar_point(
                            center,
                            radius,
                            owned[i].map(|s| s.1 as f32).unwrap_or(0.),
                            i,
                        )
                    })
                    .collect();
                let mut fill = PathBuilder::fill();
                fill.add_polygon(&points, true);
                if let Ok(path) = fill.build() {
                    window.paint_path(path, p.blue.opacity(0.16));
                }
                let mut outline = PathBuilder::stroke(px(2.));
                outline.add_polygon(&points, true);
                if let Ok(path) = outline.build() {
                    window.paint_path(path, p.blue);
                }
            },
        )
        .absolute()
        .size_full();
        div()
            .relative()
            .w(px(300.))
            .h(px(270.))
            .flex_shrink_0()
            .child(chart)
            .children(
                ["伤害", "金币", "承伤", "治疗", "推塔"]
                    .iter()
                    .enumerate()
                    .map(|(i, label)| {
                        let angle =
                            i as f32 * std::f32::consts::TAU / 5. - std::f32::consts::FRAC_PI_2;
                        div()
                            .absolute()
                            .left(px(150. + angle.cos() * 111. - 43.))
                            .top(px(135. + angle.sin() * 109. - 16.))
                            .w(px(86.))
                            .text_center()
                            .text_xs()
                            .text_color(p.muted)
                            .child(*label)
                            .child(
                                div().text_color(p.ink).child(
                                    observations[i]
                                        .map(|(_, _, rank, _)| format!("全场 #{rank}"))
                                        .unwrap_or_else(|| "无有效总量".into()),
                                ),
                            )
                    }),
            )
    }

    /// draft_page renders the client-observed roster and its detected gameplay mode.
    /// Without an active selection session, the page explains how to obtain data.
    /// Mode controls are informational because the client determines the legal
    /// champion pool and the matching statistics for the current room.
    fn draft_page(&self, p: Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let room = self.room.as_ref();
        div()
            .id("draft-scroll")
            .overflow_y_scroll()
            .flex_1()
            .min_h_0()
            .p_6()
            .child(
                div().flex().justify_between().items_center().mb_5().child(
                    div()
                        .child(
                            div()
                                .flex()
                                .gap_2()
                                .items_center()
                                .child(
                                    div()
                                        .text_xl()
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .child("当前选人"),
                                )
                                .child(badge(
                                    if room.is_some() {
                                        "实时房间"
                                    } else {
                                        "等待选人"
                                    },
                                    p.blue,
                                    p.panel,
                                ))
                                .when_some(room, |el, room| {
                                    el.child(badge(room.mode.label(), p.muted, p.panel))
                                }),
                        )
                        .child(
                            div()
                                .text_xs()
                                .text_color(p.muted)
                                .mt_1()
                                .child("选人中约每 2 秒同步 · 自动选禁需单独开启"),
                        ),
                ),
            )
            .when(room.is_none(), |el| {
                el.child(
                    div()
                        .p_6()
                        .bg(p.panel)
                        .rounded_lg()
                        .child(div().text_lg().child(if self.connected {
                            "当前客户端未处于英雄选择阶段"
                        } else {
                            "等待连接英雄联盟客户端"
                        }))
                        .child(
                            div()
                                .mt_3()
                                .text_color(p.muted)
                                .child(self.connection.clone()),
                        )
                        .child(div().mt_3().text_xs().text_color(p.muted).child(
                            "进入英雄选择后，自动显示真实阵容、可选英雄和对应模式的强度与调整。",
                        )),
                )
            })
            .when_some(room, |el, room| el.child(self.room_content(room, p, cx)))
    }

    /// room_content renders only public enemy slots and available recommendation candidates.
    fn room_content(&self, room: &Room, p: Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let known = room.enemies.iter().filter(|m| m.champion > 0).count();
        let ranked = room.mode == Mode::Ranked;
        div()
            .flex()
            .flex_col()
            .gap_5()
            .child(
                div()
                    .child(section_title(
                        "己方阵容",
                        if room.mode == Mode::Ranked {
                            "预选可更改 · 锁定已确定"
                        } else {
                            "当前英雄与替补池"
                        },
                        p,
                    ))
                    .child(
                        div().flex().gap_2().children(
                            room.allies
                                .iter()
                                .map(|member| self.member_card(member, room.mode, p, cx)),
                        ),
                    ),
            )
            .child(
                div()
                    .p_3()
                    .rounded_md()
                    .bg(p.panel)
                    .text_xs()
                    .text_color(p.muted)
                    .child(self.composition(room)),
            )
            .child(
                div()
                    .child(section_title(
                        "敌方阵容",
                        &format!(
                            "已公开 {known} / {} · 分路需确认",
                            room.enemies.len().max(5)
                        ),
                        p,
                    ))
                    .child(
                        div()
                            .flex()
                            .gap_2()
                            .children((0..room.enemies.len().max(5)).map(|i| {
                                let member = room.enemies.get(i).cloned().unwrap_or_default();
                                let id = member.champion;
                                let name = self.db.champion(id).name;
                                div()
                                    .id(("enemy", i))
                                    .when(id > 0 && room.mode == Mode::Ranked, |el| {
                                        el.role(Role::Button)
                                            .aria_label(format!("指定对位目标：{name}"))
                                    })
                                    .flex_1()
                                    .min_w_0()
                                    .border_b_1()
                                    .border_color(p.line)
                                    .pb_3()
                                    .flex()
                                    .items_center()
                                    .gap_2()
                                    .cursor_pointer()
                                    .on_click(cx.listener(move |this, _, _, cx| {
                                        if id > 0 && ranked {
                                            this.enemy = id;
                                            this.notice =
                                                "已指定公开敌方英雄，正在读取对位数据…".into();
                                            this.lookup_matchups();
                                            cx.notify();
                                        }
                                    }))
                                    .child(self.portrait(id, 30., p))
                                    .child(
                                        div()
                                            .min_w_0()
                                            .child(div().text_sm().child(if id == 0 {
                                                "未公开".into()
                                            } else {
                                                name
                                            }))
                                            .child(
                                                div()
                                                    .text_xs()
                                                    .text_color(if id == self.enemy && id > 0 {
                                                        p.blue
                                                    } else {
                                                        p.muted
                                                    })
                                                    .child(if id == 0 {
                                                        "等待客户端公开"
                                                    } else if id == self.enemy {
                                                        "当前对位目标"
                                                    } else {
                                                        "已锁定 · 点击指定"
                                                    }),
                                            ),
                                    )
                            })),
                    ),
            )
            .child(
                div()
                    .flex()
                    .gap_6()
                    .items_start()
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .child(self.recommendations(room, p, cx)),
                    )
                    .child(self.hero_inspector(room.mode, p, cx)),
            )
    }

    /// composition describes source-role tendencies without inventing numeric damage shares.
    fn composition(&self, room: &Room) -> String {
        let tanks: Vec<_> = room
            .allies
            .iter()
            .map(|m| self.db.champion(m.champion))
            .filter(|c| c.roles.iter().any(|r| r == "tank"))
            .map(|c| c.name)
            .collect();
        if tanks.is_empty() {
            "阵容观察：当前英雄标签中缺少坦克，建议关注前排和开团。实际职责仍取决于出装与打法。"
                .into()
        } else {
            format!(
                "阵容观察：可承担前排的英雄标签：{}。实际职责仍取决于出装与打法。",
                tanks.join("、")
            )
        }
    }

    /// member_card shows one friendly slot's true selection state and mode observations.
    fn member_card(
        &self,
        m: &Member,
        mode: Mode,
        p: Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement + use<> {
        let id = m.champion;
        let champion = self.db.champion(id);
        let stats = champion.stats(mode);
        let selected = id > 0 && self.selected_hero == id;
        div()
            .id(("ally", m.cell as u64))
            .when(id > 0, |el| {
                el.role(Role::Button)
                    .aria_label(format!("查看{}强度与调整", champion.name))
            })
            .flex_1()
            .min_w_0()
            .p_3()
            .rounded_md()
            .border_1()
            .border_color(if selected { p.blue } else { p.line })
            .bg(if selected { p.selected } else { p.bg })
            .cursor_pointer()
            .on_click(cx.listener(move |this, _, _, cx| {
                if id > 0 {
                    this.inspect(id, cx);
                }
            }))
            .child(
                div()
                    .flex()
                    .justify_between()
                    .text_xs()
                    .text_color(p.muted)
                    .mb_2()
                    .child(if m.local {
                        "我"
                    } else {
                        position_label(&m.position)
                    })
                    .child(if mode != Mode::Ranked {
                        "当前英雄"
                    } else if m.locked {
                        "已锁定"
                    } else {
                        "预选"
                    }),
            )
            .child(
                div()
                    .flex()
                    .gap_2()
                    .items_center()
                    .child(self.portrait(id, 34., p))
                    .child(
                        div()
                            .min_w_0()
                            .child(div().text_sm().text_ellipsis().child(champion.name.clone()))
                            .child(
                                div()
                                    .text_xs()
                                    .text_color(p.muted)
                                    .text_ellipsis()
                                    .child(champion.title.clone()),
                            ),
                    ),
            )
            .when(mode != Mode::Ranked && id > 0, |el| {
                el.child(
                    div()
                        .flex()
                        .gap_2()
                        .items_center()
                        .mt_3()
                        .child(badge(
                            stats.tier.as_deref().unwrap_or("暂无分级"),
                            p.blue,
                            p.panel,
                        ))
                        .child(stats.win_rate.map(percent).unwrap_or_else(|| "--".into())),
                )
                .child(
                    div()
                        .mt_2()
                        .text_xs()
                        .text_color(
                            stats
                                .adjustments
                                .as_ref()
                                .and_then(|a| a.first())
                                .map(|a| if a.helpful { p.good } else { p.bad })
                                .unwrap_or(p.muted),
                        )
                        .child(
                            stats
                                .adjustments
                                .as_ref()
                                .and_then(|a| a.first())
                                .map(|a| format!("{} {}", a.label, a.display))
                                .unwrap_or_else(|| {
                                    if stats.adjustments.is_some() {
                                        "未列出非默认调整".into()
                                    } else {
                                        "调整数据暂缺".into()
                                    }
                                }),
                        ),
                )
            })
    }

    /// lookup_matchups requests observations for the public target and chosen lane.
    fn lookup_matchups(&mut self) {
        if self.enemy > 0 {
            self.send(Command::Matchups {
                enemy: self.enemy,
                position: ["top", "jungle", "mid", "adc", "support"][self.position].into(),
            });
        }
    }

    /// recommendations intersects source candidates with the room's legal champion pool.
    /// Ranked rows require a chosen public enemy and matching source lane context.
    /// ARAM rows use only current or bench heroes and explain the composition
    /// benefit separately from win rate. Small samples remain explicitly labeled.
    fn recommendations(&self, room: &Room, p: Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let ranked = room.mode == Mode::Ranked;
        let mut rows: Vec<(u32, f64, u64, String, String)> = if ranked {
            self.db
                .matchups
                .iter()
                .filter(|m| {
                    m.enemy == self.enemy
                        && room.available.contains(&m.champion)
                        && m.position == ["top", "jungle", "mid", "adc", "support"][self.position]
                })
                .map(|m| {
                    (
                        m.champion,
                        m.wins as f64 / m.samples.max(1) as f64,
                        m.samples,
                        if m.samples < 200 {
                            "样本较少，谨慎参考".into()
                        } else {
                            "对位对局统计；请结合自身熟练度".into()
                        },
                        m.source.clone(),
                    )
                })
                .collect()
        } else {
            room.available
                .iter()
                .map(|&id| {
                    let c = self.db.champion(id);
                    let s = c.stats(room.mode);
                    (
                        id,
                        s.win_rate.unwrap_or(0.),
                        s.samples,
                        if c.roles.iter().any(|r| r == "tank") {
                            "可补前排；结合阵容决定是否换用".into()
                        } else {
                            "当前英雄或替补池可用候选".into()
                        },
                        s.source.clone(),
                    )
                })
                .collect()
        };
        // Shrink matchup deviations toward fifty percent as sample counts decrease, so sparse observations cannot dominate raw-win-rate ranking alone.
        rows.sort_by(|a, b| {
            if ranked {
                let score_a = (a.1 - 0.5) * a.2 as f64 / (a.2 as f64 + 500.);
                let score_b = (b.1 - 0.5) * b.2 as f64 / (b.2 as f64 + 500.);
                score_b.total_cmp(&score_a)
            } else {
                let tanks = |id| self.db.champion(id).roles.iter().any(|r| r == "tank");
                tanks(b.0)
                    .cmp(&tanks(a.0))
                    .then_with(|| b.1.total_cmp(&a.1))
            }
        });
        div()
            .flex()
            .flex_col()
            .gap_2()
            .child(section_title(
                if ranked {
                    "对位推荐"
                } else {
                    "替补池与阵容建议"
                },
                if ranked {
                    "对位对局胜率"
                } else {
                    "当前可用英雄"
                },
                p,
            ))
            .when(ranked, |el| {
                el.child(
                    div().flex().flex_wrap().gap_1().children(
                        ["上路", "打野", "中路", "下路", "辅助"]
                            .iter()
                            .enumerate()
                            .map(|(i, label)| {
                                Button::new(("position", i))
                                    .label(*label)
                                    .small()
                                    .ghost()
                                    .selected(self.position == i)
                                    .on_click(cx.listener(move |this, _, _, cx| {
                                        this.position = i;
                                        this.lookup_matchups();
                                        cx.notify();
                                    }))
                            }),
                    ),
                )
                .child(
                    div()
                        .text_xs()
                        .text_color(p.muted)
                        .mb_2()
                        .child(if self.enemy == 0 {
                            "点击上方已公开的敌方英雄，手动指定对位目标。".into()
                        } else {
                            format!(
                                "目标：{} · 分路手动指定 · 韩服 / 铂金以上",
                                self.db.champion(self.enemy).name
                            )
                        }),
                )
            })
            .when(!room.available_known, |el| {
                el.child(
                    div()
                        .text_color(p.muted)
                        .child("当前可选英雄池不可读，暂不生成推荐。"),
                )
            })
            .when(rows.is_empty() && room.available_known, |el| {
                el.child(div().text_color(p.muted).p_4().child(if ranked {
                    "等待所选目标的对位数据；未命中可用英雄时不强行推荐。"
                } else {
                    "当前没有可替换的英雄。"
                }))
            })
            .children(
                rows.into_iter()
                    .take(6)
                    .map(|(id, rate, samples, reason, source)| {
                        let c = self.db.champion(id);
                        div()
                            .id(("recommend", id as usize))
                            .role(Role::Button)
                            .aria_label(format!("查看推荐英雄：{}", c.name))
                            .py_3()
                            .border_t_1()
                            .border_color(p.line)
                            .cursor_pointer()
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.inspect(id, cx);
                            }))
                            .child(
                                div()
                                    .flex()
                                    .items_center()
                                    .gap_3()
                                    .child(self.portrait(id, 36., p))
                                    .child(
                                        div()
                                            .flex_1()
                                            .min_w_0()
                                            .child(
                                                div().flex().gap_2().child(c.name).when(
                                                    samples < 200,
                                                    |el| {
                                                        el.child(badge(
                                                            "样本较少",
                                                            p.muted,
                                                            p.panel,
                                                        ))
                                                    },
                                                ),
                                            )
                                            .child(
                                                div()
                                                    .text_xs()
                                                    .text_color(p.muted)
                                                    .mt_1()
                                                    .child(reason),
                                            ),
                                    )
                                    .child(
                                        div()
                                            .text_right()
                                            .child(if samples > 0 {
                                                percent(rate)
                                            } else {
                                                "--".into()
                                            })
                                            .child(
                                                div()
                                                    .text_xs()
                                                    .text_color(p.muted)
                                                    .child(format!("{samples} 场")),
                                            ),
                                    ),
                            )
                            .child(div().text_xs().text_color(p.muted).mt_2().child(source))
                    }),
            )
            .child(div().mt_3().text_xs().text_color(p.muted).child(if ranked {
                "统计为整场对局胜负，不代表对线期胜率；排名按样本量收缩，避免小样本胜率直接排第一。"
            } else {
                "强度与平衡调整分别展示。可用池来自当前房间，查看详情不会更改客户端英雄。"
            }))
    }

    /// hero_inspector displays mode-specific strength and complete known balance adjustments.
    fn hero_inspector(&self, mode: Mode, p: Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let c = self.db.champion(self.selected_hero);
        let s = c.stats(mode);
        let id = c.id;
        div()
            .w(px(255.))
            .flex_shrink_0()
            .bg(p.panel)
            .rounded_lg()
            .border_1()
            .border_color(p.line)
            .p_4()
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .mb_4()
                    .child(self.portrait(c.id, 46., p))
                    .child(
                        div()
                            .child(
                                div()
                                    .text_lg()
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .child(c.name.clone()),
                            )
                            .child(div().text_xs().text_color(p.muted).child(c.title.clone())),
                    ),
            )
            .when(mode != Mode::Ranked, |el| {
                el.child(badge(
                    s.tier.as_deref().unwrap_or("暂无分级"),
                    p.blue,
                    p.selected,
                ))
                .child(
                    div()
                        .flex()
                        .gap_5()
                        .mt_3()
                        .child(stat(
                            "模式胜率",
                            s.win_rate.map(percent).unwrap_or_else(|| "--".into()),
                            String::new(),
                            p,
                        ))
                        .child(stat("样本量", compact(s.samples), "场".into(), p)),
                )
                .child(
                    div()
                        .mt_4()
                        .mb_2()
                        .font_weight(FontWeight::SEMIBOLD)
                        .child("模式平衡调整"),
                )
                .children(s.adjustments.iter().flatten().map(|a| {
                    div()
                        .flex()
                        .justify_between()
                        .py_2()
                        .gap_2()
                        .border_b_1()
                        .border_color(p.line)
                        .text_sm()
                        .child(a.label.clone())
                        .child(
                            div()
                                .text_color(if a.helpful { p.good } else { p.bad })
                                .child(format!(
                                    "{} · {}",
                                    a.display,
                                    if a.helpful { "增益" } else { "削弱" }
                                )),
                        )
                }))
                .when(s.adjustments.as_ref().is_none_or(|a| a.is_empty()), |el| {
                    el.child(div().text_xs().text_color(p.muted).child(
                        if s.adjustments.is_none() {
                            "数据源未列出调整，或详情尚未读取"
                        } else {
                            "该来源未列出非默认调整"
                        },
                    ))
                })
                .child(
                    div()
                        .mt_3()
                        .text_xs()
                        .text_color(p.muted)
                        .child(s.source.clone()),
                )
                .when(mode == Mode::Aram, |el| {
                    el.child(
                        div()
                            .mt_2()
                            .text_xs()
                            .text_color(p.muted)
                            .child("普通调整端点未标补丁版本，请以客户端为准"),
                    )
                })
                .when(mode == Mode::Mayhem, |el| {
                    el.child(
                        Button::new("hero-refresh")
                            .label("刷新该英雄调整")
                            .small()
                            .mt_3()
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.notice = "正在读取英雄调整…".into();
                                this.send(Command::InspectChampion(id));
                                cx.notify();
                            })),
                    )
                })
            })
            .when(mode == Mode::Ranked, |el| {
                el.child(div().text_xs().text_color(p.muted).child(
                    "对位结果见左侧推荐。先指定公开敌方英雄与分路，推荐仅包含当前可用英雄。",
                ))
            })
    }
}

impl Lux {
    /// render_region resolves current observations for one independently cached native surface.
    /// All business state remains in Lux, while the calling Region supplies
    /// GPUI's current view identity. Hover notifications therefore invalidate that
    /// surface and its ancestors without notifying unrelated neighboring regions.
    fn render_region(
        &mut self,
        region: RegionKind,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        #[cfg(feature = "diagnostics")]
        if matches!(region, RegionKind::Workspace) {
            regions::record_build(1);
        }
        let p = Palette::new(cx);
        match region {
            RegionKind::Sidebar => self.sidebar(cx).into_any_element(),
            RegionKind::Header => self.header(p).into_any_element(),
            RegionKind::Footer => self.footer(p).into_any_element(),
            RegionKind::Scout { ally, index } => self.scout_region(ally, index, p, cx),
            RegionKind::Workspace => match self.page {
                0 => self.history_page(p, cx).into_any_element(),
                1 => self.draft_page(p, cx).into_any_element(),
                2 => self.query_page(p, cx).into_any_element(),
                3 => self.battle_page(p, cx).into_any_element(),
                _ => self.automation_page(p, window, cx).into_any_element(),
            },
        }
    }

    /// footer renders operation and public-source freshness independently from page content.
    /// Source detail stays available in its tooltip. Receiving a business update
    /// invalidates this region through its owner subscription before cached reuse.
    fn footer(&self, p: Palette) -> impl IntoElement {
        let source_tip = format!("{} · {}", self.source_status, self.db.public_updated);
        div()
            .h(px(32.))
            .flex_shrink_0()
            .px_5()
            .flex()
            .items_center()
            .gap_4()
            .border_t_1()
            .border_color(p.line)
            .bg(p.bg)
            .text_xs()
            .text_color(p.muted)
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .child(if self.notice.is_empty() {
                        self.connection.clone()
                    } else {
                        self.notice.clone()
                    }),
            )
            .child(
                div()
                    .id("automatic-source-status")
                    .max_w(px(480.))
                    .truncate()
                    .child(self.source_status.clone())
                    .tooltip(move |window, cx| {
                        gpui_kit::component::tooltip::Tooltip::new(source_tip.clone())
                            .build(window, cx)
                    }),
            )
    }
}

impl Render for Lux {
    /// render builds the current native workspace with persistent source-state labels.
    /// Navigation, account status, and operation status have fixed cached layout
    /// boundaries. Ordinary workspaces cache their page; the battle coordinator
    /// stays uncached so individual player cards retain their own cache. Pointer
    /// hover invalidates its nearest region instead of notifying
    /// the owner, while explicit owner notifications refresh cached observations.
    /// Diagnostic builds can disable those boundaries for comparison against the
    /// same current observations. The root retains typography and surface colors,
    /// allowing theme and bounds changes to invalidate toolkit cache keys normally.
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        #[cfg(feature = "diagnostics")]
        regions::record_build(0);
        #[cfg(feature = "diagnostics")]
        if let Some(probe) = &mut self.frame_probe {
            probe.tick(
                _window,
                cx,
                serde_json::json!({
                    "page": self.page,
                    "historyRecords": self.history.len(),
                    "players": self.tools.diagnostic_context(),
                    "catalogHeroes": self.db.champions.len(),
                }),
            );
        }
        let p = Palette::new(cx);
        div()
            .size_full()
            .flex()
            .bg(p.canvas)
            .text_color(p.ink)
            .font_family("Microsoft YaHei UI")
            .text_sm()
            .child(if self.use_render_cache {
                self.regions
                    .sidebar
                    .clone()
                    .cached(
                        StyleRefinement::default()
                            .w(px(160.))
                            .h_full()
                            .flex_shrink_0(),
                    )
                    .into_any_element()
            } else {
                self.sidebar(cx).into_any_element()
            })
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .flex()
                    .flex_col()
                    .child(if self.use_render_cache {
                        self.regions
                            .header
                            .clone()
                            .cached(
                                StyleRefinement::default()
                                    .w_full()
                                    .h(px(76.))
                                    .flex_shrink_0(),
                            )
                            .into_any_element()
                    } else {
                        self.header(p).into_any_element()
                    })
                    .child(if self.use_render_cache {
                        // A cached parent's miss refreshes all nested caches; leave this coordinator uncached so clean sibling player cards remain reusable.
                        if self.page == 3 {
                            self.regions.workspace.clone().into_any_element()
                        } else {
                            self.regions
                                .workspace
                                .clone()
                                .cached(StyleRefinement::default().w_full().flex_1().min_h_0())
                                .into_any_element()
                        }
                    } else {
                        self.render_region(RegionKind::Workspace, _window, cx)
                    })
                    .child(if self.use_render_cache {
                        self.regions
                            .footer
                            .clone()
                            .cached(
                                StyleRefinement::default()
                                    .w_full()
                                    .h(px(32.))
                                    .flex_shrink_0(),
                            )
                            .into_any_element()
                    } else {
                        self.footer(p).into_any_element()
                    }),
            )
    }
}

/// empty_state gives waiting pages a shared surface and a concise next-step explanation.
fn empty_state(
    title: &str,
    description: &str,
    icon: assets::IconName,
    p: Palette,
) -> impl IntoElement + use<> {
    div()
        .w_full()
        .min_h(px(280.))
        .p_6()
        .rounded_xl()
        .bg(p.bg)
        .border_1()
        .border_color(p.line)
        .flex()
        .flex_col()
        .items_center()
        .justify_center()
        .gap_3()
        .child(
            div()
                .size(px(52.))
                .rounded_xl()
                .bg(p.selected)
                .text_color(p.blue)
                .flex()
                .items_center()
                .justify_center()
                .child(div().size(px(24.)).child(icon)),
        )
        .child(
            div()
                .mt_2()
                .text_base()
                .font_weight(FontWeight::SEMIBOLD)
                .child(title.to_owned()),
        )
        .child(
            div()
                .text_sm()
                .text_color(p.muted)
                .text_center()
                .max_w(px(500.))
                .child(description.to_owned()),
        )
}

/// badge renders a compact, non-interactive source or selection label.
fn badge(label: &str, color: Hsla, background: Hsla) -> impl IntoElement + use<> {
    div()
        .px_2()
        .py_1()
        .rounded_sm()
        .text_xs()
        .text_color(color)
        .bg(background)
        .child(label.to_owned())
}

/// stat groups a readable numeric value with its unit and context.
fn stat(label: &str, value: String, note: String, p: Palette) -> impl IntoElement {
    div()
        .child(div().text_xs().text_color(p.muted).child(label.to_owned()))
        .child(
            div()
                .text_2xl()
                .font_weight(FontWeight::SEMIBOLD)
                .mt_1()
                .child(value),
        )
        .child(div().text_xs().text_color(p.muted).mt_1().child(note))
}

/// section_title aligns a local heading with concise source or status context.
fn section_title(label: &str, context: &str, p: Palette) -> impl IntoElement {
    div()
        .flex()
        .justify_between()
        .gap_3()
        .mb_3()
        .child(
            div()
                .font_weight(FontWeight::SEMIBOLD)
                .child(label.to_owned()),
        )
        .child(
            div()
                .text_xs()
                .text_color(p.muted)
                .child(context.to_owned()),
        )
}

/// compact formats combat totals with explicit Chinese ten-thousand units.
fn compact(value: u64) -> String {
    if value >= 10000 {
        format!("{:.2} 万", value as f64 / 10000.)
    } else {
        value.to_string()
    }
}

/// percent formats an observed probability using two readable decimal places.
fn percent(value: f64) -> String {
    format!("{:.2}%", value * 100.)
}

/// match_date formats recorded Unix milliseconds in the machine's local timezone.
fn match_date(value: i64) -> String {
    chrono::DateTime::from_timestamp_millis(value)
        .map(|v| {
            v.with_timezone(&chrono::Local)
                .format("%m-%d %H:%M")
                .to_string()
        })
        .unwrap_or_else(|| "日期未知".into())
}

/// position_label maps client-assigned positions to concise localized roster labels.
fn position_label(position: &str) -> &'static str {
    match position {
        "top" => "上路",
        "jungle" => "打野",
        "middle" | "mid" => "中路",
        "bottom" | "adc" => "下路",
        "utility" | "support" => "辅助",
        _ => "队友",
    }
}

/// phase_label translates gameflow state without inventing a selection session.
fn phase_label(phase: &str) -> &'static str {
    match phase {
        "Lobby" => "组队房间",
        "Matchmaking" => "匹配中",
        "ReadyCheck" => "确认对局",
        "ChampSelect" => "英雄选择中",
        "InProgress" => "对局进行中",
        "Reconnect" => "对局等待重连",
        "EndOfGame" | "PreEndOfGame" => "对局结算",
        "None" => "客户端空闲",
        _ => "等待客户端",
    }
}

/// radar_point projects a normalized combat value onto one pentagon axis.
fn radar_point(center: Point<Pixels>, radius: Pixels, value: f32, index: usize) -> Point<Pixels> {
    let angle = index as f32 * std::f32::consts::TAU / 5. - std::f32::consts::FRAC_PI_2;
    point(
        center.x + radius * angle.cos() * value,
        center.y + radius * angle.sin() * value,
    )
}
