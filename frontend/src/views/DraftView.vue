<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { action, championMap, state } from '../store'
import { decimal, modeNames } from '../format'
import type { Matchup } from '../types'
import EmptyState from '../components/EmptyState.vue'
import Portrait from '../components/Portrait.vue'
import QuickDraft from '../components/QuickDraft.vue'

// DraftView.vue shows only selections disclosed by the current client session.
// Ordinary and Mayhem statistics use separate source fields, and candidate lists
// intersect the session's actual available pool. An unknown pool never produces
// a ranked recommendation simply because a champion has favorable public data.
//
// Opposing matchup targets come only from publicly locked champions. The user
// explicitly chooses a lane; the view does not infer an undisclosed position.
// Late matchup replies retain their target and lane generation before display.
// Public final-match win rates remain labeled as outcomes rather than lane wins.
const selected = ref(0)
const manual = ref(false)
const enemy = ref(0)
const position = ref('mid')
const matchups = ref<Matchup[]>([])
const searching = ref(false)
const notice = ref('')
const search = ref('')
let matchupGeneration = 0
// Attempt keys bind automatic adjustment reads to one public patch and champion, preventing repeated snapshot updates from retrying failures indefinitely.
const adjustmentAttempts = new Set<string>()
const room = computed(() => state.room)
const hero = computed(() => championMap.value.get(selected.value))
const stats = computed(() => room.value?.mode === 'mayhem' ? hero.value?.mayhem : hero.value?.aram)
const legal = computed(() => {
  const available = new Set(room.value?.available ?? [])
  if (room.value?.mode !== 'ranked' && room.value?.current) available.add(room.value.current)
  const banned = new Set(room.value?.banned ?? [])
  const locked = new Set([...(room.value?.allies ?? []), ...(room.value?.enemies ?? [])].filter(member => member.locked).map(member => member.champion))
  return state.database.champions.filter(champion => available.has(champion.id) && !banned.has(champion.id) && (room.value?.mode !== 'ranked' || !locked.has(champion.id)))
})
const candidates = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return legal.value.filter(champion => !needle || `${champion.name}${champion.title}${champion.alias}`.toLowerCase().includes(needle)).sort((a, b) => {
    if (room.value?.mode === 'ranked') return a.id - b.id
    const aStats = room.value?.mode === 'mayhem' ? a.mayhem : a.aram
    const bStats = room.value?.mode === 'mayhem' ? b.mayhem : b.aram
    return (bStats.winRate ?? -1) - (aStats.winRate ?? -1)
  })
})
const recommendations = computed(() => {
  const available = new Set(legal.value.map(champion => champion.id))
  // Shrink small observations toward fifty percent before ordering while preserving their original displayed rate and sample count.
  return matchups.value.filter(entry => available.has(entry.championId)).sort((a, b) => (b.winRate - 50) * b.samples / (b.samples + 100) - (a.winRate - 50) * a.samples / (a.samples + 100)).slice(0, 6)
})
watch(() => room.value?.current, (current, previous) => {
  if (!manual.value || selected.value === previous) selected.value = current ?? 0
}, { immediate: true })
watch(() => room.value?.queueId, () => { manual.value = false; selected.value = room.value?.current ?? 0; matchups.value = []; matchupGeneration++; searching.value = false })
watch([enemy, position], () => { matchups.value = []; matchupGeneration++; searching.value = false })
watch(() => [room.value?.mode, selected.value, state.database.patch] as const, () => { void readAdjustments(selected.value) }, { immediate: true })

/** inspect changes only the inspected hero and requests its public adjustments. */
async function inspect(id: number): Promise<void> { selected.value = id; manual.value = id !== room.value?.current; await readAdjustments(id) }
/** readAdjustments fetches missing Mayhem details once per source patch and champion.
 * Ordinary ARAM and ranked inspections do not call the Mayhem provider. Explicit
 * retries bypass the attempt key, while automatic failures await user retry or
 * a genuinely different patch instead of looping on subsequent snapshots.
 */
async function readAdjustments(id: number, force = false): Promise<void> {
  if (!id || room.value?.mode !== 'mayhem') return
  const champion = championMap.value.get(id)
  if (!force && champion?.mayhem.adjustments != null) return
  const key = `${state.database.patch}|${id}`
  if (!force && adjustmentAttempts.has(key)) return
  adjustmentAttempts.add(key)
  const error = await action(() => api.InspectChampion(id))
  if (selected.value === id && room.value?.mode === 'mayhem') notice.value = error
}
/** lookup obtains final-match outcomes for one disclosed enemy and explicit lane. */
async function lookup(): Promise<void> {
  const request = ++matchupGeneration
  searching.value = true; notice.value = ''
  try { const result = await api.GetMatchups(enemy.value, position.value); if (request === matchupGeneration) matchups.value = result ?? [] }
  catch (reason) { if (request === matchupGeneration) notice.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if (request === matchupGeneration) searching.value = false }
}
/** openPanel opens the native companion with client-following enabled by default. */
async function openPanel(): Promise<void> { notice.value = await action(() => api.SetDraftPanel(true, true)) }
</script>

