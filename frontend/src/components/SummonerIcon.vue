<script setup lang="ts">
import { ref, watch } from 'vue'
import { api } from '../api'
import { state } from '../store'
import AppIcon from './AppIcon.vue'

// SummonerIcon.vue reads one authenticated profile asset when observed metadata changes.
// A generation check discards replies for replaced candidates or accounts. Failed
// assets retain a neutral user symbol; pointer movement never starts another request.
const props = defineProps<{ icon: number; ready: boolean }>()
const source = ref('')
watch([() => props.icon, () => props.ready, () => state.account?.puuid], async ([icon, ready], _, onCleanup) => {
  let current = true
  onCleanup(() => { current = false })
  source.value = ''
  if (!ready) return
  try {
    const result = await api.GetSummonerIcon(icon)
    if (current) source.value = result
  } catch { /* Missing client assets retain the neutral account symbol. */ }
}, { immediate: true })
</script>

<template>
  <span class="summoner-icon" aria-hidden="true">
    <img v-if="source" :src="source" alt="" draggable="false" @error="source = ''" />
    <AppIcon v-else name="user" :size="24" />
  </span>
</template>
