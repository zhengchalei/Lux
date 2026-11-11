//! scouting.rs represents queried players and the visible participants of a match.
//! Search profiles retain the queried identity with their own combat records, so
//! detail requests cannot accidentally use the logged-in player's perspective.
//! Recent summaries exclude known custom queues and games shorter than three
//! minutes, while the complete returned list remains available for inspection.
//!
//! Draft and in-game rosters have different visibility rules. Draft identities
//! require an explicitly non-hidden client member, and enemy champions still
//! require completed pick actions. Gameflow rosters are used only after selection.
//! Historical review reads the actual participant identities of a finished match;
//! its separate key and label prevent those observations from appearing live.
//! Analysis preferences bound the requested history independently from the ten
//! recent results displayed in each compact roster row. The returned sample
//! count stays visible even when the client provides fewer records than requested.
//! Mode-scoped summaries and outcome strips use the same filtering predicate.
//! Refresh states preserve the last successful profile until replacement succeeds;
//! their errors and timestamps remain separate from the retained observations.

use crate::model::{Database, MatchRecord, Mode, Room, num};
use anyhow::Result;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::BTreeMap;

/// AnalysisSettings persists the requested history depth without retaining player records.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct AnalysisSettings {
    pub match_count: usize, // Requested recent raw match count.
    pub same_mode: bool,    // Prefer the visible battle's gameplay mode.
}

impl Default for AnalysisSettings {
    /// default requests fifty records, matching the referenced Akari analysis preference.
    fn default() -> Self {
        Self {
            match_count: 50,
            same_mode: true,
        }
    }
}

impl AnalysisSettings {
    /// bounded_count constrains client requests to the supported one-to-two-hundred range.
    pub fn bounded_count(&self) -> usize {
        self.match_count.clamp(1, 200)
    }

    /// load restores analysis depth while treating an absent file as first launch.
    /// Invalid JSON is reported to the caller, which can retain the default.
    pub fn load() -> Result<Self> {
        let path = crate::sources::data_directory().join("analysis.json");
        if !path.exists() {
            return Ok(Self::default());
        }
        let mut settings: Self = serde_json::from_slice(&std::fs::read(path)?)?;
        settings.match_count = settings.bounded_count();
        Ok(settings)
    }

    /// save stores only the bounded analysis preference in the local application directory.
    pub fn save(&self) -> Result<()> {
        let directory = crate::sources::data_directory();
        std::fs::create_dir_all(&directory)?;
        std::fs::write(
            directory.join("analysis.json"),
            serde_json::to_vec_pretty(&Self {
                match_count: self.bounded_count(),
                same_mode: self.same_mode,
            })?,
        )?;
        Ok(())
    }
}

/// ProfileTarget distinguishes account search text from a previously visible identity.
#[derive(Clone, Debug)]
pub enum ProfileTarget {
    Current,
    Name(String),
    Puuid(String),
}

/// Profile associates one player with separately reported history and rank availability.
#[derive(Clone, Debug, Default)]
pub struct Profile {
    pub puuid: String,                 // Queried client-visible account identifier.
    pub name: String,                  // Public Riot ID or returned name.
    pub level: u64,                    // Client-provided summoner account level.
    pub rank: String,                  // Ranked solo display or availability explanation.
    pub matches: Vec<MatchRecord>,     // Records normalized from this player's perspective.
    pub history_error: Option<String>, // Failed history read remains explicitly unknown.
    pub captured: String,              // Local query completion timestamp string.
    pub requested: usize,              // Requested raw history depth for this result.
    pub coverage: HistoryCoverage,     // Actual provider, scope, and sample coverage.
}

/// HistoryCoverage describes the observed sample independently from the requested target.
/// Requested and scanned counts describe collection, while fetched, valid, and
/// complete counts successively describe the retained observations available to
/// consumers. The actual provider and queue remain attached to those counts.
#[derive(Clone, Debug, Default)]
pub struct HistoryCoverage {
    pub source: String,        // Provider actually supplying retained records.
    pub requested: usize,      // Target count after applying queue scope.
    pub fetched: usize,        // Retained unique records before validity filtering.
    pub scanned: usize,        // Unique records inspected during server pagination.
    pub valid: usize,          // Retained observations accepted by usability rules.
    pub complete: usize,       // Valid observations containing both full teams.
    pub queue_id: Option<u64>, // Exact server queue requested when scoped.
    pub note: Option<String>,  // Fallback, scan limit, or partial loading explanation.
}

