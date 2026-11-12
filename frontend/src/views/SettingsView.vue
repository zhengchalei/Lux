<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import { state } from '../store'
import type { ApplicationSettings, UpdateStatus } from '../types'
import InfoHint from '../components/InfoHint.vue'
import license from '../../../LICENSE?raw'
import notices from '../../../NOTICE.md?raw'
import thirdPartyLicenses from '../../../THIRD_PARTY_LICENSES.txt?raw'

// SettingsView.vue edits background lifetime, match acceptance, and software update
// preferences through independent application services. Each control saves its
// own preference, while runtime services own cancellation and persistent values.
// Scalar update progress is observed only while this workspace remains mounted.
// Tray and updater capability are observed separately for browser previews.
//
// Update actions serialize independently from acceptance settings so an unrelated
// save cannot overwrite another feature's intent. Navigation disposes progress
// observation and rejects late publication into an unmounted workspace. Matching
// and gameplay disable the explicit restart control, while the native service
// repeats that check before admitting installation. License texts stay embedded
// here for single-executable distribution without companion attribution files.
const licenseText = `${notices}\n\n${license}\n\n${thirdPartyLicenses}`
const busy = ref(false)
const notice = ref('')
const tray = ref<boolean | null>(null)
const update = ref<UpdateStatus | null>(null)
const updateBusy = ref(false)
const updateError = ref('')
const restartBlocked = computed(() => ['Matchmaking', 'ReadyCheck', 'ChampSelect', 'GameStart', 'InProgress', 'Reconnect', 'WaitingForStats'].includes(state.phase))
let updateTimer = 0
let disposed = false
let readingUpdate = false
/** readUpdate observes scalar progress while the settings workspace remains mounted.
 * One request runs at a time. Late responses after navigation are discarded,
 * and this observation never starts downloads or mutates the updater preference.
 */
async function readUpdate(): Promise<void> {
  if (disposed || readingUpdate || document.hidden) return
  readingUpdate = true
  try { const incoming = await api.GetUpdateStatus(); if (!disposed) update.value = incoming }
  catch (reason) { if (!disposed) updateError.value = reason instanceof Error ? reason.message : String(reason) }
  finally { readingUpdate = false }
}
/** updateAction submits an explicit update control and then observes its acknowledgement.
 * Controls serialize writes independently from application preferences. Service errors
 * stay inline, and a failed restart leaves the current desktop available.
 */
async function updateAction(action: 'toggle' | 'check' | 'restart'): Promise<void> {
  if (updateBusy.value || !update.value) return
  updateBusy.value = true; updateError.value = ''
  try {
    if (action === 'toggle') await api.SetAutoUpdate(!update.value.enabled)
    else if (action === 'check') await api.CheckForUpdates()
    else await api.RestartToUpdate()
    await readUpdate()
  } catch (reason) { updateError.value = reason instanceof Error ? reason.message : String(reason) }
  finally { updateBusy.value = false }
}
onMounted(() => { void readUpdate(); updateTimer = window.setInterval(() => { void readUpdate() }, 1500) })
onUnmounted(() => { disposed = true; window.clearInterval(updateTimer) })
onMounted(async () => { try { tray.value = (await api.GetDesktopStatus()).trayAvailable } catch { tray.value = false } })

/** save applies one control change using the latest observed settings.
 * The service owns persistence and acknowledgement. Failed saves remain visible,
 * and controls serialize changes so an older response cannot replace newer intent.
 * Snapshot events may acknowledge the same values while the call is pending;
 * the final local assignment uses that submitted value only after successful
 * persistence. An error leaves the observed service state authoritative, including
 * a live stop that succeeded before its settings file failed to save.
 */
async function save(patch: Partial<ApplicationSettings>): Promise<void> {
  if (busy.value) return
  busy.value = true; notice.value = ''
  try {
    const next = { ...state.application, ...patch }
    await api.SaveApplicationSettings(next)
    state.application = next
  } catch (reason) { notice.value = reason instanceof Error ? reason.message : String(reason) }
  finally { busy.value = false }
}
</script>

