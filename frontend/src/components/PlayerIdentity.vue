<script setup lang="ts">
import { computed } from 'vue'
import type { Player } from '../types'
import { openParticipant } from '../playerExplorer'
import EncounterBadge from './EncounterBadge.vue'
import PartyChip from './PartyChip.vue'
import type { DisplayParty } from '../relationships'

// PlayerIdentity.vue renders a recorded Riot ID as a keyboard-accessible profile
// action when the source provides a public account identifier. A missing identity
// remains plain text, and the displayed alias never substitutes for the identifier
// used to retrieve history, preserving distinct accounts with identical names.

const props = withDefaults(defineProps<{
  player: Player // Recorded participant account observations.
  owner: string // Source match perspective owner.
  match: number // Source recorded match identifier.
  before?: number // Source match creation reference timestamp.
  encounters?: boolean // Compact rankings may omit encounter badges.
  party?: DisplayParty // Shared scoreboard squad membership.
}>(), { encounters: true })
// Name and tag split only for typography; the full recorded string remains the
// accessible action name and hover label for both duplicate and truncated aliases.
const identity = computed(() => {
  const name = props.player.name || '召唤师'
  const split = name.lastIndexOf('#')
  return { name: split > 0 ? name.slice(0, split) : name, tag: split > 0 ? name.slice(split) : '' }
})
</script>

<template>
  <span class="recorded-identity"><button v-if="player.puuid" type="button" class="player-id-link" :aria-label="`查看 ${player.name} 的战绩`" :title="`${player.name} · 查看玩家概况与战绩`" @click="openParticipant(owner, match, player)"><span>{{ identity.name }}<small>{{ identity.tag }}</small></span></button><span v-else class="player-id-unavailable" :title="`${player.name || '召唤师'} · 来源未提供可查询的玩家身份`">{{ player.name || '身份未知' }}</span><PartyChip v-if="party" compact :party="party" /><EncounterBadge v-if="encounters !== false && player.puuid" :puuid="player.puuid" :exclude="match" :before="before" /></span>
</template>

<style scoped>
.recorded-identity { display: flex; align-items: center; gap: 5px; min-width: 0; }
.player-id-link, .player-id-unavailable { display: block; min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; font-weight: 400; }
.player-id-link { flex: 1; background: transparent; text-align: left; padding: 5px 0; border-radius: 3px; color: var(--ink); }
.player-id-link > span { display: block; overflow: hidden; text-overflow: ellipsis; }
.player-id-link small { color: var(--muted); font-size: 10px; margin-left: 4px; }
.player-id-link:hover, .player-id-link:focus-visible, .player-id-link:hover small { color: var(--blue); }
.player-id-link:hover > span { text-decoration: underline; text-underline-offset: 3px; }
.player-id-unavailable { color: var(--muted); }
</style>