/// ContributionSummary averages per-match team shares using only observed denominators.
/// Complete-game coverage and metric-specific coverage are independent: a full
/// roster with missing damage can still contribute observed gold, and absent
/// denominators never become recorded zero-percent shares in any aggregate.
#[derive(Clone, Debug, Default)]
pub struct ContributionSummary {
    pub games: usize,              // Valid matches containing both full teams.
    pub damage_games: usize,       // Matches with observed team damage denominator.
    pub taken_games: usize,        // Matches with observed team incoming denominator.
    pub gold_games: usize,         // Matches with observed team gold denominator.
    pub damage_share: Option<f64>, // Mean known team champion-damage fraction.
    pub taken_share: Option<f64>,  // Mean known team incoming-damage fraction.
    pub gold_share: Option<f64>,   // Mean known team gold-earned fraction.
}

/// usable_match applies the shared history and analysis eligibility rules.
/// Explicit remakes, non-matched games, known PVE queues, custom queues, and
/// sub-three-minute records stay inspectable outside recent competitive summaries.
/// Unknown legacy game types remain eligible because older saved records omit them.
/// Mode groups provide UI browsing scopes; exact queue collection happens in the
/// history repository before these rules run. The same predicate supplies profile
/// aggregates, hero aggregates, recent outcome strips, and contribution eligibility.
pub fn usable_match(record: &MatchRecord, mode: Option<Mode>) -> bool {
    let competitive = record.duration >= 180
        && !record.remake
        && !is_custom(record.queue_id)
        && (record.game_type.is_empty() || record.game_type == "MATCHED_GAME")
        && !matches!(
            record.queue_id,
            31 | 32
                | 33
                | 34
                | 35
                | 36
                | 52
                | 90
                | 91
                | 92
                | 800
                | 801
                | 810
                | 820
                | 830
                | 831
                | 832
                | 840
                | 841
                | 842
                | 850
                | 851
                | 852
                | 860
                | 870
                | 880
                | 890
                | 950
                | 951
                | 960
                | 961
                | 981
                | 982
                | 990
                | 1030
                | 1031
                | 1032
                | 1040
                | 1041
                | 1050
                | 1051
                | 1060
                | 1061
                | 1070
                | 1071
                | 1800
                | 1810
                | 1820
                | 1830
                | 1840
                | 1850
                | 1860
                | 1870
                | 1880
                | 1890
                | 2000
                | 2010
                | 2020
        );
    competitive
        && mode.is_none_or(|mode| match mode {
            Mode::Ranked => matches!(
                record.queue_id,
                400 | 420 | 430 | 440 | 480 | 490 | 700 | 720
            ),
            Mode::Aram => record.queue_id == 450,
            Mode::Mayhem => record.queue_id == 2400,
        })
}

/// HeroSummary counts usable recent observations of one particular champion.
#[derive(Clone, Debug, Default)]
pub struct HeroSummary {
    pub champion: u32, // Observed champion numeric identifier.
    pub games: usize,  // Counted usable recent game observations.
    pub wins: usize,   // Recorded victories in those observations.
}

/// RecentSummary aggregates usable observations without interpreting missing history as losses.
#[derive(Clone, Debug, Default)]
pub struct RecentSummary {
    pub games: usize,                      // Number of usable recent game observations.
    pub wins: usize,                       // Number of usable recent recorded victories.
    pub kda: f64,                          // Total kills and assists divided by deaths.
    pub average_kills: f64,                // Mean kills per usable recent game.
    pub average_deaths: f64,               // Mean deaths per usable recent game.
    pub average_assists: f64,              // Mean assists per usable recent game.
    pub heroes: Vec<HeroSummary>,          // Most used heroes sorted by observed frequency.
    pub contribution: ContributionSummary, // Complete-team shares and independent sample coverage.
}

impl Profile {
    /// recent yields usable games in the client's newest-first response order.
    /// Both the displayed result strip and aggregates exclude known custom games
    /// and sub-three-minute records, so their sample definitions stay consistent.
    pub fn recent(&self) -> impl Iterator<Item = &MatchRecord> {
        self.recent_in(None)
    }

    /// recent_in yields usable observations restricted to an optional gameplay mode.
    /// None explicitly includes every non-custom mode. Unknown queue identifiers
    /// remain available there but never become inferred Summoner's Rift records.
    pub fn recent_in(&self, mode: Option<Mode>) -> impl Iterator<Item = &MatchRecord> {
        self.matches.iter().filter(move |m| usable_match(m, mode))
    }

    /// summary aggregates recent games after excluding known custom queues and remakes.
    /// Empty usable history remains zero samples and must be rendered as unknown
    /// rather than a zero-percent win rate. Raw match rows are not discarded.
    /// Each combat average divides that stat's sum by the usable game count,
    /// whereas KDA divides total kills plus assists by total deaths. Those are
    /// separate quantities: averaging per-game KDA would overweight deathless
    /// games and would not match the displayed combined combat sample.
    pub fn summary(&self) -> RecentSummary {
        self.summary_in(None)
    }

