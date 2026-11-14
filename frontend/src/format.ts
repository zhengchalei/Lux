import type { Match, Mode, Summary } from './types'

// format.ts supplies display-only conversions shared by Lux's Vue workspaces.
// It does not collect or alter match samples. Missing shares stay visibly
// unknown, while mode recognition follows the backend's published queue mapping.
// Battle cards and their inspectors consume prepared summaries rather than
// recalculating independent samples in the render path.
//
// An explicit mode selects only its own aggregate because ordinary ARAM and
// Mayhem have different rules and source observations. The all-mode aggregate
// remains available through an explicit all selection. Missing mode observations
// produce empty samples and unknown contribution shares, while unknown queues
// stay outside the ranked filter. These rules keep recent result strips aligned
// with the rates and hero usage shown beside them.

/** modeNames maps source modes to their localized workspace display labels. */
export const modeNames: Record<Mode, string> = { ranked: '召唤师峡谷', aram: '普通大乱斗', mayhem: '海克斯大乱斗' }
/** decimal rounds one observed value without inventing an unavailable measurement. */
export function decimal(value: number | null | undefined, digits = 1): string { return value == null || !Number.isFinite(value) ? '—' : value.toFixed(digits) }
/** percent displays a known fractional share as a percentage. */
export function percent(value: number | null | undefined): string { return value == null ? '—' : `${decimal(value * 100)}%` }
/** compact formats large recorded amounts for narrow comparison table columns. */
export function compact(value: number): string { return value >= 10000 ? `${decimal(value / 10000)}万` : value >= 1000 ? `${decimal(value / 1000)}k` : String(value) }
/** duration displays a recorded duration in whole minutes and seconds. */
export function duration(value: number): string { return `${Math.floor(value / 60)}:${String(value % 60).padStart(2, '0')}` }
/** date formats a known millisecond timestamp in the user's local timezone. */
export function date(value: number): string { return value ? new Date(value).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' }) : '日期未知' }
/** readTime formats a service capture timestamp for the user's local clock. */
export function readTime(value: string): string { const instant = new Date(value); return Number.isNaN(instant.getTime()) ? value : instant.toLocaleTimeString('zh-CN', { hour12: false }) }
/** modeFor recognizes queue families without combining their source statistics.
 * Unrecognized queues return other so the inspector cannot silently include an
 * unsupported game type in its Summoner's Rift filter.
 */
export function modeFor(queue: number): Mode | 'other' { return [2400, 3270].includes(queue) ? 'mayhem' : [450, 3200, 3210, 3220, 3230].includes(queue) ? 'aram' : [400, 420, 430, 440, 480, 490, 700, 720].includes(queue) ? 'ranked' : 'other' }
/** scopedSummary selects only aggregates prepared for the requested display mode.
 * A missing mode cannot inherit the all-mode summary because its victories,
 * heroes, and contribution shares may belong to a different ruleset. Fresh empty
 * containers also prevent a consumer from sharing mutable missing-mode results.
 * Callers use the returned game count to show unavailable measurements instead
 * of displaying zero percent as a measured outcome. Explicit all-mode selection
 * keeps its existing compatibility fallback to the profile's all-mode summary.
 */
export function scopedSummary(profile: { summary: Summary; summaries: Partial<Record<Mode | 'all', Summary>> }, mode: Mode | 'all'): Summary {
  if (mode === 'all') return profile.summaries?.all ?? profile.summary
  return profile.summaries?.[mode] ?? {
    games: 0, wins: 0, winRate: 0, kda: 0, kills: 0, deaths: 0, assists: 0,
    recent: [], heroes: [],
    contribution: { games: 0, damageGames: 0, takenGames: 0, goldGames: 0, damageShare: null, takenShare: null, goldShare: null },
  }
}
/** matchLabel retains victory text independently from its visual outcome color. */
export function matchLabel(match: Match): string { return `${match.win ? '胜利' : '失败'} · ${match.players.find(player => player.self)?.name || '召唤师'} · ${match.kills}/${match.deaths}/${match.assists} · ${date(match.created)}` }
