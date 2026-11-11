//! automation.rs turns explicit champion priorities into local selection actions.
//! Rules are saved independently from the runtime enable switch: launching Lux
//! never arms selection automatically. Pick and ban candidates use different
//! client-provided availability lists, and a completed action or another player's
//! turn cannot produce a write. Random-selection modes are excluded because their
//! assigned champions and bench exchanges do not follow the draft action contract.
//!
//! Planning and execution remain separate. The client worker rechecks a proposed
//! action against a fresh session before sending it, while Execution tracks delay
//! and attempted operations within one selection session. A failed or ambiguous
//! request is not repeated every poll. Manually selected champions are preserved
//! unless that exact pick was previously submitted by this automation runtime.

use crate::{
    model::{Mode, num},
    sources::data_directory,
};
use anyhow::Result;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    collections::BTreeSet,
    time::{Duration, Instant},
};

/// Settings contains persisted rules without a persisted permission to run them.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    pub auto_pick: bool,       // Choose from configured pick priorities.
    pub auto_ban: bool,        // Ban from configured ban priorities.
    pub preselect: bool,       // Hover during an available planning action.
    pub lock_pick: bool,       // Complete picks only during local turn.
    pub protect_allies: bool,  // Preserve friendly intended ban candidates.
    pub preserve_manual: bool, // Preserve choices made outside this runtime.
    pub delay_seconds: u64,    // Stable decision delay in whole seconds.
    pub picks: Vec<u32>,       // Ordered permitted fallback pick champions.
    pub bans: Vec<u32>,        // Ordered permitted fallback ban champions.
    pub position: String,      // Empty permits any client-assigned position.
}

impl Default for Settings {
    /// default creates inactive-by-caller rules requiring explicit champion choices.
    fn default() -> Self {
        Self {
            auto_pick: true,
            auto_ban: false,
            preselect: false,
            lock_pick: true,
            protect_allies: true,
            preserve_manual: true,
            delay_seconds: 2,
            picks: vec![],
            bans: vec![],
            position: String::new(),
        }
    }
}

impl Settings {
    /// load restores user rules and reports malformed files to the caller.
    /// A missing file is normal on first launch and selects empty priorities;
    /// the separate runtime enable switch is never read from this file.
    pub fn load() -> Result<Self> {
        let path = data_directory().join("automation.json");
        if !path.exists() {
            return Ok(Self::default());
        }
        Ok(serde_json::from_slice(&std::fs::read(path)?)?)
    }

    /// save persists champion rules without storing account or client authentication.
    pub fn save(&self) -> Result<()> {
        let directory = data_directory();
        std::fs::create_dir_all(&directory)?;
        std::fs::write(
            directory.join("automation.json"),
            serde_json::to_vec_pretty(self)?,
        )?;
        Ok(())
    }
}

/// ActionKind distinguishes draft picks from bans and their separate legal pools.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum ActionKind {
    Pick,
    Ban,
}

impl ActionKind {
    /// protocol returns the exact action type accepted by the client.
    pub fn protocol(self) -> &'static str {
        match self {
            Self::Pick => "pick",
            Self::Ban => "ban",
        }
    }
}

/// PlannedAction identifies one candidate write that still requires fresh validation.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct PlannedAction {
    pub id: i64,          // Current session-local action numeric identifier.
    pub champion: u32,    // Verified legal configured champion identifier.
    pub complete: bool,   // Complete only a currently active action.
    pub kind: ActionKind, // Pick or ban endpoint action type.
}

/// Decision communicates a proposed operation or a visible reason to wait.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Decision {
    Wait(&'static str),
    Act(PlannedAction),
}

