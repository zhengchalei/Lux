import { computed } from 'vue'
import { state } from './store'
import { collectEvidence, encounterIndex } from './relationships'
import type { Profile } from './types'

// relationshipStore.ts prepares shared relationship indexes from retained service
// observations. Profiles keep stable identities in the main store, so unrelated
// status updates and pointer movement do not rebuild historical rosters. Account
// replacement naturally discards previous encounter and party inputs before views
// can display another account's relationship evidence.

// observedProfiles and observedEvidence reuse immutable profiles already loaded
// for the workspace. No additional history read is admitted for a badge hover.
export const observedProfiles = computed(() => [state.history, ...Object.values(state.scouts).map(scout => scout.profile), state.query].filter((profile): profile is Profile => !!profile))
export const observedEvidence = computed(() => collectEvidence(observedProfiles.value, state.account?.platform))
export const personalEncounters = computed(() => encounterIndex(state.history, state.account?.puuid || ''))
