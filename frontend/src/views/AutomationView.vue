<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { action, championMap, defaultAutomation, state } from '../store'
import type { AutomationSettings } from '../types'
import Portrait from '../components/Portrait.vue'
import VirtualHeroGrid from '../components/VirtualHeroGrid.vue'

// AutomationView.vue edits champion priorities separately from runtime permission.
// Draft preferences stay local until explicitly saved, and the runtime toggle
// uses the service's saved rules. Launching the application does not enable
// automation. Each rule control changes preferences rather than directly issuing
// a client pick, ban, lock, room, or matchmaking command.
//
// Incoming snapshots preserve unsaved edits. Saving clears the local dirty flag
// only after the service acknowledges the operation, so a persistence failure
// remains visible alongside the user's intended priority order and protection
// settings. Stopping is always available while automation is active.
const draft = ref<AutomationSettings>({ ...defaultAutomation, picks: [], bans: [] })
const dirty = ref(false)
const search = ref('')
const notice = ref('')
const busy = ref(false)
const heroes = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return state.database.champions.filter(hero => !needle || `${hero.name}${hero.title}${hero.alias}`.toLowerCase().includes(needle))
})
watch(() => state.automation, settings => { if (!dirty.value) draft.value = { ...settings, picks: [...(settings.picks ?? [])], bans: [...(settings.bans ?? [])] } }, { immediate: true })
const rules = [
  { key: 'autoPick', title: '自动选用', description: '按优先顺序选择可用英雄' },
  { key: 'autoBan', title: '自动禁用', description: '仅在本人禁用回合执行' },
  { key: 'preselect', title: '提前预选', description: '规划阶段显示偏好，不提前锁定' },
  { key: 'lockPick', title: '自动锁定', description: '本人选用回合确认当前选择' },
  { key: 'protectAllies', title: '保护队友预选', description: '不禁用队友想玩的英雄' },
  { key: 'preserveManual', title: '保留手动选择', description: '手动选定后不被规则覆盖' },
] as const

/** add appends one champion to the chosen local preference order. */
function add(id: number, ban: boolean): void { const list = ban ? draft.value.bans : draft.value.picks; if (!list.includes(id)) list.push(id); dirty.value = true }
/** remove deletes one champion preference without altering another list's order. */
function remove(index: number, ban: boolean): void { (ban ? draft.value.bans : draft.value.picks).splice(index, 1); dirty.value = true }
/** promote moves one candidate earlier in its explicit fallback preference order. */
function promote(index: number, ban: boolean): void { const list = ban ? draft.value.bans : draft.value.picks; if (index > 0) { const value = list[index]!; list.splice(index, 1); list.splice(index - 1, 0, value); dirty.value = true } }
/** save acknowledges persisted rules before allowing snapshots to replace local edits. */
async function save(): Promise<void> {
  busy.value = true
  const error = await action(() => api.SaveAutomation({ ...draft.value, picks: [...draft.value.picks], bans: [...draft.value.bans] }, state.automationEnabled))
  notice.value = error || '规则已保存并应用'
  if (!error) dirty.value = false
  busy.value = false
}
/** toggle changes runtime permission using saved rules, preserving unsaved local edits. */
async function toggle(): Promise<void> {
  busy.value = true
  notice.value = await action(() => state.automationEnabled ? api.StopAutomation() : api.SaveAutomation(state.automation, true))
  busy.value = false
}
</script>

<template>
  <div class="automation-workspace scroll-region">
    <section class="card automation-master"><div><div class="eyebrow">运行控制</div><h2>自动选禁总开关 <span :class="state.automationEnabled ? 'text-good' : 'muted'">{{ state.automationEnabled ? '运行中' : '已关闭' }}</span></h2><p>每次启动默认关闭 · 随机大乱斗不执行选禁</p></div><button class="switch" :class="{ enabled: state.automationEnabled }" role="switch" :aria-checked="state.automationEnabled" aria-label="自动选禁总开关" :disabled="busy" @click="toggle"><i /></button></section>
    <p v-if="notice" class="inline-note">{{ notice }}</p><p class="automation-runtime-note">{{ state.automationStatus || '保存规则后，在本页开启总开关开始执行。' }}</p>
    <div class="automation-columns"><section class="card automation-rules"><div class="section-heading"><h3>行为设置</h3><span v-if="dirty" class="dirty-label">有未保存修改</span></div><label v-for="rule in rules" :key="rule.key" class="rule-row"><span><strong>{{ rule.title }}</strong><small>{{ rule.description }}</small></span><input v-model="draft[rule.key]" type="checkbox" @change="dirty = true" /></label><div class="rule-extra"><label>执行延迟<select v-model.number="draft.delaySeconds" @change="dirty = true"><option v-for="seconds in [0, 1, 2, 3, 5]" :key="seconds" :value="seconds">{{ seconds }} 秒</option></select></label><label>分路限定<select v-model="draft.position" @change="dirty = true"><option value="">不限分路</option><option value="top">上路</option><option value="jungle">打野</option><option value="middle">中路</option><option value="bottom">下路</option><option value="utility">辅助</option></select></label></div><button class="button primary save-rules" :disabled="busy" @click="save">保存并应用规则</button><p class="coverage-note">候选不可用时顺延；提交前确认当前本人动作。停止不撤回已发出的请求。</p></section>
      <section class="card priority-editor"><div v-for="ban in [false, true]" :key="String(ban)" class="priority-group"><div class="section-heading"><h3>{{ ban ? '禁用优先顺序' : '选用优先顺序' }}</h3><small>从左到右，依次尝试</small></div><div class="priority-list"><div v-for="(id, index) in (ban ? draft.bans : draft.picks)" :key="id" class="priority-chip"><small>{{ index + 1 }}</small><Portrait :id="id" :size="23" /><b>{{ championMap.get(id)?.name || id }}</b><button :disabled="!index" title="提高优先级" @click="promote(index, ban)">↑</button><button :title="`移除${championMap.get(id)?.name || id}`" @click="remove(index, ban)">×</button></div><p v-if="!(ban ? draft.bans : draft.picks).length" class="muted">从下方英雄库添加{{ ban ? '禁用' : '选用' }}候选</p></div></div><div class="search-field"><span>⌕</span><input v-model="search" placeholder="搜索英雄名称 / 称号 / 英文" aria-label="搜索选禁英雄" /><small>{{ heroes.length }} 位</small></div><VirtualHeroGrid :heroes="heroes" :picks="draft.picks" :bans="draft.bans" @add="add" /></section></div>
  </div>
</template>