<template>
  <div class="draft-workspace scroll-region">
    <section class="card draft-panel-entry"><strong>大乱斗快捷选人 <span class="muted">· 英雄 / 皮肤</span></strong><button class="button" title="独立窗口跟随客户端，进入大乱斗选人后自动显示" @click="openPanel">打开贴边面板 ↗</button></section>
    <template v-if="room">
    <div class="draft-heading"><div><div class="eyebrow">实时英雄选择</div><h2>{{ modeNames[room.mode] }} <span class="quiet-pill">跟随客户端</span></h2></div><span class="muted">队列 {{ room.queueId }} · 对方只显示公开锁定英雄</span></div>
    <section v-if="room.mode !== 'ranked'" class="card quick-workspace"><QuickDraft :room="room" :database="state.database" /></section>
    <div class="draft-lineups"><section v-for="(members, side) in [room.allies, room.enemies]" :key="side" class="card lineup-card"><div class="section-heading"><h3>{{ side ? '敌方已公开' : '己方预选阵容' }}</h3><small>{{ members?.length || 0 }} 人</small></div><div class="lineup-slots"><button v-for="member in members" :key="member.cell" :class="{ local: member.local, selected: selected === member.champion && member.champion > 0 }" :disabled="!member.champion" @click="inspect(member.champion)"><Portrait :id="member.champion" :size="42" /><span>{{ member.champion ? championMap.get(member.champion)?.name : '未公开' }}</span><small>{{ member.local ? '本人 · ' : '' }}{{ member.locked ? '已锁定' : side ? '未知' : '预选' }}</small></button></div></section></div>
    <p v-if="notice" class="inline-note error">{{ notice }}</p>
    <div class="draft-body"><section class="card champion-pool"><div class="section-heading"><h3>{{ room.mode === 'ranked' ? '当前可选英雄' : '当前英雄与替补池' }}</h3><small>{{ candidates.length }} 位</small></div><div class="search-field"><span>⌕</span><input v-model="search" placeholder="搜索当前可选英雄" aria-label="搜索可选英雄" /></div><div class="draft-hero-grid"><button v-for="champion in candidates" :key="champion.id" :class="{ selected: selected === champion.id }" @click="inspect(champion.id)"><Portrait :id="champion.id" :size="40" /><strong>{{ champion.name }}</strong><small v-if="room.mode !== 'ranked'" :class="((room.mode === 'mayhem' ? champion.mayhem : champion.aram).winRate ?? 0) >= 50 ? 'text-good' : 'muted'">{{ decimal((room.mode === 'mayhem' ? champion.mayhem : champion.aram).winRate) }}%</small></button></div><p v-if="!candidates.length" class="muted">尚未读取到当前可选池，不生成可选英雄建议。</p></section>
      <aside class="card champion-inspection"><template v-if="hero"><div class="detail-heading"><Portrait :id="hero.id" :size="54" /><div><div class="eyebrow">{{ hero.title }}</div><h2>{{ hero.name }}</h2></div></div><template v-if="room.mode !== 'ranked' && stats"><div class="champion-strength"><div><small>模式强度</small><strong>{{ stats.tier || '—' }}</strong></div><div><small>对局胜率</small><strong>{{ stats.winRate == null ? '—' : `${decimal(stats.winRate)}%` }}</strong></div><div><small>样本量</small><strong>{{ stats.samples ? stats.samples.toLocaleString() : '—' }}</strong></div></div><h3>模式平衡调整</h3><div v-if="stats.adjustments?.length" class="adjustments"><div v-for="entry in stats.adjustments" :key="entry.label"><span>{{ entry.label }}</span><strong :class="entry.helpful ? 'text-good' : 'text-bad'">{{ entry.display }} <small>{{ entry.helpful ? '增益' : '削弱' }}</small></strong></div></div><p v-else class="muted">{{ stats.adjustments ? '来源未列出调整，以客户端为准。' : '调整数据待读取，不能视为无调整。' }}</p><p class="coverage-note">{{ stats.source || '来源待读取' }}</p><button v-if="room.mode === 'mayhem'" class="button" @click="readAdjustments(hero.id, true)">重新读取英雄调整</button></template><p v-else class="muted">选择已公开锁定的敌方英雄和分路，查看当前可选英雄的对位数据。</p></template><EmptyState v-else title="查看一位英雄" description="选择己方预选英雄或可选池中的英雄。" /></aside></div>
    <section v-if="room.mode === 'ranked'" class="card matchup-section"><div class="section-heading"><div><h3>对位参考</h3><small>最终对局胜率，非对线期胜率 · 不推断隐藏分路</small></div><div class="toolbar-actions"><select v-model.number="enemy" aria-label="公开锁定敌方英雄"><option :value="0">选择已锁定对手</option><option v-for="member in (room.enemies ?? []).filter(entry => entry.champion && entry.locked)" :key="member.cell" :value="member.champion">{{ championMap.get(member.champion)?.name }}</option></select><select v-model="position" aria-label="手动选择对位分路"><option value="top">上路</option><option value="jungle">打野</option><option value="mid">中路</option><option value="adc">下路</option><option value="support">辅助</option></select><button class="button primary" :disabled="!enemy || searching" @click="lookup">{{ searching ? '读取中…' : '查询对位' }}</button></div></div><div class="matchup-results"><button v-for="entry in recommendations" :key="entry.championId" @click="inspect(entry.championId)"><Portrait :id="entry.championId" :size="36" /><div><strong>{{ championMap.get(entry.championId)?.name }}</strong><small>{{ entry.samples }} 场{{ entry.samples < 100 ? ' · 小样本' : '' }}</small></div><b>{{ decimal(entry.winRate) }}%</b><span :title="entry.source">ⓘ</span></button><p v-if="!recommendations.length" class="muted">{{ matchups.length ? '当前可选池中没有对应对位样本。' : '选择对手和分路后读取数据，推荐仅限当前可选英雄。' }}</p></div></section>
  </template>
  <EmptyState v-else title="等待英雄选择" description="进入预选后自动显示己方阵容、已公开对手，以及当前模式的强度和 Buff / Debuff。" icon="⚔" />
  <p v-if="!room && notice" class="inline-note error">{{ notice }}</p>
  </div>
</template>
