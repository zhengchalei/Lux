<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import { modeNames } from '../format'
import type { Champion, Database, DraftSkins, Room, Statistics } from '../types'
import Portrait from './Portrait.vue'
import SkinTile from './SkinTile.vue'
import InfoHint from './InfoHint.vue'

// QuickDraft.vue presents personal choices, replacement-bench assignments, and client-usable
// skins in both the workspace and compact companion. Displayed selection always
// follows observed client data; a successful submission requests synchronization
// instead of inventing an optimistic assignment that another player may take.
//
// Skin reads carry a generation tied to session and current champion. Concurrent
// events coalesce into a later read, while superseded replies are discarded.
// Manual hero and skin commands share local admission; Go independently validates
// session ownership and availability before sending a single mutating request.
const props = defineProps<{ room: Room | null; database: Database; compact?: boolean; phase?: string; queueId?: number }>()
const emit = defineEmits<{ refresh: [] }>()
const search = ref('')
// Matching waits use verified queue metadata before selection supplies actual heroes.
const waitingTitle = computed(() => {
  const mode = [2400,3270].includes(props.queueId ?? 0) ? '海克斯大乱斗' : '大乱斗'
  return props.phase === 'Matchmaking' ? `正在匹配${mode}` : props.phase === 'ReadyCheck' ? `${mode}匹配成功` : '等待大乱斗选人'
})
const notice = ref('')
const error = ref('')
const busy = ref('')
const skins = ref<DraftSkins | null>(null)
const loading = ref(false)
let generation = 0
let running = false
let pending = false
let disposed = false
let adjustmentReading = false
let adjustmentPending = false
const inspected = ref(0)
const adjustmentAttempts = new Set<string>()
const catalog = computed(() => new Map(props.database.champions.map(hero => [hero.id, hero])))
const current = computed(() => props.room?.current ? heroFor(props.room.current) : undefined)
const inspectedHero = computed(() => inspected.value ? heroFor(inspected.value) : current.value)
const stats = computed(() => inspectedHero.value ? statisticsFor(inspectedHero.value) : undefined)
const personalCandidates = computed(() => [...new Set(props.room?.choices ?? [])].map(heroFor))
const candidates = computed(() => [...new Set(props.room?.bench ?? props.room?.available ?? [])].filter(id => id !== props.room?.current && !props.room?.choices?.includes(id)).map(heroFor).filter(hero => `${hero.name}${hero.title}${hero.alias}`.toLowerCase().includes(search.value.trim().toLowerCase())).sort((a, b) => {
  const first = props.room?.mode === 'mayhem' ? a.mayhem : a.aram
  const second = props.room?.mode === 'mayhem' ? b.mayhem : b.aram
  return (second.winRate ?? -1) - (first.winRate ?? -1)
}))
/** heroFor resolves display metadata without discarding an observed client choice.
 * Missing catalog entries keep their numeric identity and unavailable statistics,
 * allowing a newly introduced hero to remain selectable while metadata loads.
 */
function heroFor(id: number): Champion {
  // The public catalog can lag the live client; retain the protocol identity so
  // a missing portrait or statistic does not remove a real choice from the UI.
  return catalog.value.get(id) ?? { id, name: `英雄 ${id}`, title: '', alias: '', roles: [], aram: { tier: '', winRate: null, samples: 0, adjustments: null, source: '' }, mayhem: { tier: '', winRate: null, samples: 0, adjustments: null, source: '' } }
}
watch(() => `${props.room?.sessionKey}:${props.room?.current}`, () => {
  generation++; skins.value = null; error.value = ''; notice.value = ''; search.value = ''; inspected.value = 0
  void readSkins()
}, { immediate: true })
watch(() => props.room?.skinId, () => { notice.value = ''; void readSkins() })
watch(() => `${props.room?.mode}:${props.database.patch}:${props.room?.current}:${props.room?.available?.join(',')}:${props.room?.choices?.join(',')}:${props.room?.bench?.join(',')}`, () => { void readAdjustments() }, { immediate: true })

