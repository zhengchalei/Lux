import type { ApplicationSettings, DesktopStatus, AutomationSettings, Match, Matchup, Snapshot, DraftSkins, DraftPanelSnapshot, DraftPanelStatus, Replay, TimelineReview, AISettings, AISettingsInput, AIReview } from './types'
import { call, on } from 'mygo-runtime'
import type { UpdateStatus } from './types'

// api.ts isolates desktop transport from the Vue presentation and state model.
// MyGo can install its generated App client through installBackend. A browser
// development server may expose the same methods at /api; neither transport
// contains client authentication or sends game commands for hover interactions.
//
// Only explicit actions invoke write methods. Snapshot observation is serialized
// by the store and events merely request another observation, keeping overlapping
// renderer events from producing concurrent state reads or stale account writes.
// Transport errors remain visible to callers instead of manufacturing examples
// or turning missing player data into an empty successful result.

/** Backend defines the typed application service exposed by MyGo's generated bridge. */
export interface Backend {
  GetUpdateStatus(): Promise<UpdateStatus>
  SetAutoUpdate(enabled: boolean): Promise<void>
  CheckForUpdates(): Promise<void>
  RestartToUpdate(): Promise<void>
  SaveApplicationSettings(settings: ApplicationSettings): Promise<void>
  GetDesktopStatus(): Promise<DesktopStatus>
  GetDraftSnapshot(): Promise<DraftPanelSnapshot>
  GetDraftSkins(key: string, champion: number): Promise<DraftSkins>
  SelectDraftChampion(key: string, current: number, target: number): Promise<void>
  SetDraftSkin(key: string, champion: number, skin: number): Promise<void>
  GetSkinPreview(key: string, champion: number, skin: number): Promise<string>
  GetDraftPanelStatus(): Promise<DraftPanelStatus>
  SetDraftPanel(enabled: boolean, pinned: boolean): Promise<void>
  SetDraftPanelCollapsed(collapsed: boolean): Promise<void>
  GetRevision(): Promise<number>
  GetSnapshot(): Promise<Snapshot>
  Lookup(name: string, queueID: number): Promise<void>
  SelectSummoner(searchID: number, puuid: string, queueID: number): Promise<void>
  GetSummonerIcon(icon: number): Promise<string>
  GetTimeline(puuid: string, id: number, opponent: number): Promise<TimelineReview>
  GetAISettings(): Promise<AISettings>
  SaveAISettings(settings: AISettingsInput): Promise<AISettings>
  AnalyzeTimeline(puuid: string, id: number, opponent: number, requestID: string): Promise<AIReview>
  CancelAIAnalysis(requestID: string): Promise<void>
  QuerySelf(queueID: number): Promise<void>
  ReviewLastGame(): Promise<void>
  RefreshBattle(): Promise<void>
  RetryScout(puuid: string): Promise<void>
  SetAnalysis(count: number, sameMode: boolean): Promise<void>
  SaveAutomation(settings: AutomationSettings, enabled: boolean): Promise<void>
  StopAutomation(): Promise<void>
  GetMatchDetail(puuid: string, id: number): Promise<Match>
  GetReplay(puuid: string, id: number): Promise<Replay>
  ReplayAction(puuid: string, id: number, action: 'download' | 'watch'): Promise<void>
  GetMatchups(enemy: number, position: string): Promise<Matchup[]>
  InspectChampion(id: number): Promise<void>
}

declare global {
  interface Window { __luxBackend?: Backend; __luxSubscribe?: (listener: () => void) => (() => void) }
}

/** installBackend supplies the generated bridge before the application is mounted. */
export function installBackend(backend: Backend): void { window.__luxBackend = backend }

/** invoke routes a typed method to desktop IPC or the development server.
 * A missing desktop bridge does not introduce sample data. The optional HTTP
 * development endpoint must explicitly implement the same method contract.
 */
