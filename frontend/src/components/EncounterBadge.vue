<script setup lang="ts">
import { computed } from 'vue'
import { state } from '../store'
import { personalEncounters } from '../relationshipStore'
import { recentEvidence } from '../relationships'
import EvidenceBadge from './EvidenceBadge.vue'

// EncounterBadge.vue marks a public player who appears in the current account's
// loaded recent history. It excludes the inspected game and future observations,
// then supplies exact teammate/opponent evidence to an actionable local popover.
// An absent badge means no qualifying observation in this sample, not proof that
// the two accounts have never met.
const props = defineProps<{
  puuid: string // Target public account identifier.
  exclude?: number // Current inspected match identifier.
  before?: number // Historical reference instant in milliseconds.
  compact?: boolean // Narrow battle cards shorten the label.
}>()
// Encounter rows rebuild only for changed identities, context, or retained history;
// pointer events reveal their existing array without reading the client again.
const rows = computed(() => recentEvidence(personalEncounters.value.get(props.puuid) || [], props.exclude || 0, props.before || Date.now()))
const note = computed(() => `基于你已读取的 ${state.history?.matches.length || 0} 场战绩，检查本局之前 30 天；不含本局、重开和自定义。胜负及 K/D/A 为你的视角。点击记录查看对局。`)
</script>

<template><EvidenceBadge v-if="rows.length" :label="`${compact ? '遇过' : '遇到过'} ${rows.length}`" heading="与你的近期交手" :rows="rows" :note="note" /></template>
