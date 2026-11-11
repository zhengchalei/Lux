<script setup lang="ts">
import { computed } from 'vue'
import type { Combatant, Match, Mode, Scout } from '../types'
import { date, decimal, matchLabel, percent, scopedSummary } from '../format'
import { championMap } from '../store'
import Portrait from './Portrait.vue'
import AppIcon from './AppIcon.vue'

// ScoutCard.vue presents one player's identity, historical sample, and recent
// matches in a fixed vertical reading order. The same scope selects every
// aggregate and recent row, so changing the board's mode never mixes an overall
// win rate with a different mode's match outcomes. Unknown identities and missing
// samples retain explicit empty states rather than acquiring invented numbers.
//
// Player and match inspection emit stable player identities to the board, which
// owns the detail drawer. Each bounded recent list scrolls independently while
// both teams' identities remain visible. Native titles provide secondary facts
// without mouse listeners, backend calls, or pointer-driven reactive updates.
const props = defineProps<{
  player: Combatant // Publicly observed player slot.
  scout?: Scout // Retained player retrieval state.
  mode: Mode // Current battle gameplay mode.
  sameMode: boolean // Board sample scope preference.
  lens: 'form' | 'contribution' // Renderer-local secondary metric choice.
}>()
const emit = defineEmits<{ inspect: [puuid: string, match?: Match]; retry: [puuid: string] }>()
// Prepared observations retain the backend's sample and missing-value semantics;
// computed projections change only when this player's retained inputs change.
const profile = computed(() => props.scout?.profile)
const summary = computed(() => profile.value ? scopedSummary(profile.value, props.sameMode ? props.mode : 'all') : null)
const recent = computed(() => summary.value?.recent?.slice(0, 10) ?? [])
const recentWins = computed(() => recent.value.filter(match => match.win).length)
const queryable = computed(() => !!props.player.puuid && !props.player.hidden)
const identity = computed(() => {
  const name = profile.value?.name || props.player.name || (props.player.hidden ? '身份未公开' : '等待玩家信息')
  const separator = name.lastIndexOf('#')
  return { name: separator > 0 ? name.slice(0, separator) : name, tag: separator > 0 ? name.slice(separator) : '' }
})
const currentHero = computed(() => summary.value?.heroes?.find(hero => hero.championId === props.player.champion))
const contributions = computed(() => [
  { label: '伤害', value: summary.value?.contribution.damageShare, count: summary.value?.contribution.damageGames },
  { label: '承伤', value: summary.value?.contribution.takenShare, count: summary.value?.contribution.takenGames },
  { label: '经济', value: summary.value?.contribution.goldShare, count: summary.value?.contribution.goldGames },
])
const currentTitle = computed(() => {
  const hero = championMap.value.get(props.player.champion)
  const stats = props.mode === 'mayhem' ? hero?.mayhem : props.mode === 'aram' ? hero?.aram : null
  return [hero?.name || '英雄尚未公开', props.player.position, stats?.source,
    ...(stats?.adjustments?.map(value => `${value.label} ${value.display}`) ?? [])].filter(Boolean).join(' · ')
})
const status = computed(() => props.player.hidden ? '客户端尚未公开该玩家身份' : props.scout?.error || (props.scout?.loading ? '正在读取近期战绩…' : '暂未读取到可用战绩'))
</script>

