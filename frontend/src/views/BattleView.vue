<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { action, state } from '../store'
import { decimal, modeNames, scopedSummary } from '../format'
import type { Match } from '../types'
import ScoutCard from '../components/ScoutCard.vue'
import EmptyState from '../components/EmptyState.vue'
import AppIcon from '../components/AppIcon.vue'
import BattleInspector from '../components/BattleInspector.vue'
import InfoHint from '../components/InfoHint.vue'
import PremadeAnalysis from '../components/PremadeAnalysis.vue'

// BattleView.vue arranges publicly observed players into two five-column teams
// and opens contextual inspection without navigating away from the lineup.
// Every visible aggregate follows the board's single mode scope. Team rates pool
// player samples, including separately counted overlapping games, and their label
// describes historical observations rather than a current-match prediction.
//
// The drawer selection retains a player identity and optional recorded match.
// Battle, account, and scope transitions dismiss that selection before it can
// acquire a different context. Each card receives its independently retained
// scout object; renderer-local lens changes and scrolling do not ask the service
// to rebuild player records or transfer another snapshot through the bridge.
const emit = defineEmits<{ query: [puuid: string] }>()
// Board controls and inspection belong to this mounted workspace; the service
// remains the authority for analysis depth, mode scope, and retained observations.
const notice = ref('')
const lens = ref<'form' | 'contribution'>('form')
const inspection = ref<{ puuid: string; match?: Match } | null>(null)
const inspectedPlayer = computed(() => state.battle?.players.find(player => player.puuid === inspection.value?.puuid))
const sourceLabel = computed(() => state.battle?.source.split(' · ')[0] || '自动同步阵容')
const relationshipContext = computed(() => {
  const id = state.battle?.key.startsWith('review:') ? Number(state.battle.key.slice(7)) : 0
  const match = state.history?.matches.find(match => match.id === id) || Object.values(state.scouts).flatMap(scout => scout.profile?.matches || []).find(match => match.id === id)
  return { id, before: match?.created || Date.now() }
})
const sides = computed(() => [true, false].map(ally => {
  const players = state.battle?.players?.filter(player => player.ally === ally).slice(0, 5) ?? []
  const samples = players.map(player => state.scouts[player.puuid]?.profile).filter(profile => !!profile)
    .map(profile => scopedSummary(profile!, state.analysis.sameMode ? state.battle!.mode : 'all'))
  const games = samples.reduce((sum, sample) => sum + sample.games, 0)
  const wins = samples.reduce((sum, sample) => sum + sample.wins, 0)
  return { ally, players, members: players.filter(player => !player.hidden).map(player => ({ puuid: player.puuid, name: player.name, team: ally ? 100 : 200 })), games, rate: games ? wins / games * 100 : null, read: samples.length }
}))
const completed = computed(() => state.battle?.players?.filter(player => state.scouts[player.puuid]?.profile && !state.scouts[player.puuid]?.loading).length ?? 0)
watch(() => [state.battle?.key, state.account?.puuid, state.analysis.sameMode], () => { inspection.value = null })
/** run exposes an explicit operation's error beside its originating battle board. */
async function run(operation: () => Promise<unknown>): Promise<void> { notice.value = await action(operation) }
/** changeCount applies a supported history depth without changing current mode scope. */
function changeCount(event: Event): void { void run(() => api.SetAnalysis(Number((event.target as HTMLSelectElement).value), state.analysis.sameMode)) }
/** inspect retains the clicked player's identity and optional match for the drawer. */
function inspect(puuid: string, match?: Match): void { inspection.value = { puuid, match } }
/** query opens the complete history workspace after the existing lookup succeeds.
 * Failure leaves the board and its drawer available, with the returned error
 * beside the battle instead of navigating to an unrelated previously queried profile.
 */
async function query(puuid: string): Promise<void> {
  const error = await action(() => api.Lookup(puuid, 0))
  notice.value = error
  if (!error) emit('query', puuid)
}
</script>

