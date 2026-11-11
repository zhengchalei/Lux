<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { championMap, state } from '../store'
import type { AIReview, AISettings, TimelineEvent, TimelinePlayer, TimelineReview, TimelineStats } from '../types'
import Portrait from './Portrait.vue'
import InfoHint from './InfoHint.vue'

// TimelineReview.vue combines observed phase evidence with explicitly requested coaching.
// The client supplies discrete samples, so charts compare only known simultaneous
// values and the map draws points rather than inventing continuous movement paths.
// Economic trend labels remain separate from the model's tactical interpretation.
//
// Match and opponent changes invalidate both pending observations and model replies.
// Canceling or leaving the panel stops the admitted provider request while cached
// client facts remain reusable in Go. Settings never recover a plaintext saved key;
// replacement input is cleared after persistence. Mouse movement and local phase
// browsing never read snapshots, fetch timeline data, or invoke the model service.
const props = defineProps<{ puuid: string; gameId: number }>()
// View ownership uses separate read and analysis generations so old replies cannot replace a newly selected game's evidence or coaching.
const view = ref<TimelineReview | null>(null)
const loading = ref(false)
const error = ref('')
const opponent = ref(0)
const activeWindow = ref('')
const activeEvent = ref('')
const chartMetric = ref<'gold' | 'cs' | 'level'>('gold')
const onlyPair = ref(false)
const root = ref<HTMLElement | null>(null)
const ai = ref<AIReview | null>(null)
const aiBusy = ref(false)
const aiError = ref('')
const aiNotice = ref('')
const settings = ref<AISettings | null>(null)
const settingsOpen = ref(false)
const settingsBusy = ref(false)
const settingsError = ref('')
const formBase = ref('')
const formModel = ref('')
const formKey = ref('')
const clearKey = ref(false)
let readGeneration = 0
let analysisGeneration = 0
let activeAnalysisRequest = ''
let disposed = false
const roles: Record<string, string> = { TOP: '上路', JUNGLE: '打野', MIDDLE: '中路', BOTTOM: '下路', SUPPORT: '辅助' }
const phase = computed(() => view.value?.windows.find(item => item.id === activeWindow.value) ?? view.value?.windows[0] ?? null)
const own = computed(() => view.value?.timeline.players.find(player => player.id === view.value?.timeline.ownId))
const other = computed(() => view.value?.timeline.players.find(player => player.id === opponent.value))
const enemies = computed(() => view.value?.timeline.players.filter(player => player.team !== own.value?.team) ?? [])
const eventList = computed(() => phase.value?.events.filter(event => !onlyPair.value || involved(event, own.value?.id) || involved(event, other.value?.id)) ?? [])
const chartTitle = computed(() => ({ gold: '对位经济差', cs: '对位补刀差', level: '对位等级差' })[chartMetric.value])
const chart = computed(() => {
  const frames = view.value?.timeline.frames ?? []
  const end = Math.max(frames.at(-1)?.at ?? 1, 1)
  const points = frames.map(frame => {
    const a = frame.players.find(player => player.id === own.value?.id)?.[chartMetric.value]
    const b = frame.players.find(player => player.id === other.value?.id)?.[chartMetric.value]
    return { at: frame.at, value: a == null || b == null ? null : a - b }
  })
  // Baseline extents keep near-zero changes legible without converting missing observations into actual samples or exaggerated tactical judgments.
  const extent = Math.max(...points.map(point => Math.abs(point.value ?? 0)), chartMetric.value === 'gold' ? 500 : chartMetric.value === 'cs' ? 10 : 1)
  const segments: string[] = []
  let line: string[] = []
  for (const point of points) {
    if (point.value == null) { if (line.length) segments.push(line.join(' ')); line = []; continue }
    line.push(`${48 + point.at / end * 588},${76 - point.value / extent * 52}`)
  }
  if (line.length) segments.push(line.join(' '))
  return { end, extent, segments, startX: 48 + (phase.value?.start ?? 0) / end * 588, endX: 48 + (phase.value?.end ?? 0) / end * 588 }
})
const mapPoints = computed(() => {
  if (!view.value || !phase.value) return []
  return view.value.timeline.frames.filter(frame => frame.at >= phase.value!.start && frame.at <= phase.value!.end).flatMap(frame => frame.players.filter(player => (player.id === own.value?.id || player.id === other.value?.id) && player.x != null && player.y != null && player.x <= 16000 && player.y <= 16000).map(player => ({ id: `${frame.at}-${player.id}`, self: player.id === own.value?.id, at: frame.at, x: 10 + player.x! / 15000 * 170, y: 180 - player.y! / 15000 * 170 })))
})

