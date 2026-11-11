<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { api } from '../api'
import { championMap, state } from '../store'
import { decimal, readTime } from '../format'
import { matchesHistoryFilter, historyKinds, type HistoryFilter } from '../historyFilters'
import { participantRequest, type ParticipantRequest } from '../playerExplorer'
import type { Match, Profile } from '../types'
import AppIcon from './AppIcon.vue'
import Portrait from './Portrait.vue'
import SummonerIcon from './SummonerIcon.vue'
import InfoHint from './InfoHint.vue'
import HistoryFilters from './HistoryFilters.vue'
import VirtualMatchList from './VirtualMatchList.vue'
import MatchDetails from './MatchDetails.vue'

// PlayerExplorer.vue presents a recorded participant's overview and recent games
// above the originating workspace. The identity header, ranked card, champion
// samples, and history summary share one retrieved profile. Type filters select
// prepared statistics, while champion and outcome choices narrow the record list.
// The main query, originating match, and underlying scroll positions stay mounted.
//
// One modal owns a short back trail and one backend explorer key. Each navigation
// invalidates earlier renderer responses; the backend independently cancels older
// reads and validates recorded membership. Dismissal releases private histories,
// and account changes dismiss before a different account can inherit the page.
// Returning restores the actual profile, filters, selected match, and list offset
// rather than issuing another name lookup. Virtual rows bound the history DOM.

/** PlayerVisit retains one prior player page and its local browsing position. */
interface PlayerVisit {
  profile: Profile // Retrieved player sample observation.
  request: ParticipantRequest // Exact source for retrying navigation.
  filter: HistoryFilter // Locally retained list intersections.
  selected: Match | null // Optional inspected recorded match.
  scroll: number // History list offset in pixels.
}

// Explorer state belongs to this modal. Its key separates cleanup from any new
// modal opened before an old asynchronous release reaches the backend service.
const key = crypto.randomUUID()
const dialog = ref<HTMLDialogElement>()
const body = ref<HTMLElement>()
const profile = shallowRef<Profile | null>(null)
const request = shallowRef<ParticipantRequest>(participantRequest.value!)
const trail = shallowRef<PlayerVisit[]>([])
const selected = shallowRef<Match | null>(null)
const filter = ref<HistoryFilter>({ kind: 'all', champion: 0, outcome: 'all' })
const loading = ref(false)
const error = ref('')
let generation = 0
let listOffset = 0
const summary = computed(() => filter.value.kind === 'all' ? profile.value?.summary : profile.value?.summaries[`history:${filter.value.kind}`])
const matches = computed(() => (profile.value?.matches || []).filter(match => matchesHistoryFilter(match, filter.value)))
const scope = computed(() => historyKinds.find(kind => kind.id === filter.value.kind)!.label)
const recent = computed(() => matches.value.slice(0, 10))
const identity = computed(() => {
  const name = profile.value?.name || request.value.name || '召唤师'
  const split = name.lastIndexOf('#')
  return { name: split > 0 ? name.slice(0, split) : name, tag: split > 0 ? name.slice(split) : '' }
})
const coverage = computed(() => profile.value ? `${profile.value.coverage.source} · 读取 ${profile.value.matches.length} / 目标 ${profile.value.requested} 场。当前类型 ${summary.value?.games || 0} 场有效比赛；英雄与胜负仅筛选列表。自定义、重开和无效竞技记录不计入统计。${profile.value.coverage.note || profile.value.historyError || ''}` : '')

/** capture retains the current player's navigation state before another profile replaces it.
 * Only loaded pages enter the trail. The last five pages plus the current page
 * mirror the backend's six-profile limit, preventing invisible histories from growing indefinitely.
 */
function capture(): void {
  if (!profile.value) return
  const scroll = body.value?.querySelector('.match-list')?.scrollTop ?? listOffset
  trail.value = [...trail.value.slice(-4), { profile: profile.value, request: request.value, filter: { ...filter.value }, selected: selected.value, scroll }]
}

/** load retrieves one exact recorded participant and publishes only the current navigation.
 * A new target preserves the preceding loaded page when requested. Missing or
 * failed histories remain an error page with retry and back actions; the previous
 * player's statistics never appear under the new target's name.
 *
 * The request generation handles quick clicks, back navigation, and dismissal
 * independently from the backend cancellation generation. Profile identity must
 * match the clicked PUUID before any data is displayed. Histories stay outside
 * the global store so opening a player cannot replace the main search result,
 * selection, or another battle card's ongoing collection.
 */
