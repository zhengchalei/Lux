<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { connectStore, state } from './store'
import type { Page } from './types'
import HistoryView from './views/HistoryView.vue'
import BattleView from './views/BattleView.vue'
import DraftView from './views/DraftView.vue'
import AutomationView from './views/AutomationView.vue'
import SettingsView from './views/SettingsView.vue'
import AppIcon from './components/AppIcon.vue'
import InfoHint from './components/InfoHint.vue'

// App.vue arranges persistent navigation around independently updating workspaces.
// Backend lifecycle observation belongs to the store; pointer hover and page
// navigation remain local Vue state. The shell does not rebuild match records,
// perform combat aggregation, or call Go during mouse movement and scrolling.
const startupPage = new URLSearchParams(location.search).get('page')
const page = ref<Page>(['history', 'draft', 'battle', 'query', 'automation', 'settings'].includes(startupPage || '') ? startupPage as Page : 'history')
const dark = ref(localStorage.getItem('lux-theme') === 'dark')
const navigation = [
  { id: 'history', label: '战绩分析', icon: 'history', subtitle: '回顾对局表现，理解每一场比赛' },
  { id: 'draft', label: '选人助手', icon: 'draft', subtitle: '阵容、英雄强度与对线参考' },
  { id: 'battle', label: '对战助手', icon: 'battle', subtitle: '阵容 → 近期表现 → 单局分析' },
  { id: 'query', label: '战绩查询', icon: 'query', subtitle: '查看召唤师近期表现与英雄样本' },
  { id: 'automation', label: '自动选禁', icon: 'automation', subtitle: '管理选用、禁用顺序与执行规则' },
  { id: 'settings', label: '设置', icon: 'automation', subtitle: '后台运行与自动接受对局' },
] as const
const heading = computed(() => navigation.find(entry => entry.id === page.value)!)
const phaseLabel = computed(() => ({ None: '客户端空闲', Lobby: '房间中', Matchmaking: '正在匹配', ReadyCheck: '等待确认', ChampSelect: '英雄选择', InProgress: '正在对局', Reconnect: '等待重连', WaitingForStats: '正在结算', EndOfGame: '对局结束' })[state.phase] || state.phase || '等待客户端')
const patchLabel = computed(() => state.patch.match(/^\d+\.\d+/)?.[0] || state.patch.slice(0, 16))
let disconnect: (() => void) | undefined
watch(dark, value => { document.documentElement.dataset.theme = value ? 'dark' : 'light'; localStorage.setItem('lux-theme', value ? 'dark' : 'light') }, { immediate: true })
watch(() => state.phase, (phase, previous) => {
  // A new real champion-select phase opens its assistant once; periodic snapshots do not repeatedly steal the user's chosen workspace.
  if (phase === 'ChampSelect' && previous !== phase) page.value = 'draft'
})
onMounted(() => { disconnect = connectStore() })
onUnmounted(() => disconnect?.())
</script>

<template>
  <div class="app-shell"><aside class="sidebar"><div class="brand"><span class="brand-symbol">L</span><strong>LUX<span>英雄联盟 · 对局助手</span></strong></div><div class="nav-caption">工作台</div><nav><button v-for="entry in navigation" :key="entry.id" :aria-label="entry.label" :class="{ selected: page === entry.id }" :aria-current="page === entry.id ? 'page' : undefined" @click="page = entry.id"><span class="nav-icon"><AppIcon :name="entry.icon" /></span><span>{{ entry.label }}</span><i v-if="entry.id === 'automation' && state.automationEnabled" class="status-dot" /></button></nav><div class="sidebar-bottom"><div class="renderer-note"><i />自动连接 · 实时同步</div><button class="theme-toggle" :aria-label="dark ? '切换浅色' : '切换深色'" @click="dark = !dark"><AppIcon :name="dark ? 'sun' : 'moon'" :size="18" />{{ dark ? '切换浅色' : '切换深色' }}</button><small>LUX 0.2 · Windows</small></div></aside>
    <div class="app-main"><header class="app-header"><div><h1 :title="heading.subtitle">{{ heading.label }}</h1></div><div class="header-account"><div v-if="state.account"><strong>{{ state.account.name }}</strong><small>{{ state.account.region }}{{ state.account.platform ? ` / ${state.account.platform}` : '' }} · Lv. {{ state.account.level }}</small></div><span class="connection-state" :class="{ connected: state.connected }" :title="state.bridgeError || state.status"><i />{{ state.connected ? '客户端已连接' : '自动连接中' }}</span></div></header>
      <main class="workspace"><div v-if="state.bridgeError" class="service-error" role="status">{{ state.bridgeError }} · 正在自动恢复</div><HistoryView v-if="page === 'history'" :key="'history'" /><HistoryView v-else-if="page === 'query'" :key="'query'" query /><DraftView v-else-if="page === 'draft'" /><BattleView v-else-if="page === 'battle'" @query="page = 'query'" /><AutomationView v-else-if="page === 'automation'" /><SettingsView v-else /></main>
      <footer class="app-footer"><span>{{ phaseLabel }}<InfoHint label="客户端同步状态" :text="state.status || '自动检测客户端并在断线后重连。'" /></span><span class="footer-patch">{{ patchLabel ? `版本 ${patchLabel}` : '数据更新中' }}<InfoHint label="强度数据更新状态" :text="`${state.database.sourceStatus || '强度数据等待自动更新'}${state.patch ? ` · ${state.patch}` : ''}`" /></span></footer></div></div>
</template>
