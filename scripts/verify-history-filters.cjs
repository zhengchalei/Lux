const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

// verify-history-filters.cjs exercises the compact picker using an isolated
// renderer fixture with deliberately mixed queues, remakes, and champion counts.
// The running preview supplies compiled assets and public champion metadata.
// Fixture observations never submit commands to the real client or persist user
// preferences, and screenshots explicitly identify this synthetic data boundary.

/** main verifies filter intersections, exact scopes, dismissal, and player transitions.
 * The caller supplies a compiled preview through LUX_TEST_BASE. All player data
 * and backend methods are replaced before the page mounts; only public catalog
 * metadata comes from the preview's real sanitized snapshot.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const output = path.resolve(__dirname, '../artifacts/history-filters')
  await fs.mkdir(output, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const source = (await (await page.request.post(`${base}/api/GetSnapshot`, { data: [] })).json()).result
    assert.ok(source.database.champions.some(hero => hero.id === 99), 'Public Lux metadata exists')
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    await page.addInitScript(source => {
      const catalog = source.database.champions
      const hero = id => catalog.find(entry => entry.id === id)
      const records = []
      const definitions = [[420, 99, true], [420, 99, false], [440, 99, false], [430, 99, true], [450, 99, true], [2400, 99, false], [2400, 103, true], [3200, 99, true], [1700, 22, true], [830, 22, true], [420, 99, true, true]]
      // Many distinct champions require contained picker scrolling without adding
      // any records to the real application or changing its analysis preferences.
      for (const entry of catalog.filter(entry => ![99, 103].includes(entry.id)).slice(0, 42)) definitions.push([430, entry.id, entry.id % 2 === 0])
      for (const [queueId, championId, win, remake = false] of definitions) {
        const id = 880000 + records.length
        const players = catalog.slice(0, 10).map((entry, index) => ({ championId: index === 0 ? championId : entry.id, name: `验证玩家 ${index + 1}`, team: index < 5 ? 100 : 200, self: index === 0, kills: 5 + index, deaths: 3, assists: 7, damage: 14000 + index * 1000, gold: 11000 + index * 200, taken: 18000, heal: 900, towerDamage: 300 }))
        records.push({ id, queueId, championId, win, remake, players, name: hero(championId).name, queue: `验证队列 ${queueId}`, kills: 8, deaths: 3, assists: 9, cs: 130, damage: 14000, gold: 11000, duration: 1500, created: 1800000000000 - records.length * 3600000, team: 100, gameType: 'MATCHED_GAME', source: '隔离筛选验证', missingMetrics: [false, false, false, false, false] })
      }
      const kind = queue => queue === 420 ? 'solo' : queue === 440 ? 'flex' : [400, 430, 480, 490].includes(queue) ? 'normal' : [450, 3200, 3210, 3220, 3230].includes(queue) ? 'aram' : [2400, 3270].includes(queue) ? 'mayhem' : 'other'
      const usable = match => !match.remake && ![3200, 830].includes(match.queueId)
      const summarize = matches => {
        const games = matches.filter(usable)
        const counts = new Map()
        for (const game of games) {
          const entry = counts.get(game.championId) || { championId: game.championId, games: 0, wins: 0, kills: 8, deaths: 3, assists: 9, kda: 17 / 3 }
          entry.games++; entry.wins += Number(game.win); counts.set(game.championId, entry)
        }
        const wins = games.filter(match => match.win).length
        return { games: games.length, wins, winRate: games.length ? wins * 100 / games.length : 0, kills: games.length ? 8 : 0, deaths: games.length ? 3 : 0, assists: games.length ? 9 : 0, kda: games.length ? 17 / 3 : 0, recent: [], heroes: [...counts.values()].map(entry => ({ ...entry, winRate: entry.wins * 100 / entry.games })), contribution: { games: 0, damageGames: 0, takenGames: 0, goldGames: 0, damageShare: null, takenShare: null, goldShare: null } }
      }
      const summaries = { all: summarize(records) }
      for (const entry of ['solo', 'flex', 'normal', 'aram', 'mayhem', 'other']) summaries[`history:${entry}`] = summarize(records.filter(match => kind(match.queueId) === entry))
      const profile = { puuid: 'fixture-history-owner', name: '筛选验证#TEST', rank: '隔离测试数据', level: 100, requested: records.length, matches: records, captured: '2026-11-12T12:00:00Z', historyError: '', summary: summaries.all, summaries, coverage: { source: '隔离筛选验证', requested: records.length, fetched: records.length, scanned: records.length, valid: summaries.all.games, complete: 0, queueId: 0, note: '' } }
      Object.assign(source, { phase: 'None', room: null, battle: null, scouts: {}, query: null, history: profile, summonerSearch: null, automationEnabled: false })
      const calls = []; const listeners = new Set()
      window.__filterFixture = { source, calls, notify: () => { source.revision++; for (const listener of listeners) listener() } }
      window.__luxSubscribe = listener => { listeners.add(listener); return () => listeners.delete(listener) }
      const backend = {
        GetSnapshot: async () => structuredClone(source), GetRevision: async () => source.revision,
        GetDesktopStatus: async () => ({ trayAvailable: true }),
        GetReplay: async (puuid, id) => ({ gameId: id, state: 'unavailable', label: '验证回放不可用', reason: '隔离筛选验证', progress: null, canDownload: false, canWatch: false }),
      }
      window.__luxBackend = new Proxy(backend, { get: (target, method) => async (...args) => { calls.push([method, ...args]); if (!(method in target)) throw Error(`Unexpected fixture command: ${String(method)}`); return target[method](...args) } })
    }, source)
    await page.goto(`${base}/?page=history`)
    await page.locator('.combat-row').nth(9).waitFor()
    const trigger = page.getByRole('button', { name: '筛选战绩', exact: true })
    const panel = page.getByRole('dialog', { name: '战绩筛选', exact: true })
    const count = page.locator('.history-list-heading b')
    const summary = page.locator('.summary-combat')
    const records = await page.evaluate(() => window.__filterFixture.source.history.matches)
    assert.equal(await count.innerText(), String(records.length))
    await trigger.click()
    await panel.waitFor()
    await page.waitForFunction(() => document.activeElement === document.querySelector('.filter-search input'))
    assert.equal(await page.getByRole('textbox', { name: '搜索战绩英雄' }).evaluate(element => document.activeElement === element), true)
    assert.equal(await panel.locator('.filter-champions').evaluate(element => element.scrollHeight > element.clientHeight), true)
    await panel.locator('.filter-champions').evaluate(element => { element.scrollTop = element.scrollHeight })
    assert.equal(await panel.isVisible(), true, 'Contained scrolling keeps picker open')
    await trigger.click()
    await panel.waitFor({ state: 'hidden' })
    await trigger.click()
    await page.getByRole('textbox', { name: '搜索战绩英雄' }).fill('Lux')
    assert.equal(await panel.locator('.filter-champion').count(), 1)
    await panel.getByRole('button', { name: '筛选英雄 拉克丝', exact: true }).click()
    const expectedLux = records.filter(match => match.championId === 99)
    assert.equal(await count.innerText(), String(expectedLux.length))
    const allSummaryText = await summary.innerText()
    await page.getByRole('textbox', { name: '搜索战绩英雄' }).fill('光辉')
    assert.equal(await panel.locator('.filter-champion').count(), 1, 'Champion title is searchable')
    await page.getByRole('textbox', { name: '搜索战绩英雄' }).fill('不存在的英雄')
    await panel.getByText('当前类型中没有找到这个英雄', { exact: true }).waitFor()
    assert.equal(await count.innerText(), String(expectedLux.length), 'Search does not silently alter active champion')
    await panel.getByRole('button', { name: '清空英雄搜索', exact: true }).click()
    const modes = panel.getByRole('group', { name: '按比赛类型筛选', exact: true })
    for (const [name, queue, games] of [['单双排', 420, 2], ['灵活组排', 440, 1], ['匹配赛', 430, 43], ['大乱斗', 450, 1], ['海克斯大乱斗', 2400, 2]]) {
      await modes.getByRole('button', { name, exact: true }).click()
      const queues = name === '大乱斗' ? [450, 3200] : [queue]
      assert.equal(await count.innerText(), String(records.filter(match => queues.includes(match.queueId) && match.championId === 99).length))
      assert.ok((await summary.innerText()).includes(`${name} · ${games} 场`), `Summary follows exact ${name} eligibility`)
    }
    await modes.getByRole('button', { name: '单双排', exact: true }).click()
    const soloSummaryText = await summary.innerText()
    await panel.getByRole('button', { name: '仅看失败', exact: true }).click()
    assert.equal(await count.innerText(), '1')
    assert.equal(await summary.innerText(), soloSummaryText)
    assert.equal(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'), String(records[1].id))
    await panel.getByRole('button', { name: '完成', exact: true }).click()
    await page.getByRole('button', { name: '移除胜负筛选', exact: true }).click()
    assert.equal(await count.innerText(), '3', 'Raw remake remains inspectable')
    assert.equal(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'), String(records[1].id), 'Clearing retains currently displayed record')
    await trigger.click()
    await page.getByRole('textbox', { name: '搜索战绩英雄' }).fill('Ahri')
    await panel.getByText('当前类型中没有找到这个英雄', { exact: true }).waitFor()
    await modes.getByRole('button', { name: '海克斯大乱斗', exact: true }).click()
    await panel.getByRole('button', { name: '筛选英雄 阿狸', exact: true }).click()
    await panel.getByRole('button', { name: '仅看失败', exact: true }).click()
    await panel.getByRole('button', { name: '完成', exact: true }).click()
    assert.equal(await count.innerText(), '0')
    await page.getByText('没有符合条件的对局', { exact: true }).waitFor()
    await page.locator('.history-filter-empty').getByRole('button', { name: '重置筛选', exact: true }).click()
    assert.equal(await count.innerText(), String(records.length))
    assert.equal(await summary.innerText(), allSummaryText)
    await trigger.click()
    await page.keyboard.press('Escape')
    await panel.waitFor({ state: 'hidden' })
    assert.equal(await trigger.evaluate(element => document.activeElement === element), true)
    await trigger.click()
    await page.locator('.history-overview').click()
    await panel.waitFor({ state: 'hidden' })
    await trigger.click()
    await panel.waitFor()
    await page.setViewportSize({ width: 1060, height: 700 })
    await panel.waitFor({ state: 'hidden' })
    const layouts = []
    for (const size of [{ width: 1060, height: 700 }, { width: 1280, height: 860 }, { width: 1680, height: 1000 }]) {
      await page.setViewportSize(size)
      for (const theme of ['light', 'dark']) {
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await trigger.click()
        await panel.waitFor()
        await page.waitForFunction(() => document.activeElement === document.querySelector('.filter-search input'))
        const bounds = await panel.boundingBox()
        assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= size.width && bounds.y + bounds.height <= size.height, JSON.stringify({ size, bounds }))
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        assert.equal(await panel.evaluate(element => element.scrollWidth > element.clientWidth), false)
        layouts.push({ ...size, theme, bounds })
        await page.screenshot({ path: path.join(output, `fixture-filter-${size.width}-${theme}.png`) })
        await page.keyboard.press('Escape')
        await panel.waitFor({ state: 'hidden' })
      }
    }
    await trigger.click()
    await modes.getByRole('button', { name: '灵活组排', exact: true }).click()
    await page.evaluate(() => {
      const fixture = window.__filterFixture
      fixture.source.history.puuid = 'second-fixture-account'
      fixture.source.history.name = '第二个账号#TEST'
      fixture.source.account = { ...fixture.source.account, puuid: 'second-fixture-account' }
      fixture.notify()
    })
    await panel.waitFor({ state: 'hidden' })
    await page.waitForFunction(() => document.querySelector('.history-list-heading b').textContent === String(window.__filterFixture.source.history.matches.length))
    assert.equal(await page.locator('.filter-chip').count(), 0)
    await trigger.click()
    await page.waitForTimeout(120)
    // The store's independent recovery timer may probe revision while input runs;
    // only additional observation or detail requests would indicate pointer-driven work.
    const beforePointer = await page.evaluate(() => window.__filterFixture.calls.filter(([method]) => method !== 'GetRevision').length)
    for (let i = 0; i < 80; i++) await page.mouse.move(190 + i * 11 % 700, 180 + i * 23 % 420)
    const afterPointer = await page.evaluate(() => window.__filterFixture.calls.filter(([method]) => method !== 'GetRevision').length)
    assert.equal(afterPointer, beforePointer, 'Pointer movement never requests backend data')
    const calls = await page.evaluate(() => window.__filterFixture.calls)
    assert.ok(calls.every(([method]) => ['GetSnapshot', 'GetRevision', 'GetDesktopStatus', 'GetReplay'].includes(method)))
    assert.deepEqual(errors, [])
    const report = { source: 'isolated mixed-mode renderer fixture; no real client writes', records: records.length, layouts, exactRankedScopes: true, heroAliasAndTitleSearch: true, retainedIntersections: true, summaryEligibility: true, keyboardAndLightDismissal: true, accountReset: true, containedScroll: true, pointerBackendCalls: afterPointer - beforePointer, errors }
    await fs.writeFile(path.join(output, 'filter-verification.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report, null, 2))
  } finally { await browser.close() }
}
main().catch(error => { console.error(error); process.exitCode = 1 })