async function load(target: ParticipantRequest, retain = true): Promise<void> {
  if (profile.value?.puuid === target.puuid && !loading.value) { overview(); return }
  if (retain) capture()
  const observed = ++generation
  request.value = target
  profile.value = null
  selected.value = null
  filter.value = { kind: 'all', champion: 0, outcome: 'all' }
  listOffset = 0
  loading.value = true
  error.value = ''
  try {
    const result = await api.GetParticipantProfile(key, target.owner, target.match, target.puuid, trail.value.map(visit => visit.profile.puuid))
    if (observed !== generation) return
    if (result.puuid !== target.puuid) throw Error('返回的玩家身份不一致，请重试')
    profile.value = result
  } catch (reason) {
    if (observed === generation) error.value = reason instanceof Error ? reason.message : String(reason)
  } finally { if (observed === generation) loading.value = false }
}

/** restoreList restores the retained history offset after its virtual viewport renders. */
async function restoreList(): Promise<void> {
  await nextTick()
  const list = body.value?.querySelector<HTMLElement>('.match-list')
  if (list) list.scrollTop = listOffset
}
/** back restores the previous player page without replacing the underlying workspace.
 * At the start of the trail it dismisses the explorer. Pending responses are
 * invalidated before the retained profile and its local selection become visible.
 */
function back(): void {
  const visit = trail.value.at(-1)
  if (!visit) { close(); return }
  generation++
  trail.value = trail.value.slice(0, -1)
  profile.value = visit.profile
  request.value = visit.request
  filter.value = visit.filter
  selected.value = visit.selected
  listOffset = visit.scroll
  loading.value = false
  error.value = ''
  void restoreList()
}
/** inspect opens one recorded perspective and retains its preceding list offset. */
function inspect(match: Match): void {
  listOffset = body.value?.querySelector('.match-list')?.scrollTop || 0
  selected.value = match
}
/** overview returns from a selected match to this player's retained history list. */
function overview(): void { selected.value = null; void restoreList() }
/** close dismisses the native modal and clears the shell's current navigation intent. */
function close(): void { dialog.value?.close(); participantRequest.value = null }
/** backdrop dismisses only clicks on the modal backdrop outside its inner surface. */
function backdrop(event: MouseEvent): void { if (event.target === dialog.value) close() }

watch(participantRequest, target => { if (target) void load(target) }, { immediate: true })
watch(() => `${state.account?.puuid}|${state.account?.region}|${state.account?.platform}`, close)
onMounted(() => dialog.value?.showModal())
onUnmounted(() => {
  generation++
  // The view no longer has an error surface after dismissal. Backend release is
  // best-effort here; account cleanup and the next explorer also revoke old ownership.
  void api.CloseParticipantExplorer(key).catch(() => {})
})
</script>

