// verify-history-workspace.cjs checks match selection, scoped filtering, keyboard
// navigation, and contextual explanations against the running Go application's
// records. Browser layout checks and actual WebView2 checks produce separate
// reports. No player fixtures, game actions, or automation permissions are injected.
const fs = require('node:fs/promises')
const path = require('node:path')
const assert = require('node:assert/strict')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

// Browser ownership is retained for failure cleanup; native CDP mode disconnects
// after verification and leaves the user's executable available for direct use.
let browser

/** snapshot reads one sanitized product observation through the page's real transport.
 * Native mode uses MyGo IPC, while browser mode uses the explicit development
 * endpoint. Both results retain the same player perspectives and match identifiers.
 */
async function snapshot(page, native) {
  if (native) return page.evaluate(() => window.mygo.call('App.GetSnapshot'))
  const response = await page.request.post('/api/GetSnapshot', { data: [] })
  const envelope = await response.json()
  return envelope.result
}

/** main verifies the history journey and records its actual rendering boundaries.
 * The caller starts a connected service with self history already available.
 * Filtering operates only in Vue; this script does not change persisted sample
 * depth, create matches, or submit champion selection and automation commands.
 */
async function main() {
  const native = !!process.env.LUX_CDP
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:5173'
  browser = native ? await chromium.connectOverCDP(process.env.LUX_CDP) : await chromium.launch({ headless: true })
  const page = native ? browser.contexts()[0].pages().find(candidate => candidate.url().startsWith('http://mygo.localhost/'))
    : await browser.newPage({ viewport: { width: 1280, height: 860 }, baseURL: base })
  assert.ok(page, 'Application page is available')
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  const output = path.resolve(__dirname, '../artifacts/ui-history')
  await fs.mkdir(output, { recursive: true })
  if (!native) await page.goto(`${base}/?page=history`)
  else await page.getByRole('button', { name: '战绩分析', exact: true }).click()
  await page.locator('.history-overview').waitFor({ timeout: 45000 })
  await page.locator('.combat-row').nth(9).waitFor({ timeout: 45000 })
  // Startup can publish the first page before the remaining real records arrive; wait for an observed settled sample before asserting virtualization.
  let state
  for (let attempt = 0; attempt < 60; attempt++) {
    state = await snapshot(page, native)
    if ((state.history?.matches?.length || 0) > 20 && Object.values(state.scouts).every(scout => !scout.loading)) break
    await page.waitForTimeout(500)
  }
  const profile = state.history
  assert.ok(profile?.matches?.length > 20, 'Real history provides enough records for virtualization')
  assert.equal(state.automationEnabled, false)
  const report = { renderer: native ? 'actual MyGo WebView2' : 'headless Chromium with real Go data', records: profile.matches.length, layouts: [], checks: [], skipped: [], errors }
  for (const size of native ? [null] : [{ width: 1280, height: 860 }, { width: 1680, height: 1000 }, { width: 1060, height: 700 }]) {
    if (size) await page.setViewportSize(size)
    for (const theme of ['light', 'dark']) {
      const desired = theme === 'light' ? '切换浅色' : '切换深色'
      if (await page.getByRole('button', { name: desired, exact: true }).count()) await page.getByRole('button', { name: desired, exact: true }).click()
      await page.waitForTimeout(150)
      const layout = await page.evaluate(() => {
        const content = document.querySelector('.history-content')
        const rows = [...document.querySelectorAll('.combat-row')]
        return {
          width: innerWidth, height: innerHeight, theme: document.documentElement.dataset.theme,
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
          tableRowOverflow: rows.filter(row => row.scrollWidth > row.clientWidth + 1).length,
          visiblePlayers: rows.filter(row => { const box = row.getBoundingClientRect(); return box.top >= 0 && box.bottom <= innerHeight - 28 }).length,
          detailScroll: content.scrollHeight > content.clientHeight + 1,
          listDOM: document.querySelectorAll('.match-row').length,
          alwaysVisibleExplanations: document.querySelectorAll('.analysis-intro p, .share-summary p, .coverage-note').length,
        }
      })
      assert.equal(layout.horizontalOverflow, false)
      assert.equal(layout.tableRowOverflow, 0)
      assert.equal(layout.alwaysVisibleExplanations, 0)
      assert.ok(layout.listDOM < profile.matches.length)
      if (layout.width >= 1280 && layout.height >= 860) { assert.equal(layout.visiblePlayers, 10); assert.equal(layout.detailScroll, false) }
      report.layouts.push(layout)
      await page.screenshot({ path: path.join(output, `history-${layout.width}-${theme}${native ? '-native' : ''}.png`) })
    }
  }
  if (!native) await page.setViewportSize({ width: 1280, height: 860 })
  const light = page.getByRole('button', { name: '切换浅色', exact: true })
  if (await light.count()) await light.click()
  await page.getByRole('button', { name: '筛选战绩', exact: true }).click()
  const panel = page.getByRole('dialog', { name: '战绩筛选', exact: true })
  await page.waitForFunction(() => document.activeElement === document.querySelector('.filter-search input'))
  await page.screenshot({ path: path.join(output, `history-filter-panel${native ? '-native' : ''}.png`) })
  const mode = panel.getByRole('group', { name: '按比赛类型筛选', exact: true })
  // Real accounts need not have Mayhem records. Select an observed winning sample
  // so a disabled empty type does not masquerade as a renderer filtering failure.
  const availableMode = [
    { kind: 'mayhem', label: '海克斯大乱斗', queues: [2400, 3270] },
    { kind: 'solo', label: '单双排', queues: [420] },
    { kind: 'aram', label: '大乱斗', queues: [450, 3200, 3210, 3220, 3230] },
  ].find(entry => profile.summaries[`history:${entry.kind}`]?.heroes.some(hero => hero.wins > 0))
  assert.ok(availableMode, 'Observed competitive type has a winning champion sample')
  await mode.getByRole('button', { name: availableMode.label, exact: true }).click()
  const sample = profile.summaries[`history:${availableMode.kind}`]
  const summaryText = await page.locator('.summary-combat').innerText()
  assert.ok(summaryText.includes(`${sample.games} 场`))
  const selectedHero = sample.heroes.find(hero => hero.wins > 0)
  const heroName = state.database.champions.find(hero => hero.id === selectedHero.championId).name
  await panel.getByRole('button', { name: `筛选英雄 ${heroName}`, exact: true }).click()
  await panel.getByRole('button', { name: '仅看胜利', exact: true }).click()
  const expected = profile.matches.filter(match => availableMode.queues.includes(match.queueId) && match.championId === selectedHero.championId && match.win)
  assert.ok(expected.length, 'Observed champion has winning games')
  assert.equal(await page.locator('.history-list-heading b').innerText(), String(expected.length))
  assert.equal(await page.locator('.summary-combat').innerText(), summaryText, 'List filters do not silently change mode summary')
  assert.equal(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'), String(expected[0].id))
  await panel.getByRole('button', { name: '重置筛选', exact: true }).click()
  await panel.getByRole('button', { name: '完成', exact: true }).click()
  report.checks.push('Mode scopes summary; champion and result filters scope the list and retain selected identity')
  const list = page.locator('.match-list')
  await list.focus()
  await page.keyboard.press('Home')
  assert.equal(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'), String(profile.matches[0].id))
  await page.keyboard.press('ArrowDown')
  assert.equal(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'), String(profile.matches[1].id))
  await page.keyboard.press('End')
  const last = page.locator('.match-row[aria-pressed=true]')
  assert.equal(await last.getAttribute('data-match-id'), String(profile.matches.at(-1).id))
  assert.equal(await last.evaluate(element => document.activeElement === element), true)
  await page.keyboard.press('Home')
  await page.locator('.combat-row').nth(9).waitFor()
  const first = profile.matches[0]
  assert.equal((await page.locator('.detail-kda > strong').innerText()).replaceAll(/\s+/g, ' ').trim(), `${first.kills} / ${first.deaths} / ${first.assists}`)
  report.checks.push('Arrow and boundary keys traverse virtual records, restore row focus, and retain exact K/D/A')
  await page.getByRole('group', { name: '双方比较指标' }).getByRole('button', { name: '经济', exact: true }).click()
  const own = first.players.find(player => player.self)
  const ownScore = page.locator('.combat-row.own .score-value')
  assert.ok((await ownScore.getAttribute('title')).includes(own.gold.toLocaleString('zh-CN')))
  await page.getByRole('button', { name: '贡献分析', exact: true }).click()
  assert.equal(await page.locator('.metric-row').count(), 10)
  const contributionBounds = await page.locator('.history-content').evaluate(element => ({ height: element.clientHeight, scroll: element.scrollHeight }))
  assert.ok(contributionBounds.scroll <= contributionBounds.height + 1, 'Radar and ten-player ranking fit together at desktop size')
  await page.screenshot({ path: path.join(output, `contribution${native ? '-native' : ''}.png`) })
  report.checks.push('Team tables switch damage/economy; contribution radar and ten-player ranking fit without scrolling')
  const hint = page.getByRole('button', { name: '雷达与排名说明', exact: true })
  await hint.hover()
  await page.getByRole('tooltip').waitFor()
  assert.ok((await page.getByRole('tooltip').innerText()).includes('32%'))
  await page.mouse.move(180, 30)
  await page.getByRole('tooltip').waitFor({ state: 'detached' })
  const coverage = page.getByRole('button', { name: '统计口径与数据来源', exact: true })
  await coverage.focus()
  await page.getByRole('tooltip').waitFor()
  assert.ok((await page.getByRole('tooltip').innerText()).includes(profile.coverage.source))
  await page.keyboard.press('Escape')
  await page.getByRole('tooltip').waitFor({ state: 'detached' })
  await page.getByRole('button', { name: '近期概况', exact: true }).click()
  await page.locator('#sample-overview .profile-analysis').waitFor()
  await page.getByRole('button', { name: '近期概况', exact: true }).click()
  report.checks.push('Hover and keyboard hints reveal explanations; recent overview expands only on request')
  // A lobby can provide self history without an analyzed battle. Only exercise
  // that independent drawer when its real roster and player records already exist.
  if (state.battle?.players?.length && Object.values(state.scouts).some(scout => scout.profile?.matches?.length)) {
    await page.getByRole('button', { name: '对战助手', exact: true }).click()
    await page.locator('.player-match').first().click()
    const dialog = page.locator('dialog[open]')
    const dialogHint = dialog.getByRole('button', { name: '伤害占比说明', exact: true })
    await dialogHint.focus()
    await dialog.locator('[role=tooltip]').waitFor()
    assert.equal(await dialog.locator('[role=tooltip]').evaluate(element => { const box = element.getBoundingClientRect(); return box.left >= 0 && box.right <= innerWidth && box.top >= 0 && box.bottom <= innerHeight }), true)
    await page.keyboard.press('Escape')
    await dialog.locator('[role=tooltip]').waitFor({ state: 'detached' })
    assert.equal(await dialog.count(), 1)
    await page.keyboard.press('Escape')
    await page.locator('dialog').waitFor({ state: 'detached' })
    report.checks.push('Hints render in the native dialog layer; Escape dismisses hint first and dialog next')
  } else report.skipped.push('Battle drawer: no real analyzed roster in current observation')
  await page.getByRole('button', { name: '战绩分析', exact: true }).click()
  await page.screenshot({ path: path.join(output, `final-history${native ? '-native' : ''}.png`) })
  assert.deepEqual(errors, [])
  await fs.writeFile(path.join(output, `history-verification${native ? '-native' : ''}.json`), JSON.stringify(report, null, 2))
  process.stdout.write(JSON.stringify(report, null, 2))
  await browser.close()
}
main().catch(async error => { process.stderr.write(error.stack || String(error)); await browser?.close(); process.exitCode = 1 })
