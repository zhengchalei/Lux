// verify-summoner-search.cjs exercises the real read-only name lookup journey.
// The running Go preview supplies actual aliases, regional profiles, icons, and
// history. No fixture identity or client-changing game action enters this check.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** main checks confirmation, keyboard access, stale selection, and responsive candidate layouts.
 * It derives the test name from the real connected account. Every service result
 * is checked against its stable identity rather than an assumed numeric tag.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const out = path.resolve(__dirname, '../artifacts/name-search')
  await fs.mkdir(out, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const errors = []
    let iconReads = 0
    page.on('pageerror', error => errors.push(error.message))
    page.on('request', request => { if (request.url().endsWith('/api/GetSummonerIcon')) iconReads++ })
    const api = async (method, args = []) => (await (await page.request.post(`${base}/api/${method}`, { data: args })).json())
    const account = (await api('GetSnapshot')).result.account
    assert.ok(account?.name.includes('#'), 'connected account has a full Riot ID')
    const name = account.name.slice(0, account.name.lastIndexOf('#'))
    await page.goto(`${base}/?page=query`)
    const input = page.getByRole('textbox', { name: '查询召唤师', exact: true })
    await input.fill(name)
    await input.press('Enter')
    await page.getByRole('region', { name: '同名召唤师候选', exact: true }).waitFor()
    let source
    await assert.doesNotReject(async () => {
      for (let attempt = 0; attempt < 120; attempt++) {
        source = (await api('GetSnapshot')).result
        if (!source.queryLoading && source.summonerSearch?.name === name) return
        await page.waitForTimeout(400)
      }
      throw new Error('candidate metadata did not finish')
    })
    const search = source.summonerSearch
    assert.ok(search.candidates.length > 0, 'real alias candidates returned')
    assert.equal(source.query, null, 'name-only lookup loaded history before confirmation')
    await page.waitForFunction(count => document.querySelectorAll('.summoner-candidate').length === count, search.candidates.length)
    await page.getByText('资料读取中…', { exact: true }).waitFor({ state: 'detached' })
    const available = search.candidates.filter(candidate => candidate.available)
    assert.ok(available.length > 0, 'current-region profiles returned')
    await page.waitForFunction(count => [...document.querySelectorAll('.summoner-icon img')].filter(img => img.complete && img.naturalWidth).length === count, available.length)
    const metadataReads = iconReads
    for (let i = 0; i < 25; i++) await page.mouse.move(600 + i * 3, 220 + i * 3)
    assert.equal(iconReads, metadataReads, 'pointer movement triggers profile asset requests')
    const layouts = []
    for (const width of [1060, 1280, 1680]) {
      await page.setViewportSize({ width, height: 860 })
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        const bounds = await page.locator('.summoner-candidates').evaluate(el => ({ width: el.clientWidth, scroll: el.scrollWidth, height: el.clientHeight, total: el.scrollHeight }))
        assert.ok(bounds.scroll <= bounds.width + 1, 'candidate grid overflows')
        layouts.push({ width, theme, bounds })
        await page.screenshot({ path: path.join(out, `real-candidates-${width}-${theme}.png`) })
      }
    }
    await page.setViewportSize({ width: 1280, height: 860 })
    await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
    // Editing the submitted name hides old cards before another search can finish.
    await input.fill(`${name}改`)
    assert.equal(await page.locator('.summoner-candidate').count(), 0)
    await input.fill(name)
    const chosen = available.find(candidate => candidate.puuid === account.puuid) || available[0]
    const button = page.getByRole('button', { name: `查询 ${chosen.name}`, exact: true })
    await button.focus()
    await page.keyboard.press('Enter')
    await page.locator('.history-overview').waitFor({ timeout: 60000 })
    assert.equal(await input.inputValue(), chosen.name)
    source = (await api('GetSnapshot')).result
    assert.equal(source.query.puuid, chosen.puuid, 'selected alias loaded a different identity')
    const stale = await api('SelectSummoner', [search.id, chosen.puuid, 0])
    assert.match(stale.error, /候选账号已更新/)
    assert.equal(await page.locator('.summoner-candidate').count(), 0)
    await page.screenshot({ path: path.join(out, 'real-confirmed-history.png') })
    // A full alias retains the direct-query path and never opens candidate selection.
    await input.fill(chosen.name)
    await input.press('Enter')
    await page.locator('.history-overview').waitFor({ timeout: 60000 })
    assert.equal((await api('GetSnapshot')).result.summonerSearch, null)
    await input.fill('Lux无此召唤师零玖柒')
    await input.press('Enter')
    await page.locator('.summoner-no-results').waitFor({ timeout: 20000 })
    assert.equal((await api('GetSnapshot')).result.queryError, '')
    assert.deepEqual(errors, [])
    const report = { source: 'real Riot account aliases and current-region LCU profiles', name, candidates: search.candidates, selected: chosen.name, historyIdentity: chosen.puuid, iconReads: metadataReads, staleSelectionRejected: true, directAlias: true, emptyResults: true, layouts, errors }
    await fs.writeFile(path.join(out, 'live-verification.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify({ candidates: search.candidates.length, available: available.length, icons: metadataReads, selected: chosen.name, emptyResults: true, errors }))
  } finally { await browser.close() }
}
main().catch(error => { console.error(error); process.exitCode = 1 })
