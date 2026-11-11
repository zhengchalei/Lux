<script setup lang="ts">
import { onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { api } from '../api'
import { state } from '../store'
import type { DraftPanelSnapshot, DraftPanelStatus } from '../types'
import QuickDraft from '../components/QuickDraft.vue'

// DraftCompanion.vue renders the independent client-side selection companion.
// Its service boundary excludes match histories, and native Go code owns client
// movement. Frontend observation uses serialized revision checks and events;
// dragging, hover effects, and skin scrolling do not transmit window positions.
//
// Each accepted observation replaces the compact model as one value, preventing
// a hero from being paired with another session's pool. Closing the component
// revokes late publication and removes its subscriptions and timer. Theme state
// is read locally when the window opens, and explicit pin controls affect the
// native window controller rather than starting selection automation.
const snapshot = shallowRef<DraftPanelSnapshot>({ revision: -1, connected: false, phase: '', queueId: 0, room: null, database: state.database })
const status = ref<DraftPanelStatus>({ enabled: true, pinned: true, visible: false, attached: false, collapsed: false })
const error = ref('')
let disposed = false
let running = false
let pending = false
let unsubscribe: (() => void) | undefined

/** refresh coalesces companion observations while preserving service errors inline.
 * Only the compact DTO crosses the bridge. Disposed windows ignore late replies,
 * and the catalog mirror supplies existing local portrait accessibility labels.
 */
async function refresh(): Promise<void> {
  pending = true
  if (running || disposed) return
  running = true
  try {
    while (pending && !disposed) {
      pending = false
      try {
        const [incoming, panel] = await Promise.all([api.GetDraftSnapshot(), api.GetDraftPanelStatus()])
        if (disposed) return
        snapshot.value = incoming; status.value = panel; state.database = incoming.database; error.value = ''
      } catch (reason) { if (!disposed) error.value = reason instanceof Error ? reason.message : String(reason) }
    }
  } finally { running = false }
}
/** collapse changes the companion between full controls and its client-edge tab. */
async function collapse(value: boolean): Promise<void> {
  try { await api.SetDraftPanelCollapsed(value); await refresh() }
  catch (reason) { error.value = reason instanceof Error ? reason.message : String(reason) }
}

/** configure applies explicit companion controls and observes their resulting preferences. */
async function configure(enabled: boolean, pinned: boolean): Promise<void> {
  try { await api.SetDraftPanel(enabled, pinned); await refresh() }
  catch (reason) { error.value = reason instanceof Error ? reason.message : String(reason) }
}
// Recovery reads only a scalar revision until changed data needs another compact snapshot, keeping idle companion transfers bounded.
const timer = window.setInterval(async () => {
  if (running || disposed || document.hidden) return
  try { const [revision, panel] = await Promise.all([api.GetRevision(), api.GetDraftPanelStatus()]); if (disposed) return; status.value = panel; if (revision !== snapshot.value.revision) await refresh() }
  catch { /* The next recovery check retries observation without issuing any selection command. */ }
}, 1200)
onMounted(() => {
  document.documentElement.dataset.theme = localStorage.getItem('lux-theme') || 'light'
  document.title = 'Lux · 大乱斗选人'
  document.body.classList.add('companion-body')
  unsubscribe = window.__luxSubscribe?.(() => { void refresh() })
  void refresh()
})
onUnmounted(() => { disposed = true; unsubscribe?.(); window.clearInterval(timer) })
</script>

<template>
  <button v-if="status.collapsed" class="companion-edge-tab" title="展开大乱斗助手" aria-label="展开大乱斗助手" @click="collapse(false)"><span class="companion-brand">L</span><strong>大乱斗助手</strong><span>‹</span></button>
  <div v-else class="draft-companion"><header class="companion-titlebar"><span class="companion-brand">L</span><strong>大乱斗选人</strong><span class="companion-mode">{{ snapshot.room?.mode === 'mayhem' || [2400,3270].includes(snapshot.queueId) ? '海克斯' : 'ARAM' }}</span><div class="companion-controls"><button v-if="status.pinned" title="收起为侧边标签" aria-label="收起面板" @click="collapse(true)">›</button><button :class="{ pinned: status.pinned }" :title="status.pinned ? '取消贴边，可自由拖动' : '贴在客户端右侧'" :aria-label="status.pinned ? '取消贴边' : '贴边客户端'" @click="configure(true, !status.pinned)">⌖</button><button title="关闭本轮面板，下次大乱斗匹配自动显示" aria-label="关闭面板" @click="configure(false, status.pinned)">×</button></div></header>
    <main class="companion-content scroll-region"><p v-if="error" class="quick-feedback failed" role="status">{{ error }}</p><QuickDraft :room="snapshot.room" :database="snapshot.database" :phase="snapshot.phase" :queue-id="snapshot.queueId" compact @refresh="refresh" /></main>
    <footer class="companion-footer"><i :class="{ connected: snapshot.connected }" />{{ status.pinned ? '跟随客户端' : '自由窗口' }}<span>{{ snapshot.connected ? '已连接' : '自动连接中' }}</span></footer>
  </div>
</template>
