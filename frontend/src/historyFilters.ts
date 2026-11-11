import type { Match } from './types'

// historyFilters.ts defines local history browsing categories and intersection rules.
// Queue classification mirrors domain.HistoryKind, while hero and outcome filters
// narrow only displayed rows. Aggregate statistics remain prepared by Go, keeping
// list interactions independent from collection, transport, and match-score calculations.

/** historyKinds defines the complete match-type vocabulary shown in the filter panel. */
export const historyKinds = [
  { id: 'all', label: '全部比赛' }, { id: 'solo', label: '单双排' },
  { id: 'flex', label: '灵活组排' }, { id: 'normal', label: '匹配赛' },
  { id: 'aram', label: '大乱斗' }, { id: 'mayhem', label: '海克斯大乱斗' },
  { id: 'other', label: '其他模式' },
] as const
/** HistoryKind identifies one explicit browsing scope without implying competitive eligibility. */
export type HistoryKind = typeof historyKinds[number]['id']
/** HistoryFilter retains the current intersections for the local match list. */
export interface HistoryFilter {
  kind: HistoryKind // Selected match-type scope.
  champion: number // Champion identifier, zero includes all.
  outcome: 'all' | 'win' | 'loss' // Independent observed result scope.
}
/** historyKind maps an observed queue to its history browsing category.
 * Unknown queues remain under other modes. This classification preserves raw
 * custom rows and does not decide whether aggregate statistics include them.
 */
export function historyKind(queue: number): HistoryKind {
  if (queue === 420) return 'solo'
  if (queue === 440) return 'flex'
  if ([400, 430, 480, 490].includes(queue)) return 'normal'
  if ([450, 3200, 3210, 3220, 3230].includes(queue)) return 'aram'
  if ([2400, 3270].includes(queue)) return 'mayhem'
  return 'other'
}
/** matchesHistoryFilter intersects independently selected type, champion, and observed result.
 * A zero champion and all scope each leave their dimension unrestricted. This
 * predicate selects raw rows, including inspectable custom games and remakes.
 */
export function matchesHistoryFilter(match: Match, filter: HistoryFilter): boolean {
  return (filter.kind === 'all' || historyKind(match.queueId) === filter.kind)
    && (!filter.champion || match.championId === filter.champion)
    && (filter.outcome === 'all' || match.win === (filter.outcome === 'win'))
}
