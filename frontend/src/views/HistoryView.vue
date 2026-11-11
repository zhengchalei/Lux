<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { action, championMap, state } from '../store'
import { decimal, modeFor, readTime, scopedSummary } from '../format'
import type { Match, Mode, SummonerCandidate } from '../types'
import EmptyState from '../components/EmptyState.vue'
import MatchDetails from '../components/MatchDetails.vue'
import ProfileAnalysis from '../components/ProfileAnalysis.vue'
import VirtualMatchList from '../components/VirtualMatchList.vue'
import AppIcon from '../components/AppIcon.vue'
import InfoHint from '../components/InfoHint.vue'
import SummonerIcon from '../components/SummonerIcon.vue'

// HistoryView.vue shares the real-data history journey between self analysis
// and explicit player lookup. A single mode filter scopes the prepared aggregate
// and visible match records. Champion and result filters narrow only the list,
// so their controls remain next to that list rather than relabeling a partial
// selection as the player's complete recent performance.
//
// Match selection uses its server identity rather than a filtered array index.
// A player transition clears temporary filters and chooses an available match.
// Source, coverage, and capture explanations live in focusable hints; partial
// retrieval retains a visible status badge and useful records. The optional
// sample overview stays collapsed until requested, leaving the main workspace
// for selecting matches and comparing the actual participants in each game.
const props = defineProps<{ query?: boolean }>()
// The selected identity and local filters govern this view; backend observations
// remain retained in the store and are never mutated by browsing or hovering.
const name = ref(props.query ? state.summonerSearch?.name ?? '' : '')
const queue = ref(0)
const filter = ref<Mode | 'all'>('all')
const selected = ref(0)
const hero = ref(0)
const outcome = ref<'all' | 'win' | 'loss'>('all')
const overviewOpen = ref(false)
const notice = ref('')
const confirming = ref(false)
const search = computed(() => props.query && state.summonerSearch?.name === name.value.trim() ? state.summonerSearch : null)
const profile = computed(() => props.query ? state.query : state.history)
const summary = computed(() => profile.value ? scopedSummary(profile.value, filter.value) : null)
const matches = computed(() => (profile.value?.matches ?? []).filter(match =>
  (filter.value === 'all' || modeFor(match.queueId) === filter.value) && (!hero.value || match.championId === hero.value)
  && (outcome.value === 'all' || match.win === (outcome.value === 'win'))))
const selectedMatch = computed(() => matches.value.find(match => match.id === selected.value) ?? matches.value[0] ?? null)
const identity = computed(() => {
  const name = profile.value?.name || '当前召唤师'
  const separator = name.lastIndexOf('#')
  return { name: separator > 0 ? name.slice(0, separator) : name, tag: separator > 0 ? name.slice(separator) : '' }
})
const coverageText = computed(() => {
  const value = profile.value
  if (!value) return ''
  return `${value.coverage.source || '来源待读取'} · 目标 ${value.requested} / 读取 ${value.coverage.fetched} / 扫描 ${value.coverage.scanned} / 当前范围有效 ${summary.value?.games || 0} 场。自定义、重开及无效比赛不计入竞技统计。${value.captured ? ` 最近读取 ${readTime(value.captured)}。` : ''}${value.coverage.note || value.historyError || ''}`
})
watch(() => profile.value?.puuid, () => { hero.value = 0; selected.value = 0; outcome.value = 'all'; overviewOpen.value = false })
// These labels select independent prepared summaries; champion names come from
// the same observed sample, keeping every dropdown choice backed by actual data.
const filters = [{ id: 'all', label: '全部模式' }, { id: 'ranked', label: '峡谷' }, { id: 'aram', label: '大乱斗' }, { id: 'mayhem', label: '海克斯' }] as const
/** lookup requests a player profile using the explicitly selected query queue.
 * Self lookup ignores the typed name. Missing explicit names produce a visible
 * validation message before any service request, while failures preserve previous records.
 */