    /// summary_in calculates combat totals over the same optional mode as recent_in.
    /// Empty selections retain zero samples rather than inventing observed losses.
    /// KDA uses total deaths while per-game averages divide by usable game count,
    /// keeping distinct denominators consistent across all four prepared summaries.
    pub fn summary_in(&self, mode: Option<Mode>) -> RecentSummary {
        summarize(self.recent_in(mode).collect())
    }
}

/// summarize combines one already-filtered set without cloning its match observations.
/// Combat averages divide by valid games while KDA divides by total deaths.
/// Every hero and contribution statistic consumes that same selected set, allowing
/// background preparation to reuse this calculation across profile and hero scopes.
/// Empty samples keep zero game coverage, while deathless nonempty samples use
/// one death only as the KDA divisor. Consumers decide whether to display a
/// missing value from coverage rather than interpreting empty aggregates as losses.
fn summarize(usable: Vec<&MatchRecord>) -> RecentSummary {
    // An empty sample has no visible average; the divisor avoids NaN while consumers retain the explicit zero-game state.
    let divisor = usable.len().max(1) as f64;
    let mut heroes = BTreeMap::<u32, HeroSummary>::new();
    for m in &usable {
        let hero = heroes.entry(m.champion_id).or_insert_with(|| HeroSummary {
            champion: m.champion_id,
            ..HeroSummary::default()
        });
        hero.games += 1;
        hero.wins += usize::from(m.win);
    }
    let mut heroes: Vec<_> = heroes.into_values().collect();
    heroes.sort_by_key(|h| (std::cmp::Reverse(h.games), h.champion));
    RecentSummary {
        games: usable.len(),
        wins: usable.iter().filter(|m| m.win).count(),
        kda: usable.iter().map(|m| m.kills + m.assists).sum::<u64>() as f64
            / usable.iter().map(|m| m.deaths).sum::<u64>().max(1) as f64,
        average_kills: usable.iter().map(|m| m.kills).sum::<u64>() as f64 / divisor,
        average_deaths: usable.iter().map(|m| m.deaths).sum::<u64>() as f64 / divisor,
        average_assists: usable.iter().map(|m| m.assists).sum::<u64>() as f64 / divisor,
        heroes,
        contribution: contribution_summary(&usable),
    }
}

/// contribution_summary combines complete games without manufacturing missing team statistics.
/// Each observed fraction uses its own match denominator before averaging, so
/// long or high-damage matches cannot silently dominate the recent sample. The
/// full-roster count remains separate from metric-specific availability, and an
/// entirely missing metric produces None for the UI's unknown state. Zero team
/// totals are excluded rather than becoming a recorded zero-percent contribution.
fn contribution_summary(games: &[&MatchRecord]) -> ContributionSummary {
    let average = |metric| {
        let values: Vec<f64> = games
            .iter()
            .filter_map(|game| game.contribution(metric).map(|v| v.0))
            .collect();
        (!values.is_empty()).then(|| values.iter().sum::<f64>() / values.len() as f64)
    };
    ContributionSummary {
        games: games.iter().filter(|game| game.complete()).count(),
        damage_games: games
            .iter()
            .filter(|game| game.contribution(0).is_some())
            .count(),
        taken_games: games
            .iter()
            .filter(|game| game.contribution(2).is_some())
            .count(),
        gold_games: games
            .iter()
            .filter(|game| game.contribution(1).is_some())
            .count(),
        damage_share: average(0),
        taken_share: average(2),
        gold_share: average(1),
    }
}

/// PreparedProfile stores all-mode and mode-specific aggregates beside their source observations.
/// The profile and summaries are replaced together when client data changes;
/// pointer redraws select an existing summary without rescanning combat records.
#[derive(Clone, Debug)]
pub struct PreparedProfile {
    pub profile: Profile,                      // Original client profile snapshot.
    pub summary: RecentSummary,                // All-mode usable combat aggregates.
    modes: [RecentSummary; 3],                 // Rift, ordinary ARAM, then Mayhem aggregates.
    heroes: BTreeMap<u32, [RecentSummary; 4]>, // Per-champion all-mode and mode-scoped aggregates.
}

