<script setup lang="ts">
import { computed } from 'vue'
import type { Profile, Summary } from '../types'
import { duration } from '../format'
import { historyKind, type HistoryKind } from '../historyFilters'
import EvidenceBadge from './EvidenceBadge.vue'

// PlayerSignals.vue presents backend-prepared performance and habit observations
// beside a player's current statistics. Independent score and timeline sample
// counts explain coverage through local popovers. No tag evaluates account
// ownership, and revealing a tag never requests a history or model response.
//
// History category selection filters evidence independently of hero and outcome
// controls, matching the summary's backend scope. Gank labels require five known
// timelines and are suppressed for a current jungle assignment, while unavailable
// records remain outside the denominator. Key habits and complete metric scoring
// retain separate sample counts so a missing timeline does not erase them.
const props = defineProps<{
  profile: Profile // Retained recorded perspective source.
  summary: Summary // Same-scope prepared behavioral observations.
  position?: string // Current explicitly assigned gameplay position.
  rift?: boolean // Current view admits Rift gank labels.
  kind?: HistoryKind // History category shared with prepared summary.
}>()
// Signal projections follow stable profile/summary observations and remain cached
// while pointer movement only reveals the corresponding existing evidence panel.
const signals = computed(() => props.summary.signals)
const score = computed(() => signals.value?.score)
const scoreNote = computed(() => {
  const s=score.value
  if (!s) return ''
  // Akari's eight component weights sum to fifteen.
  const names=['KDA','胜率','伤害','承伤','补刀','经济','参团','视野'], caps=[1,1,3,2,2,2,2,2]
  return `${s.games} 场完整指标样本；${props.summary.games} 场有效记录。\n${names.map((name,i)=>`${name} ${s.parts[i]!.toFixed(2)}/${caps[i]}`).join(' · ')}\n合计 ${s.total.toFixed(2)}/15。按 AKL 规则：≥6.5 分且至少 5 场为优异；≥8 分且至少 8 场为通天代。表示近期表现，未验证账号操作者或代练身份。`
})
const gankLabel = computed(() => {
  const s=signals.value
  if (!s?.gankGames) return '抓人无样本'
  if (s.gankGames<5) return '抓人样本少'
  const n=s.gankDeaths/s.gankGames
  return n>2 ? '非常好抓' : n>=1.5 ? '好抓' : n>=1 ? '可抓' : '难抓'
})
// Display floors event milliseconds to whole game-clock seconds.
const gankRows = computed(() => props.profile.matches.filter(match => !!match.earlyGank && (!props.kind || props.kind === 'all' || historyKind(match.queueId) === props.kind)).map(match => ({ owner: props.profile.puuid, match, win: match.win, label: `${match.earlyGank!.deaths.length} 次${match.earlyGank!.deaths.length ? ` · ${match.earlyGank!.deaths.map(at => duration(Math.floor(at/1000))).join(' / ')}` : ' · 未记录到'}` })))
const gankNote = computed(() => {
  const s=signals.value
  return `${s?.gankGames || 0} 场可用时间线，前 15 分钟被敌方打野击杀或参与助攻击杀共 ${s?.gankDeaths || 0} 次${s?.gankGames ? `，场均 ${(s.gankDeaths/s.gankGames).toFixed(2)} 次` : ''}。只统计可识别双方位置的峡谷匹配；本人为打野的场次不参与。\n自动分析最多 10 场；缺失时间线不按零计算，少于 5 场不给抓人分档。该指标是历史死亡次数，不能计算所有抓人尝试的成功率。`
})
const flashNote = computed(() => {
  const s=signals.value!, total=s.flashD+s.flashF
  return `闪现 D：${s.flashD} 场（${(s.flashD/total*100).toFixed(1)}%）\n闪现 F：${s.flashF} 场（${(s.flashF/total*100).toFixed(1)}%）\n同一统计范围内，两种位置均出现过。只说明按键习惯有变化，不确认换人或代练。`
})
</script>

<template><div v-if="signals && summary.games" class="player-signals" aria-label="玩家分析标签"><EvidenceBadge v-if="score" :label="score.label || `评分 ${score.total.toFixed(1)}`" heading="近期表现评分" :rows="[]" :caption="`${score.games} 场 · AKL 规则`" :note="scoreNote" /><EvidenceBadge v-if="rift && position?.toUpperCase() !== 'JUNGLE'" :label="gankLabel" heading="前期被抓记录" :rows="gankRows" :caption="`${signals.gankGames} 场时间线`" :note="gankNote" /><EvidenceBadge v-if="signals.flashD && signals.flashF" label="闪现异位" heading="闪现位置分布" :rows="[]" :caption="`${signals.flashD+signals.flashF} 场携带闪现`" :note="flashNote" /></div></template>

<style scoped>
.player-signals { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; min-width: 0; }
.player-signals :deep(.evidence-badge) { font-size: 10px; padding: 1px 4px; }
</style>
