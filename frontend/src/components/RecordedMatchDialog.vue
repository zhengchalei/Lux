<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { recordedMatch } from '../matchNavigation'
import AppIcon from './AppIcon.vue'
import MatchDetails from './MatchDetails.vue'

// RecordedMatchDialog.vue opens relationship evidence above its originating board
// or player page. The selected source owner remains attached to the match, while
// native dialog focus and Escape behavior restore the underlying workflow after
// dismissal. Participant links can continue into the independent player explorer.
const dialog = ref<HTMLDialogElement>()
onMounted(() => dialog.value?.showModal())
/** close releases the selected evidence and dismisses its native detail surface. */
function close(): void { dialog.value?.close(); recordedMatch.value = null }
/** backdrop dismisses only clicks outside the dialog's inner detail surface. */
function backdrop(event: MouseEvent): void { if (event.target === dialog.value) close() }
</script>

<template><dialog ref="dialog" class="recorded-match-dialog" aria-label="历史对局详情" @click="backdrop" @close="recordedMatch = null"><section><header><strong>历史对局详情</strong><span>{{ recordedMatch?.label }}</span><button class="icon-button" aria-label="关闭历史对局详情" autofocus @click="close"><AppIcon name="close" :size="18" /></button></header><div v-if="recordedMatch" class="recorded-match-body scroll-region"><MatchDetails :key="`${recordedMatch.owner}:${recordedMatch.match.id}`" :puuid="recordedMatch.owner" :match="recordedMatch.match" /></div></section></dialog></template>

<style scoped>
.recorded-match-dialog { width: min(1050px, calc(100vw - 40px)); height: min(820px, calc(100dvh - 40px)); max-width: none; max-height: none; margin: auto; padding: 0; border: 1px solid var(--line); border-radius: 12px; color: var(--ink); background: var(--surface); box-shadow: 0 14px 70px #08142450; }
.recorded-match-dialog::backdrop { background: #0c172954; }
.recorded-match-dialog > section { display: flex; flex-direction: column; height: 100%; }
.recorded-match-dialog header { display: flex; align-items: center; gap: 14px; padding: 9px 20px; border-bottom: 1px solid var(--line); }
.recorded-match-dialog header strong { font-size: 13px; font-weight: 500; }
.recorded-match-dialog header span { font-size: 11px; color: var(--muted); }
.recorded-match-dialog header button { margin-left: auto; }
.recorded-match-body { flex: 1; min-height: 0; overflow-y: auto; padding: 18px 22px; }
</style>
