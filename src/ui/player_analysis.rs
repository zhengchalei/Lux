//! player_analysis.rs presents the sample provenance and recent contribution
//! context beside an inspected player's match detail. The widgets consume
//! prepared player and hero summaries instead of aggregating match history
//! during pointer redraws. An exact queue identifies the remote query scope,
//! while a local mode filter selects an already prepared subset of that query.
//!
//! Contribution values describe average friendly-team shares across observed
//! complete matches. Each metric carries its own usable denominator, because a
//! full roster can still lack individual statistics. Unknown values remain
//! visibly absent, and these historical samples never become a prediction for
//! the currently selected match or a percentile against an unavailable baseline.

use super::*;
use gpui_kit::component::tooltip::Tooltip;
use lux::scouting::{PreparedProfile, RecentSummary};

/// query_filter_mode maps query tabs to the same modes used by prepared summaries.
/// The all-mode tab returns None, preserving unknown queues in that scope;
/// each named tab restricts both the list and its aggregate sample.
pub(super) fn query_filter_mode(filter: usize) -> Option<Mode> {
    match filter {
        1 => Some(Mode::Ranked),
        2 => Some(Mode::Aram),
        3 => Some(Mode::Mayhem),
        _ => None,
    }
}

/// query_scope_label describes the exact fetched queue and the displayed local filter.
/// Queue-scoped results keep their queue name even when the all-mode tab is
/// selected, so a restricted sample cannot appear to cover every gameplay mode.
pub(super) fn query_scope_label(queue: Option<u64>, mode: Option<Mode>) -> String {
    match (queue, mode) {
        (Some(queue), Some(mode)) => {
            format!("{} · {}", lux::model::queue_label(queue), mode.label())
        }
        (Some(queue), None) => lux::model::queue_label(queue).into(),
        (None, Some(mode)) => mode.label().into(),
        (None, None) => "全部模式".into(),
    }
}

/// coverage_label formats actual retrieval progress independently from the requested analysis depth.
/// Source absence stays explicit instead of assuming LCU. Scanned and retained
/// counts remain distinct when pagination skips other queues or duplicate matches.
pub(super) fn coverage_label(profile: &PreparedProfile) -> String {
    let coverage = &profile.coverage;
    let source = if coverage.source.is_empty() {
        "来源待读取"
    } else {
        &coverage.source
    };
    format!(
        "{} · {} · 目标 {} 场 / 读取 {} 场 / 扫描 {} 场 · 完整阵容 {} 场",
        source,
        query_scope_label(coverage.queue_id, None),
        profile.requested,
        coverage.fetched,
        coverage.scanned,
        coverage.complete
    )
}

impl Lux {
    /// profile_analysis renders historical contribution coverage and one inspected hero's prepared sample.
    /// Player-level and hero-level summaries inherit the query's selected mode.
    /// Values are read from immutable snapshots, with independent metric coverage
    /// exposed on hover. Missing observations show dashes rather than a numeric
    /// zero, and a hero with no eligible games retains an explicit empty state.
    pub(super) fn profile_analysis(
        &self,
        profile: &PreparedProfile,
        summary: &RecentSummary,
        champion: u32,
        hero: Option<&RecentSummary>,
        p: Palette,
    ) -> impl IntoElement + use<> {
        let contribution = &summary.contribution;
        let hero_name = self
            .db
            .champions
            .get(&champion)
            .map(|hero| hero.name.clone())
            .unwrap_or_else(|| format!("英雄 {champion}"));
        div()
            .mb_4()
            .p_3()
            .rounded_lg()
            .border_1()
            .border_color(p.line)
            .bg(p.panel)
            .child(
                div()
                    .flex()
                    .items_center()
                    .justify_between()
                    .gap_2()
                    .child(
                        div()
                            .text_sm()
                            .font_weight(FontWeight::SEMIBOLD)
                            .child("近期队内贡献"),
                    )
                    .child(
                        div()
                            .text_xs()
                            .text_color(p.muted)
                            .child(format!("完整阵容 {} 场", contribution.games)),
                    ),
            )
            .child(
                div()
                    .flex()
                    .gap_2()
                    .mt_2()
                    .children(
                        [
                            ("伤害占比", contribution.damage_share, contribution.damage_games),
                            ("承伤占比", contribution.taken_share, contribution.taken_games),
                            ("经济占比", contribution.gold_share, contribution.gold_games),
                        ]
                        .into_iter()
                        .enumerate()
                        .map(|(index, (label, value, samples))| {
                            let tip = format!(
                                "{}：{} 场有完整分母的有效比赛，先逐局计算己方占比，再取平均。缺失数据不计入分母。",
                                label, samples
                            );
                            div()
                                .id(("recent-contribution", index))
                                .flex_1()
                                .min_w_0()
                                .child(div().text_xs().text_color(p.muted).child(label))
                                .child(
                                    div()
                                        .mt_1()
                                        .text_lg()
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .child(value.map(percent).unwrap_or_else(|| "--".into())),
                                )
                                .child(div().text_xs().text_color(p.muted).child(format!("{samples} 场样本")))
                                .tooltip(move |window, cx| Tooltip::new(tip.clone()).build(window, cx))
                        }),
                    ),
            )
            .when(champion > 0, |el| {
                el.child(
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .mt_3()
                        .pt_3()
                        .border_t_1()
                        .border_color(p.line)
                        .child(self.portrait(champion, 28., p))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .child(div().text_sm().child(format!("英雄样本 · {hero_name}")))
                                .child(
                                    div().text_xs().text_color(p.muted).child(
                                        hero.filter(|hero| hero.games > 0)
                                            .map(|hero| format!(
                                                "{} 场 · {} 胜 {} 负 · 历史胜率 {:.0}% · KDA {:.2}",
                                                hero.games, hero.wins, hero.games - hero.wins,
                                                hero.wins as f64 * 100. / hero.games as f64,
                                                hero.kda
                                            ))
                                            .unwrap_or_else(|| "这批战绩中暂无该英雄的有效样本".into()),
                                    ),
                                ),
                        ),
                )
            })
            .when_some(profile.coverage.note.clone(), |el, note| {
                el.child(
                    div()
                        .id("profile-source-note")
                        .mt_2()
                        .text_xs()
                        .text_color(p.muted)
                        .truncate()
                        .child(note.clone())
                        .tooltip(move |window, cx| Tooltip::new(note.clone()).build(window, cx)),
                )
            })
    }
}
