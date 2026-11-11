// verify-background.cjs checks immediate preference controls against an isolated
// renderer bridge. No acceptance permission reaches the running client; native
// tray lifetime is verified separately by the opt-in Go desktop test process.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** main verifies immediate settings acknowledgement, persistence errors, and compact layouts.
 * The preview supplies compiled assets only. Every preference mutation is handled
 * by this test fixture, independently from the live Go client runtime.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const out = path.resolve(__dirname, '../artifacts/background')
  await fs.mkdir(out, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    const source = (await (await page.request.post(`${base}/api/GetSnapshot`, {data:[]})).json()).result
    await page.addInitScript(source => {
      source.application = { closeToTray: true, autoAccept: false, acceptDelaySeconds: 2 }
      source.acceptStatus = '自动接受已关闭'
      source.phase = 'None'; source.room = null
      const listeners = new Set()
      const events = []
      window.__backgroundFixture = { source, events, fail: false }
      window.__luxSubscribe = listener => { listeners.add(listener); return () => listeners.delete(listener) }
      window.__luxBackend = {
        GetSnapshot: async () => structuredClone(source), GetRevision: async () => source.revision,
        GetDesktopStatus: async () => ({ trayAvailable: true }),
        GetUpdateStatus: async () => ({ supported: false, enabled: true, current: '0.2.11', latest: '', state: 'idle', progress: 0, message: '自动更新仅在桌面发行版中运行', checkedAt: '' }),
        SaveApplicationSettings: async settings => {
          if (window.__backgroundFixture.fail) throw Error('测试保存失败')
          events.push(structuredClone(settings)); source.application = settings
          source.acceptStatus = settings.autoAccept ? '等待匹配成功' : '自动接受已关闭'
          source.revision++; for (const listener of listeners) listener()
        },
      }
    }, source)
    await page.goto(`${base}/?page=settings`)
    const accept = page.getByRole('switch', { name: '自动接受对局', exact: true })
    const tray = page.getByRole('switch', { name: '关闭窗口后留在系统托盘', exact: true })
    await accept.waitFor()
    assert.equal(await accept.getAttribute('aria-checked'), 'false')
    await accept.click()
    await page.waitForFunction(() => document.querySelector('[aria-label="自动接受对局"]').getAttribute('aria-checked') === 'true')
    await page.getByLabel('接受延迟', { exact: true }).selectOption('5')
    await tray.click()
    const settings = await page.evaluate(() => window.__backgroundFixture.events)
    assert.deepEqual(settings.at(-1), { closeToTray: false, autoAccept: true, acceptDelaySeconds: 5 })
    await page.evaluate(() => { window.__backgroundFixture.fail = true })
    await accept.click()
    await page.getByRole('alert').filter({hasText:'测试保存失败'}).waitFor()
    assert.equal(await accept.getAttribute('aria-checked'), 'true', 'failed save invented acknowledgement')
    for (const [width, height] of [[1280, 860], [1060, 700]]) {
      await page.setViewportSize({ width, height })
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        await page.screenshot({path:path.join(out,`settings-${width}-${theme}.png`)})
      }
    }
    assert.deepEqual(errors, [])
    const report = { source: 'isolated renderer bridge; no live acceptance enabled', immediateSave: true, independentControls: true, saveError: true, layoutChecks: 4, errors }
    await fs.writeFile(path.join(out, 'settings-report.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report))
  } finally { await browser.close() }
}
main().catch(error => {console.error(error);process.exitCode=1})