<template>
  <div class="battle-workspace">
    <div class="board-toolbar"><div class="board-context"><h2>{{ state.battle ? modeNames[state.battle.mode] : '等待这一局的阵容' }}</h2><span class="board-source" :title="state.battle?.source">{{ sourceLabel }}</span></div><div class="board-actions"><div class="board-lenses" role="group" aria-label="分析视角"><button :aria-pressed="lens === 'form'" @click="lens = 'form'">近期表现</button><button :aria-pressed="lens === 'contribution'" @click="lens = 'contribution'">团队贡献</button></div><span class="toolbar-divider" /><label class="inline-label">样本<select :value="state.analysis.matchCount" aria-label="分析对局场数" @change="changeCount"><option v-for="count in [20, 50, 100, 200]" :key="count" :value="count">{{ count }} 场</option></select></label><button class="scope-button" :aria-pressed="state.analysis.sameMode" title="切换所有玩家的统计范围" @click="run(() => api.SetAnalysis(state.analysis.matchCount, !state.analysis.sameMode))">{{ state.analysis.sameMode ? '同模式' : '全部模式' }}</button><button class="icon-button" aria-label="刷新战绩" title="刷新战绩" :disabled="!state.connected" @click="run(() => api.RefreshBattle())"><AppIcon name="refresh" :size="17" /></button><button class="button review-button" :disabled="!state.connected" @click="run(() => api.ReviewLastGame())">复盘最近一局</button></div></div>
    <p v-if="notice" class="inline-note error">{{ notice }}</p>
    <div v-if="state.battle" class="battle-board"><section v-for="side in sides" :key="String(side.ally)" class="board-team" :class="side.ally ? 'is-friendly' : 'is-enemy'"><header class="board-team-heading"><h3><i class="team-dot" />{{ side.ally ? '己方阵容' : '敌方阵容' }}<small>{{ side.players.length }} 人</small></h3><div class="team-sample" :title="`${side.games} 场玩家样本汇总；同一场比赛在不同玩家中分别计数。此数值不是本局获胜概率。`">样本胜率 <b>{{ side.rate == null ? '—' : `${decimal(side.rate, 0)}%` }}</b></div><PremadeAnalysis :players="side.members" :exclude="relationshipContext.id" :before="relationshipContext.before" /><span class="team-read">{{ side.read }} / {{ side.players.length }} 已读取</span></header><div class="board-player-grid"><ScoutCard v-for="(player, index) in side.players" :key="`${state.battle.key}-${player.puuid || index}`" :player="player" :scout="state.scouts[player.puuid]" :mode="state.battle.mode" :same-mode="state.analysis.sameMode" :lens="lens" :exclude="relationshipContext.id" :before="relationshipContext.before" @inspect="inspect" @retry="id => run(() => api.RetryScout(id))" /></div></section></div>
    <EmptyState v-else title="进入英雄选择后，自动同步阵容" description="只读取双方已公开的信息。客户端空闲时，也可以载入最近一局复盘。" icon="♧"><button class="button primary" :disabled="!state.connected" @click="run(() => api.ReviewLastGame())">复盘最近一局</button></EmptyState>
    <div v-if="state.battle" class="board-caption"><span><i class="caption-dot win" />胜 <i class="caption-dot loss" />负 <InfoHint label="对战助手统计说明" text="卡片内可独立滚动，战绩由新到旧。胜率、K/D/A 与最近战绩采用所选模式。历史样本不代表本局获胜概率。" /></span><span>{{ completed }} / {{ state.battle.players.length }} 已完成 · {{ state.analysis.sameMode ? '同模式' : '全部模式' }}</span></div>
    <BattleInspector v-if="inspection && inspectedPlayer && state.battle" :key="inspection.puuid" :player="inspectedPlayer" :scout="state.scouts[inspection.puuid]" :mode="state.battle.mode" :same-mode="state.analysis.sameMode" :initial-match="inspection.match" @close="inspection = null" @query="query" />
  </div>
</template>