impl PreparedProfile {
    /// new prepares each supported analysis scope before publishing a profile.
    /// Callers execute preparation on a background task when source observations
    /// change. Overall summaries and each observed champion's four browsing scopes
    /// are computed from borrowed records, retaining a single profile collection.
    /// Rendering and champion selection then read cached aggregates instead of
    /// cloning full matches or repeating contribution calculations during pointer
    /// movement, scrolling, and repeated native window paints.
    pub fn new(profile: Profile) -> Self {
        let summary = profile.summary();
        let modes =
            [Mode::Ranked, Mode::Aram, Mode::Mayhem].map(|mode| profile.summary_in(Some(mode)));
        let heroes = summary
            .heroes
            .iter()
            .map(|hero| {
                let summaries = [
                    None,
                    Some(Mode::Ranked),
                    Some(Mode::Aram),
                    Some(Mode::Mayhem),
                ]
                .map(|mode| {
                    summarize(
                        profile
                            .recent_in(mode)
                            .filter(|game| game.champion_id == hero.champion)
                            .collect(),
                    )
                });
                (hero.champion, summaries)
            })
            .collect();
        Self {
            profile,
            summary,
            modes,
            heroes,
        }
    }

    /// summary_for selects precomputed aggregates matching the displayed analysis scope.
    pub fn summary_for(&self, mode: Option<Mode>) -> &RecentSummary {
        match mode {
            None => &self.summary,
            Some(Mode::Ranked) => &self.modes[0],
            Some(Mode::Aram) => &self.modes[1],
            Some(Mode::Mayhem) => &self.modes[2],
        }
    }

    /// hero_summary selects one precomputed champion sample without rescanning match observations.
    /// All supported scopes are prepared with the profile on its background task.
    /// A champion absent from the retained sample returns an empty known sample.
    pub fn hero_summary(&self, champion: u32, mode: Option<Mode>) -> RecentSummary {
        let index = match mode {
            None => 0,
            Some(Mode::Ranked) => 1,
            Some(Mode::Aram) => 2,
            Some(Mode::Mayhem) => 3,
        };
        self.heroes
            .get(&champion)
            .map(|summaries| summaries[index].clone())
            .unwrap_or_default()
    }
}

impl std::ops::Deref for PreparedProfile {
    type Target = Profile;
    /// deref exposes immutable source observations without duplicating their match collection.
    fn deref(&self) -> &Self::Target {
        &self.profile
    }
}

/// ScoutState keeps refresh progress and failures separate from the last readable profile.
#[derive(Clone, Debug, Default)]
pub struct ScoutState {
    pub profile: Option<PreparedProfile>, // Latest accepted source observations.
    pub updating: bool,                   // One queued or active player refresh.
    pub error: Option<String>,            // Latest refresh failure, including history denial.
}

impl ScoutState {
    /// begin marks one visible player pending while retaining previously displayed records.
    pub fn begin(&mut self) {
        self.updating = true;
        self.error = None;
    }

    /// finish accepts new observations while preserving readable history after a failed refresh.
    /// A resolved identity with unreadable history still provides rank on first load.
    /// When an older readable profile exists, that entire snapshot survives failure,
    /// including its original timestamp; the failure belongs to the refresh instead.
    /// Successful empty histories replace old records because no games is a valid
    /// response. Callers must validate roster and account association before applying
    /// a result, since this state represents only one already-associated participant.
    pub fn finish(&mut self, result: Result<Profile, String>) {
        self.finish_prepared(result.map(PreparedProfile::new));
    }

    /// finish_prepared accepts background aggregates while preserving the last readable player snapshot.
    /// A failed refresh stores its error independently from the previous profile.
    /// Successful empty histories replace old records, and callers still own the
    /// roster-generation check before delivering a prepared result to this state.
    /// On first load, a resolved player with denied history may still establish
    /// identity and rank. Later denial cannot attach a new capture timestamp to
    /// an older successful history because the retained profile remains unchanged.
    pub fn finish_prepared(&mut self, result: Result<PreparedProfile, String>) {
        self.updating = false;
        match result {
            Ok(profile) => {
                self.error = profile.history_error.clone();
                // A failed history read cannot erase an older readable sample or attach a new timestamp to those retained records.
                if self.error.is_none()
                    || self
                        .profile
                        .as_ref()
                        .is_none_or(|old| old.history_error.is_some())
                {
                    self.profile = Some(profile);
                }
            }
            Err(error) => self.error = Some(error),
        }
    }
}

/// ClientAccount identifies the current local account and its displayed connection context.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ClientAccount {
    pub puuid: String,  // Session account association identifier.
    pub name: String,   // Public client-returned Riot display name.
    pub region: String, // Client region code or explicit unknown label.
}

impl ClientAccount {
    /// same_account compares account and region without treating name edits as switches.
    pub fn same_account(&self, other: &Self) -> bool {
        self.puuid == other.puuid && self.region == other.region
    }
}

