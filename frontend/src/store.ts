import { computed, shallowReactive } from 'vue'
import { api } from './api'
import type { AutomationSettings, Battle, Database, Member, Profile, Room, Scout, Snapshot } from './types'

// store.ts applies coherent backend observations without invalidating every Vue view.
// Snapshot revisions gate updates. Profiles retain object identity when their
// capture and coverage stamps are unchanged, allowing unaffected player cards,
// charts, and history lists to skip rendering while another player loads.
//
// One asynchronous reader drains event demand; callbacks arriving during a read
// request one subsequent observation. A bounded recovery timer checks only the
// revision before recovering a lost event or a development endpoint update. Errors
// keep previously observed records available and label their connectivity state.
// Account transitions instead discard old player objects before applying data.

/** defaultAutomation initializes local rule controls without enabling their runtime. */
export const defaultAutomation: AutomationSettings = {
  autoPick: true, autoBan: false, preselect: false, lockPick: true,
  protectAllies: true, preserveManual: true, delaySeconds: 2, picks: [], bans: [], position: '',
}

/** emptyDatabase holds public metadata until the Go service publishes its catalog. */
const emptyDatabase: Database = { champions: [], patch: '', updated: '', sourceStatus: '' }
/** state contains credential-free data and transport status read by the workspaces. */
export const state = shallowReactive<Snapshot & { bridgeError: string; ready: boolean }>({
  revision: -1, connected: false, status: '正在连接应用服务…', phase: '', patch: '', account: null,
  room: null, history: null, battle: null, scouts: {}, query: null, queryLoading: false, queryError: '',
  summonerSearch: null,
  application: { closeToTray: true, autoAccept: false, acceptDelaySeconds: 2 }, acceptStatus: '自动接受已关闭',
  analysis: { matchCount: 50, sameMode: true }, automation: defaultAutomation,
  automationEnabled: false, automationStatus: '自动选禁未开启', database: emptyDatabase,
  bridgeError: '', ready: false,
})
/** championMap rebuilds only when public database identity actually changes. */
export const championMap = computed(() => new Map(state.database.champions.map(hero => [hero.id, hero])))

/** profileStamp identifies one published player observation without traversing match arrays. */
function profileStamp(profile: Profile): string {
  const coverage = profile.coverage
  return `${profile.puuid}|${profile.name}|${profile.rank}|${profile.captured}|${profile.requested}|${coverage?.fetched}|${coverage?.complete}|${coverage?.source}|${coverage?.note}|${profile.historyError}`
}
/** retainProfile reuses unchanged player objects while allowing new coverage to replace them. */
function retainProfile(previous: Profile | null, incoming: Profile | null): Profile | null {
  return previous && incoming && profileStamp(previous) === profileStamp(incoming) ? previous : incoming
}

/** sameMembers compares at most five public slots without traversing historical data. */
function sameMembers(previous: Member[] | null, incoming: Member[] | null): boolean {
  return (previous?.length ?? 0) === (incoming?.length ?? 0) && (previous ?? []).every((member, index) => {
    const next = incoming?.[index]
    return next && member.cell === next.cell && member.champion === next.champion && member.position === next.position && member.locked === next.locked && member.local === next.local
  })
}
/** retainRoom preserves unchanged selection identity through unrelated backend updates.
 * Personal choices and the shared bench are compared independently from legal
 * availability, so a card or permission update replaces the retained room.
 */
function retainRoom(previous: Room | null, incoming: Room | null): Room | null {
  return previous && incoming && previous.sessionKey === incoming.sessionKey && previous.canSwap === incoming.canSwap && previous.canChoose === incoming.canChoose && sameIDs(previous.choices, incoming.choices) && sameIDs(previous.bench, incoming.bench) && previous.skinId === incoming.skinId && previous.mode === incoming.mode && previous.queueId === incoming.queueId && previous.current === incoming.current && sameMembers(previous.allies, incoming.allies) && sameMembers(previous.enemies, incoming.enemies) && sameIDs(previous.available, incoming.available) && sameIDs(previous.banned, incoming.banned) ? previous : incoming
}
/** sameIDs compares ordered protocol lists without serializing broader room state. */
function sameIDs(previous: number[] | undefined, incoming: number[] | undefined): boolean {
  return (previous?.length ?? 0) === (incoming?.length ?? 0) && (previous ?? []).every((id, index) => id === incoming?.[index])
}
/** retainBattle compares the small current roster independently from loaded player histories. */
function retainBattle(previous: Battle | null, incoming: Battle | null): Battle | null {
  return previous && incoming && previous.key === incoming.key && previous.source === incoming.source && previous.mode === incoming.mode && previous.queueId === incoming.queueId && previous.players.length === incoming.players.length && previous.players.every((player, index) => {
    const next = incoming.players[index]
    return next && player.puuid === next.puuid && player.name === next.name && player.champion === next.champion && player.position === next.position && player.hidden === next.hidden && player.locked === next.locked && player.ally === next.ally
  }) ? previous : incoming
}
/** applySnapshot updates independent workspace identities from one newer backend revision.
 * Account changes invalidate every retained player identity before reconciliation.
 * The service prepares aggregates, so this operation compares publication stamps
 * rather than recalculating match statistics or serializing the complete snapshot.
 */
