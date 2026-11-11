//! model.rs normalizes client rooms, public statistics, and match contributions.
//! A room preserves pick intentions independently from locked selections, and
//! enemy intentions are never promoted into public information. Recommendations
//! consume the available pool supplied by that room rather than the complete
//! champion catalog. This prevents an ARAM suggestion from naming a champion the
//! current room cannot select, even when its public win rate is higher.
//!
//! Public observations remain grouped by mode and source. An absent adjustment
//! list is retained as unknown, incoming damage reverses benefit direction, and
//! contribution normalization compares raw values across the same match. Team
//! share is calculated separately so the largest contributor can reach the
//! radar's outer boundary while still owning only part of the team's damage.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::BTreeMap;

/// Mode identifies separate gameplay and public-statistic interpretation rules.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub enum Mode {
    #[default]
    Ranked,
    Aram,
    Mayhem,
}

impl Mode {
    /// label returns the localized gameplay name used by the interface.
    pub fn label(self) -> &'static str {
        match self {
            Self::Ranked => "召唤师峡谷",
            Self::Aram => "极地大乱斗",
            Self::Mayhem => "海克斯大乱斗",
        }
    }

    /// from_queue recognizes matchmaking and client-published custom mode identifiers.
    /// The client's queue catalog identifies 3270 as KIWI and the four custom
    /// identifiers below as specific ARAM variants. Unknown bench queues retain
    /// ordinary ARAM presentation without assigning Mayhem statistics to an
    /// unverified mode.
    pub fn from_queue(queue: u64, bench: bool) -> Self {
        match queue {
            2400 | 3270 => Self::Mayhem,
            450 | 3200 | 3210 | 3220 | 3230 => Self::Aram,
            _ if bench => Self::Aram,
            _ => Self::Ranked,
        }
    }
}

/// Adjustment retains a source value and its gameplay benefit direction.
#[derive(Clone, Debug)]
pub struct Adjustment {
    pub label: String,   // Localized affected gameplay property.
    pub display: String, // Signed change with source units.
    pub helpful: bool,   // Benefit after incoming-damage reversal.
}

/// Statistics groups observations that share a mode and source context.
#[derive(Clone, Debug, Default)]
pub struct Statistics {
    pub tier: Option<String>,  // Source-specific categorical strength label.
    pub win_rate: Option<f64>, // Wins divided by observed matches.
    pub samples: u64,          // Number of observed source matches.
    pub adjustments: Option<Vec<Adjustment>>, // None means source omitted adjustments.
    pub source: String,        // Visible source and patch context.
}

/// Champion describes an identified hero and separate mode-specific observations.
#[derive(Clone, Debug, Default)]
pub struct Champion {
    pub id: u32,            // Client champion numeric identifier.
    pub name: String,       // Localized champion personal name.
    pub title: String,      // Localized champion display title.
    pub alias: String,      // Official static asset filename stem.
    pub roles: Vec<String>, // Source-provided general gameplay roles.
    pub aram: Statistics,   // Ordinary ARAM observations only.
    pub mayhem: Statistics, // Mayhem observations from RESG only.
}

impl Champion {
    /// stats selects observations without blending ordinary ARAM and Mayhem sources.
    pub fn stats(&self, mode: Mode) -> &Statistics {
        if mode == Mode::Mayhem {
            &self.mayhem
        } else {
            &self.aram
        }
    }
}

/// Member represents a public or friendly slot in a champion selection room.
#[derive(Clone, Debug, Default)]
pub struct Member {
    pub cell: i64,        // Session-local player slot identifier.
    pub champion: u32,    // Public current or friendly intended hero.
    pub position: String, // Client-assigned role when publicly available.
    pub locked: bool,     // Completed client pick action flag.
    pub local: bool,      // This slot belongs to the user.
}