/// is_custom identifies client-verified custom queues excluded from recent competitive summaries.
pub fn is_custom(queue: u64) -> bool {
    matches!(
        queue,
        3100 | 3110 | 3120 | 3130 | 3200 | 3210 | 3220 | 3230 | 3270
    )
}

/// identity accepts real client account identifiers and rejects empty placeholder identities.
/// It never reads obfuscated identity fields. Callers separately enforce whether
/// the account is publicly visible at the current gameflow stage.
pub fn identity(value: &Value) -> Option<String> {
    value
        .as_str()
        .filter(|s| {
            !s.is_empty()
                && !s.chars().all(|c| c == '0' || c == '-')
                && s.len() <= 128
                && s.chars()
                    .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
        })
        .map(str::to_owned)
}

/// display_name formats a returned public Riot ID without inventing a missing tagline.
pub fn display_name(value: &Value) -> String {
    let name = value["gameName"]
        .as_str()
        .filter(|s| !s.is_empty())
        .or_else(|| value["displayName"].as_str())
        .unwrap_or("召唤师");
    match value["tagLine"].as_str().filter(|s| !s.is_empty()) {
        Some(tag) => format!("{name}#{tag}"),
        None => name.into(),
    }
}

/// rank_label translates the current solo queue result without guessing absent metadata.
pub fn rank_label(value: &Value) -> String {
    let queue = &value["queueMap"]["RANKED_SOLO_5x5"];
    let Some(tier) = queue["tier"].as_str() else {
        return "段位暂不可用".into();
    };
    let tier = match tier {
        "IRON" => "黑铁",
        "BRONZE" => "青铜",
        "SILVER" => "白银",
        "GOLD" => "黄金",
        "PLATINUM" => "铂金",
        "EMERALD" => "翡翠",
        "DIAMOND" => "钻石",
        "MASTER" => "大师",
        "GRANDMASTER" => "宗师",
        "CHALLENGER" => "王者",
        "NONE" | "" => return "单排未定级".into(),
        other => other,
    };
    format!(
        "{} {} · {} LP",
        tier,
        queue["division"].as_str().unwrap_or(""),
        num(&queue["leaguePoints"])
    )
}

/// Combatant describes one visible match slot without decoding anonymized identities.
#[derive(Clone, Debug, Default)]
pub struct Combatant {
    pub key: String,           // Stable side and slot association key.
    pub puuid: Option<String>, // None represents an unrevealed account identity.
    pub name: String,          // Public display or explicit unknown label.
    pub champion: u32,         // Publicly assigned or locked champion identifier.
    pub position: String,      // Client-provided public role assignment string.
    pub ally: bool,            // Slot belongs to the local player's team.
    pub local: bool,           // Slot is the current logged-in player.
    pub bot: bool,             // Client explicitly identifies a computer participant.
}

/// Battle groups participants under a live-room or historical-review identity.
#[derive(Clone, Debug)]
pub struct Battle {
    pub key: String,             // Roster generation and query association key.
    pub label: String,           // Visible live or historical source label.
    pub mode: Mode,              // Champion strength and adjustment context.
    pub queue_id: u64,           // Exact public client matchmaking queue.
    pub review: bool,            // True labels a finished match review.
    pub players: Vec<Combatant>, // Currently visible friendly and opposing slots.
}

impl Battle {
    /// draft derives identities only from members the selection client leaves visible.
    /// Hidden and obfuscated identifiers are discarded even when present in the
    /// response. Enemy hero visibility follows Room's completed-action rule.
    pub fn draft(session: &Value, room: &Room) -> Self {
        let mut players = vec![];
        for (key, ally, roster) in [
            ("myTeam", true, &room.allies),
            ("theirTeam", false, &room.enemies),
        ] {
            for (index, m) in session[key].as_array().into_iter().flatten().enumerate() {
                let cell = m["cellId"].as_i64().unwrap_or(-1);
                let visible = m["nameVisibilityType"] != "HIDDEN" && m["isAnonymized"] != true;
                let member = roster.iter().find(|p| p.cell == cell);
                let puuid = if visible { identity(&m["puuid"]) } else { None };
                players.push(Combatant {
                    key: format!("{key}:{index}"),
                    name: if puuid.is_some() {
                        display_name(m)
                    } else {
                        "身份未公开".into()
                    },
                    puuid,
                    champion: member.map(|p| p.champion).unwrap_or(0),
                    position: m["assignedPosition"].as_str().unwrap_or("").into(),
                    ally,
                    local: member.is_some_and(|p| p.local),
                    bot: m["isBot"] == true,
                });
            }
        }
        Self {
            key: format!("draft:{}:{}", session["id"], num(&session["gameId"])),
            label: "实时选人 · 客户端公开阵容".into(),
            mode: room.mode,
            queue_id: room.queue,
            review: false,
            players,
        }
    }