<template>
  <dialog ref="dialog" class="player-explorer" aria-labelledby="player-explorer-title" @close="participantRequest = null" @click="backdrop">
    <section class="player-explorer-surface">
      <header class="player-explorer-nav"><button class="player-back" @click="back"><AppIcon name="back" :size="16" />{{ trail.length ? '返回上一位玩家' : '返回来源对局' }}</button><span>玩家战绩</span><button class="icon-button" aria-label="关闭玩家战绩" autofocus @click="close"><AppIcon name="close" :size="18" /></button></header>
      <header class="player-profile-heading"><div class="player-profile-avatar"><SummonerIcon v-if="profile" :key="profile.puuid" :icon="profile.iconId" :ready="profile.iconId >= 0" /><AppIcon v-else name="user" :size="28" /><small v-if="profile?.level">{{ profile.level }}</small></div><div class="player-profile-identity"><h2 id="player-explorer-title">{{ identity.name }}<small>{{ identity.tag }}</small></h2><span>{{ loading ? '正在读取玩家资料…' : profile ? '当前大区 · 近期对局' : '玩家资料暂不可用' }}</span></div><div v-if="profile" class="player-profile-capture"><span v-if="profile.coverage.note || profile.historyError" class="partial-status">部分数据</span><small>{{ readTime(profile.captured) }} 读取</small><InfoHint label="玩家统计范围与来源" :text="coverage" /></div></header>
      <div v-if="loading || error" class="player-profile-state" :aria-busy="loading"><AppIcon :name="error ? 'info' : 'user'" :size="30" /><strong>{{ error ? '暂时无法读取这位玩家' : '正在读取战绩与常用英雄' }}</strong><p v-if="error" role="alert">{{ error }}</p><button v-if="error" class="button" @click="load(request, false)">重新读取</button></div>
      <div v-else-if="profile && summary" ref="body" class="player-explorer-body" :class="{ 'is-detail': selected }">
        <template v-if="selected"><div class="player-match-nav"><button class="player-back" @click="overview"><AppIcon name="back" :size="16" />返回 {{ identity.name }} 的概况</button><span>单局详情</span></div><div class="player-match-detail scroll-region"><MatchDetails :match="selected" :puuid="profile.puuid" :summoner="profile.name" :history="profile.matches" /></div></template>
        <template v-else>
          <aside class="player-profile-sidebar scroll-region">
            <section class="player-rank-card"><span class="player-card-label">单排 / 双排</span><strong>{{ profile.rank || '段位暂不可用' }}</strong><small>当前段位</small></section>
            <section class="player-champion-card"><div class="player-card-title"><h3>近期常用英雄</h3><InfoHint label="常用英雄样本说明" :text="`${scope}的 ${summary.games} 场有效比赛，按使用场数排序。点击英雄可筛选右侧战绩。`" /></div><button v-for="hero in summary.heroes.slice(0, 5)" :key="hero.championId" class="player-champion-row" :class="{ selected: filter.champion === hero.championId }" :aria-label="`查看常用英雄 ${championMap.get(hero.championId)?.name || hero.championId} 的战绩`" @click="filter = { ...filter, champion: filter.champion === hero.championId ? 0 : hero.championId }"><Portrait :id="hero.championId" :size="33" /><span><strong>{{ championMap.get(hero.championId)?.name || `英雄 ${hero.championId}` }}</strong><small>{{ decimal(hero.kda, 2) }} KDA · {{ hero.games }} 场</small></span><b :class="hero.winRate >= 50 ? 'text-good' : 'text-bad'">{{ decimal(hero.winRate, 0) }}%</b></button><p v-if="!summary.heroes.length" class="player-hero-empty">当前类型暂无有效英雄样本</p></section>
            <div class="player-sample-label"><span>{{ scope }}</span><span>{{ summary.games }} 场有效样本</span></div>
          </aside>
          <main class="player-profile-main">
            <section class="player-recent-summary"><div class="player-win-ring" :style="{ '--win-angle': `${summary.winRate * 3.6}deg` }" :class="{ empty: !summary.games }"><strong>{{ summary.games ? `${decimal(summary.winRate, 0)}%` : '—' }}</strong></div><div class="player-recent-result"><strong>{{ summary.wins }} 胜 <i>{{ summary.games - summary.wins }} 负</i></strong><small>{{ scope }} · {{ summary.games }} 场</small></div><div class="player-average"><strong>{{ summary.games ? decimal(summary.kills) : '—' }} <i>/</i> <em>{{ summary.games ? decimal(summary.deaths) : '—' }}</em> <i>/</i> {{ summary.games ? decimal(summary.assists) : '—' }}</strong><small>场均 K / D / A <b>{{ summary.games ? decimal(summary.kda, 2) : '—' }} KDA</b></small></div><InfoHint label="玩家胜率与场均统计说明" text="顶部胜率、场均 K/D/A 和常用英雄跟随比赛类型，均使用有效竞技样本。英雄与胜负筛选只影响下方记录；近期样本不代表赛季全部战绩。" /></section>
            <div class="player-history-filters"><HistoryFilters v-model="filter" :matches="profile.matches" :owner="profile.puuid" /></div>
            <div class="player-records-heading"><h3>近期战绩 <small aria-live="polite">{{ matches.length }} 场</small></h3><div class="player-record-strip" aria-label="列表最近十条结果"><span v-for="match in recent" :key="match.id" :class="match.remake ? 'remake' : match.win ? 'win' : 'loss'" :title="`${match.name} · ${match.queue} · ${match.remake ? '重开' : match.win ? '胜利' : '失败'}`">{{ match.remake ? '重' : match.win ? '胜' : '负' }}</span></div></div>
            <VirtualMatchList v-if="matches.length" :matches="matches" :summoner="profile?.name" :selected="0" show-metrics @select="inspect" />
            <div v-else class="player-no-matches"><strong>没有符合条件的对局</strong><button class="button" @click="filter = { kind: 'all', champion: 0, outcome: 'all' }">重置筛选</button></div>
          </main>
        </template>
      </div>
    </section>
  </dialog>
</template>

