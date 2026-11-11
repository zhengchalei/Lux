<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { championMap } from '../store'

// Portrait.vue renders a locally embedded champion portrait with an explicit
// unknown state. It never downloads assets or substitutes another champion when
// the client has not disclosed an identity, preserving hidden selections.
const props = withDefaults(defineProps<{ id: number; size?: number; label?: string }>(), { size: 36, label: '' })
const failed = ref(false)
const name = computed(() => props.label || championMap.value.get(props.id)?.name || '英雄未知')
watch(() => props.id, () => { failed.value = false })
</script>

<template>
  <span class="portrait" :style="{ width: `${size}px`, height: `${size}px` }" :title="name" :aria-label="name">
    <img v-if="id > 0 && !failed" :src="`/portraits/${id}.jpg`" :alt="name" draggable="false" @error="failed = true" />
    <span v-else class="portrait-unknown">?</span>
  </span>
</template>
