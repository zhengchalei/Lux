<script setup lang="ts">
// EmptyState.vue presents waiting and unavailable observations consistently.
// The caller supplies the actual reason rather than filling empty spaces with
// player examples, fabricated scores, or successful-looking zero statistics.
defineProps<{ title: string; description?: string; icon?: string }>()
</script>

<template>
  <div class="empty-state"><div class="empty-icon">{{ icon || '◇' }}</div><h3>{{ title }}</h3><p v-if="description">{{ description }}</p><slot /></div>
</template>