function applySnapshot(incoming: Snapshot): void {
  state.bridgeError = ''
  state.ready = true
  if (incoming.revision === state.revision) return
  // Clear identity-bearing observations before reconciling a changed account so old records cannot survive a new account's identical sample counts.
  const accountChanged = incoming.account?.puuid !== state.account?.puuid || incoming.account?.region !== state.account?.region || incoming.account?.platform !== state.account?.platform
  const oldScouts = accountChanged ? {} : state.scouts
  const nextScouts: Record<string, Scout> = {}
  for (const [id, scout] of Object.entries(incoming.scouts ?? {})) {
    const previous = oldScouts[id]
    const profile = retainProfile(previous?.profile ?? null, scout.profile)
    nextScouts[id] = previous && previous.profile === profile && previous.loading === scout.loading && previous.error === scout.error ? previous : { ...scout, profile }
  }
  const history = retainProfile(accountChanged ? null : state.history, incoming.history)
  const query = retainProfile(accountChanged ? null : state.query, incoming.query)
  const scoutKeys = Object.keys(nextScouts)
  const scouts = !accountChanged && scoutKeys.length === Object.keys(oldScouts).length && scoutKeys.every(id => nextScouts[id] === oldScouts[id]) ? oldScouts : nextScouts
  const room = retainRoom(accountChanged ? null : state.room, incoming.room)
  const battle = retainBattle(accountChanged ? null : state.battle, incoming.battle)
  const previousDatabase = state.database
  const database = incoming.database ?? emptyDatabase
  const databaseUnchanged = previousDatabase.patch === database.patch && previousDatabase.updated === database.updated && previousDatabase.sourceStatus === database.sourceStatus && previousDatabase.champions.length === (database.champions?.length ?? 0)
  // Only small control structures are compared fieldwise; list and profile observations retain identities by their service publication stamps.
  const analysis = state.analysis.matchCount === incoming.analysis.matchCount && state.analysis.sameMode === incoming.analysis.sameMode ? state.analysis : incoming.analysis
  Object.assign(state, incoming, { scouts, history, query, analysis, room, battle, database: databaseUnchanged ? previousDatabase : database })
}

/** connectStore starts one serialized observer and returns complete subscription cleanup.
 * Desktop events request fresh snapshots, while recovery reads the revision first
 * to avoid repeatedly transferring unchanged histories. Snapshot reads stay serial.
 */
export function connectStore(): () => void {
  let stopped = false
  let pending = false
  let running = false
  let checkingRevision = false
  let observation = 0
  /** refresh drains accumulated observation demand without concurrent service calls. */
  async function refresh(): Promise<void> {
    if (stopped) return
    pending = true
    if (running) return
    running = true
    try {
      while (pending && !stopped) {
        pending = false
        observation++
        try {
          const incoming = await api.GetSnapshot()
          if (!stopped) applySnapshot(incoming)
        } catch (error) {
          if (!stopped) state.bridgeError = error instanceof Error ? error.message : String(error)
        }
      }
    } finally { running = false }
  }
  /** recover probes cheaply and ignores results superseded by a full observation. */
  async function recover(): Promise<void> {
    if (stopped || running || checkingRevision) return
    checkingRevision = true
    const observed = observation
    try {
      const revision = await api.GetRevision()
      // An event may have started and completed a newer read while this probe was pending.
      if (stopped || running || observation !== observed) return
      if (revision !== state.revision) await refresh()
      else state.bridgeError = ''
    } catch (error) {
      if (!stopped && !running && observation === observed) state.bridgeError = error instanceof Error ? error.message : String(error)
    } finally { checkingRevision = false }
  }
  const unsubscribe = window.__luxSubscribe?.(() => { void refresh() })
  const interval = window.setInterval(() => { void recover() }, unsubscribe ? 3000 : 800)
  void refresh()
  return () => { stopped = true; pending = false; window.clearInterval(interval); unsubscribe?.() }
}

/** action executes an explicit service command and exposes errors to its caller. */
export async function action(operation: () => Promise<unknown>): Promise<string> {
  try { await operation(); return '' } catch (error) { return error instanceof Error ? error.message : String(error) }
}
