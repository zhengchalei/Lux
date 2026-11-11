<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, useId, watch } from 'vue'
import { championMap } from '../store'
import { historyKind, historyKinds, type HistoryFilter } from '../historyFilters'
import type { Match } from '../types'
import AppIcon from './AppIcon.vue'
import Portrait from './Portrait.vue'

// HistoryFilters.vue presents champion, match-type, and result filters in one
// compact popover. Choices apply immediately to the parent's retained filter,
// while search only narrows the champion picker. Champion options come from
// recorded games in the selected type, including records excluded from aggregate
// analysis; their counts describe browsable rows rather than eligible samples.
//
// The native auto popover owns outside-click dismissal and top-layer painting.
// Opening measures the actual panel once and clamps it inside the viewport.
// External scrolling and resizing dismiss it, while its own contained scrolling
// remains usable. A render generation invalidates delayed positioning after
// dismissal or account replacement; pointer movement never measures layout or
// requests data, preserving the history workspace's local browsing behavior.

// props and model keep retrieved account observations separate from the locally
// applied filter, which the parent owns across picker openings and dismissals.
const props = defineProps<{
  matches: Match[] // Retrieved unfiltered match records.
  owner: string // Current profile account identity.
}>()
const model = defineModel<HistoryFilter>({ required: true })
// Panel references and search belong to this mounted picker. Persistent choices
// belong to the parent model and survive native popover dismissal without writes.
const trigger = ref<HTMLButtonElement>()
const panel = ref<HTMLElement>()
const searchInput = ref<HTMLInputElement>()
const search = ref('')
const open = ref(false)
const position = ref({ left: '0px', top: '0px', visibility: 'hidden' as 'hidden' | 'visible' })
const id = useId()
let generation = 0
const activeCount = computed(() => Number(model.value.kind !== 'all') + Number(model.value.champion > 0) + Number(model.value.outcome !== 'all'))
const kindLabel = computed(() => historyKinds.find(kind => kind.id === model.value.kind)!.label)
const championLabel = computed(() => championMap.value.get(model.value.champion)?.name || `英雄 ${model.value.champion}`)
const champions = computed(() => {
  const counts = new Map<number, number>()
  for (const match of props.matches) {
    if (match.championId > 0 && (model.value.kind === 'all' || historyKind(match.queueId) === model.value.kind)) counts.set(match.championId, (counts.get(match.championId) || 0) + 1)
  }
  return [...counts].sort((a, b) => b[1] - a[1] || a[0] - b[0]).map(([id, games]) => {
    const hero = championMap.value.get(id)
    const name = hero?.name || `英雄 ${id}`
    return { id, games, name, search: `${name} ${hero?.title || ''} ${hero?.alias || ''} ${id}`.toLocaleLowerCase() }
  })
})
const visibleChampions = computed(() => champions.value.filter(hero => hero.search.includes(search.value.trim().toLocaleLowerCase())))
// Result labels remain readable without depending on the green and red colors.
const outcomes = [{ id: 'all', label: '全部结果' }, { id: 'win', label: '仅看胜利' }, { id: 'loss', label: '仅看失败' }] as const

/** set replaces one filter field while retaining the other independent choices.
 * The parent receives a new model object, so choosing a type never silently
 * clears an already selected champion or observed result.
 */
function set<K extends keyof HistoryFilter>(key: K, value: HistoryFilter[K]): void { model.value = { ...model.value, [key]: value } }
/** reset restores the full retrieved sample and clears the temporary hero search.
 * The picker stays open when invoked inside it. Collection depth and the
 * parent's current player identity remain owned by their separate controls.
 */
function reset(): void { model.value = { kind: 'all', champion: 0, outcome: 'all' }; search.value = '' }
/** cleanup invalidates pending positioning and removes temporary viewport dismissal listeners.
 * Native light dismissal and unmounting both call this operation. The applied
 * filter remains owned by the history view after the picker disappears.
 */
function cleanup(): void {
  generation++
  open.value = false
  document.removeEventListener('scroll', onScroll, true)
  window.removeEventListener('resize', close)
}
/** close dismisses the native picker without changing its applied filter model.
 * Repeated calls are permitted, including after account replacement. Cleanup
 * invalidates delayed positioning even when the browser has already hidden it.
 */