/// Room holds a sanitized selection session and the available recommendation pool.
#[derive(Clone, Debug, Default)]
pub struct Room {
    pub mode: Mode,            // Detected gameplay and statistic mode.
    pub allies: Vec<Member>,   // Friendly current and intended heroes.
    pub enemies: Vec<Member>,  // Completed public enemy picks only.
    pub bench: Vec<u32>,       // Current client-provided replacement heroes.
    pub available: Vec<u32>,   // Currently legal candidate champion identifiers.
    pub available_known: bool, // False prevents unverified ranked recommendations.
    pub queue: u64,            // Client matchmaking queue numeric identifier.
}

impl Room {
    /// from_lcu converts a session while preserving hidden enemies and pool restrictions.
    /// Friendly intentions are useful before locking and are therefore retained.
    /// Enemy selections require completed pick actions, so an in-progress hover
    /// cannot become a counterpick target. ARAM candidates come from current and
    /// bench heroes, while ranked candidates require a successfully read pickable
    /// list. Bans, disabled heroes, and selected champions are excluded from ranked
    /// recommendations, including visible opposing picks when duplicate picks are
    /// not allowed by the session.
    pub fn from_lcu(
        session: &Value,
        queue: u64,
        pickable: Option<&[u32]>,
        disabled: &[u32],
    ) -> Self {
        let mode = Mode::from_queue(queue, session["benchEnabled"].as_bool().unwrap_or(false));
        let actions: Vec<&Value> = session["actions"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(Value::as_array)
            .flatten()
            .collect();
        let local = session["localPlayerCellId"].as_i64().unwrap_or(-1);
        let members = |key: &str, friendly: bool| -> Vec<Member> {
            session[key]
                .as_array()
                .into_iter()
                .flatten()
                .map(|m| {
                    let cell = m["cellId"].as_i64().unwrap_or(-1);
                    let locked = actions.iter().any(|a| {
                        a["actorCellId"].as_i64() == Some(cell)
                            && a["type"] == "pick"
                            && a["completed"] == true
                    });
                    let actual = num(&m["championId"]) as u32;
                    let champion = if friendly {
                        if actual > 0 {
                            actual
                        } else {
                            num(&m["championPickIntent"]) as u32
                        }
                    } else if locked {
                        actual
                    } else {
                        0
                    };
                    Member {
                        cell,
                        champion,
                        position: m["assignedPosition"].as_str().unwrap_or("").into(),
                        locked,
                        local: cell == local && friendly,
                    }
                })
                .collect()
        };
        let allies = members("myTeam", true);
        let enemies = members("theirTeam", false);
        let bench: Vec<u32> = session["benchChampions"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|v| num(&v["championId"]) as u32)
            .filter(|&id| id > 0)
            .collect();
        let available_known = mode != Mode::Ranked || pickable.is_some();
        let mut available = if mode == Mode::Ranked {
            pickable.unwrap_or(&[]).to_vec()
        } else {
            bench
                .iter()
                .copied()
                .chain(allies.iter().filter(|m| m.local).map(|m| m.champion))
                .collect()
        };
        let duplicates = session["allowDuplicatePicks"].as_bool().unwrap_or(false);
        available.retain(|id| {
            *id > 0
                && !disabled.contains(id)
                && (mode != Mode::Ranked
                    || (!actions.iter().any(|a| {
                        a["type"] == "ban"
                            && a["completed"] == true
                            && num(&a["championId"]) as u32 == *id
                    }) && (duplicates
                        || !allies
                            .iter()
                            .chain(&enemies)
                            .any(|m| m.locked && m.champion == *id))))
        });
        available.sort_unstable();
        available.dedup();
        Self {
            mode,
            allies,
            enemies,
            bench,
            available,
            available_known,
            queue,
        }
    }
}

/// Player contains only combat observations required for contribution comparisons.
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Player {
    pub champion_id: u32, // Participant's chosen champion identifier.
    pub name: String,     // Champion name rather than account identity.
    pub team: u64,        // Numeric match-side team identifier.
    #[serde(rename = "self")]
    pub local: bool, // Participant matches the local player.
    pub kills: u64,       // Recorded participant kill count.
    pub deaths: u64,      // Recorded participant death count.
    pub assists: u64,     // Recorded participant assist count.
    pub damage: u64,      // Champion damage in this match.
    pub gold: u64,        // Total gold earned this match.
    pub taken: u64,       // Recorded incoming damage amount.
    pub heal: u64,        // Recorded total healing amount.
    pub tower_damage: u64, // Recorded damage to enemy turrets.
}

/// MatchRecord stores a match summary and optionally its complete combat roster.
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default, rename_all = "camelCase")]
pub struct MatchRecord {
    pub id: u64,              // Stable client match numeric identifier.
    pub champion_id: u32,     // Local participant's selected champion.
    pub name: String,         // Localized local champion display name.
    pub queue: String,        // Localized gameplay mode display name.
    pub queue_id: u64,        // Client matchmaking queue numeric identifier.
    pub win: bool,            // Local participant's match victory flag.
    pub kills: u64,           // Local participant's kill count.
    pub deaths: u64,          // Local participant's death count.
    pub assists: u64,         // Local participant's assist count.
    pub cs: u64,              // Local lane and jungle minions killed.
    pub damage: u64,          // Local damage dealt to champions.
    pub gold: u64,            // Local total gold earned.
    pub duration: u64,        // Match duration in whole seconds.
    pub created: i64,         // Match start Unix time in milliseconds.
    pub team: u64,            // Local participant's numeric team identifier.
    pub players: Vec<Player>, // Available match combat roster observations.
    pub source: String,       // Transport supplying this match observation.
    pub game_type: String,    // Client game type when explicitly supplied.
    pub remake: bool,         // Client marks an aborted or remade game.
    /// Each entry marks a missing source statistic across the participating roster;
    /// the matching contribution stays unknown instead of interpreting absence as zero.
    pub missing_metrics: [bool; 5], // Damage, gold, taken, healing, turret availability.
}

