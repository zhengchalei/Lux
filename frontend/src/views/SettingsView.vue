<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../api'
import { state } from '../store'
import type { ApplicationSettings } from '../types'
import InfoHint from '../components/InfoHint.vue'
import license from '../../../LICENSE?raw'
import notices from '../../../NOTICE.md?raw'
import thirdPartyLicenses from '../../../THIRD_PARTY_LICENSES.txt?raw'

// SettingsView.vue edits background lifetime and independent match acceptance
// preferences through the application service. Each control saves immediately,
// while the runtime owns delay cancellation and persistent values. Tray recovery
// capability is observed separately so browser previews explain their limitation.
// Single-executable releases retain attribution here without companion text files.
const licenseText = `${notices}\n\n${license}\n\n${thirdPartyLicenses}`
const busy = ref(false)
const notice = ref('')
const tray = ref<boolean | null>(null)
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
    <details class="card settings-licenses"><summary>许可证与第三方声明</summary><pre>{{ licenseText }}</pre></details>
    <p v-if="notice" class="inline-note" role="alert">{{ notice }}</p>
  </div>
</template>

<style scoped>
.settings-workspace{max-width:860px;width:100%;padding:2px 2px 24px}.settings-card{padding:24px 28px;margin-bottom:18px}.settings-card h2{font-size:16px;margin:0}.settings-tag{font-size:11px;color:var(--muted);padding:4px 8px;background:var(--surface);border-radius:5px}.settings-row{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:21px 0;border-bottom:1px solid var(--line)}.settings-row strong{display:block;font-size:13px;font-weight:550}.settings-row small{display:block;font-size:11px;line-height:1.7;margin-top:5px;color:var(--muted)}.settings-row select{background:var(--surface);border:1px solid var(--line);border-radius:6px;padding:7px 12px;color:var(--ink);font-size:12px}.settings-footnote{display:flex;align-items:center;gap:7px;font-size:11px;color:var(--muted);padding-top:17px}.settings-footnote .status-dot{position:static}
.settings-licenses{padding:18px 28px}.settings-licenses summary{cursor:pointer;font-size:12px;color:var(--muted)}.settings-licenses pre{max-height:320px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:11px;line-height:1.7;color:var(--muted);margin:18px 0 0;user-select:text}
</style>