function close(): void { panel.value?.hidePopover(); cleanup() }
/** onScroll dismisses a displaced anchor while allowing the picker's own scroll regions. */
function onScroll(event: Event): void { if (event.target instanceof Node && !panel.value?.contains(event.target)) close() }
/** beforeToggle reconciles native visibility and installs dismissal before positioning begins.
 * The opening event clears only temporary search. Closing invalidates pending
 * measurements immediately, including resize events arriving before the native toggle completes.
 */
function beforeToggle(event: Event): void {
  if ((event as ToggleEvent).newState === 'closed') { cleanup(); return }
  generation++
  open.value = true
  search.value = ''
  position.value.visibility = 'hidden'
  document.addEventListener('scroll', onScroll, { capture: true, passive: true })
  window.addEventListener('resize', close, { passive: true })
}
/** afterToggle positions the picker after the browser establishes native open geometry. */
function afterToggle(event: Event): void { if ((event as ToggleEvent).newState === 'open') void show() }
/** escape closes the picker and returns keyboard focus to its visible trigger. */
function escape(event: KeyboardEvent): void { event.preventDefault(); event.stopPropagation(); close(); trigger.value?.focus() }

/** show positions the opened picker beside its trigger and focuses champion search.
 * The browser owns the trigger's native toggle. Search starts empty on
 * reopening, while all applied choices remain visible in the toolbar chips.
 *
 * Native popovers escape clipped history containers, so their actual wrapped
 * height must be measured before choosing a position. The render generation
 * prevents a delayed measurement from reopening a dismissed or replaced account's
 * picker. Viewport margins constrain both dimensions, and external movement
 * dismisses the panel instead of introducing a continuous positioning loop.
 */
async function show(): Promise<void> {
  if (!open.value || !panel.value?.matches(':popover-open') || !trigger.value) return
  const request = generation
  await nextTick()
  if (request !== generation || !panel.value || !trigger.value) return
  const anchor = trigger.value.getBoundingClientRect()
  const bounds = panel.value.getBoundingClientRect()
  // Reserve a viewport gutter so borders and focus outlines remain fully visible at the application's minimum size.
  const margin = 12
  const below = anchor.bottom + 8
  const top = below + bounds.height <= innerHeight - margin ? below : Math.max(margin, innerHeight - bounds.height - margin)
  position.value = { left: `${Math.max(margin, Math.min(anchor.left, innerWidth - bounds.width - margin))}px`, top: `${top}px`, visibility: 'visible' }
  // Apply visible positioning before focusing, because hidden native popover inputs cannot receive focus during the measurement render.
  await nextTick()
  if (request === generation) searchInput.value?.focus({ preventScroll: true })
}
watch(() => props.owner, close)
onUnmounted(cleanup)
</script>

