<script setup lang="ts">
// AppIcon.vue supplies the local outline icons used by navigation and board
// actions. Every icon inherits text color and stays decorative because its
// enclosing button provides the accessible action name, without loading an
// external font or introducing another rendering dependency.
defineProps<{
  name: string // Local icon vocabulary key.
  size?: number // Optional pixel edge length.
}>()

// paths keeps the application's navigation and actions within one consistent
// stroke vocabulary; unknown names render the same unobtrusive square outline.
const paths: Record<string, string[]> = {
  history: ['M4 4h16v16H4z', 'M8 15v-4m4 4V8m4 7v-6'],
  battle: ['M4 4l6 2 8 10-2 2L6 10z', 'M14 6l4-2 2 2-2 4M4 20l4-4m8 0 4 4'],
  draft: ['M12 3l8 4v6c0 4-8 8-8 8s-8-4-8-8V7z', 'M8 12l3 3 5-6'],
  query: ['M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13z', 'M16 16l5 5'],
  automation: ['M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z', 'M9 3h6l1 3 3 1 2 5-2 5-3 1-1 3H9l-1-3-3-1-2-5 2-5 3-1z'],
  refresh: ['M20 8a8 8 0 0 0-14-3L3 8m0-5v5h5', 'M4 16a8 8 0 0 0 14 3l3-3m0 5v-5h-5'],
  close: ['M6 6l12 12M18 6 6 18'],
  arrow: ['M5 12h14m-6-6 6 6-6 6'],
  back: ['M19 12H5m6-6-6 6 6 6'],
  sun: ['M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z', 'M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1 1m12 12 1 1M5 19l1-1M18 6l1-1'],
  moon: ['M20 14A8 8 0 0 1 10 4a8 8 0 1 0 10 10z'],
  info: ['M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z', 'M12 11v6m0-10v.1'],
  user: ['M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z', 'M4 21v-3a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v3'],
  chevron: ['M8 10l4 4 4-4'],
}
</script>

<template>
  <svg :width="size || 20" :height="size || 20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path v-for="(path, index) in paths[name] || ['M4 4h16v16H4z']" :key="index" :d="path" /></svg>
</template>