impl MatchRecord {
    /// from_lcu locates the local participant before translating client combat fields.
    /// Match-history responses can contain only the local participant; detail
    /// responses may contain the complete roster. Missing local identity returns
    /// None so another player's summary is never shown as the user's performance.
    /// The source must also provide an explicit victory boolean; absence cannot
    /// become an observed loss. Team metric availability is recorded independently
    /// for damage, gold, incoming damage, healing, and turret damage across every
    /// returned participant, allowing complete rosters to retain partially unknown
    /// contribution values without replacing missing source fields with observations.
    pub fn from_lcu(game: &Value, puuid: &str, catalog: &BTreeMap<u32, Champion>) -> Option<Self> {
        let own = game["participantIdentities"]
            .as_array()?
            .iter()
            .find(|v| v["player"]["puuid"].as_str() == Some(puuid))?["participantId"]
            .as_u64()?;
        let parts = game["participants"].as_array()?;
        let me = parts
            .iter()
            .find(|p| p["participantId"].as_u64() == Some(own))?;
        let stats = &me["stats"];
        let win = stats["win"].as_bool()?;
        let champion_id = num(&me["championId"]) as u32;
        let queue_id = num(&game["queueId"]);
        let players = parts
            .iter()
            .map(|p| {
                let s = &p["stats"];
                let id = num(&p["championId"]) as u32;
                Player {
                    champion_id: id,
                    name: catalog
                        .get(&id)
                        .map(|c| c.name.clone())
                        .unwrap_or_else(|| format!("英雄 {id}")),
                    team: num(&p["teamId"]),
                    local: num(&p["participantId"]) == own,
                    kills: num(&s["kills"]),
                    deaths: num(&s["deaths"]),
                    assists: num(&s["assists"]),
                    damage: num(&s["totalDamageDealtToChampions"]),
                    gold: num(&s["goldEarned"]),
                    taken: num(&s["totalDamageTaken"]),
                    heal: num(&s["totalHeal"]),
                    tower_damage: num(&s["damageDealtToTurrets"]),
                }
            })
            .collect();
        Some(Self {
            id: num(&game["gameId"]),
            champion_id,
            name: catalog
                .get(&champion_id)
                .map(|c| c.name.clone())
                .unwrap_or_else(|| format!("英雄 {champion_id}")),
            queue: queue_label(queue_id).into(),
            queue_id,
            win,
            kills: num(&stats["kills"]),
            deaths: num(&stats["deaths"]),
            assists: num(&stats["assists"]),
            cs: num(&stats["totalMinionsKilled"]) + num(&stats["neutralMinionsKilled"]),
            damage: num(&stats["totalDamageDealtToChampions"]),
            gold: num(&stats["goldEarned"]),
            duration: num(&game["gameDuration"]),
            created: game["gameCreation"].as_i64().unwrap_or(0),
            team: num(&me["teamId"]),
            players,
            source: "LCU".into(),
            game_type: game["gameType"].as_str().unwrap_or("").into(),
            remake: stats["gameEndedInEarlySurrender"] == true
                || game["endOfGameResult"]
                    .as_str()
                    .is_some_and(|s| s.starts_with("Abort_")),
            missing_metrics: [
                "totalDamageDealtToChampions",
                "goldEarned",
                "totalDamageTaken",
                "totalHeal",
                "damageDealtToTurrets",
            ]
            .map(|field| parts.iter().any(|p| !p["stats"][field].is_number())),
        })
    }

