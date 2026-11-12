const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.LUX_PLAYWRIGHT || 'playwright')

// verify-player-explorer.cjs exercises public participant navigation with isolated
// renderer profiles. Same-name accounts have different tags and PUUIDs, so exact
// routing, nested back navigation, and abandoned responses can be distinguished.
// Only public champion metadata comes from the supplied running preview; mocked
// methods never submit client commands or persist application preferences.

/** main verifies the player journey, modal layouts, and request ownership.
 * LUX_TEST_BASE selects the preview serving the current renderer. All account
 * observations are replaced before mounting; screenshots use explicit fixture names.
 */
async function main() {
  const base = process.env.LUX_TEST_BASE || 'http://127.0.0.1:19373'
  const output = path.resolve(__dirname, '../artifacts/player-explorer')
  await fs.mkdir(output, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 860 } })
    const source = (await (await page.request.post(`${base}/api/GetSnapshot`, { data: [] })).json()).result
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    await page.addInitScript(source => {
      const catalog = source.database.champions
      const names = { root: '来源玩家#TEST', alpha: '同名#100', beta: '同名#200' }
      const ids = ['root', 'alpha', 'beta', 'gamma', 'delta', 'epsilon', 'zeta', 'eta', 'theta', '']
      const summarize = records => {
        const heroes = new Map()
        for (const match of records) {
          const hero = heroes.get(match.championId) || { championId: match.championId, games: 0, wins: 0, kills: 8, deaths: 3, assists: 9, kda: 17 / 3 }
          hero.games++; hero.wins += Number(match.win); heroes.set(match.championId, hero)
        }
        const wins = records.filter(match => match.win).length
        return { signals: { score: records.length ? { games: records.length, total: 8.6, parts: [1,.6,2,1,1,1,1,1], label: records.length >= 8 ? '通天代' : '' } : null, flashD: Math.ceil(records.length/2), flashF: Math.floor(records.length/2), gankGames: records.filter(m => m.earlyGank).length, gankDeaths: records.filter(m => m.earlyGank).reduce((n,m) => n+m.earlyGank.deaths.length,0) }, games: records.length, wins, winRate: records.length ? wins * 100 / records.length : 0, kills: 8, deaths: 3, assists: 9, kda: 17 / 3, recent: [], heroes: [...heroes.values()].sort((a, b) => b.games - a.games).map(hero => ({ ...hero, winRate: hero.wins * 100 / hero.games })), contribution: { games: 0, damageGames: 0, takenGames: 0, goldGames: 0, damageShare: null, takenShare: null, goldShare: null } }
      }
      const profiles = {}
      for (const [number, owner] of ['root', 'alpha', 'beta'].entries()) {
        const records = Array.from({ length: 80 }, (_, index) => {
          const championId = index % 3 === 0 ? 99 : 103
          return { earlyGank: index < 10 && index % 2 === 0 ? { deaths: [120000,240000] } : undefined, id: 900000 + number * 1000 + index, championId, name: catalog.find(hero => hero.id === championId).name, queue: index % 2 ? '极地大乱斗' : '单双排位', queueId: index === 8 ? 440 : index % 2 ? 450 : 420, win: index % 3 !== 1, kills: 8, deaths: 3, assists: 9, cs: 130, damage: 18000, gold: 11000, duration: 1500, created: 1800000000000 - index * 3600000, team: 100, source: '隔离玩家验证', gameType: 'MATCHED_GAME', remake: false, missingMetrics: [false, false, false, false, false], players: ids.map((puuid, slot) => ({ puuid, name: names[puuid] || (puuid ? `验证玩家 ${slot}#TEST` : '身份未公开'), championId: catalog[slot].id, team: slot < 5 ? 100 : 200, self: puuid === owner, kills: 5 + slot, deaths: 3, assists: 7, damage: 14000 + slot * 1000, gold: 11000, taken: 18000, heal: 900, towerDamage: 300 })) }
        })
        const summary = summarize(records)
        const summaries = { all: summary }
        for (const kind of ['solo', 'flex', 'normal', 'aram', 'mayhem', 'other']) summaries[`history:${kind}`] = summarize(records.filter(match => kind === 'solo' ? match.queueId === 420 : kind === 'aram' ? match.queueId === 450 : kind === 'flex' ? match.queueId === 440 : false))
        profiles[owner] = { puuid: owner, name: names[owner], rank: '钻石 IV · 29 LP', level: 192, iconId: -1, captured: '2026-11-12T12:00:00Z', historyError: '', requested: 80, matches: records, summary, summaries, coverage: { source: '隔离玩家验证', requested: 80, fetched: 80, scanned: 80, valid: 80, complete: 80, queueId: 0, note: '' } }
      }
      Object.assign(source, { phase: 'None', room: null, battle: null, scouts: {}, query: null, history: profiles.root, summonerSearch: null, account: { puuid: 'root', name: names.root, region: 'TEST', platform: 'TEST', level: 100 }, automationEnabled: false })
      const listeners = new Set(), calls = [], pending = []
      const fixture = { source, profiles, calls, pending, fail: '', delay: '', notify: () => { source.revision++; for (const listener of listeners) listener() } }
      window.__playerFixture = fixture
      window.__luxSubscribe = listener => { listeners.add(listener); return () => listeners.delete(listener) }
      const backend = {
        GetSnapshot: async () => structuredClone(source), GetRevision: async () => source.revision,
        GetDesktopStatus: async () => ({ trayAvailable: true }),
        GetReplay: async (puuid, id) => ({ gameId: id, state: 'unavailable', label: '验证回放不可用', reason: '隔离玩家验证', progress: null, canDownload: false, canWatch: false }),
        GetParticipantProfile: async (key, owner, id, target) => {
          if (fixture.fail === target) throw Error('隔离验证：读取失败')
          if (fixture.delay === target) await new Promise(resolve => pending.push(resolve))
          return structuredClone(profiles[target])
        },
        CloseParticipantExplorer: async () => {},
      }
      window.__luxBackend = new Proxy(backend, { get: (target, method) => async (...args) => { calls.push([method, ...args]); if (!(method in target)) throw Error(`Unexpected fixture method: ${String(method)}`); return target[method](...args) } })
    }, source)
    await page.goto(`${base}/?page=history`)
    await page.locator('.combat-row').nth(9).waitFor()
    assert.equal(await page.locator('.combat-row .player-id-unavailable').count(), 1)
    const original = page.locator('.combat-row').getByRole('button', { name: '查看 同名#100 的战绩', exact: true })
    const originalMatch = await page.locator('.history-workspace .match-row[aria-pressed=true]').getAttribute('data-match-id')
    const originalSummary = await page.locator('.summary-combat').innerText()
    assert.match(await page.locator('.detail-identity h2').innerText(), /来源玩家#TEST/)
    assert.equal(await page.locator('.record-copy strong').first().innerText(), '来源玩家#TEST')
    const encounters = page.locator('.combat-row').filter({ has: page.getByRole('button', { name: '查看 同名#100 的战绩', exact: true }) }).getByRole('button', { name: '与你的近期交手', exact: true })
    const encounterPanel = page.locator('.evidence-panel').getByRole('button', { name: '查看 900001 的对局详情', exact: true })
    const beforeHover = await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => method !== 'GetRevision').length)
    await encounters.hover()
    await encounterPanel.waitFor()
    await encounterPanel.hover()
    await page.waitForTimeout(220)
    assert.equal(await encounterPanel.isVisible(), true, 'Evidence remains actionable across the hover gap')
    assert.equal(await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => method !== 'GetRevision').length), beforeHover)
    await page.screenshot({ path: path.join(output, 'fixture-encounter.png') })
    await encounterPanel.click()
    const recorded = page.getByRole('dialog', { name: '历史对局详情', exact: true })
    await recorded.locator('.combat-row').nth(9).waitFor()
    assert.equal(await recorded.locator('.match-detail').getAttribute('data-match-id'), '900001')
    assert.match(await recorded.locator('.detail-identity h2').innerText(), /来源玩家#TEST/)
    await recorded.getByRole('button', { name: '关闭历史对局详情', exact: true }).click()
    await recorded.waitFor({ state: 'detached' })
    assert.equal(await page.locator('.history-workspace .match-row[aria-pressed=true]').getAttribute('data-match-id'), originalMatch)
    await encounters.focus()
    await encounters.press('ArrowDown')
    await page.waitForFunction(() => document.activeElement?.classList.contains('evidence-row'))
    await page.keyboard.press('Escape')
    await encounterPanel.waitFor({ state: 'hidden' })
    const partyBadge = page.locator('.history-workspace .premade-analysis .evidence-badge').first()
    assert.match(await partyBadge.innerText(), /疑似小队 A.*5 人.*79 场/)
    await partyBadge.hover()
    await page.locator('.evidence-panel:popover-open .evidence-row').first().waitFor()
    await page.screenshot({ path: path.join(output, 'fixture-premade.png') })
    await page.locator('.evidence-panel:popover-open .evidence-row').first().click()
    await recorded.locator('.combat-row').nth(9).waitFor()
    await page.keyboard.press('Escape')
    await recorded.waitFor({ state: 'detached' })
    for (const size of [{ width: 1060, height: 700 }, { width: 1280, height: 860 }, { width: 1680, height: 1000 }]) {
      await page.setViewportSize(size)
      for (const theme of ['light', 'dark']) {
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await page.mouse.move(0, 0)
        await encounters.hover()
        await encounterPanel.waitFor()
        const bounds = await page.locator('.evidence-panel:popover-open').boundingBox()
        assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= size.width && bounds.y + bounds.height <= size.height)
        assert.equal(await page.locator('.evidence-panel:popover-open').evaluate(element => element.scrollWidth > element.clientWidth), false)
        await encounterPanel.press('Escape')
      }
    }
    await page.setViewportSize({ width: 1280, height: 860 })
    await page.locator('.history-content').evaluate(element => { element.scrollTop = 0 })
    await page.evaluate(() => {
      const fixture = window.__playerFixture
      const match = fixture.profiles.root.matches[0]
      fixture.source.battle = { key: `review:${match.id}`, source: '隔离关系分析复盘', mode: 'ranked', queueId: 420, players: match.players.map(player => ({ puuid: player.puuid, name: player.name, champion: player.championId, ally: player.team === 100, hidden: !player.puuid, locked: true, position: '' })) }
      fixture.source.scouts = Object.fromEntries(Object.values(fixture.profiles).map(profile => [profile.puuid, { profile, loading: false, error: '' }]))
      fixture.notify()
    })
    await page.getByRole('button', { name: '对战助手', exact: true }).click()
    await page.locator('.player-card').nth(9).waitFor()
    assert.equal(await page.locator('.board-team-heading .premade-analysis .evidence-badge').count(), 2)
    assert.equal(await page.locator('.player-card .identity-topline .evidence-badge').count(), 8)
    assert.equal(await page.locator('.player-card').evaluateAll(cards => cards.some(card => card.scrollWidth > card.clientWidth + 1)), false)
    await page.screenshot({ path: path.join(output, 'fixture-relationships-board.png') })
    const signalTrigger = page.locator('.player-card').first().getByRole('button', { name: '前期被抓记录', exact: true })
    assert.equal(await signalTrigger.innerText(), '好抓')
    const signalCalls = await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => method !== 'GetRevision').length)
    await signalTrigger.hover()
    await page.locator('.evidence-panel:popover-open .evidence-row').first().waitFor()
    assert.match(await page.locator('.evidence-panel:popover-open').innerText(), /场均 2.00 次/)
    assert.equal(await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => method !== 'GetRevision').length), signalCalls)
    await page.locator('.evidence-panel:popover-open .evidence-row').first().press('Escape')
    const signalWorkCalls = await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => !['GetRevision','GetSnapshot'].includes(method)).length)
    const scoreTrigger = page.locator('.player-card').first().getByRole('button', { name: '近期表现评分', exact: true })
    await scoreTrigger.hover()
    await page.locator('.evidence-panel:popover-open').waitFor()
    assert.match(await page.locator('.evidence-panel:popover-open').innerText(), /KDA 1.00\/1.*视野 1.00\/2/)
    await scoreTrigger.press('Escape')
    const flashTrigger = page.locator('.player-card').first().getByRole('button', { name: '闪现位置分布', exact: true })
    await flashTrigger.hover()
    await page.locator('.evidence-panel:popover-open').waitFor()
    assert.match(await page.locator('.evidence-panel:popover-open').innerText(), /闪现 D：40 场（50.0%）/)
    await flashTrigger.press('Escape')
    // Enrichment changes counters without a new capture time. The retained store
    // must replace that profile while leaving unrelated player objects untouched.
    for (const [count, label] of [[0, '抓人无样本'], [4, '抓人样本少'], [5, '好抓']]) {
      await page.evaluate(count => { const f=window.__playerFixture; f.profiles.root.summary.signals.gankGames=count; f.notify() }, count)
      await page.waitForFunction(label => document.querySelector('.player-card .player-signals [aria-label="前期被抓记录"]')?.textContent === label, label)
    }
    await page.evaluate(() => { const f=window.__playerFixture; f.source.battle.players[0].position='JUNGLE'; f.notify() })
    await signalTrigger.waitFor({state:'detached'})
    await page.evaluate(() => { const f=window.__playerFixture; f.source.battle.players[0].position=''; f.notify() })
    await signalTrigger.waitFor()
    assert.equal(await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => !['GetRevision','GetSnapshot'].includes(method)).length), signalWorkCalls)
    await page.evaluate(() => { const f=window.__playerFixture; f.source.battle.players.forEach((p,i)=> {p.group=[0,1,5,6].includes(i)?'123':'';p.groupSource=p.group?'LCU':''}); f.notify() })
    await page.waitForFunction(() => document.querySelectorAll('.board-team-heading .party-chip[data-direct=true]').length === 2)
    assert.equal(await page.locator('.player-card .party-chip[data-direct=true]').count(),4)
    const directBadge=page.locator('.player-card .party-chip[data-direct=true] .evidence-badge').first()
    await directBadge.hover()
    await page.getByText('客户端分组 · LCU',{exact:true}).waitFor()
    assert.equal(await page.locator('.evidence-panel:popover-open .evidence-row').count(),0)
    await page.screenshot({path:path.join(output,'fixture-direct-groups.png')})
    await directBadge.press('Escape')
    await page.getByRole('button', { name: '战绩分析', exact: true }).click()
    await page.locator('.combat-row').nth(9).waitFor()
    await page.getByRole('button', {name:'筛选战绩',exact:true}).click()
    await page.getByRole('dialog',{name:'战绩筛选',exact:true}).getByRole('button',{name:'灵活组排',exact:true}).click()
    await page.getByRole('dialog',{name:'战绩筛选',exact:true}).getByRole('button',{name:'完成',exact:true}).click()
    const scopedGank=page.locator('.history-overview').getByRole('button',{name:'前期被抓记录',exact:true})
    await scopedGank.hover()
    await page.locator('.evidence-panel:popover-open').waitFor()
    assert.equal(await page.locator('.evidence-panel:popover-open .evidence-row').count(),1)
    assert.match(await page.locator('.evidence-panel:popover-open').innerText(),/1 场时间线/)
    await scopedGank.press('Escape')
    await page.getByRole('button',{name:'移除灵活组排筛选',exact:true}).click()
    await page.locator(`.history-workspace .match-row[data-match-id="${originalMatch}"]`).click()
    await original.click()
    const modal = page.locator('dialog.player-explorer')
    const heading = modal.locator('#player-explorer-title')
    await modal.locator('.player-profile-main').waitFor()
    assert.equal(await heading.innerText(), '同名#100')
    assert.match(await modal.locator('.player-rank-card').innerText(), /钻石 IV/)
    assert.equal(await modal.locator('.player-champion-row').count(), 2)
    assert.equal(await modal.locator('.record-extra').count(), await modal.locator('.match-row').count())
    let call = await page.evaluate(() => window.__playerFixture.calls.filter(([name]) => name === 'GetParticipantProfile').at(-1))
    assert.deepEqual(call.slice(2, 5), ['root', Number(originalMatch), 'alpha'])

    // A local filter must constrain records without replacing either the source
    // workspace or the player's aggregate sample with a differently named account.
    await modal.locator('.player-champion-row').first().click()
    const filterLabel = await modal.locator('.filter-chip').innerText()
    await modal.locator('.match-list').evaluate(element => { element.scrollTop = 450 })
    await page.waitForTimeout(80)
    const retainedOffset = await modal.locator('.match-list').evaluate(element => element.scrollTop)
    const selectedID = await modal.locator('.match-row').nth(6).getAttribute('data-match-id')
    await modal.locator('.match-row').nth(6).click()
    await modal.locator('.combat-row').getByRole('button', { name: '查看 同名#200 的战绩', exact: true }).click()
    await modal.locator('.player-profile-main').waitFor()
    assert.equal(await heading.innerText(), '同名#200')
    call = await page.evaluate(() => window.__playerFixture.calls.filter(([name]) => name === 'GetParticipantProfile').at(-1))
    assert.deepEqual(call.slice(2, 5), ['alpha', Number(selectedID), 'beta'])
    assert.deepEqual(call[5], ['alpha'])
    await modal.getByRole('button', { name: '返回上一位玩家', exact: true }).click()
    await modal.locator('.player-match-detail .combat-row').nth(9).waitFor()
    assert.equal(await heading.innerText(), '同名#100')
    await modal.getByRole('button', { name: '返回 同名 的概况', exact: true }).click()
    assert.equal(await modal.locator('.filter-chip').innerText(), filterLabel)
    assert.equal(await modal.locator('.match-list').evaluate(element => element.scrollTop), retainedOffset)

    const filterTrigger = modal.getByRole('button', { name: '筛选战绩', exact: true })
    const filterPanel = modal.getByRole('dialog', { name: '战绩筛选', exact: true })
    await filterTrigger.click()
    await filterPanel.waitFor()
    await filterPanel.getByRole('button', { name: '大乱斗', exact: true }).click()
    await page.keyboard.press('Escape')
    await filterPanel.waitFor({ state: 'hidden' })
    assert.equal(await modal.isVisible(), true, 'First Escape only dismisses filter')
    assert.match(await modal.locator('.player-recent-result').innerText(), /40 场/)
    // Default programmatic focus can scroll a retained container and correctly
    // dismiss its tooltip; this check isolates keyboard disclosure without scrolling.
    await modal.getByRole('button', { name: '玩家胜率与场均统计说明', exact: true }).evaluate(element => element.focus({ preventScroll: true }))
    await modal.getByRole('tooltip').waitFor()
    await page.keyboard.press('Escape')
    await modal.getByRole('tooltip').waitFor({ state: 'hidden' })
    assert.equal(await modal.isVisible(), true)

    const layouts = []
    for (const size of [{ width: 1060, height: 700 }, { width: 1280, height: 860 }, { width: 1680, height: 1000 }]) {
      await page.setViewportSize(size)
      for (const theme of ['light', 'dark']) {
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        const bounds = await modal.boundingBox()
        assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= size.width && bounds.y + bounds.height <= size.height)
        assert.equal(await modal.evaluate(element => element.scrollWidth > element.clientWidth), false)
        assert.equal(await modal.locator('.player-profile-main').evaluate(element => element.scrollWidth > element.clientWidth), false)
        assert.ok(await modal.locator('.match-row').count() < 18, 'History stays virtualized')
        layouts.push({ ...size, theme, bounds })
        await page.screenshot({ path: path.join(output, `fixture-player-${size.width}-${theme}.png`) })
      }
    }
    const beforePointer = await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => method !== 'GetRevision').length)
    for (let i = 0; i < 50; i++) await page.mouse.move(500 + i * 11 % 400, 180 + i * 23 % 390)
    const afterPointer = await page.evaluate(() => window.__playerFixture.calls.filter(([method]) => method !== 'GetRevision').length)
    assert.equal(afterPointer, beforePointer)
    await modal.getByRole('button', { name: '关闭玩家战绩', exact: true }).click()
    await modal.waitFor({ state: 'detached' })
    assert.equal(await page.locator('.history-workspace .match-row[aria-pressed=true]').getAttribute('data-match-id'), originalMatch)
    assert.equal(await page.locator('.summary-combat').innerText(), originalSummary)
    assert.equal(await original.evaluate(element => document.activeElement === element), true)

    await page.evaluate(() => { window.__playerFixture.fail = 'alpha' })
    await original.click()
    await modal.getByRole('alert').waitFor()
    assert.equal(await modal.locator('.player-profile-main').count(), 0)
    await page.evaluate(() => { window.__playerFixture.fail = '' })
    await modal.getByRole('button', { name: '重新读取', exact: true }).click()
    await modal.locator('.player-profile-main').waitFor()
    await modal.locator('.match-row').first().click()
    await page.evaluate(() => { window.__playerFixture.delay = 'beta' })
    await modal.locator('.combat-row').getByRole('button', { name: '查看 同名#200 的战绩', exact: true }).click()
    await modal.locator('[aria-busy=true]').waitFor()
    await page.waitForFunction(() => window.__playerFixture.pending.length === 1)
    await modal.getByRole('button', { name: '返回上一位玩家', exact: true }).click()
    await page.evaluate(() => { window.__playerFixture.pending.shift()(); window.__playerFixture.delay = '' })
    await page.waitForTimeout(100)
    assert.equal(await heading.innerText(), '同名#100', 'Late response cannot replace restored player')
    await modal.locator('.player-match-detail .combat-row').nth(9).waitFor()
    await modal.locator('.combat-row .evidence-badge').first().hover()
    await page.locator('.evidence-panel:popover-open .evidence-row').first().click()
    await recorded.waitFor()
    await page.evaluate(() => { window.__playerFixture.source.account.puuid = 'replacement-account'; window.__playerFixture.notify() })
    await modal.waitFor({ state: 'detached' })
    await recorded.waitFor({ state: 'detached' })
    await page.waitForFunction(() => window.__playerFixture.calls.some(([method]) => method === 'CloseParticipantExplorer'))
    assert.deepEqual(errors, [])
    const report = { source: 'isolated renderer fixture; no real client commands', summonerNamesReplaceChampionLabels: true, encounterHoverAndExactMatch: true, suspectedPartyEvidence: true, battleRelationshipBadges: true, relationshipHoverBackendCalls: 0, liveGroupOnlyUpdate: true, signalEnrichmentPublication: true, gankEvidenceScope: true, lowSampleAndJungleRules: true, scoreAndFlashExplanations: true, sameNameExactIdentity: true, unknownIdentityPlain: true, nestedBackAndFilters: true, scrollAndOriginPreserved: true, keyboardAndModalPopovers: true, errorRetry: true, lateReplyDiscarded: true, accountDismissal: true, virtualRowsBounded: true, pointerBackendCalls: afterPointer - beforePointer, layouts, errors }
    await fs.writeFile(path.join(output, 'player-verification.json'), JSON.stringify(report, null, 2))
    console.log(JSON.stringify(report, null, 2))
  } finally { await browser.close() }
}
main().catch(error => { console.error(error); process.exitCode = 1 })