<style scoped>
.player-explorer { width: min(1120px, calc(100vw - 48px)); height: min(800px, calc(100dvh - 48px)); max-width: none; max-height: none; border: 1px solid var(--line); border-radius: 12px; padding: 0; margin: auto; color: var(--ink); background: var(--canvas); overflow: hidden; box-shadow: 0 14px 70px #08142450; }
.player-explorer::backdrop { background: #0c172954; }
.player-explorer-surface { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.player-explorer-nav { display: flex; align-items: center; gap: 16px; padding: 9px 18px; border-bottom: 1px solid var(--line); background: var(--surface); flex-shrink: 0; }
.player-explorer-nav > span { color: var(--muted); font-size: 11px; margin-left: auto; }
.player-explorer-nav .icon-button { width: 30px; height: 30px; margin-left: 3px; }
.player-back { display: inline-flex; align-items: center; gap: 7px; padding: 5px 0; background: transparent; color: var(--muted); font-size: 12px; }
.player-back:hover { color: var(--blue); }
.player-profile-heading { display: flex; align-items: center; gap: 16px; padding: 21px 24px; background: var(--surface); flex-shrink: 0; }
.player-profile-avatar { display: grid; place-items: center; position: relative; width: 60px; height: 60px; flex: 0 0 60px; border-radius: 12px; background: var(--blue-soft); color: var(--blue); }
.player-profile-avatar :deep(.summoner-icon) { width: 60px; height: 60px; border-radius: 12px; }
.player-profile-avatar > small { position: absolute; bottom: -6px; left: 50%; transform: translateX(-50%); padding: 1px 8px; border-radius: 5px; background: var(--ink); color: var(--surface); font-size: 10px; line-height: 16px; }
.player-profile-identity { min-width: 0; flex: 1; }
.player-profile-identity h2 { font-size: 21px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.player-profile-identity h2 small { color: var(--muted); font-size: 14px; margin-left: 7px; font-weight: 400; }
.player-profile-identity > span { display: block; color: var(--muted); font-size: 11px; margin-top: 9px; }
.player-profile-capture { display: flex; align-items: center; gap: 8px; color: var(--muted); }
.player-profile-capture > small { font-size: 10px; }
.player-profile-state { display: flex; flex: 1; align-items: center; justify-content: center; flex-direction: column; gap: 16px; color: var(--muted); font-size: 13px; padding: 30px; }
.player-profile-state strong { font-weight: 500; color: var(--ink); font-size: 15px; }
.player-profile-state p { max-width: 540px; overflow-wrap: anywhere; }
.player-explorer-body { display: grid; grid-template-columns: 224px minmax(0, 1fr); gap: 14px; flex: 1; min-height: 0; padding: 16px; }
.player-profile-sidebar { display: flex; flex-direction: column; gap: 12px; min-width: 0; min-height: 0; overflow-y: auto; }
.player-rank-card, .player-champion-card { background: var(--surface); border: 1px solid var(--line); border-radius: 8px; }
.player-rank-card { padding: 17px; }
.player-card-label { color: var(--muted); font-size: 11px; }
.player-rank-card > strong { display: block; font-size: 17px; font-weight: 600; margin: 16px 0 9px; }
.player-rank-card > small { color: var(--muted); font-size: 10px; }
.player-card-title { display: flex; align-items: center; justify-content: space-between; padding: 9px 13px; border-bottom: 1px solid var(--line); }
.player-card-title h3 { font-size: 12px; font-weight: 500; }
.player-champion-row { display: flex; align-items: center; width: 100%; gap: 9px; padding: 11px 12px; background: transparent; text-align: left; border-bottom: 1px solid var(--line); }
.player-champion-row:last-child { border-bottom: 0; }
.player-champion-row:hover, .player-champion-row.selected { background: var(--blue-soft); }
.player-champion-row > span:not(.portrait) { flex: 1; min-width: 0; }
.player-champion-row strong { display: block; font-size: 12px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.player-champion-row small { display: block; font-size: 10px; color: var(--muted); margin-top: 5px; }
.player-champion-row > b { font-size: 12px; font-weight: 500; }
.player-hero-empty { font-size: 11px; padding: 20px 12px; text-align: center; color: var(--muted); }
.player-sample-label { display: flex; justify-content: space-between; padding: 0 4px; color: var(--muted); font-size: 10px; }
.player-profile-main { display: flex; flex-direction: column; min-width: 0; min-height: 0; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); overflow: hidden; }
.player-recent-summary { display: flex; align-items: center; gap: 14px; padding: 16px 18px; flex-shrink: 0; }
.player-win-ring { position: relative; display: grid; place-items: center; width: 66px; height: 66px; flex: 0 0 66px; border-radius: 50%; background: conic-gradient(var(--good) 0deg var(--win-angle), var(--loss) var(--win-angle) 360deg); }
.player-win-ring::before { content: ''; position: absolute; inset: 5px; background: var(--surface); border-radius: 50%; }
.player-win-ring.empty { background: var(--line); }
.player-win-ring strong { position: relative; font-size: 17px; font-weight: 600; }
.player-recent-result strong { font-size: 14px; font-weight: 500; white-space: nowrap; }
.player-recent-result i { color: var(--muted); margin-left: 7px; }
.player-recent-result small, .player-average small { display: block; color: var(--muted); font-size: 10px; margin-top: 8px; }
.player-average { margin-left: auto; text-align: right; }
.player-average > strong { font-size: 20px; font-weight: 500; white-space: nowrap; }
.player-average i { color: var(--muted); font-size: 14px; margin: 0 3px; }
.player-average em { color: var(--bad); font-style: normal; }
.player-average small b { color: var(--ink); font-weight: 500; margin-left: 8px; }
.player-history-filters { padding: 9px 12px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
.player-history-filters :deep(.filter-chips) { gap: 4px; }
.player-history-filters :deep(.history-filter-bar) { gap: 7px; }
.player-records-heading { display: flex; align-items: center; gap: 12px; justify-content: space-between; padding: 11px 14px 7px; flex-shrink: 0; }
.player-records-heading h3 { font-size: 12px; font-weight: 500; white-space: nowrap; }
.player-records-heading small { color: var(--muted); margin-left: 5px; font-size: 10px; font-weight: 400; }
.player-record-strip { display: flex; gap: 3px; }
.player-record-strip > span { display: grid; place-items: center; width: 18px; height: 18px; border-radius: 3px; font-size: 10px; }
.player-record-strip .win { color: var(--good); background: var(--win); }
.player-record-strip .loss { color: var(--bad); background: var(--loss); }
.player-record-strip .remake { color: var(--muted); background: var(--panel); }
.player-profile-main > :deep(.match-list) { flex: 1; min-height: 0; padding: 5px 9px 0; }
.player-profile-main :deep(.match-row) { padding-left: 16px; padding-right: 17px; }
.player-profile-main :deep(.record-copy) { flex: 1; }
.player-profile-main :deep(.record-copy strong) { font-size: 14px; }
.player-profile-main :deep(.record-extra) { display: flex; flex: 0 0 116px; flex-direction: column; align-items: flex-end; margin-left: 8px; gap: 7px; }
.player-profile-main :deep(.record-extra strong) { font-size: 13px; font-weight: 500; }
.player-profile-main :deep(.record-extra small) { color: var(--muted); font-size: 10px; }
.player-profile-main :deep(.record-performance) { flex: 0 0 105px; margin-left: 20px; }
.player-no-matches { display: flex; flex: 1; align-items: center; justify-content: center; flex-direction: column; gap: 15px; color: var(--muted); font-size: 12px; }
.player-no-matches > strong { font-weight: 400; }
.player-explorer-body.is-detail { display: flex; flex-direction: column; gap: 0; padding: 0 20px 20px; background: var(--surface); }
.player-match-nav { display: flex; align-items: center; justify-content: space-between; border-top: 1px solid var(--line); padding: 10px 0; flex-shrink: 0; }
.player-match-nav > span { color: var(--muted); font-size: 11px; }
.player-match-detail { flex: 1; min-height: 0; overflow-y: auto; padding: 4px; }
@media (max-width: 1150px) { .player-explorer { width: calc(100vw - 32px); height: calc(100dvh - 32px); } .player-explorer-body { grid-template-columns: 210px minmax(0, 1fr); gap: 12px; padding: 13px; } .player-recent-summary { padding: 13px; gap: 11px; } }
@media (max-height: 760px) { .player-profile-heading { padding-top: 15px; padding-bottom: 15px; } .player-profile-avatar, .player-profile-avatar :deep(.summoner-icon) { width: 50px; height: 50px; } .player-profile-avatar { flex-basis: 50px; } .player-champion-row { padding-top: 9px; padding-bottom: 9px; } .player-rank-card { padding: 13px 15px; } .player-rank-card > strong { margin-top: 11px; } }
</style>