    /// complete reports whether both five-player teams have their own identifiable participant.
    /// A long participant list alone cannot establish team contribution coverage.
    /// Imported records must contain exactly ten players, one local perspective,
    /// and two distinct teams containing five members apiece.
    /// Team identifiers must be the established blue and red sides. Arena groups
    /// or malformed teams cannot satisfy this five-versus-five comparison contract,
    /// even when their total participant count happens to reach ten. Individual
    /// metric availability remains a separate requirement checked by contribution.
    pub fn complete(&self) -> bool {
        if self.players.len() != 10 || self.players.iter().filter(|p| p.local).count() != 1 {
            return false;
        }
        let mut teams = BTreeMap::<u64, usize>::new();
        for player in &self.players {
            *teams.entry(player.team).or_default() += 1;
        }
        teams.len() == 2
            && teams.contains_key(&100)
            && teams.contains_key(&200)
            && teams.values().all(|count| *count == 5)
    }

    /// contribution separates team share, match maximum, and rank for one metric.
    /// Complete ten-player detail is required to make all-match claims. Missing
    /// detail or a zero denominator returns None; equal values share a rank.
    pub fn contribution(&self, metric: usize) -> Option<(f64, f64, usize, f64)> {
        if !self.complete() || self.missing_metrics.get(metric).copied().unwrap_or(true) {
            return None;
        }
        let own = self.players.iter().find(|p| p.local)?;
        let value = own.metric(metric) as f64;
        let team_total: u64 = self
            .players
            .iter()
            .filter(|p| p.team == own.team)
            .map(|p| p.metric(metric))
            .sum();
        let max = self.players.iter().map(|p| p.metric(metric)).max()? as f64;
        if team_total == 0 || max == 0.0 {
            return None;
        }
        let rank = 1 + self
            .players
            .iter()
            .filter(|p| p.metric(metric) > own.metric(metric))
            .count();
        let average = self
            .players
            .iter()
            .map(|p| p.metric(metric) as f64)
            .sum::<f64>()
            / self.players.len() as f64;
        Some((value / team_total as f64, value / max, rank, average / max))
    }
}

impl Player {
    /// metric selects the raw combat measurement for contribution rendering.
    pub fn metric(&self, index: usize) -> u64 {
        match index {
            0 => self.damage,
            1 => self.gold,
            2 => self.taken,
            3 => self.heal,
            _ => self.tower_damage,
        }
    }
}

/// Matchup retains the candidate perspective and source sample context.
#[derive(Clone, Debug)]
pub struct Matchup {
    pub champion: u32,    // Candidate champion numeric identifier.
    pub enemy: u32,       // Public chosen opposing champion identifier.
    pub position: String, // OP.GG lane identifier, independent of display text.
    pub wins: u64,        // Candidate victories in observed games.
    pub samples: u64,     // Total observed candidate-opponent games.
    pub source: String,   // Region, position, tier and patch context.
}

