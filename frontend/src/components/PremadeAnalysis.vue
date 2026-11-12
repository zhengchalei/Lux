<script setup lang="ts">
import { computed } from 'vue'
import { state } from '../store'
import { observedEvidence, observedProfiles } from '../relationshipStore'
import { collectEvidence, displayParties, inferPremades, premadeThreshold, type RelationshipPlayer, type DisplayParty } from '../relationships'
import type { Match } from '../types'
import PartyChip from './PartyChip.vue'
import InfoHint from './InfoHint.vue'

// PremadeAnalysis.vue presents direct public groups and historical party suggestions.
// A shared board or scoreboard supplies consistent squad letters for every member;
// standalone callers derive suggestions from already loaded public observations.
// Badges retain their direct or inferred provenance, while revealing evidence
// never initiates history reads or infers the room's original creator.
const props = defineProps<{
  players: RelationshipPlayer[] // Public current team members.
  exclude?: number // Inspected game excluded from evidence.
  before?: number // Historical review reference timestamp.
  owner?: string // Optional private explorer history owner.
  matches?: Match[] // Optional private explorer recorded sample.
  parties?: DisplayParty[] // Shared board or scoreboard party observations.
}>()
// groups retain exact shared-game evidence rather than merging partner chains.
// The source count communicates partial coverage without adding permanent prose.
const records = computed(() => props.owner && props.matches ? collectEvidence([...observedProfiles.value, { puuid: props.owner, matches: props.matches }], state.account?.platform) : observedEvidence.value)
const groups = computed(() => props.parties || displayParties(props.players, inferPremades(records.value, props.players, props.exclude || 0, props.before || Date.now())))
const note = `近 30 天已读取的有效匹配中，所有成员至少 ${premadeThreshold} 场实际同队；不含本局、未来对局、重开和自定义。重复来源只计一次，不会把不同双排组合拼成多人组。历史同队仅提示疑似预组队。胜负为该组合所在队伍；点击记录查看数据来源玩家的原始视角。`
</script>

<template><div class="premade-analysis"><span class="premade-label">预组队</span><PartyChip v-for="group in groups" :key="group.id" :party="group" /><span v-if="!groups.length" class="premade-empty">样本内未发现</span><InfoHint label="预组队分析依据" :text="`客户端公开分组优先；其余使用历史推断并标记疑似。${note}`" /></div></template>

<style scoped>
.premade-analysis { display: inline-flex; align-items: center; gap: 6px; min-width: 0; flex-wrap: wrap; }
.premade-label, .premade-empty { font-size: 10px; color: var(--muted); white-space: nowrap; }
.premade-analysis :deep(.info-hint) { width: 18px; height: 18px; }
</style>
