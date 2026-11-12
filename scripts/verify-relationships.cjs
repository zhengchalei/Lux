const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const ts = require('../frontend/node_modules/typescript')

// verify-relationships.cjs exercises the pure relationship calculations against
// recorded-shaped fixtures. It protects identity-based encounters, unique games,
// historical time boundaries, same-team evidence, and party subset inference.
// No running client, browser, application preferences, or network is required.

// Type-only imports disappear during transpilation, keeping the fixture focused
// on production calculations without mounting Vue or introducing a test runtime.
const source = fs.readFileSync(path.resolve(__dirname, '../frontend/src/relationships.ts'), 'utf8')
const loaded = { exports: {} }
new Function('exports', 'require', 'module', ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(loaded.exports, require, loaded)
const { collectEvidence, recentEvidence, encounterIndex, inferPremades, displayParties, relationshipWindow } = loaded.exports
const now = Date.UTC(2026, 10, 12), day = 86400000
const roster = ['local', 'alpha', 'beta', 'gamma', 'delta'].map((puuid, index) => ({ puuid, name: index < 3 ? `同名#${index}` : puuid, team: 100 }))

/** game creates one recorded perspective with public identities and explicit teams. */
function game(id, members, overrides = {}) {
  return { id, created: now - id * 1000, platformId: 'TEST', duration: 1500, queueId: 420, queue: '单双排', gameType: 'MATCHED_GAME', remake: false, team: 100, win: true, players: members.map(([puuid, team]) => ({ puuid, name: puuid, team, self: puuid === 'local' })), ...overrides }
}
/** rows attaches source ownership before inference without replacing recorded team membership. */
function rows(games) { return games.map(match => ({ owner: 'local', match, label: '', win: match.win })) }

// Repeated histories of the same game cannot manufacture a three-match party.
const repeated = game(1, [['alpha', 100], ['beta', 100]])
assert.equal(collectEvidence([{ puuid: 'alpha', matches: [repeated] }, { puuid: 'beta', matches: [repeated] }]).length, 1)
assert.equal(inferPremades(rows([repeated, repeated, repeated]), roster, 0, now).length, 0)
const separate = { ...repeated, platformId: 'OTHER' }
assert.equal(collectEvidence([{ puuid: 'alpha', matches: [repeated, separate] }]).length, 2)

// A/B and B/C each sharing three games must remain two pairs, not an invented trio.
const pairs = [1, 2, 3].map(id => game(id, [['alpha', 100], ['beta', 100]]))
  .concat([4, 5, 6].map(id => game(id, [['beta', 200], ['gamma', 200]])))
let groups = inferPremades(rows(pairs), roster, 0, now)
assert.deepEqual(groups.map(group => group.members.map(player => player.puuid).sort()), [['alpha', 'beta'], ['beta', 'gamma']])
assert.equal(groups[1].rows[0].win, false, 'Party result follows its historical side')
const joint = [7, 8, 9].map(id => game(id, [['alpha', 100], ['beta', 100], ['gamma', 100]]))
groups = inferPremades(rows([...pairs, ...joint]), roster, 0, now)
assert.deepEqual(groups.map(group => group.members.map(player => player.puuid).sort()), [['alpha', 'beta', 'gamma']])
assert.equal(groups[0].rows.length, 3)

// Current enemies never become a party, even after a long shared teammate history.
const splitRoster = roster.map(player => ({ ...player, team: player.puuid === 'beta' ? 200 : 100 }))
assert.equal(inferPremades(rows(pairs.slice(0, 3)), splitRoster, 0, now).length, 0)
assert.equal(inferPremades(rows([1, 2, 3].map(id => game(id, [['alpha', 100], ['beta', 200]]))), roster, 0, now).length, 0)
assert.equal(inferPremades(rows(pairs.slice(0, 2)), roster, 0, now).length, 0)

// Selected, future, expired, remake, and custom records cannot satisfy the threshold.
for (const overrides of [{ created: now + day }, { created: now - relationshipWindow - 1 }, { remake: true }, { queueId: 3200 }, { gameType: 'CUSTOM_GAME' }, { duration: 120 }]) {
  assert.equal(inferPremades(rows([1, 2, 3].map(id => game(id, [['alpha', 100], ['beta', 100]], overrides))), roster, 0, now).length, 0)
}
assert.equal(inferPremades(rows(pairs.slice(0, 3)), roster, 1, now).length, 0)

// Encounters use exact PUUIDs, local results, and actual teammate/opponent sides.
const local = game(10, [['local', 100], ['alpha', 100], ['beta', 200], ['', 100]])
const enemyWin = game(11, [['local', 200], ['alpha', 100]], { team: 200, win: false })
const index = encounterIndex({ puuid: 'local', matches: [local, enemyWin, local] }, 'local')
assert.equal(index.has('local'), false)
assert.equal(index.has(''), false)
assert.deepEqual(index.get('alpha').map(row => row.label), ['曾是队友', '曾是对手'])
assert.equal(index.get('alpha')[1].win, false)
assert.equal(index.get('beta').length, 1)
assert.equal(recentEvidence(index.get('alpha'), 10, now).length, 1)
assert.equal(encounterIndex({ puuid: 'previous-account', matches: [local] }, 'local').size, 0)
assert.equal(encounterIndex(null, 'local').size, 0)
// Direct session groups take precedence without joining equal IDs across teams.
const current = roster.map((player,index) => ({ ...player, group: index < 2 ? '12' : undefined, groupSource: index < 2 ? 'LCU' : undefined }))
const display = displayParties(current, [{ members: roster.slice(0,3), rows: rows(joint) }])
assert.equal(display.length,1)
assert.equal(display[0].direct,true)
assert.equal(display[0].members.length,2)
assert.equal(display[0].id,'A')
const both = displayParties([...current.slice(0,2), ...current.slice(0,2).map(p => ({ ...p, puuid: p.puuid+'-enemy', team: 200 }))],[])
assert.equal(both.length,2)
assert.deepEqual(both.map(p => p.id),['A','B'])
assert.equal(displayParties(roster.map(p => ({...p, group:'0',groupSource:'LCU'})),[]).length,0)
console.log(JSON.stringify({ deduplicatedGames: true, exactIdentityEncounters: true, temporalAndCompetitiveBoundaries: true, opposingCurrentTeamsExcluded: true, jointEvidencePreventsChainMerging: true, thresholdAndMaximalGroups: true, historicalTeamResults: true, accountIsolation: true, directGroupsSeparateTeams: true, directGroupsOverrideInference: true }))
