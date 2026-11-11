<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import type { Match } from '../types'
import { compact, date, duration, matchLabel } from '../format'
import Portrait from './Portrait.vue'

// VirtualMatchList.vue limits DOM construction to the visible match rows.
// Fixed row geometry keeps scrolling independent from offscreen record count.
// The selected match is keyed by its stable ID, so filtering cannot silently
// redirect a detail request to a different record occupying the same row slot.
//
// Keyboard navigation selects a record before scrolling its virtual slot into
// view. A local navigation generation invalidates pending focus when a filter
// replaces the list or another key arrives. The next render then focuses that
// match's stable identifier rather than whichever button occupies an old index.
const props = defineProps<{
  matches: Match[] // Ordered filtered match perspectives.
  selected: number // Currently selected match identifier.
  showMetrics?: boolean // Wider player pages show recorded damage and farm.
}>()
const emit = defineEmits<{ select: [match: Match] }>()
// Row extent includes the gap, making scroll positions independent of text height.
const rowHeight = 78
// Viewport measurements bound visible DOM and keep the selected match independent
// from recycled row positions as the user scrolls or changes local filters.
const viewport = ref<HTMLElement>()
const scrollTop = ref(0)
const height = ref(500)
let observer: ResizeObserver | undefined
let navigation = 0
const start = computed(() => Math.max(0, Math.floor(scrollTop.value / rowHeight) - 3))
const visible = computed(() => props.matches.slice(start.value, start.value + Math.ceil(height.value / rowHeight) + 6))
watch(() => props.matches, () => {
  // Filter and account transitions reset the list before reusing its fixed row slots, preventing an out-of-range blank viewport.
  if (viewport.value) viewport.value.scrollTop = 0
  scrollTop.value = 0
  navigation++
})
onMounted(() => {
  observer = new ResizeObserver(entries => { height.value = entries[0]?.contentRect.height || 500 })
  if (viewport.value) observer.observe(viewport.value)
})
onUnmounted(() => observer?.disconnect())
/** scroll records the latest offset for the next viewport-sized row slice. */
function scroll(event: Event): void { scrollTop.value = (event.currentTarget as HTMLElement).scrollTop }

/** navigate selects the neighboring or boundary match and restores its row focus.
 * Arrow keys, Home, and End operate within the current filtered ordering. The
 * selected match is emitted before the viewport moves so detail identity remains
 * coherent with the row; modified browser shortcuts are left untouched.
 *
 * Virtual rows may not exist until Vue processes the new scroll offset. A local
 * generation discards delayed focus after list replacement or another key. The
 * next render locates the selected match by ID, preserving focus when scrolling
 * changes which record occupies the same virtual row position.
 */
async function navigate(event: KeyboardEvent): Promise<void> {
  if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || !props.matches.length) return
  event.preventDefault()
  const current = props.matches.findIndex(match => match.id === props.selected)
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? props.matches.length - 1
    : Math.max(0, Math.min(props.matches.length - 1, current + (event.key === 'ArrowDown' ? 1 : -1)))
  const match = props.matches[index]!
  const request = ++navigation
  emit('select', match)
  if (viewport.value) {
    const top = index * rowHeight
    const bottom = top + rowHeight
    if (top < viewport.value.scrollTop) viewport.value.scrollTop = top
    else if (bottom > viewport.value.scrollTop + viewport.value.clientHeight) viewport.value.scrollTop = bottom - viewport.value.clientHeight
    scrollTop.value = viewport.value.scrollTop
  }
  await nextTick()
  if (request === navigation) viewport.value?.querySelector<HTMLButtonElement>(`[data-match-id="${match.id}"]`)?.focus({ preventScroll: true })
}
</script>

<template>
  <div ref="viewport" class="match-list scroll-region" aria-label="对局列表" tabindex="0" @scroll.passive="scroll" @keydown="navigate">
    <div :style="{ height: `${matches.length * rowHeight}px`, position: 'relative' }">
      <div class="virtual-row-window" :style="{ transform: `translateY(${start * rowHeight}px)` }">
        <button v-for="match in visible" :key="match.id" :data-match-id="match.id" :tabindex="selected === match.id ? 0 : -1" class="match-row" :class="[{ selected: selected === match.id }, match.win ? 'win' : 'loss']" :aria-label="matchLabel(match)" :aria-pressed="selected === match.id" :title="`${match.queue} · ${new Date(match.created).toLocaleString('zh-CN')} · ${duration(match.duration)}`" @click="emit('select', match)">
          <span class="record-outcome">{{ match.win ? '胜' : '负' }}</span><Portrait :id="match.championId" :size="34" />
          <span class="record-copy"><strong>{{ match.name }}</strong><small>{{ match.queue }} · {{ date(match.created) }}</small></span>
          <span v-if="showMetrics" class="record-extra"><strong>{{ match.missingMetrics?.[0] ? '—' : compact(match.damage) }}</strong><small>伤害 · {{ match.cs }} 补刀</small></span>
          <span class="record-performance"><span class="kda">{{ match.kills }}<i>/</i><span>{{ match.deaths }}</span><i>/</i>{{ match.assists }}</span><small>{{ duration(match.duration) }}</small></span>
        </button>
      </div>
    </div>
    <div v-if="!matches.length" class="muted list-empty">此范围内没有读取到战绩</div>
  </div>
</template>
