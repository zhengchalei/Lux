<script setup lang="ts">
import { computed } from 'vue'
import type { Profile, Summary } from '../types'
import { decimal, percent } from '../format'
import { championMap } from '../store'
import Portrait from './Portrait.vue'
import InfoHint from './InfoHint.vue'

// ProfileAnalysis.vue presents backend-prepared sample statistics and provenance.
// Contribution percentages retain separate known-denominator counts, while the
// champion summary describes only this retrieved sample rather than lifetime
// mastery, ranked ability, or a forecast for the currently selected match.
const props = defineProps<{
  profile: Profile // Retrieved player sample provenance.
  summary: Summary // Current mode's prepared statistics.
  hero: number // Selected list champion filter.
}>()
const emit = defineEmits<{ hero: [id: number] }>()
// Hero and contribution projections follow the supplied summary, preserving its
// sample scope while per-metric coverage remains available through explicit hints.
const selectedHero = computed(() => props.summary.heroes?.find(hero => hero.championId === props.hero))
const contributions = computed(() => [
  { label: '伤害占比', value: props.summary.contribution.damageShare, count: props.summary.contribution.damageGames },
  { label: '承伤占比', value: props.summary.contribution.takenShare, count: props.summary.contribution.takenGames },
  { label: '经济占比', value: props.summary.contribution.goldShare, count: props.summary.contribution.goldGames },
])
</script>

<template>
  <section class="profile-analysis">
    <div class="section-heading"><h3>近期队内贡献</h3><InfoHint label="近期贡献统计说明" :text="`${profile.coverage.source || '来源待读取'} · 目标 ${profile.requested} / 读取 ${profile.coverage.fetched} / 扫描 ${profile.coverage.scanned} 场，当前范围有效 ${summary.games} 场。贡献先计算每局己方份额，再取平均；各项只使用分母完整的比赛。${profile.coverage.note || profile.historyError || ''}`" /></div>
    <div class="contribution-stats"><div v-for="entry in contributions" :key="entry.label" :title="`${entry.count} 场分母完整的有效比赛，逐局计算己方份额后取平均。`"><small>{{ entry.label }}</small><strong>{{ percent(entry.value) }}</strong></div></div>
    <div class="hero-samples"><button v-for="entry in summary.heroes?.slice(0, 6)" :key="entry.championId" class="hero-sample" :class="{ selected: hero === entry.championId }" @click="emit('hero', hero === entry.championId ? 0 : entry.championId)"><Portrait :id="entry.championId" :size="30" /><span><strong>{{ championMap.get(entry.championId)?.name || `英雄 ${entry.championId}` }}</strong><small>{{ entry.games }} 场 · {{ decimal(entry.winRate, 0) }}%</small></span></button></div>
    <div v-if="selectedHero" class="hero-inspection"><b>{{ championMap.get(hero)?.name }} · 本批样本</b><span>{{ selectedHero.games }} 场 / {{ selectedHero.wins }} 胜</span><span>{{ decimal(selectedHero.kills) }} / {{ decimal(selectedHero.deaths) }} / {{ decimal(selectedHero.assists) }}</span><strong>KDA {{ decimal(selectedHero.kda, 2) }}</strong></div>
    <span v-if="profile.historyError || profile.coverage.note" class="partial-status" :title="profile.coverage.note || profile.historyError">部分数据</span>
  </section>
</template>