async function invoke<K extends keyof Backend>(method: K, ...args: Parameters<Backend[K]>): Promise<Awaited<ReturnType<Backend[K]>>> {
  const backend = window.__luxBackend
  if (backend) {
    const operation = backend[method] as (...input: Parameters<Backend[K]>) => ReturnType<Backend[K]>
    return await operation(...args) as Awaited<ReturnType<Backend[K]>>
  }
  if ('mygo' in window) return await call<Awaited<ReturnType<Backend[K]>>>(`App.${method}`, ...args)
  const response = await fetch(`/api/${method}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(args) })
  if (!response.ok) throw new Error(`应用服务暂不可用（${response.status}）`)
  if (response.status === 204) return undefined as Awaited<ReturnType<Backend[K]>>
  const reply = await response.json() as { result?: unknown; error?: string }
  if (reply.error) throw new Error(reply.error)
  return (Object.hasOwn(reply, 'result') ? reply.result : reply) as Awaited<ReturnType<Backend[K]>>
}

// MyGo event payloads carry only change demand; one animation turn coalesces bursts before the store serializes its asynchronous snapshot reads.
if ('mygo' in window) window.__luxSubscribe = listener => {
  let scheduled = 0
  const unsubscribe = on('lux:changed', () => {
    if (!scheduled) scheduled = requestAnimationFrame(() => { scheduled = 0; listener() })
  })
  return () => { unsubscribe(); cancelAnimationFrame(scheduled) }
}

/** api exposes stable names independently of the generated MyGo client's spelling. */
export const api: Backend = {
  GetUpdateStatus: () => invoke('GetUpdateStatus'),
  SetAutoUpdate: enabled => invoke('SetAutoUpdate', enabled),
  CheckForUpdates: () => invoke('CheckForUpdates'),
  RestartToUpdate: () => invoke('RestartToUpdate'),
  SaveApplicationSettings: settings => invoke('SaveApplicationSettings', settings),
  GetDesktopStatus: () => invoke('GetDesktopStatus'),
  GetDraftSnapshot: () => invoke('GetDraftSnapshot'),
  GetDraftSkins: (key, champion) => invoke('GetDraftSkins', key, champion),
  SelectDraftChampion: (key, current, target) => invoke('SelectDraftChampion', key, current, target),
  SetDraftSkin: (key, champion, skin) => invoke('SetDraftSkin', key, champion, skin),
  GetSkinPreview: (key, champion, skin) => invoke('GetSkinPreview', key, champion, skin),
  GetDraftPanelStatus: () => invoke('GetDraftPanelStatus'),
  SetDraftPanel: (enabled, pinned) => invoke('SetDraftPanel', enabled, pinned),
  SetDraftPanelCollapsed: collapsed => invoke('SetDraftPanelCollapsed', collapsed),
  GetRevision: () => invoke('GetRevision'),
  GetSnapshot: () => invoke('GetSnapshot'),
  Lookup: (name, queue) => invoke('Lookup', name, queue),
  SelectSummoner: (id, puuid, queue) => invoke('SelectSummoner', id, puuid, queue),
  GetSummonerIcon: icon => invoke('GetSummonerIcon', icon),
  GetTimeline: (puuid, id, opponent) => invoke('GetTimeline', puuid, id, opponent),
  GetAISettings: () => invoke('GetAISettings'),
  SaveAISettings: settings => invoke('SaveAISettings', settings),
  AnalyzeTimeline: (puuid, id, opponent, requestID) => invoke('AnalyzeTimeline', puuid, id, opponent, requestID),
  CancelAIAnalysis: requestID => invoke('CancelAIAnalysis', requestID),
  QuerySelf: queue => invoke('QuerySelf', queue),
  ReviewLastGame: () => invoke('ReviewLastGame'),
  RefreshBattle: () => invoke('RefreshBattle'),
  RetryScout: puuid => invoke('RetryScout', puuid),
  SetAnalysis: (count, sameMode) => invoke('SetAnalysis', count, sameMode),
  SaveAutomation: (settings, enabled) => invoke('SaveAutomation', settings, enabled),
  StopAutomation: () => invoke('StopAutomation'),
  GetMatchDetail: (puuid, id) => invoke('GetMatchDetail', puuid, id),
  GetReplay: (puuid, id) => invoke('GetReplay', puuid, id),
  ReplayAction: (puuid, id, action) => invoke('ReplayAction', puuid, id, action),
  GetMatchups: (enemy, position) => invoke('GetMatchups', enemy, position),
  InspectChampion: id => invoke('InspectChampion', id),
}
