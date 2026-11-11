import type { Match, Profile } from './types'

// relationships.ts derives encounters and suspected parties from recorded public
// identities. Every evidence row retains its source profile and match, allowing
// the renderer to open the exact observed perspective. Matches shared by several
// player histories count once per server and game, with the most identified
// roster retained rather than joining incompatible participant observations.
//
// Party inference enumerates subsets within each current team and counts only
// games where every member actually shared a historical team. An A/B pair and
// B/C pair therefore cannot manufacture a three-person party. Current, future,
// expired, aborted, custom, and noncompetitive observations do not contribute.
// These are evidence-based suggestions, not client-confirmed party membership.

/** EvidenceRow binds a compact historical result to its authorized source perspective. */
export interface EvidenceRow {
  owner: string // Retrieved source account identifier.
  match: Match // Immutable observed match perspective.
  label: string // Historical relationship display label.
  win: boolean // Result from the relationship's team.
}
/** RelationshipPlayer identifies one publicly known member of the current team. */
export interface RelationshipPlayer {
  puuid: string // Recorded public account identifier.
  name: string // Recorded summoner display name.
  team: number // Current team identifier.
}
/** PremadeGroup retains jointly observed members and their exact shared evidence. */
export interface PremadeGroup {
  members: RelationshipPlayer[] // Members of one current team.
  rows: EvidenceRow[] // Distinct historical same-team matches.
}

// Relationship windows cover thirty days before the inspected match or present
// live roster. Three distinct qualifying games are required for a party suggestion.
export const relationshipWindow = 30 * 24 * 60 * 60 * 1000
export const premadeThreshold = 3

/** evidenceKey separates equal game numbers published by different game servers. */
export function evidenceKey(match: Match, platform = ''): string { return `${(match.platformId || platform).toUpperCase()}:${match.id}` }

/** collectEvidence deduplicates observed matches without merging conflicting rosters.
 * Profiles are ordered by preference, with the local account first. A more
 * identified roster can replace a weaker observation, but its own perspective
 * remains attached so details never borrow another account's statistics.
 * Equal coverage retains the earlier preferred source. Returned entries reference
 * immutable profile records, so callers filter or annotate wrappers without
 * mutating the service's shared player histories or copying every combat roster.
 */
export function collectEvidence(profiles: Pick<Profile, 'puuid' | 'matches'>[], platform = ''): EvidenceRow[] {
  const games = new Map<string, EvidenceRow>()
  for (const profile of profiles) {
    if (!profile.puuid) continue
    for (const match of profile.matches || []) {
      const key = evidenceKey(match, platform)
      const previous = games.get(key)
      if (!previous || match.players.filter(player => !!player.puuid).length > previous.match.players.filter(player => !!player.puuid).length) {
        games.set(key, { owner: profile.puuid, match, label: '历史同队', win: match.win })
      }
    }
  }
  return [...games.values()].sort((a, b) => b.match.created - a.match.created)
}

/** recentEvidence selects earlier competitive observations within the relationship window.
 * The current numeric game is excluded across perspectives. A historical review
 * supplies its creation instant; live analysis supplies the current clock once
 * per observation, preventing future games from influencing an earlier review.
 */
export function recentEvidence(rows: EvidenceRow[], exclude: number, before: number): EvidenceRow[] {
  const queues = [400, 420, 430, 440, 450, 480, 490, 700, 720, 2400]
  return rows.filter(({ match }) => match.id !== exclude && match.created > 0 && match.created < before && match.created >= before - relationshipWindow && match.duration >= 180 && !match.remake && (!match.gameType || match.gameType === 'MATCHED_GAME') && queues.includes(match.queueId))
}

/** encounterIndex indexes each local-account match under the other public participants.
 * Only the requested account's own perspective qualifies. Unknown identities and
 * repeated participant identifiers do not create links or duplicate encounters.
 */
export function encounterIndex(profile: Pick<Profile, 'puuid' | 'matches'> | null, local: string): Map<string, EvidenceRow[]> {
  const index = new Map<string, EvidenceRow[]>()
  if (!profile || !local || profile.puuid !== local) return index
  for (const row of collectEvidence([profile])) {
    const own = row.match.players.find(player => player.puuid === local)
    if (!own || ![100, 200].includes(own.team)) continue
    const seen = new Set<string>()
    for (const player of row.match.players) {
      if (!player.puuid || player.puuid === local || seen.has(player.puuid) || ![100, 200].includes(player.team)) continue
      seen.add(player.puuid)
      const rows = index.get(player.puuid) || []
      rows.push({ ...row, label: player.team === own.team ? '曾是队友' : '曾是对手' })
      index.set(player.puuid, rows)
    }
  }
  return index
}

/** inferPremades counts actual joint historical teams among the current public roster.
 * Each current side contains at most five unique accounts. Every qualifying
 * historical team contributes once to each observed subset, and repeated games
 * are deduplicated before admission. Maximal supported subsets remain visible;
 * overlapping pairs stay separate unless their full union has joint evidence.
 *
 * Missing source identities never stand in for current players, and a game
 * whose participants disagree about one account's team cannot use that account.
 * Results retain exact evidence rows and team-relative wins for inspection,
 * without asserting that historical co-occurrence proves a premade party.
 */
export function inferPremades(rows: EvidenceRow[], players: RelationshipPlayer[], exclude: number, before: number): PremadeGroup[] {
  const candidates = new Map<string, PremadeGroup>()
  const history = [...new Map(recentEvidence(rows, exclude, before).map(row => [evidenceKey(row.match), row])).values()]
  for (const team of [100, 200]) {
    const members = [...new Map(players.filter(player => player.team === team && player.puuid).map(player => [player.puuid, player])).values()].slice(0, 5)
    for (const row of history) {
      const historical = new Map<string, number>()
      const conflicts = new Set<string>()
      for (const player of row.match.players) {
        if (historical.has(player.puuid) && historical.get(player.puuid) !== player.team) conflicts.add(player.puuid)
        historical.set(player.puuid, player.team)
      }
      for (const side of [100, 200]) {
        const together = members.filter(player => historical.get(player.puuid) === side && !conflicts.has(player.puuid))
        // Five current slots bound enumeration to thirty-one subsets per historical
        // team; membership never expands through an indirect chain of shared partners.
        for (let mask = 1; mask < 1 << together.length; mask++) {
          const subset = together.filter((_, bit) => mask & 1 << bit)
          if (subset.length < 2) continue
          const key = subset.map(player => player.puuid).sort().join('|')
          const group = candidates.get(key) || { members: subset, rows: [] }
          group.rows.push({ ...row, label: `${subset.length} 人历史同队`, win: side === row.match.team ? row.match.win : !row.match.win })
          candidates.set(key, group)
        }
      }
    }
  }
  const supported = [...candidates.values()].filter(group => group.rows.length >= premadeThreshold)
  return supported.filter(group => !supported.some(other => other.members.length > group.members.length && group.members.every(player => other.members.some(member => member.puuid === player.puuid))))
    .sort((a, b) => b.members.length - a.members.length || b.rows.length - a.rows.length || a.members[0]!.puuid.localeCompare(b.members[0]!.puuid))
}