/// Database owns public champion observations and explicitly requested matchup results.
#[derive(Clone, Default)]
pub struct Database {
    pub champions: BTreeMap<u32, Champion>, // Public champion catalog and observations.
    pub matchups: Vec<Matchup>,             // Results loaded for a user-selected opponent.
    pub public_updated: String,             // Most recent external source refresh description.
}

impl Database {
    /// bundled loads public champion sources without shipping personal match history.
    /// Source labels retain snapshot timestamps until an external refresh succeeds.
    /// Matchups start empty and require an explicit query for a visible opponent;
    /// account history arrives independently through the local client worker.
    pub fn bundled() -> Self {
        let mut db = Self::default();
        let champions: Value = serde_json::from_str(include_str!("../assets/champions.json"))
            .expect("Bundled champion catalog is valid");
        db.merge_resg(&champions, "16.22");
        let balance: Value = serde_json::from_str(include_str!("../assets/mayhem-balance.json"))
            .expect("Bundled public balance data is valid");
        for (id, details) in balance["champions"].as_object().into_iter().flatten() {
            if let Some(c) = id.parse().ok().and_then(|id| db.champions.get_mut(&id)) {
                c.mayhem.adjustments = resg_adjustments(&details["mayhem"]["balance"]);
            }
        }
        db.merge_aram(
            &serde_json::from_str::<Value>(include_str!("../assets/aram.json")).unwrap(),
            "16.22",
        );
        db.merge_balance(
            &serde_json::from_str::<Value>(include_str!("../assets/aram-balance.json")).unwrap(),
        );
        db.public_updated = "内置统计快照 · RESG 2026-11-07 / OP.GG 2026-11-10".into();
        db
    }

    /// merge_resg replaces Mayhem observations from one explicitly identified patch.
    /// Catalog entries retain their ordinary ARAM observations. A changed patch
    /// clears old Mayhem adjustments until matching champion details arrive.
    pub fn merge_resg(&mut self, value: &Value, patch: &str) {
        for row in value.as_array().into_iter().flatten() {
            let id = num(&row["id"]) as u32;
            if id == 0 {
                continue;
            }
            let c = self.champions.entry(id).or_default();
            if !c.mayhem.source.contains(patch) {
                c.mayhem.adjustments = None;
            }
            c.id = id;
            c.name = row["title"].as_str().unwrap_or("").into();
            c.title = row["name"].as_str().unwrap_or("").into();
            c.alias = row["alias"].as_str().unwrap_or("").into();
            c.roles = row["roles"]
                .as_array()
                .into_iter()
                .flatten()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect();
            c.mayhem.tier = row["tier"].as_str().map(str::to_owned);
            c.mayhem.win_rate = row["wr"].as_f64();
            c.mayhem.samples = num(&row["tm"]);
            c.mayhem.source = format!("RESG · {patch} · 全样本");
        }
    }

    /// merge_aram updates ordinary ARAM strength without changing Mayhem statistics.
    pub fn merge_aram(&mut self, value: &Value, patch: &str) {
        for row in value["data"].as_array().into_iter().flatten() {
            if let Some(c) = self.champions.get_mut(&(num(&row["id"]) as u32)) {
                let s = &row["average_stats"];
                c.aram.tier = s["tier"].as_u64().map(|v| format!("T{v}"));
                c.aram.win_rate = s["win_rate"].as_f64();
                c.aram.samples = num(&s["play"]);
                c.aram.source = format!("OP.GG · 韩服 · 全段位 · {patch}");
            }
        }
    }

