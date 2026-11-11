// verify-quick-draft.cjs checks compact selection interactions with isolated protocol fixtures.
// The browser receives a test-only backend, so champion and appearance clicks
// never modify the real client. Reports label fixture layouts separately from
// live desktop observations and preserve screenshots for both supported themes.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

/** main verifies selection transitions, skin updates, and compact layout bounds.
 * A running real preview provides compiled assets and its public champion catalog.
 * All game-changing methods are intercepted by the test-only in-page adapter.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const out = path.resolve(__dirname, '../artifacts/quick-draft')
  await fs.mkdir(out, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 344, height: 680 } })
    const raw = await (await page.request.post(`${base}/api/GetSnapshot`, { data: [] })).json()
    const source = raw.result
    const errors = []
    page.on('pageerror', e => errors.push(e.message))
    await page.addInitScript(({ source }) => {
      const room = { sessionKey: 'fixture-selection', mode: 'aram', queueId: 450, current: 0, skinId: 0, canSwap: false, canChoose: true, choices: [103, 22, 999999], bench: [81, 51], available: [103, 22, 999999], banned: [], allies: [{ cell: 1, champion: 0, local: true, locked: false, position: '' }], enemies: [] }
      source.room = room; source.phase = 'ChampSelect'; source.connected = true
      // Explicit test effects keep presentation checks independent from changing
      // public source values; these never become a live client's balance data.
      const effects = [{ label: '造成伤害', display: '+5%', helpful: true }, { label: '承受伤害', display: '+10%', helpful: false }, { label: '治疗效果', display: '+20%', helpful: true }, { label: '护盾效果', display: '-10%', helpful: false }, { label: '技能急速', display: '+10', helpful: true }]
      source.database.champions.find(hero => hero.id === 103).aram.adjustments = effects
      source.database.champions.find(hero => hero.id === 22).aram.adjustments = []
      const events = []; const listeners = new Set(); const panelState = { enabled: true, pinned: true, visible: true, attached: true, collapsed: false }
      const notify = () => { source.revision++; for (const listener of listeners) listener() }
      window.__quickFixture = { source, events, notify }
      window.__luxSubscribe = listener => { listeners.add(listener); return () => listeners.delete(listener) }
      window.__luxBackend = {
        GetRevision: async () => source.revision,
        GetSnapshot: async () => structuredClone(source),
        GetDraftSnapshot: async () => structuredClone({ revision: source.revision, connected: source.connected, phase: source.phase, room: source.room, database: source.database }),
        GetDraftPanelStatus: async () => ({ ...panelState }),
        SetDraftPanelCollapsed: async value => { panelState.collapsed = value; notify() },
        SetDraftPanel: async (...args) => { events.push(['panel', ...args]) },
        GetDraftSkins: async (key, champion) => ({ sessionKey: key, championId: champion, selected: room.skinId, enabled: true, skins: Array.from({ length: 16 }, (_, i) => ({ id: champion * 1000 + i, name: i === 0 ? '原始皮肤' : `测试皮肤 ${i}`, base: i === 0 })) }),
        GetSkinPreview: async () => `/portraits/${room.current}.jpg`,
        SelectDraftChampion: async (key, current, target) => {
          events.push(['hero', key, current, target])
          if (key !== room.sessionKey || current !== room.current) throw Error('房间已变化')
          if (!room.canChoose) {
            room.bench = [...new Set([...room.bench.filter(id => id !== target), current])]
            room.available = [...new Set([...room.available.filter(id => id !== target), current])]
          }
          room.current = target; room.skinId = target * 1000; notify()
        },
        SetDraftSkin: async (key, champion, skin) => {
          events.push(['skin', key, champion, skin])
          if (key !== room.sessionKey || champion !== room.current) throw Error('英雄已变化')
          room.skinId = skin; notify()
        },
        InspectChampion: async id => {
          events.push(['inspect', id])
          const hero = source.database.champions.find(hero => hero.id === id)
          if (hero) { hero.mayhem.adjustments = structuredClone(effects); source.database.updated = String(source.revision + 1); notify() }
        },
      }
    }, { source })
    await page.goto(`${base}/?panel=draft`)
    await page.getByRole('button', { name: '预选 阿狸', exact: true }).waitFor()
    assert.equal(await page.locator('.quick-choices > button').count(), 3, 'personal cards missing')
    assert.equal(await page.getByRole('button', { name: '预选 英雄 999999', exact: true }).count(), 1, 'missing catalog entry discarded')
    assert.equal(await page.locator('.skin-tile').count(), 0, 'skins shown before first champion')
    assert.equal(await page.getByRole('button', { name: '选用 伊泽瑞尔', exact: true }).isDisabled(), true, 'bench active during personal selection')
    await page.getByRole('button', { name: '预选 阿狸', exact: true }).hover()
    await page.waitForFunction(() => document.querySelectorAll('.quick-buffs > span').length === 5)
    assert.equal(await page.locator('.quick-buffs .buff-up').count(), 3)
    assert.equal(await page.locator('.quick-buffs .buff-down').count(), 2)
    assert.equal(await page.locator('.quick-balance').innerText().then(text => text.includes('候选预览')), true)
    await page.locator('.quick-choices > button').nth(1).hover()
    await page.getByText('无额外 Buff / Debuff', { exact: true }).waitFor()
    await page.getByRole('button', { name: '预选 英雄 999999', exact: true }).hover()
    await page.getByText('调整数据待更新', { exact: true }).waitFor()
    assert.deepEqual(await page.evaluate(() => window.__quickFixture.events), [], 'balance inspection submits a client command')
    await page.screenshot({ path: path.join(out, 'fixture-personal-choices.png') })
    await page.getByRole('button', { name: '预选 阿狸', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('.quick-current h2')?.textContent === '阿狸')
    await page.waitForFunction(() => document.querySelectorAll('.quick-buffs > span').length === 5)
    assert.equal(await page.getByRole('button', { name: '预选 阿狸', exact: true }).getAttribute('aria-pressed'), 'true')
    await page.waitForFunction(() => document.querySelectorAll('.skin-tile').length === 16)
    const personalCommands = await page.evaluate(() => [...window.__quickFixture.events])
    assert.deepEqual(personalCommands, [['hero', 'fixture-selection', 0, 103]])
    // The client ends personal selection independently from hero assignment. Publish
    // that transition explicitly to verify stale personal cards leave the panel.
    await page.evaluate(() => {
      const fixture = window.__quickFixture; const room = fixture.source.room
      room.canChoose = false; room.canSwap = true; room.choices = []
      room.available = [103, 81, 51]; fixture.notify()
    })
    await page.locator('.quick-personal').waitFor({ state: 'detached' })
    assert.equal(await page.getByRole('button', { name: '选用 伊泽瑞尔', exact: true }).isEnabled(), true)
    await page.evaluate(() => {
      const fixture = window.__quickFixture; const room = fixture.source.room
      room.current = 99; room.skinId = 99000
      room.bench = [103, 22, 81, 51, 202, 18, 21, 222, 110]
      room.available = [99, ...room.bench]; fixture.events.length = 0; fixture.notify()
    })
    await page.getByRole('button', { name: '选用 阿狸', exact: true }).waitFor()
    assert.equal(await page.locator('.skin-tile').count(), 16)
    await page.getByRole('button', { name: '选用 阿狸', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('.quick-current h2')?.textContent === '阿狸')
    await page.getByRole('button', { name: '使用皮肤 测试皮肤 1', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('[aria-label="使用皮肤 测试皮肤 1"]')?.getAttribute('aria-pressed') === 'true')
    await page.getByRole('button', { name: '收起面板', exact: true }).click()
    await page.getByRole('button', { name: '展开大乱斗助手', exact: true }).click()
    await page.locator('.quick-current').waitFor()
    const afterCommands = await page.evaluate(() => window.__quickFixture.events.filter(e => e[0] !== 'panel'))
    assert.deepEqual(afterCommands, [['hero', 'fixture-selection', 99, 103], ['skin', 'fixture-selection', 103, 103001]])
    for (const size of [[344, 680], [310, 420], [430, 800]]) {
      await page.setViewportSize({ width: size[0], height: size[1] })
      await page.locator('.companion-content').evaluate(el => { el.scrollTop = 0 })
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'horizontal overflow')
        await page.screenshot({ path: path.join(out, `fixture-panel-${size[0]}-${theme}.png`) })
      }
    }
    for (let i = 0; i < 30; i++) await page.mouse.move(20 + i * 5, 170 + i * 7)
    await page.mouse.wheel(0, 600)
    assert.equal(await page.evaluate(() => window.__quickFixture.events.filter(e => e[0] !== 'panel').length), 2, 'pointer input submits commands')
    await page.evaluate(() => {
      const f = window.__quickFixture
      f.source.room.mode = 'mayhem'; f.source.room.current = 103
      f.source.room.available = [103]; f.source.room.bench = [81]
      f.source.database.patch = 'fixture-mayhem'
      for (const id of [103, 81]) f.source.database.champions.find(hero => hero.id === id).mayhem.adjustments = null
      f.notify()
    })
    await page.waitForFunction(() => window.__quickFixture.events.some(e => e[0] === 'inspect' && e[1] === 81))
    await page.getByRole('button', { name: '选用 伊泽瑞尔', exact: true }).hover()
    await page.waitForFunction(() => document.querySelectorAll('.quick-buffs > span').length === 5)
    assert.equal(await page.locator('.balance-heading strong').innerText(), '伊泽瑞尔')
    await page.evaluate(() => { window.__quickFixture.source.room = null; window.__quickFixture.notify() })
    await page.getByRole('heading', { name: '等待大乱斗选人' }).waitFor()
    assert.equal(await page.locator('.skin-tile').count(), 0, 'old skins survive a closed session')
    await page.setViewportSize({ width: 1280, height: 860 })
    await page.goto(`${base}/?page=draft`)
    await page.getByRole('button', { name: '预选 阿狸', exact: true }).waitFor()
    assert.equal(await page.locator('.quick-choices > button').count(), 3, 'main workspace lost personal cards')
    // Only the personal list changes here; the current hero and bench stay equal.
    // This protects the main store's identity retention from suppressing card updates.
    await page.evaluate(() => {
      window.__quickFixture.source.room.choices = [103, 22]
      window.__quickFixture.notify()
    })
    await page.waitForFunction(() => document.querySelectorAll('.quick-choices > button').length === 2)
    assert.deepEqual(errors, [])
    const report = { source: 'isolated protocol fixture; no live game actions', personalChoices: 3, missingCatalogRetained: true, mainWorkspaceCardUpdates: true, personalCommands, finalizationTransition: true, viewportChecks: 6, commands: afterCommands, pointerWrites: 0, sessionCleanup: true, foldRestore: true, errors }
    await fs.writeFile(path.join(out, 'fixture-verification.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report))
  } finally { await browser.close() }
}
main().catch(error => { console.error(error); process.exitCode = 1 })
