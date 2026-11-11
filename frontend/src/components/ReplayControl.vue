<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import { state } from '../store'
import type { Replay } from '../types'
import InfoHint from './InfoHint.vue'

// ReplayControl.vue presents download progress and playback beside match detail tabs.
// A selected match owns a generation, so late status or command replies cannot
// replace a newer game's controls. The client remains authoritative after an
// action; submitting download never automatically opens the viewer when it ends.
//
// A serialized reader coalesces selection changes and bounded timer demand. Only
// checking, downloading, and ready states poll while the document is visible;
// terminal or failed states expose an explicit refresh instead. Pointer movement
// and tooltip display never call Go. Unmounting clears polling and revokes all
// pending publication, including a command completed after its detail panel closes.

// The selected player and game own these local controls. Read coordination stays
// within this component and does not place progress in the shared history snapshot.
const props = defineProps<{ puuid: string; gameId: number }>()
const replay = ref<Replay | null>(null)
const reading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
let generation = 0
let running = false
let pending = false
let disposed = false
let timer: number | undefined
const label = computed(() => busy.value ? '正在提交…' : error.value ? '回放读取失败' : replay.value?.progress != null ? `下载中 ${replay.value.progress}%` : replay.value?.label ?? '检查回放中')
const explanation = computed(() => error.value || notice.value || replay.value?.reason || '查询客户端回放状态；下载完成后可在此观看。')
const actionable = computed(() => !busy.value && !reading.value && (replay.value?.canDownload || replay.value?.canWatch))

/** stopTimer clears this panel's outstanding delayed status observation. */
function stopTimer(): void { window.clearTimeout(timer); timer = undefined }

/** schedule observes only active or ready replays while the detail remains visible.
 * Download progress updates at a bounded interval. Ready files use a slower
 * check so client activity can revoke playback without transferring full histories.
 */
function schedule(): void {
  stopTimer()
  if (disposed || error.value || !['checking', 'found', 'downloading', 'watch'].includes(replay.value?.state ?? '')) return
  // Ready replay files change less often than downloads; both delays avoid a
  // continuous animation loop or a backend call for ordinary pointer movement.
  timer = window.setTimeout(() => { if (!document.hidden && !busy.value) void read(); else schedule() }, replay.value?.state === 'watch' ? 5000 : 1200)
}

/** read serializes status reads and rejects observations for a superseded match.
 * Demand arriving during transport requests one later read. Selection changes
 * retain no preceding status, and unmounting prevents either success or errors
 * from entering another panel's controls. Only matching game identifiers publish.
 * A read already in flight finishes before the next demanded game starts, so
 * rapidly browsing matches does not create an unbounded group of replay checks.
 * The final active observation alone decides whether another poll is scheduled.
 */
async function read(): Promise<void> {
  pending = true
  if (running || disposed) return
  running = true
  try {
    while (pending && !disposed) {
      pending = false
      const observed = generation; const puuid = props.puuid; const id = props.gameId
      if (!puuid || !id) continue
      reading.value = true
      try {
        const result = await api.GetReplay(puuid, id)
        if (!disposed && observed === generation && result.gameId === id) { replay.value = result; error.value = '' }
      } catch (reason) { if (!disposed && observed === generation) error.value = message(reason) }
      finally { if (!disposed && observed === generation) reading.value = false }
    }
  } finally { running = false; schedule() }
}

/** message converts a rejected client operation into a hover explanation. */
function message(reason: unknown): string { return reason instanceof Error ? reason.message : String(reason) }

/** submit sends one explicit command and then rechecks authoritative client state.
 * Buttons reject overlap and never auto-launch after a download. A failed command
 * exposes its error without repeating a potentially completed download or launch.
 */
async function submit(): Promise<void> {
  if (!actionable.value) return
  const action = replay.value?.canWatch ? 'watch' : 'download'
  const observed = generation
  busy.value = true; error.value = ''; notice.value = ''; stopTimer()
  try {
    await api.ReplayAction(props.puuid, props.gameId, action)
    if (!disposed && observed === generation) {
      notice.value = action === 'watch' ? '已请求客户端打开回放' : '已请求客户端下载回放'
      await read()
    }
  } catch (reason) { if (!disposed && observed === generation) error.value = message(reason) }
  finally { if (!disposed && observed === generation) busy.value = false }
}

// Stable ownership primitives keep unrelated snapshot replacements from restarting replay reads or discarding an in-flight command's local status.
watch([() => props.puuid, () => props.gameId, () => state.connected, () => state.account?.puuid], () => {
  generation++; stopTimer(); replay.value = null; error.value = ''; notice.value = ''; busy.value = false; reading.value = false
  void read()
}, { immediate: true })
onUnmounted(() => { disposed = true; generation++; pending = false; stopTimer() })
</script>

<template>
  <span class="replay-control" :data-state="replay?.state ?? 'checking'">
    <button class="replay-action" :class="{ available: actionable }" :disabled="!actionable" :title="explanation" :aria-label="replay?.canWatch ? '观看回放' : replay?.canDownload ? '下载回放' : label" @click="submit"><span aria-hidden="true">{{ replay?.canWatch ? '▷' : '↓' }}</span>{{ label }}</button>
    <button v-if="error || (!reading && !busy && !actionable && !['checking', 'found', 'downloading'].includes(replay?.state ?? ''))" class="replay-refresh" aria-label="刷新回放状态" title="重新检查回放状态" @click="read">↻</button>
    <InfoHint label="回放说明" :text="explanation" />
    <span class="sr-only" role="status">{{ error || notice }}</span>
  </span>
</template>

<style scoped>
.replay-control{display:inline-flex;align-items:center;gap:5px;min-width:0}.replay-action{display:inline-flex;align-items:center;gap:6px;font-size:11px;border:1px solid var(--line);border-radius:6px;padding:6px 9px;background:var(--panel);color:var(--muted);white-space:nowrap;opacity:1}.replay-action.available{color:var(--blue);background:var(--blue-soft);border-color:transparent}.replay-action.available:hover{border-color:var(--blue)}.replay-action>span{font-size:14px}.replay-refresh{background:transparent;color:var(--muted);padding:4px;font-size:16px}.replay-refresh:hover{color:var(--blue)}
.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
</style>
