// verify-battle-board.cjs checks the real battle board and its contextual drawer
// in a browser or an explicitly debug-enabled desktop WebView2. The checks use
// the running service's records, retain player perspective through inspection,
// and never create game sessions, modify client selections, or enable automation.
const fs = require('node:fs/promises')
const path = require('node:path')
const assert = require('node:assert/strict')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

// The owned browser is closed on failure; a native CDP connection is only
// disconnected, leaving the user's application available for direct evaluation.
let browser

/** main validates readable rosters and the player-to-match inspection journey.
 * Start a real Go service with an available battle before running the script.
 * Optional CDP mode reuses the actual desktop window and preserves its size.
 */
async function main() {
  const native = !!process.env.LUX_CDP
  browser = native ? await chromium.connectOverCDP(process.env.LUX_CDP) : await chromium.launch({ headless: true })
  const page = native ? browser.contexts()[0].pages().find(candidate => candidate.url().startsWith('http://mygo.localhost/'))
    : await browser.newPage({ viewport: { width: 1280, height: 860 }, deviceScaleFactor: 1 })
  assert.ok(page, 'Desktop page is available')
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  const output = path.resolve(__dirname, '../artifacts/ui-board')
  await fs.mkdir(output, { recursive: true })
  if (!native) await page.goto(`${process.env.LUX_TEST_BASE || 'http://127.0.0.1:5173'}/?page=battle`)
  else await page.getByRole('button', { name: '对战助手', exact: true }).click()
  await page.locator('.player-card').nth(9).waitFor({ timeout: 45000 })
  await page.locator('.player-card').nth(9).locator('.player-match').first().waitFor({ timeout: 45000 })
  const report = { renderer: native ? 'actual MyGo WebView2' : 'headless Chromium, real Go service', layouts: [], journey: [], errors }
  const sizes = native ? [null] : [{ width: 1280, height: 860 }, { width: 1680, height: 1000 }, { width: 1060, height: 700 }]
  for (const size of sizes) {
    if (size) await page.setViewportSize(size)
    for (const theme of ['light', 'dark']) {
      const desired = theme === 'light' ? '切换浅色' : '切换深色'
      if (await page.getByRole('button', { name: desired, exact: true }).count()) await page.getByRole('button', { name: desired, exact: true }).click()
      const layout = await page.evaluate(() => {
        const cards = [...document.querySelectorAll('.player-card')]
        const workspace = document.querySelector('.battle-workspace')
        return {
          width: innerWidth, height: innerHeight, theme: document.documentElement.dataset.theme,
          columns: [...document.querySelectorAll('.board-player-grid')].map(team => getComputedStyle(team).gridTemplateColumns.split(' ').length),
          count: cards.length,
          visible: cards.filter(card => { const b = card.getBoundingClientRect(); return b.top >= 0 && b.bottom <= innerHeight - 28 }).length,
          cardOverflow: cards.filter(card => card.scrollWidth > card.clientWidth + 1).length,
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
          workspaceScroll: workspace.scrollHeight > workspace.clientHeight + 1,
        }
      })
      assert.equal(layout.horizontalOverflow, false, 'Window has no horizontal overflow')
      assert.equal(layout.cardOverflow, 0, 'Card content stays within its column')
      assert.deepEqual(layout.columns, [5, 5])
      assert.equal(layout.count, 10)
      if (layout.width >= 1280 && layout.height >= 860) { assert.equal(layout.visible, 10); assert.equal(layout.workspaceScroll, false) }
      if (layout.width === 1060 && layout.height === 700) {
        assert.equal(layout.workspaceScroll, true, 'Minimum window allows readable vertical scrolling')
        const bottomReachable = await page.locator('.battle-workspace').evaluate(element => {
          element.scrollTop = element.scrollHeight
          const bottom = element.querySelector('.board-caption').getBoundingClientRect().bottom
          const reachable = bottom <= element.getBoundingClientRect().bottom + 1
          element.scrollTop = 0
          return reachable
        })
        assert.equal(bottomReachable, true, 'Enemy team and caption remain reachable')
      }
      report.layouts.push(layout)
      await page.screenshot({ path: path.join(output, `board-${layout.width}-${theme}${native ? '-native' : ''}.png`) })
    }
  }
  if (!native) await page.setViewportSize({ width: 1280, height: 860 })
  if (await page.getByRole('button', { name: '切换浅色', exact: true }).count()) await page.getByRole('button', { name: '切换浅色', exact: true }).click()
  await page.getByRole('button', { name: '团队贡献', exact: true }).click()
  assert.equal(await page.locator('.contribution-lens').count(), 10)
  await page.getByRole('button', { name: '近期表现', exact: true }).click()
  const recent = page.locator('.player-recent').first()
  await recent.evaluate(element => { element.scrollTop = 70 })
  const offset = await recent.evaluate(element => element.scrollTop)
  const detailButton = page.locator('.player-detail').first()
  await detailButton.click()
  await page.locator('dialog[open]').waitFor()
  assert.equal(await page.locator('dialog .profile-analysis').count(), 1)
  const playerName = await page.locator('#inspector-title').innerText()
  const rows = await page.locator('dialog .match-row').count()
  assert.ok(rows < 25, 'Large player samples keep their DOM bounded')
  await page.keyboard.press('Escape')
  await page.locator('dialog').waitFor({ state: 'detached' })
  assert.equal(await detailButton.evaluate(element => document.activeElement === element), true, 'Escape restores the clicked control')
  assert.equal(await recent.evaluate(element => element.scrollTop), offset, 'Drawer preserves board scrolling')
  report.journey.push('Player analysis opens in a modal drawer; Escape restores focus and board offset')
  const selectedRow = page.locator('.player-card').first().locator('.player-match').nth(2)
  const rowLabel = await selectedRow.getAttribute('aria-label')
  await selectedRow.click()
  await page.locator('dialog .combat-row').nth(9).waitFor({ timeout: 30000 })
  assert.equal(await page.locator('#inspector-title').innerText(), playerName)
  assert.equal(await page.locator('dialog .combat-row.own').count(), 1)
  const expectedKda = rowLabel.split(' · ')[2].replaceAll('/', ' / ')
  assert.equal((await page.locator('dialog .detail-kda > strong').innerText()).replaceAll(/\s+/g, ' ').trim(), expectedKda)
  await page.getByRole('button', { name: '贡献分析', exact: true }).click()
  await page.locator('dialog .radar').waitFor()
  await page.screenshot({ path: path.join(output, `match-inspection${native ? '-native' : ''}.png`) })
  await page.getByRole('button', { name: '返回玩家分析', exact: true }).click()
  await page.locator('dialog .profile-analysis').waitFor()
  await page.getByRole('button', { name: '关闭详情', exact: true }).click()
  report.journey.push('Recent match retains player perspective, ten participants, K/D/A, radar, and return path')
  for (const name of ['战绩分析', '选人助手', '战绩查询', '自动选禁', '对战助手']) {
    await page.getByRole('button', { name, exact: true }).click()
    await page.getByRole('heading', { name, exact: true }).waitFor()
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
  }
  assert.equal(await page.locator('.service-error').count(), 0)
  assert.deepEqual(errors, [])
  report.journey.push('All five workspaces navigate without horizontal overflow or renderer exceptions')
  await fs.writeFile(path.join(output, `board-verification${native ? '-native' : ''}.json`), JSON.stringify(report, null, 2))
  process.stdout.write(JSON.stringify(report, null, 2))
  await browser.close()
}
main().catch(async error => { process.stderr.write(String(error)); await browser?.close(); process.exitCode = 1 })