async function lookup(self = false): Promise<void> {
  if (!self && !name.value.trim()) { notice.value = '请输入召唤师名称或 名称#编号'; return }
  notice.value = await action(() => self ? api.QuerySelf(queue.value) : api.Lookup(name.value.trim(), queue.value))
}
/** confirmCandidate loads history only after the user selects a returned full Riot ID.
 * The service validates the search generation again. Successful admission replaces
 * the input with that exact alias; stale selections leave a visible retry message.
 */
async function confirmCandidate(candidate: SummonerCandidate): Promise<void> {
  if (!search.value || confirming.value || !candidate.available || candidate.loading) return
  confirming.value = true
  try {
    notice.value = await action(() => api.SelectSummoner(search.value!.id, candidate.puuid, queue.value))
    if (!notice.value) name.value = candidate.name
  } finally { confirming.value = false }
}
/** selectMatch retains a stable match identity for the independent detail panel. */
function selectMatch(match: Match): void { selected.value = match.id }
</script>

<template>
  <div class="history-workspace">
    <div v-if="query" class="query-toolbar card"><div class="search-field"><span>⌕</span><input v-model="name" placeholder="召唤师名称 / 名称#编号" aria-label="查询召唤师" @keyup.enter="lookup()" /></div><select v-model.number="queue" aria-label="查询准确队列"><option :value="0">全部队列</option><option :value="420">单排 / 双排</option><option :value="440">灵活组排</option><option :value="450">普通大乱斗</option><option :value="2400">海克斯大乱斗</option></select><button class="button primary" :disabled="!name.trim() || confirming" :aria-busy="state.queryLoading" @click="lookup()">{{ state.queryLoading ? '查询中…' : '查询战绩' }}</button><button class="button" :disabled="confirming" @click="lookup(true)">查询自己</button></div>
    <p v-if="notice || (query && state.queryError)" class="inline-note error">{{ notice || state.queryError }}</p>
    <section v-if="search" class="summoner-results card" aria-label="同名召唤师候选">
      <div class="summoner-results-heading"><div><h2>选择你要查询的召唤师</h2><span>“{{ search.name }}” · {{ search.candidates.length }} 个同名账号</span></div><span v-if="state.queryLoading" class="summoner-reading" role="status">资料读取中…</span><InfoHint label="同名查询说明" text="展示客户端返回的同名账号，结果范围由客户端决定。头像、等级和单双排段位来自当前大区；资料不可用的账号可输入完整 Riot ID 重试。选择后才会读取该账号战绩。" /></div>
      <div v-if="search.candidates.length" class="summoner-candidates scroll-region">
        <button v-for="candidate in search.candidates" :key="candidate.puuid" class="summoner-candidate" :disabled="candidate.loading || !candidate.available || confirming" :aria-label="`查询 ${candidate.name}`" :title="candidate.reason || `查看 ${candidate.name} 的战绩`" @click="confirmCandidate(candidate)">
          <div class="summoner-avatar"><SummonerIcon :icon="candidate.iconId" :ready="candidate.available" /><span class="summoner-level">{{ candidate.loading ? '…' : candidate.level > 0 ? `Lv.${candidate.level}` : '等级未知' }}</span></div>
          <div class="summoner-candidate-info"><strong>{{ candidate.name.slice(0, candidate.name.lastIndexOf('#')) }}<small>{{ candidate.name.slice(candidate.name.lastIndexOf('#')) }}</small></strong><span :class="{ pending: candidate.loading }">{{ candidate.loading ? '正在读取等级与段位…' : candidate.rank }}</span></div>
          <AppIcon v-if="candidate.available && !candidate.loading" name="chevron" :size="16" class="summoner-select-arrow" />
        </button>
      </div>
      <div v-else class="summoner-no-results">未找到同名账号，可检查名称或输入完整名称#编号。</div>
    </section>
    <template v-if="profile && summary">
      <section class="history-overview card">
        <span class="profile-emblem"><AppIcon name="user" :size="24" /></span><div class="history-identity"><h2 :title="profile.name">{{ identity.name }}<small>{{ identity.tag }}</small></h2><span class="history-rank">{{ profile.rank || '段位待读取' }}</span><span v-if="profile.historyError || profile.coverage.note" class="partial-status">部分数据</span><span v-if="state.queryLoading && query" class="partial-status">更新中</span></div>
        <div class="history-summary-stat"><strong :class="summary.games ? (summary.winRate >= 50 ? 'text-good' : 'text-bad') : ''">{{ summary.games ? `${decimal(summary.winRate, 0)}%` : '—' }}</strong><span>近期胜率 <small>{{ summary.wins }} 胜 {{ summary.games - summary.wins }} 负</small></span></div>
        <div class="history-summary-stat summary-combat" :title="`KDA ${decimal(summary.kda, 2)}；按当前模式的 ${summary.games} 场有效比赛统计，英雄和胜负筛选只影响下方列表。`"><strong><template v-if="summary.games">{{ decimal(summary.kills) }}<i>/</i><em>{{ decimal(summary.deaths) }}</em><i>/</i>{{ decimal(summary.assists) }}</template><template v-else>— / — / —</template></strong><span>场均 K / D / A <small>{{ summary.games }} 场</small></span></div>
        <div class="history-overview-actions"><InfoHint label="统计口径与数据来源" :text="coverageText" /><button class="button overview-toggle" :aria-expanded="overviewOpen" aria-controls="sample-overview" @click="overviewOpen = !overviewOpen">近期概况<AppIcon name="chevron" :size="14" /></button></div>
      </section>
      <div v-if="overviewOpen" id="sample-overview" class="history-sample-overview scroll-region"><ProfileAnalysis :profile="profile" :summary="summary" :hero="hero" @hero="hero = $event" /></div>
      <div class="history-layout card"><div class="history-controlbar"><div class="tabs history-mode-tabs" role="group" aria-label="战绩模式"><button v-for="entry in filters" :key="entry.id" :aria-pressed="filter === entry.id" :class="{ active: filter === entry.id }" @click="filter = entry.id; hero = 0">{{ entry.label }}</button></div><div class="history-record-filters"><select v-model.number="hero" aria-label="按英雄筛选"><option :value="0">全部英雄</option><option v-for="entry in summary.heroes" :key="entry.championId" :value="entry.championId">{{ championMap.get(entry.championId)?.name || `英雄 ${entry.championId}` }} · {{ entry.games }} 场</option></select><select v-model="outcome" aria-label="按胜负筛选"><option value="all">全部结果</option><option value="win">仅看胜利</option><option value="loss">仅看失败</option></select></div></div><div class="history-browser"><aside class="history-rail"><div class="history-list-heading"><span>近期对局 <b>{{ matches.length }}</b></span><button v-if="hero || outcome !== 'all'" class="text-button" @click="hero = 0; outcome = 'all'">清除筛选</button><InfoHint label="战绩列表说明" text="从新到旧排列。方向键切换比赛，Home / End 跳转首尾。英雄和胜负筛选只影响列表；顶部统计跟随模式。" /></div><VirtualMatchList :matches="matches" :selected="selectedMatch?.id || 0" @select="selectMatch" /></aside><div class="history-content scroll-region"><MatchDetails :match="selectedMatch" :puuid="profile.puuid" /></div></div></div>
    </template>
    <EmptyState v-else-if="!search" :title="query ? (state.queryLoading ? '正在查询召唤师' : '了解近期表现，从一位召唤师开始') : '等待你的真实战绩'" :description="query ? '输入名称选择同名账号，或用完整名称#编号直接查询。' : state.connected ? '客户端已连接，正在读取当前账号的近期比赛。' : state.status" icon="◫" />
  </div>
</template>
