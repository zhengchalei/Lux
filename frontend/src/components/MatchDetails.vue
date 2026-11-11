<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import type { Match, Player } from '../types'
import { compact, date, duration, percent } from '../format'
import Portrait from './Portrait.vue'
import EmptyState from './EmptyState.vue'
import InfoHint from './InfoHint.vue'
import ReplayControl from './ReplayControl.vue'
import TimelineReview from './TimelineReview.vue'

// MatchDetails.vue displays one selected match's observed combat comparison.
// Radar axes normalize raw values against the same match's maximum, while team
// share uses only the selected player's friendly denominator. These quantities
// remain independent: a highest-damage player reaches the outer radar ring even
// when their friendly-team share is thirty-two percent.
//
// Missing roster metrics stay unknown rather than entering the radar as zeros.
// Detail requests retain both account and match identity through an asynchronous
// generation check, preventing a late reply from replacing a newer selection.
// Hints reveal local explanations without triggering another service request.
// The overview orders the viewed player's team first and compares raw damage or
// gold within this same match; unavailable measurements retain unknown values.
// Timeline review loads only when its tab is opened and owns its independent
// evidence and provider-request cancellation when match selection changes.
const props = defineProps<{
  match: Match | null // Selected recorded match perspective.
  puuid: string // Owner of that match perspective.
}>()
// Selection and observed detail state stay local to this panel. Overview table
// comparisons and contribution axes use separate metric choices without rereading data.
const tab = ref<'overview' | 'contribution' | 'timeline'>('overview')
const metric = ref(0)
const detail = ref<Match | null>(null)
const busy = ref(false)
const error = ref('')
const scoreMetric = ref<'damage' | 'gold'>('damage')
let generation = 0
// Metrics share backend missing-value positions and the comparison table's ordering.
const metrics = [
  { key: 'damage', label: '英雄伤害', short: '伤害' },
  { key: 'gold', label: '获得经济', short: '经济' },
  { key: 'taken', label: '承受伤害', short: '承伤' },
  { key: 'heal', label: '治疗量', short: '治疗' },
  { key: 'towerDamage', label: '防御塔伤害', short: '推塔' },
] as const
const current = computed(() => detail.value?.id === props.match?.id ? detail.value : props.match)
const players = computed(() => current.value?.players ?? [])
const own = computed(() => players.value.find(player => player.self))
const full = computed(() => players.value.length >= 10)
const teams = computed(() => current.value ? [current.value.team, current.value.team === 100 ? 200 : 100] : [])
const scoreKnown = computed(() => full.value && !current.value?.missingMetrics?.[scoreMetric.value === 'damage' ? 0 : 1])
const scoreMaximum = computed(() => Math.max(...players.value.map(player => player[scoreMetric.value]), 1))
const damageShare = computed(() => {
  if (!full.value || !own.value || current.value?.missingMetrics?.[0]) return null
  const total = players.value.filter(player => player.team === own.value!.team).reduce((sum, player) => sum + player.damage, 0)
  return total > 0 ? own.value.damage / total : null
})
const sourceText = computed(() => `${current.value?.source || '来源待读取'} · 比赛 ${current.value?.id || '—'}。数据保留当前查询玩家的视角；双方表格中带标记的是当前查看的玩家。`)
const radar = computed(() => metrics.map((entry, index) => {
  const values = players.value.map(player => player[entry.key])
  const max = Math.max(...values, 0)
  const unavailable = !full.value || !!current.value?.missingMetrics?.[index] || !max
  const ownValue = own.value?.[entry.key]
  return {
    label: entry.short,
    own: unavailable || ownValue == null ? null : ownValue / max,
    mean: unavailable ? null : values.reduce((sum, value) => sum + value, 0) / values.length / max,
  }
}))
const sorted = computed(() => [...players.value].sort((a, b) => b[metrics[metric.value]!.key] - a[metrics[metric.value]!.key]))
const maximum = computed(() => Math.max(...players.value.map(player => player[metrics[metric.value]!.key]), 1))
const share = computed(() => {
  if (!full.value || !own.value || current.value?.missingMetrics?.[metric.value]) return null
  const key = metrics[metric.value]!.key
  const total = players.value.filter(player => player.team === own.value!.team).reduce((sum, player) => sum + player[key], 0)
  return total ? own.value[key] / total : null
})
const ranking = computed(() => {
  if (!full.value || !own.value || current.value?.missingMetrics?.[metric.value]) return null
  const key = metrics[metric.value]!.key
  return 1 + players.value.filter(player => player[key] > own.value![key]).length
})
watch([() => props.puuid, () => props.match?.id], async ([puuid, id]) => {
  const request = ++generation
  detail.value = null; error.value = ''; busy.value = false
  if (!id || !puuid || (props.match?.players?.length ?? 0) >= 10) return
  busy.value = true
  try {
    const result = await api.GetMatchDetail(puuid, id)
    if (request === generation && result.id === id) detail.value = result
  } catch (reason) { if (request === generation) error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if (request === generation) busy.value = false }
}, { immediate: true })