/** readSkins serializes availability reads and discards responses for replaced heroes.
 * Empty sessions clear skin state immediately. Pending refreshes coalesce while
 * network requests run, and unmounting prevents subsequent result publication.
 * The observed generation is captured before transport begins, so a preceding
 * champion's skin list cannot replace the next champion's empty loading state.
 * A pending demand after that transition reads the new session independently.
 */
async function readSkins(): Promise<void> {
  pending = true
  if (running || disposed) return
  running = true
  try {
    while (pending && !disposed) {
      pending = false
      const room = props.room
      const observed = generation
      if (!room?.sessionKey || !room.current || room.mode === 'ranked') { skins.value = null; continue }
      loading.value = true
      try {
        const result = await api.GetDraftSkins(room.sessionKey, room.current)
        if (!disposed && observed === generation) { skins.value = result; error.value = '' }
      } catch (reason) { if (!disposed && observed === generation) error.value = message(reason) }
      finally { if (!disposed && observed === generation) loading.value = false }
    }
  } finally { running = false }
}

/** readAdjustments fills missing Mayhem details while coalescing changing candidate sets.
 * Each patch and hero is attempted once per mounted picker. A room transition
 * during transport requests another pass over current identities, including the
 * selected hero and shared bench independently from legal selection availability.
 * Unmounting stops later requests; public replies update the shared catalog and
 * never publish a selected champion or issue a client command. Pointer previews
 * use cached observations only and cannot start additional public reads.
 */
async function readAdjustments(): Promise<void> {
  adjustmentPending = true
  if (adjustmentReading || props.room?.mode !== 'mayhem') return
  adjustmentReading = true
  try {
    while (adjustmentPending && !disposed && props.room?.mode === 'mayhem') {
      adjustmentPending = false
      for (const id of new Set([props.room.current, ...(props.room.available ?? []), ...(props.room.choices ?? []), ...(props.room.bench ?? [])])) {
        if (disposed || props.room?.mode !== 'mayhem') break
        const key = `${props.database.patch}:${id}`
        if (!id || catalog.value.get(id)?.mayhem.adjustments != null || adjustmentAttempts.has(key)) continue
        adjustmentAttempts.add(key)
        try { await api.InspectChampion(id); emit('refresh') }
        catch { /* Missing public balance details retain their unknown presentation until a later mount or patch. */ }
      }
    }
  } finally { adjustmentReading = false }
}

/** statisticsFor selects balance observations belonging to the current gameplay mode. */
function statisticsFor(hero: Champion): Statistics { return props.room?.mode === 'mayhem' ? hero.mayhem : hero.aram }

/** balanceSummary describes observed effect counts without treating missing values as neutral. */
function balanceSummary(hero: Champion): string {
  const effects = statisticsFor(hero).adjustments
  if (effects == null) return '调整待更新'
  if (!effects.length) return '无额外调整'
  const buffs = effects.filter(entry => entry.helpful).length
  return [buffs ? `↑${buffs}` : '', effects.length - buffs ? `↓${effects.length - buffs}` : ''].filter(Boolean).join(' ')
}

/** message converts rejected service commands into readable inline action feedback. */
function message(reason: unknown): string { return reason instanceof Error ? reason.message : String(reason) }

/** choose submits one explicit personal choice, hero swap, or skin selection.
 * Local admission prevents double clicks. The client remains authoritative after
 * success, and failures are shown inline without repeating a mutating request.
 */
async function choose(kind: 'hero' | 'skin', id: number): Promise<void> {
  const room = props.room
  if (!room?.sessionKey || busy.value) return
  busy.value = `${kind}:${id}`; error.value = ''; notice.value = ''
  const observed = generation
  try {
    if (kind === 'hero') await api.SelectDraftChampion(room.sessionKey, room.current, id)
    else await api.SetDraftSkin(room.sessionKey, room.current, id)
    if (observed === generation) notice.value = '已提交，等待客户端同步'
  } catch (reason) { if (observed === generation) error.value = message(reason) }
  finally { busy.value = ''; emit('refresh'); if (kind === 'skin') void readSkins() }
}

