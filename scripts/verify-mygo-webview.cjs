// verify-mygo-webview.cjs checks the running MyGo window through its opt-in CDP port.
// It observes the application's real Go-backed state and records renderer frame
// intervals during injected pointer and wheel input. Results describe WebView2
// scheduling rather than physical mouse-to-display latency or a guaranteed frame rate.
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** measure collects frame intervals while a supplied input sequence runs.
 * The renderer must be visible and the caller chooses the real application page.
 * Observers are removed after each sequence so production behavior remains unchanged.
 */
async function measure(page, input) {
  await page.evaluate(() => {
    window.__luxProbe = { active: true, frames: [], longTasks: [], mouseEvents: 0, scrollEvents: 0, last: 0 }
    const probe = window.__luxProbe
    probe.mouse = () => probe.mouseEvents++
    probe.scroll = () => probe.scrollEvents++
    document.addEventListener('mousemove', probe.mouse, { passive: true })
    document.addEventListener('scroll', probe.scroll, { passive: true, capture: true })
    probe.observer = new PerformanceObserver(list => probe.longTasks.push(...list.getEntries().map(entry => entry.duration)))
    probe.observer.observe({ entryTypes: ['longtask'] })
    function tick(now) {
      if (!probe.active) return
      if (probe.last) probe.frames.push(now - probe.last)
      probe.last = now
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
  await input()
  return page.evaluate(() => {
    const probe = window.__luxProbe
    probe.active = false
    probe.observer.disconnect()
    document.removeEventListener('mousemove', probe.mouse)
    document.removeEventListener('scroll', probe.scroll, true)
    const sorted = probe.frames.toSorted((a, b) => a - b)
    const percentile = n => Math.round((sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * n))] || 0) * 100) / 100
    const result = { frames: sorted.length, medianMs: percentile(0.5), p95Ms: percentile(0.95), p99Ms: percentile(0.99), over25ms: sorted.filter(n => n > 25).length, over50ms: sorted.filter(n => n > 50).length, mouseEvents: probe.mouseEvents, scrollEvents: probe.scrollEvents, longTasks: probe.longTasks }
    delete window.__luxProbe
    return result
  })
}

/** main validates native IPC, navigation, real rosters, and renderer cadence.
 * The executable must be started with remote debugging explicitly enabled for
 * this test. Automation settings are never enabled or saved by this script.
 */
async function main() {
  const browser = await chromium.connectOverCDP(process.env.LUX_CDP || 'http://127.0.0.1:19374')
  const page = browser.contexts()[0].pages().find(candidate => candidate.url().startsWith('http://mygo.localhost/'))
  if (!page) throw new Error('Actual MyGo WebView2 page unavailable')
  const output = path.resolve(process.env.LUX_VERIFY_OUTPUT || path.join(__dirname, '../artifacts/mygo-dev'))
  await fs.mkdir(output, { recursive: true })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  const report = { renderer: 'actual desktop MyGo WebView2', input: 'CDP injected pointer/wheel; not physical end-to-end latency', pages: [], performance: {}, consoleErrors: errors }
  report.data = await page.evaluate(async () => {
    const snapshot = await window.mygo.call('App.GetSnapshot')
    const profiles = Object.values(snapshot.scouts).filter(scout => scout.profile).map(scout => scout.profile)
    return { connected: snapshot.connected, requested: snapshot.analysis.matchCount, loadedPlayers: profiles.length, minPlayerMatches: profiles.length ? Math.min(...profiles.map(profile => profile.matches.length)) : 0, maxPlayerMatches: profiles.length ? Math.max(...profiles.map(profile => profile.matches.length)) : 0, historyMatches: snapshot.history?.matches.length || 0, automationEnabled: snapshot.automationEnabled }
  })
  for (const label of ['战绩分析', '选人助手', '对战助手', '战绩查询', '自动选禁']) {
    await page.getByRole('button', { name: label, exact: true }).click()
    await page.getByRole('heading', { name: label, exact: true }).waitFor()
    const observation = await page.evaluate(() => ({ bridge: 'mygo' in window, overflow: document.documentElement.scrollWidth > innerWidth, serviceError: document.querySelector('.service-error')?.textContent || '', brokenImages: [...document.images].filter(image => image.complete && !image.naturalWidth).length }))
    if (!observation.bridge || observation.overflow || observation.serviceError || observation.brokenImages) throw new Error(JSON.stringify({ label, ...observation }))
    report.pages.push({ label, ...observation })
  }
  await page.getByRole('button', { name: '对战助手', exact: true }).click()
  report.roster = await page.locator('.player-card').evaluateAll(cards => ({ count: cards.length, visible: cards.filter(card => { const b = card.getBoundingClientRect(); return b.top >= 0 && b.bottom <= innerHeight - 30 }).length }))
  report.performance.battlePointer = await measure(page, async () => {
    // Traverse real card bounds so CSS hover, portraits, and result cells receive ordinary renderer pointer events.
    for (let i = 0; i < 220; i++) { await page.mouse.move(200 + (i * 37) % 1010, 180 + (i * 19) % 600); await page.waitForTimeout(8) }
  })
  const recent = page.locator('.player-recent').first()
  report.performance.battleRecentScroll = await measure(page, async () => {
    const bounds = await recent.boundingBox()
    await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2)
    for (let i = 0; i < 80; i++) { await page.mouse.wheel(0, i % 20 < 10 ? 30 : -30); await page.waitForTimeout(12) }
  })
  await page.screenshot({ path: path.join(output, 'battle-native.png') })
  await page.getByRole('button', { name: '战绩分析', exact: true }).click()
  const list = page.locator('.match-list')
  await list.waitFor()
  report.performance.historyPointer = await measure(page, async () => {
    // Cross the match list, comparison table, and information triggers using ordinary renderer pointer input after records have settled.
    for (let i = 0; i < 160; i++) { await page.mouse.move(185 + (i * 43) % 1040, 95 + (i * 23) % 690); await page.waitForTimeout(8) }
  })
  report.performance.historyScroll = await measure(page, async () => {
    const bounds = await list.boundingBox()
    await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2)
    for (let i = 0; i < 120; i++) { await page.mouse.wheel(0, i % 40 < 20 ? 58 : -58); await page.waitForTimeout(12) }
  })
  report.virtualList = await list.evaluate(element => ({ rendered: element.querySelectorAll('.match-row').length, scrollHeight: element.scrollHeight, clientHeight: element.clientHeight }))
  await page.screenshot({ path: path.join(output, 'history-native.png') })
  if (errors.length) throw new Error(errors.join('; '))
  await fs.writeFile(path.join(output, 'native-webview-report.json'), JSON.stringify(report, null, 2))
  process.stdout.write(JSON.stringify(report, null, 2))
  // Disconnect the testing connection while leaving the user's desktop application running for direct evaluation.
  await browser.close()
}
main().catch(error => { process.stderr.write(String(error)); process.exitCode = 1 })