/** radarPoint converts a normalized radial observation into its SVG coordinates. */
function radarPoint(index: number, amount: number, radius = 84): string {
  const angle = index / metrics.length * Math.PI * 2 - Math.PI / 2
  return `${130 + Math.cos(angle) * amount * radius},${112 + Math.sin(angle) * amount * radius}`
}
/** polygon connects only fully observed axes in the same normalized comparison. */
function polygon(field: 'own' | 'mean'): string { return radar.value.map((value, index) => radarPoint(index, value[field] ?? 0)).join(' ') }
/** metricValue returns one participant's selected raw combat measurement. */
function metricValue(player: Player): number { return player[metrics[metric.value]!.key] }
</script>

<template>
  <section v-if="current" class="match-detail">
    <div class="detail-heading">
      <Portrait :id="current.championId" :size="48" />
      <div class="detail-identity"><h2>{{ current.name }} <span class="detail-outcome" :class="current.win ? 'is-win' : 'is-loss'">{{ current.win ? '胜利' : '失败' }}</span></h2><div class="detail-meta">{{ current.queue }} <i>·</i> {{ date(current.created) }} <i>·</i> {{ duration(current.duration) }}</div></div>
      <div class="detail-kda"><strong>{{ current.kills }} <i>/</i> <span>{{ current.deaths }}</span> <i>/</i> {{ current.assists }}</strong><small>K / D / A</small></div>
    </div>
    <div class="tabs detail-tabs"><button :aria-pressed="tab === 'overview'" :class="{ active: tab === 'overview' }" @click="tab = 'overview'">对局概览</button><button :aria-pressed="tab === 'contribution'" :class="{ active: tab === 'contribution' }" @click="tab = 'contribution'">贡献分析</button><button :aria-pressed="tab === 'timeline'" :class="{ active: tab === 'timeline' }" @click="tab = 'timeline'">时间线复盘</button><span class="push-right" style="display:flex;align-items:center;gap:8px"><ReplayControl :puuid="puuid" :game-id="current.id" /><InfoHint label="本局数据来源" :text="sourceText" /></span></div>
    <div v-if="busy" class="inline-note">正在补齐本局十人数据…</div><div v-if="error" class="inline-note error">{{ error }}</div>
    <div v-if="tab === 'overview'" class="detail-overview">
      <div class="match-stat-grid"><div><small>英雄伤害</small><strong>{{ current.missingMetrics?.[0] ? '—' : compact(current.damage) }}</strong></div><div><small>伤害占比<InfoHint label="伤害占比说明" text="本局英雄伤害 ÷ 己方五人英雄伤害总和。只有完整阵容和有效分母才计算，与雷达按全场最高值归一化的口径不同。" /></small><strong>{{ percent(damageShare) }}</strong></div><div><small>获得经济</small><strong>{{ current.missingMetrics?.[1] ? '—' : compact(current.gold) }}</strong></div><div :title="`每分钟补刀 ${(current.cs / Math.max(current.duration / 60, 1)).toFixed(1)}`"><small>补刀</small><strong>{{ current.cs }}<small>{{ (current.cs / Math.max(current.duration / 60, 1)).toFixed(1) }} / 分</small></strong></div></div>
      <div v-if="full" class="combat-table">
        <div class="scoreboard-toolbar"><h3>双方数据</h3><div class="scoreboard-metrics" role="group" aria-label="双方比较指标"><button :aria-pressed="scoreMetric === 'damage'" @click="scoreMetric = 'damage'">伤害</button><button :aria-pressed="scoreMetric === 'gold'" @click="scoreMetric = 'gold'">经济</button></div></div>
        <section v-for="team in teams" :key="team" class="combat-team" :class="team === current.team ? 'is-friendly' : 'is-enemy'"><div class="score-team-heading"><h3><i />{{ team === current.team ? '己方' : '敌方' }}</h3><span>{{ players.filter(player => player.team === team).reduce((total, player) => total + player.kills, 0) }} 击杀</span></div><div class="score-columns"><span>英雄</span><span>K / D / A</span><span>{{ scoreMetric === 'damage' ? '英雄伤害' : '获得经济' }}</span></div><div v-for="player in players.filter(player => player.team === team)" :key="`${team}-${player.championId}`" class="combat-row" :class="{ own: player.self }" :aria-label="`${player.name}${player.self ? '，当前查看玩家' : ''}`"><span class="score-hero"><Portrait :id="player.championId" :size="30" /><strong :title="player.name">{{ player.name }}</strong><i v-if="player.self" class="score-perspective" title="当前查看玩家" /></span><span class="kda">{{ player.kills }}<i>/</i><span>{{ player.deaths }}</span><i>/</i>{{ player.assists }}</span><span class="score-value" :title="scoreKnown ? `${scoreMetric === 'damage' ? '英雄伤害' : '获得经济'} ${player[scoreMetric].toLocaleString('zh-CN')}` : '来源未提供完整指标'"><b>{{ scoreKnown ? compact(player[scoreMetric]) : '—' }}</b><span class="score-track"><i v-if="scoreKnown" :style="{ width: `${player[scoreMetric] / scoreMaximum * 100}%` }" /></span></span></div></section>
      </div>
      <EmptyState v-else-if="!busy" title="完整阵容暂不可用" description="当前摘要仍可查看；十人贡献需要本局完整数据。" />
    </div>
    <div v-else-if="tab === 'contribution'" class="contribution-panel">
      <div class="contribution-heading"><h3>本场贡献</h3><InfoHint label="雷达与排名说明" text="雷达各轴以本场该项原始最高值为外圈，灰线为全场人均。队内占比和全场排名分别计算；全场最高者位于外圈，即使队内占比只有 32%。缺失指标不会按 0 计入雷达。" /><span class="radar-legend"><i />当前玩家 <i class="mean" />全场人均</span></div>
      <div class="contribution-layout"><div v-if="full && own" class="radar-and-share">
        <svg class="radar" viewBox="0 0 260 225" role="img" aria-label="本场五项原始数据对比雷达，外圈为全场最高">
          <polygon v-for="ring in [0.25, 0.5, 0.75, 1]" :key="ring" :points="metrics.map((_, index) => radarPoint(index, ring)).join(' ')" class="radar-grid" />
          <line v-for="(_, index) in metrics" :key="index" x1="130" y1="112" :x2="radarPoint(index, 1).split(',')[0]" :y2="radarPoint(index, 1).split(',')[1]" class="radar-grid" />
          <polygon v-if="radar.every(axis => axis.mean != null)" :points="polygon('mean')" class="radar-mean" />
          <polygon v-if="radar.every(axis => axis.own != null)" :points="polygon('own')" class="radar-own" />
          <text v-for="(entry, index) in metrics" :key="entry.key" :x="radarPoint(index, 1.25).split(',')[0]" :y="Number(radarPoint(index, 1.15).split(',')[1]) + 4" text-anchor="middle">{{ entry.short }}{{ radar[index]?.own == null ? ' · 未知' : '' }}</text>
        </svg>
        <div class="share-summary"><small>{{ metrics[metric]!.label }} · 己方占比</small><strong>{{ percent(share) }}</strong><span>全场排名 <b>{{ ranking ? `${ranking} / ${players.length}` : '—' }}</b></span></div>
      </div>
      <EmptyState v-else title="等待完整贡献数据" description="缺少完整阵容时不计算队内份额或全场排名。" />
      <div class="contribution-ranking"><div class="tabs metric-tabs"><button v-for="(entry, index) in metrics" :key="entry.key" :aria-pressed="metric === index" :class="{ active: metric === index }" @click="metric = index">{{ entry.short }}</button></div>
      <div v-if="full && !current.missingMetrics?.[metric]" class="metric-ranking"><div v-for="player in sorted" :key="`${player.team}-${player.championId}`" class="metric-row" :class="{ own: player.self }"><Portrait :id="player.championId" :size="24" /><span>{{ player.name }}</span><div class="bar-track"><i :style="{ width: `${metricValue(player) / maximum * 100}%` }" :class="player.team === current.team ? 'friendly' : 'enemy'" /></div><strong>{{ compact(metricValue(player)) }}</strong></div></div>
      <p v-else-if="full" class="muted">来源未提供完整的{{ metrics[metric]!.label }}，此项保持未知。</p>
      </div></div>
    </div>
    <TimelineReview v-else :puuid="puuid" :game-id="current.id" />
  </section>
  <EmptyState v-else title="选择一局，开始复盘" description="从左侧战绩中选择一局，查看完整阵容和贡献。" />
</template>
