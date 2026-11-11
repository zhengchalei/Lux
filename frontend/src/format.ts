import type { Match, Mode, Summary } from './types'

// format.ts supplies display-only conversions shared by Lux's Vue workspaces.
// It does not collect or alter match samples. Missing shares stay visibly
// unknown, while mode recognition follows the backend's published queue mapping.

/** modeNames maps source modes to their localized workspace display labels. */
export const modeNames: Record<Mode, string> = { ranked: '召唤师峡谷', aram: '极地大乱斗', mayhem: '海克斯大乱斗' }
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
/** modeFor recognizes separate ARAM variants without combining their source statistics. */
export function modeFor(queue: number): Mode { return [2400, 3270].includes(queue) ? 'mayhem' : [450, 3200, 3210, 3220, 3230].includes(queue) ? 'aram' : 'ranked' }
/** scopedSummary selects already prepared aggregates for exactly one display mode. */
export function scopedSummary(profile: { summary: Summary; summaries: Partial<Record<Mode | 'all', Summary>> }, mode: Mode | 'all'): Summary {
  return profile.summaries?.[mode] ?? profile.summary
}
/** matchLabel retains victory text independently from its visual outcome color. */
export function matchLabel(match: Match): string { return `${match.win ? '胜利' : '失败'} · ${match.name} · ${match.kills}/${match.deaths}/${match.assists} · ${date(match.created)}` }
