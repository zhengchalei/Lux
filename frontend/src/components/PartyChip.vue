<script setup lang="ts">
import type { DisplayParty } from '../relationships'
import EvidenceBadge from './EvidenceBadge.vue'

// PartyChip.vue marks every member of one displayed squad with a matching letter.
// Its evidence popover distinguishes a current client grouping from historical
// same-team inference, allowing inspection without treating repeated co-occurrence
// as a confirmed current party or issuing new requests on pointer movement.
defineProps<{
  party: DisplayParty // Shared squad membership and provenance.
  compact?: boolean // Render only the squad letter.
}>()
</script>

<template><span class="party-chip" :data-party="party.id" :data-direct="party.direct" :style="{ '--party-color': ['#248178','#6168b6','#af6d29','#a55183'][Math.max(0,party.id.charCodeAt(0)-65)%4] }"><EvidenceBadge tone="party" :label="compact ? `${party.direct ? '' : '?'}${party.id}` : `${party.direct ? '小队' : '疑似小队'} ${party.id} · ${party.members.length} 人${party.direct ? '' : ` · ${party.rows.length} 场`}`" :heading="party.members.map(member => member.name).join('、')" :rows="party.rows" :caption="party.direct ? `客户端分组 · ${party.source}` : undefined" :note="party.direct ? '当前对局公开的同组成员；编号只用于本局分组，不表示队长或建队者。' : '近 30 天、本局之前至少 3 场所有成员真实同队。相同比赛去重，不合并间接双排链条；仅为历史推断。点击查看证据对局。'" /></span></template>

<style scoped>
.party-chip { display: inline-flex; flex-shrink: 0; }
.party-chip :deep(.evidence-badge) { color: var(--party-color); background: color-mix(in srgb, var(--party-color) 12%, transparent); }
:root[data-theme=dark] .party-chip :deep(.evidence-badge) { color: color-mix(in srgb, var(--party-color) 55%, white); }
</style>
