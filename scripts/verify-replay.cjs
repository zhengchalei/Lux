// verify-replay.cjs checks the queried-match replay journey with an isolated bridge.
// The source profile comes from the running Go preview, but replay responses and
// writes are intercepted in-page. No fixture download or viewer request reaches
// the user's League client; real-client checks are reported separately.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** main exercises download, progress, explicit watch, terminal states, and match replacement.
 * A connected Go preview supplies the actual display profile and compiled assets.
 * The fixture bridge keeps these interaction checks separate from live game actions.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const out = path.resolve(__dirname, '../artifacts/replay')
  await fs.mkdir(out, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const source = (await (await page.request.post(`${base}/api/GetSnapshot`, { data: [] })).json()).result
    assert.ok(source.history?.matches?.length >= 2, 'preview has a real queried profile')
    const errors = []; page.on('pageerror', reason => errors.push(reason.message))
    await page.addInitScript(({ source }) => {
      const listeners = new Set()
      source.query = null
      const fixture = { source, commands: [], reads: 0, replayState: 'download', progress: null, hold: false, release: null }
      const notify = () => { source.revision++; for (const listener of listeners) listener() }
      window.__replayFixture = fixture
      window.__luxSubscribe = listener => { listeners.add(listener); return () => listeners.delete(listener) }
      window.__luxBackend = {
        GetRevision: async () => source.revision,
        GetSnapshot: async () => structuredClone(source),
        QuerySelf: async () => { source.query = structuredClone(source.history); notify() },
        GetMatchDetail: async (puuid, id) => structuredClone(source.history.matches.find(match => match.id === id)),
        GetReplay: async (puuid, id) => {
          fixture.reads++
          const state = fixture.replayState
          const result = { gameId: id, state, label: { download: '下载回放', downloading: '正在下载', watch: '观看回放', incompatible: '版本不兼容', missingOrExpired: '回放已失效' }[state] || '回放暂不可用', progress: fixture.progress, reason: state === 'incompatible' ? '本局回放与当前游戏版本不兼容，客户端无法播放。' : '客户端回放状态', canDownload: state === 'download', canWatch: state === 'watch' }
          if (fixture.hold) { fixture.hold = false; await new Promise(resolve => { fixture.release = resolve }) }
          return result
        },
        ReplayAction: async (puuid, id, action) => {
          fixture.commands.push([id, action])
          if (action === 'download') { fixture.replayState = 'downloading'; fixture.progress = 37 }
        },
      }
    }, { source })
    await page.goto(`${base}/?page=query`)
    await page.getByRole('button', { name: '查询自己', exact: true }).click()
    await page.getByRole('button', { name: '下载回放', exact: true }).waitFor()
    const first = await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id')
    await page.getByRole('button', { name: '下载回放', exact: true }).click()
    await page.getByRole('button', { name: '下载中 37%', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: '下载中 37%', exact: true }).isDisabled(), true)
    assert.equal(await page.evaluate(() => window.__replayFixture.commands.length), 1, 'download auto-launched viewer')
    await page.evaluate(() => { window.__replayFixture.replayState = 'watch'; window.__replayFixture.progress = null })
    await page.getByRole('button', { name: '观看回放', exact: true }).waitFor()
    await page.getByRole('button', { name: '观看回放', exact: true }).click()
    await page.waitForFunction(() => window.__replayFixture.commands.length === 2)
    // Selection changes during a held status read must publish the next game's
    // state independently and discard the reply captured for the preceding game.
    await page.evaluate(() => { const f = window.__replayFixture; f.replayState = 'missingOrExpired'; f.hold = true })
    await page.waitForFunction(() => !!window.__replayFixture.release, { timeout: 8000 })
    const secondRow = page.locator('.match-row').nth(1)
    const second = await secondRow.getAttribute('data-match-id')
    await secondRow.click()
    await page.evaluate(() => { window.__replayFixture.replayState = 'incompatible'; window.__replayFixture.release() })
    await page.getByRole('button', { name: '版本不兼容', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: '版本不兼容', exact: true }).isDisabled(), true)
    await page.getByRole('button', { name: '回放说明', exact: true }).hover()
    await page.getByRole('tooltip').waitFor()
    assert.match(await page.getByRole('tooltip').innerText(), /版本不兼容/)
    await page.mouse.move(180, 30)
    await page.getByRole('tooltip').waitFor({ state: 'detached' })
    const commands = await page.evaluate(() => window.__replayFixture.commands)
    assert.deepEqual(commands, [[Number(first), 'download'], [Number(first), 'watch']])
    const layouts = []
    for (const width of [1060, 1280, 1680]) {
      await page.setViewportSize({ width, height: 860 })
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        const bounds = await page.locator('.detail-tabs').evaluate(el => ({ width: el.clientWidth, scroll: el.scrollWidth }))
        assert.ok(bounds.scroll <= bounds.width + 1, 'detail controls overflow')
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        await page.screenshot({ path: path.join(out, `fixture-replay-${width}-${theme}.png`) })
        layouts.push({ width, theme })
      }
    }
    await page.evaluate(() => { window.__replayFixture.replayState = 'missingOrExpired' })
    await page.getByRole('button', { name: '刷新回放状态', exact: true }).click()
    await page.getByRole('button', { name: '回放已失效', exact: true }).waitFor()
    const reads = await page.evaluate(() => window.__replayFixture.reads)
    await page.waitForTimeout(1400)
    assert.equal(await page.evaluate(() => window.__replayFixture.reads), reads, 'terminal state keeps polling')
    for (let i = 0; i < 25; i++) await page.mouse.move(500 + i * 4, 300 + i * 3)
    assert.equal(await page.evaluate(() => window.__replayFixture.reads), reads, 'pointer causes replay reads')
    await page.getByRole('button', { name: '自动选禁', exact: true }).click()
    await page.locator('.replay-control').waitFor({ state: 'detached' })
    assert.deepEqual(errors, [])
    const report = { source: 'isolated replay bridge with real profile; no live commands', commands, replacement: [first, second], layouts, progress: 37, staleReplyDiscarded: true, noAutomaticWatch: true, terminalPollingStopped: true, pointerReads: 0, errors }
    await fs.writeFile(path.join(out, 'fixture-verification.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report))
  } finally { await browser.close() }
}
main().catch(reason => { console.error(reason); process.exitCode = 1 })