<template>
  <article class="player-card" :class="{ 'is-loading': scout?.loading }" :aria-label="`${identity.name}的近期表现`">
    <header class="player-identity">
      <span class="current-champion" :title="currentTitle"><Portrait :id="player.champion" :size="36" /><i v-if="player.locked" class="champion-locked" title="英雄已锁定" /></span>
      <div class="identity-copy"><button class="identity-name" :disabled="!queryable" :title="profile?.name || player.name" @click="emit('inspect', player.puuid)">{{ identity.name }}</button><div class="identity-meta"><span class="identity-rank" :title="profile?.rank">{{ profile?.rank || (player.hidden ? '身份未公开' : '段位待读取') }}</span><span v-if="identity.tag" class="riot-tag">{{ identity.tag }}</span></div></div>
      <i v-if="scout?.loading" class="player-updating" title="正在更新战绩" />
    </header>
    <div class="player-metrics">
      <div class="winrate-metric"><strong :class="summary?.games ? (summary.winRate >= 50 ? 'text-good' : 'text-bad') : ''">{{ summary?.games ? decimal(summary.winRate, 0) : '—' }}<small v-if="summary?.games">%</small></strong><span>近期胜率 <small v-if="summary?.games">· {{ summary.games }} 场</small></span></div>
      <div class="kda-metric" title="场均击杀 / 死亡 / 助攻"><strong><template v-if="summary?.games">{{ decimal(summary.kills) }}<i>/</i><em>{{ decimal(summary.deaths) }}</em><i>/</i>{{ decimal(summary.assists) }}</template><template v-else>— / — / —</template></strong><span class="metric-label-wide">场均 K / D / A</span><span class="metric-label-short" aria-hidden="true">场均</span></div>
    </div>
    <div v-if="lens === 'form'" class="player-lens champion-sample" :title="'当前英雄在已读取、相同统计范围内的表现，不代表历史熟练度'">
      <span>当前英雄</span><template v-if="currentHero"><b>{{ currentHero.games }} 场</b><span>·</span><b>{{ decimal(currentHero.winRate, 0) }}% 胜率</b></template><span v-else class="lens-empty">{{ player.champion ? '样本内暂无记录' : '等待选择' }}</span>
    </div>
    <div v-else class="player-lens contribution-lens"><span v-for="entry in contributions" :key="entry.label" :title="`${entry.count || 0} 场分母已知的有效比赛，逐局计算队内份额后取平均`">{{ entry.label }} <b>{{ percent(entry.value ?? null) }}</b></span></div>
    <div class="recent-heading"><span>最近 {{ recent.length || 10 }} 场</span><span v-if="recent.length" class="recent-count"><b class="text-good">{{ recentWins }} 胜</b><i>/</i><b class="text-bad">{{ recent.length - recentWins }} 负</b></span><div v-if="recent.length" class="outcome-sequence" title="从左到右：由新到旧"><i v-for="match in recent" :key="match.id" :class="match.win ? 'win' : 'loss'" /></div></div>
    <div class="player-recent scroll-region" :aria-label="`${identity.name}的最近战绩`" tabindex="0">
      <button v-for="match in recent" :key="match.id" class="player-match" :class="match.win ? 'win' : 'loss'" :aria-label="matchLabel(match)" :title="`${match.queue} · ${new Date(match.created).toLocaleString('zh-CN')} · 点击查看单局`" @click="emit('inspect', player.puuid, match)">
        <Portrait :id="match.championId" :size="23" /><span class="match-outcome">{{ match.win ? '胜' : '负' }}</span><time>{{ date(match.created) }}</time><span class="match-kda">{{ match.kills }}<i>/</i>{{ match.deaths }}<i>/</i>{{ match.assists }}</span>
      </button>
      <div v-if="!recent.length" class="player-empty"><span>{{ status }}</span><button v-if="scout?.error && queryable" class="button" @click="emit('retry', player.puuid)">重新读取</button></div>
    </div>
    <footer class="player-footer"><div class="player-common"><span>常用</span><span v-for="hero in summary?.heroes?.slice(0, 3)" :key="hero.championId" :title="`${championMap.get(hero.championId)?.name || '英雄'} · 样本 ${hero.games} 场 · 胜率 ${decimal(hero.winRate, 0)}%`"><Portrait :id="hero.championId" :size="19" /></span><span v-if="!summary?.heroes?.length">—</span></div><button :disabled="!queryable || !profile" class="player-detail" :aria-label="`查看${identity.name}详情`" :title="`有效 ${summary?.games || 0} 场 / 已读取 ${profile?.coverage.fetched || 0} 场`" @click="emit('inspect', player.puuid)">详情<AppIcon name="arrow" :size="13" /></button></footer>
  </article>
</template>
