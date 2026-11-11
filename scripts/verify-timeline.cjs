// verify-timeline.cjs combines live client evidence with an isolated compatible-model fixture.
// Timeline, participant identity, images, and comparison phases come from the real
// Go preview. The local provider labels every response as protocol test output;
// it verifies integration rather than claiming actual model coaching quality.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const http = require('node:http')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** main checks live phase comparison and explicit, cancelable model integration.
 * Provider configuration belongs to the preview's isolated LOCALAPPDATA directory.
 * No real provider key or external model service participates in this verification.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const out = path.resolve(__dirname, '../artifacts/timeline')
  await fs.mkdir(out, { recursive: true })
  let mode = 'valid'; let calls = 0; let canceled = 0; let lastFacts
  const timers = new Set()
  const provider = http.createServer(async (request, response) => {
    const chunks = []; for await (const chunk of request) chunks.push(chunk)
    assert.equal(request.url, '/v1/chat/completions')
    assert.equal(request.headers.authorization, 'Bearer timeline-protocol-test-key')
    const body = JSON.parse(Buffer.concat(chunks).toString())
    assert.equal(body.model, 'protocol-fixture-model')
    lastFacts = JSON.parse(body.messages[1].content); calls++
    assert.equal(lastFacts.players.length, 2)
    assert.equal(JSON.stringify(lastFacts).includes('puuid'), false)
    assert.equal(Object.hasOwn(lastFacts.players[0], 'name'), false)
    const first = lastFacts.windows[0]
    const second = lastFacts.windows.find(window => window.events.length) || first
    const makeFinding = (window, verdict) => ({ windowId: window.id, verdict, title: '协议测试：阶段依据跳转', reason: '使用实际客户端阶段与事件验证引用绑定，非真实模型判断。', opponent: '协议测试响应，只验证所选对手的数据传递。', advice: '正式接入模型后，根据此阶段实际事件给出条件性复盘建议。', evidenceIds: [window.id, ...(window.events[0] ? [window.events[0].id] : [])] })
    const result = { summary: '协议测试响应：验证真实时间线与模型接口联调，不代表真实模型评价。', insights: [makeFinding(first, 'neutral'), makeFinding(second, 'review')], goals: ['协议测试：确认引用可定位到实际事件'] }
    if (mode === 'bad') result.insights[0].evidenceIds = ['E999999']
    const send = () => { if (!response.destroyed) { response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ choices: [{ message: { content: JSON.stringify(result) } }] })) } }
    if (mode === 'slow') {
      const timer = setTimeout(() => { timers.delete(timer); send() }, 15000); timers.add(timer)
      response.on('close', () => { canceled++; clearTimeout(timer); timers.delete(timer) })
    } else send()
  })
  await new Promise(resolve => provider.listen(19375, '127.0.0.1', resolve))
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const errors = []; let timelineReads = 0
    page.on('pageerror', error => errors.push(error.message))
    page.on('request', request => { if (request.url().endsWith('/api/GetTimeline')) timelineReads++ })
    const api = async (method, args = []) => (await (await page.request.post(`${base}/api/${method}`, { data: args })).json())
    await page.goto(`${base}/?page=query`)
    await page.getByRole('combobox', { name: '查询准确队列' }).selectOption('420')
    await page.getByRole('button', { name: '查询自己', exact: true }).click()
    let state
    for (let attempt = 0; attempt < 150; attempt++) {
      state = (await api('GetSnapshot')).result
      if (state.query && !state.queryLoading && state.query.coverage.queueId === 420) break
      await page.waitForTimeout(300)
    }
    assert.ok(state.query && !state.queryLoading && state.query.coverage.queueId === 420)
    await page.locator('.history-overview').waitFor({ timeout: 60000 })
    await page.getByRole('button', { name: '时间线复盘', exact: true }).click()
    await page.locator('.review-phase').waitFor({ timeout: 30000 })
    const matchID = Number(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'))
    let source = (await api('GetTimeline', [state.query.puuid, matchID, 0])).result
    assert.ok(source.timeline.frames.length > 2 && source.timeline.events.length > 0)
    const originalOpponent = source.opponent
    const select = page.getByRole('combobox', { name: '选择对比选手' })
    if (!originalOpponent) { const options = await select.locator('option').all(); await select.selectOption(await options[1].getAttribute('value')); await page.getByRole('status').filter({ hasText: '正在读取本局真实时间线' }).waitFor({ state: 'detached' }) }
    const current = Number(await select.inputValue())
    const alternate = source.timeline.players.find(player => player.team !== source.timeline.players.find(player => player.id === source.timeline.ownId).team && player.id !== current)
    await select.selectOption(String(alternate.id))
    await page.getByRole('status').filter({ hasText: '正在读取本局真实时间线' }).waitFor({ state: 'detached' })
    source = (await api('GetTimeline', [state.query.puuid, matchID, alternate.id])).result
    assert.equal(source.opponent, alternate.id)
    await select.selectOption(String(current))
    await page.getByRole('status').filter({ hasText: '正在读取本局真实时间线' }).waitFor({ state: 'detached' })
    await page.waitForTimeout(200)
    const readsBeforeLocal = timelineReads
    await page.locator('.review-phase-list button').nth(2).click()
    await page.getByRole('group', { name: '时间线比较指标' }).getByRole('button', { name: '补刀', exact: true }).click()
    const phaseBeforeSnapshot = await page.locator('.review-phase-list button[aria-pressed=true]').innerText()
    await api('GetMatchDetail', [state.query.puuid, matchID])
    await page.waitForTimeout(1100)
    assert.equal(await page.locator('.review-phase-list button[aria-pressed=true]').innerText(), phaseBeforeSnapshot, 'unrelated snapshot reset the selected phase')
    for (let i = 0; i < 20; i++) await page.mouse.move(800 + i * 3, 450 + i * 2)
    assert.equal(timelineReads, readsBeforeLocal, 'local phase or pointer triggered timeline reads')
    assert.equal(calls, 0, 'model invoked before explicit action')
    const layouts = []
    for (const width of [1060, 1280, 1680]) {
      await page.setViewportSize({ width, height: 860 })
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        const bounds = await page.locator('.timeline-review').evaluate(el => ({ width: el.clientWidth, scroll: el.scrollWidth }))
        assert.ok(bounds.scroll <= bounds.width + 1, 'timeline panel overflow')
        await page.screenshot({ path: path.join(out, `live-timeline-${width}-${theme}.png`) })
        layouts.push({ width, theme, bounds })
      }
    }
    await page.setViewportSize({ width: 1280, height: 860 }); await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
    await page.getByRole('button', { name: 'AI 设置', exact: true }).click()
    await page.getByRole('textbox', { name: 'AI 接口地址', exact: true }).fill('http://127.0.0.1:19375/v1')
    await page.getByRole('textbox', { name: 'AI 模型名称', exact: true }).fill('protocol-fixture-model')
    await page.getByLabel('AI API Key', { exact: true }).fill('timeline-protocol-test-key')
    await page.getByRole('button', { name: '保存设置', exact: true }).click()
    await page.getByRole('form', { name: 'AI 服务设置' }).waitFor({ state: 'detached' })
    assert.equal(JSON.stringify(await api('GetAISettings')).includes('timeline-protocol-test-key'), false)
    const persisted = await fs.readFile(path.join(out, 'isolated-settings/Lux/ai-provider.json'), 'utf8')
    assert.equal(persisted.includes('timeline-protocol-test-key'), false)
    await page.getByRole('button', { name: 'AI 分析本局', exact: true }).click()
    await page.getByRole('region', { name: 'AI 复盘结果', exact: true }).waitFor({ timeout: 20000 })
    assert.equal(calls, 1)
    const eventCitation = page.locator('.review-citations button').filter({ hasText: '事件 E' }).first()
    if (await eventCitation.count()) { await eventCitation.click(); assert.equal(await page.locator('.review-events li.highlighted').count(), 1) }
    await page.locator('.review-ai-result').screenshot({ path: path.join(out, 'fixture-ai-result.png') })
    mode = 'bad'
    await page.getByRole('button', { name: '重新分析', exact: true }).click()
    await page.getByRole('alert').filter({ hasText: 'AI 引用与对应时间段不符' }).waitFor()
    mode = 'slow'
    await page.getByRole('button', { name: '重新分析', exact: true }).click()
    await page.getByRole('button', { name: '取消分析', exact: true }).waitFor()
    await page.waitForFunction(() => document.querySelector('.review-ai-progress'))
    // The fixture receives the request before cancellation, distinguishing actual
    // transport cancellation from merely hiding an in-flight UI indicator.
    for (let i = 0; i < 50 && calls < 3; i++) await page.waitForTimeout(40)
    await page.getByRole('button', { name: '取消分析', exact: true }).click()
    for (let i = 0; i < 50 && !canceled; i++) await page.waitForTimeout(40)
    assert.ok(canceled > 0, 'provider transport was not canceled')
    await page.getByRole('button', { name: '重新分析', exact: true }).click()
    for (let i = 0; i < 50 && calls < 4; i++) await page.waitForTimeout(40)
    const oldMatch = matchID
    await page.locator('.match-row').nth(1).click()
    await page.locator('.review-phase').waitFor({ timeout: 30000 })
    for (let i = 0; i < 50 && canceled < 2; i++) await page.waitForTimeout(40)
    assert.ok(canceled >= 2, 'match replacement did not cancel provider transport')
    assert.equal(await page.locator('.review-ai-result').count(), 0, 'old model result survived a changed match')
    const selectedMatch = Number(await page.locator('.match-row[aria-pressed=true]').getAttribute('data-match-id'))
    assert.notEqual(selectedMatch, oldMatch)
    assert.deepEqual(errors, [])
    const report = { source: 'real client timeline, isolated local model fixture', fixtureModel: true, gameId: matchID, frames: source.timeline.frames.length, events: source.timeline.events.length, phases: source.windows.length, own: source.timeline.ownId, roleOpponent: originalOpponent, providerRequests: calls, canceledRequests: canceled, identityRedacted: true, savedKeyEncrypted: true, citationRejection: true, phaseAndPointerReads: false, layouts, errors }
    await fs.writeFile(path.join(out, 'verification.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report))
  } finally {
    await browser.close(); for (const timer of timers) clearTimeout(timer)
    provider.closeAllConnections(); await new Promise(resolve => provider.close(resolve))
  }
}
main().catch(error => { console.error(error); process.exitCode = 1 })