<template>
  <div class="history-filter-bar">
    <button ref="trigger" class="filter-trigger" :popovertarget="id" :class="{ active: open || activeCount }" :aria-expanded="open" :aria-controls="id" aria-haspopup="dialog" aria-label="筛选战绩"><AppIcon name="filter" :size="16" />筛选<span v-if="activeCount" class="filter-count">{{ activeCount }}</span><AppIcon name="chevron" :size="13" /></button>
    <div class="filter-chips" aria-label="当前战绩筛选">
      <span v-if="!activeCount" class="filter-placeholder">全部英雄 · 全部比赛</span>
      <button v-if="model.kind !== 'all'" class="filter-chip" :aria-label="`移除${kindLabel}筛选`" @click="set('kind', 'all')">{{ kindLabel }}<AppIcon name="close" :size="11" /></button>
      <button v-if="model.champion" class="filter-chip" :aria-label="`移除${championLabel}筛选`" @click="set('champion', 0)"><Portrait :id="model.champion" :size="19" />{{ championLabel }}<AppIcon name="close" :size="11" /></button>
      <button v-if="model.outcome !== 'all'" class="filter-chip" :class="model.outcome" aria-label="移除胜负筛选" @click="set('outcome', 'all')">{{ model.outcome === 'win' ? '仅看胜利' : '仅看失败' }}<AppIcon name="close" :size="11" /></button>
    </div>
    <button v-if="activeCount" class="filter-reset" @click="reset">重置筛选</button>
  </div>
  <section :id="id" ref="panel" popover="auto" class="history-filter-panel" role="dialog" aria-label="战绩筛选" :style="position" @beforetoggle="beforeToggle" @toggle="afterToggle" @keydown.esc="escape">
    <header class="filter-panel-heading"><h3>按英雄</h3>
      <label class="filter-search"><AppIcon name="query" :size="16" /><input ref="searchInput" v-model="search" aria-label="搜索战绩英雄" placeholder="搜索英雄名称 / 别名" /><button v-if="search" aria-label="清空英雄搜索" @click="search = ''; searchInput?.focus()"><AppIcon name="close" :size="13" /></button></label>
      <button class="filter-dismiss" aria-label="关闭战绩筛选" @click="close"><AppIcon name="close" :size="16" /></button>
    </header>
    <div class="filter-panel-body scroll-region">
      <div class="filter-champions scroll-region" role="group" aria-label="按英雄筛选">
        <button v-if="!search" :aria-pressed="!model.champion" :class="{ selected: !model.champion }" class="all-champions" aria-label="全部英雄" @click="set('champion', 0)"><span>全部</span></button>
        <button v-for="hero in visibleChampions" :key="hero.id" class="filter-champion" :class="{ selected: model.champion === hero.id }" :aria-pressed="model.champion === hero.id" :aria-label="`筛选英雄 ${hero.name}`" :title="`${hero.name} · 当前类型 ${hero.games} 场`" @click="set('champion', model.champion === hero.id ? 0 : hero.id)"><span class="filter-portrait"><Portrait :id="hero.id" :size="40" /><span v-if="model.champion === hero.id" class="filter-check" aria-hidden="true">✓</span></span></button>
        <p v-if="!visibleChampions.length" class="filter-hero-empty">{{ search ? '当前类型中没有找到这个英雄' : '当前类型暂无英雄记录' }}</p>
      </div>
      <h3 class="filter-section-title">按类型</h3>
      <div class="filter-options" role="group" aria-label="按比赛类型筛选"><button v-for="kind in historyKinds" :key="kind.id" :class="{ selected: model.kind === kind.id }" :aria-pressed="model.kind === kind.id" @click="set('kind', kind.id)">{{ kind.label }}</button></div>
      <h3 class="filter-section-title">按胜负</h3>
      <div class="filter-options" role="group" aria-label="按胜负筛选"><button v-for="outcome in outcomes" :key="outcome.id" :class="[outcome.id, { selected: model.outcome === outcome.id }]" :aria-pressed="model.outcome === outcome.id" @click="set('outcome', outcome.id)">{{ outcome.label }}</button></div>
    </div>
    <footer class="filter-panel-footer"><button class="filter-reset" :disabled="!activeCount && !search" @click="reset">重置筛选</button><button class="button primary" @click="close">完成</button></footer>
  </section>
</template>