    /// merge_balance translates ordinary ARAM adjustments using each field's baseline.
    /// An omitted row leaves its previous observation unchanged. A present row
    /// with only baseline values represents a known empty adjustment list.
    pub fn merge_balance(&mut self, value: &Value) {
        for row in value["data"].as_array().into_iter().flatten() {
            if let Some(c) = self.champions.get_mut(&(num(&row["champion_id"]) as u32)) {
                let fields = [
                    ("damage_dealt", "造成伤害", 100.0, false),
                    ("damage_taken", "承受伤害", 100.0, true),
                    ("healing", "治疗效果", 100.0, false),
                    ("shield_amount", "护盾效果", 100.0, false),
                    ("attack_speed", "攻击速度", 100.0, false),
                    ("cooldown_reduction", "技能急速", 0.0, false),
                    ("tenacity", "韧性", 0.0, false),
                    ("energy_regen", "能量回复", 100.0, false),
                ];
                c.aram.adjustments = Some(
                    fields
                        .iter()
                        .filter_map(|(key, label, base, inverse)| {
                            row[*key].as_f64().filter(|v| *v != *base).map(|v| {
                                let delta = v - base;
                                Adjustment {
                                    label: (*label).into(),
                                    display: format!(
                                        "{delta:+.0}{}",
                                        if *base == 100.0 { "%" } else { "" }
                                    ),
                                    helpful: if *inverse { delta < 0.0 } else { delta > 0.0 },
                                }
                            })
                        })
                        .collect(),
                );
            }
        }
    }

    /// champion returns a safe catalog fallback for newly released heroes.
    pub fn champion(&self, id: u32) -> Champion {
        self.champions
            .get(&id)
            .cloned()
            .unwrap_or_else(|| Champion {
                id,
                name: if id == 0 {
                    "未选择".into()
                } else {
                    format!("英雄 {id}")
                },
                ..Champion::default()
            })
    }
}

/// resg_adjustments translates static RESG balance values without executing modules.
/// Missing or null balance data stays unknown. Percentage values without an
/// explicit sign are absolute multipliers relative to one hundred percent.
pub fn resg_adjustments(value: &Value) -> Option<Vec<Adjustment>> {
    Some(
        value
            .as_object()?
            .iter()
            .filter_map(|(label, raw)| {
                let text = raw.as_str()?;
                let number = text.trim_end_matches('%').parse::<f64>().ok()?;
                let base = if text.contains('%') && !text.starts_with(['+', '-']) {
                    100.0
                } else {
                    0.0
                };
                let delta = number - base;
                Some(Adjustment {
                    label: label.clone(),
                    display: format!("{delta:+.1}{}", if text.contains('%') { "%" } else { "" }),
                    helpful: if label.contains("承受")
                        || label.contains("受到")
                        || label.contains("所受")
                    {
                        delta < 0.0
                    } else {
                        delta > 0.0
                    },
                })
            })
            .collect(),
    )
}

/// num interprets absent unsigned protocol counters as zero for display.
pub fn num(value: &Value) -> u64 {
    value.as_u64().unwrap_or(0)
}

