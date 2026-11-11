<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { DraftSkin } from '../types'
import Portrait from './Portrait.vue'

// SkinTile.vue loads one client-provided thumbnail when its appearance card enters
// the viewport. Component keys include session and champion identity, preventing
// late artwork from being reused after a replacement-bench swap changes heroes.
const props = defineProps<{ sessionKey: string; champion: number; skin: DraftSkin; selected: boolean; disabled: boolean }>()
const emit = defineEmits<{ choose: [id: number] }>()
const tile = ref<HTMLElement>()
const image = ref('')
let observer: IntersectionObserver | undefined
let disposed = false
let attempted = false

/** load reads artwork once and keeps a local portrait when unavailable.
 * Unmounting suppresses late publication. Failures do not start an image retry
 * loop, so scrolling back over an unavailable preview creates no bridge traffic.
 */
async function load(): Promise<void> {
  if (attempted) return
  attempted = true
  try { const url = await api.GetSkinPreview(props.sessionKey, props.champion, props.skin.id); if (!disposed) image.value = url }
  catch { /* An unavailable thumbnail retains the explicit champion portrait fallback. */ }
}
onMounted(() => {
  observer = new IntersectionObserver(entries => { if (entries.some(entry => entry.isIntersecting)) { observer?.disconnect(); void load() } })
  if (tile.value) observer.observe(tile.value)
})
onUnmounted(() => { disposed = true; observer?.disconnect() })
</script>

<template>
  <button ref="tile" class="skin-tile" :class="{ chosen: selected }" :disabled="disabled" :title="skin.name" :aria-label="`使用皮肤 ${skin.name}`" :aria-pressed="selected" @click="emit('choose', skin.id)">
    <span class="skin-art"><img v-if="image" :src="image" alt="" draggable="false" /><Portrait v-else :id="champion" :size="38" /><span v-if="selected" class="skin-check">✓</span></span>
    <span class="skin-name">{{ skin.base ? '原始皮肤' : skin.name }}</span>
  </button>
</template>