/// decide chooses a configured champion from the current local action and pool.
/// Callers must supply independently fetched pickable, bannable, and disabled
/// observations; missing metadata leaves the action untouched. Only a verified
/// local draft action can complete, while preselection can update a pending pick
/// during planning. Friendly intents protect bans, not opposing hidden intents.
/// A manual choice remains untouched unless own_pick identifies this runtime's
/// previous successful selection of that same action and champion.
pub fn decide(
    settings: &Settings,
    session: &Value,
    pickable: Option<&[u32]>,
    bannable: Option<&[u32]>,
    disabled: Option<&[u32]>,
    own_pick: Option<(i64, u32)>,
) -> Decision {
    let queue = num(&session["queueId"]);
    if Mode::from_queue(queue, session["benchEnabled"] == true) != Mode::Ranked {
        return Decision::Wait("随机选人模式不执行自动选禁");
    }
    let local = session["localPlayerCellId"].as_i64().unwrap_or(-1);
    if local < 0 {
        return Decision::Wait("等待本人的选人位置");
    }
    let actions: Vec<_> = session["actions"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_array)
        .flatten()
        .collect();
    let action = actions
        .iter()
        .copied()
        .filter(|a| {
            a["actorCellId"].as_i64() == Some(local)
                && a["completed"] == false
                && (a["type"] == "pick" || a["type"] == "ban")
        })
        .find(|a| a["isInProgress"] == true)
        .or_else(|| {
            if settings.preselect {
                actions.iter().copied().find(|a| {
                    a["actorCellId"].as_i64() == Some(local)
                        && a["completed"] == false
                        && a["type"] == "pick"
                })
            } else {
                None
            }
        });
    let Some(action) = action else {
        return Decision::Wait("等待本人的选禁回合");
    };
    let Some(id) = action["id"].as_i64() else {
        return Decision::Wait("选禁动作尚不可用");
    };
    let kind = if action["type"] == "ban" {
        ActionKind::Ban
    } else {
        ActionKind::Pick
    };
    let active = action["isInProgress"] == true;
    if kind == ActionKind::Ban && (!settings.auto_ban || !active) {
        return Decision::Wait("自动禁用未开启");
    }
    if kind == ActionKind::Pick && ((!settings.auto_pick || !active) && !settings.preselect) {
        return Decision::Wait("自动选用未开启");
    }
    let members: Vec<_> = session["myTeam"].as_array().into_iter().flatten().collect();
    if !settings.position.is_empty()
        && !members.iter().any(|m| {
            m["cellId"].as_i64() == Some(local)
                && m["assignedPosition"].as_str() == Some(settings.position.as_str())
        })
    {
        return Decision::Wait("当前分路与规则不符");
    }
    let current = num(&action["championId"]) as u32;
    let manual = if current > 0 {
        current
    } else {
        members
            .iter()
            .find(|m| m["cellId"].as_i64() == Some(local))
            .map(|m| num(&m["championPickIntent"]) as u32)
            .unwrap_or(0)
    };
    if kind == ActionKind::Pick
        && settings.preserve_manual
        && manual > 0
        && own_pick != Some((id, manual))
    {
        return Decision::Wait("已保留手动预选，请手动锁定或关闭保护");
    }
    let Some(disabled) = disabled else {
        return Decision::Wait("等待客户端禁用英雄列表");
    };
    let (priorities, pool) = if kind == ActionKind::Ban {
        (&settings.bans, bannable)
    } else {
        (&settings.picks, pickable)
    };
    let Some(pool) = pool else {
        return Decision::Wait("等待客户端可选／可禁列表");
    };
    let candidate = priorities.iter().copied().find(|hero| {
        *hero > 0
            && pool.contains(hero)
            && !disabled.contains(hero)
            && !actions
                .iter()
                .any(|a| a["completed"] == true && num(&a["championId"]) == u64::from(*hero))
            && (kind != ActionKind::Ban
                || !settings.protect_allies
                || (!settings.picks.contains(hero)
                    && !members.iter().any(|m| {
                        num(&m["championPickIntent"]) == u64::from(*hero)
                            || num(&m["championId"]) == u64::from(*hero)
                    })))
    });
    let Some(champion) = candidate else {
        return Decision::Wait("优先列表暂无可用英雄，等待手动操作");
    };
    let complete = active && (kind == ActionKind::Ban || settings.auto_pick && settings.lock_pick);
    if !complete && current == champion {
        return Decision::Wait("已预选，等待本人回合或手动锁定");
    }
    Decision::Act(PlannedAction {
        id,
        champion,
        complete,
        kind,
    })
}

/// Execution tracks delays and write attempts within one observed selection session.
#[derive(Default)]
pub struct Execution {
    attempted: BTreeSet<PlannedAction>, // Operations already submitted in this session.
    pending: Option<(PlannedAction, Instant)>, // Stable candidate and first observed instant.
    pub own_pick: Option<(i64, u32)>,   // Last successful pick from this runtime.
}

impl Execution {
    /// ready delays a stable candidate and suppresses previously attempted operations.
    /// Callers reset this state when leaving selection or applying new rules.
    /// Changing the candidate restarts its delay rather than inheriting another
    /// hero's countdown, and a failed request remains attempted until that reset.
    pub fn ready(&mut self, plan: &PlannedAction, delay: u64, now: Instant) -> bool {
        if self.attempted.contains(plan) {
            return false;
        }
        if self.pending.as_ref().is_none_or(|(old, _)| old != plan) {
            self.pending = Some((plan.clone(), now));
        }
        self.pending.as_ref().is_some_and(|(_, at)| {
            now.saturating_duration_since(*at) >= Duration::from_secs(delay.min(10))
        })
    }

    /// attempted records a submitted operation before its possibly ambiguous network result.
    pub fn attempted(&mut self, plan: PlannedAction) {
        self.attempted.insert(plan);
    }

