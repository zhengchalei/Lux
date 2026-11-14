// verify-mode-scopes.cjs checks the shared display scope against distinct rule sets.
// These fixtures intentionally disagree so an accidental all-mode or cross-mode
// fallback cannot pass by producing plausible values. They execute the actual
// TypeScript helper without modifying the application or contacting the client.
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const ts = require('../frontend/node_modules/typescript')
const source = fs.readFileSync(path.resolve(__dirname, '../frontend/src/format.ts'), 'utf8')
const loaded = { exports: {} }
new Function('exports', 'require', 'module', ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText)(loaded.exports, require, loaded)
const { scopedSummary, modeFor, modeNames } = loaded.exports
const all = { games: 100, winRate: 70, recent: [true], heroes: [{ championId: 99 }] }
const aram = { games: 20, winRate: 40 }
const mayhem = { games: 30, winRate: 80 }
const profile = { summary: all, summaries: { all, aram, mayhem } }
assert.equal(scopedSummary(profile, 'aram'), aram)
assert.equal(scopedSummary(profile, 'mayhem'), mayhem)
assert.equal(scopedSummary(profile, 'all'), all)
const partial = { summary: all, summaries: { aram } }
const missing = scopedSummary(partial, 'mayhem')
assert.equal(missing.games, 0)
assert.deepEqual(missing.heroes, [])
assert.deepEqual(missing.recent, [])
assert.equal(missing.contribution.damageShare, null)
assert.notEqual(missing.heroes, scopedSummary(partial, 'mayhem').heroes)
assert.equal(scopedSummary(partial, 'all'), all)
assert.equal(scopedSummary({ summary: all, summaries: { mayhem } }, 'aram').games, 0)
for (const queue of [2400, 3270]) assert.equal(modeFor(queue), 'mayhem')
for (const queue of [450, 3200, 3210, 3220, 3230]) assert.equal(modeFor(queue), 'aram')
for (const queue of [400, 420, 430, 440, 480, 490, 700, 720]) assert.equal(modeFor(queue), 'ranked')
for (const queue of [0, 1700, 99999]) assert.equal(modeFor(queue), 'other')
assert.equal(modeNames.aram, '普通大乱斗')
assert.equal(modeNames.mayhem, '海克斯大乱斗')
console.log('Mode scope regression passed: exact summaries, missing observations, queue mappings, labels')
