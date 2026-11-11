// browser-smoke.cjs exercises the real Go preview endpoint in a Chromium renderer.
// It never injects player records, enables automation, or submits game actions.
// The report separates optional connected-client evidence from offline UI checks
// so browser layout proof cannot be mistaken for a complete WebView2 game test.
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')
// Retain the owned browser so a failed assertion also releases its child processes.
let activeBrowser

/** main verifies navigation, local rule editing, and available real match layouts.
 * The caller must start Lux's loopback preview server before running this script.
 * Backend errors and console exceptions fail the run instead of replacing state.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const output = path.resolve(__dirname, '../.artifacts')
  await fs.mkdir(output, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  activeBrowser = browser
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 }, deviceScaleFactor: 1 })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  const reply = await page.request.post(`${base}/api/GetSnapshot`, { data: [] })
  const envelope = await reply.json()
  const snapshot = envelope.result || envelope
  await page.goto(base, { waitUntil: 'networkidle' })
  const checks = []
  for (const [label, key] of [['战绩分析', 'history'], ['选人助手', 'draft'], ['对战助手', 'battle'], ['战绩查询', 'query'], ['自动选禁', 'automation']]) {
    await page.getByRole('button', { name: label, exact: true }).click()
    await page.getByRole('heading', { name: label, exact: true }).waitFor()
    if (key === 'battle' && snapshot.connected) {
      await page.locator('.connection-state.connected').waitFor({ timeout: 45000 })
      await page.getByRole('button', { name: '复盘最近一局', exact: true }).first().click()
      await page.waitForFunction(() => document.querySelectorAll('.player-card').length > 0, { timeout: 30000 })
      await page.waitForTimeout(2500)
    }
    if (key === 'query' && snapshot.connected) {
      await page.getByRole('button', { name: '查询自己', exact: true }).click()
      await page.locator('.history-overview').waitFor({ timeout: 30000 })
      await page.waitForTimeout(1000)
    }
    await page.screenshot({ path: path.join(output, `browser-${key}.png`) })
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)
    if (overflow) throw new Error(`${key} overflows the window horizontally`)
    checks.push(`${key} navigation and viewport width`)
  }
  const candidate = snapshot.database.champions.find(hero => !(snapshot.automation.picks || []).includes(hero.id))
  await page.getByRole('textbox', { name: '搜索选禁英雄' }).fill(candidate.name)
  await page.getByTitle(`添加${candidate.name}到选用顺序`, { exact: true }).click()
  if (!(await page.locator('.dirty-label').isVisible())) throw new Error('Local rule edit did not show unsaved state')
  const switchValue = await page.getByRole('switch', { name: '自动选禁总开关' }).getAttribute('aria-checked')
  if (switchValue !== 'false') throw new Error('Automation unexpectedly enabled')
  checks.push('Local rule edits keep runtime disabled')
  await page.getByRole('button', { name: '切换深色', exact: true }).click()
  await page.screenshot({ path: path.join(output, 'browser-automation-dark.png') })
  await page.getByRole('button', { name: '切换浅色', exact: true }).click()
  checks.push('Light and dark theme')
  await page.goto(`${base}/?page=battle`, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: '对战助手', exact: true }).waitFor()
  checks.push('URL workspace selection')
  const battleCards = await page.locator('.player-card').count()
  let visibleBattleCards = 0
  if (battleCards) {
    visibleBattleCards = await page.locator('.player-card').evaluateAll(cards => cards.filter(card => { const bounds = card.getBoundingClientRect(); return bounds.top >= 0 && bounds.bottom <= innerHeight - 30 }).length)
    if (visibleBattleCards !== battleCards) throw new Error('Ten-player cards require page scrolling at 1280×860')
  }
  const latestReply = await page.request.post(`${base}/api/GetSnapshot`, { data: [] })
  const latestEnvelope = await latestReply.json()
  const latest = latestEnvelope.result || latestEnvelope
  if (latest.query) {
    await page.goto(`${base}/?page=query`, { waitUntil: 'networkidle' })
    await page.locator('.history-overview').waitFor()
    const list = page.locator('.match-list')
    const before = await page.locator('.match-row').first().getAttribute('aria-label')
    await list.evaluate(element => { element.scrollTop = element.scrollHeight })
    await page.waitForTimeout(150)
    const after = await page.locator('.match-row').first().getAttribute('aria-label')
    const domRows = await page.locator('.match-row').count()
    if (latest.query.matches.length > 20 && (before === after || domRows >= latest.query.matches.length)) throw new Error('Match list did not virtualize real records')
    checks.push('Real queried history virtualizes and scrolls')
  }
  if (errors.length) throw new Error(`Renderer exceptions: ${errors.join('; ')}`)
  const report = { renderer: 'headless Chromium browser, not native WebView2', connected: !!latest.connected, champions: latest.database?.champions?.length || 0, historyRecords: latest.history?.matches?.length || 0, queryRecords: latest.query?.matches?.length || 0, battleCards, visibleBattleCards, checks, rendererExceptions: errors, playerRecordsInjected: false, automationEnabled: false }
  await fs.writeFile(path.join(output, 'browser-smoke.json'), JSON.stringify(report, null, 2))
  process.stdout.write(JSON.stringify(report, null, 2))
  await browser.close()
}
main().catch(async error => { process.stderr.write(String(error)); await activeBrowser?.close(); process.exitCode = 1 })