    /// gameflow derives a public roster after selection from the gameflow team lists.
    /// Callers invoke this only during GameStart, InProgress, or Reconnect. The
    /// local identity determines team orientation; absent orientation returns None.
    pub fn gameflow(flow: &Value, own: &str) -> Option<Self> {
        let data = &flow["gameData"];
        let teams = [("teamOne", 100_u64), ("teamTwo", 200_u64)];
        let own_team = teams
            .iter()
            .find(|(key, _)| {
                data[*key]
                    .as_array()
                    .is_some_and(|members| members.iter().any(|m| m["puuid"].as_str() == Some(own)))
            })?
            .1;
        let mut players = vec![];
        for (key, team) in teams {
            for (index, m) in data[key].as_array().into_iter().flatten().enumerate() {
                let puuid = identity(&m["puuid"]);
                let champion = num(&m["championId"]) as u32;
                let champion = if champion > 0 {
                    champion
                } else {
                    data["playerChampionSelections"]
                        .as_array()
                        .into_iter()
                        .flatten()
                        .find(|p| p["puuid"] == m["puuid"])
                        .map(|p| num(&p["championId"]) as u32)
                        .unwrap_or(0)
                };
                players.push(Combatant {
                    key: format!("{key}:{index}"),
                    local: puuid.as_deref() == Some(own),
                    name: if m["isBot"] == true {
                        "电脑".into()
                    } else {
                        display_name(m)
                    },
                    puuid,
                    champion,
                    position: m["selectedPosition"].as_str().unwrap_or("").into(),
                    ally: team == own_team,
                    bot: m["isBot"] == true,
                });
            }
        }
        Some(Self {
            key: format!("game:{}", num(&data["gameId"])),
            label: "当前对局 · 客户端公开阵容".into(),
            mode: Mode::from_queue(num(&data["queue"]["id"]), false),
            queue_id: num(&data["queue"]["id"]),
            review: false,
            players,
        })
    }