<template>
  <div class="settings-workspace scroll-region">
    <section class="card settings-card"><div class="section-heading"><h2>后台运行</h2><span class="settings-tag">轻量常驻</span></div>
      <div class="settings-row"><div><strong>关闭窗口后留在系统托盘</strong><small>释放主界面，保留自动连接与选人助手</small></div><button class="switch" :class="{ enabled: state.application.closeToTray }" role="switch" :aria-checked="state.application.closeToTray" aria-label="关闭窗口后留在系统托盘" :disabled="busy" @click="save({ closeToTray: !state.application.closeToTray })"><i /></button></div>
      <div class="settings-footnote">单击托盘图标打开 Lux，右键查看菜单<InfoHint label="后台资源说明" text="关闭主窗口会销毁它的 WebView；恢复时重新载入界面。客户端连接、自动接受与大乱斗小窗口继续运行。托盘不可用时，关闭窗口会直接退出。" /></div>
      <p v-if="tray === false" class="inline-note">当前环境未启用系统托盘，关闭窗口会退出。</p>
    </section>
    <section class="card settings-card"><div class="section-heading"><h2>匹配确认</h2><span class="settings-tag" :class="{ 'text-good': state.application.autoAccept }">{{ state.application.autoAccept ? '已开启' : '已关闭' }}</span></div>
      <div class="settings-row"><div><strong>自动接受对局</strong><small>匹配成功后确认进入选人，后台同样有效</small></div><button class="switch" :class="{ enabled: state.application.autoAccept }" role="switch" :aria-checked="state.application.autoAccept" aria-label="自动接受对局" :disabled="busy" @click="save({ autoAccept: !state.application.autoAccept })"><i /></button></div>
      <label class="settings-row"><span><strong>接受延迟</strong><small>留一点时间查看客户端</small></span><select aria-label="接受延迟" :value="state.application.acceptDelaySeconds" :disabled="busy" @change="save({ acceptDelaySeconds: Number(($event.target as HTMLSelectElement).value) })"><option v-for="seconds in [0, 1, 2, 3, 5, 10]" :key="seconds" :value="seconds">{{ seconds === 0 ? '立即接受' : `${seconds} 秒` }}</option></select></label>
      <div class="settings-footnote" role="status"><i class="status-dot" v-if="state.application.autoAccept" />{{ state.acceptStatus }}<InfoHint label="自动接受规则" text="只响应客户端的匹配成功确认。手动拒绝、离开确认阶段或关闭开关会停止待执行操作；每轮确认最多提交一次。与自动选禁的开关独立，重启后保留此设置。" /></div>
    </section>
    <section class="card settings-card settings-update"><div class="section-heading"><h2>软件更新</h2><span v-if="update" class="settings-tag">当前 v{{ update.current }}</span></div>
      <div class="settings-row"><div><strong>自动更新</strong><small>从 GitHub 获取新版 EXE，退出 Lux 时安装</small></div><button class="switch" :class="{ enabled: update?.enabled }" role="switch" :aria-checked="update?.enabled ?? false" aria-label="自动更新" :disabled="updateBusy || !update?.supported || update.state === 'installing'" @click="updateAction('toggle')"><i /></button></div>
      <div class="update-status" role="status"><span>{{ update?.message || '正在读取更新状态…' }}</span><strong v-if="update?.latest && update.latest !== update.current">v{{ update.latest }}</strong></div>
      <progress v-if="update?.state === 'downloading'" :value="update.progress" max="100" :aria-label="`更新下载进度 ${update.progress}%`" />
      <div class="update-controls"><button class="button" :disabled="updateBusy || !update?.supported || !update.enabled || ['checking', 'downloading', 'installing'].includes(update.state)" @click="updateAction('check')">检查更新</button><button v-if="update?.state === 'ready'" class="button primary" :disabled="updateBusy || restartBlocked" :title="restartBlocked ? '结束匹配、选人或对局后可重启' : '安装已下载的新版并重新打开 Lux'" @click="updateAction('restart')">更新并重启</button><InfoHint label="自动更新说明" text="启动时及每 6 小时检查 GitHub 正式版，下载后校验大小与 SHA-256。关闭到托盘继续运行；从托盘退出 Lux 后安装，下次打开使用新版。不会因更新自动打断对局。下载失败保留当前版本，跟随 Windows 系统代理。" /></div>
      <p v-if="updateError" class="inline-note" role="alert">{{ updateError }}</p>
    </section>
    <details class="card settings-licenses"><summary>许可证与第三方声明</summary><pre>{{ licenseText }}</pre></details>
    <p v-if="notice" class="inline-note" role="alert">{{ notice }}</p>
  </div>
</template>

<style scoped>
.settings-workspace { height: 100%; min-height: 0; max-width: 980px; width: 100%; padding: 2px 2px 24px; }
.settings-card { padding: 24px 28px; margin-bottom: 18px; }
.settings-card h2 { font-size: 18px; margin: 0; }
.settings-tag { font-size: 12px; color: var(--muted); padding: 4px 8px; background: var(--surface); border-radius: 5px; }
.settings-row { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding: 22px 0; border-bottom: 1px solid var(--line); }
.settings-row > div, .settings-row > span { min-width: 0; }
.settings-row strong { display: block; font-size: 15px; font-weight: 600; line-height: 1.5; }
.settings-row small { display: block; font-size: 13px; line-height: 1.65; margin-top: 6px; color: var(--muted); }
.settings-row select { flex-shrink: 0; background: var(--surface); border: 1px solid var(--line); border-radius: 6px; padding: 8px 12px; color: var(--ink); font-size: 14px; }
.settings-footnote { display: flex; align-items: center; gap: 7px; font-size: 13px; line-height: 1.65; color: var(--muted); padding-top: 17px; }
.settings-footnote .status-dot { position: static; }
.settings-licenses { padding: 20px 28px; }
.settings-licenses summary { cursor: pointer; font-size: 14px; color: var(--muted); }
.settings-licenses pre { max-height: 320px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; line-height: 1.7; color: var(--muted); margin: 18px 0 0; user-select: text; }
.update-status { display: flex; justify-content: space-between; gap: 14px; color: var(--muted); font-size: 13px; line-height: 1.65; padding: 16px 0 12px; overflow-wrap: anywhere; }
.update-status strong { flex-shrink: 0; color: var(--ink); }
.update-controls { display: flex; align-items: center; gap: 10px; }
.settings-update progress { width: 100%; height: 5px; accent-color: var(--accent); margin: 0 0 14px; }
.settings-update .inline-note { margin-top: 14px; }
</style>