    /// succeeded retains ownership needed to complete an earlier automatic hover.
    pub fn succeeded(&mut self, plan: &PlannedAction) {
        if plan.kind == ActionKind::Pick {
            self.own_pick = Some((plan.id, plan.champion));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// session creates a local active draft pick for interaction scenarios.
    fn session() -> Value {
        json!({"queueId":420,"localPlayerCellId":0,"myTeam":[{"cellId":0,"championPickIntent":0}],
        "actions":[[{"id":1,"actorCellId":0,"type":"pick","completed":false,"isInProgress":true,"championId":0}]]})
    }

    /// picks_fall_back_without_operating_another_player protects turn and availability rules.
    /// A banned first choice selects the next configured legal hero; another
    /// player's action and missing availability produce no operation.
    #[test]
    fn picks_fall_back_without_operating_another_player() {
        let config = Settings {
            picks: vec![99, 103],
            delay_seconds: 0,
            ..Settings::default()
        };
        let mut v = session();
        assert!(matches!(
            decide(&config, &v, Some(&[103]), None, Some(&[]), None),
            Decision::Act(PlannedAction {
                champion: 103,
                complete: true,
                ..
            })
        ));
        v["actions"][0][0]["actorCellId"] = json!(1);
        assert!(matches!(
            decide(&config, &v, Some(&[103]), None, Some(&[]), None),
            Decision::Wait(_)
        ));
        assert!(matches!(
            decide(&config, &session(), None, None, Some(&[]), None),
            Decision::Wait(_)
        ));
    }

    /// bans_protect_friendly_intents_and_use_the_bannable_pool protects teammate choices.
    /// A friend's intended champion is skipped even when bannable, and a ban
    /// outside the currently active local turn cannot complete.
    #[test]
    fn bans_protect_friendly_intents_and_use_the_bannable_pool() {
        let config = Settings {
            auto_ban: true,
            bans: vec![99, 103],
            ..Settings::default()
        };
        let mut v = session();
        v["actions"][0][0]["type"] = json!("ban");
        v["myTeam"]
            .as_array_mut()
            .unwrap()
            .push(json!({"cellId":1,"championPickIntent":99}));
        assert!(matches!(
            decide(&config, &v, None, Some(&[99, 103]), Some(&[]), None),
            Decision::Act(PlannedAction {
                champion: 103,
                kind: ActionKind::Ban,
                complete: true,
                ..
            })
        ));
        v["actions"][0][0]["isInProgress"] = json!(false);
        assert!(matches!(
            decide(&config, &v, None, Some(&[99, 103]), Some(&[]), None),
            Decision::Wait(_)
        ));
    }

    /// manual_choices_and_random_modes_remain_untouched protects user-controlled selection changes.
    /// An automatic hover can later lock, while an unrelated manual choice,
    /// completed action, or Mayhem queue cannot produce a replacement write.
    #[test]
    fn manual_choices_and_random_modes_remain_untouched() {
        let config = Settings {
            picks: vec![99],
            ..Settings::default()
        };
        let mut v = session();
        v["actions"][0][0]["championId"] = json!(99);
        assert!(matches!(
            decide(&config, &v, Some(&[99]), None, Some(&[]), None),
            Decision::Wait(_)
        ));
        assert!(matches!(
            decide(&config, &v, Some(&[99]), None, Some(&[]), Some((1, 99))),
            Decision::Act(_)
        ));
        v["actions"][0][0]["completed"] = json!(true);
        assert!(matches!(
            decide(&config, &v, Some(&[99]), None, Some(&[]), Some((1, 99))),
            Decision::Wait(_)
        ));
        v["queueId"] = json!(3270);
        assert!(matches!(
            decide(&config, &v, Some(&[99]), None, Some(&[]), None),
            Decision::Wait(_)
        ));
    }

    /// delays_restart_and_ambiguous_requests_do_not_repeat protects polling execution behavior.
    /// Changing the fallback hero restarts the countdown; an attempted operation
    /// stays suppressed even if the server's response never confirmed success.
    #[test]
    fn delays_restart_and_ambiguous_requests_do_not_repeat() {
        let now = Instant::now();
        let mut state = Execution::default();
        let mut plan = PlannedAction {
            id: 1,
            champion: 99,
            complete: true,
            kind: ActionKind::Pick,
        };
        assert!(!state.ready(&plan, 2, now));
        plan.champion = 103;
        assert!(!state.ready(&plan, 2, now + Duration::from_secs(2)));
        assert!(state.ready(&plan, 2, now + Duration::from_secs(4)));
        state.attempted(plan.clone());
        assert!(!state.ready(&plan, 2, now + Duration::from_secs(8)));
    }
}