/** stamp formats elapsed client milliseconds without implying a sampled exact-minute boundary. */
function stamp(milliseconds: number): string { const seconds = Math.floor(milliseconds / 1000); return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}` }
/** signed retains unavailable observations rather than displaying a guessed zero. */
function signed(value: number | null | undefined): string { return value == null ? '—' : `${value > 0 ? '+' : ''}${value.toLocaleString('zh-CN')}` }
/** gain compares observed boundaries only when both measurements are known. */
function gain(start: number | null | undefined, end: number | null | undefined): string { return start == null || end == null ? '—' : signed(end - start) }
/** hero identifies a recorded participant by champion without requiring another lookup. */
function hero(player?: TimelinePlayer): string { return player ? championMap.value.get(player.champion)?.name ?? `英雄 ${player.champion}` : '未选择' }
/** involved checks explicitly recorded event participation, without inferring objective attendance. */
function involved(event: TimelineEvent, id?: number): boolean { return !!id && (event.actor === id || event.target === id || event.assists.includes(id)) }
/** eventText labels one observed outcome without inventing movement or tactical intent. */
function eventText(event: TimelineEvent): string {
  const players = view.value?.timeline.players ?? []
  const actor = players.find(player => player.id === event.actor)
  const target = players.find(player => player.id === event.target)
  return event.kind === 'CHAMPION_KILL' ? `${actor ? hero(actor) : '环境 / 未记录来源'} 击杀 ${hero(target)}` : `${actor ? `${hero(actor)} · ` : ''}${event.label}`
}
/** metricRows prepares paired interval measurements without reinterpreting role-specific performance. */
function metricRows(start: TimelineStats, end: TimelineStats, kda: number[]) { return [{ label: '获得经济', value: gain(start.gold, end.gold) }, { label: '获得补刀', value: gain(start.cs, end.cs) }, { label: '等级变化', value: gain(start.level, end.level) }, { label: 'K / D / A', value: kda.join(' / ') }] }
/** stopAnalysis revokes local reply ownership before canceling the admitted provider request.
 * Only this panel's active analysis triggers the service call. Cached observed
 * timeline evidence remains visible while cancellation releases network resources.
 */
async function stopAnalysis(): Promise<void> {
  analysisGeneration++
  if (!aiBusy.value) return
  aiBusy.value = false
  const requestID = activeAnalysisRequest
  activeAnalysisRequest = ''
  try { await api.CancelAIAnalysis(requestID) } catch { /* The discarded reply cannot regain this panel's generation. */ }
}
/** load associates an observed comparison with the selected match and opponent.
 * Replaced requests retain no publication permission. Changing the opponent clears
 * old coaching, then reuses the backend's immutable match evidence when available.
 */
async function load(selected = 0): Promise<void> {
  const request = ++readGeneration
  await stopAnalysis()
  if (request !== readGeneration || disposed) return
  ai.value = null; aiError.value = ''; aiNotice.value = ''; error.value = ''; loading.value = true
  try {
    const result = await api.GetTimeline(props.puuid, props.gameId, selected)
    if (request !== readGeneration || disposed) return
    view.value = result; opponent.value = result.opponent
    if (!result.windows.some(window => window.id === activeWindow.value)) activeWindow.value = result.windows[0]?.id ?? ''
  } catch (reason) { if (request === readGeneration && !disposed) { view.value = null; error.value = reason instanceof Error ? reason.message : String(reason) } }
  finally { if (request === readGeneration && !disposed) loading.value = false }
}
/** saveSettings persists user-entered provider details and clears replacement secret input.
 * A blank key preserves the encrypted existing key. Failure leaves the prior
 * provider active and keeps the form open for correcting its fields.
 */
async function saveSettings(): Promise<void> {
  settingsBusy.value = true; settingsError.value = ''
  try {
    settings.value = await api.SaveAISettings({ baseUrl: formBase.value, model: formModel.value, apiKey: formKey.value, clearKey: clearKey.value })
    formKey.value = ''; clearKey.value = false; settingsOpen.value = false; aiNotice.value = 'AI 设置已保存'
  } catch (reason) { settingsError.value = reason instanceof Error ? reason.message : String(reason) }
  finally { settingsBusy.value = false }
}
/** analyze explicitly sends the selected comparison to the configured compatible provider.
 * Settings absence opens configuration before any request. The reply must still
 * belong to this exact match and opponent when its evidence-linked result arrives.
 */
async function analyze(): Promise<void> {
  if (!view.value || !opponent.value || aiBusy.value) return
  if (!settings.value?.baseUrl || !settings.value.model) { settingsOpen.value = true; aiNotice.value = '请先填写接口地址和模型名称'; return }
  const request = ++analysisGeneration
  const requestID = Array.from(crypto.getRandomValues(new Uint32Array(4)), value => value.toString(16)).join('-')
  activeAnalysisRequest = requestID
  aiBusy.value = true; aiError.value = ''; aiNotice.value = ''
  try {
    const result = await api.AnalyzeTimeline(props.puuid, props.gameId, opponent.value, requestID)
    if (request === analysisGeneration && !disposed) ai.value = result
  } catch (reason) { if (request === analysisGeneration && !disposed) aiError.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if (request === analysisGeneration && !disposed) { aiBusy.value = false; activeAnalysisRequest = '' } }
}
/** showEvidence selects its actual phase before revealing an observed cited event. */
async function showEvidence(id: string, windowId: string): Promise<void> {
  activeWindow.value = windowId; activeEvent.value = id; onlyPair.value = false
  await nextTick()
  const element = id.startsWith('E') ? root.value?.querySelector(`[data-evidence="${id}"]`) : root.value?.querySelector('.review-phase')
  element?.scrollIntoView({ block: 'nearest', behavior: 'instant' })
}
// Compare primitive ownership sources separately so an unrelated snapshot's new account object cannot reload this match or cancel active coaching.
watch([() => props.puuid, () => props.gameId, () => state.account?.puuid], () => { view.value = null; activeWindow.value = ''; void load() }, { immediate: true })
onMounted(async () => {
  try {
    const result = await api.GetAISettings()
    if (disposed) return
    settings.value = result; formBase.value = result.baseUrl; formModel.value = result.model
  } catch (reason) { if (!disposed) settingsError.value = reason instanceof Error ? reason.message : String(reason) }
})
onBeforeUnmount(() => { disposed = true; readGeneration++; void stopAnalysis(); formKey.value = '' })
</script>

<template>
  <div ref="root" class="timeline-review">
    <div class="review-actions">
      <label>对比选手<select v-model.number="opponent" aria-label="选择对比选手" :disabled="loading || !view" @change="load(opponent)"><option :value="0" disabled>请选择敌方选手</option><option v-for="player in enemies" :key="player.id" :value="player.id">{{ hero(player) }}{{ player.role ? ` · ${roles[player.role] || player.role}` : '' }}</option></select></label>
      <InfoHint label="时间线数据说明" :text="`来自客户端的真实采样，通常约每 ${view?.timeline.sampleSeconds || 60} 秒一帧。阶段边界使用实际采样时间；位置点不代表连续轨迹。颜色仅表示对位经济变化，AI 判断另附依据。位置不明或大乱斗时，请手动选择对比对象。`" />
      <span class="review-action-spacer" /><button class="button" :aria-expanded="settingsOpen" @click="settingsOpen = !settingsOpen">AI 设置</button>
      <button v-if="aiBusy" class="button" @click="stopAnalysis(); aiNotice = '分析已取消'">取消分析</button><button v-else class="button primary" :disabled="loading || !opponent || !view" :title="settings?.model || '先配置自定义兼容接口'" @click="analyze()">{{ ai ? '重新分析' : 'AI 分析本局' }}</button>
    </div>
    <form v-if="settingsOpen" class="review-settings" aria-label="AI 服务设置" @submit.prevent="saveSettings">
      <div class="review-section-heading"><h3>自定义 OpenAI 兼容接口</h3><InfoHint label="AI 数据与密钥说明" text="仅在点击 AI 分析时向你配置的服务发送本局英雄、位置采样、阶段统计及事件，不发送玩家名称、PUUID、客户端令牌或回放文件。API Key 使用 Windows 当前用户加密保存。" /></div>
      <label>接口地址<input v-model="formBase" aria-label="AI 接口地址" placeholder="https://你的服务地址/v1" autocomplete="off" required /></label><label>模型名称<input v-model="formModel" aria-label="AI 模型名称" placeholder="服务提供的模型 ID" autocomplete="off" required /></label><label>API Key<input v-model="formKey" type="password" aria-label="AI API Key" :placeholder="settings?.hasKey ? '已保存，留空保留原密钥' : '需要鉴权时填写'" autocomplete="new-password" /></label>
      <div class="review-settings-footer"><label v-if="settings?.hasKey" class="review-clear-key"><input v-model="clearKey" type="checkbox" />清除已保存密钥</label><span v-if="settings?.note">{{ settings.note }}</span><button type="submit" class="button primary" :disabled="settingsBusy || aiBusy">{{ settingsBusy ? '保存中…' : '保存设置' }}</button></div><p v-if="settingsError" class="inline-note error" role="alert">{{ settingsError }}</p>
    </form>
    <p v-if="error" class="inline-note error" role="alert">{{ error }} <button class="text-button" @click="load(opponent)">重试</button></p>
    <p v-if="loading" class="inline-note" role="status">正在读取本局真实时间线…</p>
    <template v-if="view && phase">
      <section class="review-chart"><div class="review-section-heading"><h3>{{ chartTitle }}</h3><div class="review-chart-metrics" role="group" aria-label="时间线比较指标"><button v-for="entry in ([['gold', '经济'], ['cs', '补刀'], ['level', '等级']] as const)" :key="entry[0]" :aria-pressed="chartMetric === entry[0]" @click="chartMetric = entry[0]">{{ entry[1] }}</button></div><small>正值为{{ hero(own) }}领先</small></div>
        <svg v-if="opponent" viewBox="0 0 660 155" role="img" :aria-label="`${chartTitle}，按真实时间线采样`"><rect :x="chart.startX" y="15" :width="Math.max(chart.endX - chart.startX, 1)" height="119" class="review-chart-phase" /><line x1="48" y1="76" x2="636" y2="76" class="review-zero" /><line x1="48" y1="24" x2="636" y2="24" class="review-grid" /><line x1="48" y1="128" x2="636" y2="128" class="review-grid" /><text x="0" y="28">+{{ chart.extent }}</text><text x="25" y="80">0</text><text x="0" y="132">−{{ chart.extent }}</text><polyline v-for="(segment, index) in chart.segments" :key="index" :points="segment" class="review-curve" /><text x="48" y="150">0:00</text><text x="636" y="150" text-anchor="end">{{ stamp(chart.end) }}</text></svg><p v-else class="review-unpaired">请选择敌方选手，查看同一时刻的差距与行为。</p>
      </section>
      <div class="review-phase-list" role="group" aria-label="复盘时间段"><button v-for="window in view.windows" :key="window.id" :class="[window.tone, { selected: phase.id === window.id }]" :aria-pressed="phase.id === window.id" @click="activeWindow = window.id; activeEvent = ''"><strong>{{ stamp(window.start) }}–{{ stamp(window.end) }}</strong><small>{{ window.goldTrend == null ? '经济差未知' : `经济差变化 ${signed(window.goldTrend)}` }}</small></button></div>
      <section class="review-phase"><div class="review-section-heading"><h3>{{ stamp(phase.start) }}–{{ stamp(phase.end) }}</h3><span>阶段末经济差 <b>{{ signed(phase.goldDiff) }}</b></span><span>补刀差 <b>{{ signed(phase.csDiff) }}</b></span></div>
        <div class="review-paired-stats"><div v-for="entry in [{ player: own, start: phase.ownStart, end: phase.ownEnd, kda: phase.ownKda, self: true }, { player: other, start: phase.otherStart, end: phase.otherEnd, kda: phase.otherKda, self: false }]" :key="String(entry.self)" class="review-player-stats" :class="{ self: entry.self }"><div class="review-player-heading"><Portrait :id="entry.player?.champion || 0" :size="32" /><strong>{{ hero(entry.player) }}<small>{{ entry.self ? '当前查看玩家' : '对比选手' }}{{ entry.player?.role ? ` · ${roles[entry.player.role] || entry.player.role}` : '' }}</small></strong></div><div class="review-player-metrics"><div v-for="metric in metricRows(entry.start, entry.end, entry.kda)" :key="metric.label"><small>{{ metric.label }}</small><b>{{ entry.player ? metric.value : '—' }}</b></div></div></div></div>
        <div class="review-evidence-layout"><div class="review-events"><div class="review-section-heading"><h4>关键事件 <small>{{ eventList.length }}</small></h4><label class="review-pair-filter"><input v-model="onlyPair" type="checkbox" />仅看双方参与</label></div><ol v-if="eventList.length"><li v-for="event in eventList" :key="event.id" :data-evidence="event.id" :class="{ highlighted: activeEvent === event.id, own: involved(event, own?.id), opposing: involved(event, other?.id) }"><time>{{ stamp(event.at) }}</time><span>{{ eventText(event) }}<small v-if="involved(event, own?.id)">本人参与</small><small v-if="involved(event, other?.id)">对方参与</small></span><b>{{ event.id }}</b></li></ol><p v-else class="review-empty-events">这段时间没有符合筛选的已记录关键事件。</p></div>
          <figure v-if="view.timeline.mapId === 11" class="review-map"><figcaption>位置采样<InfoHint label="位置采样说明" text="概略地图显示本阶段的实际位置采样点，蓝色为当前玩家，红色为对比选手。每个点可悬停查看时间。没有连续轨迹、视野或兵线信息，无法仅据此确定走位与支援决策。" /></figcaption><svg viewBox="0 0 190 190" role="img" aria-label="本阶段双方离散位置采样"><path d="M12 178V12H178M12 178H178V12M12 178L178 12" class="review-map-lanes" /><circle v-for="point in mapPoints" :key="point.id" :cx="point.x" :cy="point.y" r="4.5" :class="point.self ? 'own' : 'opposing'"><title>{{ point.self ? '当前玩家' : '对比选手' }} · {{ stamp(point.at) }}</title></circle></svg><div><i class="own" />{{ hero(own) }}<i class="opposing" />{{ hero(other) }}</div></figure>
        </div>
      </section>
    </template>
    <div v-if="aiBusy" class="review-ai-progress" role="status"><span class="review-pulse" />AI 正在结合双方时间线复盘…<small>可继续浏览阶段和事件</small></div><p v-if="aiError" class="inline-note error" role="alert">{{ aiError }}</p><p v-if="aiNotice" class="inline-note" role="status">{{ aiNotice }}</p>
    <section v-if="ai" class="review-ai-result" aria-label="AI 复盘结果"><div class="review-section-heading"><h3>AI 复盘</h3><span>{{ ai.model }}</span><InfoHint label="AI 复盘依据说明" text="以下为模型基于本局已记录数据的解释与条件性建议。每条分析附有可点击的阶段或事件引用；引用校验只能确认依据存在，不能保证模型的战术判断正确。" /></div><p class="review-ai-summary">{{ ai.summary }}</p><div class="review-ai-insights"><article v-for="(finding, index) in ai.insights" :key="index" :class="finding.verdict"><div class="review-insight-heading"><span>{{ ({ good: '做得好', review: '值得回看', neutral: '证据有限' })[finding.verdict] }}</span><button class="text-button" @click="showEvidence(finding.windowId, finding.windowId)">{{ (() => { const w = view?.windows.find(w => w.id === finding.windowId); return w ? `${stamp(w.start)}–${stamp(w.end)}` : finding.windowId })() }}</button></div><h4>{{ finding.title }}</h4><p class="review-opponent-note">对方：{{ finding.opponent }}</p><details><summary>查看依据与改进建议</summary><p>{{ finding.reason }}</p><p><b>下次怎么做</b>{{ finding.advice }}</p></details><div class="review-citations"><button v-for="id in finding.evidenceIds" :key="id" :aria-label="`查看依据 ${id}`" @click="showEvidence(id, finding.windowId)">{{ id.startsWith('W') ? '阶段统计' : `事件 ${id}` }}</button></div></article></div><div v-if="ai.goals.length" class="review-practice"><h4>下一局重点练习</h4><ol><li v-for="goal in ai.goals" :key="goal">{{ goal }}</li></ol></div></section>
  </div>
</template>
