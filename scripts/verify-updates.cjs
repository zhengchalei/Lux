// verify-updates.cjs checks update settings against an isolated renderer service.
// The preview supplies compiled product assets; all release checks, preference
// writes, and restart requests remain in this fixture. Real executable replacement
// is exercised separately by the Windows updater tests in an isolated directory.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** main verifies update progress, cancellation, restart protection, and settings layouts.
 * No fixture command reaches the real desktop or GitHub. Failures remain inline,
 * and navigation disposes progress observation without publishing a stale response.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const out = path.resolve(__dirname, '../artifacts/updates')
  await fs.mkdir(out, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const source = (await (await page.request.post(`${base}/api/GetSnapshot`, { data: [] })).json()).result
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    await page.addInitScript(source => {
      source.phase = 'None'; source.room = null
      const update = { supported: true, enabled: true, current: '0.2.11', latest: '0.2.11', state: 'current', progress: 0, message: '已是最新版本', checkedAt: '' }
      const events = []; const listeners = new Set()
      window.__updateFixture = { update, events, source, notify: () => { source.revision++; for (const callback of listeners) callback() } }
      window.__luxSubscribe = listener => { listeners.add(listener); return () => listeners.delete(listener) }
      window.__luxBackend = {
        GetSnapshot: async () => structuredClone(source), GetRevision: async () => source.revision,
        GetDesktopStatus: async () => ({ trayAvailable: true }),
        GetUpdateStatus: async () => structuredClone(update),
        SetAutoUpdate: async enabled => { events.push(['auto', enabled]); update.enabled = enabled; update.state = 'idle'; update.message = enabled ? '等待检查更新' : '自动更新已关闭' },
        CheckForUpdates: async () => { events.push(['check']); update.state = 'checking'; update.message = '正在检查 GitHub Release…' },
        RestartToUpdate: async () => { events.push(['restart']); throw Error('测试程序目录不可写，当前版本保留') },
      }
    }, source)
    await page.goto(`${base}/?page=settings`)
    const auto = page.getByRole('switch', { name: '自动更新', exact: true })
    const check = page.getByRole('button', { name: '检查更新', exact: true })
    const card = page.locator('.settings-update')
    await auto.waitFor()
    await page.getByText('已是最新版本', { exact: true }).waitFor()
    await check.click()
    await page.getByText('正在检查 GitHub Release…', { exact: true }).waitFor()
    assert.equal(await check.isDisabled(), true)
    await page.evaluate(() => Object.assign(window.__updateFixture.update, { state: 'downloading', latest: '0.2.12', progress: 43, message: '正在下载新版 Lux.exe…' }))
    await page.getByRole('progressbar', { name: '更新下载进度 43%' }).waitFor()
    await auto.click()
    await page.getByText('自动更新已关闭', { exact: true }).waitFor()
    assert.equal(await check.isDisabled(), true)
    assert.equal(await page.locator('progress').count(), 0)
    await auto.click()
    await page.evaluate(() => Object.assign(window.__updateFixture.update, { state: 'ready', progress: 100, message: '新版已下载，将在退出 Lux 后自动安装' }))
    const restart = page.getByRole('button', { name: '更新并重启', exact: true })
    await restart.waitFor()
    await page.evaluate(() => { const fixture = window.__updateFixture; fixture.source.phase = 'InProgress'; fixture.notify() })
    await page.waitForFunction(() => document.querySelector('.update-controls .primary')?.disabled)
    assert.equal(await restart.isDisabled(), true)
    await page.evaluate(() => { const fixture = window.__updateFixture; fixture.source.phase = 'None'; fixture.notify() })
    await page.waitForFunction(() => !document.querySelector('.update-controls .primary')?.disabled)
    await restart.click()
    await page.getByRole('alert').filter({ hasText: '测试程序目录不可写，当前版本保留' }).waitFor()
    const events = await page.evaluate(() => window.__updateFixture.events)
    assert.deepEqual(events, [['check'], ['auto', false], ['auto', true], ['restart']])
    for (const width of [1060, 1280]) {
      await page.setViewportSize({ width, height: 860 })
      for (const theme of ['light', 'dark']) {
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await card.scrollIntoViewIfNeeded()
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        await page.screenshot({ path: path.join(out, `settings-${width}-${theme}.png`) })
      }
    }
    await page.getByRole('button', { name: '战绩分析', exact: true }).click()
    assert.equal(await card.count(), 0)
    assert.deepEqual(errors, [])
    const report = { source: 'isolated update renderer fixture; no real install', progress: true, cancelOnDisable: true, restartBlockedDuringGame: true, failureVisible: true, layoutChecks: 4, disposedOnNavigation: true, events, errors }
    await fs.writeFile(path.join(out, 'renderer-report.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report))
  } finally { await browser.close() }
}
main().catch(error => { console.error(error); process.exitCode = 1 })