/** buffHint summarizes the selected mode's source observations for pointer hints. */
function buffHint(id: number): string {
  const hero = catalog.value.get(id)
  const data = props.room?.mode === 'mayhem' ? hero?.mayhem : hero?.aram
  return [hero?.name, data?.tier, data?.winRate == null ? '' : `胜率 ${data.winRate.toFixed(1)}%`, ...(data?.adjustments?.map(entry => `${entry.label} ${entry.display}`) ?? ['调整数据待读取'])].filter(Boolean).join(' · ')
}
// Availability changes such as a team boost arrive through bounded refreshes; hover and scrolling never invoke selection commands.
const timer = window.setInterval(() => { if (!document.hidden && props.room?.current && !busy.value) void readSkins() }, 4000)
onUnmounted(() => { disposed = true; generation++; window.clearInterval(timer) })
</script>

<template>
  <div class="quick-draft" :class="{ compact }" @mouseleave="inspected = 0">
    <template v-if="room && room.mode !== 'ranked'">
      <section class="quick-current">
        <Portrait :id="room.current" :size="compact ? 52 : 62" :label="current?.name" />
        <div class="quick-current-copy"><small>{{ modeNames[room.mode] }} · 当前英雄</small><h2>{{ current?.name || (room.canChoose ? '请选择英雄' : '等待分配') }}</h2><span v-if="current"><b v-if="statisticsFor(current).tier">{{ statisticsFor(current).tier }}</b>{{ statisticsFor(current).winRate == null ? '胜率待更新' : `${statisticsFor(current).winRate!.toFixed(1)}% 胜率` }}</span></div>
        <span class="quick-live"><i />实时</span>
      </section>
      <section v-if="room.current || personalCandidates.length || candidates.length" class="quick-balance" aria-label="英雄 Buff 与 Debuff"><div class="balance-heading"><strong>{{ inspectedHero?.name || '英雄调整' }}</strong><span>{{ inspected && inspected !== room.current ? '候选预览' : 'Buff / Debuff' }}</span><InfoHint label="查看英雄调整" text="绿色表示增益，红色表示减益。移到候选英雄上可提前查看；点击英雄仍然直接选用。调整数据按当前模式读取。" /></div><div v-if="stats?.adjustments?.length" class="quick-buffs"><span v-for="entry in stats.adjustments" :key="entry.label" :class="entry.helpful ? 'buff-up' : 'buff-down'"><b>{{ entry.helpful ? '↑' : '↓' }}</b> {{ entry.label }} {{ entry.display }}</span></div><p v-else class="balance-empty">{{ !inspectedHero ? '移到英雄上，提前查看增益与减益' : stats?.adjustments == null ? '调整数据待更新' : '无额外 Buff / Debuff' }}</p></section>
      <section v-if="personalCandidates.length || room.canChoose" class="quick-personal"><div class="quick-section-heading"><h3>我的预选 <small>{{ personalCandidates.length }}</small></h3><InfoHint label="个人预选规则" text="这是客户端分配给你的预选英雄。点击即可选用，选定后可切换皮肤；公共替补池需等待预选阶段结束。" /></div>
        <div v-if="personalCandidates.length" class="quick-heroes quick-choices"><button v-for="hero in personalCandidates" :key="hero.id" :class="{ chosen: room.current === hero.id }" :aria-pressed="room.current === hero.id" :disabled="!!busy || !room.canChoose || room.current === hero.id || !room.available?.includes(hero.id)" :title="buffHint(hero.id)" :aria-label="`预选 ${hero.name}`" @mouseenter="inspected = hero.id" @focus="inspected = hero.id" @blur="inspected = 0" @click="choose('hero', hero.id)"><Portrait :id="hero.id" :size="compact ? 46 : 54" :label="hero.name" /><strong>{{ hero.name }}</strong><small :class="room.current === hero.id ? 'text-good' : 'muted'">{{ room.current === hero.id ? '已选用' : (room.mode === 'mayhem' ? hero.mayhem : hero.aram).winRate == null ? '胜率待更新' : `${(room.mode === 'mayhem' ? hero.mayhem : hero.aram).winRate!.toFixed(1)}%` }}</small><span class="hero-balance-summary">{{ balanceSummary(hero) }}</span></button></div>
        <p v-else class="quick-empty">正在同步个人预选英雄…</p>
      </section>
      <section class="quick-pool"><div class="quick-section-heading"><h3>替补英雄 <small>{{ candidates.length }}</small></h3><InfoHint label="快速换英雄规则" text="点击即可与当前替补池交换。队友已取走的英雄不可选；不会消耗重随次数。强度与调整按当前模式展示。" /></div>
        <input v-if="(room.bench?.length ?? 0) > 7" v-model="search" class="quick-search" placeholder="搜索替补英雄" aria-label="搜索替补英雄" />
        <div v-if="candidates.length" class="quick-heroes"><button v-for="hero in candidates" :key="hero.id" :disabled="!!busy || !room.canSwap || !room.available?.includes(hero.id)" :title="room.canChoose ? '预选结束后可交换 · ' + buffHint(hero.id) : buffHint(hero.id)" :aria-label="`选用 ${hero.name}`" @mouseenter="inspected = hero.id" @focus="inspected = hero.id" @blur="inspected = 0" @click="choose('hero', hero.id)"><Portrait :id="hero.id" :size="compact ? 40 : 48" :label="hero.name" /><strong>{{ hero.name }}</strong><small :class="((room.mode === 'mayhem' ? hero.mayhem : hero.aram).winRate ?? 0) >= 50 ? 'text-good' : 'muted'">{{ (room.mode === 'mayhem' ? hero.mayhem : hero.aram).winRate?.toFixed(1) ?? '—' }}<template v-if="(room.mode === 'mayhem' ? hero.mayhem : hero.aram).winRate != null">%</template></small><span class="hero-balance-summary">{{ balanceSummary(hero) }}</span></button></div>
        <p v-else class="quick-empty">{{ search ? '没有匹配的替补英雄' : '替补池暂无英雄' }}</p>
      </section>
      <section v-if="room.current" class="quick-skins"><div class="quick-section-heading"><h3>选择皮肤 <small>{{ skins?.skins.length || 0 }}</small></h3><button class="quick-text-button" :disabled="loading || !!busy" aria-label="刷新皮肤列表" @click="readSkins">{{ loading ? '读取中' : '刷新' }}</button></div>
        <p v-if="skins && !skins.enabled" class="quick-empty">当前阶段暂不可切换皮肤</p>
        <div v-if="skins?.skins.length" class="quick-skin-grid"><SkinTile v-for="skin in skins.skins" :key="`${room.sessionKey}:${room.current}:${skin.id}`" :session-key="room.sessionKey" :champion="room.current" :skin="skin" :selected="skins.selected === skin.id" :disabled="!!busy || !skins.enabled || skins.selected === skin.id" @choose="choose('skin', $event)" /></div>
        <p v-else-if="!loading && !error" class="quick-empty">等待客户端可用皮肤</p>
      </section>
      <p v-if="busy || error || notice" class="quick-feedback" :class="{ failed: error }" role="status">{{ busy ? '正在切换…' : error || notice }}</p>
    </template>
    <div v-else class="quick-waiting"><span>✦</span><h2>{{ waitingTitle }}</h2><p>进入选人后<br />自动显示个人预选、替补英雄与可用皮肤</p></div>
  </div>
</template>