<style scoped>
.history-filter-bar { display: flex; align-items: center; gap: 12px; width: 100%; min-width: 0; }
.filter-trigger { display: inline-flex; align-items: center; gap: 7px; min-height: 32px; border: 1px solid var(--line); border-radius: 7px; padding: 5px 10px; background: var(--surface); font-size: 12px; flex-shrink: 0; }
.filter-trigger:hover, .filter-trigger.active { color: var(--blue); border-color: var(--blue); background: var(--blue-soft); }
.filter-count { display: grid; place-items: center; background: var(--blue); color: var(--surface); border-radius: 4px; font-size: 10px; min-width: 16px; height: 16px; }
.filter-chips { display: flex; align-items: center; gap: 7px; min-width: 0; }
.filter-placeholder { color: var(--muted); font-size: 12px; }
.filter-chip { display: inline-flex; align-items: center; gap: 6px; border-radius: 5px; padding: 4px 7px; min-height: 27px; background: var(--panel); font-size: 11px; white-space: nowrap; }
.filter-chip:hover { color: var(--blue); background: var(--blue-soft); }
.filter-chip.win { color: var(--good); background: var(--win); }
.filter-chip.loss { color: var(--bad); background: var(--loss); }
.filter-reset { color: var(--muted); background: transparent; padding: 5px 0; font-size: 12px; white-space: nowrap; }
.filter-reset:hover:not(:disabled) { color: var(--blue); }
.history-filter-bar > .filter-reset { margin-left: auto; }
.history-filter-panel { position: fixed; inset: auto; margin: 0; width: 386px; max-width: calc(100vw - 24px); max-height: calc(100dvh - 24px); padding: 0; color: var(--ink); border: 1px solid var(--line); border-radius: 11px; background: var(--surface); box-shadow: 0 10px 38px #0c162735; font-size: 12px; overflow: hidden; }
.history-filter-panel:popover-open { display: flex; flex-direction: column; }
.history-filter-panel::backdrop { background: transparent; }
.filter-panel-heading { display: flex; align-items: center; gap: 10px; padding: 14px 16px 8px; flex-shrink: 0; }
.filter-panel-heading > h3 { font-size: 11px; font-weight: 500; color: var(--muted); white-space: nowrap; }
.filter-dismiss { display: grid; place-items: center; color: var(--muted); background: transparent; padding: 4px; border-radius: 4px; }
.filter-dismiss:hover { background: var(--panel); color: var(--ink); }
.filter-panel-body { padding: 0 16px 16px; min-height: 0; overflow-y: auto; }
.filter-section-title { color: var(--muted); font-size: 11px; font-weight: 500; }
.all-champions { display: grid; place-items: center; color: var(--muted); background: transparent; font-size: 11px; padding: 2px 0; }
.all-champions > span { display: grid; place-items: center; width: 44px; height: 44px; border: 1px solid var(--line); border-radius: 7px; background: var(--panel); }
.all-champions.selected > span, .all-champions:hover > span { color: var(--blue); background: var(--blue-soft); border-color: var(--blue); }
.filter-search { display: flex; flex: 1; min-width: 0; align-items: center; gap: 7px; padding: 7px 9px; border: 1px solid var(--line); border-radius: 6px; background: var(--panel); color: var(--muted); }
.filter-search:focus-within { border-color: var(--blue); }
.filter-search input { flex: 1; width: 0; min-width: 0; border: 0; background: transparent; font-size: 12px; outline: none; }
.filter-search button { display: grid; place-items: center; color: var(--muted); background: transparent; padding: 0; }
.filter-champions { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); align-content: start; gap: 7px 5px; max-height: 111px; overflow-y: auto; overscroll-behavior: contain; padding: 4px 2px; margin: 0 -2px; }
.filter-champion { display: flex; flex-direction: column; align-items: center; gap: 5px; padding: 2px 0; min-width: 0; border-radius: 5px; background: transparent; }
.filter-portrait { position: relative; display: flex; border: 2px solid transparent; border-radius: 8px; }
.filter-portrait :deep(.portrait) { border-radius: 5px; }
.filter-champion:hover .filter-portrait, .filter-champion.selected .filter-portrait { border-color: var(--blue); }
.filter-champion.selected { color: var(--blue); }
.filter-check { display: grid; place-items: center; position: absolute; bottom: -2px; right: -2px; width: 15px; height: 15px; border-radius: 4px 0 5px 0; background: var(--blue); color: var(--surface); font-size: 10px; }
.filter-hero-empty { grid-column: 1 / -1; color: var(--muted); text-align: center; padding: 22px 0; font-size: 12px; }
.filter-section-title { margin: 17px 0 9px; }
.filter-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 7px; }
.filter-options button { border: 1px solid var(--line); border-radius: 5px; padding: 7px 2px; background: var(--panel); font-size: 12px; white-space: nowrap; }
.filter-options button:hover, .filter-options button.selected { color: var(--blue); border-color: var(--blue); background: var(--blue-soft); }
.filter-options .win.selected { color: var(--good); border-color: var(--good); background: var(--win); }
.filter-options .loss.selected { color: var(--bad); border-color: var(--bad); background: var(--loss); }
.filter-panel-footer { display: flex; justify-content: space-between; align-items: center; border-top: 1px solid var(--line); padding: 10px 16px; flex-shrink: 0; }
.filter-panel-footer .button { min-height: 30px; padding: 5px 20px; font-size: 12px; }
</style>