/// queue_label returns readable names for known and otherwise unclassified queues.
pub fn queue_label(queue: u64) -> &'static str {
    match queue {
        420 => "单双排位",
        440 => "灵活排位",
        450 => "极地大乱斗",
        2400 => "海克斯大乱斗",
        3100 => "峡谷自定义",
        3200 | 3210 | 3220 | 3230 => "大乱斗自定义",
        3270 => "海克斯自定义",
        430 => "匹配模式",
        400 => "征召模式",
        _ => "其他模式",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// custom_modes_use_their_own_statistics protects real custom-room classification.
    /// Current client metadata identifies these separate ARAM and KIWI queues,
    /// including rooms whose bench flag is false before a champion is assigned.
    #[test]
    fn custom_modes_use_their_own_statistics() {
        for queue in [3200, 3210, 3220, 3230] {
            assert_eq!(Mode::from_queue(queue, false), Mode::Aram);
        }
        assert_eq!(Mode::from_queue(3270, false), Mode::Mayhem);
        assert_eq!(Mode::from_queue(3270, true), Mode::Mayhem);
        assert_eq!(Mode::from_queue(3100, false), Mode::Ranked);
        assert_eq!(Mode::from_queue(9999, false), Mode::Ranked);
        assert_eq!(Mode::from_queue(9999, true), Mode::Aram);
    }

    /// hidden_enemy_intents_stay_hidden protects public-only counterpick targets.
    /// A friendly hover remains useful before locking; an opposing hover must not
    /// become visible until the corresponding completed pick action arrives.
    #[test]
    fn hidden_enemy_intents_stay_hidden() {
        let mut v = json!({"localPlayerCellId":0,"myTeam":[{"cellId":0,"championId":0,"championPickIntent":99}],"theirTeam":[{"cellId":5,"championId":103,"championPickIntent":103}],"actions":[[{"actorCellId":5,"championId":103,"type":"pick","completed":false}]]});
        let room = Room::from_lcu(&v, 420, Some(&[99, 103]), &[]);
        assert_eq!(room.allies[0].champion, 99);
        assert_eq!(room.enemies[0].champion, 0);
        v["actions"][0][0]["completed"] = json!(true);
        assert_eq!(
            Room::from_lcu(&v, 420, Some(&[99, 103]), &[]).enemies[0].champion,
            103
        );
    }

    /// recommendations_respect_room_availability protects actionable selection suggestions.
    /// Banned and disabled heroes must be excluded, and a missing ranked pool must
    /// not silently expand into every champion in the static catalog.
    #[test]
    fn recommendations_respect_room_availability() {
        let v = json!({"myTeam":[],"theirTeam":[],"actions":[[{"type":"ban","completed":true,"championId":55}]]});
        assert_eq!(
            Room::from_lcu(&v, 420, Some(&[55, 99, 142]), &[142]).available,
            vec![99]
        );
        assert!(!Room::from_lcu(&v, 420, None, &[]).available_known);
        let aram = json!({"benchEnabled":true,"localPlayerCellId":0,"myTeam":[{"cellId":0,"championId":99}],"benchChampions":[{"championId":12}],"theirTeam":[]});
        assert_eq!(
            Room::from_lcu(&aram, 450, None, &[]).available,
            vec![12, 99]
        );
    }

    /// balance_direction_and_mode_isolation protects the champion adjustment display.
    /// Incoming damage increases are harmful, and ordinary ARAM updates must never
    /// replace the distinct Mayhem shield value recorded by RESG.
    #[test]
    fn balance_direction_and_mode_isolation() {
        let db = Database::bundled();
        let c = db.champion(99);
        let ordinary = c.aram.adjustments.unwrap();
        let mayhem = c.mayhem.adjustments.unwrap();
        assert!(
            !mayhem
                .iter()
                .find(|a| a.label == "承受伤害")
                .unwrap()
                .helpful
        );
        assert_eq!(
            ordinary
                .iter()
                .find(|a| a.label == "护盾效果")
                .unwrap()
                .display,
            "-10%"
        );
        assert_eq!(
            mayhem
                .iter()
                .find(|a| a.label == "护盾效果")
                .unwrap()
                .display,
            "-20.0%"
        );
        assert!(resg_adjustments(&json!(null)).is_none());
    }

    /// largest_contributor_reaches_radar_edge protects the corrected comparison scale.
    /// The leading player owns only a fraction of team damage yet must reach one
    /// hundred percent of the all-match maximum on that radar dimension.
    #[test]
    fn largest_contributor_reaches_radar_edge() {
        let players = (0..10)
            .map(|i| Player {
                local: i == 0,
                team: if i < 5 { 100 } else { 200 },
                damage: if i == 0 { 320 } else { 170 },
                ..Player::default()
            })
            .collect();
        let record = MatchRecord {
            players,
            ..MatchRecord::default()
        };
        let (share, normalized, rank, _) = record.contribution(0).unwrap();
        assert!(share < 0.33);
        assert_eq!(normalized, 1.0);
        assert_eq!(rank, 1);
        let mut partial = record;
        partial.players.truncate(1);
        assert!(partial.contribution(0).is_none());
    }
}
