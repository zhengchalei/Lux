<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { Champion } from '../types'
import Portrait from './Portrait.vue'

// VirtualHeroGrid.vue renders only the visible rows of the automation catalog.
// Fixed rows and responsive column counts keep search and scrolling local to
// the renderer. Adding a hero emits an explicit preference edit; merely hovering
// or scrolling the catalog never invokes a backend service or client action.
const props = defineProps<{ heroes: Champion[]; picks: number[]; bans: number[] }>()
const emit = defineEmits<{ add: [id: number, ban: boolean] }>()
const viewport = ref<HTMLElement>()
const top = ref(0)
const height = ref(380)
const width = ref(600)
// Catalog geometry matches typography.css's fixed row height. Wider cells reserve
// room for readable hero labels and both actions; short grids retain two columns.
const rowHeight = 120
const columnWidth = 140
const columns = computed(() => Math.max(2, Math.floor(width.value / columnWidth)))
const rows = computed(() => Math.ceil(props.heroes.length / columns.value))
const first = computed(() => Math.max(0, Math.floor(top.value / rowHeight) - 1))
const visible = computed(() => props.heroes.slice(first.value * columns.value, (first.value + Math.ceil(height.value / rowHeight) + 2) * columns.value))
let observer: ResizeObserver | undefined
watch(() => props.heroes, () => { if (viewport.value) viewport.value.scrollTop = 0; top.value = 0 })
onMounted(() => {
  observer = new ResizeObserver(entries => { const rect = entries[0]?.contentRect; if (rect) { height.value = rect.height; width.value = rect.width } })
  if (viewport.value) observer.observe(viewport.value)
})
onUnmounted(() => observer?.disconnect())
</script>

<template>
  <div ref="viewport" class="virtual-hero-grid scroll-region" @scroll.passive="top = ($event.target as HTMLElement).scrollTop"><div :style="{ height: `${rows * rowHeight}px`, position: 'relative' }"><div class="hero-grid-window" :style="{ gridTemplateColumns: `repeat(${columns}, 1fr)`, transform: `translateY(${first * rowHeight}px)` }"><div v-for="hero in visible" :key="hero.id" class="catalog-hero"><Portrait :id="hero.id" :size="40" /><strong>{{ hero.name }}</strong><div><button :disabled="picks.includes(hero.id)" :title="`添加${hero.name}到选用顺序`" @click="emit('add', hero.id, false)">{{ picks.includes(hero.id) ? '已选' : '+ 选' }}</button><button :disabled="bans.includes(hero.id)" :title="`添加${hero.name}到禁用顺序`" @click="emit('add', hero.id, true)">{{ bans.includes(hero.id) ? '已禁' : '+ 禁' }}</button></div></div></div></div></div>
</template>