    /// finished associates a completed match's public participants with their recorded champions.
    /// The historical label remains separate from live observations. Missing local
    /// participant identity prevents assigning friendly and enemy sides incorrectly.
    pub fn finished(game: &Value, own: &str, db: &Database) -> Option<Self> {
        let record = MatchRecord::from_lcu(game, own, &db.champions)?;
        let mut players = vec![];
        for p in game["participants"].as_array()? {
            let person = game["participantIdentities"]
                .as_array()?
                .iter()
                .find(|i| i["participantId"] == p["participantId"])?;
            let account = &person["player"];
            let puuid = identity(&account["puuid"]);
            players.push(Combatant {
                key: format!("participant:{}", num(&p["participantId"])),
                local: puuid.as_deref() == Some(own),
                puuid,
                name: display_name(account),
                champion: num(&p["championId"]) as u32,
                position: p["timeline"]["lane"].as_str().unwrap_or("").into(),
                ally: num(&p["teamId"]) == record.team,
                bot: false,
            });
        }
        Some(Self {
            key: format!("review:{}", record.id),
            label: format!("历史复盘 · {} · {}", record.name, record.queue),
            mode: Mode::from_queue(record.queue_id, false),
            queue_id: record.queue_id,
            review: true,
            players,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// complete_game constructs a full two-team observation with controlled contribution denominators.
    fn complete_game(id: u64, own_damage: u64, teammate_damage: u64) -> MatchRecord {
        MatchRecord {
            id,
            champion_id: 99,
            queue_id: 450,
            duration: 1200,
            game_type: "MATCHED_GAME".into(),
            players: (0..10)
                .map(|index| crate::model::Player {
                    local: index == 0,
                    team: if index < 5 { 100 } else { 200 },
                    damage: match index {
                        0 => own_damage,
                        1 => teammate_damage,
                        _ => 0,
                    },
                    gold: 100,
                    ..crate::model::Player::default()
                })
                .collect(),
            ..MatchRecord::default()
        }
    }

    /// contributions_average_per_game_with_independent_known_samples protects recent contribution meaning.
    /// Large and small damage games receive equal weight, personal-only records
    /// cannot supply team shares, and missing damage does not suppress known gold.
    #[test]
    fn contributions_average_per_game_with_independent_known_samples() {
        let first = complete_game(1, 500, 500);
        let second = complete_game(2, 1, 9);
        let mut missing = complete_game(3, 200, 100);
        missing.missing_metrics[0] = true;
        let partial = MatchRecord {
            id: 4,
            champion_id: 55,
            queue_id: 450,
            duration: 1200,
            ..MatchRecord::default()
        };
        let prepared = PreparedProfile::new(Profile {
            matches: vec![first, second, missing, partial],
            ..Profile::default()
        });
        let contribution = &prepared.summary.contribution;
        assert_eq!(prepared.summary.games, 4);
        assert_eq!(
            (
                contribution.games,
                contribution.damage_games,
                contribution.gold_games
            ),
            (3, 2, 3)
        );
        assert!((contribution.damage_share.unwrap() - 0.3).abs() < 0.00001);
        assert!((contribution.gold_share.unwrap() - 0.2).abs() < 0.00001);
        assert!(contribution.taken_share.is_none());
        assert_eq!(contribution.taken_games, 0);
        assert_eq!(prepared.hero_summary(99, Some(Mode::Aram)).games, 3);
        assert_eq!(prepared.hero_summary(99, Some(Mode::Ranked)).games, 0);
    }

    /// client_remake_and_game_type_flags_exclude_invalid_recent_samples protects source-aware eligibility.
    /// A long canceled game and a private match must remain excluded despite
    /// sharing the normal ARAM queue identifier with an otherwise valid game.
    #[test]
    fn client_remake_and_game_type_flags_exclude_invalid_recent_samples() {
        let mut record = complete_game(1, 20, 80);
        assert!(usable_match(&record, Some(Mode::Aram)));
        record.remake = true;
        assert!(!usable_match(&record, None));
        record.remake = false;
        record.game_type = "CUSTOM_GAME".into();
        assert!(!usable_match(&record, None));
        record.game_type = "MATCHED_GAME".into();
        record.queue_id = 830;
        assert!(!usable_match(&record, None));
    }

    /// mode_scopes_share_their_aggregate_and_result_samples prevents cross-mode player comparisons.
    /// Ordinary ARAM, Mayhem, Rift, unknown queues, remakes, and private matches coexist
    /// in the response, but each scoped summary must use only its own usable rows.
    #[test]
    fn mode_scopes_share_their_aggregate_and_result_samples() {
        let profile = Profile {
            matches: [420, 450, 2400, 1700, 3200, 450]
                .into_iter()
                .enumerate()
                .map(|(i, queue_id)| MatchRecord {
                    queue_id,
                    duration: if i == 5 { 90 } else { 1200 },
                    champion_id: i as u32 + 1,
                    kills: (i + 1) as u64,
                    deaths: 2,
                    assists: 4,
                    win: i == 1,
                    ..MatchRecord::default()
                })
                .collect(),
            ..Profile::default()
        };
        let prepared = PreparedProfile::new(profile);
        for (mode, hero, kills) in [
            (Mode::Ranked, 1, 1.),
            (Mode::Aram, 2, 2.),
            (Mode::Mayhem, 3, 3.),
        ] {
            let summary = prepared.summary_for(Some(mode));
            assert_eq!(summary.games, 1);
            assert_eq!(summary.average_kills, kills);
            assert_eq!(summary.heroes[0].champion, hero);
            assert_eq!(
                prepared
                    .recent_in(Some(mode))
                    .map(|m| m.champion_id)
                    .collect::<Vec<_>>(),
                vec![hero]
            );
        }
        assert_eq!(prepared.summary.games, 4);
        assert_eq!(prepared.summary_for(Some(Mode::Aram)).wins, 1);
        assert_eq!(prepared.summary_for(Some(Mode::Mayhem)).wins, 0);
        let empty = PreparedProfile::new(Profile::default());
        assert_eq!(empty.summary_for(Some(Mode::Aram)).games, 0);
    }

    /// refresh_failures_retain_original_records_and_timestamps protects progressive player updates.
    /// Both transport failure and history denial preserve the last readable snapshot;
    /// a later successful empty history must replace it rather than remain stale.
    #[test]
    fn refresh_failures_retain_original_records_and_timestamps() {
        let mut state = ScoutState::default();
        state.begin();
        state.finish(Ok(Profile {
            captured: "old".into(),
            matches: vec![MatchRecord {
                queue_id: 450,
                duration: 900,
                ..MatchRecord::default()
            }],
            ..Profile::default()
        }));
        state.begin();
        assert_eq!(state.profile.as_ref().unwrap().matches.len(), 1);
        assert!(state.updating);
        state.finish(Err("network".into()));
        assert_eq!(state.profile.as_ref().unwrap().captured, "old");
        assert!(!state.updating);
        assert_eq!(state.error.as_deref(), Some("network"));
        state.begin();
        state.finish(Ok(Profile {
            captured: "denied".into(),
            history_error: Some("denied".into()),
            ..Profile::default()
        }));
        assert_eq!(state.profile.as_ref().unwrap().captured, "old");
        assert_eq!(state.profile.as_ref().unwrap().matches.len(), 1);
        state.begin();
        state.finish(Ok(Profile {
            captured: "empty-success".into(),
            ..Profile::default()
        }));
        assert!(state.profile.as_ref().unwrap().matches.is_empty());
        assert_eq!(state.profile.as_ref().unwrap().captured, "empty-success");
        assert!(state.error.is_none());
        let mut first = ScoutState::default();
        first.finish(Ok(Profile {
            rank: "铂金".into(),
            history_error: Some("denied".into()),
            ..Profile::default()
        }));
        assert_eq!(first.profile.as_ref().unwrap().rank, "铂金");
        assert!(first.error.is_some());
    }

    /// existing_preferences_default_to_current_mode preserves earlier saved analysis depth.
    /// An explicit all-mode choice survives serialization, and account-name edits do
    /// not change account association while a different region does.
    #[test]
    fn existing_preferences_default_to_current_mode() {
        let mut settings: AnalysisSettings =
            serde_json::from_str(r#"{"match_count":100}"#).unwrap();
        assert!(settings.same_mode);
        assert_eq!(settings.bounded_count(), 100);
        settings.same_mode = false;
        let restored: AnalysisSettings =
            serde_json::from_str(&serde_json::to_string(&settings).unwrap()).unwrap();
        assert!(!restored.same_mode);
        let a = ClientAccount {
            puuid: "one".into(),
            name: "old".into(),
            region: "HN1".into(),
        };
        let mut b = a.clone();
        b.name = "new".into();
        assert!(a.same_account(&b));
        b.region = "HN2".into();
        assert!(!a.same_account(&b));
    }

    /// hidden_accounts_never_enter_the_lookup_roster protects anonymous draft participants.
    /// Even a nonempty real or obfuscated identifier in a hidden member is
    /// discarded, and an opposing hover remains an unknown hero.
    #[test]
    fn hidden_accounts_never_enter_the_lookup_roster() {
        let v = json!({"localPlayerCellId":0,"myTeam":[{"cellId":0,"puuid":"visible-own","nameVisibilityType":"VISIBLE","championPickIntent":99}],
            "theirTeam":[{"cellId":5,"puuid":"hidden-real","obfuscatedPuuid":"hidden-code","nameVisibilityType":"HIDDEN","championId":103}],
            "actions":[[{"actorCellId":5,"type":"pick","completed":false}]]});
        let room = Room::from_lcu(&v, 420, None, &[]);
        let battle = Battle::draft(&v, &room);
        assert_eq!(battle.players[0].puuid.as_deref(), Some("visible-own"));
        assert!(battle.players[1].puuid.is_none());
        assert_eq!(battle.players[1].champion, 0);
        assert!(identity(&json!("00000000-0000-0000-0000-000000000000")).is_none());
    }

    /// recent_summaries_exclude_custom_and_short_games protects displayed sample context.
    /// A private test loss and a remake do not lower the usable recent win
    /// rate, while the raw profile list still preserves both records.
    #[test]
    fn recent_summaries_exclude_custom_and_short_games() {
        let profile = Profile {
            matches: vec![
                MatchRecord {
                    queue_id: 420,
                    duration: 1200,
                    win: true,
                    kills: 5,
                    assists: 3,
                    deaths: 2,
                    champion_id: 99,
                    ..MatchRecord::default()
                },
                MatchRecord {
                    queue_id: 3100,
                    duration: 600,
                    ..MatchRecord::default()
                },
                MatchRecord {
                    queue_id: 420,
                    duration: 1800,
                    kills: 9,
                    deaths: 6,
                    assists: 13,
                    champion_id: 103,
                    ..MatchRecord::default()
                },
                MatchRecord {
                    queue_id: 420,
                    duration: 100,
                    ..MatchRecord::default()
                },
            ],
            ..Profile::default()
        };
        let summary = profile.summary();
        assert_eq!(summary.games, 2);
        assert_eq!(summary.wins, 1);
        assert_eq!(summary.kda, 3.75);
        assert_eq!(summary.average_kills, 7.);
        assert_eq!(summary.average_deaths, 4.);
        assert_eq!(summary.average_assists, 8.);
        assert_eq!(profile.recent().count(), 2);
        assert_eq!(profile.matches.len(), 4);
        assert_eq!(rank_label(&json!({})), "段位暂不可用");
    }
}
