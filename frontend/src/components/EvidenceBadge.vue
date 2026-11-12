<script setup lang="ts">
import { nextTick, onUnmounted, ref, useId, watch } from 'vue'
import type { EvidenceRow } from '../relationships'
import { date } from '../format'
import { openRecordedMatch } from '../matchNavigation'
import Portrait from './Portrait.vue'

// EvidenceBadge.vue reveals actionable historical evidence beside a compact
// relationship label. Its native popover participates in the browser top layer,
// including when the trigger belongs to a match dialog. Rows retain their exact
// recorded perspectives; selecting one opens details without searching by name.
//
// Hover, focus, and click share one local opening lifecycle. A short leave delay
// lets the pointer cross into the panel, while keyboard focus keeps its buttons
// available. Positioning occurs once per opening; external scrolling, resize,
// source replacement, and teardown release listeners and pending positioning.
// No mousemove handler or backend request participates in revealing evidence.

const props = defineProps<{
  label: string // Compact relationship trigger text.
  heading: string // Accessible evidence panel heading.
  rows: EvidenceRow[] // Newest-first recorded relationship observations.
  note: string // Source and inference explanation.
  caption?: string // Optional source-only panel coverage label.
  tone?: 'encounter' | 'party' // Independent relationship color semantics.
}>()
// Popover ownership remains local to this badge. A native open transition starts
// temporary viewport listeners; its close transition invalidates delayed placement.
const trigger = ref<HTMLButtonElement>()
const panel = ref<HTMLElement>()
const target = ref<Element | string>('body')
const open = ref(false)
const position = ref({ left: '0px', top: '0px', visibility: 'hidden' as 'visible' | 'hidden' })
const id = useId()
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined

/** cancelLeave clears the pending pointer departure when the panel is reentered. */
function cancelLeave(): void { clearTimeout(timer) }
/** cleanup releases temporary listeners and invalidates unfinished positioning after native dismissal. */
function cleanup(): void {
  generation++
  open.value = false
  cancelLeave()
  document.removeEventListener('scroll', onScroll, true)
  window.removeEventListener('resize', close)
}
/** close dismisses the popover and releases its local interaction lifetime. */
function close(): void { panel.value?.hidePopover(); cleanup() }
/** onScroll dismisses displaced anchors while permitting the evidence list's contained scrolling. */
function onScroll(event: Event): void { if (event.target instanceof Node && !panel.value?.contains(event.target)) close() }
/** leave allows pointer travel into the panel and preserves focused evidence actions.
 * Departure is delayed briefly to bridge the trigger gap. Keyboard focus inside
 * the trigger or panel keeps the evidence available until focus or context changes.
 */
function leave(): void {
  cancelLeave()
  timer = setTimeout(() => { if (document.activeElement !== trigger.value && !panel.value?.contains(document.activeElement)) close() }, 180)
}
/** blur dismisses evidence after focus leaves both the trigger and its actionable panel. */
function blur(event: FocusEvent): void { if (event.relatedTarget !== trigger.value && !(event.relatedTarget instanceof Node && panel.value?.contains(event.relatedTarget))) close() }
/** toggle tracks native opening and dismissal before its top-layer geometry changes. */
function toggle(event: Event): void {
  if ((event as ToggleEvent).newState === 'closed') { cleanup(); return }
  open.value = true
  position.value.visibility = 'hidden'
  document.addEventListener('scroll', onScroll, { capture: true, passive: true })
  window.addEventListener('resize', close, { passive: true })
}
/** show opens and positions evidence without moving focus during a pointer hover.
 * Content teleports into the nearest open dialog before native admission, keeping
 * modal focus containment and top-layer painting coherent. Repeated opens do nothing.
 *
 * Placement waits for rendered rows and uses a generation check so dismissal or
 * source replacement cannot reposition an abandoned panel. Viewport clamping and
 * a bounded row list keep evidence usable near either screen edge. Keyboard
 * users enter its first action with ArrowDown instead of losing trigger focus.
 */
