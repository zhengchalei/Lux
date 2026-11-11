<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { Combatant, Match, Mode, Scout } from '../types'
import { decimal, modeFor, scopedSummary } from '../format'
import AppIcon from './AppIcon.vue'
import Portrait from './Portrait.vue'
import ProfileAnalysis from './ProfileAnalysis.vue'
import VirtualMatchList from './VirtualMatchList.vue'
import MatchDetails from './MatchDetails.vue'

// BattleInspector.vue opens a player's sample or one selected match beside the
// persistent battle board. The parent keys the dialog by player identity and
// removes it when its battle changes, preventing an old selection from appearing
// under a different roster. The selected match retains its recorded perspective;
// MatchDetails owns asynchronous detail loading and its response identity check.
//
// The native modal dialog supplies focus containment, Escape handling, and focus
// restoration when closed. Rendering the existing virtual match list bounds the
// expanded sample's DOM even at two hundred requested records. Filtering uses
// the board's mode scope, while champion filtering changes only the displayed
// list and leaves the sample statistics explicitly scoped to the board.
const props = defineProps<{
  player: Combatant // Inspected public player identity.
  scout?: Scout // Retained player retrieval state.
  mode: Mode // Current battle gameplay mode.
  sameMode: boolean // Board sample scope preference.
  initialMatch?: Match // Optional recorded match perspective.
}>()
const emit = defineEmits<{ close: []; query: [puuid: string] }>()
// The dialog owns temporary inspection state; closing it discards filters while
// the still-mounted parent board retains every independent recent-list offset.
const dialog = ref<HTMLDialogElement>()
const selected = ref<Match | null>(props.initialMatch ?? null)
const hero = ref(0)
const profile = computed(() => props.scout?.profile)
const summary = computed(() => profile.value ? scopedSummary(profile.value, props.sameMode ? props.mode : 'all') : null)
const matches = computed(() => (profile.value?.matches ?? []).filter(match =>
  (!props.sameMode || modeFor(match.queueId) === props.mode) && (!hero.value || match.championId === hero.value)))
onMounted(() => dialog.value?.showModal())

/** close dismisses this dialog and lets its close event notify the parent. */
function close(): void { dialog.value?.close() }
/** backdrop dismisses inspection when a click targets the dialog's native backdrop.
 * Content clicks target the inner surface or its descendants and leave the
 * current player and selected match open for further inspection.
 */
function backdrop(event: MouseEvent): void { if (event.target === dialog.value) close() }
</script>

<template>
  <dialog ref="dialog" class="battle-inspector" aria-labelledby="inspector-title" @close="emit('close')" @click="backdrop">
    <section class="inspector-surface">
      <header class="inspector-header"><div><span class="eyebrow">{{ selected ? '单局详情' : '玩家分析' }} · {{ sameMode ? '与对局相同模式' : '全部模式' }}</span><h2 id="inspector-title">{{ profile?.name || player.name }}</h2></div><button class="icon-button" aria-label="关闭详情" autofocus @click="close"><AppIcon name="close" /></button></header>
      <div class="inspector-context"><Portrait :id="player.champion" :size="30" /><span>{{ profile?.rank || '段位待读取' }}</span><span class="push-right">样本 {{ summary?.games || 0 }} 场 <i>·</i> 胜率 {{ summary?.games ? `${decimal(summary.winRate, 0)}%` : '—' }}</span></div>
      <div v-if="selected" class="inspector-content scroll-region"><button class="inspector-back" @click="selected = null"><AppIcon name="back" :size="16" />返回玩家分析</button><MatchDetails :match="selected" :puuid="player.puuid" /></div>
      <div v-else-if="profile && summary" class="inspector-profile"><div class="inspector-analysis scroll-region"><ProfileAnalysis :profile="profile" :summary="summary" :hero="hero" @hero="hero = $event" /></div><div class="inspector-list-heading"><h3>{{ hero ? '选中英雄的战绩' : '已读取战绩' }}</h3><button v-if="hero" class="text-button" @click="hero = 0">清除英雄筛选</button><span class="push-right">{{ matches.length }} 场 · 点击查看单局</span></div><VirtualMatchList :matches="matches" :selected="0" @select="selected = $event" /></div>
      <div v-else class="player-empty">{{ scout?.error || '正在读取玩家信息…' }}</div>
      <footer class="inspector-footer"><span>数据随客户端自动同步</span><button class="button" @click="emit('query', player.puuid)">打开完整战绩页面<AppIcon name="arrow" :size="14" /></button></footer>
    </section>
  </dialog>
</template>