async function show(): Promise<void> {
  cancelLeave()
  if (open.value || !trigger.value) return
  const observed = ++generation
  target.value = trigger.value.closest('dialog[open]') || 'body'
  await nextTick()
  if (observed !== generation || !panel.value) return
  panel.value.showPopover()
  await nextTick()
  if (observed !== generation || !panel.value || !trigger.value) return
  const anchor = trigger.value.getBoundingClientRect(), bounds = panel.value.getBoundingClientRect()
  const top = anchor.bottom + 6 + bounds.height <= innerHeight - 12 ? anchor.bottom + 6 : Math.max(12, anchor.top - bounds.height - 6)
  position.value = { left: `${Math.max(12, Math.min(anchor.left, innerWidth - bounds.width - 12))}px`, top: `${top}px`, visibility: 'visible' }
}
/** focusRows enters the first evidence action after an explicit keyboard opening. */
async function focusRows(event: KeyboardEvent): Promise<void> { event.preventDefault(); await show(); panel.value?.querySelector<HTMLButtonElement>('.evidence-row')?.focus() }
/** escape consumes one Escape for evidence before the surrounding modal can dismiss. */
function escape(event: KeyboardEvent): void { if (open.value) { event.preventDefault(); event.stopPropagation(); trigger.value?.focus({ preventScroll: true }); close() } }
/** inspect opens the selected recorded perspective after releasing its evidence popover. */
function inspect(row: EvidenceRow): void { trigger.value?.focus({ preventScroll: true }); close(); openRecordedMatch(row) }
watch(() => [props.rows, props.heading], close)
onUnmounted(cleanup)
</script>

<template>
  <button ref="trigger" type="button" class="evidence-badge" :class="tone || 'encounter'" :aria-label="heading" aria-haspopup="dialog" :aria-expanded="open" :aria-controls="id" @mouseenter="show" @mouseleave="leave" @focus="show" @blur="blur" @click="show" @keydown.down="focusRows" @keydown.esc="escape">{{ label }}</button>
  <Teleport :to="target"><div :id="id" ref="panel" popover="auto" role="dialog" :aria-label="heading" class="evidence-panel" :style="position" @beforetoggle="toggle" @mouseenter="cancelLeave" @mouseleave="leave" @focusout="blur" @keydown.esc="escape"><template v-if="open"><header><strong>{{ heading }}</strong><small>{{ caption || (rows.length > 8 ? `最近 8 场 · 共 ${rows.length} 场` : `${rows.length} 场记录`) }}</small></header><div class="evidence-rows scroll-region"><button v-for="row in rows.slice(0, 8)" :key="`${row.match.platformId || ''}:${row.match.id}`" class="evidence-row" :aria-label="`查看 ${row.match.id} 的对局详情`" :title="new Date(row.match.created).toLocaleString('zh-CN')" @click="inspect(row)"><Portrait v-if="tone !== 'party'" :id="row.match.championId" :size="27" /><span><strong>{{ row.label }}</strong><small>{{ row.match.queue }} · {{ date(row.match.created) }}</small></span><b :class="row.win ? 'text-good' : 'text-bad'">{{ row.win ? '胜' : '负' }}</b><small v-if="tone !== 'party'">{{ row.match.kills }}/{{ row.match.deaths }}/{{ row.match.assists }}</small></button></div><p>{{ note }}</p></template></div></Teleport>
</template>

<style scoped>
.evidence-badge { display: inline-flex; align-items: center; flex-shrink: 0; padding: 2px 5px; border-radius: 4px; font-size: 12px; white-space: nowrap; line-height: 16px; color: var(--blue); background: var(--blue-soft); }
.evidence-badge.party { color: #8360ad; background: #a47bc215; }
:root[data-theme=dark] .evidence-badge.party { color: #c6aae9; }
.evidence-badge:hover, .evidence-badge:focus-visible { box-shadow: inset 0 0 0 1px currentColor; }
.evidence-panel { position: fixed; inset: auto; margin: 0; width: min(380px, calc(100vw - 24px)); max-height: calc(100dvh - 24px); overflow: auto; border: 1px solid var(--line); border-radius: 9px; padding: 13px; background: var(--surface); color: var(--ink); box-shadow: 0 8px 35px #08142435; }
.evidence-panel header { display: flex; justify-content: space-between; align-items: center; gap: 10px; margin-bottom: 9px; }
.evidence-panel header strong { font-size: 12px; font-weight: 500; overflow-wrap: anywhere; }
.evidence-panel header small { color: var(--muted); font-size: 12px; white-space: nowrap; }
.evidence-rows { max-height: 270px; overflow-y: auto; overscroll-behavior: contain; }
.evidence-row { display: flex; align-items: center; gap: 9px; width: 100%; padding: 9px 4px; border-radius: 5px; background: transparent; text-align: left; }
.evidence-row:hover { background: var(--blue-soft); }
.evidence-row > span:not(.portrait) { min-width: 0; flex: 1; }
.evidence-row > :deep(.portrait) { flex: 0 0 auto; }
.evidence-row strong { display: block; font-size: 12px; font-weight: 500; }
.evidence-row span small { display: block; margin-top: 5px; }
.evidence-row small { font-size: 12px; color: var(--muted); }
.evidence-row b { font-size: 12px; font-weight: 500; }
.evidence-panel p { border-top: 1px solid var(--line); margin-top: 8px; padding-top: 9px; color: var(--muted); font-size: 12px; line-height: 1.7; white-space: pre-line; }
</style>
